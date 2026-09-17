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

const GoldenDir = "testdata"

var updateGolden = flag.Bool("update", false,
	"rewrite the golden files this run compares against, instead of comparing")

func Update() bool { return *updateGolden }

func GoldenPath(name string) string { return filepath.Join(GoldenDir, name) }

func Golden(t testing.TB, name string, got []byte) {
	t.Helper()
	goldenAt(t, GoldenPath(name), got, Update())
}

func goldenAt(t testing.TB, path string, got []byte, update bool) {
	t.Helper()
	err := CompareGolden(path, got, update)
	switch {
	case err == nil:
		if update {
			t.Logf("testkit: rewrote %s (%d bytes); read the diff before committing it", path, len(got))
		}
	case errors.Is(err, os.ErrNotExist):
		t.Fatalf("%v", err)
	default:
		t.Errorf("%v", err)
	}
}

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
	return fmt.Errorf("the bytes do not match the golden file %s (-want +got):\n%s\n"+
		"Regenerate with `mise run golden-update` once the diff above is what you meant",
		path, cmp.Diff(string(want), string(got)))
}
