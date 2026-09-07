// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/trace"
)

// testCoveragePhase is [session.coveragePhase] for `test.narrowing = "test"`:
// it profiles every test on its own and runs each mutant against only the
// tests that reach it, rather than against every binary that does.
//
// It fails open exactly as the binary-level pass does. A profiling run that
// could not happen, a profile that will not render, or a mapping that lines up
// with nothing is a warning and a run with coverage off, never a failed run:
// coverage-guided selection is an optimisation, and a run without it reaches
// the same verdicts more slowly.
func (s *session) testCoveragePhase(
	ctx context.Context,
	opts execute.Options,
	scratch string,
	modulePath string,
	bins []execute.TestBinary,
	runs []execute.MutantRun,
	st *state,
) ([]execute.MutantRun, coverageResult, error) {
	endProfile := s.stage("coverage-profile", countNoun(len(bins), "binary"))
	collected, err := execute.CollectTestCoverage(ctx, opts, bins, filepath.Join(scratch, coverageDirName))
	endProfile(err)
	if err != nil {
		if interrupted(err) {
			return runs, coverageResult{}, err
		}
		s.unavailable(err.Error())
		return runs, coverageResult{}, nil
	}

	endNarrow := s.stage("coverage-narrow", countNoun(len(runs), "mutant"))
	covered, result, err := s.narrowToTests(ctx, opts, scratch, modulePath, bins, runs, collected, st)
	if err != nil {
		endNarrow(err)
		if interrupted(err) {
			return runs, coverageResult{}, err
		}
		s.unavailable(err.Error())
		return runs, coverageResult{}, nil
	}
	endNarrow(nil)
	return covered, result, nil
}

// narrowToTests turns the per-test profiles into the runs to execute.
//
// It splits the test binaries into two kinds. A binary all of whose tests pass
// on their own is *clean*: its mutants are narrowed to the tests that reach
// them, and each such set is checked with a control — the same tests, no
// mutant — before it is trusted, so that a set which passes test by test but
// fails together does not turn into a false kill. A binary any of whose tests
// fails on its own is *dirty*: its tests cannot be isolated soundly, so its
// mutants run against the whole binary, exactly as the package-level pass runs
// them. That is the safe reading, and it is the difference between narrowing a
// run and misreporting one.
func (s *session) narrowToTests(
	ctx context.Context,
	opts execute.Options,
	scratch string,
	modulePath string,
	bins []execute.TestBinary,
	runs []execute.MutantRun,
	collected []execute.TestCoverageData,
	st *state,
) ([]execute.MutantRun, coverageResult, error) {
	index := binaryIndex(bins)

	// A binary is dirty if any of its tests did not pass alone. Those tests
	// are named in one warning and left out of the narrowing.
	dirty := make(map[string]bool)
	profiled := make(map[string]bool)
	var orderDependent []string
	for _, data := range collected {
		profiled[data.ImportPath] = true
		if !data.Passed {
			dirty[data.ImportPath] = true
			orderDependent = append(orderDependent, data.ImportPath+" "+data.Name)
		}
	}
	// A binary whose whole is profiled rather than narrowed to its tests: one
	// with a test that fails alone, and one with no runnable tests at all,
	// which CollectTestCoverage returns nothing for. The second contributes no
	// coverage either way, but profiling its whole keeps the test-narrowed
	// profile set the same as the package-level pass's rather than missing a
	// binary the mapping was entitled to see.
	whole := make(map[string]bool)
	for _, bin := range bins {
		if dirty[bin.ImportPath] || !profiled[bin.ImportPath] {
			whole[bin.ImportPath] = true
		}
	}
	if len(orderDependent) > 0 {
		slices.Sort(orderDependent)
		s.warnTests(coverage.CodeOrderDependentTests, trace.NoteOrderDependentTests,
			"tests fail when run on their own and were left out of test-level narrowing; "+
				"the mutants they reach were measured against the whole binary instead", orderDependent)
	}

	profileDir := filepath.Join(scratch, profileDirName)
	if err := s.makeProfileDir(profileDir); err != nil {
		return nil, coverageResult{}, err
	}

	// The tests of clean binaries, each profiled on its own.
	testProfiles := make(map[coverage.TestKey]coverage.Profile)
	for i, data := range collected {
		if !data.Passed || whole[data.ImportPath] {
			continue
		}
		path := filepath.Join(profileDir, "t"+strconv.Itoa(i)+".txt")
		profile, err := s.renderProfile(ctx, opts, scratch, data.Dir, data.ImportPath+" "+data.Name, path)
		if err != nil {
			return nil, coverageResult{}, err
		}
		testProfiles[coverage.TestKey{ImportPath: data.ImportPath, Name: data.Name}] = profile
	}

	// The whole-binary profile of each dirty binary, so a mutant reached only
	// by an order-dependent test is still executed rather than reported
	// uncovered.
	binProfiles, err := s.wholeBinaryProfiles(ctx, opts, scratch, profileDir, bins, whole)
	if err != nil {
		return nil, coverageResult{}, err
	}

	decided := coverageMutants(runs, st)
	testMapped := coverage.MapTests(coverage.TestOptions{
		ModulePath: modulePath,
		Mutants:    decided,
		Profiles:   testProfiles,
	})
	var binMapped coverage.Result
	if len(binProfiles) > 0 {
		binMapped = coverage.Map(coverage.Options{
			ModulePath: modulePath,
			Mutants:    decided,
			Profiles:   binProfiles,
		})
	}
	if testMapped.Matched == 0 && binMapped.Matched == 0 {
		return nil, coverageResult{}, &coverage.Error{
			Code: coverage.CodeUnavailable,
			Message: "the coverage profiles name no file inside " + modulePath +
				", so every mutant would be reported as uncovered",
		}
	}

	// One plan per mutant, and a control for every distinct set of tests a plan
	// narrows to, built before any mutant runs so a set that fails together is
	// known before its verdicts are.
	plans := narrowingPlans(runs, testMapped, binMapped)
	verifier := newSetVerifier()
	for _, run := range runs {
		if p := plans[run.ID]; len(p.tests) > 0 {
			verifier.want(p.tests, run)
		}
	}
	reliable := verifier.run(ctx, opts, bins)
	if len(reliable.unreliable) > 0 {
		s.warnTests(coverage.CodeUnreliableTestSet, trace.NoteUnreliableTestSet,
			"these tests fail together without a mutant, so a failure under one could not be read as the mutant's; "+
				"the mutants they narrowed were measured against the whole binary instead", reliable.unreliable)
	}

	covered, result := s.decideRuns(index, runs, decided, plans, reliable, len(bins), len(testMapped.Tests), st)
	return covered, result, nil
}

// A mutantPlan is where the two mappings placed one mutant: the tests of clean
// binaries that reach it, and the dirty binaries that reach it and run whole.
type mutantPlan struct {
	tests    map[string][]string
	binaries []string
}

// narrowingPlans places every mutant the mappings were asked about.
func narrowingPlans(runs []execute.MutantRun, testMapped coverage.TestResult, binMapped coverage.Result) map[string]mutantPlan {
	plans := make(map[string]mutantPlan, len(runs))
	for _, run := range runs {
		var p mutantPlan
		for _, key := range testMapped.CoveringOf(run.ID) {
			if p.tests == nil {
				p.tests = make(map[string][]string)
			}
			p.tests[key.ImportPath] = append(p.tests[key.ImportPath], key.Name)
		}
		p.binaries = append(p.binaries, binMapped.CoveringOf(run.ID)...)
		plans[run.ID] = p
	}
	return plans
}

// decideRuns turns the plans and the control verdicts into the runs to execute.
//
// It is pure and split out for that reason: everything above it profiles, maps
// and checks against a toolchain, and everything in here is the rule that
// decides, for one mutant, which tests it runs against — narrowed when the set
// is reliable, widened to the whole covering binaries when it is not, and filed
// as an uncovered survivor when nothing reaches it.
func (s *session) decideRuns(
	index map[string]int,
	runs []execute.MutantRun,
	decided []coverage.Mutant,
	plans map[string]mutantPlan,
	reliable setVerdicts,
	binaryCount, testCount int,
	st *state,
) ([]execute.MutantRun, coverageResult) {
	asked := make(map[string]bool, len(decided))
	placed := make(map[string]coverage.Mutant, len(decided))
	for _, m := range decided {
		asked[m.ID] = true
		placed[m.ID] = m
	}

	result := coverageResult{
		mode:          CoverageTest,
		binaries:      binaryCount,
		tests:         testCount,
		covering:      make(map[string][]string, len(decided)),
		coveringTests: make(map[string][]report.TestRef, len(decided)),
	}
	covered := make([]execute.MutantRun, 0, len(runs))
	uncovered := make([]string, 0)
	widened := 0
	for _, run := range runs {
		p := plans[run.ID]
		// A set the control could not trust is dropped, and the mutant runs
		// against the whole binaries that reach it instead — the same reading
		// the package-level pass gives.
		usable := p.tests
		if len(p.tests) > 0 && !reliable.ok(p.tests) {
			usable = nil
			widened++
		}
		// The binaries are the same whether the mutant is narrowed or widened:
		// widening drops the test selection, not the binaries the tests
		// belonged to. Only Tests tells the two apart.
		binaries := coveringBinaries(p.tests, p.binaries)
		if asked[run.ID] {
			s.recordTestCoverage(placed[run.ID], binaries, usable)
		}
		switch {
		case !asked[run.ID]:
		case len(binaries) == 0:
			uncovered = append(uncovered, run.ID)
			continue
		default:
			run.Binaries = indicesOf(binaries, index)
			run.Tests = usable
			result.covering[run.ID] = binaries
			result.coveringTests[run.ID] = testRefsOf(usable)
		}
		covered = append(covered, run)
	}
	result.widened = widened

	s.emit(CoverageMapped{
		Binaries:  result.binaries,
		Tests:     result.tests,
		Covered:   len(covered),
		Uncovered: len(uncovered),
		Widened:   widened,
	})
	for _, id := range uncovered {
		s.recordUncovered(id, st)
	}
	return covered, result
}

// coveringBinaries is the set of import paths a mutant is measured against: the
// binaries the narrowed tests belong to, plus the dirty binaries run whole.
func coveringBinaries(tests map[string][]string, dirty []string) []string {
	seen := make(map[string]bool, len(tests)+len(dirty))
	var paths []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for importPath := range tests {
		add(importPath)
	}
	for _, importPath := range dirty {
		add(importPath)
	}
	slices.Sort(paths)
	return paths
}

// recordTestCoverage writes one coverage-map event for a mutant the mapping was
// asked about, naming the binaries it is measured against and the tests it was
// narrowed to — the decision as it stands after any widening, not the mapping
// before it.
func (s *session) recordTestCoverage(m coverage.Mutant, binaries []string, tests map[string][]string) {
	s.trace.Coverage(trace.CoverageRecord{
		MutantID:      m.ID,
		Path:          m.Path,
		StartLine:     m.StartLine,
		EndLine:       m.EndLine,
		Covering:      binaries,
		CoveringTests: testLabels(tests),
		Uncovered:     len(binaries) == 0,
	})
}

// makeProfileDir creates the directory the rendered profiles are written into.
func (s *session) makeProfileDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &Error{
			Code:    CodeScratchDir,
			Message: "the directory for the rendered coverage profiles could not be created",
			Err:     err,
		}
	}
	return nil
}

// testLabels flattens a selection into sorted `<import path> <name>` labels, as
// a recording carries them.
func testLabels(tests map[string][]string) []string {
	if len(tests) == 0 {
		return nil
	}
	var labels []string
	for importPath, names := range tests {
		for _, name := range names {
			labels = append(labels, importPath+" "+name)
		}
	}
	slices.Sort(labels)
	return labels
}

// wholeBinaryProfiles renders the whole-binary profile of every binary that is
// not narrowed to its tests: one with an order-dependent test, so that a mutant
// reached only by that test is executed rather than reported uncovered, and one
// with no runnable tests, so the profile set matches the package-level pass's.
func (s *session) wholeBinaryProfiles(
	ctx context.Context,
	opts execute.Options,
	scratch string,
	profileDir string,
	bins []execute.TestBinary,
	whole map[string]bool,
) (map[string]coverage.Profile, error) {
	if len(whole) == 0 {
		return nil, nil
	}
	var wholeBins []execute.TestBinary
	for _, bin := range bins {
		if whole[bin.ImportPath] {
			wholeBins = append(wholeBins, bin)
		}
	}
	collected, err := execute.CollectCoverage(ctx, opts, wholeBins,
		filepath.Join(scratch, coverageDirName, "whole"))
	if err != nil {
		return nil, err
	}
	profiles := make(map[string]coverage.Profile, len(collected))
	for i, data := range collected {
		path := filepath.Join(profileDir, "b"+strconv.Itoa(i)+".txt")
		profile, err := s.renderProfile(ctx, opts, scratch, data.Dir, data.ImportPath, path)
		if err != nil {
			return nil, err
		}
		profiles[data.ImportPath] = profile
	}
	return profiles, nil
}

// warnTests publishes one warning naming a list of tests, and records the same
// list as a note, so a recording carries what the console said.
func (s *session) warnTests(code coverage.Code, note, message string, tests []string) {
	s.warnDetail(string(code), message+": "+strings.Join(tests, ", "), strings.Join(tests, "\n"))
	s.trace.Note(note, "", strings.Join(tests, "\n"))
}
