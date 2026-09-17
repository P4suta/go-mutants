// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/validate"
)

type discovered struct {
	modules   []discover.WorkspaceModule
	results   []discover.Result
	workspace bool
}

func discoverTree(
	ctx context.Context,
	workspace bool,
	opts discover.Options,
) (discovered, error) {
	if !workspace {
		result, err := discover.Discover(ctx, opts)
		if err != nil {
			return discovered{}, err
		}
		return discovered{
			modules: []discover.WorkspaceModule{{Dir: ".", Path: result.ModulePath}},
			results: []discover.Result{result},
		}, nil
	}
	found, err := discover.DiscoverWorkspace(ctx, opts)
	if err != nil {
		return discovered{}, err
	}
	tree := discovered{workspace: true}
	for _, module := range found {
		tree.modules = append(tree.modules, module.Module)
		tree.results = append(tree.results, module.Result)
	}
	return tree, nil
}

func (d discovered) candidates() []discover.Located {
	if len(d.results) == 1 {
		return d.results[0].Candidates
	}
	var all []discover.Located
	for _, result := range d.results {
		all = append(all, result.Candidates...)
	}
	return all
}

func (d discovered) skips() []discover.Skip {
	if len(d.results) == 1 {
		return d.results[0].Skips
	}
	var all []discover.Skip
	for _, result := range d.results {
		all = append(all, result.Skips...)
	}
	return all
}

func (d discovered) goVersion() string {
	if len(d.results) == 0 {
		return ""
	}
	return d.results[0].GoVersion
}

func (d discovered) modulePath() string {
	if d.workspace || len(d.modules) != 1 {
		return ""
	}
	return d.modules[0].Path
}

func (d discovered) validateModules() []validate.Module {
	modules := make([]validate.Module, 0, len(d.modules))
	for _, module := range d.modules {
		modules = append(modules, validate.Module{Dir: module.Dir, Path: module.Path})
	}
	return modules
}

func (d discovered) coverPkg(suffix string) string {
	paths := make([]string, 0, len(d.modules))
	for _, module := range d.modules {
		paths = append(paths, module.Path+suffix)
	}
	return strings.Join(paths, ",")
}

func workspaceEnv(env []string, workspace bool) []string {
	if !workspace {
		return env
	}
	return withoutGowork(env)
}

func engineCommandEnv(env []string, workspace bool) []string {
	if workspace {
		return withoutGowork(env)
	}
	return append(withoutGowork(env), "GOWORK=off")
}

func withoutGowork(env []string) []string {
	const key = "GOWORK="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, key) {
			out = append(out, entry)
		}
	}
	return out
}

func (s *session) document(opts report.Options, st *state) (*report.Report, *report.WorkspaceReport, error) {
	if !st.found.workspace {
		rep, err := report.Build(opts)
		return rep, nil, err
	}
	modules := make([]report.WorkspaceModule, 0, len(st.found.modules))
	for i, module := range st.found.modules {
		modules = append(modules, report.WorkspaceModule{
			Dir:      module.Dir,
			Path:     module.Path,
			Located:  st.found.results[i].Candidates,
			Skips:    st.found.results[i].Skips,
			Selected: st.selectedOf(module.Path),
		})
	}
	doc, err := report.BuildWorkspace(report.WorkspaceOptions{Options: opts, Modules: modules})
	return nil, doc, err
}

func (st *state) selectedOf(module string) int {
	selected := 0
	for _, m := range st.catalog.Mutants() {
		if m.ModulePath != module {
			continue
		}
		if _, notRun := st.notRun[m.ID]; notRun {
			continue
		}
		selected++
	}
	return selected
}

type publishedRunView struct {
	report    *report.Report
	workspace *report.WorkspaceReport
}

func publishedRun(rep *report.Report, workspace *report.WorkspaceReport) publishedRunView {
	return publishedRunView{report: rep, workspace: workspace}
}

func (p publishedRunView) reports() []*report.Report {
	if p.workspace != nil {
		return p.workspace.Reports()
	}
	if p.report == nil {
		return nil
	}
	return []*report.Report{p.report}
}

func (p publishedRunView) tally() (mutation.Tally, error) {
	if p.workspace != nil {
		return p.workspace.Tally()
	}
	return p.report.Tally()
}

func (p publishedRunView) expectationFailure() bool {
	if p.workspace != nil {
		return p.workspace.ExpectationFailure()
	}
	return p.report.ExpectationFailure()
}

func (p publishedRunView) mutants() []report.Mutant {
	if p.workspace == nil {
		if p.report == nil {
			return nil
		}
		return p.report.Mutants
	}
	var all []report.Mutant
	for _, rep := range p.reports() {
		all = append(all, rep.Mutants...)
	}
	return all
}

func (p publishedRunView) notable(st *state) []MutantResult {
	return notable(st, p.mutants())
}

func (p publishedRunView) uncovered() int { return uncoveredOf(p.mutants()) }

func (p publishedRunView) rejected() int {
	count := 0
	for _, rep := range p.reports() {
		count += len(rep.Rejected)
	}
	return count
}

func (p publishedRunView) cacheHits() int {
	hits := 0
	for _, rep := range p.reports() {
		hits += rep.Cache.Hits
	}
	return hits
}

func (p publishedRunView) cacheMode() report.CacheMode {
	for _, rep := range p.reports() {
		return rep.Cache.Mode
	}
	return ""
}

func (p publishedRunView) expectations() []report.Expectation {
	var rows []report.Expectation
	for _, rep := range p.reports() {
		rows = append(rows, rep.Expectations...)
	}
	if p.workspace != nil {
		rows = append(rows, p.workspace.Expectations...)
	}
	return rows
}

const wholeTree = "./..."

func expandWholeTree(argv []string, modules []discover.WorkspaceModule) []string {
	if len(modules) == 0 {
		return argv
	}
	out := make([]string, 0, len(argv)+len(modules))
	for _, arg := range argv {
		if arg != wholeTree {
			out = append(out, arg)
			continue
		}
		for _, module := range modules {
			out = append(out, modulePattern(module.Dir))
		}
	}
	return out
}

func modulePattern(dir string) string {
	if dir == "" || dir == "." {
		return wholeTree
	}
	return "./" + filepath.ToSlash(dir) + "/..."
}

func treePatterns(modules []discover.WorkspaceModule) []string {
	if len(modules) == 0 {
		return []string{wholeTree}
	}
	patterns := make([]string, 0, len(modules))
	for _, module := range modules {
		patterns = append(patterns, modulePattern(module.Dir))
	}
	return patterns
}

func workspaceModulesOf(workspace *discover.Workspace) []discover.WorkspaceModule {
	if workspace == nil {
		return nil
	}
	return workspace.Modules
}

func (st *state) moduleOf(id string) string {
	if st.catalog == nil {
		return ""
	}
	m, ok := st.catalog.ByID(id)
	if !ok {
		return ""
	}
	return m.ModulePath
}
