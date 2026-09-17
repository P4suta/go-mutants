// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"testing"

	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
)

func Snapshot(t testing.TB, name string) *snapshot.Snapshot {
	t.Helper()
	return SnapshotOf(t, testkit.Fixture(t, name))
}

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
