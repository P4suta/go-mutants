// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"slices"
	"testing"

	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

const (
	decideLine        = 11
	decideColumn      = 4
	decideStartLine   = 10
	decideStartColumn = 2
	decideEndLine     = 12
	decideEndColumn   = 16

	linesPastTheBody = 5
)

func decideEvidence(t *testing.T, profiles map[string][]string) evidence {
	t.Helper()
	recorded, err := readEvidence(writeProfiles(t, profiles), fixtureModule)
	if err != nil {
		t.Fatal(err)
	}
	return recorded
}

func decidePair(change func(*killPair)) killPair {
	pair := killPair{
		mutant: firstMutant, path: subjectPath,
		line: decideLine, column: decideColumn, target: killerTarget, killer: killerTarget,
	}
	if change != nil {
		change(&pair)
	}
	return pair
}

func TestDecideReachAnswersForEveryShapeOfCoverageItIsGiven(t *testing.T) {
	t.Parallel()
	covering := map[string][]string{killerTarget: {ran(decideStartLine, decideStartColumn, decideEndLine, decideEndColumn)}}
	elsewhere := map[string][]string{killerTarget: {
		ran(decideEndLine+10, 1, decideEndLine+11, 1),
		linked(decideStartLine, decideStartColumn, decideEndLine, decideEndColumn),
	}}
	for _, test := range []struct {
		name     string
		profiles map[string][]string
		change   func(*killPair)
		want     conclusion
		why      string
	}{
		{name: "a killer that covers the position", profiles: covering, want: kept},
		{
			name: "a killer that left no profile", profiles: covering,
			change: func(pair *killPair) { pair.target = "TestAbsent" },
			want:   unverifiable, why: whyNoProfile,
		},
		{
			name: "a killer that covers no block of the file", profiles: covering,
			change: func(pair *killPair) { pair.path = "pkg/other.go" },
			want:   discharged, why: whyCoversNoneOfTheFile,
		},
		{
			name: "a killer whose blocks never ran", profiles: elsewhere,
			want: discharged, why: whyOutsideCoveredBlocks,
		},
		{
			name: "a position outside every instrumented block", profiles: elsewhere,
			change: func(pair *killPair) { pair.line = decideEndLine + linesPastTheBody },
			want:   kept,
		},
		{
			name: "a position whose column the recording never carried", profiles: elsewhere,
			change: func(pair *killPair) { pair.column = 0 },
			want:   kept,
		},
		{
			name: "a position at the first column of a covered block", profiles: covering,
			change: func(pair *killPair) { pair.line, pair.column = decideStartLine, decideStartColumn },
			want:   kept,
		},
		{
			name: "an evidence target that names another profile", profiles: covering,
			change: func(pair *killPair) { pair.target, pair.evidenceTarget = "TestAbsent", killerTarget },
			want:   kept,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := decideReach(decidePair(test.change), decideEvidence(t, test.profiles))
			if got.conclusion != test.want || got.why != test.why {
				t.Fatalf("decideReach = %+v, want conclusion %d because %q", got, test.want, test.why)
			}
		})
	}
}

func TestDecideSuiteReachAsksTheSuiteWhatItInstrumentedAndWhatItRan(t *testing.T) {
	t.Parallel()
	covering := map[string][]string{killerTarget: {ran(decideStartLine, decideStartColumn, decideEndLine, decideEndColumn)}}
	elsewhere := map[string][]string{killerTarget: {
		ran(decideEndLine+10, 1, decideEndLine+11, 1),
		linked(decideStartLine, decideStartColumn, decideEndLine, decideEndColumn),
	}}
	for _, test := range []struct {
		name     string
		profiles map[string][]string
		change   func(*killPair)
		want     conclusion
		why      string
	}{
		{name: "a suite that ran the position", profiles: covering, want: kept},
		{
			name: "a suite that left no profile", profiles: covering,
			change: func(pair *killPair) { pair.target = "TestAbsent" },
			want:   unverifiable, why: whyNoProfile,
		},
		{
			name: "a position the suite never instrumented", profiles: covering,
			change: func(pair *killPair) { pair.line = decideEndLine + linesPastTheBody },
			want:   kept,
		},
		{
			name: "a position the suite instrumented and never ran", profiles: elsewhere,
			want: discharged, why: whyOutsideCoveredBlocks,
		},
		{
			name: "a position whose column the recording never carried", profiles: elsewhere,
			change: func(pair *killPair) { pair.column = 0 },
			want:   kept,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := decideSuiteReach(decidePair(test.change), decideEvidence(t, test.profiles))
			if got.conclusion != test.want || got.why != test.why {
				t.Fatalf("decideSuiteReach = %+v, want conclusion %d because %q", got, test.want, test.why)
			}
		})
	}
}

func TestDecideInfectionAnswersForEveryShapeOfProbeFactItIsGiven(t *testing.T) {
	t.Parallel()
	measured := &probeFacts{outcome: trace.ProbeOutcomeMeasured, infected: map[string]struct{}{}}
	infecting := &probeFacts{
		outcome: trace.ProbeOutcomeMeasured, infected: map[string]struct{}{firstMutant: {}},
	}
	for _, test := range []struct {
		name   string
		change func(*killPair)
		want   conclusion
		why    string
	}{
		{
			name:   "a route of another granularity",
			change: func(pair *killPair) { pair.granularity = trace.GranularityFile; pair.probed = true },
			want:   inapplicable,
		},
		{
			name:   "a route nothing probed",
			change: func(pair *killPair) { pair.granularity = trace.GranularityBlock },
			want:   inapplicable,
		},
		{
			name:   "a probed route with no facts",
			change: func(pair *killPair) { pair.probed = true },
			want:   kept,
		},
		{
			name: "a target probed twice",
			change: func(pair *killPair) {
				pair.probed, pair.probe = true, &probeFacts{conflicting: true}
			},
			want: unverifiable, why: whyProbeRecordedTwice,
		},
		{
			name: "a probe that measured nothing",
			change: func(pair *killPair) {
				pair.probed, pair.probe = true, &probeFacts{outcome: trace.ProbeOutcomeUnavailable}
			},
			want: kept,
		},
		{
			name:   "a probe that saw the mutant infect",
			change: func(pair *killPair) { pair.probed, pair.probe = true, infecting },
			want:   kept,
		},
		{
			name:   "a probe that measured and saw nothing",
			change: func(pair *killPair) { pair.probed, pair.probe = true, measured },
			want:   discharged, why: whyNeverInfected,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := decideInfection(decidePair(test.change), evidence{})
			if got.conclusion != test.want || got.why != test.why {
				t.Fatalf("decideInfection = %+v, want conclusion %d because %q", got, test.want, test.why)
			}
		})
	}
}

func TestDecideBranchAnswersForEveryShapeOfCatalogItIsGiven(t *testing.T) {
	t.Parallel()
	covering := map[string][]string{killerTarget: {ran(decideStartLine, decideStartColumn, decideEndLine, decideEndColumn)}}
	body := &branchProof{
		BodyStartLine: decideStartLine, BodyStartColumn: decideStartColumn,
		BodyEndLine: decideEndLine, BodyEndColumn: decideEndColumn,
	}
	listedWith := func(change func(*catalogMutant)) *mutantCatalog {
		listed := catalogMutant{
			ID: firstMutant, Path: subjectPath,
			Line: decideStartLine - 1, Column: 1, Branch: body,
		}
		if change != nil {
			change(&listed)
		}
		return &mutantCatalog{mutants: map[string]catalogMutant{listed.ID: listed}}
	}
	for _, test := range []struct {
		name     string
		catalog  *mutantCatalog
		profiles map[string][]string
		change   func(*killPair)
		want     conclusion
		why      string
	}{
		{
			name: "a mutant the catalog does not list", catalog: &mutantCatalog{mutants: map[string]catalogMutant{}},
			profiles: covering, want: unverifiable, why: whyNotInCatalog,
		},
		{
			name:     "a mutant that gates no branch",
			catalog:  listedWith(func(m *catalogMutant) { m.Branch = nil }),
			profiles: covering, want: inapplicable,
		},
		{
			name:     "a branch whose positions make no sense",
			catalog:  listedWith(func(m *catalogMutant) { m.Line = 0 }),
			profiles: covering, want: kept,
		},
		{
			name:     "a killer that left no profile",
			catalog:  listedWith(nil),
			profiles: covering,
			change:   func(pair *killPair) { pair.target = "TestAbsent" },
			want:     unverifiable, why: whyNoProfile,
		},
		{
			name:     "a killer that ran the body",
			catalog:  listedWith(nil),
			profiles: covering, want: kept,
		},
		{
			name:    "a killer that never took the body",
			catalog: listedWith(nil),
			profiles: map[string][]string{killerTarget: {
				ran(decideEndLine+10, 1, decideEndLine+11, 1),
				linked(decideStartLine, decideStartColumn, decideEndLine, decideEndColumn),
			}},
			want: discharged, why: whyBodyNeverTaken,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := decideBranch(test.catalog, decidePair(test.change), decideEvidence(t, test.profiles))
			if got.conclusion != test.want || got.why != test.why {
				t.Fatalf("decideBranch = %+v, want conclusion %d because %q", got, test.want, test.why)
			}
		})
	}
}

func TestStartsInBodyAsksWhetherAnyBlockOpensInside(t *testing.T) {
	t.Parallel()
	body := branchProof{
		BodyStartLine: decideStartLine, BodyStartColumn: decideStartColumn,
		BodyEndLine: decideEndLine, BodyEndColumn: decideEndColumn,
	}
	inside := goanalysis.FileCoverage{Blocks: []goanalysis.CoverageBlock{
		{StartLine: decideStartLine, StartColumn: decideStartColumn},
	}}
	outside := goanalysis.FileCoverage{Blocks: []goanalysis.CoverageBlock{
		{StartLine: decideStartLine - 1, StartColumn: decideStartColumn},
	}}
	if !startsInBody(inside, body) {
		t.Fatal("a block opening exactly where the body starts was said to be outside it")
	}
	if startsInBody(outside, body) {
		t.Fatal("a block opening before the body was said to be inside it")
	}
	if startsInBody(goanalysis.FileCoverage{}, body) {
		t.Fatal("a file with no blocks was said to open one inside the body")
	}
}

func TestKillerTestsReadsTheSelectorATestBinaryWasGiven(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		arguments []string
		want      []string
		selective bool
	}{
		{name: "no selector at all", arguments: []string{"-test.count=1"}},
		{name: "one bare name", arguments: []string{runArgument + "TestOne"}, want: []string{"TestOne"}, selective: true},
		{
			name:      "one anchored name",
			arguments: []string{runArgument + "^TestOne$"}, want: []string{"TestOne"}, selective: true,
		},
		{
			name:      "a group of names",
			arguments: []string{runArgument + "^(TestOne|TestTwo)$"},
			want:      []string{"TestOne", "TestTwo"}, selective: true,
		},
		{
			name:      "a group of one",
			arguments: []string{runArgument + "^(TestOne)$"}, want: []string{"TestOne"}, selective: true,
		},
		{name: "a selector that names nothing", arguments: []string{runArgument + "^$"}, selective: true},
		{
			name:      "the selector among other arguments",
			arguments: []string{"-test.count=1", runArgument + "^TestOne$", "-test.v"},
			want:      []string{"TestOne"}, selective: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, selective := killerTests(test.arguments)
			if selective != test.selective || !slices.Equal(got, test.want) {
				t.Fatalf("killerTests(%q) = (%q, %t), want (%q, %t)",
					test.arguments, got, selective, test.want, test.selective)
			}
		})
	}
}

func TestGoCommandNameRecognisesTheToolAndNothingBesideIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: "go", want: true},
		{name: "go.exe", want: true},
		{name: "/usr/local/bin/go", want: true},
		{name: "/go", want: true},
		{name: `C:\tools\go.exe`, want: true},
		{name: "gofmt"},
		{name: "/usr/local/bin/gofmt"},
		{name: ""},
	} {
		t.Run("name "+test.name, func(t *testing.T) {
			t.Parallel()
			if got := goCommandName(test.name); got != test.want {
				t.Fatalf("goCommandName(%q) = %t, want %t", test.name, got, test.want)
			}
		})
	}
}

func TestProfileTargetNamesTheTargetAProfileBelongsTo(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "TestOne" + profileSuffix, want: "TestOne"},
		{path: "profiles/TestOne" + profileSuffix, want: "TestOne"},
		{path: "/TestOne" + profileSuffix, want: "TestOne"},
		{path: `profiles\TestOne` + profileSuffix, want: "TestOne"},
		{path: "TestOne.txt"},
		{path: profileSuffix},
	} {
		t.Run("path "+test.path, func(t *testing.T) {
			t.Parallel()
			if got := profileTarget(test.path); got != test.want {
				t.Fatalf("profileTarget(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func TestCompareRowsOrdersByPositionThenIdentityThenLayer(t *testing.T) {
	t.Parallel()
	row := func(path string, line, column int, mutant, target, layer string) auditRow {
		return auditRow{
			pair:  killPair{path: path, line: line, column: column, mutant: mutant, target: target},
			layer: layer,
		}
	}
	base := row("a.go", 2, 3, "m-2", "TestB", "reach")
	for _, test := range []struct {
		name  string
		first auditRow
		want  int
	}{
		{name: "an earlier path", first: row("0.go", 9, 9, "m-9", "TestZ", "suite-reach"), want: -1},
		{name: "a later path", first: row("z.go", 1, 1, "m-1", "TestA", "branch"), want: 1},
		{name: "an earlier line", first: row("a.go", 1, 9, "m-9", "TestZ", "suite-reach"), want: -1},
		{name: "an earlier column", first: row("a.go", 2, 1, "m-9", "TestZ", "suite-reach"), want: -1},
		{name: "an earlier mutant", first: row("a.go", 2, 3, "m-1", "TestZ", "suite-reach"), want: -1},
		{name: "an earlier target", first: row("a.go", 2, 3, "m-2", "TestA", "suite-reach"), want: -1},
		{name: "an earlier layer", first: row("a.go", 2, 3, "m-2", "TestB", "branch"), want: -1},
		{name: "the same row", first: base},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := compareRows(test.first, base); got != test.want {
				t.Fatalf("compareRows(%+v, %+v) = %d, want %d", test.first, base, got, test.want)
			}
		})
	}
}

func TestAPairIsPositionedOnlyWhenBothOfItsHalvesAreAboveZero(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		line   int
		column int
		want   bool
	}{
		{name: "a position the recording carried", line: decideLine, column: decideColumn, want: true},
		{name: "the first position of a file", line: 1, column: 1, want: true},
		{name: "no line", column: decideColumn},
		{name: "no column", line: decideLine},
		{name: "neither"},
		{name: "a line before the first", line: -1, column: decideColumn},
		{name: "a column before the first", line: decideLine, column: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			pair := decidePair(func(pair *killPair) { pair.line, pair.column = test.line, test.column })
			if got := pair.positioned(); got != test.want {
				t.Fatalf("a pair at %d:%d reports positioned %t, want %t",
					test.line, test.column, got, test.want)
			}
		})
	}
}

func TestAfterTheLastSeparatorKeepsEverythingWhenThereIsNone(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ path, want string }{
		{path: "go", want: "go"},
		{path: "", want: ""},
		{path: "/", want: ""},
		{path: "/usr/bin/go", want: "go"},
		{path: `C:\tools\go.exe`, want: "go.exe"},
		{path: "a/b\\c", want: "c"},
	} {
		t.Run("path "+test.path, func(t *testing.T) {
			t.Parallel()
			if got := afterLastSeparator(test.path); got != test.want {
				t.Fatalf("afterLastSeparator(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}
