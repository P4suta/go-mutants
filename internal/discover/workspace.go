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

const WorkspaceFile = "go.work"

const moduleFile = "go.mod"

type Workspace struct {
	Root    string
	Modules []WorkspaceModule
}

type WorkspaceModule struct {
	Dir  string
	Path string
}

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

type WorkspaceResult struct {
	Module WorkspaceModule
	Result Result
}

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
		moduleOpts.WorkspaceRoot = root
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
