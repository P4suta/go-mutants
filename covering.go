// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/execute"
)

type TestRef struct {
	Package string
	Name    string
}

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

func (s *Session) profilePerTest(ctx context.Context, packages map[string]bool) (map[coverage.TestKey]coverage.Profile, error) {
	scratch, err := os.MkdirTemp(s.scratch, "covering-")
	if err != nil {
		return nil, fmt.Errorf("gomutants: covering tests: scratch directory: %w", err)
	}
	if !s.keepTemp {
		defer func() { _ = os.RemoveAll(scratch) }()
	}

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

	profiles := make(map[coverage.TestKey]coverage.Profile, len(collected))
	for _, data := range collected {
		if !data.Passed {
			continue
		}
		profile, err := readCoverageProfile(data.Path, data.ImportPath+" "+data.Name)
		if err != nil {
			return nil, err
		}
		profiles[coverage.TestKey{ImportPath: data.ImportPath, Name: data.Name}] = profile
	}
	return profiles, nil
}

func readCoverageProfile(path, subject string) (coverage.Profile, error) {
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

func coverPackagePattern(packages map[string]bool) string {
	paths := make([]string, 0, len(packages))
	for path := range packages {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return strings.Join(paths, ",")
}
