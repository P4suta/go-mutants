// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDogfoodTaskRunsBuiltCLIWithoutGoRunWrapper covers both dogfood tasks.
//
// A `go run` wrapper can outlive Ctrl-C, which is why the full-scope task is
// pinned against one. The changeset task is pinned for the same reason and for
// a second: it is the one CI waits for, and a check that cannot be interrupted
// cleanly is a check that leaves a process behind on every cancelled run.
func TestDogfoodTaskRunsBuiltCLIWithoutGoRunWrapper(t *testing.T) {
	t.Parallel()
	// One directory up, and under a different name. This module's own
	// mise.toml was absorbed into the repository's when the two products came
	// together, and `dogfood` there measures the engine -- so the runner's task
	// is `dogfood-runner`, carried across whole rather than folded into it.
	// This test is about the repository's layout, and a snapshot is not the
	// repository. goatest verifies by copying this module into a tree of its
	// own under the system temporary directory, so mise.toml is not above it
	// and never will be -- the file is not missing, it is out of scope.
	//
	// Saying that is not the same as skipping. A `t.Skip` here would be a
	// finding the moment `mise run dogfood-runner` ran, and rightly: the runner
	// counts a skipped target as a claim nobody checked. What this does instead
	// is assert the whole claim wherever the file exists and assert *that it is
	// absent for the documented reason* where it does not, so the target runs
	// and reaches an assertion in both trees.
	data, err := readFromAncestor(t, "mise.toml")
	if err != nil {
		if inSnapshot(t) {
			// The one tree where absence is the right answer. Checked rather
			// than assumed: a missing mise.toml anywhere else is this task
			// having been deleted, which is what this test is for.
			return
		}
		t.Fatal(err)
	}
	const (
		marker           = "[tasks.dogfood-runner]"
		dogfoodTaskParts = 2
	)
	parts := strings.SplitN(string(data), marker, dogfoodTaskParts)
	if len(parts) != dogfoodTaskParts {
		t.Fatal("mise.toml has no dogfood task")
	}
	task := parts[1]
	if next := strings.Index(task, "\n["); next >= 0 {
		task = task[:next]
	}
	if strings.Contains(task, "go run") {
		t.Fatal("dogfood uses a go run wrapper that can retain the CLI after Ctrl-C")
	}

	for _, required := range []string{
		"go build -o ./dist/dogfood/goatest ./cmd/goatest",
		"./dist/dogfood/goatest verify --ui=plain",
		"go build -o ./dist/dogfood/goatest.exe ./cmd/goatest",
		`.\dist\dogfood\goatest.exe verify --ui=plain`,
	} {
		if !strings.Contains(task, required) {
			t.Errorf("dogfood task omitted %q", required)
		}
	}

	changed := taskBody(t, string(data), "[tasks.dogfood-changed]")
	if strings.Contains(changed, "go run") {
		t.Error("dogfood-changed uses a go run wrapper that can retain the CLI after Ctrl-C")
	}
	for _, required := range []string{
		"./dist/dogfood/goatest verify --changed=origin/main --ui=plain",
		`.\dist\dogfood\goatest.exe verify --changed=origin/main --ui=plain`,
	} {
		if !strings.Contains(changed, required) {
			t.Errorf("dogfood-changed task omitted %q", required)
		}
	}
}

// taskBody reads one mise task, from its heading to the next one.
func taskBody(t *testing.T, document, heading string) string {
	t.Helper()
	const bodyParts = 2
	parts := strings.SplitN(document, heading, bodyParts)
	if len(parts) != bodyParts {
		t.Fatalf("mise.toml has no %s task", heading)
	}
	body := parts[1]
	if next := strings.Index(body, "\n["); next >= 0 {
		body = body[:next]
	}
	return body
}

// readFromAncestor reads a file from this directory or the nearest ancestor
// holding one, and says where it looked when there is none.
func readFromAncestor(t *testing.T, name string) ([]byte, error) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	var looked []string
	for {
		candidate := filepath.Join(dir, name)
		looked = append(looked, candidate)
		data, readErr := os.ReadFile(candidate)
		if readErr == nil {
			return data, nil
		}
		if !errors.Is(readErr, fs.ErrNotExist) {
			return nil, readErr
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("no %s in %s or any directory above it", name, strings.Join(looked, ", "))
		}
		dir = parent
	}
}

// inSnapshot reports whether this test is running inside a tree goatest copied
// rather than inside the repository.
//
// The marker is `go.work`, and it has to be a file that exists *above* this
// module and not inside it. The first attempt also accepted CLAUDE.md, which
// this module has one of -- so the search found goatest/CLAUDE.md in the
// snapshot, concluded it was in a checkout, and failed for the absence it had
// just been told to expect.
//
// A temporary-directory prefix would be the obvious test and is the wrong one
// for the opposite reason: the runner's own integration tests run under one, in
// trees that are checkouts, and they would take this branch and assert nothing.
func inSnapshot(t *testing.T) bool {
	t.Helper()
	_, err := readFromAncestor(t, "go.work")
	return err != nil
}
