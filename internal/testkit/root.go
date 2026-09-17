// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const ModulePath = "github.com/P4suta/go-mutants"

const FixturesDir = "fixtures"

func Root(t testing.TB) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	root, err := moduleRoot(cwd)
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	return root
}

func Fixture(t testing.TB, name string) string {
	t.Helper()
	path, err := fixturePath(Root(t), name)
	if err != nil {
		t.Fatalf("resolving a fixture: %v", err)
	}
	rememberFixture(t, path)
	logInputs(t, "fixture="+path)
	return path
}

func FixtureNames(t testing.TB) []string {
	t.Helper()
	dir := filepath.Join(Root(t), FixturesDir)
	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the corpus at %s: %v", dir, err)
	}
	names := make([]string, 0, len(found))
	for _, entry := range found {
		if !entry.IsDir() || !isFixtureRoot(filepath.Join(dir, entry.Name())) {
			continue
		}
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

func isFixtureRoot(dir string) bool {
	for _, name := range []string{"go.mod", "go.work"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func moduleRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("making %s absolute: %w", start, err)
	}
	for {
		path, err := modulePathOf(filepath.Join(dir, "go.mod"))
		if err != nil {
			return "", err
		}
		if path == ModulePath {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod naming module %s at or above %s", ModulePath, start)
		}
		dir = parent
	}
}

func modulePathOf(gomod string) (string, error) {
	data, err := os.ReadFile(gomod)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading %s: %w", gomod, err)
	}
	for line := range strings.Lines(string(data)) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module")
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(rest)
		if trimmed == rest {
			continue
		}
		return strings.Trim(trimmed, "\""), nil
	}
	return "", nil
}

func fixturePath(root, name string) (string, error) {
	switch {
	case name == "":
		return "", errors.New("a fixture name may not be empty")
	case name != filepath.Base(name), name != filepath.Clean(name):
		return "", fmt.Errorf("fixture %q is not a name directly inside %s/", name, FixturesDir)
	case name == "." || name == "..":
		return "", fmt.Errorf("fixture %q is not a name directly inside %s/", name, FixturesDir)
	}
	path := filepath.Join(root, FixturesDir, name)
	if !isFixtureRoot(path) {
		return "", fmt.Errorf("fixture %q is not a tree a `go` command can be pointed at: "+
			"%s holds neither a go.mod nor a go.work", name, path)
	}
	return path, nil
}

func logInputs(t testing.TB, fields ...string) {
	t.Helper()
	if len(fields) == 0 {
		return
	}
	ForceFail(t)
	t.Logf("testkit: %s keep=%s", strings.Join(fields, " "), KeepPolicy())
}
