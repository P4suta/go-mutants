// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
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

// loadMode is what discovery needs from the loader and nothing more.
//
// NeedSyntax and NeedTypesInfo are the two that cost real time, and both are
// unavoidable: candidates are byte spans taken from a parsed file, and telling
// the universe's `true` from a local variable called `true`, or a type
// argument from a map index, is a question only the type checker can answer.
//
// NeedDeps is deliberately absent, and its absence is worth more than any bit
// present here. Asking for it makes go/packages parse and type-check the whole
// transitive dependency closure, the standard library included, from source;
// without it the loader type-checks the root packages against the compiler's
// export data instead. Discovery only ever walks root packages — nothing here
// reads pkg.Imports or any field of a dependency — so the two produce the same
// [types.Info] for the files that matter, and differ only in what they charge
// for it: one file per dependency's export data against every file in the
// closure. Even the fixture module, whose only import is "embed", loads in
// roughly half the time; a tree with a real dependency graph is where that
// becomes minutes and a large heap, on the critical path of every run.
const loadMode = packages.NeedName |
	packages.NeedFiles |
	packages.NeedCompiledGoFiles |
	packages.NeedSyntax |
	packages.NeedTypes |
	packages.NeedTypesInfo |
	packages.NeedModule

// errorSample is how many package errors a [CodePackageErrors] message quotes.
// A tree that does not compile usually does not compile in one place, and the
// first few errors are the ones worth acting on; the rest are noise the user
// will see again from `go build` anyway.
const errorSample = 3

// A loadResult is the loader's output in a deterministic order.
type loadResult struct {
	// fset is the file set every position in every syntax tree refers to.
	fset *token.FileSet
	// packages are the root packages, sorted by package path and then by ID so
	// that a package and its test variants always appear in the same order.
	packages []*packages.Package
}

// load runs the package loader over the whole snapshot.
func load(ctx context.Context, root string, toolchain gocmd.Toolchain) (*loadResult, error) {
	fset := token.NewFileSet()
	cfg := &packages.Config{
		Context: ctx,
		Mode:    loadMode,
		Dir:     root,
		Env:     environment(toolchain),
		Fset:    fset,
		// Test files are loaded and type-checked but never mutated. They are
		// here because a tree whose tests do not compile is not a tree that can
		// be mutation tested, and because an external test package is the only
		// place some packages are used at all.
		Tests: true,
	}
	loaded, err := packages.Load(cfg, "./...")
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
		if c := strings.Compare(x.PkgPath, y.PkgPath); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	return &loadResult{fset: fset, packages: loaded}, nil
}

// toolchainHint names the toolchain that was located, when one was, so that a
// failed load can be compared against it at a glance.
func toolchainHint(toolchain gocmd.Toolchain) string {
	if toolchain.GoBin == "" {
		return ""
	}
	return " (the located toolchain is " + toolchain.GoBin + ")"
}

// environment is the child environment for the loader: this process's, with
// workspace mode switched off and the located toolchain's directory in front
// of PATH.
//
// GOWORK=off is what turns [Discover]'s refusal of a snapshot-root `go.work`
// into a guarantee. That refusal only sees a workspace file sitting at the root
// itself, while the go command also searches every parent directory and obeys
// $GOWORK — so a snapshot one level below somebody's workspace would otherwise
// resolve its dependencies through a file the snapshot does not contain, and
// every digest, cache key, and identity this phase mints assumes the snapshot
// is the whole truth. Pinning it also means one snapshot discovers the same way
// whatever environment the run was started from.
//
// Prepending the toolchain directory matters even though it does not decide
// which `go` binary runs — os/exec resolved that from this process's PATH
// before the environment was ever consulted. What it decides is what that
// binary sees: a `go` that finds a different `go` ahead of it on PATH can hand
// work to it, and the toolchain line in a go.mod is resolved the same way.
func environment(toolchain gocmd.Toolchain) []string {
	env := setEnv(os.Environ(), "GOWORK", "off")
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

// setEnv sets one variable in a "KEY=VALUE" environment.
//
// Every entry naming the variable is replaced rather than a second one
// appended: os/exec resolves a duplicate by keeping the last, so appending
// would work, and an environment whose meaning depends on knowing that rule is
// one a maintainer reads wrong.
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

// sameEnvKey compares two environment variable names the way the operating
// system does: case-insensitively on Windows, where a variable answers to any
// spelling of its name — PATH is written "Path" as often as "PATH" — and
// exactly everywhere else.
func sameEnvKey(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// mainModule returns the module rooted at the snapshot root.
//
// Insisting that the main module's directory *is* the snapshot root is not
// pedantry: every identity go-mutants mints is module-relative, and the
// snapshot manifest is rooted at the snapshot. If the two disagreed, a
// candidate's path would name a file the snapshot does not contain.
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

// gate refuses a tree that does not compile.
//
// cgo packages are exempt, and only they: they are excluded from mutation
// wholesale, so whether their C preprocessing step succeeded is not a question
// discovery has to have an answer to. Anything importing one is not exempt,
// because that failure is a real gap in the type information discovery reads.
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

// formatPackageError renders one loader error as "package: pos: message", with
// whichever of the three parts the loader actually filled in.
//
// The result is always a single line; see [collapseLines] for why that is this
// function's responsibility rather than the caller's.
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

// collapseLines folds a loader message onto one line.
//
// [packages.Error.Msg] is whatever the loader was handed, and for a `go list`
// failure that is the command's whole standard error: a `# import/path` banner
// followed by one line per compiler diagnostic. Every diagnostic go-mutants
// prints is one "error GOM####: ..." line, so a message carrying newlines would
// put lines into the report that no `grep '^error '` and no CI log parser can
// attribute to anything — the code sits on the first line only. Collapsing here
// keeps that promise at the source, where the multi-line text is produced,
// instead of leaving every renderer downstream to discover it.
//
// The parts are joined with "; ", which is the separator [gate] already puts
// between one package error and the next, so a folded message reads the same way
// as the list it is embedded in. Empty lines are dropped rather than becoming
// empty parts.
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

// plural renders a count with its noun, pluralised the only way English needs
// here.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// A fileRef is one module source file: where it is on disk, and what it is
// called in an identity.
type fileRef struct {
	abs string
	rel string
}

// moduleFiles returns every Go source file a package owns that discovery could
// mutate: under the module root, not a test file, deduplicated and sorted.
//
// Both the package's Go files and the files excluded by build constraints are
// listed, because which of the two a cgo file lands in is decided by
// CGO_ENABLED rather than by anything about the file. That matters for exactly
// one caller, [findCgoPackages], and it is the reason a cgo package can be
// recognised — and skipped, with its files named — on a machine with no C
// compiler at all.
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

// A cgoExemption is the set of packages that import "C", by loader ID and by
// import path, together with the test variants the go command derives from
// them.
type cgoExemption struct {
	// ids holds the loader IDs of the packages a cgo import was found in.
	ids map[string]bool
	// bases holds their import paths, with any test-variant decoration
	// removed.
	bases map[string]bool
}

// covers reports whether a package is excluded from mutation, and therefore
// exempt from the load gate.
//
// The test variants have to be named explicitly, because the file scan cannot
// find them: an external test package owns nothing but `_test.go` files, and
// the generated test main package owns a file in the build cache, so neither
// holds the cgo import that identifies the package they belong to. Their
// failure is the same failure — "could not import the cgo package next door" —
// and reporting it would be reporting the exempt package's build error under a
// different name.
//
// A package genuinely named `x_test` sitting beside a cgo package `x` would be
// exempted too. That costs a diagnostic in a directory layout nobody uses; the
// alternative, matching on the loader's ID decoration, is a private detail of
// go/packages that would change under us.
func (e cgoExemption) covers(pkg *packages.Package) bool {
	if e.ids[pkg.ID] {
		return true
	}
	base := packagePath(pkg)
	switch {
	case e.bases[base]:
		return true
	case strings.HasSuffix(base, "_test") && e.bases[strings.TrimSuffix(base, "_test")]:
		return true
	case strings.HasSuffix(base, ".test") && e.bases[strings.TrimSuffix(base, ".test")]:
		return true
	}
	return false
}

// findCgoPackages finds the packages that import "C".
//
// The question is asked of the source rather than of the loader's package
// graph on purpose. With cgo enabled the import is rewritten away before the
// loader ever produces syntax, and with cgo disabled the file is not part of
// the package at all — in both cases the only place the truth survives intact
// is the file on disk.
func findCgoPackages(loaded *loadResult, root string) cgoExemption {
	exemption := cgoExemption{ids: make(map[string]bool), bases: make(map[string]bool)}
	fset := token.NewFileSet()
	// A package and its test variants own the same files, so the answer is
	// remembered per file rather than recomputed per package.
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
			exemption.ids[pkg.ID] = true
			if base := packagePath(pkg); base != "" {
				exemption.bases[base] = true
			}
			break
		}
	}
	return exemption
}

// importsC reports whether one file imports "C".
//
// A file that does not parse is reported as not importing it: the gate is
// about to fail on the parse error anyway, and guessing that an unparsable
// file is cgo would exempt it from exactly the check that would have explained
// the problem.
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

// isTestFile reports whether a path names a Go test file, which is built and
// run but never mutated.
func isTestFile(path string) bool {
	return strings.HasSuffix(filepath.Base(path), "_test.go")
}

// samePath reports whether two paths name the same directory.
//
// Cleaning is not enough on its own: a temporary directory is behind a symlink
// on macOS and behind a short name on Windows, and the go command may report
// either spelling. Resolving both is the only comparison that survives that,
// and it falls back to the cleaned comparison when resolution fails, which is
// what happens for a path that no longer exists.
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

// pathsEqual compares two paths the way the platform's file system does.
func pathsEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// relativePath converts an absolute file path into the '/'-normalized
// module-relative form identities use, reporting false for anything outside
// the module root.
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
