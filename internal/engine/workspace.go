// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/P4suta/go-mutants/internal/discover"
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

// workFile is the workspace file inside a snapshot of this tree, and the empty
// string for a tree that is one module.
//
// It is what every `go` command of a workspace run puts in GOWORK. See
// [discover.Options.WorkFile] for the half of the rule that does not change.
func (d discovered) workFile(root string) string {
	if !d.workspace {
		return ""
	}
	return filepath.Join(root, discover.WorkspaceFile)
}
