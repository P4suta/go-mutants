// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// memorySamplingSupported: Linux answers from /proc, so the mechanism exists
// here — whether this particular machine will answer is [memorySamplingAvailable].
const memorySamplingSupported = true

// procRoot is where this package looks for the process table.
//
// It is a variable so the fail-closed path has a seam: "what happens when /proc
// is not readable" is a question about a container or a hardened kernel, and
// neither is something a test can arrange by asking politely.
var procRoot = "/proc"

// procfsReadable caches the one question worth asking of the machine rather
// than of the build: can this process read the process table at all.
//
// It is asked once because the answer cannot change under a running process —
// /proc is either mounted and permitted or it is not — and because it is asked
// before every bounded mutant, which for a run of thousands would otherwise be
// thousands of file reads for one fixed fact.
var procfsReadable = sync.OnceValue(procfsProbe)

// procfsProbe is the read itself: our own stat entry, which is the smallest
// thing that proves both halves — the filesystem is mounted, and this process
// is permitted to read a process's stat file. A ReadDir of the root would prove
// only the first.
func procfsProbe() bool {
	_, err := os.ReadFile(filepath.Join(procRoot, "self", "stat"))
	return err == nil
}

// memorySamplingAvailable reports whether this machine's procfs can be read.
func memorySamplingAvailable() bool { return procfsReadable() }

// kernelBoundsMemory: nothing but the sampler ends a tree for its memory here.
// The rlimits that could are rejected — see [memoryWatchdog] — and the OOM
// killer is the machine's decision rather than this bound's, so a peak above
// the line means the tree went over it and finished anyway. See
// [exceededAtExit].
const kernelBoundsMemory = false

// accountedPeakBelongsToTheChild: it is not. Linux fills wait4's ru_maxrss from
// the mm the child ran on, and a child that os/exec started — with
// clone(CLONE_VM|CLONE_VFORK), on every Linux Go supports — begins its life on
// the parent's mm and carries the parent's high-water mark with it, so the
// number wait4 returns is max(what the parent had ever held when it forked,
// what the child itself reached). Measured on this repository's own machine:
// a `/bin/true` started by a Go process that had touched a gibibyte reported
// 1,052,672 KiB. For a tool whose parent process has just run discovery over a
// module, that is the parent's size wearing the child's name, and a bound
// derived from it would be four times the wrong program. So on Linux the
// sampler is the only witness ([peakOf]), and ru_maxrss is used for nothing
// but the burst-detection it cannot be trusted with either.
const accountedPeakBelongsToTheChild = false

// maxRSSUnit converts ru_maxrss into bytes. Linux reports it in kibibytes,
// which getrusage(2) documents and which every other platform disagrees with,
// so the conversion is a per-platform constant rather than a shared assumption.
const maxRSSUnit = 1 << 10

// statFields are the one-based positions in /proc/<pid>/stat this package
// reads, counted as proc(5) counts them.
const (
	statPgrp = 5
	statRSS  = 24
)

// treeResidentMemory sums the memory of a child and every descendant it has,
// in bytes, walking the kernel's own record of who started whom.
//
// It exists for two reasons, and speed is the first. A sample that has to find
// the tree by scanning every process on the machine costs one file read per
// process — several hundred on a developer's box — and takes milliseconds,
// which is longer than most of what a mutation run starts lives: a test
// binary that fails its first assertion is gone in ten. Walking from the child
// through /proc/<pid>/task/<tid>/children reads the tree's own few files and
// is over in microseconds, which is what lets the first sample be taken at
// once and be a number rather than a miss.
//
// The second is that the walk sees a little more than the group scan. A
// descendant that called setsid or setpgid left the process group — the group
// is what [groupSupervisor] kills, and that hole is documented there — but it
// is still in this tree, still holding memory the run is paying for, and it is
// counted here. Measuring more than can be killed is the honest direction: a
// bound may then trip on a process the kill does not reach, which is reported
// as exceeded exactly as the rest of the tree is.
//
// The children file needs CONFIG_PROC_CHILDREN, which every distribution
// kernel sets; where the root's own file cannot be read the sample falls back
// to [groupResidentMemory], the scan this code did before it could do better.
func treeResidentMemory(pid int) (int64, bool) {
	if pid <= 0 {
		return 0, false
	}
	kids, ok := processChildren(pid)
	if !ok {
		return groupResidentMemory(pid)
	}
	pageSize := int64(os.Getpagesize())
	seen := map[int]bool{pid: true}
	queue := append([]int{pid}, kids...)
	var total int64
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current != pid {
			if seen[current] {
				continue
			}
			seen[current] = true
			if more, ok := processChildren(current); ok {
				queue = append(queue, more...)
			}
		}
		if pss, ok := processProportionalSet(current); ok {
			total += pss
			continue
		}
		if _, pages, ok := processStat(current); ok {
			total += pages * pageSize
		}
	}
	return total, true
}

// processChildren reads the pids a process started, across all of its
// threads, and reports whether the kernel keeps that record for it. A process
// that exited between the walk's steps is not an error: it has no children
// and nothing to add.
func processChildren(pid int) ([]int, bool) {
	taskDir := filepath.Join(procRoot, strconv.Itoa(pid), "task")
	threads, err := os.ReadDir(taskDir)
	if err != nil {
		return nil, false
	}
	var kids []int
	readAny := false
	for _, thread := range threads {
		data, err := os.ReadFile(filepath.Join(taskDir, thread.Name(), "children"))
		if err != nil {
			continue
		}
		readAny = true
		for _, field := range strings.Fields(string(data)) {
			if kid, err := strconv.Atoi(field); err == nil && kid > 0 {
				kids = append(kids, kid)
			}
		}
	}
	return kids, readAny
}

// groupResidentMemory sums the memory of every process in pgid, in bytes,
// counting a page shared between them once rather than once each. It is the
// sample [treeResidentMemory] falls back to on a kernel that cannot name a
// process's children.
//
// The proportional set size is what it reads, and the difference is a
// correctness bug rather than a refinement. A `go test -fuzz` run is a
// coordinator plus one worker process per core, and every one of them maps the
// *same* 100 MiB shared-memory region the fuzzing engine communicates through
// (`internal/fuzz`'s workerSharedMemSize). VmRSS counts a shared page once in
// every process that has it resident, so summing VmRSS over that group reports
// the region once per worker: on an eight-core machine, 800 MiB of memory
// nobody is using, which is most of a gibibyte bound spent on double counting
// and a legitimate fuzz run killed for it. Pss divides a shared page between
// its sharers, so the region is counted once however many processes map it,
// which is the number a budget is about.
//
// The process group is this platform's whole notion of the tree — it is what
// [groupSupervisor] kills — so summing over it measures exactly the set the
// bound is a budget for, and it has exactly the same hole: a descendant that
// called setsid for itself left the group, and is neither counted here nor
// killed there. That is the POSIX limitation the package documentation already
// states about supervision, and a bound cannot be tighter than the supervision
// underneath it.
//
// It is one /proc directory read plus one small file read per process on the
// machine. That sounds worse than it is: the loop stops at the pgrp field for
// everything that is not ours, and it runs ten times a second per bounded
// mutant rather than per process.
//
// The false return is reserved for a kernel that has no /proc at all, which is
// a machine this mechanism does not exist on. A group with no members — every
// process in it exited between the sample and the read — is zero and true,
// because "nothing is running" is an answer.
func groupResidentMemory(pgid int) (int64, bool) {
	if pgid <= 0 {
		return 0, false
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, false
	}

	pageSize := int64(os.Getpagesize())
	var total int64
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		group, pages, ok := processStat(pid)
		// A process that exited between the directory listing and the read is
		// ordinary at this rate, and it is not an error: it is a process that
		// is no longer using anything.
		if !ok || group != pgid {
			continue
		}
		if pss, ok := processProportionalSet(pid); ok {
			total += pss
			continue
		}
		// No smaps_rollup: an older kernel, or one that refuses it. The
		// resident set over-counts a shared region, which makes the bound
		// tighter than it should be — the safe direction for a fallback, and
		// exactly what this code did before it could do better.
		total += pages * pageSize
	}
	return total, true
}

// processProportionalSet reads a process's proportional set size from
// /proc/<pid>/smaps_rollup, in bytes, and reports whether the kernel would say.
//
// smaps_rollup is the pre-summed form of /proc/<pid>/smaps and arrived in Linux
// 4.14; reading smaps itself would be a line per mapping and hundreds of them
// per Go process, ten times a second. A kernel without it, or one that refuses
// it, is answered with false and the caller falls back to the resident set.
//
// It costs one more small file read per process *in the group* rather than per
// process on the machine: the group filter above runs first, so a bounded
// mutant pays it for its own two or three processes and for nothing else.
func processProportionalSet(pid int) (int64, bool) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "smaps_rollup"))
	if err != nil {
		return 0, false
	}
	for line := range strings.Lines(string(data)) {
		rest, found := strings.CutPrefix(line, "Pss:")
		if !found {
			continue
		}
		// "Pss:  1234 kB", and the unit is always kB — proc(5) documents it and
		// the kernel has never written another — so the number is what is
		// parsed and the suffix is what is checked.
		fields := strings.Fields(rest)
		if len(fields) != 2 || fields[1] != "kB" {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || kb < 0 {
			return 0, false
		}
		return kb << 10, true
	}
	return 0, false
}

// processStat reads a process's group id and resident pages from
// /proc/<pid>/stat.
//
// The parsing starts at the last ')' rather than at the second field, because
// the second field is the executable name in parentheses and a program is free
// to be called `foo) 1 2 3 (bar`. Splitting on spaces from the left would then
// read that name as the numbers this function returns, which is a way to be
// told a lie by any test binary that wanted to tell one.
func processStat(pid int) (pgrp int, rssPages int64, ok bool) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, false
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, 0, false
	}
	// Fields from here on are the third onwards, so proc(5)'s field N is at
	// index N-statFirstAfterComm in this slice.
	const statFirstAfterComm = 3
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) <= statRSS-statFirstAfterComm {
		return 0, 0, false
	}
	group, err := strconv.Atoi(fields[statPgrp-statFirstAfterComm])
	if err != nil {
		return 0, 0, false
	}
	pages, err := strconv.ParseInt(fields[statRSS-statFirstAfterComm], 10, 64)
	if err != nil || pages < 0 {
		return 0, 0, false
	}
	return group, pages, true
}
