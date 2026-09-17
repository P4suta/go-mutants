// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"

	"github.com/P4suta/go-mutants/trace"
)

const (
	moduleCall        = "module"
	moduleAllPackages = "./..."
	moduleListFields  = "ImportPath,Dir,Name,GoFiles,TestGoFiles,XTestGoFiles,Imports,Deps,EmbedFiles"
	moduleListLimit   = 32 << 20
)

var ErrInvalidQuery = errors.New("gomutants: module: invalid query")

type ModuleQuery struct {
	Packages []string
	Tags     []string
}

type Package struct {
	ImportPath   string
	Dir          string
	Name         string
	HasTests     bool
	GoFiles      []string
	TestGoFiles  []string
	XTestGoFiles []string
	Imports      []string
	Deps         []string
	EmbedFiles   []string
}

type Module struct {
	Path      string
	GoVersion string
	Toolchain string
	Packages  []Package
	TraceSeq  int64
}

type moduleAnswer struct {
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	interested int
	settled    bool
	module     Module
	err        error
}

type moduleQuery struct {
	packages []string
	tags     []string
}

func (w *Workspace) Module(ctx context.Context, q ModuleQuery) (Module, error) {
	if w == nil {
		return Module{}, errors.New("gomutants: module: nil workspace")
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if err := w.allowed(moduleCall); err != nil {
		return Module{}, err
	}
	query, err := normaliseModuleQuery(q)
	if err != nil {
		return Module{}, err
	}

	key := query.key()
	answer, leading := w.joinModule(key, ctx)
	release := w.moduleReleaser(key, answer)
	if !leading {
		defer release()
		select {
		case <-answer.done:
			if answer.err != nil {
				return Module{}, answer.err
			}
			return cloneModule(answer.module), nil
		case <-ctx.Done():
			return Module{}, moduleFailure(ctx.Err())
		}
	}
	watching := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			release()
		case <-watching:
		}
	}()

	var (
		module   Module
		listErr  error
		finished bool
	)
	defer func() {
		close(watching)
		if !finished && listErr == nil {
			listErr = errors.New("gomutants: module: the listing did not finish")
		}
		w.settleModule(key, answer, module, listErr)
		release()
	}()
	module, listErr = w.listModule(answer.ctx, query)
	finished = true
	if listErr != nil {
		return Module{}, listErr
	}
	return cloneModule(module), nil
}

func (w *Workspace) joinModule(key string, ctx context.Context) (*moduleAnswer, bool) {
	w.moduleMu.Lock()
	defer w.moduleMu.Unlock()
	if answer, ok := w.modules[key]; ok {
		answer.interested++
		return answer, false
	}
	listing, cancel := context.WithCancel(context.WithoutCancel(ctx))
	answer := &moduleAnswer{done: make(chan struct{}), ctx: listing, cancel: cancel, interested: 1}
	if w.modules == nil {
		w.modules = make(map[string]*moduleAnswer)
	}
	w.modules[key] = answer
	return answer, true
}

func (w *Workspace) moduleReleaser(key string, answer *moduleAnswer) func() {
	var once sync.Once
	return func() { once.Do(func() { w.leaveModule(key, answer) }) }
}

func (w *Workspace) leaveModule(key string, answer *moduleAnswer) {
	w.moduleMu.Lock()
	defer w.moduleMu.Unlock()
	answer.interested--
	if answer.interested > 0 || answer.settled {
		return
	}
	answer.cancel()
	if w.modules[key] == answer {
		delete(w.modules, key)
	}
}

func (w *Workspace) settleModule(key string, answer *moduleAnswer, module Module, err error) {
	w.moduleMu.Lock()
	defer w.moduleMu.Unlock()
	answer.module, answer.err, answer.settled = module, err, true
	switch {
	case w.modules[key] != answer:
	case err != nil:
		delete(w.modules, key)
	}
	answer.cancel()
	close(answer.done)
}

func (w *Workspace) forgetModules() {
	w.moduleMu.Lock()
	defer w.moduleMu.Unlock()
	clear(w.modules)
}

func (w *Workspace) listModule(ctx context.Context, query moduleQuery) (Module, error) {
	var module Module
	err := w.underTreeRead(func() error {
		if allowedErr := w.allowed(moduleCall); allowedErr != nil {
			return allowedErr
		}
		declared, err := readModuleFile(w.snapshot.Root)
		if err != nil {
			return err
		}
		listed, err := w.runCommand(ctx, Command{
			Argv:        query.argv(),
			Env:         []string{"GOWORK=off"},
			OutputLimit: moduleListLimit,
		}, w.env, commandLabel{kind: trace.ExecKindGoList, subject: moduleCall, splitStdout: true})
		if err != nil {
			return err
		}
		packages, err := decodeListing(query, listed)
		if err != nil {
			return err
		}
		module = Module{
			Path:      declared.Path,
			GoVersion: declared.GoVersion,
			Toolchain: w.toolchain.Version.Raw,
			Packages:  packages,
			TraceSeq:  listed.TraceSeq,
		}
		return nil
	})
	if err != nil {
		return Module{}, moduleFailure(err)
	}
	return module, nil
}

func moduleFailure(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalidQuery), errors.Is(err, ErrWorkspaceClosed), errors.Is(err, ErrPrepareFailed):
		return err
	}
	var typed *ExecutionError
	if errors.As(err, &typed) && typed.Call == moduleCall {
		return err
	}
	return &ExecutionError{
		Call:  moduleCall,
		Code:  DiagnosticCode(err),
		cause: fmt.Errorf("gomutants: module: %w", err),
	}
}

func readModuleFile(root string) (Module, error) {
	path := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		return Module{}, fmt.Errorf("read go.mod: %w", err)
	}
	parsed, err := modfile.ParseLax("go.mod", data, nil)
	if err != nil {
		return Module{}, fmt.Errorf("parse go.mod: %w", err)
	}
	var module Module
	if parsed.Module != nil {
		module.Path = parsed.Module.Mod.Path
	}
	if parsed.Go != nil {
		module.GoVersion = parsed.Go.Version
	}
	return module, nil
}

type listedPackage struct {
	ImportPath   string   `json:"ImportPath"`
	Dir          string   `json:"Dir"`
	Name         string   `json:"Name"`
	GoFiles      []string `json:"GoFiles"`
	TestGoFiles  []string `json:"TestGoFiles"`
	XTestGoFiles []string `json:"XTestGoFiles"`
	Imports      []string `json:"Imports"`
	Deps         []string `json:"Deps"`
	EmbedFiles   []string `json:"EmbedFiles"`
}

func decodeListing(query moduleQuery, listed commandRun) ([]Package, error) {
	switch {
	case listed.TimedOut:
		return nil, moduleListError(
			"gomutants: module: "+query.describe()+" did not answer within the command timeout", listed)
	case listed.ExitCode != 0:
		return nil, moduleListError(
			"gomutants: module: "+query.describe()+" exited with status "+strconv.Itoa(listed.ExitCode), listed)
	case listed.Truncated:
		return nil, moduleListError(
			"gomutants: module: "+query.describe()+" produced "+strconv.FormatInt(listed.TotalBytes, 10)+
				" bytes, more than the "+strconv.Itoa(moduleListLimit)+" this call keeps; "+
				"the tail of a JSON stream is not a shorter document but an unparsable one", listed)
	}

	decoder := json.NewDecoder(bytes.NewReader(listed.stdout))
	packages := make([]Package, 0, 16)
	for decoder.More() {
		var one listedPackage
		if err := decoder.Decode(&one); err != nil {
			return nil, moduleListError("gomutants: module: reading the "+query.describe()+
				" document: "+err.Error(), listed)
		}
		packages = append(packages, Package{
			ImportPath:   one.ImportPath,
			Dir:          one.Dir,
			Name:         one.Name,
			HasTests:     len(one.TestGoFiles) != 0 || len(one.XTestGoFiles) != 0,
			GoFiles:      one.GoFiles,
			TestGoFiles:  one.TestGoFiles,
			XTestGoFiles: one.XTestGoFiles,
			Imports:      one.Imports,
			Deps:         one.Deps,
			EmbedFiles:   one.EmbedFiles,
		})
	}
	slices.SortFunc(packages, func(a, b Package) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	packages = slices.CompactFunc(packages, func(a, b Package) bool { return a.ImportPath == b.ImportPath })
	return packages, nil
}

func moduleListError(message string, listed commandRun) error {
	return &ExecutionError{
		Call:   moduleCall,
		Output: string(listed.Output),
		cause:  errors.New(message),
	}
}

func normaliseModuleQuery(q ModuleQuery) (moduleQuery, error) {
	packages := slices.Clone(q.Packages)
	if len(packages) == 0 {
		packages = []string{moduleAllPackages}
	}
	for _, pattern := range packages {
		if err := checkModulePattern(pattern); err != nil {
			return moduleQuery{}, err
		}
	}
	tags := slices.Clone(q.Tags)
	for _, tag := range tags {
		if strings.TrimSpace(tag) == "" || strings.ContainsAny(tag, ", \t\r\n") {
			return moduleQuery{}, fmt.Errorf(
				"%w: build tag %q must be one tag, with no comma and no whitespace", ErrInvalidQuery, tag)
		}
	}
	slices.Sort(tags)
	tags = slices.Compact(tags)
	return moduleQuery{packages: packages, tags: tags}, nil
}

func checkModulePattern(pattern string) error {
	switch {
	case strings.TrimSpace(pattern) == "":
		return fmt.Errorf("%w: a package pattern is empty", ErrInvalidQuery)
	case strings.HasPrefix(pattern, "-"):
		return fmt.Errorf("%w: package pattern %q begins with a dash, which the go command reads as a flag",
			ErrInvalidQuery, pattern)
	case absoluteAnywhere(pattern):
		return fmt.Errorf("%w: package pattern %q is absolute; patterns are module-relative",
			ErrInvalidQuery, pattern)
	case slices.Contains(strings.Split(strings.ReplaceAll(pattern, `\`, "/"), "/"), ".."):
		return fmt.Errorf("%w: package pattern %q escapes the module", ErrInvalidQuery, pattern)
	case !relativePackagePattern(pattern):
		return fmt.Errorf(`%w: package pattern %q is not module-relative; use "." or a "./" pattern`,
			ErrInvalidQuery, pattern)
	}
	return nil
}

func absoluteAnywhere(pattern string) bool {
	native := filepath.FromSlash(pattern)
	switch {
	case strings.HasPrefix(pattern, "/"), strings.HasPrefix(pattern, `\\`):
		return true
	case filepath.IsAbs(native):
		return true
	case len(pattern) < 2 || pattern[1] != ':' || !isDriveLetter(pattern[0]):
		return false
	}
	return len(pattern) == 2 || pattern[2] == '/' || pattern[2] == '\\'
}

func isDriveLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func (q moduleQuery) argv() []string {
	argv := make([]string, 0, len(q.packages)+4)
	argv = append(argv, "go", "list", "-e=false", "-json="+moduleListFields)
	if len(q.tags) != 0 {
		argv = append(argv, "-tags="+strings.Join(q.tags, ","))
	}
	return append(argv, q.packages...)
}

func (q moduleQuery) key() string {
	var key strings.Builder
	for _, pattern := range q.packages {
		key.WriteString(strconv.Quote(pattern))
		key.WriteByte(' ')
	}
	key.WriteString("-tags ")
	for _, tag := range q.tags {
		key.WriteString(strconv.Quote(tag))
		key.WriteByte(' ')
	}
	return key.String()
}

func (q moduleQuery) describe() string {
	return "`" + strings.Join(q.argv(), " ") + "`"
}

func cloneModule(module Module) Module {
	clone := module
	if module.Packages == nil {
		return clone
	}
	clone.Packages = make([]Package, len(module.Packages))
	for i, pkg := range module.Packages {
		clone.Packages[i] = Package{
			ImportPath:   pkg.ImportPath,
			Dir:          pkg.Dir,
			Name:         pkg.Name,
			HasTests:     pkg.HasTests,
			GoFiles:      slices.Clone(pkg.GoFiles),
			TestGoFiles:  slices.Clone(pkg.TestGoFiles),
			XTestGoFiles: slices.Clone(pkg.XTestGoFiles),
			Imports:      slices.Clone(pkg.Imports),
			Deps:         slices.Clone(pkg.Deps),
			EmbedFiles:   slices.Clone(pkg.EmbedFiles),
		}
	}
	return clone
}
