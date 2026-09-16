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

// discovered is what the discovery phase established about the tree, whether
// that tree is one module or a workspace of them.
//
// Everything below this phase asked one module's worth of questions --
// "the module path", "the candidates", "the skips" -- and in a workspace each
// of those has N answers. Holding the N and answering the old questions here is
// what keeps a workspace from being a second pipeline: the catalogue spans the
// modules, and so does everything derived from it.
type discovered struct {
	// modules are the modules measured, in order. A single-module tree is one
	// entry rooted at ".", so nothing below has to ask which kind it is.
	modules []discover.WorkspaceModule
	// results are their discoveries, in the same order.
	results []discover.Result
	// workspace is whether the tree is a `go.work`, which is not the same as
	// holding more than one module: a workspace may `use` exactly one, and its
	// mutants still carry a module path and are minted under the workspace
	// recipe. See [mutation.IDDomainWorkspace].
	workspace bool
}

// discoverTree runs discovery over a module or over every module of a
// workspace, and reports what it found as one thing.
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

// candidates is every module's candidates, in module order.
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

// skips is every module's skips, in module order.
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

// goVersion is the `go` directive the tree declares.
//
// The modules of a workspace may declare different ones, and the first is the
// answer here: the workspace builds with one toolchain, and what this feeds is
// the report's account of which one. A module's own directive is in its own
// go.mod, which the snapshot carries.
func (d discovered) goVersion() string {
	if len(d.results) == 0 {
		return ""
	}
	return d.results[0].GoVersion
}

// modulePath is the module path of a single-module tree.
//
// A workspace has none: `workspace.module_path` is required of a run report and
// a workspace has no single answer for it, which is why a workspace run
// publishes one report per module. See ADR 0012.
func (d discovered) modulePath() string {
	if d.workspace || len(d.modules) != 1 {
		return ""
	}
	return d.modules[0].Path
}

// validateModules is the tree's modules as the validation phase names them.
func (d discovered) validateModules() []validate.Module {
	modules := make([]validate.Module, 0, len(d.modules))
	for _, module := range d.modules {
		modules = append(modules, validate.Module{Dir: module.Dir, Path: module.Path})
	}
	return modules
}

// coverPkg is the `-coverpkg` argument that instruments every module of the
// tree, which is what cross-module coverage needs: a test in one module that
// reaches a mutant in another has to have been compiled with that module's
// packages instrumented, or its profile would not mention them at all.
func (d discovered) coverPkg(suffix string) string {
	paths := make([]string, 0, len(d.modules))
	for _, module := range d.modules {
		paths = append(paths, module.Path+suffix)
	}
	return strings.Join(paths, ",")
}

// workspaceEnv removes GOWORK for a workspace run, and leaves the environment
// of every other run exactly as it was.
//
// Removed rather than named, so that the go command finds the workspace file of
// the tree it is running in -- the snapshot, or a worker's copy of it -- by
// walking up from its own working directory. A named path could not promise
// that: a path has a spelling and a working directory has another, and a
// temporary directory reached through a symlink gives the go command a resolved
// cwd and an unresolved GOWORK, which it compares as text.
//
// Left alone rather than pinned to `off` for a run over one module, which is
// what internal/discover and internal/execute do for the commands *they* issue.
// The difference is whose command it is: those two build the snapshot, and the
// package set they build has to be the one discovery type-checked. This
// environment also carries the user's own test command, which is their program
// and not go-mutants'.
func workspaceEnv(env []string, workspace bool) []string {
	if !workspace {
		return env
	}
	return withoutGowork(env)
}

// engineCommandEnv is [workspaceEnv] for a command go-mutants writes itself.
//
// The distinction the paragraph above draws is whose command it is, and the
// baseline holds one of each: `go build` over the snapshot's own patterns is
// go-mutants', and the test command beside it is the user's. They were sharing
// an environment, so the build inherited whatever $GOWORK this process was
// started with -- a file outside the snapshot, describing modules the snapshot
// does not contain, reached by a build that is supposed to be a statement about
// the snapshot alone.
//
// So a command of ours says which it is, the way internal/discover and
// internal/execute already do: removed for a workspace run, `off` otherwise.
// Never left as it arrived, because arriving is not a decision.
func engineCommandEnv(env []string, workspace bool) []string {
	if workspace {
		return withoutGowork(env)
	}
	return append(withoutGowork(env), "GOWORK=off")
}

// withoutGowork is env with every GOWORK entry dropped.
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

// document builds the run's report: one run report for a tree of one module,
// and a workspace report holding one per module for a `go.work`.
//
// Exactly one of the two is returned, and which it is says which kind of run
// this was. A caller reading the wrong one gets nil rather than a document
// about something else, which is the answer that cannot be misread.
func (s *session) document(opts report.Options, st *state) (*report.Report, *report.WorkspaceReport, error) {
	if !st.found.workspace {
		rep, err := report.Build(opts)
		return rep, nil, err
	}
	// A workspace run's per-module facts: what discovery found in each module,
	// and how much of it the run set out to execute. Everything else --
	// the catalogue, every result, every rejection, the run's own facts -- is
	// the run's and is given once.
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

// selectedOf is how many of one module's mutants the run set out to execute.
//
// Counted rather than carried, because the selection is made over the whole
// catalogue: a run selects mutants, not modules, and every module's share of
// that is a fact about the selection rather than a second decision.
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

// published is the document a run published, whichever kind it is, and the
// answers the closing block and the exit code need out of it.
//
// It exists so that the difference between the two kinds is stated once. Every
// number below is read out of the published document rather than counted off
// the run beside it — which is the rule the closing block already followed —
// and for a workspace that means added up across the modules' documents,
// because those are the documents.
type publishedRunView struct {
	report    *report.Report
	workspace *report.WorkspaceReport
}

// publishedRun names whichever of the two a run produced.
func publishedRun(rep *report.Report, workspace *report.WorkspaceReport) publishedRunView {
	return publishedRunView{report: rep, workspace: workspace}
}

// reports is the run reports the run published: the one, or the modules'.
func (p publishedRunView) reports() []*report.Report {
	if p.workspace != nil {
		return p.workspace.Reports()
	}
	if p.report == nil {
		return nil
	}
	return []*report.Report{p.report}
}

// tally is what the run's own summary counts.
func (p publishedRunView) tally() (mutation.Tally, error) {
	if p.workspace != nil {
		return p.workspace.Tally()
	}
	return p.report.Tally()
}

// expectationFailure reports whether the ledger failed anywhere in the run.
func (p publishedRunView) expectationFailure() bool {
	if p.workspace != nil {
		return p.workspace.ExpectationFailure()
	}
	return p.report.ExpectationFailure()
}

// mutants is every mutant the run reported, in document order.
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

// notable is the results the closing block lists.
func (p publishedRunView) notable(st *state) []MutantResult {
	return notable(st, p.mutants())
}

// uncovered is how many mutants no test binary reaches.
func (p publishedRunView) uncovered() int { return uncoveredOf(p.mutants()) }

// rejected is how many candidates validation refused.
func (p publishedRunView) rejected() int {
	count := 0
	for _, rep := range p.reports() {
		count += len(rep.Rejected)
	}
	return count
}

// cacheHits is how many outcomes the run adopted from the cache.
func (p publishedRunView) cacheHits() int {
	hits := 0
	for _, rep := range p.reports() {
		hits += rep.Cache.Hits
	}
	return hits
}

// cacheMode is the mode the run resolved.
//
// One answer for the whole run, because there is one cache and one decision
// about it: every module's document records the same mode, and the first is as
// good as any. A run that published nothing has none.
func (p publishedRunView) cacheMode() report.CacheMode {
	for _, rep := range p.reports() {
		return rep.Cache.Mode
	}
	return ""
}

// expectations is every ledger row the run reported, module rows and orphans
// alike.
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

// wholeTree is the pattern that means "every package of the tree", as the go
// command spells it for one module.
const wholeTree = "./..."

// expandWholeTree rewrites `./...` into one pattern per module of a workspace,
// and leaves every other argument and every non-workspace run alone.
//
// The go command does not accept `./...` at a workspace root. The root is not
// itself a module, and a pattern beginning with `./` matches the packages under
// the current directory that the workspace's modules hold — which, spelled from
// the root, is `directory prefix . does not contain modules listed in go.work
// or their selected dependencies`. What the user meant by it is every module's
// own `./...`, and that is what `go test ./app/... ./lib/...` says.
//
// Only the exact pattern is rewritten. A user who narrowed their scope to
// `./app/internal/...` has already said which module they meant and in what
// terms, and second-guessing a pattern that works would be inventing a rule
// they did not write.
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

// modulePattern is `./...` rooted at one module of a workspace.
func modulePattern(dir string) string {
	if dir == "" || dir == "." {
		return wholeTree
	}
	return "./" + filepath.ToSlash(dir) + "/..."
}

// treePatterns is what "every package" means for a tree: one pattern for a
// module, and one per module for a workspace.
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

// workspaceModulesOf is a detected workspace's modules, and none at all for a
// tree that is one module.
func workspaceModulesOf(workspace *discover.Workspace) []discover.WorkspaceModule {
	if workspace == nil {
		return nil
	}
	return workspace.Modules
}

// moduleOf is the module one catalogued mutant belongs to, and the empty string
// for a run over one module -- or for a state with no catalogue, which is what
// a test that drives one phase on its own builds.
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
