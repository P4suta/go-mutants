// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"cmp"
	"go/ast"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

type Completion struct {
	Path  string
	Local string
}

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

func defaultLocal(importPath string) string { return path.Base(importPath) }

func MergeCompletions(into, from []Completion) []Completion {
	for _, one := range from {
		if !slices.Contains(into, one) {
			into = append(into, one)
		}
	}
	slices.SortFunc(into, func(a, b Completion) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(a.Local, b.Local))
	})
	return into
}

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
