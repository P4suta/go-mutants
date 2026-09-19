// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/evidence"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
)

func matchingEvidence(t *testing.T) *MutationEvidence {
	t.Helper()
	return newMutationEvidence(evidence.MutationStore{
		Schema: evidence.MutationSchemaV1, ModulePath: evidenceModule,
	}, nil, nil, nil, "snapshot=fixture")
}

func TestARecordedTargetMatchesOnlyTheKeyTheRunWouldMintForIt(t *testing.T) {
	t.Parallel()
	identity := evidenceIdentity("TestValue", goanalysis.KindTest)
	for _, test := range []struct {
		name     string
		key      string
		whole    string
		baseline bool
		recorded evidence.TargetKey
		matches  bool
	}{
		{
			name: "a narrow key the run mints the same way", key: "key-a",
			recorded: evidence.TargetKey{Key: "key-a"}, matches: true,
		},
		{
			name: "a narrow key the run mints differently", key: "key-a",
			recorded: evidence.TargetKey{Key: "key-b"},
		},
		{
			name:     "a narrow key the run cannot mint at all",
			recorded: evidence.TargetKey{Key: "key-a"},
		},
		{
			name: "a narrow key for a target the baseline widened", key: "key-a", baseline: true,
			recorded: evidence.TargetKey{Key: "key-a"},
		},
		{
			name: "a whole-tree key the run mints the same way", whole: "whole-a",
			recorded: evidence.TargetKey{Key: "whole-a", WholeTree: true}, matches: true,
		},
		{
			name: "a whole-tree key the run mints differently", whole: "whole-a",
			recorded: evidence.TargetKey{Key: "whole-b", WholeTree: true},
		},
		{
			name:     "a whole-tree key the run cannot mint at all",
			recorded: evidence.TargetKey{Key: "whole-a", WholeTree: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			collected := matchingEvidence(t)
			if test.key != "" {
				collected.keys = map[targetIdentity]string{identity: test.key}
			}
			if test.whole != "" {
				collected.wholeKeys[identity] = test.whole
			}
			collected.baselineWholeTree[identity] = test.baseline
			if got := collected.targetMatches(identity, test.recorded); got != test.matches {
				t.Fatalf("%s matched=%t, want %t", test.name, got, test.matches)
			}
		})
	}
}

func TestARecordedSuiteMatchesOnlyTheKeyTheRunWouldMintForIt(t *testing.T) {
	t.Parallel()
	const pkg = evidenceModule
	for _, test := range []struct {
		name     string
		key      string
		whole    string
		baseline bool
		recorded evidence.SuiteKey
		matches  bool
	}{
		{
			name: "a narrow key the run mints the same way", key: "key-a",
			recorded: evidence.SuiteKey{Key: "key-a"}, matches: true,
		},
		{
			name: "a narrow key the run mints differently", key: "key-a",
			recorded: evidence.SuiteKey{Key: "key-b"},
		},
		{name: "a narrow key the run cannot mint at all", recorded: evidence.SuiteKey{Key: "key-a"}},
		{
			name: "a narrow key for a package the baseline widened", key: "key-a", baseline: true,
			recorded: evidence.SuiteKey{Key: "key-a"},
		},
		{
			name: "a whole-tree key the run mints the same way", whole: "whole-a",
			recorded: evidence.SuiteKey{Key: "whole-a", WholeTree: true}, matches: true,
		},
		{
			name: "a whole-tree key the run mints differently", whole: "whole-a",
			recorded: evidence.SuiteKey{Key: "whole-b", WholeTree: true},
		},
		{
			name:     "a whole-tree key the run cannot mint at all",
			recorded: evidence.SuiteKey{Key: "whole-a", WholeTree: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			collected := matchingEvidence(t)
			if test.key != "" {
				collected.suites = map[string]string{pkg: test.key}
			}
			if test.whole != "" {
				collected.wholeSuites[pkg] = test.whole
			}
			collected.suiteBaselineWhole[pkg] = test.baseline
			if got := collected.suiteMatches(pkg, test.recorded); got != test.matches {
				t.Fatalf("%s matched=%t, want %t", test.name, got, test.matches)
			}
		})
	}
}

func TestASuiteKeyIsMintedOnlyWhereEveryTargetInThePackageWasMeasured(t *testing.T) {
	t.Parallel()
	identity := evidenceIdentity("TestValue", goanalysis.KindTest)
	sources := targetKeySources{packages: map[string]goanalysis.Package{evidenceModule: {ImportPath: evidenceModule}}}
	measured := evidenceTarget("TestValue", goanalysis.KindTest, 0)
	for _, test := range []struct {
		name   string
		target func(TargetEvidence) TargetEvidence
		keys   map[targetIdentity]string
		passed map[targetIdentity]bool
		mints  bool
	}{
		{
			name:   "a target that was measured and passed",
			target: func(target TargetEvidence) TargetEvidence { return target },
			keys:   map[targetIdentity]string{identity: "key-a"},
			passed: map[targetIdentity]bool{identity: true}, mints: true,
		},
		{
			name: "a target nothing covered",
			target: func(target TargetEvidence) TargetEvidence {
				target.Covered = nil
				return target
			},
			keys:   map[targetIdentity]string{identity: "key-a"},
			passed: map[targetIdentity]bool{identity: true},
		},
		{
			name:   "a target that did not pass",
			target: func(target TargetEvidence) TargetEvidence { return target },
			keys:   map[targetIdentity]string{identity: "key-a"},
			passed: map[targetIdentity]bool{},
		},
		{
			name:   "a target the run could not key",
			target: func(target TargetEvidence) TargetEvidence { return target },
			keys:   map[targetIdentity]string{},
			passed: map[targetIdentity]bool{identity: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			suites := suiteKeys(sources, []TargetEvidence{test.target(measured)}, test.keys, test.passed, nil, false)
			if minted := len(suites) != 0; minted != test.mints {
				t.Fatalf("%s minted %v, want any=%t", test.name, suites, test.mints)
			}
		})
	}
}
