// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/checkpoint"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const resumeTargetLine = 7

func resumeTarget() goanalysis.Target {
	return goanalysis.Target{
		ID: "target-a", Name: "TestValue", Kind: goanalysis.KindTest,
		Package: "fixture.example/module/pkg", Path: "pkg/value_test.go", Line: resumeTargetLine,
	}
}

func resumeUnit(target goanalysis.Target) checkpoint.BaselineTarget {
	return checkpoint.BaselineTarget{
		ID: target.ID,
		Inventory: report.TargetDisposition{
			ID: target.ID, Name: target.Name, Kind: string(target.Kind), Package: target.Package,
			Path: target.Path, Line: target.Line, Status: "passed",
		},
	}
}

func resumeController(t *testing.T, state checkpoint.State) *runCheckpointController {
	t.Helper()
	cache := &coordinatorCache{checkpoint: state, checkpointFound: true}
	controller := openRunCheckpoint(cache, "digest", Options{}, true)
	if controller == nil {
		t.Fatal("the checkpoint controller refused to open")
	}
	return controller
}

func TestASavedBaselineIsDiscardedWhenAnyPartOfItsInventoryMoved(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*checkpoint.BaselineTarget)
		keeps  bool
	}{
		{name: "an inventory that still matches", change: func(*checkpoint.BaselineTarget) {}, keeps: true},
		{name: "an identity that is no longer there",
			change: func(unit *checkpoint.BaselineTarget) { unit.ID = "target-gone" }},
		{name: "a name that moved",
			change: func(unit *checkpoint.BaselineTarget) { unit.Inventory.Name = "TestOther" }},
		{name: "a kind that changed",
			change: func(unit *checkpoint.BaselineTarget) { unit.Inventory.Kind = "fuzz" }},
		{name: "a package that changed",
			change: func(unit *checkpoint.BaselineTarget) { unit.Inventory.Package = "fixture.example/module/other" }},
		{name: "a path that changed",
			change: func(unit *checkpoint.BaselineTarget) { unit.Inventory.Path = "pkg/other_test.go" }},
		{name: "a line that moved",
			change: func(unit *checkpoint.BaselineTarget) { unit.Inventory.Line = resumeTargetLine + 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target := resumeTarget()
			unit := resumeUnit(target)
			test.change(&unit)
			controller := resumeController(t, checkpoint.State{
				Schema: checkpoint.SchemaV1, InputDigest: "digest",
				Baseline: checkpoint.Baseline{Targets: []checkpoint.BaselineTarget{unit}, BuildVetComplete: true},
			})
			resumed := controller.baseline([]goanalysis.Target{target})
			if kept := resumed != nil; kept != test.keeps {
				t.Fatalf("%s kept the saved baseline=%t, want %t", test.name, kept, test.keeps)
			}
		})
	}
}

func TestASavedBaselineIsDiscardedWhenARoutedTargetNoLongerAgrees(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*goanalysis.Target)
		keeps  bool
	}{
		{name: "a routed target that still agrees", change: func(*goanalysis.Target) {}, keeps: true},
		{name: "a routed identity that changed", change: func(t *goanalysis.Target) { t.ID = "target-other" }},
		{name: "a routed path that changed", change: func(t *goanalysis.Target) { t.Path = "pkg/other_test.go" }},
		{name: "a routed package that changed",
			change: func(t *goanalysis.Target) { t.Package = "fixture.example/module/other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target := resumeTarget()
			unit := resumeUnit(target)
			routed := resumeTarget()
			test.change(&routed)
			unit.Target = &checkpoint.TargetEvidence{Target: checkpoint.Target{
				ID: routed.ID, Path: routed.Path, Package: routed.Package,
			}}
			controller := resumeController(t, checkpoint.State{
				Schema: checkpoint.SchemaV1, InputDigest: "digest",
				Baseline: checkpoint.Baseline{Targets: []checkpoint.BaselineTarget{unit}, BuildVetComplete: true},
			})
			resumed := controller.baseline([]goanalysis.Target{target})
			if kept := resumed != nil; kept != test.keeps {
				t.Fatalf("%s kept the saved baseline=%t, want %t", test.name, kept, test.keeps)
			}
		})
	}
}

func TestACompletedBaselineIsDiscardedWhenTheTargetCountMoved(t *testing.T) {
	t.Parallel()
	target := resumeTarget()
	other := resumeTarget()
	other.ID, other.Name = "target-b", "TestOther"
	for _, test := range []struct {
		name     string
		complete bool
		targets  []goanalysis.Target
		keeps    bool
	}{
		{name: "a complete baseline of the same count", complete: true,
			targets: []goanalysis.Target{target}, keeps: true},
		{name: "a complete baseline of another count", complete: true,
			targets: []goanalysis.Target{target, other}},
		{name: "an incomplete baseline of another count",
			targets: []goanalysis.Target{target, other}, keeps: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			controller := resumeController(t, checkpoint.State{
				Schema: checkpoint.SchemaV1, InputDigest: "digest",
				Baseline: checkpoint.Baseline{
					Targets:  []checkpoint.BaselineTarget{resumeUnit(target)},
					Complete: test.complete, BuildVetComplete: true,
				},
			})
			resumed := controller.baseline(test.targets)
			if kept := resumed != nil; kept != test.keeps {
				t.Fatalf("%s kept the saved baseline=%t, want %t", test.name, kept, test.keeps)
			}
		})
	}
}

func TestASavedBaselineIsDiscardedWhenASuitesPackageIsGone(t *testing.T) {
	t.Parallel()
	target := resumeTarget()
	for _, test := range []struct {
		name  string
		pkg   string
		keeps bool
	}{
		{name: "a suite of a package the run still holds", pkg: target.Package, keeps: true},
		{name: "a suite of a package the run no longer holds", pkg: "fixture.example/module/gone"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			controller := resumeController(t, checkpoint.State{
				Schema: checkpoint.SchemaV1, InputDigest: "digest",
				Baseline: checkpoint.Baseline{
					Targets:          []checkpoint.BaselineTarget{resumeUnit(target)},
					Suites:           []checkpoint.BaselineSuite{{Package: test.pkg}},
					BuildVetComplete: true,
				},
			})
			resumed := controller.baseline([]goanalysis.Target{target})
			if kept := resumed != nil; kept != test.keeps {
				t.Fatalf("%s kept the saved baseline=%t, want %t", test.name, kept, test.keeps)
			}
		})
	}
}

func TestABaselineWithNothingSavedResumesNothing(t *testing.T) {
	t.Parallel()
	target := resumeTarget()
	for _, test := range []struct {
		name     string
		buildVet bool
		targets  []checkpoint.BaselineTarget
		resumes  bool
	}{
		{name: "nothing done and nothing classified"},
		{name: "the build and vet done alone", buildVet: true, resumes: true},
		{name: "a target classified alone", targets: []checkpoint.BaselineTarget{resumeUnit(target)}, resumes: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			controller := resumeController(t, checkpoint.State{
				Schema: checkpoint.SchemaV1, InputDigest: "digest",
				Baseline: checkpoint.Baseline{Targets: test.targets, BuildVetComplete: test.buildVet},
			})
			resumed := controller.baseline([]goanalysis.Target{target})
			if resumes := resumed != nil; resumes != test.resumes {
				t.Fatalf("%s resumed=%t, want %t", test.name, resumes, test.resumes)
			}
		})
	}
	if none := (*runCheckpointController)(nil).baseline([]goanalysis.Target{target}); none != nil {
		t.Errorf("a run with no checkpoint controller resumed %+v, want nothing", none)
	}
}
