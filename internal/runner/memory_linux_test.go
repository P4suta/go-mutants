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

// TestAKernelWithoutProcfsCannotEnforceABound is the fail-closed claim, and it
// is a claim about the *report* rather than about the bound.
//
// Nothing goes wrong if /proc is unreadable: the sampler reads no number, stops
// on the first tick, and the run goes on with its mutants bounded in time
// alone. That is the right behaviour and it is also invisible — a container
// with a hidden procfs would report itself as a platform that enforces bounds,
// derive one from the baseline, put it in the report, and enforce nothing. So
// the capability is probed rather than assumed from the build tag, and a
// machine that cannot answer says so where every other unenforceable platform
// says it.
func TestAKernelWithoutProcfsCannotEnforceABound(t *testing.T) {
	// Not parallel: it moves a package-level seam.
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

	// A readable root of this test's own making, rather than the machine's:
	// asserting that the real /proc answers would be asserting something about
	// whoever is running the suite — a hardened kernel, a container, a sandbox
	// — and this test is about the seam rather than about them.
	procRoot = readableProc(t)
	resetProcfsProbe()
	if !MemoryBoundSupported() {
		t.Error("MemoryBoundSupported() = false with a readable procfs")
	}

	// And the machine's own answer is restored, whatever it was.
	procRoot = original
	resetProcfsProbe()
	if got := MemoryBoundSupported(); got != before {
		t.Errorf("MemoryBoundSupported() = %t after restoring the real proc root, want the %t it was before",
			got, before)
	}
}

// readableProc is a proc root with the one entry the probe reads, so a test can
// arrange the answer "yes" without depending on the machine giving it.
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

// TestTheProcfsProbeIsMadeOnce pins that the answer is cached: it is asked
// before every bounded run, and a run of thousands of mutants must not pay a
// file read per mutant for a question whose answer cannot change.
func TestTheProcfsProbeIsMadeOnce(t *testing.T) {
	original := procRoot
	t.Cleanup(func() {
		procRoot = original
		resetProcfsProbe()
	})

	// A root of this test's own making, so that the cached answer is `true`
	// whatever the machine would have said.
	procRoot = readableProc(t)
	resetProcfsProbe()
	if !MemoryBoundSupported() {
		t.Fatal("MemoryBoundSupported() = false with a readable procfs")
	}
	// The seam moves and the answer does not, because nothing reads procfs a
	// second time.
	procRoot = filepath.Join(t.TempDir(), "no-such-proc")
	if !MemoryBoundSupported() {
		t.Error("the probe was made again after it had already answered")
	}
}

// fakeProc writes a proc root holding the processes a test describes: a `stat`
// line with the group and a resident page count, and — when the test says so —
// a `smaps_rollup` carrying a proportional set size.
//
// The stat line's second field is deliberately a name with a space and a
// parenthesis in it, because that is the shape a program can choose for itself
// and the parser has to survive: split on spaces from the left and the name's
// own bytes become the numbers this function is trying to report.
func fakeProc(t *testing.T, procs []fakeProcess) string {
	t.Helper()

	root := t.TempDir()
	for _, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(p.pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
		// pid, comm, then state and the rest: pgrp is field 5 and rss field 24.
		fields := make([]string, 0, 24)
		fields = append(fields, strconv.Itoa(p.pid), "(go) test)", "S", "1", strconv.Itoa(p.pgrp))
		for len(fields) < 24 {
			fields = append(fields, "0")
		}
		fields[23] = strconv.FormatInt(p.rssPages, 10)
		writeProcFile(t, filepath.Join(dir, "stat"), strings.Join(fields, " ")+"\n")

		if p.children != nil {
			// The children file lives under a thread directory; the main thread
			// has the pid's own id, and a process may have others.
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

// fakeProcess is one row of a fake proc root. A negative pssKB writes no
// smaps_rollup at all, which is the older kernel this code has to fall back on.
type fakeProcess struct {
	pid      int
	pgrp     int
	rssPages int64
	pssKB    int64
	// children, when non-nil, writes a task/<pid>/children file naming them —
	// an empty slice writes an empty file, which is a kernel that has the
	// file and a process that has no children. nil writes no file at all,
	// which is a kernel built without CONFIG_PROC_CHILDREN.
	children []int
}

func writeProcFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestTheGroupsMemoryIsSummedProportionallyAcrossSharers is the reason this
// reads Pss rather than VmRSS, and it is a correctness bug rather than a
// refinement.
//
// A `go test -fuzz` run is a coordinator plus one worker process per core, and
// every one of them maps the *same* 100 MiB shared-memory region. VmRSS counts
// a shared page once in every process that has it resident, so summing VmRSS
// over that group reports the region once per worker — on an eight-core machine
// that is 800 MiB of memory nobody is using, which is most of a gibibyte bound
// spent on double counting, and a legitimate fuzz run killed for it.
//
// Pss divides a shared page between its sharers, so the same region is counted
// once however many processes map it. That is the number a budget is about.
func TestTheGroupsMemoryIsSummedProportionallyAcrossSharers(t *testing.T) {
	original := procRoot
	t.Cleanup(func() { procRoot = original })

	// Two processes in one group, each with 200 MiB resident and each with half
	// of it shared with the other: 100 MiB private plus 100 MiB of one shared
	// region. VmRSS would say 400 MiB; the truth is 300 MiB.
	const mib = 1 << 20
	pages := int64(200 * mib / os.Getpagesize())
	procRoot = fakeProc(t, []fakeProcess{
		{pid: 100, pgrp: 100, rssPages: pages, pssKB: 150 * 1024},
		{pid: 101, pgrp: 100, rssPages: pages, pssKB: 150 * 1024},
		// A third process in another group, which must not be counted at all.
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

// TestAKernelWithoutSmapsRollupFallsBackToTheResidentSet keeps the older kernel
// working.
//
// smaps_rollup arrived in Linux 4.14 and a hardened kernel can refuse it even
// where it exists. Falling back to VmRSS over-counts a shared region, which
// makes the bound *tighter* than it should be — the safe direction for a
// fallback, and the behaviour every version of this code had before Pss.
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
	// 64 MiB from the process with no rollup, 32 MiB from the one that has one.
	if want := int64(96 * mib); got != want {
		t.Errorf("groupResidentMemory = %d, want %d", got, want)
	}
}

// TestTheTreeIsWalkedThroughTheChildrenFiles pins the measurement a
// sample is made of when the kernel can name a process's children: the child
// itself, its children, and their children — including one that left the
// process group, which the group scan would not see and the kill will not
// reach, but which is still memory this tree is holding.
//
// It is also the reason a sample is cheap enough to take at once: the walk
// reads the tree's own few files rather than one file per process on the
// machine, which on a busy developer box is several hundred reads and the
// milliseconds a short-lived test binary does not have.
func TestTheTreeIsWalkedThroughTheChildrenFiles(t *testing.T) {
	original := procRoot
	t.Cleanup(func() { procRoot = original })

	const mib = 1 << 20
	procRoot = fakeProc(t, []fakeProcess{
		{pid: 100, pgrp: 100, rssPages: 1, pssKB: 10 * 1024, children: []int{101}},
		{pid: 101, pgrp: 100, rssPages: 1, pssKB: 20 * 1024, children: []int{102}},
		// A grandchild that called setsid: out of the group, still in the tree.
		{pid: 102, pgrp: 102, rssPages: 1, pssKB: 40 * 1024, children: []int{}},
		// A process in the same group that is not a descendant cannot happen
		// on a real machine — a group member is by construction a descendant
		// of its leader — but if it did, the walk does not invent it.
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

// TestAKernelWithoutChildrenFilesFallsBackToTheGroupScan keeps a kernel built
// without CONFIG_PROC_CHILDREN working: with no children file to walk, the
// sample is the process-group scan it always was.
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

// TestAChildTheChildrenFileOmittedIsStillCountedByTheThoroughSample is the
// race proc(5) warns about, staged: the children file is reliable only for a
// stopped process, and a live child can be missing from it when a sibling
// exits during the read. The quick sample honestly reports what the walk
// reached; the thorough one — the one a bound is enforced from — adds every
// group member the walk missed.
func TestAChildTheChildrenFileOmittedIsStillCountedByTheThoroughSample(t *testing.T) {
	original := procRoot
	t.Cleanup(func() { procRoot = original })

	const mib = 1 << 20
	procRoot = fakeProc(t, []fakeProcess{
		// The root names only one of its two children.
		{pid: 100, pgrp: 100, rssPages: 1, pssKB: 10 * 1024, children: []int{101}},
		{pid: 101, pgrp: 100, rssPages: 1, pssKB: 20 * 1024, children: []int{}},
		// Alive, in the group, and absent from the file.
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
