// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
)

// WorkspaceFile is the name of the file whose presence at the snapshot root
// makes a tree a multi-module workspace.
const WorkspaceFile = "go.work"

// moduleFile is the file that makes a directory a module root.
const moduleFile = "go.mod"

// Workspace is what a [WorkspaceFile] at the snapshot root says about the tree.
//
// It is the answer to "which modules is this, and where are they", which is a
// different question from "is this a workspace" -- os.Stat answers that one.
// Both halves are needed before anything else can happen: the directories
// decide what a baseline runs in and what a test scope resolves against, and
// the module paths are the tenth field of a workspace mutant's identity, which
// is the only thing keeping two modules' `app.go` apart. See
// [mutation.IDDomainWorkspace].
type Workspace struct {
	// Root is the directory holding the workspace file, exactly as it was
	// given -- so a caller comparing it with the snapshot root compares two
	// strings from the same source.
	Root string
	// Modules are the modules the workspace joins, ordered by [
	// WorkspaceModule.Dir].
	//
	// Ordered, because the order decides which module's baseline runs first
	// and how far a partial run got, and an order taken from the order
	// somebody typed `use` lines in would reorder itself on an unrelated edit.
	Modules []WorkspaceModule
}

// WorkspaceModule is one module a [Workspace] joins.
type WorkspaceModule struct {
	// Dir is the module root relative to [Workspace.Root], slash-separated,
	// and "." for a workspace that uses its own root.
	Dir string
	// Path is the module's import path, read from the `module` line of its
	// go.mod by the same parser the go command uses.
	Path string
}

// DetectWorkspace reads the workspace a directory holds, and reports (nil, nil)
// for a directory that is a module rather than a workspace.
//
// Only the directory it is given is examined. The go command would also find a
// workspace file in a parent directory or through $GOWORK, and neither is part
// of the tree under test; see [environment] for how the loader is kept from
// obeying either.
//
// Everything it refuses is a tree nothing further can honestly measure, and the
// refusals divide into three kinds:
//
//   - A file that does not parse, or that uses nothing, leaves no modules to
//     measure and no way to say which module anything belongs to.
//   - A `use` that does not resolve to a module leaves a module path missing
//     from the identity table. A mutant with no module path is one minted under
//     the single-module recipe, silently sharing an identity with a mutant of
//     another module, and two modules declaring one path is the same collision
//     arriving by the other route.
//   - Anything reaching outside the root reaches outside the snapshot. The run
//     is keyed on the snapshot's digest, so a module or a replacement the
//     snapshot does not carry is one the measurement cannot be reproduced from.
//
// The refusals all carry [CodeWorkspace], which named exactly one condition
// before this and names exactly one now: a `go.work` this run cannot proceed
// on. What changed is which workspaces that is.
func DetectWorkspace(dir string) (*Workspace, error) {
	path := filepath.Join(dir, WorkspaceFile)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, &Error{
			Code:    CodeWorkspace,
			Message: path + " could not be read",
			Err:     err,
		}
	}
	parsed, err := modfile.ParseWork(path, data, nil)
	if err != nil {
		return nil, &Error{
			Code:    CodeWorkspace,
			Message: path + " is not a workspace file the go command can parse",
			Err:     err,
		}
	}
	if len(parsed.Use) == 0 {
		return nil, &Error{
			Code: CodeWorkspace,
			Message: path + " uses no module, so there is nothing to measure; " +
				"add a `use` line for each module of the workspace",
		}
	}
	modules, err := workspaceModules(dir, path, parsed)
	if err != nil {
		return nil, err
	}
	if err := checkReplacements(dir, path, parsed); err != nil {
		return nil, err
	}
	return &Workspace{Root: dir, Modules: modules}, nil
}

// workspaceModules resolves every `use` to a module inside the root.
func workspaceModules(root, path string, parsed *modfile.WorkFile) ([]WorkspaceModule, error) {
	modules := make([]WorkspaceModule, 0, len(parsed.Use))
	for _, use := range parsed.Use {
		rel, err := containedIn(root, use.Path)
		if err != nil {
			return nil, &Error{
				Code: CodeWorkspace,
				Message: path + " uses " + use.Path + ", which is outside " + root + "; " +
					"a module the snapshot does not carry is one the measurement " +
					"cannot be reproduced from",
			}
		}
		module, err := moduleAt(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, &Error{
				Code:    CodeWorkspace,
				Message: path + " uses " + use.Path + ", and " + err.Error(),
			}
		}
		modules = append(modules, WorkspaceModule{Dir: rel, Path: module})
	}
	slices.SortFunc(modules, func(a, b WorkspaceModule) int {
		return strings.Compare(a.Dir, b.Dir)
	})
	// Both collisions are properties of the whole set rather than of adjacent
	// rows: two modules spelling one path need not be neighbours in directory
	// order, and a check that compared neighbours would let a third module
	// between them hide the pair.
	dirs := make(map[string]string, len(modules))
	paths := make(map[string]string, len(modules))
	for _, module := range modules {
		if _, used := dirs[module.Dir]; used {
			return nil, &Error{
				Code:    CodeWorkspace,
				Message: path + " uses " + module.Dir + " more than once",
			}
		}
		dirs[module.Dir] = module.Path
		if first, declared := paths[module.Path]; declared {
			return nil, &Error{
				Code: CodeWorkspace,
				Message: path + " joins two modules declaring the module path " +
					module.Path + " (" + first + " and " + module.Dir +
					"); a mutant's identity is its module-relative path under its " +
					"module path, so two modules spelling one path cannot be told apart",
			}
		}
		paths[module.Path] = module.Dir
	}
	return modules, nil
}

// checkReplacements refuses a `replace` directive pointing outside the root.
//
// A replacement by module version names no directory and is the build's
// business rather than this one's. A replacement by filesystem path is a
// directory the build reads, so it has to be one the snapshot carries.
func checkReplacements(root, path string, parsed *modfile.WorkFile) error {
	for _, replace := range parsed.Replace {
		if replace.New.Version != "" {
			continue
		}
		if _, err := containedIn(root, replace.New.Path); err != nil {
			return &Error{
				Code: CodeWorkspace,
				Message: path + " replaces " + replace.Old.Path + " with " +
					replace.New.Path + ", which is outside " + root + "; " +
					"the snapshot would not carry it and the build inside it " +
					"would resolve differently",
			}
		}
	}
	return nil
}

// containedIn resolves a workspace-file path against the root and returns it
// slash-separated and relative, or an error when it leaves the root.
//
// The comparison is lexical, which is what it has to be: the root is the string
// the caller gave and every path here is resolved against that same string, so
// the answer does not depend on what the filesystem happens to link where.
func containedIn(root, declared string) (string, error) {
	resolved := filepath.FromSlash(declared)
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(root, resolved)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", errors.New("outside the root")
	}
	return rel, nil
}

// moduleAt returns the module path declared by the go.mod in dir.
//
// Its errors are clauses rather than sentences, because every caller here has
// already said which `use` line led to the directory.
func moduleAt(dir string) (string, error) {
	path := filepath.Join(dir, moduleFile)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", errors.New("there is no " + moduleFile + " in " + dir)
	case err != nil:
		return "", errors.New(path + " could not be read: " + err.Error())
	}
	module := modfile.ModulePath(data)
	if module == "" {
		return "", errors.New(path + " declares no module path")
	}
	return module, nil
}

// WorkspaceResult is one module's discovery inside a workspace.
//
// It is a [Result] and the module it belongs to, and the [Result] is exactly
// the one a run over that module alone would produce -- same shape, same
// fields, same module-relative paths -- with one difference: every candidate
// carries the module's path, so the catalogue can tell two modules' `app.go`
// apart. That sameness is the design. A workspace run is N module runs under
// one run id, each publishing an unmodified run report of its own, and a
// per-module result that had a different shape would be a second document type
// arriving through the back door.
type WorkspaceResult struct {
	// Module is which module this is, and where.
	Module WorkspaceModule
	// Result is what discovery found in it.
	Result Result
}

// DiscoverWorkspace discovers every module of a workspace, in module order.
//
// Each module is discovered on its own, rooted at its own directory, so
// everything a [Result] carries is relative to the module it belongs to. What
// differs from N separate runs is three things, and each of them is a thing a
// workspace makes true:
//
//   - The loader obeys the snapshot's own workspace file rather than running
//     with GOWORK=off, because a module of a workspace resolves its siblings
//     through it and would not load without it. Every workspace file outside
//     the snapshot stays ignored. See [Options.Workspace].
//   - Every candidate carries its module's path, which is the coordinate that
//     keeps two modules' identically named files apart. See
//     [mutation.Identity.ModulePath].
//   - The include and exclude patterns are matched against workspace-relative
//     paths, because that is the tree the person writing them is looking at.
//     See [Options.PathPrefix].
//
// A directory that is not a workspace is refused rather than discovered as the
// module it is: a caller that asked for a workspace and was handed one module's
// candidates with no module path on them would have a single-module catalogue
// wearing a workspace run's name.
func DiscoverWorkspace(ctx context.Context, opts Options) ([]WorkspaceResult, error) {
	root, err := resolveRoot(opts.SnapshotRoot)
	if err != nil {
		return nil, err
	}
	workspace, err := DetectWorkspace(root)
	if err != nil {
		return nil, err
	}
	if workspace == nil {
		return nil, &Error{
			Code: CodeSnapshotRoot,
			Message: "there is no " + WorkspaceFile + " at " + root +
				", so it is a module rather than a workspace",
		}
	}
	results := make([]WorkspaceResult, 0, len(workspace.Modules))
	for _, module := range workspace.Modules {
		moduleOpts := opts
		moduleOpts.SnapshotRoot = filepath.Join(root, filepath.FromSlash(module.Dir))
		moduleOpts.Workspace = true
		if module.Dir != "." {
			moduleOpts.PathPrefix = module.Dir
		}
		result, err := Discover(ctx, moduleOpts)
		if err != nil {
			return nil, err
		}
		if result.ModulePath != module.Path {
			return nil, &Error{
				Code: CodeWorkspace,
				Message: filepath.Join(root, WorkspaceFile) + " uses " + module.Dir +
					" as " + module.Path + ", and the go command loaded " +
					result.ModulePath + " there",
			}
		}
		for i := range result.Candidates {
			result.Candidates[i].ModulePath = module.Path
		}
		results = append(results, WorkspaceResult{Module: module, Result: result})
	}
	return results, nil
}

// CheckWorkspace reports a directory holding a [WorkspaceFile] as
// [CodeWorkspace], and nil for one that is a module rather than a workspace.
//
// [Discover] calls it on the snapshot, which is where the refusal has to be
// final: everything [Discover] does — the identities it mints, the digests, the
// one module path it reports — assumes one module, and a workspace is measured
// by [DiscoverWorkspace] instead. It is exported so that a caller can ask the
// question *before* paying for the answer: a pass pointed at a workspace root
// would otherwise load the tree before arriving here, and the first thing to go
// wrong on that route is not this refusal at all — `go list ./...` at a
// workspace root places no package, so the caller is told that their package
// pattern matches nothing, which is a true sentence about the wrong subject.
//
// It is [DetectWorkspace] with the answer thrown away, and a workspace is
// refused *after* it has been read rather than before: a malformed workspace
// file says what is wrong with it here, rather than saying only that this is
// not the entry point for one.
func CheckWorkspace(dir string) error {
	workspace, err := DetectWorkspace(dir)
	if err != nil {
		return err
	}
	if workspace == nil {
		return nil
	}
	return &Error{
		Code: CodeWorkspace,
		Message: filepath.Join(dir, WorkspaceFile) + " makes this a multi-module " +
			"workspace, and a single-module discovery cannot measure one: its " +
			"mutants have no module to be relative to and its modules have no " +
			"one baseline. Point this at one of the workspace's modules, or " +
			"discover the workspace itself",
	}
}
