// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	// architectureDoc is the page this file holds to the tree.
	architectureDoc = "docs/architecture.md"
	// packageLayoutHeading opens the table that names every package.
	packageLayoutHeading = "## Package layout"
)

// architectureStatuses is the closed vocabulary the table's third column uses.
//
// Closing it is what stops a half-truth being written into a status column. The
// column has said "`run`, `list`" for a package with eight commands and
// "2 families" for one that implements eleven, and both read as facts rather
// than as the stale notes they were. A word that is not one of these four is a
// sentence somebody is about to have to re-check by hand.
var architectureStatuses = []string{
	"implemented",
	"test-only support",
	"developer tool",
	"vendored",
}

// archSkipDirectories are the directories the package scan does not enter.
//
// It is a local list rather than importgate_test.go's, which excludes
// `vendor-assets` -- a package that is a row of this table and has to stay one.
var archSkipDirectories = []string{".git", "fixtures", "testdata"}

// TestArchitectureDocNamesEveryPackage keeps the layout table equal to the
// tree, in both directions.
//
// A package the table does not name is a package a reader of the architecture
// does not know exists; a row naming a directory that holds no Go is a row
// describing something that has moved or gone.
func TestArchitectureDocNamesEveryPackage(t *testing.T) {
	t.Parallel()

	root := Root(t)
	packages := goPackages(t, root)
	if len(packages) < 25 {
		t.Fatalf("the scan found %d packages, which is too few to be this repository;"+
			" it has stopped seeing them", len(packages))
	}
	rows := architectureRows(t, root)
	if len(rows) == 0 {
		t.Fatalf("%s names no packages; the parser has stopped seeing the table", architectureDoc)
	}

	named := make([]string, 0, len(rows))
	for _, row := range rows {
		named = append(named, row[0])
	}
	for _, pkg := range packages {
		if !slices.Contains(named, pkg) {
			t.Errorf("%s does not name %s", architectureDoc, pkg)
		}
	}
	for _, pkg := range named {
		if !slices.Contains(packages, pkg) {
			t.Errorf("%s names %s, which holds no Go source in this tree", architectureDoc, pkg)
		}
	}
}

// TestEveryArchitecturePackageStatusIsOneOfTheFourWords closes the status
// column's vocabulary, in both directions.
func TestEveryArchitecturePackageStatusIsOneOfTheFourWords(t *testing.T) {
	t.Parallel()

	used := map[string]bool{}
	for _, row := range architectureRows(t, Root(t)) {
		if len(row) < 3 {
			t.Errorf("%s's row for %s has %d cells, want a path, a responsibility and a status",
				architectureDoc, row[0], len(row))
			continue
		}
		status := row[2]
		if !slices.Contains(architectureStatuses, status) {
			t.Errorf("%s says %s is %q, which is not one of %v;\n"+
				"\ta status column that can say anything is one that has said"+
				" \"`run`, `list`\" about a package with eight commands",
				architectureDoc, row[0], status, architectureStatuses)
			continue
		}
		used[status] = true
	}
	for _, status := range architectureStatuses {
		if !used[status] {
			t.Errorf("architectureStatuses allows %q, which no row uses;"+
				" delete it -- a vocabulary is only closed while every word in it is earned", status)
		}
	}
}

// architectureRows is the layout table, as `{path, responsibility, status}`,
// with the path taken from the first backticked token of the first cell.
//
// The first token rather than the whole cell, because the module root's row
// names the package as well as the path: “ `.` -- the module root
// (`gomutants`) “.
func architectureRows(t *testing.T, root string) [][]string {
	t.Helper()

	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(architectureDoc)))
	if err != nil {
		t.Fatalf("reading %s: %v", architectureDoc, err)
	}
	var rows [][]string
	inTable := false
	for _, line := range strings.Split(string(source), "\n") {
		if strings.HasPrefix(line, "## ") {
			inTable = strings.TrimRight(line, " ") == packageLayoutHeading
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !inTable || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := splitCells(strings.Trim(trimmed, "|"))
		if len(cells) == 0 || cells[0] == "Package" || strings.HasPrefix(cells[0], "-") {
			continue
		}
		path, ok := firstBackticked(cells[0])
		if !ok {
			t.Errorf("%s has a layout row whose first cell names no path: %q", architectureDoc, cells[0])
			continue
		}
		rows = append(rows, append([]string{path}, cells[1:]...))
	}
	return rows
}

// firstBackticked is the first `quoted` token of a cell.
func firstBackticked(cell string) (string, bool) {
	open := strings.Index(cell, "`")
	if open < 0 {
		return "", false
	}
	rest := cell[open+1:]
	end := strings.Index(rest, "`")
	if end <= 0 {
		return "", false
	}
	return rest[:end], true
}

// goPackages is every directory of this module that holds Go source a build
// compiles, module-relative and slash-separated, with the root as ".".
func goPackages(t *testing.T, root string) []string {
	t.Helper()

	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && slices.Contains(archSkipDirectories, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		found[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	packages := make([]string, 0, len(found))
	for pkg := range found {
		packages = append(packages, pkg)
	}
	slices.Sort(packages)
	return packages
}
