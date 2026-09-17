// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"cmp"
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
)

const loadMode = packages.NeedName |
	packages.NeedFiles |
	packages.NeedCompiledGoFiles |
	packages.NeedSyntax |
	packages.NeedTypes |
	packages.NeedTypesInfo |
	packages.NeedModule

const errorSample = 3

type loadResult struct {
	fset     *token.FileSet
	packages []*packages.Package
}

func load(
	ctx context.Context,
	root string,
	toolchain gocmd.Toolchain,
	baseEnv []string,
	workspace bool,
	patterns []string,
) (*loadResult, error) {
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	fset := token.NewFileSet()
	cfg := &packages.Config{
		Context: ctx,
		Mode:    loadMode,
		Dir:     root,
		Env:     environmentFrom(baseEnv, toolchain, workspace),
		Fset:    fset,
		Tests:   false,
	}
	loaded, err := packages.Load(cfg, patterns...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, &Error{Code: CodeLoadFailed, Message: "discovery was cancelled", Err: ctxErr}
		}
		return nil, &Error{
			Code: CodeLoadFailed,
			Message: "could not load the packages under " + strconv.Quote(root) +
				"; go/packages runs the `go` command found on this process's PATH" +
				toolchainHint(toolchain),
			Err: err,
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, &Error{Code: CodeLoadFailed, Message: "discovery was cancelled", Err: ctxErr}
	}
	slices.SortFunc(loaded, func(x, y *packages.Package) int {
		return cmp.Or(strings.Compare(x.PkgPath, y.PkgPath), strings.Compare(x.ID, y.ID))
	})
	return &loadResult{fset: fset, packages: loaded}, nil
}

func toolchainHint(toolchain gocmd.Toolchain) string {
	if toolchain.GoBin == "" {
		return ""
	}
	return " (the located toolchain is " + toolchain.GoBin + ")"
}

func environment(toolchain gocmd.Toolchain) []string {
	return environmentFrom(nil, toolchain, false)
}

func environmentFrom(base []string, toolchain gocmd.Toolchain, workspace bool) []string {
	if base == nil {
		base = os.Environ()
	} else {
		base = slices.Clone(base)
	}
	env := setEnv(base, "GOWORK", "off")
	if workspace {
		env = unsetEnv(base, "GOWORK")
	}
	if toolchain.GoBin == "" {
		return env
	}
	dir := filepath.Dir(toolchain.GoBin)
	if dir == "" || dir == "." {
		return env
	}
	for i, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !sameEnvKey(key, "PATH") {
			continue
		}
		if value == dir || strings.HasPrefix(value, dir+string(filepath.ListSeparator)) {
			return env
		}
		env[i] = key + "=" + dir + string(filepath.ListSeparator) + value
		return env
	}
	return append(env, "PATH="+dir)
}

func setEnv(env []string, name, value string) []string {
	entry := name + "=" + value
	out := make([]string, 0, len(env)+1)
	set := false
	for _, existing := range env {
		key, _, ok := strings.Cut(existing, "=")
		if ok && sameEnvKey(key, name) {
			if set {
				continue
			}
			existing, set = entry, true
		}
		out = append(out, existing)
	}
	if !set {
		out = append(out, entry)
	}
	return out
}

func unsetEnv(env []string, name string) []string {
	out := make([]string, 0, len(env))
	for _, existing := range env {
		if key, _, ok := strings.Cut(existing, "="); ok && sameEnvKey(key, name) {
			continue
		}
		out = append(out, existing)
	}
	return out
}

func sameEnvKey(a, b string) bool { return sameEnvKeyOn(runtime.GOOS, a, b) }

func sameEnvKeyOn(goos, a, b string) bool {
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func mainModule(loaded *loadResult, root string) (*packages.Module, error) {
	for _, pkg := range loaded.packages {
		module := pkg.Module
		if module == nil || !module.Main || module.Dir == "" {
			continue
		}
		if samePath(module.Dir, root) {
			return module, nil
		}
		return nil, &Error{
			Code: CodeModuleNotFound,
			Message: "the main module " + module.Path + " is rooted at " + strconv.Quote(module.Dir) +
				", not at the snapshot root " + strconv.Quote(root),
		}
	}
	return nil, &Error{
		Code:    CodeModuleNotFound,
		Message: "no Go package under " + strconv.Quote(root) + " belongs to a module rooted there",
	}
}

func gate(loaded *loadResult, exempt cgoExemption) error {
	var (
		total  int
		sample []string
	)
	for _, pkg := range loaded.packages {
		if exempt.covers(pkg) {
			continue
		}
		for _, e := range pkg.Errors {
			total++
			if len(sample) < errorSample {
				sample = append(sample, formatPackageError(pkg, e))
			}
		}
	}
	if total == 0 {
		return nil
	}
	message := "discovery needs a tree that compiles, and " + plural(total, "package error") + " stopped it: " +
		strings.Join(sample, "; ")
	if total > len(sample) {
		message += "; and " + plural(total-len(sample), "more error")
	}
	return &Error{Code: CodePackageErrors, Message: message}
}

func formatPackageError(pkg *packages.Package, e packages.Error) string {
	var b strings.Builder
	if path := packagePath(pkg); path != "" {
		b.WriteString(path)
		b.WriteString(": ")
	}
	if e.Pos != "" {
		b.WriteString(collapseLines(e.Pos))
		b.WriteString(": ")
	}
	b.WriteString(collapseLines(e.Msg))
	return b.String()
}

func collapseLines(s string) string {
	s = strings.TrimSpace(s)
	if !strings.ContainsAny(s, "\n\r") {
		return s
	}
	parts := make([]string, 0, 4)
	for _, line := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "; ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

type fileRef struct {
	abs string
	rel string
}

func moduleFiles(pkg *packages.Package, root string) []fileRef {
	var refs []fileRef
	seen := make(map[string]bool)
	for _, list := range [][]string{pkg.GoFiles, pkg.IgnoredFiles} {
		for _, abs := range list {
			if seen[abs] || !strings.HasSuffix(abs, ".go") || isTestFile(abs) {
				continue
			}
			seen[abs] = true
			rel, ok := relativePath(root, abs)
			if !ok {
				continue
			}
			refs = append(refs, fileRef{abs: abs, rel: rel})
		}
	}
	slices.SortFunc(refs, func(x, y fileRef) int { return strings.Compare(x.rel, y.rel) })
	return refs
}

type cgoExemption map[string]bool

func (e cgoExemption) covers(pkg *packages.Package) bool { return e[pkg.ID] }

func findCgoPackages(loaded *loadResult, root string) cgoExemption {
	exemption := make(cgoExemption)
	fset := token.NewFileSet()
	answers := make(map[string]bool)
	for _, pkg := range loaded.packages {
		for _, ref := range moduleFiles(pkg, root) {
			answer, known := answers[ref.abs]
			if !known {
				answer = importsC(fset, ref.abs)
				answers[ref.abs] = answer
			}
			if !answer {
				continue
			}
			exemption[pkg.ID] = true
			break
		}
	}
	return exemption
}

func importsC(fset *token.FileSet, path string) bool {
	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil || file == nil {
		return false
	}
	for _, spec := range file.Imports {
		if spec.Path != nil && spec.Path.Value == `"C"` {
			return true
		}
	}
	return false
}

func isTestFile(path string) bool {
	return strings.HasSuffix(filepath.Base(path), "_test.go")
}

func samePath(a, b string) bool {
	cleanA, cleanB := filepath.Clean(a), filepath.Clean(b)
	if pathsEqual(cleanA, cleanB) {
		return true
	}
	resolvedA, errA := filepath.EvalSymlinks(cleanA)
	resolvedB, errB := filepath.EvalSymlinks(cleanB)
	if errA != nil || errB != nil {
		return false
	}
	return pathsEqual(resolvedA, resolvedB)
}

func pathsEqual(a, b string) bool { return pathsEqualOn(runtime.GOOS, a, b) }

func pathsEqualOn(goos, a, b string) bool {
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func relativePath(root, file string) (string, bool) {
	rel, err := filepath.Rel(root, file)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	normalized, err := mutation.NormalizePath(rel)
	if err != nil {
		return "", false
	}
	return normalized, true
}
