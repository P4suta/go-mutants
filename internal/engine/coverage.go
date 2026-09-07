// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// The scratch subdirectories the coverage pass writes into.
//
// Both sit under the run's scratch directory, which is a sibling of the
// snapshot rather than a child of it, and that placement is the whole of the
// interaction between coverage and the drift gate. [snapshot.Snapshot.Redigest]
// walks the manifest — the files that were copied — so nothing written outside
// the snapshot root can appear as drift, and the gate's allowlist therefore
// needs no coverage entry at all. See the argument in the package
// documentation.
const (
	// coverageDirName holds one subdirectory per test binary, each containing
	// the covmeta and covcounters files that binary emitted.
	coverageDirName = "coverage"
	// profileDirName holds the textfmt rendering of each of those, one file per
	// binary, named by the same index.
	profileDirName = "profiles"
)

// coverPkgSuffix is appended to the module path to make the `-coverpkg`
// pattern.
//
// The whole module rather than the package under test: a test binary's profile
// has to be able to say that package A's lines were reached by package B's
// tests, which is the only interesting thing coverage-guided selection has to
// decide. `-coverpkg=./...` would mean the same set and is deliberately not
// used, because it is resolved against a working directory and the module path
// is not.
const coverPkgSuffix = "/..."

// A coverageResult is what the coverage phase decided about a run.
//
// The zero value is a run with coverage off, which is the honest default: every
// path that fails, warns, or is never taken leaves it alone and the run measures
// every mutant against every binary.
type coverageResult struct {
	// mode is what the report and the summary say.
	mode CoverageMode
	// binaries is how many test binaries were profiled.
	binaries int
	// tests is how many tests were profiled on their own, in [CoverageTest].
	tests int
	// widened is how many covered mutants a [CoverageTest] run measured
	// against a whole binary after all; see [CoverageMapped.Widened].
	widened int
	// covering maps a mutant id onto the sorted import paths of the binaries
	// that reach it. Empty in [CoverageOff].
	covering map[string][]string
	// coveringTests maps a mutant id onto the sorted tests that reach it, in
	// [CoverageTest]; empty in every other mode.
	coveringTests map[string][]report.TestRef
	// coverageFallback is the whole failure that made the run give coverage up:
	// the coded message and the compiler's own diagnostics under it, as
	// [session.buildTestBinaries] kept them. It is empty on every run that
	// never fell back, which is nearly all of them.
	//
	// The console has already been told in one line. This is the copy for
	// somebody asking why, and it is deliberately not the warning: a run that
	// is about to succeed does not print a compiler blob at the user.
	coverageFallback string
}

// Mode resolves the zero value to [CoverageOff], so that a run which never
// reached the coverage phase still names its mode rather than reporting the
// empty string.
func (c coverageResult) Mode() CoverageMode {
	if c.mode == "" {
		return CoverageOff
	}
	return c.mode
}

// Whether a run may narrow itself by coverage at all is decided one step
// earlier, by [testScope], and it is not a configuration switch: the mapping is
// either sound or it is not. It is sound exactly when go-mutants recognised the
// test command, because a recognised command is one whose binaries go-mutants
// compiled itself and can name — so "these lines were reached by this binary"
// attributes to something the execution phase can act on. An unrecognised
// command is an opaque program: `./scripts/test.sh`, `gotestsum`, a wrapper that
// runs one package or twenty, possibly not `go test` at all. A wrong attribution
// there would not cost time, it would cost a kill.
//
// A scoped command narrows what the mapping is *over* and changes nothing about
// what it means. The binaries are the ones the user's own command runs, so a
// mutant no scoped binary reaches is an uncovered survivor — which is the honest
// reading of a test scope that leaves the line out, and the same answer the run
// would reach by executing every scoped binary against it and watching them all
// pass.

// coveragePhase profiles the test binaries and narrows the run to what each
// mutant needs.
//
// It returns the runs to execute — the covered mutants, each carrying the
// binaries that reach it — and records the uncovered ones in st as survivors
// that were never executed. On any path where coverage is unavailable it
// returns the runs untouched and a [coverageResult] in [CoverageOff], having
// published a warning saying which path that was.
//
// The only error it returns is an interruption. Everything else is a warning,
// because coverage-guided selection is an optimisation and a run that cannot
// have it still reaches exactly the same verdicts, slower. See
// [coverage.CodeUnavailable].
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
	// Nothing to narrow. Skipping is not merely tidy: the pass costs a full run
	// of every test binary, and paying that to decide the fate of no mutants is
	// the one case where the optimisation is pure loss.
	//
	// That is the *only* case it is skipped in, and `--mutant` deserves a word
	// because it is the one where the arithmetic is genuinely arguable: a
	// single-mutant run pays one suite run per binary to decide the fate of one
	// mutant, which is between break-even and much worse when that mutant is
	// killed by the first binary tried. It is kept anyway, because the question
	// `--mutant` is asked in order to answer is "why did this one survive", and
	// "no test reaches this line" is the best answer there is — and the only way
	// to reach it is to look.
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

	// Named for what the stage does rather than for the event it produces: the
	// `coverage-map` events are one per mutant and are what the stage spends its
	// time making, but the stage itself is the narrowing — the decision about
	// which mutants this run will not execute.
	endNarrow := s.stage("coverage-narrow", countNoun(len(runs), "mutant"))
	decided := coverageMutants(runs, st)
	mapped := coverage.Map(coverage.Options{
		ModulePath: modulePath,
		Mutants:    decided,
		Profiles:   profiles,
	})
	// A profile set that named no file inside the module is not a workspace
	// with no coverage, it is a mapping that failed to line up — the module
	// path and the names the toolchain wrote do not agree — and believing it
	// would report every mutant as an uncovered survivor. Failing open here is
	// the difference between a slow run and a fiction.
	if mapped.Matched == 0 {
		unmapped := &coverage.Error{
			Code: coverage.CodeUnavailable,
			Message: "the coverage profiles name no file inside " + modulePath +
				", so every mutant would be reported as uncovered",
		}
		// The stage says it failed even though the run goes on: what the phase
		// was there to establish was not established, and a stage that reported
		// success would leave the recording claiming a mapping happened.
		endNarrow(unmapped)
		s.unavailable(unmapped.Message)
		return runs, coverageResult{}, nil
	}

	covered, result := s.narrow(mapped, decided, bins, runs, st)
	endNarrow(nil)
	return covered, result, nil
}

// narrow turns the mapping into the runs to execute, filing everything it
// leaves out as an uncovered survivor.
//
// It is split from [session.coveragePhase] so that the decision can be tested
// without a toolchain: everything above it is process work, and everything in
// here is the rule that decides which mutants a run spends its time on.
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
	// Which mutants the mapping was actually asked about. A mutant
	// [coverageMutants] left out — one the display index has no coordinates for,
	// which internal/engine documents as impossible — has no answer here, and an
	// absent answer must not be read as "nothing covers it": that would turn the
	// impossible case into a mutant silently never executed. It keeps its nil
	// binary list instead, which is every binary, and the run is exactly as slow
	// for that one mutant as a run with coverage off.
	asked := make(map[string]bool, len(decided))
	for _, m := range decided {
		asked[m.ID] = true
	}

	// Where the mapping placed each mutant, so that the account of the decision
	// can state the block it was placed in rather than only the verdict.
	placed := make(map[string]coverage.Mutant, len(decided))
	for _, m := range decided {
		placed[m.ID] = m
	}

	index := binaryIndex(bins)
	covered := make([]execute.MutantRun, 0, len(runs))
	uncovered := make([]string, 0)
	for _, run := range runs {
		covering := mapped.CoveringOf(run.ID)
		// One event per mutant the mapping was asked about, covered or not. It
		// is the whole evidence for the mutants a coverage-guided run does not
		// execute, and a decision recorded only for the ones that survived it
		// would be an account a reader could not check. A mutant the mapping was
		// never asked about has no placement to record and is left out rather
		// than recorded with an empty one.
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

	// The partition is announced before any of it is settled, and the two loops
	// are separate for that reason alone. [CoverageMapped] is the line that says
	// how much of the run is about to not happen, and a reader who saw the first
	// skipped mutant scroll past before the summary of the skipping would be
	// reading the run backwards.
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

// profile runs each test binary once with coverage collection on and renders
// what it collected as a textfmt document this process can read.
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

	profileDir := filepath.Join(scratch, profileDirName)
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, &Error{
			Code:    CodeScratchDir,
			Message: "the directory for the rendered coverage profiles could not be created",
			Err:     err,
		}
	}

	profiles := make(map[string]coverage.Profile, len(collected))
	for i, data := range collected {
		path := filepath.Join(profileDir, strconv.Itoa(i)+".txt")
		profile, err := s.renderProfile(ctx, opts, scratch, data.Dir, data.ImportPath, path)
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

// renderProfile turns one coverage directory into a textfmt document at path
// and reads it back. subject is what the rendering is about — an import path,
// or an import path and a test — and is what the recording and every message
// name it by.
func (s *session) renderProfile(
	ctx context.Context,
	opts execute.Options,
	scratch string,
	dir string,
	subject string,
	path string,
) (coverage.Profile, error) {
	spec := opts.Toolchain.Command("tool", "covdata", "textfmt", "-i="+dir, "-o="+path)
	spec.Dir = opts.SnapshotRoot
	spec.Env = childEnv(scratch)
	spec.Timeout = BaselineCap
	spec.Trace = s.trace
	spec.Kind = trace.ExecKindCovdataTextfmt
	spec.Subject = subject

	if err := check(ctx, spec, runner.Run(ctx, spec), CodeCoverageRender,
		"`go tool covdata textfmt` over the profile of "+subject+" failed"); err != nil {
		return coverage.Profile{}, err
	}
	// The rendered profile is a file the run wrote and the only readable
	// form the coverage decision was made from, so a `--keep-temp` run has
	// something to point somebody at.
	s.trace.Artifact(trace.ArtifactCoverageProfile, path)

	file, err := os.Open(path)
	if err != nil {
		return coverage.Profile{}, &Error{
			Code:    CodeCoverageRender,
			Message: "the rendered coverage profile for " + subject + " could not be read",
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
			Message: "the rendered coverage profile for " + subject + " could not be closed",
			Err:     closeErr,
		}
	}
	return profile, nil
}

// usable refuses a profile set that parsed but says nothing.
//
// Every document read, and not one block between them. That is not a workspace
// nothing covers — a workspace with no statements at all has no mutants either,
// and this phase is only reached when there are some — so it is coverage
// collection having silently produced nothing, which is exactly the failure
// mode that would otherwise report every mutant as an uncovered survivor and a
// perfect suite as untested.
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

// coverageMutants reduces the scheduled runs to what the mapping needs.
//
// A mutant whose coordinates the display index does not have is deliberately
// left out, which gives it no covering binary and would make it an uncovered
// survivor — so [coveragePhase] never sees one: it is filtered here and the
// caller keeps it, unnarrowed, by the same rule the whole phase fails open
// under. internal/engine documents a catalogued mutant with no candidate behind
// it as impossible; this is what impossible costs if it ever happens.
func coverageMutants(runs []execute.MutantRun, st *state) []coverage.Mutant {
	mutants := make([]coverage.Mutant, 0, len(runs))
	for _, run := range runs {
		shown := st.display[run.ID]
		if shown.Line < 1 || shown.Path == "" {
			continue
		}
		mutants = append(mutants, coverage.Mutant{
			ID:        run.ID,
			Path:      shown.Path,
			StartLine: shown.Line,
			EndLine:   coverage.EndLine(shown.Line, shown.Original),
		})
	}
	return mutants
}

// recordUncovered files one mutant that no test binary reaches.
//
// It is a survivor, and the outcome is not a convention: none of the binaries
// the run measures with reaches the line, so none of them could have caught the
// edit, and the score is entitled to count it against the suite exactly as it
// counts a survivor that was executed. Under a scoped `test.command` those
// binaries are the ones the user's own command runs rather than every binary in
// the module, which narrows what the claim is over and not what it means. What
// `uncovered` adds is the reason, which is the more actionable half — "write a
// test for this line" rather than "sharpen the test you have".
//
// The event is published even though nothing ran, so that a renderer's counts
// and the report's agree; see [MutantFinished] for the pairing contract that
// costs.
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

// unavailable publishes the fail-open warning: what went wrong, and what the run
// is doing about it.
//
// The second half is not padding. A warning that said only "coverage failed"
// leaves a reader wondering whether the results can be trusted, and the answer
// is that they can — the run is about to do strictly more work than it would
// have, and every verdict it reaches is one it would have reached anyway.
func (s *session) unavailable(why string) {
	s.unavailableInFull(why, why)
}

// unavailableInFull is [session.unavailable] for the one caller with more to
// say than fits on a console line.
//
// The two texts are deliberately different lengths. `why` is folded onto one
// line, because the run is about to carry on and succeed and a console during a
// successful run does not print a compiler blob at the user; `whole` is
// everything there is, for the reader who is asking why. Every path that gives
// coverage up goes through here, so `note{coverage-unavailable}` means one thing
// whichever of them it was.
//
// `whole` goes onto the warning as well as into the recording, so that `run -v`
// can print it under the warning it belongs to without reading the stream. It is
// carried only when it says more than the message already does: when the whole
// reason is the line the message was built from, a detail would be the same
// sentence printed twice.
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

// binaryIndex maps each test binary's import path onto its position, which is
// the form [execute.MutantRun.Binaries] takes.
func binaryIndex(bins []execute.TestBinary) map[string]int {
	index := make(map[string]int, len(bins))
	for i, bin := range bins {
		index[bin.ImportPath] = i
	}
	return index
}

// indicesOf turns covering import paths into binary positions, keeping the
// sorted order the mapping produced.
//
// A path with no position is skipped rather than reported, and it cannot
// happen: the mapping's binaries are the keys of a map this package built from
// the same slice. The guard is here because the alternative to skipping is an
// index that names a binary the executor does not have, which
// [execute.MutantRun.Binaries] refuses — turning an impossible condition into a
// failed mutant rather than an unnarrowed one.
func indicesOf(covering []string, index map[string]int) []int {
	indices := make([]int, 0, len(covering))
	for _, importPath := range covering {
		if i, ok := index[importPath]; ok {
			indices = append(indices, i)
		}
	}
	if len(indices) == 0 {
		// Documented as impossible above. Nil is "every binary", which is the
		// safe reading of "this run no longer knows which ones".
		return nil
	}
	return indices
}

// reportCoverageMode maps this package's spelling onto the document's, as
// [reportTimeoutSource] does for the other enum the two share.
func reportCoverageMode(mode CoverageMode) report.CoverageMode {
	switch mode {
	case CoveragePackage:
		return report.CoveragePackage
	case CoverageTest:
		return report.CoverageTest
	default:
		return report.CoverageOff
	}
}
