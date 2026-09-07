// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// DumpDirName is where [DumpFiles] copies what it printed, whole.
//
// Each call gets a numbered directory of its own inside it, named after the
// tree it came from. One directory for all of them was the first shape and it
// was wrong for the commonest case: a test that instruments the same fixture
// twice and compares the two trees — internal/validate's determinism test does
// exactly that — has two roots holding the same relative paths, so the second
// dump wrote over the first and what a reader found was one tree made of halves
// of each, with nothing saying so.
const DumpDirName = "dump"

// The caps [DumpFiles] prints inside.
//
// A CI log has a size limit of its own, and a dump that blows through it takes
// the failure it was supposed to explain with it. 64 KiB is far more than any
// instrumented Go file in this repository and far less than a log's budget; the
// megabyte is what a whole snapshot's worth of them would otherwise cost.
const (
	DumpFileLimit  = 64 << 10
	DumpTotalLimit = 1 << 20
)

// DumpFiles prints the files under root that a glob matches, but only when the
// test failed.
//
// It is what turns "the validation rejected three candidates" into a failure
// somebody can read: the instrumented source is written into a directory that is
// about to be removed, and a test that failed over its contents printed nothing
// about them. `**/*.go` is the pattern that matters here — `**` matches any run
// of path elements and everything else is [path.Match] on one element — and the
// paths are matched as slash-separated relative paths on every platform.
//
// Every printed header names the file by its full path rather than its path
// inside the tree, because a test with two snapshots prints two `limits.go` and
// a reader has to be able to tell which tree each came from — and because the
// path is what somebody pastes into an editor.
//
// The output is capped at [DumpFileLimit] per file and [DumpTotalLimit] over
// all of them, and it says what it elided. Whatever is kept is also copied
// whole into `<kept>/dump/<n>-<tree>/`, because the cap is a property of the log
// rather than of the evidence. [Verbose] makes it print on a test that passed.
func DumpFiles(t testing.TB, root string, globs ...string) {
	t.Helper()
	if len(globs) == 0 {
		globs = []string{"**"}
	}
	// Both resolved now rather than in the cleanup: creating the kept directory
	// is what registers the cleanup that decides its fate, and a cleanup may not
	// be the thing that starts that.
	kept := KeptDir(t)
	into := ""
	if kept != "" {
		into = filepath.Join(kept, DumpDirName, dumpName(t, root))
	}
	t.Cleanup(func() {
		keeping := Keeping(t)
		printing := t.Failed() || Verbose()
		if !keeping && !printing {
			return
		}
		found, err := matchingFiles(root, globs)
		if err != nil {
			t.Logf("testkit: the files under %s could not be listed for a dump: %v", root, err)
			return
		}
		if keeping && into != "" {
			copyDump(t, root, found, into)
		}
		if printing {
			printDump(t, root, found)
		}
	})
}

// dumpName is the directory one call's copies go in: its position in the test,
// then the name of the tree it came from.
//
// Both halves are needed. The number is what keeps two dumps of two trees apart
// when the trees are named alike — two snapshots of one fixture are
// `go-mutants-snap-<digest>` and differ only in the digest — and the name is
// what lets a reader tell which is which without counting the calls in the test.
func dumpName(t testing.TB, root string) string {
	l := ledgerFor(t)
	l.mu.Lock()
	l.dumps++
	index := l.dumps
	l.mu.Unlock()
	return strconv.Itoa(index) + "-" + sanitizedName(filepath.Base(root), keptNameBudget)
}

// matchingFiles lists the files under root that any glob matches, by relative
// slash-separated path, sorted.
func matchingFiles(root string, globs []string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, walkErr error) error {
		switch {
		case walkErr != nil:
			return walkErr
		case entry.IsDir():
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		slashed := filepath.ToSlash(rel)
		if slices.ContainsFunc(globs, func(glob string) bool { return matchPath(glob, slashed) }) {
			found = append(found, slashed)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(found)
	return found, nil
}

// matchPath matches one relative path against a glob in which `**` stands for
// any run of path elements.
//
// [path.Match] alone cannot express it: its `*` stops at a separator, so
// `**/*.go` matches `a/b.go` and not `b.go`, and every caller wants both. The
// pattern is split on `/` and matched element by element, which is exact for
// the shapes this repository uses and answers the same way on every platform,
// because the paths are made slash-separated before they arrive.
func matchPath(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pattern, name []string) bool {
	switch {
	case len(pattern) == 0:
		return len(name) == 0
	case pattern[0] == "**":
		for skip := 0; skip <= len(name); skip++ {
			if matchSegments(pattern[1:], name[skip:]) {
				return true
			}
		}
		return false
	case len(name) == 0:
		return false
	}
	ok, err := path.Match(pattern[0], name[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], name[1:])
}

// printDump prints the matched files, inside the two caps.
func printDump(t testing.TB, root string, found []string) {
	t.Helper()
	total := 0
	for index, rel := range found {
		if total >= DumpTotalLimit {
			t.Logf("… elided %d more file(s) under %s: the dump is capped at %d bytes",
				len(found)-index, root, DumpTotalLimit)
			return
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		size, shown, err := readCapped(full, DumpFileLimit)
		if err != nil {
			t.Logf("--- %s could not be read: %v", full, err)
			continue
		}
		note := ""
		if size > int64(len(shown)) {
			note = fmt.Sprintf("\n… elided %d bytes: one file is capped at %d",
				size-int64(len(shown)), DumpFileLimit)
		}
		total += len(shown)
		t.Logf("--- %s (%d bytes)\n%s%s", full, size, shown, note)
	}
}

// readCapped returns a file's true size and at most limit bytes of it.
//
// The size comes from a stat and the bytes from a bounded read, so that a dump
// pointed at a tree holding something enormous — a coverage profile, a test
// binary a build left behind — costs the cap rather than the file. Reading the
// whole thing to print a cap of it is the shape this replaces.
func readCapped(path string, limit int) (int64, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return 0, nil, err
	}
	buffer := make([]byte, limit)
	// A short file is the ordinary case rather than a failure: io.ReadFull
	// reports one as ErrUnexpectedEOF and an empty one as EOF, and both mean the
	// whole file is in the buffer.
	read, err := io.ReadFull(file, buffer)
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
	case err != nil:
		return 0, nil, err
	}
	return info.Size(), buffer[:read], nil
}

// copyDump copies the matched files whole into the kept directory, because the
// caps above are a property of the log rather than of the evidence.
func copyDump(t testing.TB, root string, found []string, into string) {
	t.Helper()
	for _, rel := range found {
		target := filepath.Join(into, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Logf("testkit: the dump directory %s could not be created: %v", into, err)
			return
		}
		if err := copyFile(filepath.Join(root, filepath.FromSlash(rel)), target, 0o644); err != nil {
			t.Logf("testkit: %s could not be copied into the dump: %v", rel, err)
		}
	}
}
