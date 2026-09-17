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

const memorySamplingSupported = true

var procRoot = "/proc"

var procfsReadable = sync.OnceValue(procfsProbe)

func procfsProbe() bool {
	_, err := os.ReadFile(filepath.Join(procRoot, "self", "stat"))
	return err == nil
}

func memorySamplingAvailable() bool { return procfsReadable() }

const kernelBoundsMemory = false

const accountedPeakBelongsToTheChild = false

const maxRSSUnit = 1 << 10

const (
	statPgrp = 5
	statRSS  = 24
)

func treeResidentMemory(pid int) (int64, bool) {
	if pid <= 0 {
		return 0, false
	}
	kids, ok := processChildren(pid)
	if !ok {
		return groupResidentMemory(pid)
	}
	total, _ := sumTree(pid, kids, map[int]bool{})
	return total, true
}

func treeAndGroupResidentMemory(pid int) (int64, bool) {
	if pid <= 0 {
		return 0, false
	}
	seen := map[int]bool{}
	var total int64
	if kids, ok := processChildren(pid); ok {
		total, seen = sumTree(pid, kids, seen)
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		if len(seen) == 0 {
			return 0, false
		}
		return total, true
	}
	pageSize := int64(os.Getpagesize())
	for _, entry := range entries {
		other, err := strconv.Atoi(entry.Name())
		if err != nil || other <= 0 || seen[other] {
			continue
		}
		group, pages, ok := processStat(other)
		if !ok || group != pid {
			continue
		}
		seen[other] = true
		if pss, ok := processProportionalSet(other); ok {
			total += pss
			continue
		}
		total += pages * pageSize
	}
	return total, true
}

func sumTree(pid int, kids []int, seen map[int]bool) (int64, map[int]bool) {
	pageSize := int64(os.Getpagesize())
	seen[pid] = true
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
	return total, seen
}

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
		if !ok || group != pgid {
			continue
		}
		if pss, ok := processProportionalSet(pid); ok {
			total += pss
			continue
		}
		total += pages * pageSize
	}
	return total, true
}

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

func processStat(pid int) (pgrp int, rssPages int64, ok bool) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, false
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, 0, false
	}
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
