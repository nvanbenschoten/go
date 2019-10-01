// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package causalprof implements causal profiles as described by
// https://web.cs.umass.edu/publication/docs/2015/UM-CS-2015-008.pdf
package causalprof

import (
	"fmt"
	"io"
	"math/rand"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"
)

var cpu struct {
	sync.Mutex
	profiling int32
	done      chan bool
}

// Start enables causal profiling. While running, results of causal profiling experiments will
// be written to w. Start returns an error if causal profiling or CPU profiling is already enabled.
func Start(w io.Writer) error {
	cpu.Lock()
	defer cpu.Unlock()
	if cpu.done == nil {
		cpu.done = make(chan bool)
	}

	if cpu.profiling != 0 {
		return fmt.Errorf("causal profiling already in use")
	}

	if pprof.IsCPUProfiling() {
		return fmt.Errorf("cpu profiling already in use")
	}
	atomic.StoreInt32(&cpu.profiling, 1)
	runtime.SetCPUProfileRate(profilingHz)
	go profileWriter(w)
	return nil
}

const profilingHz = 1000
const profilingPeriod = 1e9 * 1 / profilingHz
const delayPerPercent = profilingPeriod / 100
const percentileResolution = 10
const progressPerExperiment = 100
const maxTrialsPerExperiment = 5

// Stop stops causal profiling if enabled.
// Stop interrupts any currently running experiment without printing
// its results.
func Stop() {
	cpu.Lock()
	defer cpu.Unlock()

	if cpu.profiling == 0 {
		return
	}
	atomic.StoreInt32(&cpu.profiling, 0)

	runtime_causalProfileStopProf()
	cpu.done <- true
}

type experiment struct {
	trials    int
	hasNull   bool
	remaining []int
}

func profileWriter(w io.Writer) {
	experiments := make(map[uintptr]*experiment)
	profMultiple := time.Duration(200)
	for {
		pc := runtime_causalProfileStart()
		if pc == 0 {
			<-cpu.done
			break
		}
		expinfo, ok := experiments[pc]
		if !ok {
			expinfo = new(experiment)
			experiments[pc] = expinfo
		}
		exp := selectExperiment(expinfo)
		if exp == -1 {
			runtime_causalProfileInstall(0)
			continue
		}
		delaypersample := uint64(exp) * (percentileResolution * delayPerPercent)

		resetProgress()
		runtime_causalProfileInstall(delaypersample)
		select {
		case <-time.After(profMultiple * (time.Second / profilingHz)):
		case <-cpu.done:
			runtime_causalProfileInstall(0)
			return
		}
		runtime_causalProfileInstall(0)
		diff, cnt := compareprogress()
		if diff == -1 {
			continue
		}
		samples, allsamples := runtime_causalProfileSampleStats()
		_func := runtime.FuncForPC(pc)
		file, line := _func.FileLine(pc)
		fmt.Fprintf(w, "# %s %s:%d\n", _func.Name(), file, line)
		fmt.Fprintf(w, "# speedup %d%%\n", delaypersample/delayPerPercent)
		fmt.Fprintf(w, "# count %d\n", cnt)
		fmt.Fprintf(w, "# %dns/op\n", diff)
		fmt.Fprintf(w, "%#x %d %d %d %d\n", pc, delaypersample/delayPerPercent, diff, samples, allsamples)
		// allow system state to return to normal
		if progressPerExperiment > cnt {
			if progressPerExperiment > 2*cnt {
				profMultiple *= 2
			}
		} else {
			if progressPerExperiment < cnt/2 {
				profMultiple /= 2
			}
		}
		time.Sleep(profMultiple / 5 * (time.Second / profilingHz))
	}
}

func selectExperiment(expinfo *experiment) int {
	if expinfo.hasNull && len(expinfo.remaining) == 0 {
		if expinfo.trials == maxTrialsPerExperiment {
			return -1
		}
		expinfo.trials++
		expinfo.remaining = rand.Perm(100 / percentileResolution)
	}
	if !expinfo.hasNull && (len(expinfo.remaining) == 0 || rand.Intn(2) == 1) {
		expinfo.hasNull = true
		return 0
	}
	exp := expinfo.remaining[0] + 1
	expinfo.remaining = expinfo.remaining[1:]
	return exp
}

func runtime_causalProfileStart() uintptr
func runtime_causalProfileInstall(delay uint64)
func runtime_causalProfileGetDelay() uint64
func runtime_causalProfileStopProf()
func runtime_causalProfileSampleStats() (uint64, uint64)

var progress int
var progresstime time.Duration
var experimentNum uint64
var progressmu sync.Mutex

func resetProgress() {
	progressmu.Lock()
	defer progressmu.Unlock()
	progress = 0
	progresstime = 0
	atomic.AddUint64(&experimentNum, 1)
}

type Progress struct {
	startTime     time.Time
	startDelay    uint64
	experimentNum uint64
}

func StartProgress() Progress {
	profiling := atomic.LoadInt32(&cpu.profiling)
	if profiling == 0 {
		return Progress{}
	}
	return Progress{
		startTime:     time.Now(),
		startDelay:    runtime_causalProfileGetDelay(),
		experimentNum: atomic.LoadUint64(&experimentNum),
	}
}

func (p *Progress) Stop() {
	if p.startTime.IsZero() {
		return
	}
	t := time.Since(p.startTime)
	d := runtime_causalProfileGetDelay() - p.startDelay
	t -= time.Duration(d)
	progressmu.Lock()
	defer progressmu.Unlock()
	if experimentNum != p.experimentNum {
		return
	}
	progresstime += t
	progress += 1
}

func compareprogress() (int, int) {
	progressmu.Lock()
	defer progressmu.Unlock()
	if progress == 0 {
		return -1, 0
	}
	cnt := progress
	prog := int(int64(progresstime) / int64(cnt))
	progress = 0
	progresstime = 0
	atomic.AddUint64(&experimentNum, 1)
	return prog, cnt
}
