// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const SPDXHeader = "// SPDX-FileCopyrightText: 2026 go-mutants contributors\n" +
	"// SPDX-License-Identifier: MIT OR Apache-2.0\n\n"

const TreeAge = time.Hour

func Copy(t testing.TB, name string) string {
	t.Helper()
	from, err := fixturePath(Root(t), name)
	if err != nil {
		t.Fatalf("resolving a fixture to copy: %v", err)
	}
	to := filepath.Join(Scratch(t), name)
	CopyTree(t, from, to)
	AgeTree(t, to)
	rememberFixture(t, from)
	logInputs(t, "fixture="+from, "scratch="+to)
	return to
}

func CopyTree(t testing.TB, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatalf("creating the directories above %s: %v", to, err)
	}
	err := filepath.WalkDir(from, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		if err := refuseIrregular(path, entry); err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm()|0o200)
	})
	if err != nil {
		t.Fatalf("copying %s to %s: %v", from, to, err)
	}
}

func refuseIrregular(path string, entry fs.DirEntry) error {
	if entry.Type().IsRegular() {
		return nil
	}
	return fmt.Errorf("%s is a %v, and only directories and regular files are handled here", path, entry.Type())
}

func copyFile(from, to string, perm fs.FileMode) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	destination, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		return err
	}
	return destination.Close()
}

func AgeTree(t testing.TB, root string) {
	t.Helper()
	when := time.Now().Add(-TreeAge)

	var directories []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if err := refuseIrregular(path, entry); err != nil {
			return err
		}
		return os.Chtimes(path, when, when)
	})
	if err != nil {
		t.Fatalf("ageing the files under %s: %v", root, err)
	}

	slices.Sort(directories)
	slices.Reverse(directories)
	for _, dir := range directories {
		if err := os.Chtimes(dir, when, when); err != nil {
			t.Fatalf("ageing the directory %s: %v", dir, err)
		}
	}
}

func WriteFile(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating the directories above %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func WriteSource(t testing.TB, dir, name, body string) {
	t.Helper()
	WriteFile(t, filepath.Join(dir, name), []byte(SPDXHeader+body))
}

func agePath(t testing.TB, root, path string) {
	t.Helper()
	when := time.Now().Add(-TreeAge)
	for current := path; ; current = filepath.Dir(current) {
		if err := os.Chtimes(current, when, when); err != nil {
			t.Fatalf("ageing %s: %v", current, err)
		}
		if SamePath(current, root) || !within(root, current) {
			return
		}
		if parent := filepath.Dir(current); parent == current {
			return
		}
	}
}

func ReadFile(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

func Entries(t testing.TB, dir string) []string {
	t.Helper()
	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(found))
	for _, entry := range found {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

func SamePath(a, b string) bool {
	return resolvePath(a) == resolvePath(b)
}

func resolvePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}
