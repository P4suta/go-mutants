// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/checkpoint"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

func journalTarget(id string) checkpoint.BaselineTarget {
	return checkpoint.BaselineTarget{
		ID: id, Executed: true,
		Inventory: report.TargetDisposition{
			ID: id, Name: "Test" + id, Kind: "test", Package: "example.test/fixture", Status: "passed",
		},
	}
}

func journalSuite(pkg string) checkpoint.BaselineSuite {
	return checkpoint.BaselineSuite{Package: pkg}
}

func journalBaseline(change func(*checkpoint.Baseline)) checkpoint.Baseline {
	baseline := checkpoint.Baseline{
		BuildVetComplete: true,
		Evidence:         []report.Evidence{{Kind: "baseline", ID: "e1", Status: "passed"}},
		Findings:         []report.Finding{},
		Targets:          []checkpoint.BaselineTarget{journalTarget("t1")},
		Suites:           []checkpoint.BaselineSuite{journalSuite("example.test/one")},
	}
	if change != nil {
		change(&baseline)
	}
	return baseline
}

func TestABaselineTargetJournalCarriesOnlyWhatWasAppended(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		previous func(*checkpoint.Baseline)
		next     func(*checkpoint.Baseline)
		want     []string
		appended bool
	}{
		{
			name: "one target appended to what was there",
			next: func(b *checkpoint.Baseline) { b.Targets = append(b.Targets, journalTarget("t2")) },
			want: []string{"t2"}, appended: true,
		},
		{
			name: "two targets appended out of order",
			next: func(b *checkpoint.Baseline) {
				b.Targets = append(b.Targets, journalTarget("t3"), journalTarget("t2"))
			},
			want: []string{"t2", "t3"}, appended: true,
		},
		{name: "nothing appended at all"},
		{
			name: "a target removed rather than appended",
			next: func(b *checkpoint.Baseline) { b.Targets = nil },
		},
		{
			name: "a target rewritten while another was appended",
			next: func(b *checkpoint.Baseline) {
				b.Targets[0].Executed, b.Targets[0].Skipped = false, true
				b.Targets = append(b.Targets, journalTarget("t2"))
			},
		},
		{
			name: "a target the previous checkpoint named twice",
			previous: func(b *checkpoint.Baseline) {
				b.Targets = append(b.Targets, journalTarget("t1"))
			},
			next: func(b *checkpoint.Baseline) {
				b.Targets = append(b.Targets, journalTarget("t1"), journalTarget("t2"))
			},
		},
		{
			name: "a target the next checkpoint names twice",
			next: func(b *checkpoint.Baseline) {
				b.Targets = append(b.Targets, journalTarget("t2"), journalTarget("t2"))
			},
		},
		{
			name:     "a build and vet pass the previous checkpoint had not finished",
			previous: func(b *checkpoint.Baseline) { b.BuildVetComplete = false },
			next:     func(b *checkpoint.Baseline) { b.Targets = append(b.Targets, journalTarget("t2")) },
		},
		{
			name: "a build and vet pass the next checkpoint had not finished",
			next: func(b *checkpoint.Baseline) {
				b.BuildVetComplete = false
				b.Targets = append(b.Targets, journalTarget("t2"))
			},
		},
		{
			name:     "a previous checkpoint that was already complete",
			previous: func(b *checkpoint.Baseline) { b.Complete = true },
			next:     func(b *checkpoint.Baseline) { b.Targets = append(b.Targets, journalTarget("t2")) },
		},
		{
			name: "a next checkpoint that completed the baseline",
			next: func(b *checkpoint.Baseline) {
				b.Complete = true
				b.Targets = append(b.Targets, journalTarget("t2"))
			},
		},
		{
			name: "evidence that changed beside the appended target",
			next: func(b *checkpoint.Baseline) {
				b.Evidence = append(b.Evidence, report.Evidence{Kind: "baseline", ID: "e2", Status: "passed"})
				b.Targets = append(b.Targets, journalTarget("t2"))
			},
		},
		{
			name: "findings that changed beside the appended target",
			next: func(b *checkpoint.Baseline) {
				b.Findings = append(b.Findings, report.Finding{ID: "f1", Kind: "coverage", Summary: "unreached"})
				b.Targets = append(b.Targets, journalTarget("t2"))
			},
		},
		{
			name: "suites that changed beside the appended target",
			next: func(b *checkpoint.Baseline) {
				b.Suites = append(b.Suites, journalSuite("example.test/two"))
				b.Targets = append(b.Targets, journalTarget("t2"))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			previous := journalBaseline(test.previous)
			next := journalBaseline(test.previous)
			if test.next != nil {
				test.next(&next)
			}
			suffix, appended := baselineCheckpointJournalSuffix(previous, next)
			if appended != test.appended {
				t.Fatalf("the journal said appended %t, want %t: %+v", appended, test.appended, suffix)
			}
			if !appended {
				if suffix != nil {
					t.Errorf("a journal it refused answered with %+v, want nothing at all", suffix)
				}
				return
			}
			got := make([]string, 0, len(suffix))
			for _, unit := range suffix {
				got = append(got, unit.ID)
			}
			if len(got) != len(test.want) {
				t.Fatalf("the journal carries %q, want %q", got, test.want)
			}
			for index, want := range test.want {
				if got[index] != want {
					t.Fatalf("the journal carries %q, want %q", got, test.want)
				}
			}
		})
	}
}

func TestABaselineSuiteJournalCarriesOnlyWhatWasAppended(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		previous func(*checkpoint.Baseline)
		next     func(*checkpoint.Baseline)
		want     []string
		appended bool
	}{
		{
			name: "one suite appended to what was there",
			next: func(b *checkpoint.Baseline) { b.Suites = append(b.Suites, journalSuite("example.test/two")) },
			want: []string{"example.test/two"}, appended: true,
		},
		{
			name: "two suites appended out of order",
			next: func(b *checkpoint.Baseline) {
				b.Suites = append(b.Suites, journalSuite("example.test/three"), journalSuite("example.test/two"))
			},
			want: []string{"example.test/three", "example.test/two"}, appended: true,
		},
		{name: "nothing appended at all"},
		{
			name: "a suite removed rather than appended",
			next: func(b *checkpoint.Baseline) { b.Suites = nil },
		},
		{
			name: "a suite rewritten while another was appended",
			next: func(b *checkpoint.Baseline) {
				b.Suites[0].DurationNS = 1
				b.Suites = append(b.Suites, journalSuite("example.test/two"))
			},
		},
		{
			name: "a suite the previous checkpoint named twice",
			previous: func(b *checkpoint.Baseline) {
				b.Suites = append(b.Suites, journalSuite("example.test/one"))
			},
			next: func(b *checkpoint.Baseline) {
				b.Suites = append(b.Suites, journalSuite("example.test/one"), journalSuite("example.test/two"))
			},
		},
		{
			name: "a suite the next checkpoint names twice",
			next: func(b *checkpoint.Baseline) {
				b.Suites = append(b.Suites, journalSuite("example.test/two"), journalSuite("example.test/two"))
			},
		},
		{
			name:     "a build and vet pass the previous checkpoint had not finished",
			previous: func(b *checkpoint.Baseline) { b.BuildVetComplete = false },
			next:     func(b *checkpoint.Baseline) { b.Suites = append(b.Suites, journalSuite("example.test/two")) },
		},
		{
			name: "a next checkpoint that completed the baseline",
			next: func(b *checkpoint.Baseline) {
				b.Complete = true
				b.Suites = append(b.Suites, journalSuite("example.test/two"))
			},
		},
		{
			name: "targets that changed beside the appended suite",
			next: func(b *checkpoint.Baseline) {
				b.Targets = append(b.Targets, journalTarget("t2"))
				b.Suites = append(b.Suites, journalSuite("example.test/two"))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			previous := journalBaseline(test.previous)
			next := journalBaseline(test.previous)
			if test.next != nil {
				test.next(&next)
			}
			suffix, appended := baselineSuiteCheckpointJournalSuffix(previous, next)
			if appended != test.appended {
				t.Fatalf("the journal said appended %t, want %t: %+v", appended, test.appended, suffix)
			}
			if !appended {
				if suffix != nil {
					t.Errorf("a journal it refused answered with %+v, want nothing at all", suffix)
				}
				return
			}
			got := make([]string, 0, len(suffix))
			for _, unit := range suffix {
				got = append(got, unit.Package)
			}
			if len(got) != len(test.want) {
				t.Fatalf("the journal carries %q, want %q", got, test.want)
			}
			for index, want := range test.want {
				if got[index] != want {
					t.Fatalf("the journal carries %q, want %q", got, test.want)
				}
			}
		})
	}
}
