// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// spdxNeedle is what an inline header is, spelled in two pieces so that this
// rule file is not its own first offender -- the same trick tiers_test.go uses
// for the toolchain needles.
const spdxNeedle = "SPDX-License" + "-Identifier:"

// headerWindow is how far into a file the scan looks for an inline header.
//
// A header belongs at the top. Four kilobytes is far past any preamble this
// repository writes and far short of reading a megabyte of vendored JavaScript
// into memory for a question its first line answers.
const headerWindow = 4 << 10

// plannedPaths are the annotations that deliberately run ahead of the tree: a
// path a bot will create, annotated now so that the day it appears is not also
// the day this gate first fails.
//
// It is a ledger for the same reason the toolchain allowlist is one. "This row
// matches nothing" is exactly the report a stale annotation earns, so the only
// honest way to exempt one is to write it down and say why -- and a row here
// that has started matching is itself stale, and fails.
var plannedPaths = map[string]string{
	".release-please/CHANGELOG.generated.md": "release-please creates it on the first release; REUSE.toml annotates it ahead of time so the first release is not also the first licensing failure",
}

// TestEveryFileIsLicensed is the REUSE gate, and it is a test rather than a
// tool for three reasons.
//
// REUSE.toml has existed since this repository did, and nothing has ever
// enforced it: `reuse` is in no [tools] block, no mise task and no workflow, so
// the manifest has been a promise rather than a check. A test needs no tool
// pinned on three platforms, runs in the tier that runs on every push, and can
// read the repository's own conventions -- which is the third reason: the
// question "which files does this repository commit" is a fact about git, and a
// filesystem walk would have to reimplement .gitignore to answer it.
//
// Both directions, for the reason every ledger here is checked both ways: a
// file that carries neither an inline header nor an annotation is unlicensed,
// and an annotation that covers no committed file is a row somebody forgot to
// delete -- which is how a manifest stops describing the tree it is about.
func TestEveryFileIsLicensed(t *testing.T) {
	root := Root(t)
	tracked := trackedFiles(t, root)
	if len(tracked) < 100 {
		t.Fatalf("git reports %d tracked files, which is too few to be this repository;"+
			" the scan is not looking where it thinks", len(tracked))
	}

	annotations := ReusePaths(t, root)
	if len(annotations) == 0 {
		t.Fatalf("%s declares no annotation paths; the parser has stopped seeing them", ReuseFile)
	}

	covered := map[string]bool{}
	for _, rel := range tracked {
		if hasInlineHeader(t, filepath.Join(root, filepath.FromSlash(rel))) {
			continue
		}
		// An error is a pattern this matcher will not guess at, and swallowing
		// it as "no match" would report the file it covers as unlicensed -- a
		// diagnosis about the wrong thing entirely.
		covering, err := ReuseCovering(annotations, rel)
		if err != nil {
			t.Fatalf("matching %s: %v", rel, err)
		}
		for _, pattern := range covering {
			covered[pattern] = true
		}
		if len(covering) == 0 {
			t.Errorf("%s carries no %s header and no %s annotation covers it;\n"+
				"\tadd the header, or add the path to %s with a comment saying why it cannot carry one",
				rel, spdxNeedle, ReuseFile, ReuseFile)
		}
	}

	for _, pattern := range annotations {
		live, err := ReuseAnnotationCoversATrackedFile(pattern, tracked)
		if err != nil {
			t.Fatalf("matching %q: %v", pattern, err)
		}
		matchesSomething := covered[pattern] || live
		if _, planned := plannedPaths[pattern]; planned {
			if matchesSomething {
				t.Errorf("%s annotates %q, which this tree now holds;\n"+
					"\tdelete its row from plannedPaths -- the annotation is doing its job\n"+
					"\tand no longer needs an excuse", ReuseFile, pattern)
			}
			continue
		}
		if covered[pattern] {
			continue
		}
		// Both remaining causes are a row to delete, and neither is a file
		// without a licence -- which is the reading this has to head off, since
		// the other thing this gate reports is exactly that.
		//
		// This branch used to log rather than fail, on the argument that a file
		// carrying a header *and* an annotation is belt and braces and that
		// deleting the row is a judgement. The argument is fine and the
		// mechanism was not: `go test` discards the log of a test that passes
		// and CI does not pass `-v`, so "say so once" said it to nobody. A gate
		// that reports into a void is the shape this repository refuses
		// elsewhere, and the manifest's own purpose settles which way to go --
		// it is for the files that *cannot* carry a header, so a row for one
		// that can is outside what the file is for.
		if matchesSomething {
			t.Errorf("%s annotates %q, which is committed and carries its own %s;\n"+
				"\tdelete the row -- this manifest is for the files that cannot carry a header,\n"+
				"\tand this is not a file without a licence", ReuseFile, pattern, spdxNeedle)
			continue
		}
		t.Errorf("%s annotates %q, which no committed file matches;\n"+
			"\tdelete the row -- a manifest that describes files this tree does not hold\n"+
			"\tis one a reader cannot trust about the files it does;\n"+
			"\tthis is not a file without a licence either", ReuseFile, pattern)
	}

	for pattern := range plannedPaths {
		if !slices.Contains(annotations, pattern) {
			t.Errorf("plannedPaths excuses %q, which %s does not annotate any more;\n"+
				"\tdelete the row -- a stale excuse is as wrong as a missing one", pattern, ReuseFile)
		}
	}
}

// trackedFiles is every path git holds or would hold: the index, plus the files
// beside it that nothing ignores.
//
// `--others --exclude-standard` is the half that decides when this gate fires.
// Without it the scan sees only what is committed, so a new file's licensing is
// checked one commit *after* it lands -- which is exactly how this gate first
// failed on a schema it had watched being added. With it, the question is asked
// where it can still be answered cheaply: in the working tree, by the pre-commit
// hook, before the file is in the history.
//
// GitBinary is called for its policy rather than for its answer: it is what
// turns a missing git into a skip locally and into a failure under
// GO_MUTANTS_TEST_REQUIRE_TOOLS, which CI sets. A gate that quietly retired
// itself on a runner whose git went missing would report green for nothing --
// and it is what puts this file in the tier ledger, where a git-driving unit
// test belongs.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	_ = GitBinary(t)
	out := Git(t, root, "ls-files", "--cached", "--others", "--exclude-standard")
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files
}

// hasInlineHeader reports whether a file states its licence in its own bytes.
func hasInlineHeader(t *testing.T, path string) bool {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("closing %s: %v", path, closeErr)
		}
	}()
	buffer := make([]byte, headerWindow)
	n, err := file.Read(buffer)
	if n == 0 && err != nil {
		return false
	}
	return strings.Contains(string(buffer[:n]), spdxNeedle)
}
