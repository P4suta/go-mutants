// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
	"github.com/P4suta/go-mutants/goatest/internal/report"
	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

var repositoryTestLogMagic = []byte("# test log\n")

type RepositoryObserver struct {
	root       string
	directory  string
	candidates map[string]goanalysis.RepositoryReadCandidate
	packages   map[string]goanalysis.Package
	sources    targetKeySources
	files      repositoryObserverFiles
}

func repositoryObservationScope(root string, packages []goanalysis.Package) (map[string]goanalysis.RepositoryReadCandidate, map[string]bool) {
	candidates := goanalysis.RepositoryReadCandidates(root, packages)
	return completeRepositoryObservationScope(candidates, packages)
}

func completeRepositoryObservationScope(
	candidates map[string]goanalysis.RepositoryReadCandidate,
	packages []goanalysis.Package,
) (map[string]goanalysis.RepositoryReadCandidate, map[string]bool) {
	readers := make(map[string]bool, len(packages))
	for _, pkg := range packages {
		if _, found := candidates[pkg.ImportPath]; !found {
			candidates[pkg.ImportPath] = goanalysis.RepositoryReadCandidate{}
		}
		readers[pkg.ImportPath] = true
	}
	return candidates, readers
}

func newRepositoryObserver(root, directory string, candidates map[string]goanalysis.RepositoryReadCandidate, sources targetKeySources) *RepositoryObserver {
	return newRepositoryObserverWithFiles(root, directory, candidates, sources, repositoryObserverFiles{
		absolute: filepath.Abs,
		create:   func(directory, pattern string) (repositoryLogFile, error) { return os.CreateTemp(directory, pattern) },
		remove:   os.Remove,
		read:     os.ReadFile,
	})
}

type repositoryLogFile interface {
	Name() string
	Close() error
}

type repositoryObserverFiles struct {
	absolute func(string) (string, error)
	create   func(string, string) (repositoryLogFile, error)
	remove   func(string) error
	read     func(string) ([]byte, error)
}

func newRepositoryObserverWithFiles(
	root, directory string,
	candidates map[string]goanalysis.RepositoryReadCandidate,
	sources targetKeySources,
	files repositoryObserverFiles,
) *RepositoryObserver {
	packages := make(map[string]goanalysis.Package, len(sources.model.Packages))
	for _, pkg := range sources.model.Packages {
		packages[pkg.ImportPath] = pkg
	}
	absolute, err := files.absolute(root)
	if err != nil || root == "" {
		absolute = ""
	} else {
		absolute = filepath.Clean(absolute)
	}
	selected := make(map[string]goanalysis.RepositoryReadCandidate, len(candidates))
	for path, candidate := range candidates {
		selected[path] = candidate
	}
	if strings.ContainsRune(absolute, '\n') || slices.ContainsFunc(sources.extraFiles, func(name string) bool {
		return strings.ContainsRune(name, '\n')
	}) {
		for path, candidate := range selected {
			candidate.Unobservable = true
			selected[path] = candidate
		}
	}
	return &RepositoryObserver{
		root: absolute, directory: directory,
		candidates: selected, packages: packages, sources: sources, files: files,
	}
}

func fixedRepositoryObservation(reason wholeTreeReason) func() repositoryObservation {
	return func() repositoryObservation { return repositoryObservation{reason: reason} }
}

func (observer *RepositoryObserver) instrumentPackage(pkg string, arguments []string) ([]string, func() repositoryObservation) {
	if observer == nil {
		return arguments, fixedRepositoryObservation(wholeTreeObserved)
	}
	owner, known := observer.packages[pkg]
	if !known {
		if _, selected := observer.candidate(pkg); selected {
			return arguments, fixedRepositoryObservation(wholeTreeStaticUnobservable)
		}
		return arguments, fixedRepositoryObservation(wholeTreeObserved)
	}
	return observer.instrument(pkg, owner.RelativeDir, arguments)
}

type wholeTreeReason string

const (
	wholeTreeObserved           wholeTreeReason = ""
	wholeTreeStaticUnobservable wholeTreeReason = trace.WholeTreeStaticUnobservable
	wholeTreeLogUnavailable     wholeTreeReason = trace.WholeTreeLogUnavailable
	wholeTreeLogAmbiguous       wholeTreeReason = trace.WholeTreeLogAmbiguous
	wholeTreeDirectoryAccess    wholeTreeReason = trace.WholeTreeDirectoryAccess
	wholeTreeOutsideInput       wholeTreeReason = trace.WholeTreeOutsideInput
)

type repositoryObservation struct {
	reason   wholeTreeReason
	accesses []repositoryAccess
}

type repositoryAccess struct {
	path      string
	directory bool
}

func (observer *RepositoryObserver) instrument(pkg, relativeDir string, arguments []string) ([]string, func() repositoryObservation) {
	candidate, selected := observer.candidate(pkg)
	if !selected {
		return arguments, fixedRepositoryObservation(wholeTreeObserved)
	}
	if candidate.Unobservable {
		return arguments, fixedRepositoryObservation(wholeTreeStaticUnobservable)
	}
	if observer.root == "" || observer.directory == "" {
		return arguments, fixedRepositoryObservation(wholeTreeLogUnavailable)
	}
	file, err := observer.files.create(observer.directory, "test-action-*.log")
	if err != nil {
		return arguments, fixedRepositoryObservation(wholeTreeLogUnavailable)
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = observer.files.remove(name)
		return arguments, fixedRepositoryObservation(wholeTreeLogUnavailable)
	}
	instrumented := append(slices.Clone(arguments), "-test.testlogfile="+name)
	initialDirectory := filepath.Join(observer.root, filepath.FromSlash(relativeDir))
	return instrumented, func() repositoryObservation {
		defer func() { _ = observer.files.remove(name) }()
		data, err := observer.files.read(name)
		if err != nil {
			return repositoryObservation{reason: wholeTreeLogUnavailable}
		}
		return parseRepositoryTestLog(data, observer.root, initialDirectory)
	}
}

func (observer *RepositoryObserver) candidate(pkg string) (goanalysis.RepositoryReadCandidate, bool) {
	if observer == nil {
		return goanalysis.RepositoryReadCandidate{}, false
	}
	candidate, selected := observer.candidates[pkg]
	return candidate, selected
}

func (observer *RepositoryObserver) wholeTree(target goanalysis.Target, observation repositoryObservation) bool {
	return observer.wholeTreeReason(target, observation) != wholeTreeObserved
}

func (observer *RepositoryObserver) wholeTreeReason(target goanalysis.Target, observation repositoryObservation) wholeTreeReason {
	candidate, selected := observer.candidate(target.Package)
	if !selected {
		return wholeTreeObserved
	}
	if candidate.Unobservable {
		return wholeTreeStaticUnobservable
	}
	if observation.reason != wholeTreeObserved {
		return observation.reason
	}
	inputs := observer.sources.narrowInputsFor(target)
	for _, access := range observation.accesses {
		if access.directory {
			return wholeTreeDirectoryAccess
		}
		if _, known := inputs.Files[access.path]; known {
			continue
		}
		if _, known := inputs.Corpus[access.path]; !known {
			return wholeTreeOutsideInput
		}
	}
	return wholeTreeObserved
}

func (observer *RepositoryObserver) wholeTreeSuite(pkg string, observation repositoryObservation) bool {
	return observer.wholeTreeSuiteReason(pkg, observation) != wholeTreeObserved
}

func (observer *RepositoryObserver) wholeTreeSuiteReason(pkg string, observation repositoryObservation) wholeTreeReason {
	if observer == nil {
		return wholeTreeObserved
	}
	owner, known := observer.packages[pkg]
	if !known {
		if _, selected := observer.candidate(pkg); selected {
			return wholeTreeStaticUnobservable
		}
		return wholeTreeObserved
	}
	return observer.wholeTreeReason(goanalysis.Target{
		Package: pkg, RelativeDir: owner.RelativeDir, Dependencies: owner.Dependencies,
	}, observation)
}

func wholeTreeKeyLimitation(targets []TargetEvidence, suites map[string]PackageSuiteCoverage) (report.Limitation, bool) {
	widenedTargets, widenedSuites := wholeTreeWideningCounts(targets, suites)
	if widenedTargets == 0 && widenedSuites == 0 {
		return report.Limitation{}, false
	}
	return report.Limitation{
		Code: report.LimitationWholeTreeBehaviourKeys,
		Summary: fmt.Sprintf(
			"%d of %d targets and %d of %d package suites read outside their ordinary inputs, so their evidence is reused only while nothing in the tree changes; goatest trace summary names which boundary widened each one",
			widenedTargets, len(targets), widenedSuites, len(suites)),
	}, true
}

func wholeTreeWideningCounts(targets []TargetEvidence, suites map[string]PackageSuiteCoverage) (int, int) {
	widenedTargets := 0
	for _, target := range targets {
		if target.WholeTree {
			widenedTargets++
		}
	}
	widenedSuites := 0
	for _, suite := range suites {
		if suite.WholeTree {
			widenedSuites++
		}
	}
	return widenedTargets, widenedSuites
}

func parseRepositoryTestLog(data []byte, root, initialDirectory string) repositoryObservation {
	return parseRepositoryTestLogWithStat(data, root, initialDirectory, os.Stat)
}

func parseRepositoryTestLogWithStat(
	data []byte,
	root, initialDirectory string,
	stat func(string) (os.FileInfo, error),
) repositoryObservation {
	if !bytes.HasPrefix(data, repositoryTestLogMagic) || len(data) == 0 || data[len(data)-1] != '\n' {
		return repositoryObservation{reason: wholeTreeLogAmbiguous}
	}
	observation := repositoryObservation{}
	workingDirectory := initialDirectory
	for _, raw := range bytes.Split(bytes.TrimPrefix(data, repositoryTestLogMagic), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		operation, name, found := strings.Cut(string(raw), " ")
		if !found || name == "" {
			observation.reason = wholeTreeLogAmbiguous
			continue
		}
		switch operation {
		case "getenv":
			continue
		case "chdir":
			if !filepath.IsAbs(name) {
				observation.reason = wholeTreeLogAmbiguous
				continue
			}
			workingDirectory = filepath.Clean(name)
			if relative, inside := repositoryRelativePath(root, workingDirectory); inside {
				observation.accesses = append(observation.accesses, repositoryAccess{path: relative, directory: true})
			}
		case "open", "stat":
			resolved := name
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(workingDirectory, resolved)
			}
			resolved = filepath.Clean(resolved)
			relative, inside := repositoryRelativePath(root, resolved)
			if !inside {
				continue
			}
			info, err := stat(resolved)
			if err != nil {
				observation.accesses = append(observation.accesses, repositoryAccess{path: relative, directory: true})
				continue
			}
			observation.accesses = append(observation.accesses, repositoryAccess{path: relative, directory: info.IsDir()})
		default:
			observation.reason = wholeTreeLogAmbiguous
		}
	}
	return observation
}

func repositoryRelativePath(root, name string) (string, bool) {
	return repositoryRelativePathWith(root, name, filepath.Rel)
}

func repositoryRelativePathWith(root, name string, relativePath func(string, string) (string, error)) (string, bool) {
	if root == "" || name == "" {
		return "", false
	}
	relative, err := relativePath(root, name)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func repositoryTestLogFailure(output string, arguments []string) bool {
	path, found := repositoryTestLogPath(arguments)
	if !found {
		return false
	}
	if !strings.Contains(output, "testing:") {
		return false
	}
	if strings.Contains(output, path) {
		return true
	}

	encoded, _ := json.Marshal(path)
	return strings.Contains(output, string(encoded[1:len(encoded)-1]))
}

func repositoryTestLogPath(arguments []string) (string, bool) {
	for _, argument := range arguments {
		if path, found := strings.CutPrefix(argument, "-test.testlogfile="); found {
			return path, path != ""
		}
	}
	return "", false
}

func unmeasuredSuiteLimitation(unmeasured map[string]gomutants.ProbeOutcome) (report.Limitation, bool) {
	if len(unmeasured) == 0 {
		return report.Limitation{}, false
	}
	byOutcome := make(map[gomutants.ProbeOutcome][]string)
	for pkg, outcome := range unmeasured {
		byOutcome[outcome] = append(byOutcome[outcome], pkg)
	}
	var clauses []string
	for _, outcome := range gomutants.KnownProbeOutcomes() {
		packages := byOutcome[outcome]
		if len(packages) == 0 {
			continue
		}
		slices.Sort(packages)
		clauses = append(clauses, fmt.Sprintf("%s: %s", suiteUnmeasuredReason(outcome), strings.Join(packages, ", ")))
	}
	return report.Limitation{
		Code: report.LimitationPackageSuiteUnmeasured,
		Summary: fmt.Sprintf(
			"%d package suites produced no coverage facts, so every mutant in them that no target reaches was bounded by its own exact original control alone (%s)",
			len(unmeasured), strings.Join(clauses, "; ")),
	}, true
}

func suiteUnmeasuredReason(outcome gomutants.ProbeOutcome) string {
	reason := ""
	switch outcome {
	case gomutants.ProbeMeasured:
	case gomutants.ProbeTestFailed:
		reason = "their tests did not pass"
	case gomutants.ProbeTimedOut:
		reason = "they did not finish inside their budget"
	case gomutants.ProbeUnavailable:
		reason = "no coverage facts were produced"
	}
	return reason
}
