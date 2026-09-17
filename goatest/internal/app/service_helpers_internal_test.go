// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/cli"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const (
	limitedBufferRoom    = 8
	limitedBufferOverrun = 12
)

func TestASafeTraceNameIsATimestampAndAProcessAndNothingElse(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: "20260101T000000Z-4242", want: true},
		{name: "20260101T000000Z-1", want: true},
		{name: ""},
		{name: "."},
		{name: ".."},
		{name: "nested/20260101T000000Z-4242"},
		{name: `nested\20260101T000000Z-4242`},
		{name: "20260101T000000Z"},
		{name: "whenever-4242"},
		{name: "20260101T000000Z-"},
		{name: "20260101T000000Z-zero"},
		{name: "20260101T000000Z-0"},
		{name: "20260101T000000Z--1"},
		{name: "20260101T000000Z-04242"},
		{name: "20260101T000000Z-4242-extra"},
	} {
		t.Run("name "+test.name, func(t *testing.T) {
			t.Parallel()
			if got := safeTraceName(test.name); got != test.want {
				t.Fatalf("safeTraceName(%q) = %t, want %t", test.name, got, test.want)
			}
		})
	}
}

func TestALimitedBufferKeepsWhatFitsAndSaysWhatItDropped(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		limit     int
		writes    []string
		want      string
		truncated bool
	}{
		{name: "one write that fits", limit: limitedBufferRoom, writes: []string{"12345"}, want: "12345"},
		{
			name: "one write that exactly fills it", limit: limitedBufferRoom,
			writes: []string{"12345678"}, want: "12345678",
		},
		{
			name: "one write past the limit", limit: limitedBufferRoom,
			writes: []string{"123456789"}, want: "12345678", truncated: true,
		},
		{
			name: "two writes that cross the limit", limit: limitedBufferRoom,
			writes: []string{"12345", "6789"}, want: "12345678", truncated: true,
		},
		{
			name: "a write after the limit was reached", limit: limitedBufferRoom,
			writes: []string{"123456789", "more"}, want: "12345678", truncated: true,
		},
		{
			name: "a buffer that names no limit", limit: 0,
			writes: []string{"12345"}, want: "12345",
		},
		{
			name: "a buffer whose limit is below zero", limit: -1,
			writes: []string{"12345"}, want: "12345",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			buffer := limitedDoctorBuffer{limit: test.limit}
			for _, write := range test.writes {
				count, err := buffer.Write([]byte(write))
				if err != nil || count != len(write) {
					t.Fatalf("writing %q reported (%d, %v), want (%d, nil)", write, count, err, len(write))
				}
			}
			got := buffer.String()
			want := test.want
			if test.truncated {
				want += "\n" + doctorTruncationNotice
			}
			if got != want {
				t.Fatalf("the buffer holds %q, want %q", got, want)
			}
		})
	}
}

func TestDoctorReasonTallyOrdersByCountAndThenByName(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		reasons map[string]int
		want    string
	}{
		{name: "no reason at all", want: "no reason recorded"},
		{name: "one reason", reasons: map[string]int{"cgo": 1}, want: "cgo 1"},
		{
			name:    "two reasons of different counts",
			reasons: map[string]int{"cgo": 1, "dot import": limitedBufferOverrun},
			want:    "dot import 12, cgo 1",
		},
		{
			name:    "two reasons of one count",
			reasons: map[string]int{"dot import": 1, "cgo": 1},
			want:    "cgo 1, dot import 1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := doctorReasonTally(test.reasons); got != test.want {
				t.Fatalf("doctorReasonTally = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDoctorNameSampleNamesAFewAndCountsTheRest(t *testing.T) {
	t.Parallel()
	names := []string{"a", "b", "c", "d", "e", "f", "g"}
	for _, test := range []struct {
		name  string
		names []string
		want  []string
	}{
		{name: "fewer than the sample size", names: names[:2], want: []string{"a", "b"}},
		{name: "exactly the sample size", names: names[:doctorNameSampleSize], want: names[:doctorNameSampleSize]},
		{
			name: "one more than the sample size", names: names[:doctorNameSampleSize+1],
			want: append(slices.Clone(names[:doctorNameSampleSize]), "and 1 more"),
		},
		{
			name: "two more than the sample size", names: names,
			want: append(slices.Clone(names[:doctorNameSampleSize]), "and 2 more"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := doctorNameSample(test.names); !slices.Equal(got, test.want) {
				t.Fatalf("doctorNameSample = %q, want %q", got, test.want)
			}
		})
	}
}

func scriptedGit(answers map[string]string, failures map[string]error) GitOutput {
	return func(_ context.Context, _ string, arguments ...string) ([]byte, error) {
		if err, refused := failures[arguments[0]]; refused {
			return nil, err
		}
		return []byte(answers[arguments[0]]), nil
	}
}

func TestInspectGitReadsTheIdentityAndTheChangeItCanSee(t *testing.T) {
	t.Parallel()
	answers := map[string]string{
		"rev-parse":  "commit-a\n",
		"status":     "",
		"merge-base": "commit-base\n",
		"diff":       "changed.go\x00",
		"ls-files":   "added.go\x00",
	}
	for _, test := range []struct {
		name     string
		change   func(map[string]string)
		request  cli.Request
		failures map[string]error
		want     report.Git
		refuses  string
	}{
		{
			name: "a clean work tree",
			want: report.Git{
				Available: true, Commit: "commit-a", MergeBase: "commit-base",
				ChangedFiles: []string{"added.go", "changed.go"},
			},
		},
		{
			name:   "a work tree somebody has edited",
			change: func(a map[string]string) { a["status"] = " M changed.go\x00" },
			want: report.Git{
				Available: true, Commit: "commit-a", MergeBase: "commit-base", Dirty: true,
				ChangedFiles: []string{"added.go", "changed.go"},
			},
		},
		{
			name:    "a reference the caller named",
			request: cli.Request{ChangedRef: "main"},
			want: report.Git{
				Available: true, Commit: "commit-a", MergeBase: "commit-base",
				ChangedFiles: []string{"added.go", "changed.go"},
			},
		},
		{
			name:     "a merge base against HEAD that git cannot find",
			failures: map[string]error{"merge-base": errors.New("no merge base")},
			want: report.Git{
				Available: true, Commit: "commit-a", MergeBase: "commit-a",
				ChangedFiles: []string{"added.go", "changed.go"},
			},
		},
		{
			name:     "a merge base against another reference that git cannot find",
			request:  cli.Request{ChangedRef: "main"},
			failures: map[string]error{"merge-base": errors.New("no merge base")},
			refuses:  "no merge base",
		},
		{
			name:     "a commit git will not name",
			failures: map[string]error{"rev-parse": errors.New("no HEAD")}, refuses: "no HEAD",
		},
		{
			name:     "a status git will not give",
			failures: map[string]error{"status": errors.New("no status")}, refuses: "no status",
		},
		{
			name:     "a diff git will not produce",
			failures: map[string]error{"diff": errors.New("no diff")}, refuses: "no diff",
		},
		{
			name:     "a listing git will not produce",
			failures: map[string]error{"ls-files": errors.New("no listing")}, refuses: "no listing",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scripted := make(map[string]string, len(answers))
			for key, value := range answers {
				scripted[key] = value
			}
			if test.change != nil {
				test.change(scripted)
			}
			metadata, err := inspectGit(t.Context(), ".", test.request, scriptedGit(scripted, test.failures))
			if test.refuses != "" {
				if err == nil || !strings.Contains(err.Error(), test.refuses) {
					t.Fatalf("inspectGit reported %v, want it to say %q", err, test.refuses)
				}
				if metadata.Available {
					t.Errorf("a refused inspection answered with %+v, want no identity", metadata)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if metadata.Available != test.want.Available || metadata.Commit != test.want.Commit ||
				metadata.MergeBase != test.want.MergeBase || metadata.Dirty != test.want.Dirty ||
				!slices.Equal(metadata.ChangedFiles, test.want.ChangedFiles) {
				t.Fatalf("inspectGit = %+v, want %+v", metadata, test.want)
			}
		})
	}
}

func TestARequestedScopeNamesTheReferenceAChangesetComparesAgainst(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		request cli.Request
		kind    report.RunKind
		wantRef string
	}{
		{name: "a changeset that names no reference", kind: report.RunChangeset, wantRef: "HEAD"},
		{
			name: "a changeset that names one", kind: report.RunChangeset,
			request: cli.Request{ChangedRef: "main"}, wantRef: "main",
		},
		{name: "a full run", kind: report.RunFull},
		{
			name: "a full run that named a reference anyway", kind: report.RunFull,
			request: cli.Request{ChangedRef: "main"}, wantRef: "main",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scope := requestedScope(test.request, test.kind)
			if scope.Kind != string(test.kind) || scope.Project != "." || scope.Ref != test.wantRef {
				t.Fatalf("requestedScope = %+v, want kind %q and reference %q", scope, test.kind, test.wantRef)
			}
		})
	}
}

func TestAScopedVerdictSaysWhatTheRunActuallyAssured(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		verdict  report.Verdict
		kind     report.RunKind
		resolved string
		findings int
		want     report.Verdict
	}{
		{
			name: "a replay that reproduced nothing", verdict: report.VerdictAssured,
			kind: report.RunReplay, want: report.VerdictResolved,
		},
		{
			name: "a replay that reproduced the finding", verdict: report.VerdictAssured,
			kind: report.RunReplay, findings: 1, want: report.VerdictReproduced,
		},
		{
			name: "a replay that could not run", verdict: report.VerdictError,
			kind: report.RunReplay, want: report.VerdictError,
		},
		{
			name: "a replay that could not run and found something", verdict: report.VerdictError,
			kind: report.RunReplay, findings: 1, want: report.VerdictError,
		},
		{
			name: "an assurance of the whole project", verdict: report.VerdictAssured,
			kind: report.RunFull, resolved: string(report.RunFull), want: report.VerdictAssured,
		},
		{
			name: "an assurance narrowed to a changeset", verdict: report.VerdictAssured,
			kind: report.RunChangeset, resolved: string(report.RunChangeset), want: report.VerdictChangeAssured,
		},
		{
			name: "an assurance narrowed to a package", verdict: report.VerdictAssured,
			kind: report.RunPackage, resolved: string(report.RunPackage), want: report.VerdictScopeAssured,
		},
		{
			name: "a changeset that resolved to the whole project", verdict: report.VerdictAssured,
			kind: report.RunChangeset, resolved: string(report.RunFull), want: report.VerdictAssured,
		},
		{
			name: "an operation that assured nothing", verdict: report.VerdictAssured,
			kind: report.RunOperation, resolved: string(report.RunOperation), want: report.VerdictAssured,
		},
		{
			name: "a defect anywhere", verdict: report.VerdictDefect,
			kind: report.RunFull, resolved: string(report.RunFull), want: report.VerdictDefect,
		},
		{
			name: "insufficient evidence anywhere", verdict: report.VerdictInsufficient,
			kind: report.RunPackage, resolved: string(report.RunPackage), want: report.VerdictInsufficient,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := scopedVerdict(test.verdict, test.kind, test.resolved, test.findings)
			if got != test.want {
				t.Fatalf("scopedVerdict = %q, want %q", got, test.want)
			}
		})
	}
}
