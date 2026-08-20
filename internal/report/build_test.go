// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
)

// TestBuildRefuses walks every way a caller can hand [report.Build] something
// that would produce a document nobody should trust.
//
// Each case starts from the fixture and breaks exactly one thing, and each
// asserts the diagnostic code rather than the message: the codes are the
// interface, and a message can be reworded without a release note.
func TestBuildRefuses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want report.Code
		// break_ edits the otherwise valid options. It is given the fixture's
		// catalogue order, which is what the mutant indices below refer to.
		break_ func(t *testing.T, opts *report.Options, mutants []mutation.Mutant)
	}{
		{
			name: "a run id that is not a run id",
			want: report.CodeInvalidRunID,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.RunID = "../../etc/passwd"
			},
		},
		{
			name: "a run id with the wrong shape",
			want: report.CodeInvalidRunID,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.RunID = "20260218T091500Z-3F9C"
			},
		},
		{
			name: "no status at all",
			want: report.CodeInvalidStatus,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Status = ""
			},
		},
		{
			name: "the event stream's spelling of a status",
			want: report.CodeInvalidStatus,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Status = report.Status("ok")
			},
		},
		{
			name: "no clock",
			want: report.CodeInvalidTimestamps,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Finished = time.Time{}
			},
		},
		{
			name: "a run that finished before it started",
			want: report.CodeInvalidTimestamps,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Finished = o.Started.Add(-time.Second)
			},
		},
		{
			name: "a workspace digest that could not name a directory",
			want: report.CodeInvalidWorkspaceDigest,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.WorkspaceDigest = "not-a-digest"
			},
		},
		{
			name: "an uppercase workspace digest",
			want: report.CodeInvalidWorkspaceDigest,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.WorkspaceDigest = strings.ToUpper(o.WorkspaceDigest)
			},
		},
		{
			name: "no catalogue",
			want: report.CodeNoCatalog,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Catalog = nil
			},
		},
		{
			name: "no test command anywhere",
			want: report.CodeInvalidTestCommand,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.TestCommand = nil
				o.Config.Test.Command = nil
			},
		},
		{
			name: "a result for a mutant nobody catalogued",
			want: report.CodeUnknownMutant,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Results = append(slices.Clone(o.Results), report.MutantResult{
					ID:      staleID,
					Outcome: mutation.OutcomeKilled,
				})
			},
		},
		{
			name: "a rejection for a mutant nobody catalogued",
			want: report.CodeUnknownMutant,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Rejections = append(slices.Clone(o.Rejections), report.Rejection{
					ID:         staleID,
					Diagnostic: "does not compile",
				})
			},
		},
		{
			name: "one mutant with two results",
			want: report.CodeDuplicateEntry,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Results = append(slices.Clone(o.Results), o.Results[0])
			},
		},
		{
			name: "one mutant rejected twice",
			want: report.CodeDuplicateEntry,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Rejections = append(slices.Clone(o.Rejections), o.Rejections[0])
			},
		},
		{
			name: "a mutant both rejected and executed",
			want: report.CodeDuplicateEntry,
			break_: func(_ *testing.T, o *report.Options, m []mutation.Mutant) {
				o.Results = append(slices.Clone(o.Results), report.MutantResult{
					ID:      m[7].ID,
					Outcome: mutation.OutcomeSurvived,
				})
			},
		},
		{
			name: "a catalogued mutant nobody accounted for",
			want: report.CodeMissingResult,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Results = slices.Clone(o.Results)[1:]
			},
		},
		{
			name: "a mutant with no coordinates",
			want: report.CodeMissingLocation,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Located = slices.Clone(o.Located)[1:]
			},
		},
		{
			name: "an outcome that does not exist",
			want: report.CodeInvalidOutcome,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				results := slices.Clone(o.Results)
				results[0].Outcome = mutation.Outcome(42)
				o.Results = results
			},
		},
		{
			name: "more mutants selected than catalogued",
			want: report.CodeInvalidSelection,
			break_: func(_ *testing.T, o *report.Options, m []mutation.Mutant) {
				o.Selected = len(m) + 1
			},
		},
		{
			name: "a negative selection",
			want: report.CodeInvalidSelection,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Selected = -1
			},
		},
		{
			name: "a selection mode from a later phase",
			want: report.CodeInvalidSelection,
			break_: func(_ *testing.T, o *report.Options, _ []mutation.Mutant) {
				o.Mode = report.SelectionMode("shard")
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			opts := fixtureOptions(t)
			c.break_(t, &opts, opts.Catalog.Mutants())
			r, err := report.Build(opts)
			if err == nil {
				t.Fatalf("Build accepted it and returned a report with %d mutants", len(r.Mutants))
			}
			if got := report.CodeOf(err); got != c.want {
				t.Fatalf("code = %q, want %q (%v)", got, c.want, err)
			}
			if r != nil {
				t.Error("Build returned a report alongside the error")
			}
		})
	}
}

// TestBuildFillsInWhatItCan checks the defaults that are safe to have, and the
// wording of the ones that stand in for a fact the run does not know.
func TestBuildFillsInWhatItCan(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	opts.Mode = ""
	opts.Platform = report.Platform{}
	opts.TimeoutSource = ""
	opts.ModulePath = ""
	opts.GoVersion = ""
	opts.TestCommand = nil
	opts.Config.Test.Command = []string{"gotestsum", "--"}

	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Selection.Mode != report.ModeAll {
		t.Errorf("mode = %q, want %q", r.Selection.Mode, report.ModeAll)
	}
	if r.Workspace.Platform != (report.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}) {
		t.Errorf("platform = %+v, want this host's", r.Workspace.Platform)
	}
	if r.Workspace.ModulePath != "unknown" || r.Workspace.GoVersion != "unknown" {
		t.Errorf("workspace = %+v, want the unknown values spelled out", r.Workspace)
	}
	if !slices.Equal(r.Test.Command, []string{"gotestsum", "--"}) {
		t.Errorf("command = %v, want test.command from the configuration", r.Test.Command)
	}
	if r.Test.TimeoutSource != report.TimeoutDerived {
		t.Errorf("timeout_source = %q, want %q for an unconfigured timeout", r.Test.TimeoutSource, report.TimeoutDerived)
	}
	if err := schemas.Validate(schemas.RunReportV1, mustMarshal(t, r)); err != nil {
		t.Fatalf("the filled-in report does not satisfy the schema: %v", err)
	}
}

// TestTimeoutSourceFollowsTheConfiguration proves the one default that could
// mislabel a fact: a configured timeout is never reported as a derived one.
func TestTimeoutSourceFollowsTheConfiguration(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	opts.TimeoutSource = ""
	opts.Config.Test.Timeout = 30 * time.Second
	opts.Timeout = 30 * time.Second

	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Test.TimeoutSource != report.TimeoutExplicit {
		t.Errorf("timeout_source = %q, want %q", r.Test.TimeoutSource, report.TimeoutExplicit)
	}
	if r.Test.TimeoutMS != 30_000 {
		t.Errorf("timeout_ms = %d, want 30000", r.Test.TimeoutMS)
	}
}

// TestPolicyFailureIsTheFirstReason walks the gates that can fail a run and
// checks the one named in the document.
//
// The verdict is computed from the report's own tally, so this is also the test
// that the number a user reads and the gate that read it cannot disagree.
func TestPolicyFailureIsTheFirstReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		setup func(t *testing.T, opts *report.Options)
		want  string
	}{
		{
			name: "an errored mutant outranks every policy gate",
			setup: func(_ *testing.T, o *report.Options) {
				o.Config.Policy.Strict = true
			},
			want: string(mutation.ReasonErroredMutants),
		},
		{
			name: "an infrastructure failure comes first of all",
			setup: func(_ *testing.T, o *report.Options) {
				o.InfrastructureError = true
			},
			want: string(mutation.ReasonInfrastructure),
		},
		{
			name: "strict survivors, once the harness is clean",
			setup: func(t *testing.T, o *report.Options) {
				o.Config.Policy.Strict = true
				clearErrors(t, o)
			},
			want: string(mutation.ReasonUnexpectedSurvivors),
		},
		{
			name: "a score below the floor",
			setup: func(t *testing.T, o *report.Options) {
				o.Config.Policy.MinimumScore = 90
				clearErrors(t, o)
			},
			want: string(mutation.ReasonBelowMinimumScore),
		},
		{
			name: "a stale ledger row, once the harness is clean",
			setup: func(t *testing.T, o *report.Options) {
				clearErrors(t, o)
				o.Config.Mutation.Expect = []config.Expectation{{ID: staleID, Reason: "long gone"}}
			},
			want: string(mutation.ReasonExpectationFailure),
		},
		{
			name:  "no mutants at all",
			setup: discoverNothing,
			want:  string(mutation.ReasonNoMutants),
		},
		{
			name:  "a clean run names no failure",
			setup: func(t *testing.T, o *report.Options) { clearErrors(t, o) },
			want:  "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			opts := fixtureOptions(t)
			c.setup(t, &opts)
			r, err := report.Build(opts)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			got := ""
			if r.Summary.Policy.Failure != nil {
				got = *r.Summary.Policy.Failure
			}
			if got != c.want {
				t.Errorf("policy.failure = %q, want %q", got, c.want)
			}
			if err := schemas.Validate(schemas.RunReportV1, mustMarshal(t, r)); err != nil {
				t.Fatalf("the report does not satisfy the schema: %v", err)
			}
		})
	}
}

// TestEmptyRunIsAWholeDocument proves that a run with nothing in it still
// produces a complete, valid document rather than one full of nulls.
func TestEmptyRunIsAWholeDocument(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	discoverNothing(t, &opts)
	opts.Skips = nil
	opts.Warnings = nil
	opts.Baseline = nil
	opts.Status = report.StatusFailed
	opts.InfrastructureError = true

	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	encoded := string(mustMarshal(t, r))
	for _, empty := range []string{
		`"mutants": []`, `"rejected": []`, `"skips": []`,
		`"expectations": []`, `"warnings": []`, `"durations_ms": []`,
	} {
		if !strings.Contains(encoded, empty) {
			t.Errorf("an empty list was not written as %s:\n%s", empty, encoded)
		}
	}
	if strings.Contains(encoded, "null,") && !strings.Contains(encoded, `"score_percent": null`) {
		t.Errorf("an unexpected null reached the document:\n%s", encoded)
	}
	if err := schemas.Validate(schemas.RunReportV1, []byte(encoded)); err != nil {
		t.Fatalf("the empty report does not satisfy the schema: %v", err)
	}
	if r.Test.Baseline.Runs != 0 || r.Test.Baseline.SlowestMS != 0 {
		t.Errorf("baseline = %+v, want an unmeasured one", r.Test.Baseline)
	}
}

// TestOptionalStringsAreNullNotEmpty proves that "the harness named no test" is
// written as null rather than as a name that is not a name.
func TestOptionalStringsAreNullNotEmpty(t *testing.T) {
	t.Parallel()

	r := buildFixture(t)
	var killed, notRun *report.Mutant
	for i := range r.Mutants {
		switch r.Mutants[i].Outcome {
		case report.OutcomeKilled:
			killed = &r.Mutants[i]
		case report.OutcomeNotRun:
			notRun = &r.Mutants[i]
		}
	}
	if killed == nil || notRun == nil {
		t.Fatal("the fixture no longer has both a killed and a not-run mutant")
	}
	if killed.KilledBy == nil || *killed.KilledBy != alphaPackage {
		t.Errorf("killed_by = %v, want %q", killed.KilledBy, alphaPackage)
	}
	if killed.OutputTail == nil || *killed.OutputTail == "" {
		t.Error("a killed mutant kept no output tail")
	}
	if notRun.KilledBy != nil || notRun.OutputTail != nil {
		t.Errorf("a not-run mutant carries killed_by %v and output_tail %v, want both null",
			notRun.KilledBy, notRun.OutputTail)
	}
	if notRun.Attempts != 0 {
		t.Errorf("a not-run mutant reports %d attempts, want 0", notRun.Attempts)
	}
}

// TestRejectedMutantsAreNotCounted proves a mutant that cannot compile stays
// out of the summary entirely: it is neither an error nor a survivor, and it
// must not reach the denominator of a score.
func TestRejectedMutantsAreNotCounted(t *testing.T) {
	t.Parallel()

	r := buildFixture(t)
	if len(r.Rejected) != 1 {
		t.Fatalf("the fixture reports %d rejections, want 1", len(r.Rejected))
	}
	rejected := r.Rejected[0]
	if rejected.Diagnostic != fixtureDiagnostic {
		t.Errorf("diagnostic = %q, want %q", rejected.Diagnostic, fixtureDiagnostic)
	}
	if rejected.Line == 0 || rejected.Column == 0 {
		t.Errorf("the rejection has no coordinates: %+v", rejected)
	}
	for _, m := range r.Mutants {
		if m.ID == rejected.ID {
			t.Fatalf("mutant %s is reported as both rejected and executed", m.DisplayID)
		}
	}
	if r.Summary.Total != len(r.Mutants) {
		t.Errorf("summary.total = %d, want the %d executed mutants", r.Summary.Total, len(r.Mutants))
	}
}

// TestCatalogueOrderIsTheDocumentOrder proves the arrays are the catalogue's
// own order rather than a second opinion about it.
func TestCatalogueOrderIsTheDocumentOrder(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	// Discovery's own output order must not matter: the catalogue decides.
	opts.Located = slices.Clone(opts.Located)
	slices.Reverse(opts.Located)
	opts.Results = slices.Clone(opts.Results)
	slices.Reverse(opts.Results)

	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !slices.IsSortedFunc(r.Mutants, func(x, y report.Mutant) int {
		if c := strings.Compare(x.Path, y.Path); c != 0 {
			return c
		}
		return int(x.StartByte) - int(y.StartByte)
	}) {
		t.Errorf("the mutants are not in (path, start_byte) order: %+v", r.Mutants)
	}
	if !slices.Equal(mustMarshal(t, r), mustMarshal(t, buildFixture(t))) {
		t.Error("reversing discovery's output changed the document")
	}
}

// clearErrors turns the fixture's errored mutant into a survivor, so that a
// test about the policy gates is not answered by the harness tier first.
func clearErrors(t *testing.T, opts *report.Options) {
	t.Helper()
	results := slices.Clone(opts.Results)
	found := false
	for i := range results {
		if results[i].Outcome == mutation.OutcomeErrored {
			results[i].Outcome = mutation.OutcomeSurvived
			results[i].OutputTail = ""
			found = true
		}
	}
	if !found {
		t.Fatal("the fixture no longer has an errored mutant")
	}
	opts.Results = results
	// The unfulfilled row names a killed mutant, which is a contract failure of
	// its own and would answer before any policy gate.
	opts.Config.Mutation.Expect = nil
}

// discoverNothing empties the options of everything one discovery pass
// produced, for the run that found no mutants at all.
func discoverNothing(t *testing.T, opts *report.Options) {
	t.Helper()
	catalog, err := discover.BuildCatalog(discover.Result{})
	if err != nil {
		t.Fatalf("building an empty catalogue: %v", err)
	}
	opts.Catalog = catalog
	opts.Located = nil
	opts.Results = nil
	opts.Rejections = nil
	opts.Selected = 0
	opts.Config.Mutation.Expect = nil
}
