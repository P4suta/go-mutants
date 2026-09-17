// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/trace"
)

const (
	coverageDirName = "coverage"
	profileDirName  = "profiles"
)

const coverPkgSuffix = "/..."

type coverageResult struct {
	mode             CoverageMode
	binaries         int
	tests            int
	widened          int
	covering         map[string][]string
	coveringTests    map[string][]report.TestRef
	coverageFallback string
}

func (c coverageResult) Mode() CoverageMode {
	if c.mode == "" {
		return CoverageOff
	}
	return c.mode
}

func (s *session) coveragePhase(
	ctx context.Context,
	opts execute.Options,
	scratch string,
	modulePath string,
	bins []execute.TestBinary,
	runs []execute.MutantRun,
	st *state,
	narrowing config.Narrowing,
) ([]execute.MutantRun, coverageResult, error) {
	if len(runs) == 0 || len(bins) == 0 {
		return runs, coverageResult{}, nil
	}
	if narrowing != config.NarrowingPackage {
		return s.testCoveragePhase(ctx, opts, scratch, modulePath, bins, runs, st)
	}

	endProfile := s.stage("coverage-profile", countNoun(len(bins), "binary"))
	profiles, err := s.profile(ctx, opts, scratch, bins)
	endProfile(err)
	if err != nil {
		if interrupted(err) {
			return runs, coverageResult{}, err
		}
		s.unavailable(err.Error())
		return runs, coverageResult{}, nil
	}

	endNarrow := s.stage("coverage-narrow", countNoun(len(runs), "mutant"))
	decided := coverageMutants(runs, st)
	mapped := coverage.Map(coverage.Options{
		ModulePath: modulePath,
		Mutants:    decided,
		Profiles:   profiles,
	})
	if mapped.Matched == 0 {
		unmapped := &coverage.Error{
			Code: coverage.CodeUnavailable,
			Message: "the coverage profiles name no file inside " + modulePath +
				", so every mutant would be reported as uncovered",
		}
		endNarrow(unmapped)
		s.unavailable(unmapped.Message)
		return runs, coverageResult{}, nil
	}

	covered, result := s.narrow(mapped, decided, bins, runs, st)
	endNarrow(nil)
	return covered, result, nil
}

func (s *session) narrow(
	mapped coverage.Result,
	decided []coverage.Mutant,
	bins []execute.TestBinary,
	runs []execute.MutantRun,
	st *state,
) ([]execute.MutantRun, coverageResult) {
	result := coverageResult{
		mode:     CoveragePackage,
		binaries: len(mapped.Binaries),
		covering: mapped.Covering,
	}
	asked := make(map[string]bool, len(decided))
	for _, m := range decided {
		asked[m.ID] = true
	}

	placed := make(map[string]coverage.Mutant, len(decided))
	for _, m := range decided {
		placed[m.ID] = m
	}

	index := binaryIndex(bins)
	covered := make([]execute.MutantRun, 0, len(runs))
	uncovered := make([]string, 0)
	for _, run := range runs {
		covering := mapped.CoveringOf(run.ID)
		if asked[run.ID] {
			where := placed[run.ID]
			s.trace.Coverage(trace.CoverageRecord{
				MutantID:  run.ID,
				Path:      where.Path,
				StartLine: where.StartLine,
				EndLine:   where.EndLine,
				Covering:  covering,
				Uncovered: len(covering) == 0,
			})
		}
		switch {
		case !asked[run.ID]:
		case len(covering) == 0:
			uncovered = append(uncovered, run.ID)
			continue
		default:
			run.Binaries = indicesOf(covering, index)
		}
		covered = append(covered, run)
	}

	s.emit(CoverageMapped{
		Binaries:  result.binaries,
		Covered:   len(covered),
		Uncovered: len(uncovered),
	})
	for _, id := range uncovered {
		s.recordUncovered(id, st)
	}
	return covered, result
}

func (s *session) profile(
	ctx context.Context,
	opts execute.Options,
	scratch string,
	bins []execute.TestBinary,
) (map[string]coverage.Profile, error) {
	collected, err := execute.CollectCoverage(ctx, opts, bins, filepath.Join(scratch, coverageDirName))
	if err != nil {
		return nil, err
	}

	profiles := make(map[string]coverage.Profile, len(collected))
	for _, data := range collected {
		profile, err := s.readProfile(data.Path, data.ImportPath)
		if err != nil {
			return nil, err
		}
		profiles[data.ImportPath] = profile
	}
	if err := usable(profiles); err != nil {
		return nil, err
	}
	return profiles, nil
}

func (s *session) readProfile(path, subject string) (coverage.Profile, error) {
	s.trace.Artifact(trace.ArtifactCoverageProfile, path)

	file, err := os.Open(path)
	if err != nil {
		return coverage.Profile{}, &Error{
			Code:    CodeCoverageRender,
			Message: "the coverage profile for " + subject + " could not be read",
			Err:     err,
		}
	}
	profile, parseErr := coverage.ParseTextfmt(file)
	closeErr := file.Close()
	if parseErr != nil {
		return coverage.Profile{}, parseErr
	}
	if closeErr != nil {
		return coverage.Profile{}, &Error{
			Code:    CodeCoverageRender,
			Message: "the coverage profile for " + subject + " could not be closed",
			Err:     closeErr,
		}
	}
	return profile, nil
}

func usable(profiles map[string]coverage.Profile) error {
	if len(profiles) == 0 {
		return &coverage.Error{
			Code:    coverage.CodeUnavailable,
			Message: "no test binary produced a coverage profile",
		}
	}
	for _, profile := range profiles {
		if len(profile.Blocks) > 0 {
			return nil
		}
	}
	return &coverage.Error{
		Code:    coverage.CodeUnavailable,
		Message: "every coverage profile is empty",
	}
}

func coverageMutants(runs []execute.MutantRun, st *state) []coverage.Mutant {
	mutants := make([]coverage.Mutant, 0, len(runs))
	for _, run := range runs {
		shown := st.display[run.ID]
		if shown.Line < 1 || shown.Path == "" {
			continue
		}
		mutants = append(mutants, coverage.Mutant{
			ID:         run.ID,
			Path:       shown.Path,
			ModulePath: st.moduleOf(run.ID),
			StartLine:  shown.Line,
			EndLine:    coverage.EndLine(shown.Line, shown.Original),
		})
	}
	return mutants
}

func (s *session) recordUncovered(id string, st *state) {
	st.results[id] = report.MutantResult{
		ID:        id,
		Outcome:   mutation.OutcomeSurvived,
		Uncovered: true,
	}
	shown := st.display[id]
	shown.Outcome = mutation.OutcomeSurvived
	shown.Duration = 0
	shown.Uncovered = true
	s.emit(MutantFinished{Result: shown.clone()})
}

func (s *session) unavailable(why string) {
	s.unavailableInFull(why, why)
}

func (s *session) unavailableInFull(why, whole string) {
	detail := whole
	if whole == firstLine(why) {
		detail = ""
	}
	s.warnDetail(string(coverage.CodeUnavailable),
		"coverage-guided selection is off because "+strings.TrimSuffix(firstLine(why), ".")+
			"; every mutant will be measured against every test binary, which is slower and never wrong",
		detail)
	s.trace.Note(trace.NoteCoverageUnavailable, "", whole)
}

func binaryIndex(bins []execute.TestBinary) map[string]int {
	index := make(map[string]int, len(bins))
	for i, bin := range bins {
		index[bin.ImportPath] = i
	}
	return index
}

func indicesOf(covering []string, index map[string]int) []int {
	indices := make([]int, 0, len(covering))
	for _, importPath := range covering {
		if i, ok := index[importPath]; ok {
			indices = append(indices, i)
		}
	}
	if len(indices) == 0 {
		return nil
	}
	return indices
}

func reportCoverageMode(mode CoverageMode) report.CoverageMode {
	switch mode {
	case CoveragePackage:
		return report.CoveragePackage
	case CoverageTest:
		return report.CoverageTest
	case CoverageOff:
	}
	return report.CoverageOff
}
