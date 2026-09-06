// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/glob"
)

func BenchmarkCreateRepository(b *testing.B) {
	root := os.Getenv("GO_MUTANTS_BENCH_ROOT")
	if root == "" {
		b.Skip("GO_MUTANTS_BENCH_ROOT is empty")
	}
	parent := b.TempDir()
	options := Options{
		DestParent: parent,
		ReportDir:  ".goatest",
		Exclude: []glob.Pattern{
			glob.MustCompile("reports"),
			glob.MustCompile("dist"),
		},
	}
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
	root := os.Getenv("GO_MUTANTS_BENCH_ROOT")
	if root == "" {
		b.Skip("GO_MUTANTS_BENCH_ROOT is empty")
	}
	patterns, err := exclusions(Options{
		ReportDir: ".goatest",
		Exclude: []glob.Pattern{
			glob.MustCompile("reports"),
			glob.MustCompile("dist"),
		},
	})
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
			parent := b.TempDir()
			for range b.N {
				b.StopTimer()
				destination, err := os.MkdirTemp(parent, "copy-")
				if err != nil {
					b.Fatal(err)
				}
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
