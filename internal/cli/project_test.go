// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
)

// TestAProjectIsAModuleOrTheWorkspaceOfSeveral pins what "this directory's
// history" means, which is the question every history command asks first.
//
// A module and the workspace it belongs to are different projects here, and
// deliberately so: a mutant measured in a workspace and the same mutant
// measured alone have different identities, so a listing that mixed the two
// would offer runs whose ids do not mean the same thing. See ADR 0012.
func TestAProjectIsAModuleOrTheWorkspaceOfSeveral(t *testing.T) {
	t.Parallel()

	t.Run("a module", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeProjectFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.26\n")
		got, err := projectAt(dir)
		if err != nil {
			t.Fatalf("projectAt: %v", err)
		}
		if got.name() != "example.com/m" {
			t.Errorf("name() = %q, want the module path", got.name())
		}
		if !got.holds(report.StoredRun{ModulePath: "example.com/m"}) {
			t.Error("a module does not hold its own run")
		}
		if got.holds(report.StoredRun{ModulePath: "example.com/other"}) {
			t.Error("a module holds another module's run")
		}
		if got.holds(report.StoredRun{Modules: []string{"example.com/m"}}) {
			t.Error("a module holds the run of a workspace that joins it")
		}
	})

	t.Run("a workspace", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeProjectFile(t, filepath.Join(dir, "go.work"), "go 1.26\n\nuse (\n\t./a\n\t./b\n)\n")
		writeProjectFile(t, filepath.Join(dir, "a", "go.mod"), "module example.com/a\n\ngo 1.26\n")
		writeProjectFile(t, filepath.Join(dir, "b", "go.mod"), "module example.com/b\n\ngo 1.26\n")

		got, err := projectAt(dir)
		if err != nil {
			t.Fatalf("projectAt: %v", err)
		}
		for _, want := range []string{"example.com/a", "example.com/b"} {
			if !strings.Contains(got.name(), want) {
				t.Errorf("name() = %q, want it to name %s", got.name(), want)
			}
		}
		if !got.holds(report.StoredRun{Modules: []string{"example.com/a", "example.com/b"}}) {
			t.Error("a workspace does not hold its own run")
		}
		if got.holds(report.StoredRun{Modules: []string{"example.com/a"}}) {
			t.Error("a workspace holds the run of a workspace joining fewer modules")
		}
		if got.holds(report.StoredRun{ModulePath: "example.com/a"}) {
			t.Error("a workspace holds the run of one of its modules measured alone")
		}
	})

	t.Run("neither", func(t *testing.T) {
		t.Parallel()

		if _, err := projectAt(t.TempDir()); err == nil {
			t.Error("a directory that is neither a module nor a workspace was accepted")
		}
	})
}

// writeProjectFile writes one file of a tree under test, making its directory.
func writeProjectFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestDoctorNamesAWorkspaceWhereAModuleWouldBe keeps the first command a user
// runs from telling them go-mutants cannot run where it can.
//
// `doctor` is the "can this tool work here" check, and a `go.work` is a tree it
// works on — as one run over every module the file joins. A check that only
// knew about `go.mod` would fail on a workspace and send the reader looking for
// a module that is not supposed to be there.
func TestDoctorNamesAWorkspaceWhereAModuleWouldBe(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeProjectFile(t, filepath.Join(dir, "go.work"), "go 1.26\n\nuse (\n\t./a\n\t./b\n)\n")
	writeProjectFile(t, filepath.Join(dir, "a", "go.mod"), "module example.com/a\n\ngo 1.26\n")
	writeProjectFile(t, filepath.Join(dir, "b", "go.mod"), "module example.com/b\n\ngo 1.26\n")

	got := moduleCheck(dir)
	if got.Status != statusOK {
		t.Fatalf("the module check is %q at a workspace root: %s", got.Status, got.Detail)
	}
	for _, want := range []string{"2 modules", "go.work", "example.com/a", "example.com/b"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("the detail %q does not name %q", got.Detail, want)
		}
	}

	// And a directory that is neither still fails, with the sentence that sends
	// the reader to a module root.
	if bare := moduleCheck(t.TempDir()); bare.Status != statusFail {
		t.Errorf("the module check is %q where there is neither a module nor a workspace", bare.Status)
	}
}
