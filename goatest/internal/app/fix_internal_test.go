// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/config"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/provider"
	"github.com/P4suta/go-mutants/goatest/internal/repair"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const (
	diffTextLimit      = 32 << 10
	fixCandidateIDOne  = "0123456789abcdef"
	fixCandidateIDTwo  = "fedcba9876543210"
	binaryChangeLength = 2
)

func fixCandidateRecord(id, path string) repair.CandidateRecord {
	return repair.CandidateRecord{
		ID: id, Snapshot: "snapshot-a",
		Finding: report.Finding{ID: "finding-" + id, Kind: "surviving-mutant", Summary: "survived"},
		Candidate: provider.Candidate{
			Kind: "patch", Path: path, Content: []byte("package repaired\n"),
		},
	}
}

func TestSelectedCandidateRecordsReadWhatWasNamedOrEverythingStored(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, id := range []string{fixCandidateIDOne, fixCandidateIDTwo} {
		if _, err := repair.StoreCandidate(root, fixCandidateRecord(id, "generated_test.go")); err != nil {
			t.Fatal(err)
		}
	}

	everything, err := selectedCandidateRecords(root, nil)
	if err != nil || len(everything) != binaryChangeLength {
		t.Fatalf("selecting nothing read %d records (%v), want every one stored", len(everything), err)
	}
	one, err := selectedCandidateRecords(root, []string{fixCandidateIDTwo})
	if err != nil || len(one) != 1 || one[0].ID != fixCandidateIDTwo {
		t.Fatalf("selecting one read %+v (%v), want the record it named", one, err)
	}
	if _, err := selectedCandidateRecords(root, []string{fixCandidateIDOne, fixCandidateIDOne}); err == nil ||
		!strings.Contains(err.Error(), "duplicate repair candidate ID") {
		t.Fatalf("selecting one candidate twice reported %v, want it refused", err)
	}
	if _, err := selectedCandidateRecords(root, []string{"0000000000000000"}); err == nil {
		t.Fatal("selecting a candidate nobody stored was accepted")
	}
}

func TestACandidateDiffSaysWhatItCanAndWhyItCannot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "same_test.go"),
		[]byte("package repaired\n"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "changed_test.go"),
		[]byte("package fixture\n"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary_test.go"),
		[]byte{'a', 0, 'b'}, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "leading_test.go"),
		[]byte{0, 'a'}, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name      string
		candidate provider.Candidate
		want      string
		empty     bool
	}{
		{
			name:      "a path no repair may touch",
			candidate: provider.Candidate{Kind: "patch", Path: "main.go", Content: []byte("package main\n")},
			want:      "diff unavailable",
		},
		{
			name:      "a file the candidate already matches",
			candidate: provider.Candidate{Kind: "patch", Path: "same_test.go", Content: []byte("package repaired\n")},
			empty:     true,
		},
		{
			name:      "a file the candidate changes",
			candidate: provider.Candidate{Kind: "patch", Path: "changed_test.go", Content: []byte("package repaired\n")},
			want:      "--- a/changed_test.go",
		},
		{
			name:      "a file that is not text",
			candidate: provider.Candidate{Kind: "patch", Path: "binary_test.go", Content: []byte("package repaired\n")},
			want:      "binary change:",
		},
		{
			name:      "a candidate that is not text",
			candidate: provider.Candidate{Kind: "patch", Path: "changed_test.go", Content: []byte{'a', 0}},
			want:      "binary change:",
		},
		{
			name: "a candidate that is not valid UTF-8",
			candidate: provider.Candidate{
				Kind: "patch", Path: "changed_test.go", Content: []byte{0xff, 0xfe},
			},
			want: "binary change:",
		},
		{
			name:      "a file whose very first byte is not text",
			candidate: provider.Candidate{Kind: "patch", Path: "leading_test.go", Content: []byte("package repaired\n")},
			want:      "binary change:",
		},
		{
			name: "a candidate whose very first byte is not text",
			candidate: provider.Candidate{
				Kind: "patch", Path: "changed_test.go", Content: []byte{0, 'a'},
			},
			want: "binary change:",
		},
		{
			name: "a file that is not there",
			candidate: provider.Candidate{
				Kind: "patch", Path: "absent_test.go", Content: []byte("package repaired\n"),
			},
			want: "--- a/absent_test.go",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := candidateDiff(root, test.candidate)
			if test.empty {
				if got != "" {
					t.Fatalf("a candidate the file already matches rendered %q, want nothing", got)
				}
				return
			}
			if !strings.Contains(got, test.want) {
				t.Fatalf("candidateDiff rendered %q, want it to hold %q", got, test.want)
			}
		})
	}
}

func TestABoundedDiffTextStopsAtItsLimitAndSaysSo(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		data      []byte
		truncated bool
		want      string
	}{
		{name: "nothing at all", want: ""},
		{name: "one line", data: []byte("one\n"), want: "one"},
		{name: "one line with no ending", data: []byte("one"), want: "one"},
		{
			name: "exactly the limit", data: []byte(strings.Repeat("a", diffTextLimit)),
			want: strings.Repeat("a", diffTextLimit),
		},
		{
			name: "one byte past the limit", data: []byte(strings.Repeat("a", diffTextLimit+1)),
			truncated: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := boundedDiffText(test.data)
			if test.truncated {
				if !strings.HasSuffix(got, "[goatest: diff truncated]") {
					t.Fatalf("a text past the limit rendered %d bytes without saying it was cut", len(got))
				}
				return
			}
			if got != test.want {
				t.Fatalf("boundedDiffText rendered %q, want %q", got, test.want)
			}
		})
	}
}

func TestMergingAFixEnvironmentKeepsTheLastValueOfEachNameWhateverItsCase(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		base    []string
		overlay []string
		want    []string
	}{
		{name: "nothing at all", want: []string{}},
		{name: "one name", base: []string{"PATH=/bin"}, want: []string{"PATH=/bin"}},
		{
			name: "a name the overlay replaces",
			base: []string{"PATH=/bin"}, overlay: []string{"PATH=/usr/bin"}, want: []string{"PATH=/usr/bin"},
		},
		{
			name: "a name the overlay replaces in another case",
			base: []string{"Path=/bin"}, overlay: []string{"PATH=/usr/bin"}, want: []string{"PATH=/usr/bin"},
		},
		{
			name: "an entry with no equals sign",
			base: []string{"PATH=/bin", "NOTANENTRY"}, want: []string{"PATH=/bin"},
		},
		{
			name: "an entry with no name",
			base: []string{"PATH=/bin", "=value"}, want: []string{"PATH=/bin"},
		},
		{
			name: "two names, answered in order",
			base: []string{"ZONE=utc", "ALPHA=one"}, want: []string{"ALPHA=one", "ZONE=utc"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := mergeFixEnvironment(test.base, test.overlay); !slices.Equal(got, test.want) {
				t.Fatalf("mergeFixEnvironment = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAFixEnvironmentWithoutResourcesIsTheBaseItWasGiven(t *testing.T) {
	t.Parallel()
	environment, release, err := fixEnvironment(t.Context(), config.Config{}, []string{"PATH=/bin"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	if !slices.Equal(environment, []string{"PATH=/bin"}) {
		t.Fatalf("a fix with no resource runs with %q, want the base it was given", environment)
	}
}

func TestAFixEnvironmentWithNoBaseTakesTheOneThisProcessHas(t *testing.T) {
	t.Parallel()
	environment, release, err := fixEnvironment(t.Context(), config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	if len(environment) == 0 {
		t.Fatal("a fix given no base at all runs with nothing; it should take this process's own")
	}
	if !slices.Equal(environment, mergeFixEnvironment(os.Environ(), nil)) {
		t.Fatalf("a fix given no base runs with %d entries, want this process's own", len(environment))
	}
}

func TestAFixEnvironmentReportsAResourceItCannotStart(t *testing.T) {
	t.Parallel()
	loaded := config.Config{Resources: map[string]config.Resource{
		"absent": {Command: []string{filepath.Join(t.TempDir(), "absent")}},
	}}
	environment, release, err := fixEnvironment(t.Context(), loaded, []string{"PATH=/bin"})
	if err == nil {
		t.Fatal("a resource nobody can start was accepted")
	}
	if environment != nil {
		t.Errorf("a fix that could not start answered with %q, want no environment", environment)
	}
	if release == nil {
		t.Fatal("a fix that could not start answered with no release, want one that does nothing")
	}
	if releaseErr := release(); releaseErr != nil {
		t.Errorf("releasing what was never acquired reported %v, want none", releaseErr)
	}
}
