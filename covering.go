// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// A TestRef names one test of one test binary: the import path of the package
// the binary was built from, and the top-level test, example or fuzz target
// name as `go test -test.list` prints it.
//
// It is the unit [Session.CoveringTests] reports a mutant's coverage in, and
// the unit a caller narrows a run to: the tests a consumer would run to
// exercise a mutant, rather than the whole package.
type TestRef struct {
	// Package is the import path of the package the test's binary was built
	// from.
	Package string
	// Name is the test's top-level name — a `TestX`, `ExampleX` or `FuzzX`. A
	// subtest is not named here; its parent is, because the parent is what a
	// run selects at the granularity a binary can be asked for.
	Name string
}

// CoveringTests reports, for every accepted mutant, the tests whose own
// coverage reaches its lines.
//
// It is the per-test twin of the covering *packages* a report already carries,
// and it exists for a consumer that keeps per-mutant evidence: knowing that a
// mutant is reached only by `TestClamp` of one package, rather than by that
// whole package, is what lets the consumer re-check the mutant against one test
// instead of a suite. The engine uses the same mapping to narrow a run; this
// hands it to a library caller to use however it keeps its own evidence.
//
// It measures what it reports rather than reusing a run's: it compiles the
// prepared test binaries once with coverage instrumentation, runs each of their
// tests on its own with no mutant activated, and maps every accepted mutant to
// the tests whose profile shows a covered statement on its lines. That is one
// full profiling pass, paid when the method is called and not before.
//
// A test that does not pass when run on its own is left out: its isolated
// coverage is not trustworthy — it is order-dependent, or was never green — so
// the tests it alone would cover are reported as covered by nothing rather than
// by a test that cannot be relied on to exercise them. A mutant no passing test
// reaches is absent from the returned map, which is the honest statement that
// this measurement found nothing covering it.
//
// The result keys are mutant ids, as [Mutant.ID] and [MutantResult.ID] carry
// them; each value is sorted by package and then by test name. Only accepted
// mutants appear: a mutant validation rejected has no prepared form to cover.
//
// The mapping is by line interval only, exactly as the engine's is: a block
// counts as reaching a mutant when it covers the line, whether or not the
// mutated expression was evaluated. The over-approximation errs towards naming
// a test rather than missing one, which is the safe direction for a consumer
// deciding what to re-run.
func (s *Session) CoveringTests(ctx context.Context) (map[string][]TestRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, fmt.Errorf("gomutants: session covering tests: %w", ErrSessionClosed)
	}

	mutants := make([]coverage.Mutant, 0, len(s.publicCatalog.Mutants))
	packages := make(map[string]bool)
	for _, m := range s.publicCatalog.Mutants {
		if !m.Accepted {
			continue
		}
		mutants = append(mutants, coverage.Mutant{
			ID:        m.ID,
			Path:      m.Path,
			StartLine: m.Line,
			EndLine:   m.EndLine,
		})
		packages[m.Package] = true
	}
	if len(mutants) == 0 {
		return map[string][]TestRef{}, nil
	}

	profiles, err := s.profilePerTest(ctx, packages)
	if err != nil {
		return nil, err
	}

	mapped := coverage.MapTests(coverage.TestOptions{
		ModulePath: s.modulePath,
		Mutants:    mutants,
		Profiles:   profiles,
	})

	covering := make(map[string][]TestRef, len(mapped.Covering))
	for id, keys := range mapped.Covering {
		refs := make([]TestRef, 0, len(keys))
		for _, key := range keys {
			refs = append(refs, TestRef{Package: key.ImportPath, Name: key.Name})
		}
		covering[id] = refs
	}
	return covering, nil
}

// profilePerTest compiles the prepared binaries with coverage on, runs each
// test alone, and returns the profile of every test that passed on its own,
// keyed by test.
func (s *Session) profilePerTest(ctx context.Context, packages map[string]bool) (map[coverage.TestKey]coverage.Profile, error) {
	scratch := filepath.Join(s.scratch, "covering")

	// The session's own build, with coverage turned on and its own
	// directories: the same instrumented tree, the same overlay carried in the
	// environment, the same scope — so the binaries are the ones a run measures,
	// built once more with `-cover`.
	//
	// -coverpkg names exactly the packages the accepted mutants live in, not
	// `<module>/...`. The module pattern would draw the go cover tool at every
	// package including the generated runtime, which this session carries in an
	// overlay rather than on disk — and the cover tool, run as a separate
	// process, cannot read the overlay and fails on a file that is not there.
	// The mutated packages are real files the overlay only replaces, so the
	// tool opens them, and they are the only packages whose line coverage the
	// mapping needs.
	opts := s.executeOptions
	opts.CoverPkg = coverPackagePattern(packages)
	opts.BinDir = filepath.Join(scratch, "bin")
	opts.ScratchDir = filepath.Join(scratch, "targets")

	bins, err := execute.BuildTestBinaries(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("gomutants: covering tests: building the coverage-instrumented binaries: %w", err)
	}

	collected, err := execute.CollectTestCoverage(ctx, opts, bins, filepath.Join(scratch, "raw"))
	if err != nil {
		return nil, fmt.Errorf("gomutants: covering tests: profiling each test: %w", err)
	}

	rendered := filepath.Join(scratch, "textfmt")
	if err := os.MkdirAll(rendered, 0o755); err != nil {
		return nil, fmt.Errorf("gomutants: covering tests: coverage directory: %w", err)
	}

	profiles := make(map[coverage.TestKey]coverage.Profile, len(collected))
	for i, data := range collected {
		if !data.Passed {
			continue
		}
		path := filepath.Join(rendered, strconv.Itoa(i)+".txt")
		profile, err := s.renderCoverageProfile(ctx, opts, data.Dir, path,
			data.ImportPath+" "+data.Name)
		if err != nil {
			return nil, err
		}
		profiles[coverage.TestKey{ImportPath: data.ImportPath, Name: data.Name}] = profile
	}
	return profiles, nil
}

// renderCoverageProfile converts one raw coverage directory into a textfmt
// document with `go tool covdata` and reads it back, exactly as the engine
// does — the toolchain writes the raw form and only it can render it.
func (s *Session) renderCoverageProfile(
	ctx context.Context,
	opts execute.Options,
	dir string,
	path string,
	subject string,
) (coverage.Profile, error) {
	spec := opts.Toolchain.Command("tool", "covdata", "textfmt", "-i="+dir, "-o="+path)
	spec.Dir = s.root
	spec.Env = opts.Env
	spec.Timeout = opts.Timeout
	spec.Trace = s.recorder
	spec.Kind = trace.ExecKindCovdataTextfmt
	spec.Subject = subject

	result := runner.Run(ctx, spec)
	if result.Err != nil {
		return coverage.Profile{}, fmt.Errorf(
			"gomutants: covering tests: rendering the coverage of %s: %w", subject, result.Err)
	}
	if result.ExitCode != 0 {
		return coverage.Profile{}, fmt.Errorf(
			"gomutants: covering tests: `go tool covdata textfmt` over %s exited with status %d",
			subject, result.ExitCode)
	}

	file, err := os.Open(path)
	if err != nil {
		return coverage.Profile{}, fmt.Errorf(
			"gomutants: covering tests: reading the coverage of %s: %w", subject, err)
	}
	profile, parseErr := coverage.ParseTextfmt(file)
	closeErr := file.Close()
	if parseErr != nil {
		return coverage.Profile{}, fmt.Errorf(
			"gomutants: covering tests: parsing the coverage of %s: %w", subject, parseErr)
	}
	if closeErr != nil {
		return coverage.Profile{}, fmt.Errorf(
			"gomutants: covering tests: closing the coverage of %s: %w", subject, closeErr)
	}
	return profile, nil
}

// coverPackagePattern joins the packages to instrument into a -coverpkg value,
// sorted so the value is the same between two runs of one session.
func coverPackagePattern(packages map[string]bool) string {
	paths := make([]string, 0, len(packages))
	for path := range packages {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return strings.Join(paths, ",")
}
