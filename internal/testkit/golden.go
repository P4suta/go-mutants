// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// GoldenDir is the directory every golden file in this repository lives in,
// relative to the package whose test compares against it.
//
// `go test` runs a test binary in its own package's source directory and the go
// command ignores `testdata` when it looks for packages, which is why the
// convention exists and why it is a constant here rather than an argument: one
// package spelling it differently from the next is how a `-update` task comes
// to miss a golden.
const GoldenDir = "testdata"

// updateGolden is the repository's one -update flag.
//
// It is registered here, in a non-test file, on purpose. flag.Bool in an init
// registers the flag in every binary that links this package — and this package
// is test-only, enforced by TestProductionCodeDoesNotImportTestkit, so that set
// is exactly "the test binaries". Registering it once is not a tidiness
// argument: internal/report and internal/instrument each registered their own,
// and the moment a third package that imported either of them had registered a
// second one in the same binary, package flag would have panicked with "flag
// redefined: update" before a single test ran.
//
// A flag rather than an environment variable because that is what the go
// command already passes through — with one trap worth knowing about. cmd/go
// knows its own test flags and nothing else, and on an unrecognised one it stops
// parsing and hands the rest of the command line to the test binary. So
// `go test -update ./internal/report` runs the package in the *working
// directory* and passes it a path it ignores; the spelling that works, and the
// one `mise run golden-update` uses, names the packages first:
//
//	go test ./internal/report -update
var updateGolden = flag.Bool("update", false,
	"rewrite the golden files this run compares against, instead of comparing")

// Update reports whether this run was asked to rewrite its goldens.
//
// A test with a golden of a shape this package cannot compare — a directory of
// files, a document it has to normalise first — reads the flag through this and
// records under it, so that there is still one flag.
func Update() bool { return *updateGolden }

// GoldenPath is where the golden called name lives, for a test that needs the
// path itself rather than the comparison.
func GoldenPath(name string) string { return filepath.Join(GoldenDir, name) }

// Golden compares got against `testdata/<name>`, and rewrites it under -update.
//
// It is the whole golden protocol in one call, and every rule in it is one the
// four hand-written copies in this repository disagreed about:
//
//   - A missing file ends the test. It is never recorded silently, because a
//     silent first recording turns the first run of a new test — and every run
//     after a golden was deleted or lost in a merge — into a green one that pins
//     nothing at all. Recording is a decision somebody makes with -update and
//     reads the diff of.
//   - A mismatch is reported as a diff, once, with the file named. The goldens
//     here are whole documents; printing both halves of a two-thousand-line JSON
//     comparison means saving them out of a CI log and diffing them by hand.
//   - A mismatch is [testing.TB.Errorf] rather than Fatalf, so a table of
//     goldens reports every case that moved in one run instead of the first.
func Golden(t testing.TB, name string, got []byte) {
	t.Helper()
	goldenAt(t, GoldenPath(name), got, Update())
}

// goldenAt is [Golden] with the path and the decision passed in.
//
// The split is what lets this package's own tests drive the reporting without
// a golden file of their own in testdata — which would be a golden file the
// golden-update task then has to name — and without depending on the state of a
// process-wide flag.
func goldenAt(t testing.TB, path string, got []byte, update bool) {
	t.Helper()
	err := CompareGolden(path, got, update)
	switch {
	case err == nil:
		if update {
			t.Logf("testkit: rewrote %s (%d bytes); read the diff before committing it", path, len(got))
		}
	case errors.Is(err, os.ErrNotExist):
		// Fatal rather than Errorf: there is nothing to compare, so every
		// assertion after this one in the same test is about a document nobody
		// has ever read.
		t.Fatalf("%v", err)
	default:
		t.Errorf("%v", err)
	}
}

// CompareGolden compares got against the file at path, or rewrites it.
//
// It is the [testing.TB]-free half of [Golden], for the callers that are not a
// test — a devtool that regenerates a corpus, a check that asks whether a
// document would still match without failing anything.
//
// A missing file is reported as an error wrapping [os.ErrNotExist], so a caller
// can tell "the recording is not there" from "the recording says something
// else": the first is a mistake to fix with -update and the second is a change
// to justify.
func CompareGolden(path string, got []byte, update bool) error {
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("creating the directory above the golden file %s: %w", path, err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			return fmt.Errorf("rewriting the golden file %s: %w", path, err)
		}
		return nil
	}
	want, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading the golden file %s: %w\n"+
			"A golden file is never recorded by a comparison. Regenerate it on purpose with "+
			"`mise run golden-update` (or `go test ./<package> -update`) and read the diff before committing",
			path, err)
	}
	if bytes.Equal(want, got) {
		return nil
	}
	// cmp.Diff renders two multi-line strings as a line diff with the runs they
	// agree on elided, which is the only readable form for documents this size.
	// The two are converted to strings rather than compared as []byte so that
	// it takes that path rather than printing a byte-by-byte listing.
	return fmt.Errorf("the bytes do not match the golden file %s (-want +got):\n%s\n"+
		"Regenerate with `mise run golden-update` once the diff above is what you meant",
		path, cmp.Diff(string(want), string(got)))
}
