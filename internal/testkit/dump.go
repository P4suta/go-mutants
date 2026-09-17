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

const DumpDirName = "dump"

const (
	DumpFileLimit  = 64 << 10
	DumpTotalLimit = 1 << 20
)

func DumpFiles(t testing.TB, root string, globs ...string) {
	t.Helper()
	if len(globs) == 0 {
		globs = []string{"**"}
	}
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

func dumpName(t testing.TB, root string) string {
	l := ledgerFor(t)
	l.mu.Lock()
	l.dumps++
	index := l.dumps
	l.mu.Unlock()
	return strconv.Itoa(index) + "-" + sanitizedName(filepath.Base(root), keptNameBudget)
}

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
	read, err := io.ReadFull(file, buffer)
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
	case err != nil:
		return 0, nil, err
	}
	return info.Size(), buffer[:read], nil
}

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
