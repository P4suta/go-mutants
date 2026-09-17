// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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

var selectionRequest = gomutants.Selection{Lines: map[string][]gomutants.LineRange{
	"./clamp.go":     {{First: 41, Last: 41}, {First: 39, Last: 40}},
	"untested.go":    {{First: 1, Last: 3}},
	"docs/absent.md": {{First: 1, Last: 10}},
}}

var selectionNormalised = gomutants.Selection{Lines: map[string][]gomutants.LineRange{
	"clamp.go":       {{First: 39, Last: 41}},
	"untested.go":    {{First: 1, Last: 3}},
	"docs/absent.md": {{First: 1, Last: 10}},
}}

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

func selected(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := selectedFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the killable fixture with a selection: %v", prepared.err)
	}
	return prepared
}

func TestPrepareWithSelectionMatchesAFullRunsCatalogue(t *testing.T) {
	full := controlled(t).catalog
	narrowed := selected(t).catalog

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
	if !reflect.DeepEqual(full.Rejections, narrowed.Rejections) {
		t.Errorf("Rejections = %+v with a selection and %+v without one; validation runs over the"+
			" whole catalogue whatever the caller means to execute", narrowed.Rejections, full.Rejections)
	}

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

	for _, want := range []struct {
		path, rule string
		selected   bool
	}{
		{path: "clamp.go", rule: "lt-to-le", selected: true},
		{path: "clamp.go", rule: "gt-to-ge", selected: false},
		{path: "untested.go", rule: "neq-to-eq", selected: false},
	} {
		mutant := mutantkit.APIMutantAt(t, narrowed, want.path, want.rule)
		if mutant.Selected != want.selected {
			t.Errorf("%s in %s is at line %d-%d and is Selected=%t, want %t against %v",
				want.rule, want.path, mutant.Line, mutant.EndLine, mutant.Selected, want.selected,
				selectionNormalised.Lines[want.path])
		}
	}
	if len(narrowed.Mutants) != 3 {
		t.Errorf("the fixture catalogues %d mutants, and the rows above name 3; a mutant nobody"+
			" named is one nobody decided about", len(narrowed.Mutants))
	}
	for _, mutant := range full.Mutants {
		if !mutant.Selected {
			t.Errorf("mutant %s is not Selected in a session prepared with no selection", mutant.DisplayID)
		}
	}

	if full.PreparedDigest != narrowed.PreparedDigest {
		t.Errorf("PreparedDigest = %s with a selection and %s without one; the two sessions are"+
			" the same prepared tree and a consumer's stored evidence about it has just"+
			" stopped hitting", narrowed.PreparedDigest, full.PreparedDigest)
	}
}

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

	run, execErr := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "version"}})
	if errors.Is(execErr, gomutants.ErrPrepareFailed) {
		t.Errorf("Exec after a refused option = %v; the preparation never began, and the tree is"+
			" the one Open froze", execErr)
	}
	if execErr != nil || run.ExitCode != 0 {
		t.Errorf("Exec after a refused option = (%+v, %v), want the command to have run", run, execErr)
	}

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
