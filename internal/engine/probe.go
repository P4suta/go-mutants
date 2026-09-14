// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/probe"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/validate"
	"github.com/P4suta/go-mutants/trace"
)

// The probe phase: proving, before anything is activated, which executions this
// run does not have to make.
//
// # Where it sits, and why exactly there
//
// After coverage and before the cache. Both halves of that are forced.
//
// After coverage, because the two narrowings compose in one direction only: the
// rule needs to know which binaries cover a mutant, and a mutant nothing covers
// is settled by coverage first — a probe asked about one would find the empty
// intersection, which is vacuously "no binary named it" and would report a
// survivor nothing ever looked at.
//
// Before the cache, because a mutant this phase settles is a mutant this run did
// not execute, and internal/cache's correctness argument requires every such
// mutant to be settled before the cache is asked about it. The same sentence
// justifies coverage's position; this is the second narrowing it covers.
//
// # What it costs, and when it is worth it
//
// A second snapshot of the module, a second instrumentation, a second
// validation, a second build of every test binary, and one suite run per
// binary. That is why it is off by default and why there is no flag for it:
// like `test.narrowing`, it is a decision about how a project is measured
// rather than about one invocation, and the arithmetic depends entirely on the
// suite. A module whose tests are fast and whose mutants are thinly covered
// pays a few suite runs and skips thousands of them; one whose suite is slow
// and whose every test touches everything pays the same and skips nothing.
//
// # Everything here fails open
//
// Not one condition in this file can fail a run. A tree that will not build, a
// pass that fails, a log that cannot be read, a log that names a mutant nobody
// has heard of: each is a warning, and each leaves the run measuring exactly
// what it would have measured without probing at all. That is internal/coverage's
// rule and internal/coverage's argument — an optimisation that can fail a run is
// not an optimisation — and it is what makes this phase safe to leave on.

// probePhase measures the probe tree and settles what it can.
//
// It returns the runs the executor should make, which is the input minus the
// mutants it settled and with the rest narrowed where the evidence allowed. A
// phase that established nothing returns its input unchanged.
//
// The only error it returns is an interruption, which is the one condition that
// must not be swallowed: a Ctrl-C during a probe pass is the user stopping the
// run, and turning it into "probing was unavailable" would carry on measuring
// thousands of mutants after they asked it to stop.
func (s *session) probePhase(
	ctx context.Context,
	opts probeOptions,
	runs []execute.MutantRun,
	st *state,
	temps *temporaries,
) ([]execute.MutantRun, ProbeFacts, error) {
	if len(runs) == 0 || len(opts.bins) == 0 {
		// Not a warning. A run whose every mutant coverage already settled has
		// nothing left for a probe to say, and paying for a tree to say it is
		// the one case where the optimisation is pure loss.
		return runs, ProbeFacts{}, nil
	}

	end := s.stage("probe-tree", countNoun(opts.catalog.Len(), "mutant"))
	probeOpts, probeBins, err := s.probeTree(ctx, opts, temps)
	end(err)
	if err != nil {
		if interrupted(err) {
			return runs, ProbeFacts{}, err
		}
		s.probeUnavailable(err.Error())
		return runs, ProbeFacts{}, nil
	}

	endPass := s.stage("probe", countNoun(len(probeBins), "binary"))
	passes, err := s.probePasses(ctx, probeOpts, opts, probeBins)
	endPass(err)
	if err != nil {
		if interrupted(err) {
			return runs, ProbeFacts{}, err
		}
		s.probeUnavailable(err.Error())
		return runs, ProbeFacts{}, nil
	}

	endSettle := s.stage("probe-narrow", countNoun(len(runs), "mutant"))
	decision, err := probe.Settle(candidatesOf(runs, opts.hints, st), passes)
	endSettle(err)
	if err != nil {
		// Including "nothing to probe", which is not a failure and is not
		// warned about: it is the shape of a run whose mutants were all settled
		// before this phase was reached.
		if probeCodeOf(err) != probe.CodeNothingToProbe {
			s.probeUnavailable(err.Error())
		}
		return runs, ProbeFacts{}, nil
	}
	return s.applyProbe(decision, runs, opts.bins, st, len(probeBins)), ProbeFacts{
		Binaries: len(probeBins),
		Settled:  len(decision.Settled),
		Narrowed: len(decision.Narrowed),
	}, nil
}

// probeOptions is everything the phase needs from the run around it.
//
// It is a struct because the alternative is a twelve-argument function, and
// because every field of it is something the phase reads and none is something
// it changes.
type probeOptions struct {
	// root is the module the run was pointed at, which the probe tree is a
	// second snapshot of. The mutant tree's snapshot is instrumented in place
	// by the time this phase runs, so it cannot be copied.
	root string
	// catalog and hints are the run's own, unchanged: the probe tree measures
	// the same mutants, and a second discovery pass would be a second answer to
	// a question already settled.
	catalog *mutation.Catalog
	hints   instrument.Hints
	// modulePath, toolchain, env and jobs are the build inputs, exactly as the
	// mutant tree's are.
	modulePath string
	toolchain  gocmd.Toolchain
	env        []string
	jobs       int
	// scratch is the run's scratch directory, under which the probe tree's
	// binaries, targets and logs are kept apart from the mutant tree's.
	scratch string
	// exec is the mutant tree's execution options, which the probe pass borrows
	// its budgets and its package scope from: a pass is only evidence about a
	// mutant run if the same tests ran the same way.
	exec execute.Options
	// bins are the mutant tree's test binaries, which the probe tree's are
	// matched to by import path.
	bins []execute.TestBinary
}

// probeTree makes the second snapshot, instruments it as a probe tree, and
// builds its test binaries.
//
// The snapshot is taken from the same root the run's own was, so the two trees
// are copies of the same bytes -- and the run has already proved that root held
// still, because the drift gate ran before this phase.
//
// There is deliberately no verification command on this tree, for the reason
// the library's own probe preparation gives: the mutant tree's verify exists
// because a whole run is scored against it and one broken build would falsify
// every number, while a probe pass reports a failing target as "no facts"
// already.
func (s *session) probeTree(
	ctx context.Context, opts probeOptions, temps *temporaries,
) (execute.Options, []execute.TestBinary, error) {
	snap, err := snapshot.Create(opts.root, snapshot.Options{DestParent: temps.snapshot.Parent()})
	if err != nil {
		return execute.Options{}, nil, err
	}
	temps.probe = snap
	s.trace.Snapshot(trace.SnapshotRecord{
		Kind:   trace.SnapshotKindProbe,
		Source: opts.root,
		Dir:    snap.Root,
		Stable: snap.StableDir,
		Files:  len(snap.Manifest),
	})

	if _, err = validate.Validate(ctx, validate.Options{
		Snap:         snap,
		Catalog:      opts.catalog,
		Hints:        opts.hints,
		ModulePath:   opts.modulePath,
		Toolchain:    opts.toolchain,
		Jobs:         opts.jobs,
		BuildTimeout: BaselineCap,
		Env:          opts.env,
		Mode:         instrument.ModeProbe,
		Trace:        s.trace,
	}); err != nil {
		return execute.Options{}, nil, err
	}

	probeOpts := opts.exec
	probeOpts.SnapshotRoot = snap.Root
	probeOpts.BinDir = filepath.Join(opts.scratch, "probe-bin")
	probeOpts.ScratchDir = filepath.Join(opts.scratch, "probe-targets")
	// A probe tree's binaries are never isolated and never restored: nothing is
	// activated in them, so there is no mutant whose writes could reach the
	// next one, and a pass that wrote into its own package directory is a
	// suite this feature has nothing to say about either way.
	probeOpts.Trees = nil
	probeOpts.Restores = nil
	bins, err := execute.BuildTestBinaries(ctx, probeOpts)
	if err != nil {
		return execute.Options{}, nil, err
	}
	return probeOpts, bins, nil
}

// probePasses runs the probe tree once per test binary and reports what each
// established.
//
// One pass per binary, and never one pass per test. internal/probe's own
// documentation carries the argument in full; the short of it is that a test
// profiled alone is a different execution from the same test inside its suite,
// so a per-test log licenses less than it appears to and costs a second
// soundness argument to buy.
//
// Each pass gets a log of its own, because two passes appending to one file
// cannot be told apart afterwards: each would read the other's indices as its
// own and every mutant either saw would look like a mutant both saw.
func (s *session) probePasses(
	ctx context.Context, probeOpts execute.Options, opts probeOptions, bins []execute.TestBinary,
) ([]probe.Pass, error) {
	passes := make([]probe.Pass, 0, len(bins))
	matched := binaryIndex(opts.bins)
	for i, bin := range bins {
		// The mutant tree's index for this package, because that is the
		// coordinate system a mutant's covering set is written in. The two
		// trees are built from the same packages, so a binary the mutant tree
		// does not have is one whose facts nothing could use.
		target, known := matched[bin.ImportPath]
		if !known {
			continue
		}
		run := execute.ProbeRun{
			Timeout:     opts.exec.Timeout,
			MemoryLimit: opts.exec.MemoryLimit,
			Binaries:    []int{i},
			LogPath:     filepath.Join(opts.scratch, "probe-"+strconv.Itoa(i)+".log"),
			Digest:      opts.catalog.Digest(),
			Mutants:     opts.catalog.Len(),
		}
		attempt := execute.RunProbe(ctx, probeOpts, run, bins)
		s.trace.ProbeExec(execute.ProbePassRecord(run, attempt))
		if attempt.Err != nil {
			if interrupted(attempt.Err) {
				return nil, attempt.Err
			}
			// A pass that could not be made is a binary nothing is known
			// about, which internal/probe reads as "unknown" rather than as
			// "saw nothing". It is recorded as such and the phase carries on:
			// one unusable binary costs the mutants it covers their saving and
			// costs the rest nothing.
			s.probeUnavailable(attempt.Err.Error())
			passes = append(passes, probe.Pass{Binary: target})
			continue
		}
		if attempt.Outcome != execute.ProbeMeasured {
			s.probeUnavailable("the probe pass over " + bin.ImportPath + " ended " + string(attempt.Outcome) +
				", so nothing is known about which mutants it could observe")
			passes = append(passes, probe.Pass{Binary: target})
			continue
		}
		passes = append(passes, probe.Pass{Binary: target, Infected: attempt.Infected})
	}
	return passes, nil
}

// candidatesOf is the run's mutants as internal/probe needs to see them.
//
// Probed is read from the hints rather than re-derived, because only
// internal/instrument knows which probe forms exist: a mutant with none left
// its file untouched in the probe tree, so it is *accepted* by that tree's
// validation exactly as a probed one is, and reading acceptance as evidence
// would settle a mutant nothing could ever have recorded.
func candidatesOf(runs []execute.MutantRun, hints instrument.Hints, st *state) []probe.Candidate {
	candidates := make([]probe.Candidate, 0, len(runs))
	for _, run := range runs {
		m, known := st.catalog.ByID(run.ID)
		if !known {
			continue
		}
		candidates = append(candidates, probe.Candidate{
			ID:       run.ID,
			Index:    m.Index,
			Probed:   hints.Probes(m),
			Binaries: slices.Clone(run.Binaries),
		})
	}
	return candidates
}

// applyProbe turns the decision into the runs the executor will make.
func (s *session) applyProbe(
	decision probe.Decision, runs []execute.MutantRun, bins []execute.TestBinary, st *state, binaries int,
) []execute.MutantRun {
	settled := make(map[string]bool, len(decision.Settled))
	for _, id := range decision.Settled {
		settled[id] = true
	}
	kept := make([]execute.MutantRun, 0, len(runs))
	for _, run := range runs {
		if settled[run.ID] {
			continue
		}
		if narrowed, ok := decision.Narrowed[run.ID]; ok {
			run.Binaries = narrowed
			run.Tests = testsOf(run.Tests, narrowed, bins)
		}
		kept = append(kept, run)
	}

	// The partition is announced before any of it is settled, for
	// [session.narrow]'s reason: a reader who saw the first skipped mutant
	// scroll past before the summary of the skipping would be reading the run
	// backwards.
	s.emit(Probed{
		Binaries:  binaries,
		Settled:   len(decision.Settled),
		Narrowed:  len(decision.Narrowed),
		Remaining: len(kept),
	})
	for _, id := range decision.Settled {
		s.recordUnobserved(id, st)
	}
	return kept
}

// testsOf drops the test selections of binaries a narrowing removed.
//
// internal/execute refuses a selection naming a binary the run does not start,
// and it is right to: such an entry describes a measurement never made. So a
// narrowing that takes a binary away has to take its tests with it, and a
// narrowing that leaves nothing selected leaves the map nil, which is every
// remaining binary run whole.
func testsOf(tests map[string][]string, binaries []int, bins []execute.TestBinary) map[string][]string {
	if len(tests) == 0 {
		return nil
	}
	kept := make(map[string][]string, len(binaries))
	for _, index := range binaries {
		if index < 0 || index >= len(bins) {
			continue
		}
		if selected, narrowed := tests[bins[index].ImportPath]; narrowed {
			kept[bins[index].ImportPath] = selected
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// recordUnobserved files a mutant no covering binary could observe.
//
// It is a survivor and not an uncovered one, and the distinction is the whole
// point of the field: a test binary reaches this mutant's lines and runs them,
// and what the probe established is that running them changes nothing the tests
// look at. "Uncovered" would send a reader to write a test for a line that is
// already tested.
func (s *session) recordUnobserved(id string, st *state) {
	st.results[id] = report.MutantResult{
		ID:         id,
		Outcome:    mutation.OutcomeSurvived,
		Unobserved: true,
	}
	shown := st.display[id]
	shown.Outcome = mutation.OutcomeSurvived
	shown.Duration = 0
	s.emit(MutantFinished{Result: shown.clone()})
}

// probeUnavailable publishes the fail-open warning under this phase's own code.
func (s *session) probeUnavailable(why string) {
	s.warnCode(string(probe.CodeUnavailable), why+
		"; the run is measuring every mutant against every covering binary, as a run without probing does")
}

// probeCodeOf is the code a probe failure carries, or the empty code.
func probeCodeOf(err error) probe.Code {
	var coded *probe.Error
	if !errors.As(err, &coded) {
		return ""
	}
	return coded.Code
}

// probingEnabled reports whether this run was configured to probe.
func probingEnabled(cfg *config.Config) bool {
	return cfg.Test.Probing == config.ProbingOn
}
