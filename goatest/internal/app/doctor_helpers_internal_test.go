// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"os"
	"slices"
	"testing"
)

const (
	tallyOfMany          = 3
	tallyOfSome          = 2
	oneMoreThanTheSample = doctorNameSampleSize + 1
)

func TestAReasonTallyReadsTheCommonestFirstAndBreaksATieByName(t *testing.T) {
	t.Parallel()
	if tally := doctorReasonTally(nil); tally != "no reason recorded" {
		t.Fatalf("a tally of nothing reads %q, want it said out loud", tally)
	}
	tally := doctorReasonTally(map[string]int{
		"directory-access": tallyOfSome, "outside-input": tallyOfMany, "log-ambiguous": tallyOfSome,
	})
	want := "outside-input 3, directory-access 2, log-ambiguous 2"
	if tally != want {
		t.Fatalf("the tally reads %q, want %q", tally, want)
	}
}

func TestANameSampleNamesEveryoneUpToItsSizeAndCountsTheRest(t *testing.T) {
	t.Parallel()
	names := make([]string, 0, oneMoreThanTheSample)
	for index := range oneMoreThanTheSample {
		names = append(names, string(rune('a'+index)))
	}
	whole := doctorNameSample(names[:doctorNameSampleSize])
	if !slices.Equal(whole, names[:doctorNameSampleSize]) {
		t.Fatalf("a sample of exactly the size reads %q, want every name", whole)
	}
	sampled := doctorNameSample(names)
	if len(sampled) != doctorNameSampleSize+1 {
		t.Fatalf("a sample of one more reads %q, want the size and a count", sampled)
	}
	if sampled[doctorNameSampleSize] != "and 1 more" {
		t.Fatalf("the sample ends with %q, want the count of what it left out", sampled[doctorNameSampleSize])
	}
	if slices.Equal(sampled[:doctorNameSampleSize], names[:doctorNameSampleSize]) == false {
		t.Errorf("the sample reads %q, want the first names it was given", sampled)
	}
}

func TestAnEnvironmentOverrideReplacesAnEntryWhateverItsCase(t *testing.T) {
	t.Parallel()
	result := withEnvironment(
		[]string{"goproxy=direct", "PATH=/bin", "=orphan", "novalue", "EMPTY="},
		map[string]string{"GOPROXY": "off"},
	)
	if !slices.Contains(result, "GOPROXY=off") {
		t.Fatalf("the environment reads %q, want the entry it already had overridden", result)
	}
	if slices.Contains(result, "goproxy=direct") {
		t.Errorf("the environment reads %q, want the entry the override replaced gone", result)
	}
	for _, unwanted := range []string{"=orphan", "novalue"} {
		if slices.Contains(result, unwanted) {
			t.Errorf("the environment reads %q, want %q left out", result, unwanted)
		}
	}
	if !slices.Contains(result, "EMPTY=") || !slices.Contains(result, "PATH=/bin") {
		t.Errorf("the environment reads %q, want every entry it could read", result)
	}
	if !slices.IsSorted(result) {
		t.Errorf("the environment reads %q, want it in an order that does not change between runs", result)
	}
	added := withEnvironment(nil, map[string]string{"GOWORK": "off"})
	if !slices.Equal(added, []string{"GOWORK=off"}) {
		t.Errorf("an override over nothing reads %q, want the override alone", added)
	}
}

func TestSplittingDoctorLinesAnswersTheCountItWasAskedFor(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		output string
		count  int
		want   []string
	}{
		{name: "as many lines as it asks for", output: "one\ntwo", count: 2, want: []string{"one", "two"}},
		{name: "fewer lines than it asks for", output: "one", count: 2, want: []string{"one", ""}},
		{name: "more lines than it asks for", output: "one\ntwo\nthree", count: 2, want: []string{"one", "two"}},
		{name: "lines that end the way Windows ends them", output: "one\r\ntwo", count: 2, want: []string{"one", "two"}},
		{name: "lines with room around them", output: "  one  \n  two  ", count: 2, want: []string{"one", "two"}},
		{name: "no output at all", count: 2, want: []string{"", ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := splitDoctorLines(test.output, test.count); !slices.Equal(got, test.want) {
				t.Fatalf("splitting %q read %q, want %q", test.output, got, test.want)
			}
		})
	}
}

func TestAnOptionalValueIsConfiguredOnlyWhereItNamesSomething(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "a path", value: "/fixture/go.work", want: "ready"},
		{name: "nothing at all", want: "not-configured"},
		{name: "the null device", value: os.DevNull, want: "not-configured"},
		{name: "the word off", value: "off", want: "not-configured"},
		{name: "the word off in capitals", value: "OFF", want: "not-configured"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := doctorOptionalStatus(test.value); got != test.want {
				t.Fatalf("a value of %q reads %q, want %q", test.value, got, test.want)
			}
		})
	}
	if doctorBooleanStatus(true) != "ready" || doctorBooleanStatus(false) != "disabled" {
		t.Fatalf("a boolean status reads %q and %q", doctorBooleanStatus(true), doctorBooleanStatus(false))
	}
}

func TestAGitDetailSaysWhatWentWrongOrThatNothingDid(t *testing.T) {
	t.Parallel()
	if detail := doctorErrorDetail(nil); detail != "not a Git work tree" {
		t.Fatalf("a git that answered reads %q, want the reason it was refused", detail)
	}
	failure := errors.New("git could not be started")
	if detail := doctorErrorDetail(failure); detail != failure.Error() {
		t.Fatalf("a git that failed reads %q, want %q", detail, failure)
	}
}
