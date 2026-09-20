// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const twoLimitations = 2

func selectionReport() report.Report {
	return report.Report{
		Findings: []report.Finding{
			{ID: "finding-a", Kind: "surviving-mutant", Summary: "one survived"},
			{ID: "finding-b", Kind: "surviving-mutant", Summary: "another survived"},
		},
		Repairs: []report.Repair{
			{ID: "repair-a", Finding: "finding-a"},
			{ID: "repair-b", Finding: "finding-b"},
		},
	}
}

func TestSelectingAReplayFindingKeepsOnlyItOrNothingAtAll(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		id       string
		findings int
		repairs  int
	}{
		{name: "no finding asked for", findings: 2, repairs: 2},
		{name: "a finding the run reproduced", id: "finding-a", findings: 1, repairs: 1},
		{name: "a finding the run did not reproduce", id: "finding-gone"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			selected := selectReplayFinding(selectionReport(), test.id)
			if len(selected.Findings) != test.findings {
				t.Fatalf("%s kept %d findings, want %d: %+v", test.name, len(selected.Findings),
					test.findings, selected.Findings)
			}
			if len(selected.Repairs) != test.repairs {
				t.Fatalf("%s kept %d repairs, want %d: %+v", test.name, len(selected.Repairs),
					test.repairs, selected.Repairs)
			}
			if test.findings == 1 && selected.Findings[0].ID != test.id {
				t.Errorf("%s kept %q, want %q", test.name, selected.Findings[0].ID, test.id)
			}
		})
	}
}

func TestALimitationIsStatedOnceHoweverOftenItIsReached(t *testing.T) {
	t.Parallel()
	first := report.Limitation{Code: "code-a", Summary: "the first"}
	same := report.Limitation{Code: "code-a", Summary: "the first"}
	otherSummary := report.Limitation{Code: "code-a", Summary: "another summary"}
	otherCode := report.Limitation{Code: "code-b", Summary: "the first"}

	stated := appendLimitation(nil, first)
	if len(stated) != 1 {
		t.Fatalf("the first limitation was stated %d times", len(stated))
	}
	if again := appendLimitation(stated, same); len(again) != 1 {
		t.Fatalf("the same limitation was stated twice: %+v", again)
	}
	if bySummary := appendLimitation(stated, otherSummary); len(bySummary) != twoLimitations {
		t.Errorf("a limitation of the same code and another summary was folded away: %+v", bySummary)
	}
	if byCode := appendLimitation(stated, otherCode); len(byCode) != twoLimitations {
		t.Errorf("a limitation of another code and the same summary was folded away: %+v", byCode)
	}
}

func TestABuildCacheDirectoryFallsBackOnlyWhereTheConfigurationCanBeRead(t *testing.T) {
	t.Parallel()
	userCache := t.TempDir()
	for _, test := range []struct {
		name      string
		config    string
		userCache func() (string, error)
		answers   bool
	}{
		{name: "a configuration that names no build directory", config: "version = 1\ncontract = \"standard-v1\"\n"},
		{
			name:   "a configuration that names one",
			config: "version = 1\ncontract = \"standard-v1\"\n[cache]\nbuild_dir = \"build\"\n", answers: true,
		},
		{name: "a configuration nothing can read", config: "version = \"one\"\n"},
		{
			name:      "a configuration it can read beside a user cache",
			config:    "version = 1\ncontract = \"standard-v1\"\n",
			userCache: func() (string, error) { return userCache, nil },
			answers:   true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
				[]byte(test.config), filemode.PrivateFile); err != nil {
				t.Fatal(err)
			}
			service := Service{Root: root, UserCacheDir: test.userCache}
			if answered := service.buildCacheDirectory(root) != ""; answered != test.answers {
				t.Fatalf("%s answered=%t, want %t", test.name, answered, test.answers)
			}
		})
	}
}
