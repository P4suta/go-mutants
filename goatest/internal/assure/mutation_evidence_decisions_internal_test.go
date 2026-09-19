// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"reflect"
	"slices"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/goatest/internal/evidence"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

func evidenceDisposition(target TargetEvidence, status string) report.TargetDisposition {
	return report.TargetDisposition{
		ID: target.Target.ID, Name: target.Target.Name, Kind: string(target.Target.Kind),
		Package: target.Target.Package, Status: status,
	}
}

func TestTargetKeySourcesIndexOnlyOwnedCorpusEntries(t *testing.T) {
	t.Parallel()
	const valid = "testdata/fuzz/FuzzValue/seed"
	sources := newTargetKeySources(evidence.Inputs{Corpus: map[string]string{
		valid: "valid", "docs/notes.md": "invalid",
	}}, goanalysis.Model{}, "standard-v1", Options{}, nil)
	if got := sources.corpus[".\x00FuzzValue"]; !reflect.DeepEqual(got, []string{valid}) {
		t.Fatalf("owned corpus = %v", got)
	}
	if _, indexed := sources.corpus["\x00"]; indexed || len(sources.corpus) != 1 {
		t.Fatalf("corpus index = %+v", sources.corpus)
	}
}

func TestCorpusOwnerAcceptsOnlyNamedFuzzCorpora(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		path       string
		owner      string
		target     string
		identified bool
	}{
		{name: "root corpus", path: "testdata/fuzz/FuzzRoot/seed", owner: ".", target: "FuzzRoot", identified: true},
		{name: "nested corpus", path: "pkg/testdata/fuzz/FuzzNested/seed", owner: "pkg", target: "FuzzNested", identified: true},
		{name: "ordinary file", path: "value.go"},
		{name: "ordinary testdata", path: "testdata/golden.txt"},
		{name: "unnamed corpus", path: "testdata/fuzz//seed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			owner, target, identified := corpusOwner(test.path)
			if owner != test.owner || target != test.target || identified != test.identified {
				t.Fatalf("corpus owner = (%q, %q, %t)", owner, target, identified)
			}
		})
	}
}

func TestNarrowTargetInputsIncludeOnlyFilesThatExistInAnInputSet(t *testing.T) {
	t.Parallel()
	const (
		pkg      = "fixture.example/module"
		embedded = "embedded.bin"
		missing  = "missing.bin"
	)
	sources := newTargetKeySources(evidence.Inputs{
		Files: map[string]string{"value.go": "value"}, Corpus: map[string]string{embedded: "embedded"},
	}, goanalysis.Model{Packages: []goanalysis.Package{{
		ImportPath: pkg, RelativeDir: ".", EmbedFiles: []string{embedded, missing},
	}}}, "standard-v1", Options{}, nil)
	inputs := sources.narrowInputsFor(goanalysis.Target{Package: pkg})
	if inputs.Files["value.go"] != "value" || inputs.Files[embedded] != "embedded" {
		t.Fatalf("narrow files = %+v", inputs.Files)
	}
	for _, absent := range []string{"go.mod", "go.sum", missing} {
		if _, included := inputs.Files[absent]; included {
			t.Fatalf("missing input %q was included: %+v", absent, inputs.Files)
		}
	}
}

func TestNarrowTargetInputsCreateCorpusOnlyForTheMatchingFuzzer(t *testing.T) {
	t.Parallel()
	sources := targetKeyFixture()
	testTarget := goanalysis.Target{Package: evidenceModule, RelativeDir: ".", Name: "TestValue", Kind: goanalysis.KindTest}
	fuzzTarget := goanalysis.Target{Package: evidenceModule, RelativeDir: ".", Name: "FuzzValue", Kind: goanalysis.KindFuzz}
	if corpus := sources.narrowInputsFor(testTarget).Corpus; corpus != nil {
		t.Fatalf("test corpus = %+v, want nil", corpus)
	}
	want := map[string]string{"testdata/fuzz/FuzzValue/seed-a": digestText("seed-a")}
	if corpus := sources.narrowInputsFor(fuzzTarget).Corpus; !reflect.DeepEqual(corpus, want) {
		t.Fatalf("fuzz corpus = %+v, want %+v", corpus, want)
	}
}

func TestWholeTreeInputsRetainCorpusDigests(t *testing.T) {
	t.Parallel()
	sources := repositoryReaderKeyFixture(map[string]bool{evidenceModule: true})
	target := goanalysis.Target{Package: evidenceModule, RelativeDir: "."}
	inputs := sources.wholeTreeInputsFor(target)
	const corpus = "testdata/fuzz/FuzzValue/seed-a"
	if inputs.Files[corpus] != sources.inputs.Corpus[corpus] {
		t.Fatalf("whole-tree corpus digest = %q, want %q", inputs.Files[corpus], sources.inputs.Corpus[corpus])
	}
}

func TestWholeTreeKeysRequireARepositoryReader(t *testing.T) {
	t.Parallel()
	target := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond).Target
	if key := targetKeyFixture().targetKey(target, nil, true); key != "" {
		t.Fatalf("non-reader whole-tree target key = %q", key)
	}
	reader := repositoryReaderKeyFixture(map[string]bool{evidenceModule: true})
	if key := reader.targetKey(target, nil, true); key == "" {
		t.Fatal("repository reader has no whole-tree target key")
	}
	if key := targetKeyFixture().suiteKey("unknown/package", nil, nil, false); key != "" {
		t.Fatalf("unknown suite key = %q", key)
	}
	if key := targetKeyFixture().suiteKey(evidenceModule, nil, nil, true); key != "" {
		t.Fatalf("non-reader whole-tree suite key = %q", key)
	}
	if key := reader.suiteKey(evidenceModule, nil, nil, true); key == "" {
		t.Fatal("repository reader has no whole-tree suite key")
	}
}

func TestTargetPackageClosureExcludesSelfDuplicatesAndUnknownPackages(t *testing.T) {
	t.Parallel()
	sources := targetKeyFixture()
	if closure := sources.closure(goanalysis.Target{Package: "unknown/package", Dependencies: []string{"also/unknown"}}); len(closure) != 0 {
		t.Fatalf("unknown closure = %+v", closure)
	}
	closure := sources.closure(goanalysis.Target{
		Package:      evidenceModule,
		Dependencies: []string{evidenceModule, evidenceModule + "/internal/helper", "unknown/package"},
	})
	want := []string{evidenceModule, evidenceModule + "/internal/helper"}
	got := make([]string, len(closure))
	for index, pkg := range closure {
		got[index] = pkg.ImportPath
	}
	if !slices.Equal(got, want) {
		t.Fatalf("closure = %v, want %v", got, want)
	}
}

func TestRunMutationEvidenceTracksOnlyPassedTargetsAndKeepsWholeTreeSticky(t *testing.T) {
	t.Parallel()
	first := evidenceTarget("TestFirst", goanalysis.KindTest, time.Millisecond)
	first.WholeTree = true
	second := evidenceTarget("TestSecond", goanalysis.KindTest, time.Millisecond)
	collected := newRunMutationEvidence(
		evidence.MutationStore{}, targetKeyFixture(), []TargetEvidence{first, second},
		[]report.TargetDisposition{evidenceDisposition(first, "passed"), evidenceDisposition(second, "failed")},
		nil, "snapshot",
	)
	if !collected.passed[identify(first.Target)] || collected.passed[identify(second.Target)] {
		t.Fatalf("passed targets = %+v", collected.passed)
	}
	if !collected.suiteBaselineWhole[evidenceModule] {
		t.Fatal("a later narrow target erased the package whole-tree baseline")
	}
}

func TestSuiteKeysDoNotPublishAnEmptyWholeTreeKey(t *testing.T) {
	t.Parallel()
	target := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond)
	identity := identify(target.Target)
	keys := suiteKeys(targetKeyFixture(), []TargetEvidence{target},
		map[targetIdentity]string{identity: "target-key"}, map[targetIdentity]bool{identity: true}, nil, true)
	if key, published := keys[evidenceModule]; published || key != "" {
		t.Fatalf("empty whole-tree suite key was published: %+v", keys)
	}
}

func TestWholeTargetKeyCachesUnknownTargetsAsUnavailable(t *testing.T) {
	t.Parallel()
	identity := evidenceIdentity("Missing", goanalysis.KindTest)
	collected := matchingEvidence(t)
	collected.sources = repositoryReaderKeyFixture(map[string]bool{evidenceModule: true})
	if key := collected.wholeTargetKey(identity); key != "" || collected.wholeKeys[identity] != "" {
		t.Fatalf("unknown whole target key = %q, cache %+v", key, collected.wholeKeys)
	}
}

func TestWholeSuiteKeyUsesOnlyMeasuredTargetsOfItsPackage(t *testing.T) {
	t.Parallel()
	readers := map[string]bool{evidenceModule: true, evidenceModule + "/other": true}
	sources := repositoryReaderKeyFixture(readers)
	main := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond)
	other := evidenceTarget("TestOther", goanalysis.KindTest, time.Millisecond)
	other.Target.Package = evidenceModule + "/other"
	other.Target.RelativeDir = "other"
	other.Covered = nil
	collected := newRunMutationEvidence(evidence.MutationStore{}, sources, []TargetEvidence{main, other},
		[]report.TargetDisposition{evidenceDisposition(main, "passed"), evidenceDisposition(other, "passed")}, nil, "snapshot")
	key := collected.wholeSuiteKey(evidenceModule)
	identity := identify(main.Target)
	targetKey := collected.wholeTargetKey(identity)
	want := sources.suiteKey(evidenceModule, []evidence.TargetKey{{
		Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: targetKey, WholeTree: true,
	}}, nil, true)
	if key == "" || key != want {
		t.Fatalf("whole suite key = %q, want %q", key, want)
	}
}

func TestWholeSuiteKeyRejectsEachUnmeasuredTargetShape(t *testing.T) {
	t.Parallel()
	sources := repositoryReaderKeyFixture(map[string]bool{evidenceModule: true})
	for _, test := range []struct {
		name   string
		target func(TargetEvidence) TargetEvidence
		status string
	}{
		{name: "uncovered", target: func(target TargetEvidence) TargetEvidence { target.Covered = nil; return target }, status: "passed"},
		{name: "failed", target: func(target TargetEvidence) TargetEvidence { return target }, status: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target := test.target(evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond))
			collected := newRunMutationEvidence(evidence.MutationStore{}, sources, []TargetEvidence{target},
				[]report.TargetDisposition{evidenceDisposition(target, test.status)}, nil, "snapshot")
			if key := collected.wholeSuiteKey(evidenceModule); key != "" {
				t.Fatalf("unmeasured whole suite key = %q", key)
			}
		})
	}
}

func TestContainsTargetSetRequiresCardinalityIdentityPassAndKey(t *testing.T) {
	t.Parallel()
	target := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond)
	identity := identify(target.Target)
	key := digestText("target-key")
	collected := newMutationEvidence(evidence.MutationStore{}, map[targetIdentity]string{identity: key},
		map[targetIdentity]bool{identity: true}, nil, "snapshot")
	base := evidence.TargetKey{Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: key}
	if !collected.containsTargetSet([]TargetEvidence{target}, []evidence.TargetKey{base}) {
		t.Fatal("matching target set did not match")
	}
	if collected.containsTargetSet([]TargetEvidence{target}, []evidence.TargetKey{base, base}) {
		t.Fatal("a larger recorded set reused one target twice")
	}
	for _, change := range []func(*evidence.TargetKey){
		func(candidate *evidence.TargetKey) { candidate.Package = "other/package" },
		func(candidate *evidence.TargetKey) { candidate.Name = "TestOther" },
		func(candidate *evidence.TargetKey) { candidate.Kind = string(goanalysis.KindFuzz) },
		func(candidate *evidence.TargetKey) { candidate.Key = "other-key" },
	} {
		candidate := base
		change(&candidate)
		if collected.containsTargetSet([]TargetEvidence{target}, []evidence.TargetKey{candidate}) {
			t.Fatalf("mismatched candidate was accepted: %+v", candidate)
		}
	}
}

func TestReuseKillRequiresAKnownKilledRecordWithKiller(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("reuse-kill-decisions")
	target := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond)
	identity := identify(target.Target)
	key := digestText("target-key")
	killer := evidence.TargetKey{Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: key}
	for _, test := range []struct {
		name   string
		record *evidence.MutationRecord
		reused bool
	}{
		{name: "unknown"},
		{name: "survived", record: &evidence.MutationRecord{MutantID: mutant.ID, Outcome: evidence.MutationOutcomeSurvived, KilledBy: []evidence.TargetKey{killer}}},
		{name: "no killer", record: &evidence.MutationRecord{MutantID: mutant.ID, Outcome: evidence.MutationOutcomeKilled}},
		{name: "killed", reused: true, record: &evidence.MutationRecord{
			MutantID: mutant.ID, Outcome: evidence.MutationOutcomeKilled, KilledBy: []evidence.TargetKey{killer}, Provenance: "snapshot=earlier",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var records []evidence.MutationRecord
			if test.record != nil {
				records = append(records, *test.record)
			}
			collected := newMutationEvidence(evidence.MutationStore{Records: records},
				map[targetIdentity]string{identity: key}, map[targetIdentity]bool{identity: true}, nil, "snapshot=current")
			detail, provenance, reused := collected.reuseKill(mutant, mutationRoute{reaching: []TargetEvidence{target}})
			if reused != test.reused {
				t.Fatalf("reuse = %t, want %t", reused, test.reused)
			}
			if reused && (detail != target.Target.Name || provenance != test.record.Provenance) {
				t.Fatalf("reused kill = (%q, %q)", detail, provenance)
			}
		})
	}
}

func TestMutationKillRecordDetailDistinguishesOneAndRelatedTargets(t *testing.T) {
	t.Parallel()
	one := evidence.TargetKey{Package: evidenceModule, Name: "TestOne"}
	two := evidence.TargetKey{Package: evidenceModule, Name: "TestTwo"}
	if got := mutationKillRecordDetail([]evidence.TargetKey{one}); got != one.Name {
		t.Fatalf("single detail = %q", got)
	}
	if got := mutationKillRecordDetail([]evidence.TargetKey{one, two}); got != evidenceModule+" (2 related targets)" {
		t.Fatalf("related detail = %q", got)
	}
}

func TestReuseVerdictRequiresAKnownSupportedRecordWithFinding(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("reuse-verdict-decisions")
	target := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond)
	identity := identify(target.Target)
	key := digestText("target-key")
	exhausted := evidence.TargetKey{Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: key}
	finding := &evidence.FindingSeed{Kind: "surviving-mutant", Summary: "survived"}
	for _, test := range []struct {
		name   string
		record *evidence.MutationRecord
		reused bool
	}{
		{name: "unknown"},
		{name: "missing finding", record: &evidence.MutationRecord{MutantID: mutant.ID, Outcome: evidence.MutationOutcomeSurvived, Exhausted: []evidence.TargetKey{exhausted}}},
		{name: "unsupported outcome", record: &evidence.MutationRecord{MutantID: mutant.ID, Outcome: evidence.MutationOutcomeKilled, Finding: finding}},
		{name: "survived", reused: true, record: &evidence.MutationRecord{
			MutantID: mutant.ID, Outcome: evidence.MutationOutcomeSurvived, Exhausted: []evidence.TargetKey{exhausted},
			Finding: finding, Provenance: "snapshot=earlier",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var records []evidence.MutationRecord
			if test.record != nil {
				records = append(records, *test.record)
			}
			collected := newMutationEvidence(evidence.MutationStore{Records: records},
				map[targetIdentity]string{identity: key}, map[targetIdentity]bool{identity: true}, nil, "snapshot=current")
			got, provenance, reused := collected.reuseVerdict(mutant, mutationRoute{reaching: []TargetEvidence{target}})
			if reused != test.reused {
				t.Fatalf("reuse = %t, want %t", reused, test.reused)
			}
			if reused && (got != *finding || provenance != test.record.Provenance) {
				t.Fatalf("reused verdict = (%+v, %q)", got, provenance)
			}
		})
	}
}

func TestExhaustedEvidenceRequiresEveryReachingTargetIdentityAndKey(t *testing.T) {
	t.Parallel()
	target := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond)
	identity := identify(target.Target)
	key := digestText("target-key")
	collected := newMutationEvidence(evidence.MutationStore{}, map[targetIdentity]string{identity: key},
		map[targetIdentity]bool{identity: true}, nil, "snapshot")
	base := evidence.TargetKey{Package: identity.pkg, Name: identity.name, Kind: identity.kind, Key: key}
	if collected.exhausts([]evidence.TargetKey{base}, nil) || !collected.exhausts([]evidence.TargetKey{base}, []TargetEvidence{target}) {
		t.Fatal("empty or matching reaching set was classified incorrectly")
	}
	for _, change := range []func(*evidence.TargetKey){
		func(candidate *evidence.TargetKey) { candidate.Package = "other/package" },
		func(candidate *evidence.TargetKey) { candidate.Name = "TestOther" },
		func(candidate *evidence.TargetKey) { candidate.Kind = string(goanalysis.KindFuzz) },
		func(candidate *evidence.TargetKey) { candidate.Key = "other-key" },
	} {
		candidate := base
		change(&candidate)
		if collected.exhausts([]evidence.TargetKey{candidate}, []TargetEvidence{target}) {
			t.Fatalf("mismatched exhausted target was accepted: %+v", candidate)
		}
	}
}

func TestMutationEvidenceRecordersRejectEveryIncompleteInput(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("record-decisions")
	target := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond)
	identity := identify(target.Target)
	key := digestText("target-key")

	var absent *MutationEvidence
	absent.recordSuite(mutant, false, "kind", "summary")
	absent.recordExhausted(mutant, []TargetEvidence{target}, "kind", "summary")

	for _, test := range []struct {
		name    string
		targets []TargetEvidence
		kind    string
		summary string
		suite   bool
	}{
		{name: "suite kind", kind: "", summary: "summary", suite: true},
		{name: "suite summary", kind: "kind", summary: "", suite: true},
		{name: "no exhausted targets", kind: "kind", summary: "summary"},
		{name: "exhausted kind", targets: []TargetEvidence{target}, summary: "summary"},
		{name: "exhausted summary", targets: []TargetEvidence{target}, kind: "kind"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			collected := newMutationEvidence(evidence.MutationStore{}, map[targetIdentity]string{identity: key},
				map[targetIdentity]bool{identity: true}, map[string]string{mutant.Package: "suite-key"}, "snapshot")
			if test.suite {
				collected.recordSuite(mutant, false, test.kind, test.summary)
			} else {
				collected.recordExhausted(mutant, test.targets, test.kind, test.summary)
			}
			if len(collected.recorded) != 0 {
				t.Fatalf("incomplete input recorded %+v", collected.recorded)
			}
		})
	}

	noSuiteKey := newMutationEvidence(evidence.MutationStore{}, nil, nil, nil, "snapshot")
	noSuiteKey.recordSuite(mutant, false, "kind", "summary")
	if len(noSuiteKey.recorded) != 0 {
		t.Fatalf("empty suite key recorded %+v", noSuiteKey.recorded)
	}
	noTargetKey := newMutationEvidence(evidence.MutationStore{}, nil,
		map[targetIdentity]bool{identity: true}, nil, "snapshot")
	noTargetKey.recordExhausted(mutant, []TargetEvidence{target}, "kind", "summary")
	if len(noTargetKey.recorded) != 0 {
		t.Fatalf("empty target key recorded %+v", noTargetKey.recorded)
	}
}

func TestRecordKillRejectsEmptyFailedAndUnkeyedKillers(t *testing.T) {
	t.Parallel()
	mutant := evidenceMutant("record-kill-decisions")
	valid := evidenceTarget("TestValue", goanalysis.KindTest, time.Millisecond)
	identity := identify(valid.Target)
	zero := targetIdentity{}
	for _, test := range []struct {
		name   string
		target TargetEvidence
		keys   map[targetIdentity]string
		passed map[targetIdentity]bool
	}{
		{name: "empty identity", target: TargetEvidence{}, keys: map[targetIdentity]string{zero: "key"}, passed: map[targetIdentity]bool{zero: true}},
		{name: "failed", target: valid, keys: map[targetIdentity]string{identity: "key"}},
		{name: "unkeyed", target: valid, passed: map[targetIdentity]bool{identity: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			collected := newMutationEvidence(evidence.MutationStore{}, test.keys, test.passed, nil, "snapshot")
			collected.recordKill(mutant, []TargetEvidence{test.target})
			if len(collected.recorded) != 0 {
				t.Fatalf("invalid killer recorded %+v", collected.recorded)
			}
		})
	}
}

func TestMutationEvidenceStoreFiltersReplacementsAndSortsByMutant(t *testing.T) {
	t.Parallel()
	selectedA := gomutants.Mutant{ID: "a"}
	selectedB := gomutants.Mutant{ID: "b"}
	stale := evidence.MutationRecord{MutantID: "stale", Provenance: "stale"}
	oldA := evidence.MutationRecord{MutantID: "a", Provenance: "old"}
	newA := evidence.MutationRecord{MutantID: "a", Provenance: "new"}
	recordB := evidence.MutationRecord{MutantID: "b", Provenance: "kept"}
	collected := newMutationEvidence(evidence.MutationStore{Records: []evidence.MutationRecord{stale, oldA, recordB}}, nil, nil, nil, "snapshot")
	collected.recorded["a"] = newA
	collected.recorded["stale"] = stale
	stored := collected.store(gomutants.Catalog{Mutants: []gomutants.Mutant{selectedB, selectedA}}, evidenceModule)
	if !reflect.DeepEqual(stored.Records, []evidence.MutationRecord{newA, recordB}) {
		t.Fatalf("stored records = %+v", stored.Records)
	}
	if compareMutationRecords(evidence.MutationRecord{MutantID: "a"}, evidence.MutationRecord{MutantID: "b"}) >= 0 ||
		compareMutationRecords(evidence.MutationRecord{MutantID: "b"}, evidence.MutationRecord{MutantID: "a"}) <= 0 {
		t.Fatal("mutation record comparison did not order distinct IDs")
	}
	var none *MutationEvidence
	if empty := none.store(gomutants.Catalog{}, evidenceModule); !reflect.DeepEqual(empty, evidence.MutationStore{}) {
		t.Fatalf("nil evidence store = %+v", empty)
	}
}
