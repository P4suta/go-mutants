// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"testing"

	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// Snapshot copies one corpus module into a disposable directory the way a real
// run does, and returns the snapshot.
//
// It is a snapshot rather than a plain copy because that is what every phase
// below it takes: discovery resolves packages under a snapshot root,
// instrumentation rewrites files in place under one, and the workspace digest a
// report carries is computed from the manifest [snapshot.Create] builds. A test
// that copied the tree by hand would be testing a pipeline nobody runs.
func Snapshot(t testing.TB, name string) *snapshot.Snapshot {
	t.Helper()
	return SnapshotOf(t, testkit.Fixture(t, name))
}

// SnapshotOf snapshots a tree the test already has — a synthesized module, a
// fixture it has edited, a copy it made itself.
//
// Three things happen in a fixed order, and each of them is a bug somebody
// already had:
//
//   - The destination is the test's own temporary directory. A snapshot taken
//     with the default DestParent lands in the operating system's temporary
//     directory, where a failed run's debris outlives the run and the next test's
//     sweep has to decide whether it is alive.
//   - The removal is registered immediately, before anything else in this
//     function can fail. A cleanup registered after the ageing walk would be
//     skipped by an ageing walk that failed the test, which is precisely the case
//     that leaves a tree behind.
//   - The tree is aged. cmd/go indexes a package directory only when every file
//     in it is at least two seconds old, so a snapshot taken a moment ago behaves
//     differently from one a user has: the first `go list` over it indexes
//     nothing and the second one might, which makes any assertion about cache
//     entries — or about a build being reused — depend on how long the copy took.
//
// The destination is [testkit.Scratch], so that a failed test's snapshot is what
// the keep policy keeps — and the removal above is skipped when it is being
// kept, because a snapshot removed by its own cleanup is precisely the
// instrumented tree somebody wanted to read. Every `.go` file under it is
// registered with [testkit.DumpFiles], which is what makes an instrumentation or
// validation failure print the source it was about rather than only its verdict.
func SnapshotOf(t testing.TB, root string) *snapshot.Snapshot {
	t.Helper()
	parent := testkit.Scratch(t)
	snap, err := snapshot.Create(root, snapshot.Options{DestParent: parent})
	if err != nil {
		t.Fatalf("snapshotting %s into %s: %v", root, parent, err)
		return nil
	}
	t.Cleanup(func() {
		if testkit.Keeping(t) {
			return
		}
		if err := snap.Cleanup(); err != nil {
			t.Errorf("cleaning up the snapshot at %s: %v", snap.Root, err)
		}
	})
	testkit.AgeTree(t, snap.Root)
	testkit.DumpFiles(t, snap.Root, "**/*.go")
	logInputs(t, "source="+root, "snapshot="+snap.Root, "digest="+snap.WorkspaceDigest)
	return snap
}
