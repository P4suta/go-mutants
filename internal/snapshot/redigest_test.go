// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

func redigest(t *testing.T, snap *Snapshot) []Drift {
	t.Helper()
	drifts, err := snap.Redigest()
	if err != nil {
		t.Fatalf("Redigest: %v", err)
	}
	return drifts
}

func TestRedigestFindsNoDriftInAFreshSnapshot(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, goldenFiles)
	mkdirAll(t, src, "empty")

	snap := create(t, src, Options{})
	if drifts := redigest(t, snap); len(drifts) != 0 {
		t.Errorf("Redigest = %v, want no drift", drifts)
	}
}

func TestRedigestDetectsDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(t *testing.T, root string)
		want   []Drift
	}{
		{
			name: "changed contents, same length",
			mutate: func(t *testing.T, root string) {
				overwrite(t, filepath.Join(root, "a", "one.go"), "package a\n\nvar N = 2\n")
			},
			want: []Drift{{
				Kind:       DriftChanged,
				RelPath:    "a/one.go",
				WantSize:   21,
				WantSHA256: digestOf("package a\n\nvar N = 1\n"),
				GotSize:    21,
				GotSHA256:  digestOf("package a\n\nvar N = 2\n"),
			}},
		},
		{
			name: "truncated to nothing",
			mutate: func(t *testing.T, root string) {
				overwrite(t, filepath.Join(root, "a", "one.go"), "")
			},
			want: []Drift{{
				Kind:       DriftChanged,
				RelPath:    "a/one.go",
				WantSize:   21,
				WantSHA256: digestOf("package a\n\nvar N = 1\n"),
				GotSize:    0,
				GotSHA256:  digestOf(""),
			}},
		},
		{
			name: "a test wrote a golden file",
			mutate: func(t *testing.T, root string) {
				overwrite(t, filepath.Join(root, "a", "testdata", "golden.txt"), "regenerated\n")
			},
			want: []Drift{{
				Kind:      DriftAdded,
				RelPath:   "a/testdata/golden.txt",
				GotSize:   12,
				GotSHA256: digestOf("regenerated\n"),
			}},
		},
		{
			name: "a test deleted a file",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "b", "two.go")); err != nil {
					t.Fatalf("Remove: %v", err)
				}
			},
			want: []Drift{{
				Kind:       DriftRemoved,
				RelPath:    "b/two.go",
				WantSize:   11,
				WantSHA256: digestOf("package b\n\n"),
			}},
		},
		{
			name: "something appeared in an excluded path",
			mutate: func(t *testing.T, root string) {
				overwrite(t, filepath.Join(root, "reports", "mutation", "report.json"), "{}\n")
			},
			want: []Drift{{
				Kind:      DriftAdded,
				RelPath:   "reports/mutation/report.json",
				GotSize:   3,
				GotSHA256: digestOf("{}\n"),
			}},
		},
		{
			name: "everything at once",
			mutate: func(t *testing.T, root string) {
				overwrite(t, filepath.Join(root, "a", "one.go"), "package a\n")
				overwrite(t, filepath.Join(root, "a", "added.go"), "package a\n")
				if err := os.Remove(filepath.Join(root, "b", "two.go")); err != nil {
					t.Fatalf("Remove: %v", err)
				}
			},
			want: []Drift{
				{
					Kind:      DriftAdded,
					RelPath:   "a/added.go",
					GotSize:   10,
					GotSHA256: digestOf("package a\n"),
				},
				{
					Kind:       DriftChanged,
					RelPath:    "a/one.go",
					WantSize:   21,
					WantSHA256: digestOf("package a\n\nvar N = 1\n"),
					GotSize:    10,
					GotSHA256:  digestOf("package a\n"),
				},
				{
					Kind:       DriftRemoved,
					RelPath:    "b/two.go",
					WantSize:   11,
					WantSHA256: digestOf("package b\n\n"),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := t.TempDir()
			writeTree(t, src, map[string]string{
				"a/one.go": "package a\n\nvar N = 1\n",
				"b/two.go": "package b\n\n",
			})
			snap := create(t, src, Options{})
			tt.mutate(t, snap.Root)

			got := redigest(t, snap)
			if len(got) != len(tt.want) {
				t.Fatalf("Redigest returned %d drifts, want %d:\n got: %+v\nwant: %+v", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("drift[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
			if got := readFile(t, filepath.Join(src, "a", "one.go")); got != "package a\n\nvar N = 1\n" {
				t.Errorf("the source tree changed: %q", got)
			}
		})
	}
}

func TestRedigestRejectsSymlink(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a/one.go": "package a\n"})
	snap := create(t, src, Options{})
	symlinkOrSkip(t, filepath.Join(snap.Root, "a", "one.go"), filepath.Join(snap.Root, "a", "link.go"))

	drifts, err := snap.Redigest()
	assertCode(t, err, CodeSymlink)
	if drifts != nil {
		t.Errorf("Redigest returned drift alongside the error: %v", drifts)
	}
}

func TestRedigestIgnoresEmptyDirectories(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a/one.go": "package a\n"})
	snap := create(t, src, Options{})
	mkdirAll(t, snap.Root, "brand/new/dir")

	if drifts := redigest(t, snap); len(drifts) != 0 {
		t.Errorf("Redigest = %v, want no drift for an empty directory", drifts)
	}
}

func TestRedigestNamesTheRootThatIsGone(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a/one.go": "package a\n"})
	snap := create(t, src, Options{})
	root := snap.Root
	if err := snap.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	drifts, err := snap.Redigest()
	assertCode(t, err, CodeWalk)
	if drifts != nil {
		t.Errorf("Redigest returned drift alongside the error: %v", drifts)
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error is not a *Error: %v", err)
	}
	if e.Path != root {
		t.Errorf("Error.Path = %q, want the absolute snapshot root %q", e.Path, root)
	}
	if !filepath.IsAbs(e.Path) {
		t.Errorf("Error.Path = %q, want an absolute path", e.Path)
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("the rendered error does not name the root: %v", err)
	}
}

func TestDriftKindStrings(t *testing.T) {
	t.Parallel()

	tests := map[DriftKind]string{
		DriftAdded:   "added",
		DriftRemoved: "removed",
		DriftChanged: "changed",
		DriftKind(0): "unknown",
	}
	for kind, want := range tests {
		if got := kind.String(); got != want {
			t.Errorf("DriftKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

func overwrite(t *testing.T, abs, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", abs, err)
	}
}

func digestOf(content string) string { return mutation.DigestString(content) }
