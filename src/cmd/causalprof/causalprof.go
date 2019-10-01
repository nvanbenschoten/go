// causalprof inteprets results from causal profiling files
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"cmd/internal/objfile"
)

func main() {
	flag.Parse()
	args := flag.Args()

	if len(args) != 2 {
		usage()
	}
	samples, err := readProfFile(args[0])
	if err != nil {
		fatalln(err.Error())
	}
	// get a symbol table to turn addresses into file:line
	obj, err := objfile.Open(args[1])
	if err != nil {
		fatalln(err.Error())
	}
	pcln, err := obj.PCLineTable()
	if err != nil {
		fatalln(err.Error())
	}

	// make an index of experiments concerning the same callsite
	index := make(map[uint64][]*sample)
	for _, s := range samples {
		i := index[s.pc]
		i = append(i, s)
		index[s.pc] = i
	}
	// throw away any callsite with an insufficient sample count
	for pc, i := range index {
		if len(i) < 20 {
			delete(index, pc)
		}
	}
	if len(index) == 0 {
		fmt.Println("not enough data")
	}
	// sort each callsite by slowdown
	for _, i := range index {
		sort.Sort(bySpeedup(i))
	}
	// merge each duplicate (callsite, slowdown)
	for pc, i := range index {
		merged := []*sample{i[0]}
		for _, s := range i[1:] {
			last := merged[len(merged)-1]
			if last.speedup == s.speedup {
				last.merge(s)
			} else {
				merged = append(merged, s)
			}
		}
		index[pc] = merged
	}
	// accumulate a single authoritative null experiment
	var nullexp sample
	for pc, i := range index {
		if i[0].speedup == 0 {
			nullexp.merge(i[0])
			index[pc] = i[1:]
		}
	}
	for pc, i := range index {
		file, line, fn := pcln.PCToLine(pc - 1)
		if fn == nil {
			fmt.Printf("%#x\n", pc)
		} else {
			fmt.Printf("%#x %s:%d\n", pc, file, line)
		}
		fmt.Printf("%3d%%\t%dns\n", nullexp.speedup, nullexp.nsPerOp())
		for _, s := range i {
			if s.speedup == 0 {
				panic("unexpected")
			}
			percent := float64(s.nsPerOp()-nullexp.nsPerOp()) / float64(nullexp.nsPerOp())
			percent *= 100
			percentsamples := (float64(s.speedup)) * (float64(s.delaysamples) / float64(s.allsamples))
			fmt.Printf("%3d%%\t%dns\t%+.3g%%\t%.3g%%\n", s.speedup, s.nsPerOp(), percent, percentsamples)
		}
		fmt.Println()
	}
}

type sample struct {
	pc           uint64
	speedup      int
	merged       int64
	nsPerOpAgg   int64
	delaysamples int64
	allsamples   int64
}

func (s *sample) merge(o *sample) {
	// if s.pc != o.pc || s.speedup != o.speedup {
	// 	panic("different pcs or speedups")
	// }
	s.nsPerOpAgg += o.nsPerOpAgg
	s.merged += o.merged
	s.delaysamples += o.delaysamples
	s.allsamples += o.allsamples
}

func (s *sample) nsPerOp() int64 {
	return s.nsPerOpAgg / s.merged
}

type bySpeedup []*sample

func (b bySpeedup) Len() int           { return len(b) }
func (b bySpeedup) Less(i, j int) bool { return b[i].speedup < b[j].speedup }
func (b bySpeedup) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }

func readProfFile(path string) ([]*sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	var samples []*sample
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		s := scan.Text()
		if len(s) < 1 || s[0] == '#' {
			continue
		}
		fields := strings.Fields(s)
		if len(fields) != 5 {
			return nil, fmt.Errorf("corrupt causalprof file, had %d fields; expected 3", len(fields))
		}
		pc, err := strconv.ParseUint(fields[0], 0, 64)
		if err != nil {
			return nil, err
		}
		speedup, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, err
		}
		nsPerOp, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, err
		}
		delaysamples, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return nil, err
		}
		allsamples, err := strconv.ParseInt(fields[4], 10, 64)
		if err != nil {
			return nil, err
		}
		samples = append(samples, &sample{
			pc:           pc,
			speedup:      speedup,
			nsPerOpAgg:   nsPerOp,
			merged:       1,
			delaysamples: delaysamples,
			allsamples:   allsamples,
		})
	}
	return samples, scan.Err()
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: causalprof file program")
	os.Exit(1)
}

func fatalln(err string) {
	fmt.Fprintln(os.Stderr, "causalprof:", err)
	os.Exit(1)
}
