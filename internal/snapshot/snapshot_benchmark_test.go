// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/glob"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// benchRootEnv points these benchmarks at a tree of the reader's own.
//
// A real checkout — go-mutants itself, or whatever workspace somebody is
// finding slow to snapshot — is far larger than anything in the corpus and is
// what the parallel copy was tuned against, so the variable stays. What changed
// is the answer when it is unset: it used to be a skip, which meant the only
// two benchmarks in this repository reported nothing on every run for which
// nobody had exported a path, which was every run. `mise run bench` and the
// nightly job now get a number by default, and a number that moves is the point
// of committing bench.txt to a job's artifacts.
const benchRootEnv = "GO_MUTANTS_BENCH_ROOT"

// benchRoot is the tree to copy: the named one, or a private copy of the corpus
// module with the most files in it.
//
// A copy rather than the corpus directory itself, and the same copy every other
// suite takes, because `git status --porcelain fixtures/` is a CI gate and a
// benchmark walking the checked-in tree would be reading a directory another
// suite in the same run may be writing into.
func benchRoot(b *testing.B) string {
	b.Helper()
	if root := os.Getenv(benchRootEnv); root != "" {
		return root
	}
	return testkit.Copy(b, "families")
}

// benchOptions are the exclusions a real run carries, so that the walk being
// measured is the walk that happens.
func benchOptions(parent string) Options {
	return Options{
		DestParent: parent,
		ReportDir:  ".goatest",
		Exclude: []glob.Pattern{
			glob.MustCompile("reports"),
			glob.MustCompile("dist"),
		},
	}
}

func BenchmarkCreateRepository(b *testing.B) {
	root := benchRoot(b)
	options := benchOptions(b.TempDir())
	b.ResetTimer()
	for range b.N {
		snapshot, err := Create(root, options)
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		if err := snapshot.Cleanup(); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

func BenchmarkCopyRepository(b *testing.B) {
	root := benchRoot(b)
	patterns, err := exclusions(benchOptions(""))
	if err != nil {
		b.Fatal(err)
	}
	walker := &walker{root: root, exclude: patterns}
	if err := walker.walk(""); err != nil {
		b.Fatal(err)
	}
	slices.SortFunc(walker.files, byRelPath)
	slices.SortFunc(walker.dirs, byRelPath)
	for _, benchmark := range []struct {
		name string
		jobs int
	}{
		{name: "serial", jobs: 1},
		{name: "automatic", jobs: snapshotCopyJobs(len(walker.files))},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			for range b.N {
				b.StopTimer()
				// b.TempDir() rather than os.MkdirTemp under a parent of our
				// own: it is a fresh directory per call just the same, and the
				// testing package removes it even when an iteration fails
				// before reaching the removal below.
				destination := b.TempDir()
				for _, directory := range walker.dirs {
					if err := os.MkdirAll(filepath.Join(destination, filepath.FromSlash(directory.rel)), dirPerm(directory.mode)); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
				_, _, copyErr := copySnapshotFiles(walker.files, destination, benchmark.jobs, copyFile)
				b.StopTimer()
				if copyErr != nil {
					b.Fatal(copyErr)
				}
				if err := os.RemoveAll(destination); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
		})
	}
}
