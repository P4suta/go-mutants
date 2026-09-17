// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAKernelWithoutProcfsCannotEnforceABound(t *testing.T) {
	original := procRoot
	before := MemoryBoundSupported()
	t.Cleanup(func() {
		procRoot = original
		resetProcfsProbe()
	})

	procRoot = filepath.Join(t.TempDir(), "no-such-proc")
	resetProcfsProbe()
	if MemoryBoundSupported() {
		t.Error("MemoryBoundSupported() = true with no procfs to read")
	}
	if _, ok := groupResidentMemory(1); ok {
		t.Error("groupResidentMemory answered from a procfs that is not there")
	}

	procRoot = readableProc(t)
	resetProcfsProbe()
	if !MemoryBoundSupported() {
		t.Error("MemoryBoundSupported() = false with a readable procfs")
	}

	procRoot = original
	resetProcfsProbe()
	if got := MemoryBoundSupported(); got != before {
		t.Errorf("MemoryBoundSupported() = %t after restoring the real proc root, want the %t it was before",
			got, before)
	}
}

func readableProc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	self := filepath.Join(root, "self")
	if err := os.MkdirAll(self, 0o755); err != nil {
		t.Fatalf("creating %s: %v", self, err)
	}
	writeProcFile(t, filepath.Join(self, "stat"), "1 (go) test) S 1 1 0\n")
	return root
}

func TestTheProcfsProbeIsMadeOnce(t *testing.T) {
	original := procRoot
	t.Cleanup(func() {
		procRoot = original
		resetProcfsProbe()
	})

	procRoot = readableProc(t)
	resetProcfsProbe()
	if !MemoryBoundSupported() {
		t.Fatal("MemoryBoundSupported() = false with a readable procfs")
	}
	procRoot = filepath.Join(t.TempDir(), "no-such-proc")
	if !MemoryBoundSupported() {
		t.Error("the probe was made again after it had already answered")
	}
}

func fakeProc(t *testing.T, procs []fakeProcess) string {
	t.Helper()

	root := t.TempDir()
	for _, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(p.pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
		fields := make([]string, 0, 24)
		fields = append(fields, strconv.Itoa(p.pid), "(go) test)", "S", "1", strconv.Itoa(p.pgrp))
		for len(fields) < 24 {
			fields = append(fields, "0")
		}
		fields[23] = strconv.FormatInt(p.rssPages, 10)
		writeProcFile(t, filepath.Join(dir, "stat"), strings.Join(fields, " ")+"\n")

		if p.children != nil {
			taskDir := filepath.Join(dir, "task", strconv.Itoa(p.pid))
			if err := os.MkdirAll(taskDir, 0o755); err != nil {
				t.Fatalf("creating %s: %v", taskDir, err)
			}
			kids := make([]string, 0, len(p.children))
			for _, kid := range p.children {
				kids = append(kids, strconv.Itoa(kid))
			}
			writeProcFile(t, filepath.Join(taskDir, "children"), strings.Join(kids, " ")+" ")
		}

		if p.pssKB < 0 {
			continue
		}
		writeProcFile(t, filepath.Join(dir, "smaps_rollup"),
			"55d5e0000000-7ffd00000000 ---p 00000000 00:00 0                          [rollup]\n"+
				"Rss:              999999 kB\n"+
				"Pss:              "+strconv.FormatInt(p.pssKB, 10)+" kB\n"+
				"Shared_Clean:          0 kB\n")
	}
	return root
}

type fakeProcess struct {
	pid      int
	pgrp     int
	rssPages int64
	pssKB    int64
	children []int
}

func writeProcFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestTheGroupsMemoryIsSummedProportionallyAcrossSharers(t *testing.T) {
	original := procRoot
	t.Cleanup(func() { procRoot = original })

	const mib = 1 << 20
	pages := int64(200 * mib / os.Getpagesize())
	procRoot = fakeProc(t, []fakeProcess{
		{pid: 100, pgrp: 100, rssPages: pages, pssKB: 150 * 1024},
		{pid: 101, pgrp: 100, rssPages: pages, pssKB: 150 * 1024},
		{pid: 200, pgrp: 200, rssPages: pages, pssKB: 150 * 1024},
	})

	got, ok := groupResidentMemory(100)
	if !ok {
		t.Fatal("groupResidentMemory could not read the fake proc root")
	}
	if want := int64(300 * mib); got != want {
		t.Errorf("groupResidentMemory = %d, want %d: a shared region counted once per sharer is %d",
			got, want, 400*mib)
	}
}

func TestAKernelWithoutSmapsRollupFallsBackToTheResidentSet(t *testing.T) {
	original := procRoot
	t.Cleanup(func() { procRoot = original })

	const mib = 1 << 20
	pages := int64(64 * mib / os.Getpagesize())
	procRoot = fakeProc(t, []fakeProcess{
		{pid: 300, pgrp: 300, rssPages: pages, pssKB: -1},
		{pid: 301, pgrp: 300, rssPages: pages, pssKB: 32 * 1024},
	})

	got, ok := groupResidentMemory(300)
	if !ok {
		t.Fatal("groupResidentMemory could not read the fake proc root")
	}
	if want := int64(96 * mib); got != want {
		t.Errorf("groupResidentMemory = %d, want %d", got, want)
	}
}

func TestTheTreeIsWalkedThroughTheChildrenFiles(t *testing.T) {
	original := procRoot
	t.Cleanup(func() { procRoot = original })

	const mib = 1 << 20
	procRoot = fakeProc(t, []fakeProcess{
		{pid: 100, pgrp: 100, rssPages: 1, pssKB: 10 * 1024, children: []int{101}},
		{pid: 101, pgrp: 100, rssPages: 1, pssKB: 20 * 1024, children: []int{102}},
		{pid: 102, pgrp: 102, rssPages: 1, pssKB: 40 * 1024, children: []int{}},
		{pid: 300, pgrp: 300, rssPages: 1, pssKB: 80 * 1024, children: []int{}},
	})

	got, ok := treeResidentMemory(100)
	if !ok {
		t.Fatal("treeResidentMemory could not read the fake proc root")
	}
	if want := int64(70 * mib); got != want {
		t.Errorf("treeResidentMemory = %d, want %d: the child, its child and the grandchild that left the group",
			got, want)
	}
}

func TestAKernelWithoutChildrenFilesFallsBackToTheGroupScan(t *testing.T) {
	original := procRoot
	t.Cleanup(func() { procRoot = original })

	const mib = 1 << 20
	procRoot = fakeProc(t, []fakeProcess{
		{pid: 100, pgrp: 100, rssPages: 1, pssKB: 10 * 1024},
		{pid: 101, pgrp: 100, rssPages: 1, pssKB: 20 * 1024},
		{pid: 102, pgrp: 102, rssPages: 1, pssKB: 40 * 1024},
	})

	got, ok := treeResidentMemory(100)
	if !ok {
		t.Fatal("treeResidentMemory could not read the fake proc root")
	}
	if want := int64(30 * mib); got != want {
		t.Errorf("treeResidentMemory = %d, want %d: without children files only the group can be summed",
			got, want)
	}
}

func TestAChildTheChildrenFileOmittedIsStillCountedByTheThoroughSample(t *testing.T) {
	original := procRoot
	t.Cleanup(func() { procRoot = original })

	const mib = 1 << 20
	procRoot = fakeProc(t, []fakeProcess{
		{pid: 100, pgrp: 100, rssPages: 1, pssKB: 10 * 1024, children: []int{101}},
		{pid: 101, pgrp: 100, rssPages: 1, pssKB: 20 * 1024, children: []int{}},
		{pid: 103, pgrp: 100, rssPages: 1, pssKB: 40 * 1024, children: []int{}},
		{pid: 300, pgrp: 300, rssPages: 1, pssKB: 80 * 1024, children: []int{}},
	})

	quick, ok := treeResidentMemory(100)
	if !ok || quick != 30*mib {
		t.Errorf("treeResidentMemory = %d, %v; want %d: the walk reports what the file names", quick, ok, 30*mib)
	}
	thorough, ok := treeAndGroupResidentMemory(100)
	if !ok || thorough != 70*mib {
		t.Errorf("treeAndGroupResidentMemory = %d, %v; want %d: the group member the file omitted is counted once",
			thorough, ok, 70*mib)
	}
}
