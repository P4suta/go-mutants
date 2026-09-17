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

const benchRootEnv = "GO_MUTANTS_BENCH_ROOT"

func benchRoot(b *testing.B) string {
	b.Helper()
	if root := os.Getenv(benchRootEnv); root != "" {
		return root
	}
	return testkit.Copy(b, "families")
}

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
