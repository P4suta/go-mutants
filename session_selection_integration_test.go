// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// Selecting by line range: what [gomutants.PrepareOptions.Selection] changes
// about a prepared session, and — the longer half — everything it does not.
//
// The consumer motivation is narrow and worth stating. goatest already narrows
// a run to the code somebody touched, and it can only do it by *file*: it drops
// every mutant in a file the diff did not name and executes every mutant in a
// file it did, including the two hundred on lines nobody edited. The engine has
// owned the line-level rule since `--changed` existed. So the claim these tests
// circle is that reaching it through the library costs a consumer nothing it was
// relying on: the same catalogue, the same identities, the same verdicts about
// what compiles, and a session that will still execute anything asked of it.

package gomutants_test

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// selectionRequest is the selection the narrowed session is prepared with, and
// it is written in the shape a caller's really arrives in rather than in the
// shape the engine wants.
//
// fixtures/killable holds exactly three comparison mutants: `<` on line 41 of
// clamp.go, `>` on line 42, and `!=` on line 14 of untested.go. The ranges below
// reach the first and none of the others, which is what makes every claim here
// non-vacuous — a selection that happened to select everything would satisfy
// most of them by accident.
//
// The three entries are each a case in their own right:
//
//   - `./clamp.go` in two unordered, adjacent ranges, so that the normalised
//     copy the catalogue hands back has to be a cleaned path and one merged
//     range rather than an echo of the request;
//   - `untested.go` with lines that exist and hold no mutant, which selects
//     nothing in a file the selection does name;
//   - a path the module does not hold at all, which is the ordinary case for a
//     selection built out of a diff — deleted files, documents, testdata — and
//     is documented to select nothing rather than to be refused.
var selectionRequest = gomutants.Selection{Lines: map[string][]gomutants.LineRange{
	"./clamp.go":     {{First: 41, Last: 41}, {First: 39, Last: 40}},
	"untested.go":    {{First: 1, Last: 3}},
	"docs/absent.md": {{First: 1, Last: 10}},
}}

// selectionNormalised is what [selectionRequest] becomes: paths cleaned, ranges
// sorted and merged, everything else left exactly where the caller put it.
var selectionNormalised = gomutants.Selection{Lines: map[string][]gomutants.LineRange{
	"clamp.go":       {{First: 39, Last: 41}},
	"untested.go":    {{First: 1, Last: 3}},
	"docs/absent.md": {{First: 1, Last: 10}},
}}

// selectedFixture is fixtures/killable prepared *with* that selection.
//
// It is a session of its own because a selection is a preparation option, and
// there is no way to ask an already prepared session what a different one would
// have said. Everything else about it is [controlledFixture] to the letter —
// the same fixture, the same injected source, the same operators, the same
// frozen environment — because the claim the file opens with is a comparison
// between two catalogues, and a second difference between them would make every
// equality below prove nothing.
var selectedFixture = sync.OnceValue(func() *preparedFixture {
	selection := selectionRequest
	return prepareFixtureWith("killable",
		map[string]string{"session_test.go": killableExtraTests},
		gomutants.OpenOptions{Env: hostEnvWithoutFixtureGates()},
		gomutants.PrepareOptions{
			Operators:     []string{"comparison"},
			SkipVerify:    true,
			MutantTimeout: 30 * time.Second,
			Selection:     &selection,
		})
})

// selected returns that session, failing the calling test if preparing it did
// not work.
func selected(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := selectedFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the killable fixture with a selection: %v", prepared.err)
	}
	return prepared
}

// TestPrepareWithSelectionMatchesAFullRunsCatalogue is the guarantee the whole
// feature rests on, and it is a claim about what did *not* change.
//
// A selection narrows execution and nothing else. Discovery, validation and the
// catalogue still cover the whole module, so the two sessions catalogue the same
// mutants in the same order under the same identities, agree on which of them
// the compiler accepted, and agree on every rejection. That is what lets a
// consumer compare a narrowed run against the full run before it, hand an id
// out of either straight back to the engine, and merge two narrowings of one
// tree — and it is exactly what narrowing *discovery* would have destroyed,
// since a mutant id is minted from its file's own bytes and the catalogue is
// deduplicated across the module.
//
// What does move is [gomutants.Catalog.PreparedDigest], and it has to. A caller
// that stored "this mutant survived" against a session where it was out of the
// selection never executed it; reusing that under the full session would be
// reporting a measurement nobody made.
func TestPrepareWithSelectionMatchesAFullRunsCatalogue(t *testing.T) {
	full := controlled(t).catalog
	narrowed := selected(t).catalog

	// Everything the prepared digest hashes but the selection flags, checked
	// first: without this the difference at the end of the test could be any of
	// them, and the day selection stopped changing the key this would go on
	// passing.
	if full.WorkspaceDigest != narrowed.WorkspaceDigest {
		t.Fatalf("the two sessions froze different trees (%s and %s); every claim here is about"+
			" two preparations of one tree", full.WorkspaceDigest, narrowed.WorkspaceDigest)
	}
	for _, field := range []struct{ name, full, narrowed string }{
		{"Digest", full.Digest, narrowed.Digest},
		{"ModulePath", full.ModulePath, narrowed.ModulePath},
		{"GoVersion", full.GoVersion, narrowed.GoVersion},
		{"Toolchain", full.Toolchain, narrowed.Toolchain},
		{"Profile", full.Profile, narrowed.Profile},
	} {
		if field.full != field.narrowed {
			t.Errorf("%s = %q with a selection and %q without one", field.name, field.narrowed, field.full)
		}
	}
	if !slices.Equal(full.TestPackages, narrowed.TestPackages) {
		t.Errorf("TestPackages = %v with a selection and %v without one", narrowed.TestPackages, full.TestPackages)
	}
	// fixtures/killable compiles every mutant, so this row is *vacuous* here —
	// both slices are empty — and it is kept as the shape of the claim rather
	// than as its proof. The claim itself is carried by the two things that do
	// bite: the whole-mutant comparison below, which includes Accepted and is
	// what a rejection is the other side of, and assertCatalogInvariants, which
	// runs over fixtures/rejectable and is where "rejected iff not accepted" is
	// exercised against a catalogue that has rejections. Preparing this fixture
	// a third time over rejectable would buy the row its own evidence at the
	// price of another minute of preparation for a claim already proved.
	if !reflect.DeepEqual(full.Rejections, narrowed.Rejections) {
		t.Errorf("Rejections = %+v with a selection and %+v without one; validation runs over the"+
			" whole catalogue whatever the caller means to execute", narrowed.Rejections, full.Rejections)
	}

	// Every field of every mutant but Selected, in catalogue order. Comparing
	// the slices rather than a chosen handful is the point: a field that started
	// following the selection would be a field a consumer's stored evidence is
	// keyed on moving for a reason nobody documented.
	if len(full.Mutants) != len(narrowed.Mutants) {
		t.Fatalf("the selection changed the catalogue from %d mutants to %d; it narrows execution"+
			" and never discovery", len(full.Mutants), len(narrowed.Mutants))
	}
	stripped := slices.Clone(narrowed.Mutants)
	for i := range stripped {
		stripped[i].Selected = full.Mutants[i].Selected
	}
	if !reflect.DeepEqual(full.Mutants, stripped) {
		t.Errorf("the two catalogues disagree about a mutant beyond Selected:\nwith a selection:"+
			" %+v\nwithout one: %+v", stripped, full.Mutants)
	}

	// The normalised selection, handed back rather than echoed.
	if full.Selection != nil {
		t.Errorf("the unnarrowed session carries Selection %+v, want nil", full.Selection)
	}
	if narrowed.Selection == nil {
		t.Fatal("the narrowed session carries no Selection, so nothing can say which lines its" +
			" score would cover")
	}
	if !reflect.DeepEqual(*narrowed.Selection, selectionNormalised) {
		t.Errorf("Catalog.Selection = %+v, want the normalised %+v", *narrowed.Selection, selectionNormalised)
	}

	// The answer, written out. Re-deriving the predicate here would compare the
	// engine against a copy of itself and pass however wrong both were; naming
	// the three mutants of fixtures/killable and what each one has to be is a
	// second opinion, and one a reader can check against the fixture by eye.
	//
	// The boundary is the row that earns its place. `clamp.go` is selected
	// through line 41 and `gt-to-ge` sits on line 42, one line past the end of
	// the range — so an off-by-one anywhere in the intersection would show up
	// here and nowhere else in this file. `neq-to-eq` is in a file the selection
	// *does* name, at line 14, outside the lines it named there, which is the
	// other way to be excluded and the one a path-level filter would get wrong.
	for _, want := range []struct {
		path, rule string
		selected   bool
	}{
		{path: "clamp.go", rule: "lt-to-le", selected: true},      // line 41, inside 39-41
		{path: "clamp.go", rule: "gt-to-ge", selected: false},     // line 42, one past the end
		{path: "untested.go", rule: "neq-to-eq", selected: false}, // line 14, outside 1-3
	} {
		mutant := mutantkit.APIMutantAt(t, narrowed, want.path, want.rule)
		if mutant.Selected != want.selected {
			t.Errorf("%s in %s is at line %d-%d and is Selected=%t, want %t against %v",
				want.rule, want.path, mutant.Line, mutant.EndLine, mutant.Selected, want.selected,
				selectionNormalised.Lines[want.path])
		}
	}
	// Three named mutants are the whole catalogue, which is what makes the rows
	// above a complete statement rather than a sample.
	if len(narrowed.Mutants) != 3 {
		t.Errorf("the fixture catalogues %d mutants, and the rows above name 3; a mutant nobody"+
			" named is one nobody decided about", len(narrowed.Mutants))
	}
	for _, mutant := range full.Mutants {
		if !mutant.Selected {
			t.Errorf("mutant %s is not Selected in a session prepared with no selection", mutant.DisplayID)
		}
	}

	// And the key does not move, which is the decision this whole feature turns
	// on. A selection is the caller's plan; per-mutant evidence keyed on this
	// digest is a fact about the tree, the toolchain and the mutant. Moving the
	// key the first time somebody narrowed a run would cost them every row they
	// had stored, and buy nothing — an unselected mutant is simply one there is
	// no evidence about, which a caller records by not recording it. See
	// TestPreparedDigestIsUnchangedBySelection for the argument in full.
	if full.PreparedDigest != narrowed.PreparedDigest {
		t.Errorf("PreparedDigest = %s with a selection and %s without one; the two sessions are"+
			" the same prepared tree and a consumer's stored evidence about it has just"+
			" stopped hitting", narrowed.PreparedDigest, full.PreparedDigest)
	}
}

// TestAnUnselectedMutantCanStillBeExecuted is the advisory half of the
// contract, and the mutant it executes is chosen so that the answer cannot be
// faked.
//
// `gt-to-ge` in clamp.go is outside the selection and is killed by the fixture's
// own suite. A session that refused it would error; one that quietly ran nothing
// would report a survivor. Only a session that really executed it comes back
// with a kill, and the second half of the test — the unselected mutant nothing
// calls, which survives — rules out a run in which every mutant looks killed.
//
// It matters because a selection is a *plan*. A consumer that finds an
// interesting survivor and wants the mutants beside it measured must not have to
// prepare the module a second time to do it, and a consumer whose selection was
// wrong must not discover that as an error from a session it has already paid
// for.
func TestAnUnselectedMutantCanStillBeExecuted(t *testing.T) {
	prepared := selected(t)

	for _, test := range []struct {
		name string
		path string
		rule string
		want gomutants.Outcome
	}{
		{
			name: "one the fixture's own tests kill",
			path: "clamp.go",
			rule: "gt-to-ge",
			want: gomutants.OutcomeKilled,
		},
		{
			name: "one nothing calls",
			path: "untested.go",
			rule: "neq-to-eq",
			want: gomutants.OutcomeSurvived,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutant := mutantkit.APIMutantAt(t, prepared.catalog, test.path, test.rule)
			if mutant.Selected {
				t.Fatalf("mutant %s at %s:%d is Selected; this test is about the ones the"+
					" selection left out", mutant.DisplayID, mutant.Path, mutant.Line)
			}
			result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
				Mutant:  mutant.ID,
				Package: killableModule,
				Args:    []string{"-test.run=^TestClamp$"},
			})
			if err != nil {
				t.Fatalf("executing the unselected mutant %s: %v", mutant.DisplayID, err)
			}
			if result.Outcome != test.want {
				t.Errorf("Outcome = %s, want %s; the session executed an unselected mutant and"+
					" reached a different verdict than the suite gives", result.Outcome, test.want)
			}
			if test.want == gomutants.OutcomeKilled && result.KilledBy != killableModule {
				t.Errorf("KilledBy = %q, want %q", result.KilledBy, killableModule)
			}
		})
	}
}

// TestAnInvalidOptionLeavesTheWorkspaceUnprepared is the lifecycle half of a
// refused selection, and it is a claim about every option error rather than
// about this one.
//
// `Prepare` spends its workspace, deliberately: a preparation that stopped
// part-way may have left instrumented sources in the frozen tree, so the tree
// promises nothing and both a second `Prepare` and every `Workspace.Exec` are
// refused. That reasoning is about a preparation that *began*. An option the
// engine will not accept is caught before anything is discovered, instrumented
// or built — nothing side-effecting has run, and the tree is byte for byte the
// one `Open` froze — so spending the workspace there charges a caller a full
// re-open and a full re-snapshot for a typo in a line number.
//
// A selection is where this stops being theoretical. It is the one option whose
// value is *computed*, usually from a diff, so it is the one a long-lived
// consumer will get wrong at runtime rather than in review; and the failure it
// produced was silent in the worst way — the second `Prepare`, with the request
// corrected, came back `ErrWorkspacePrepared` and blamed the caller for
// retrying.
//
// The integrity gate is deliberately on the other side of the line and stays
// there: it reads the tree, and a workspace whose snapshot has already moved is
// spent whatever the caller does next.
func TestAnInvalidOptionLeavesTheWorkspaceUnprepared(t *testing.T) {
	t.Parallel()

	root := testkit.Copy(t, "simple")
	workspace, err := gomutants.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		SkipVerify: true,
		Selection: &gomutants.Selection{Lines: map[string][]gomutants.LineRange{
			"simple.go": {{First: 0, Last: 4}},
		}},
	})
	if err == nil {
		_ = session.Close()
		t.Fatal("Prepare accepted a range counted from zero, want a refusal")
	}
	if !errors.Is(err, gomutants.ErrInvalidSelection) {
		t.Fatalf("Prepare = %v, want ErrInvalidSelection", err)
	}

	// A command still runs, which is the observable half: nothing was
	// instrumented, so there is nothing for ErrPrepareFailed to be protecting.
	run, execErr := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "version"}})
	if errors.Is(execErr, gomutants.ErrPrepareFailed) {
		t.Errorf("Exec after a refused option = %v; the preparation never began, and the tree is"+
			" the one Open froze", execErr)
	}
	if execErr != nil || run.ExitCode != 0 {
		t.Errorf("Exec after a refused option = (%+v, %v), want the command to have run", run, execErr)
	}

	// And the workspace is still worth preparing, which is the half that costs
	// a consumer a snapshot when it is wrong.
	session, err = workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		SkipVerify: true,
		Selection: &gomutants.Selection{Lines: map[string][]gomutants.LineRange{
			"simple.go": {{First: 1, Last: 4}},
		}},
	})
	if err != nil {
		t.Fatalf("Prepare after a refused option = %v, want the corrected request to be served", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if errors.Is(err, gomutants.ErrWorkspacePrepared) {
		t.Error("the refused option spent the workspace, so a caller that fixed its request has" +
			" to open another one and freeze the tree again")
	}
	if catalog := session.Catalog(); catalog.Selection == nil {
		t.Error("the second preparation carries no Selection, so it did not apply the corrected one")
	}
}

// TestSelectionRejectionsAreTypedAndNameThePath is what a consumer resolving
// somebody's `--changed`-shaped input has to be able to do: tell "you wrote the
// request wrong" from "the engine broke", and say which entry.
//
// Every case here is a request that *looks* like a narrowing and would select
// nothing — an absolute path, a path escaping the module, a range counted from
// zero, a range whose ends arrived the wrong way round. Refusing them is the
// judgement `--changed` already makes: a selection nobody can satisfy measures
// no mutants, and a run that measured no mutants and exited 0 is the failure a
// selection feature must never produce.
//
// The refusal comes back before anything is discovered, which is why each case
// affords a workspace of its own: a mistake in the caller's own request must not
// cost the ten minutes a preparation takes to find.
func TestSelectionRejectionsAreTypedAndNameThePath(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		lines map[string][]gomutants.LineRange
		says  string
	}{
		{
			name:  "an absolute path",
			lines: map[string][]gomutants.LineRange{"/etc/passwd.go": {{First: 1, Last: 1}}},
			says:  `"/etc/passwd.go"`,
		},
		{
			name:  "a path that escapes the module",
			lines: map[string][]gomutants.LineRange{"../elsewhere/x.go": {{First: 1, Last: 1}}},
			says:  `"../elsewhere/x.go"`,
		},
		{
			name:  "a range counted from zero",
			lines: map[string][]gomutants.LineRange{"simple.go": {{First: 0, Last: 4}}},
			says:  `"simple.go"`,
		},
		{
			name:  "a range that runs backwards",
			lines: map[string][]gomutants.LineRange{"simple.go": {{First: 9, Last: 4}}},
			says:  `"simple.go"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := testkit.Copy(t, "simple")
			workspace, err := gomutants.Open(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = workspace.Close() })

			session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
				SkipVerify: true,
				Selection:  &gomutants.Selection{Lines: maps.Clone(test.lines)},
			})
			if err == nil {
				_ = session.Close()
				t.Fatal("Prepare accepted a selection nothing could satisfy, want a refusal")
			}
			if !errors.Is(err, gomutants.ErrInvalidSelection) {
				t.Errorf("Prepare = %v, which errors.Is does not match ErrInvalidSelection;"+
					" a consumer cannot tell its user's mistake from an engine failure", err)
			}
			if !strings.Contains(err.Error(), test.says) {
				t.Errorf("Prepare = %q, which does not name %s; the caller has to be told which"+
					" entry it wrote wrong", err, test.says)
			}
		})
	}
}
