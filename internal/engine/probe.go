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

func (s *session) probePhase(
	ctx context.Context,
	opts probeOptions,
	runs []execute.MutantRun,
	st *state,
	temps *temporaries,
) ([]execute.MutantRun, ProbeFacts, error) {
	if len(runs) == 0 || len(opts.bins) == 0 {
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
	passes, err := s.probePasses(ctx, probeOpts, opts, probeBins, temps.probe)
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

type probeOptions struct {
	root       string
	catalog    *mutation.Catalog
	hints      instrument.Hints
	modulePath string
	toolchain  gocmd.Toolchain
	env        []string
	jobs       int
	scratch    string
	exec       execute.Options
	bins       []execute.TestBinary
}

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
		Modules:      []validate.Module{{Dir: ".", Path: opts.modulePath}},
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
	probeOpts.Trees = nil
	probeOpts.Restores = nil
	bins, err := execute.BuildTestBinaries(ctx, probeOpts)
	if err != nil {
		return execute.Options{}, nil, err
	}
	return probeOpts, bins, nil
}

func (s *session) probePasses(
	ctx context.Context, probeOpts execute.Options, opts probeOptions, bins []execute.TestBinary,
	tree *snapshot.Snapshot,
) ([]probe.Pass, error) {
	passes := make([]probe.Pass, 0, len(bins))
	matched := binaryIndex(opts.bins)
	for i, bin := range bins {
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
		if err := s.restoreProbeTree(tree); err != nil {
			return nil, err
		}
	}
	return passes, nil
}

func (s *session) restoreProbeTree(tree *snapshot.Snapshot) error {
	if tree == nil {
		return nil
	}
	drifted, err := tree.Restore()
	if err != nil {
		return err
	}
	if len(drifted) > 0 {
		s.trace.Note(trace.NoteProbeTreeRestored, "",
			countNoun(len(drifted), "file")+" put back after a probe pass")
	}
	return nil
}

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

func (s *session) probeUnavailable(why string) {
	s.warnCode(string(probe.CodeUnavailable), why+
		"; the run is measuring every mutant against every covering binary, as a run without probing does")
}

func probeCodeOf(err error) probe.Code {
	var coded *probe.Error
	if !errors.As(err, &coded) {
		return ""
	}
	return coded.Code
}

func probingEnabled(cfg *config.Config) bool {
	return cfg.Test.Probing == config.ProbingOn
}
