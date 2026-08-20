// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/snapshot"
)

// driftFixture copies a two-file tree into a snapshot and returns it, so that a
// test can then move bytes around inside the copy and ask the gate what it
// makes of them.
func driftFixture(t *testing.T) *snapshot.Snapshot {
	t.Helper()
	source := t.TempDir()
	for name, content := range map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.26\n",
		"a.go":   "package m\n\nfunc A() bool { return true }\n",
		"b.go":   "package m\n\nfunc B() bool { return false }\n",
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	snap, err := snapshot.Create(source, snapshot.Options{DestParent: t.TempDir()})
	if err != nil {
		t.Fatalf("snapshotting: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("cleaning up the snapshot: %v", cleanupErr)
		}
	})
	return snap
}

// write puts bytes at a snapshot-relative path, creating the directory.
func write(t *testing.T, snap *snapshot.Snapshot, rel, content string) {
	t.Helper()
	path := filepath.Join(snap.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating the directory for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", rel, err)
	}
}

// instrumented describes an instrumentation pass that guarded a.go and wrote
// the runtime package, which is the state the drift gate is meant to accept.
func instrumented() instrument.Result {
	return instrument.Result{
		RuntimeDir:        "gomutants_rt",
		RuntimeImport:     "example.com/m/gomutants_rt",
		FilesInstrumented: []string{"a.go"},
		GuardsByFile:      map[string]int{"a.go": 1},
	}
}

func TestDriftGateAcceptsTheInstrumentationsOwnChanges(t *testing.T) {
	snap := driftFixture(t)
	// Exactly what a validated instrumentation pass leaves behind: the guarded
	// file rewritten, and the generated runtime added.
	write(t, snap, "a.go", "package m\n\nfunc A() bool { if __gm.M[0] { return false }; return true }\n")
	write(t, snap, "gomutants_rt/gomutants_rt.go", "package gomutants_rt\n\nvar M [1]bool\n")

	if err := driftGate(snap, instrumented()); err != nil {
		t.Fatalf("the gate refused the instrumentation's own drift: %v", err)
	}
}

func TestDriftGateNamesATestThatWroteIntoItsPackageDirectory(t *testing.T) {
	snap := driftFixture(t)
	write(t, snap, "a.go", "package m\n\nfunc A() bool { if __gm.M[0] { return false }; return true }\n")
	write(t, snap, "gomutants_rt/gomutants_rt.go", "package gomutants_rt\n\nvar M [1]bool\n")
	// The hazard: a test that updated a golden file in the tree every later
	// mutant is measured against.
	write(t, snap, "testdata/golden.txt", "updated\n")

	err := driftGate(snap, instrumented())
	if CodeOf(err) != CodeWorkspaceDrift {
		t.Fatalf("the gate returned %v, want %s", err, CodeWorkspaceDrift)
	}
	// The files have to be named, or the user is left with an exit code and a
	// tree that is already gone.
	if output := OutputOf(err); !strings.Contains(output, "added testdata/golden.txt") {
		t.Errorf("the drift error does not name the file:\n%s", output)
	}
	if strings.Contains(OutputOf(err), "a.go") {
		t.Errorf("the drift error blamed a file the instrumentation rewrote:\n%s", OutputOf(err))
	}
}

// TestDriftGateNoticesAFileTheRewriteDidNotTouch is the same gate from the
// other side: a file validation restored to pristine — because every candidate
// in it was rejected — is not in FilesInstrumented, so a change to it is
// somebody else's doing.
func TestDriftGateNoticesAFileTheRewriteDidNotTouch(t *testing.T) {
	snap := driftFixture(t)
	write(t, snap, "b.go", "package m\n\nfunc B() bool { return true }\n")

	err := driftGate(snap, instrumented())
	if CodeOf(err) != CodeWorkspaceDrift {
		t.Fatalf("the gate returned %v, want %s", err, CodeWorkspaceDrift)
	}
	if output := OutputOf(err); !strings.Contains(output, "changed b.go") {
		t.Errorf("the drift error does not name the file:\n%s", output)
	}
}

func TestDriftGateNoticesADeletedFile(t *testing.T) {
	snap := driftFixture(t)
	if err := os.Remove(filepath.Join(snap.Root, "b.go")); err != nil {
		t.Fatalf("removing b.go: %v", err)
	}
	err := driftGate(snap, instrumented())
	if CodeOf(err) != CodeWorkspaceDrift {
		t.Fatalf("the gate returned %v, want %s", err, CodeWorkspaceDrift)
	}
	if output := OutputOf(err); !strings.Contains(output, "removed b.go") {
		t.Errorf("the drift error does not name the file:\n%s", output)
	}
}

// TestNotableIsWorstFirstAndTotallyOrdered pins the summary block's order.
//
// Worst first is what makes the block worth reading; the tie-break is what
// makes it diffable. Two rules can propose an edit on one line, so path and
// line alone are not a total order and a block built on them would change shape
// between two runs of the same workspace.
func TestNotableIsWorstFirstAndTotallyOrdered(t *testing.T) {
	rows := []struct {
		id      string
		path    string
		line    int
		column  int
		rule    string
		outcome mutation.Outcome
	}{
		{"e0", "z.go", 1, 1, "eq-to-neq", mutation.OutcomeErrored},
		{"k0", "a.go", 1, 1, "eq-to-neq", mutation.OutcomeKilled},
		{"s1", "b.go", 7, 3, "lt-to-le", mutation.OutcomeSurvived},
		{"i0", "m.go", 2, 1, "eq-to-neq", mutation.OutcomeInconclusive},
		{"s0", "b.go", 7, 1, "gt-to-ge", mutation.OutcomeSurvived},
		{"n0", "a.go", 9, 1, "eq-to-neq", mutation.OutcomeNotRun},
		{"t0", "c.go", 4, 1, "neq-to-eq", mutation.OutcomeTimedOut},
		{"s2", "a.go", 3, 1, "eq-to-neq", mutation.OutcomeSurvived},
	}
	st := &state{display: make(map[string]MutantResult)}
	rep := &report.Report{}
	for _, row := range rows {
		st.display[row.id] = MutantResult{
			ID: row.id, DisplayID: row.id, Path: row.path,
			Line: row.line, Column: row.column, Rule: row.rule,
		}
		outcome, err := report.OutcomeOf(row.outcome)
		if err != nil {
			t.Fatalf("rendering %s: %v", row.outcome, err)
		}
		rep.Mutants = append(rep.Mutants, report.Mutant{ID: row.id, Outcome: outcome})
	}

	got := make([]string, 0, len(rows))
	for _, m := range notable(st, rep) {
		got = append(got, fmt.Sprintf("%s %s", m.Outcome, m.ID))
	}
	want := []string{
		"survived s2", // a.go:3
		"survived s0", // b.go:7:1
		"survived s1", // b.go:7:3, the same line, later column
		"timed_out t0",
		"inconclusive i0",
		"errored e0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("notable =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

func TestSkipCountsAggregateByReason(t *testing.T) {
	skips := []discover.Skip{
		{Path: "b.go", Reason: discover.SkipConstDecl, Count: 2},
		{Path: "a.go", Reason: discover.SkipGenerated, Count: 1},
		{Path: "a.go", Reason: discover.SkipConstDecl, Count: 3},
	}
	if got := skipTotal(skips); got != 6 {
		t.Errorf("skipTotal = %d, want 6", got)
	}
	want := []SkipCount{
		{Reason: "const-decl", Count: 5},
		{Reason: "generated", Count: 1},
	}
	if got := skipCounts(skips); !slices.Equal(got, want) {
		t.Errorf("skipCounts = %+v, want %+v", got, want)
	}
	if got := skipCounts(nil); len(got) != 0 {
		t.Errorf("skipCounts(nil) = %+v, want nothing", got)
	}
}

func TestSelectRulesFollowsTheProfileUntilAnOperatorIsNamed(t *testing.T) {
	balanced := config.Defaults()
	tiered, err := SelectRules(balanced)
	if err != nil {
		t.Fatalf("SelectRules with the default profile: %v", err)
	}
	if len(tiered) == 0 {
		t.Fatal("the balanced profile selected nothing")
	}

	// A named operator is looked up in the whole catalogue, so a family outside
	// the profile is honoured rather than silently dropped.
	named := config.Defaults()
	named.Mutation.Operators = []string{"bitwise"}
	rules, err := SelectRules(named)
	if err != nil {
		t.Fatalf("SelectRules with an out-of-profile family: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("naming a family outside the profile selected nothing")
	}
	for _, rule := range rules {
		if rule.Family != "bitwise" {
			t.Errorf("selected %s, want only the bitwise family", rule)
		}
	}
	// Canonical registry order, whatever order the names were written in: rule
	// order is part of what makes a catalogue reproducible.
	registry := mutation.CanonicalRegistry()
	if !slices.IsSortedFunc(rules, func(x, y mutation.Rule) int {
		xp, _ := registry.Position(x.Name)
		yp, _ := registry.Position(y.Name)
		return xp - yp
	}) {
		t.Errorf("selected rules are not in registry order: %v", rules)
	}
}

func TestSelectRulesRefusesANameTheCatalogueDoesNotKnow(t *testing.T) {
	cfg := config.Defaults()
	cfg.Mutation.Operators = []string{"telepathy"}
	if _, err := SelectRules(cfg); CodeOf(err) != CodeUnknownOperator {
		t.Errorf("SelectRules with an unknown operator = %v, want %s", err, CodeUnknownOperator)
	}
}

// TestSelectionErrorCarriesNoCodeAndKeepsTheSentinel pins the one error this
// package raises without a GOM code, and why that is safe: the command line can
// still tell what went wrong.
func TestSelectionErrorCarriesNoCodeAndKeepsTheSentinel(t *testing.T) {
	err := error(&SelectionError{Prefix: "beef", Err: mutation.ErrAmbiguousPrefix})
	if code := CodeOf(err); code != "" {
		t.Errorf("CodeOf = %q, want no code: internal/cli owns the invocation vocabulary", code)
	}
	if !errors.Is(err, mutation.ErrAmbiguousPrefix) {
		t.Error("the catalogue's sentinel is not reachable through errors.Is")
	}
	if !strings.Contains(err.Error(), `"beef"`) {
		t.Errorf("Error() = %q, want it to quote the prefix", err.Error())
	}
	// It is not a cancellation, which is the other thing internal/cli tests an
	// engine error for.
	if interrupted(err) {
		t.Error("a selection error was read as an interruption")
	}
}

// TestRejectedSelectionNamesTheMutantAndQuotesTheCompiler pins the message
// behind [CodeSelectedMutantRejected].
//
// Everything it asserts is what the warning exists to carry: without the id and
// the coordinates the user cannot tell which of several mutants was refused,
// and without the compiler's own words the message says only what nothing
// having run already implied.
func TestRejectedSelectionNamesTheMutantAndQuotesTheCompiler(t *testing.T) {
	id := "a1b2c3d4" + strings.Repeat("0", 56)
	chosen := mutation.Mutant{
		ID:        id,
		DisplayID: "a1b2c3d4e5f6",
		Candidate: mutation.Candidate{
			Path: "flag.go",
			Rule: mutation.Rule{Name: "eq-to-neq", Version: 1},
		},
	}
	st := &state{
		display: map[string]MutantResult{
			id: {Path: "flag.go", Line: 8, Column: 9},
		},
		rejections: []report.Rejection{
			{ID: "beef", Diagnostic: "some other mutant's problem"},
			{ID: id, Diagnostic: "./flag.go:8:9: cannot use guard as Flag value\n./flag.go:8:9: in argument to f"},
		},
	}

	got := rejectedSelection("a1b2c3d4", chosen, st)
	for _, needle := range []string{
		`--mutant "a1b2c3d4"`,
		"a1b2c3d4e5f6",
		"flag.go:8:9",
		"eq-to-neq",
		"cannot use guard as Flag value",
		// The second compiler line is the one that often names the type, so it
		// is folded in rather than dropped.
		"in argument to f",
	} {
		if !strings.Contains(got, needle) {
			t.Errorf("the message does not mention %q:\n%s", needle, got)
		}
	}
	if strings.Contains(got, "some other mutant's problem") {
		t.Errorf("the message quoted the wrong rejection:\n%s", got)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("the message is not one line: %q", got)
	}

	// A rejection that carried no diagnostic must not produce a message ending
	// in a dangling colon, and a mutant discovery left without coordinates must
	// not be located at ":0:0".
	bare := &state{display: map[string]MutantResult{id: {Path: "flag.go"}}}
	got = rejectedSelection("a1b2c3d4", chosen, bare)
	if strings.HasSuffix(got, ":") || strings.Contains(got, ":0:0") {
		t.Errorf("the message invented detail it did not have: %q", got)
	}
	if !strings.Contains(got, "a1b2c3d4e5f6") {
		t.Errorf("the message stopped naming the mutant: %q", got)
	}

	// A user who pasted the whole display id gets it once, not twice: "--mutant
	// "a1b2c3d4e5f6" selected a1b2c3d4e5f6" is a sentence that reads like a bug.
	got = rejectedSelection(chosen.DisplayID, chosen, st)
	if n := strings.Count(got, chosen.DisplayID); n != 1 {
		t.Errorf("the display id appears %d times, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, "flag.go:8:9") {
		t.Errorf("the message lost the coordinates:\n%s", got)
	}
}

func TestFoldLinesCollapsesAMultiLineDiagnostic(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"one line", "one line"},
		{"first\nsecond", "first; second"},
		{"first\r\nsecond", "first; second"},
		{"\n\n  padded  \n\n", "padded"},
	}
	for _, test := range cases {
		if got := foldLines(test.in); got != test.want {
			t.Errorf("foldLines(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestReportTimeoutSourceMapsBothSpellings(t *testing.T) {
	if got := reportTimeoutSource(TimeoutExplicit); got != report.TimeoutExplicit {
		t.Errorf("explicit mapped to %q", got)
	}
	if got := reportTimeoutSource(TimeoutDerived); got != report.TimeoutDerived {
		t.Errorf("derived mapped to %q", got)
	}
	// An engine that somehow held neither says "derived", which is the truthful
	// default: nothing was configured.
	if got := reportTimeoutSource(""); got != report.TimeoutDerived {
		t.Errorf("the empty source mapped to %q, want %q", got, report.TimeoutDerived)
	}
}

func TestGoVersionPrefersTheModulesOwnDirective(t *testing.T) {
	if got := goVersion("1.26", "go1.27.1"); got != "1.26" {
		t.Errorf("goVersion = %q, want the module's own directive", got)
	}
	if got := goVersion("", "go1.27.1"); got != "go1.27.1" {
		t.Errorf("goVersion = %q, want the toolchain as the fallback", got)
	}
	if got := goVersion("", ""); got != "" {
		t.Errorf("goVersion = %q, want the empty string report.Build fills in", got)
	}
	if got := or("", unknownValue); got != unknownValue {
		t.Errorf("or = %q, want %q", got, unknownValue)
	}
}
