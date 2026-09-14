// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Import completion is what turns "this file has no name for that package" from
// a refusal into an edit.
//
// Every guard form but Form C has to write a type down, and a type is written
// with the name its package is bound to *in the file being rewritten*. A file
// can perfectly well hold an expression whose type belongs to a package it does
// not import — a helper in a sibling file returns one — and until this existed
// that was a [SkipUnnameableDeclType]: go-mutants knew exactly what it wanted
// to write and could not say it in Go.
//
// # The rule, and why it is the safe one
//
// A completion may only add a path **some file of the same package already
// imports**. That single restriction settles every question an import injector
// normally has to answer, and settles them by construction rather than by
// analysis:
//
//   - No cycle is possible. The package compiles today with that edge in its
//     import graph, and moving the edge from one of its files to another does
//     not change the graph.
//   - Visibility is unchanged. `internal/` boundaries, module boundaries and
//     vendoring all judge the *importing package*, which is the same package.
//   - go.mod needs nothing. The requirement that makes the path resolvable is
//     already there, for the sibling.
//
// It also rescues the two import forms a file can hold that bind no usable
// name: a blank import imports the package and binds nothing, and a dot import
// binds its contents rather than the package. Both are edges the package
// already has, so both are completable — which is why [indexImports]
// deliberately skips them and this deliberately does not.
//
// # What it does not fix
//
// Reach, not spelling. A type whose package no file of this one imports stays a
// refusal, and so does a type naming something unexported in another package:
// an import makes a package *nameable*, never its unexported names. That is why
// [SkipUnnameableDeclType] still has work to do, and why the fixture in
// testdata/mainmod/split holds one of each side by side.

// A Completion is an import a file must gain for a rewrite's spelling to
// compile: the path, and the local name that spelling binds it to.
//
// The name is decided at discovery rather than left to the rewriter, because
// the rendered type string already contains it. A rewriter that picked its own
// name would have to re-render every type beside it, which is the second
// spelling engine guard.go exists to avoid.
type Completion struct {
	// Path is the import path, exactly as the sibling file spells it.
	Path string
	// Local is the name the rewritten file binds it to. It is never empty and
	// never the blank or dot forms: a completion exists to make a name
	// available, so a completion with no name would be nothing at all.
	Local string
}

// importsOf indexes the paths a package's files import, with the name to prefer
// for each.
//
// First usable spelling wins, in file order, so the answer is the package's own
// source rather than a map iteration. An explicit alias is preferred to none,
// because a package that renames an import has a reason and a completion that
// ignored it would put two names for one package in front of a reader.
//
// A blank or dot import contributes the path with no name, which the caller
// resolves to the package's own name. See the package-level note above for why
// those count.
func importsOf(files []*ast.File) map[string]string {
	ordered := slices.Clone(files)
	slices.SortStableFunc(ordered, func(a, b *ast.File) int {
		return int(a.Pos() - b.Pos())
	})
	found := map[string]string{}
	for _, file := range ordered {
		for _, spec := range file.Imports {
			if spec.Path == nil {
				continue
			}
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil || importPath == "" {
				continue
			}
			local := ""
			if spec.Name != nil && spec.Name.Name != "_" && spec.Name.Name != "." {
				local = spec.Name.Name
			}
			previous, seen := found[importPath]
			if !seen || (previous == "" && local != "") {
				found[importPath] = local
			}
		}
	}
	return found
}

// defaultLocal is the name an import with no alias binds, which is the last
// element of its path rather than the package's declared name.
//
// The two differ often enough to matter — `gopkg.in/yaml.v3` declares `yaml`,
// `google.golang.org/grpc` declares `grpc` — and only the declared name is
// correct. So this is a fallback for the case where the checker has no package
// to ask, and every real completion asks the package itself.
func defaultLocal(importPath string) string { return path.Base(importPath) }

// MergeCompletions unions two completion lists, keeping them sorted by path and
// free of repeats.
//
// It is exported for internal/instrument, which unions the completions of every
// site it writes into one import declaration. One implementation rather than
// two: the ordering is what makes an instrumented file a function of its
// mutants, and a second union with its own idea of order would make the bytes
// depend on which phase did the merging.
//
// A union rather than a concatenation, because the result becomes import
// declarations: a package named twice would be a package declared twice, which
// is a compile error rather than a redundancy. Sorting makes the list a
// function of what a rewrite needs rather than of the order the type checker
// happened to ask about packages, which is what keeps two runs over one
// workspace producing identical bytes.
func MergeCompletions(into, from []Completion) []Completion {
	for _, one := range from {
		if !slices.Contains(into, one) {
			into = append(into, one)
		}
	}
	slices.SortFunc(into, func(a, b Completion) int {
		if a.Path != b.Path {
			return strings.Compare(a.Path, b.Path)
		}
		return strings.Compare(a.Local, b.Local)
	})
	return into
}

// packageImports is the import index of a whole package, computed once.
//
// Every file of the package counts, the one being walked included, and that is
// deliberate rather than sloppy. The resolver consults the walked file's own
// usable imports first, so a path it already has a name for is never completed
// from here; what is left of its own imports are the two forms that bind no
// name — a blank import and a dot import — and those are edges the package
// genuinely has and genuinely cannot spell. Completing them is the point.
//
// Test files are left out. An import only a `_test.go` file carries is not one
// the package compiles with in the build under test, and the instrumented tree
// is a non-test build.
func (d *discovery) packageImports(loaded *loadResult, pkg *packages.Package) map[string]string {
	if d.siblings == nil {
		d.siblings = map[string]map[string]string{}
	}
	key := packagePath(pkg)
	if cached, ok := d.siblings[key]; ok {
		return cached
	}
	files := make([]*ast.File, 0, len(pkg.Syntax))
	for _, file := range pkg.Syntax {
		tokFile := loaded.fset.File(file.Package)
		if tokFile == nil || isTestFile(tokFile.Name()) {
			continue
		}
		files = append(files, file)
	}
	index := importsOf(files)
	d.siblings[key] = index
	return index
}
