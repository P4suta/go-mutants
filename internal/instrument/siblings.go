// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"errors"
	"go/ast"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// packageNames answers "what does this package's package block already bind?"
// for the packages one instrumentation pass writes into.
//
// The question cannot be answered from the file being instrumented, which is
// the whole reason this type exists. An import alias lives in a file block, and
// Go forbids one name being bound in both a file block and the package block of
// the same package — not as shadowing but as a redeclaration the compiler
// rejects. So a `var __gm` three files away decides what this file may call the
// generated runtime, and finding out costs a directory read.
//
// Answers are cached per package rather than per file, because a package with
// forty catalogued files would otherwise read and parse its own directory forty
// times. Nothing invalidates the cache: instrumentation only ever injects
// file-scoped import aliases, which are not package-block names, so rewriting
// one file cannot change the answer for its siblings.
type packageNames struct {
	byPackage map[packageKey]map[string]bool
}

// packageKey names one package block: the directory holding it and the name its
// files declare.
//
// The directory alone would not do. A directory holds up to two packages — foo
// and its external test package foo_test — and they are separate scopes, so a
// name bound in one cannot collide with an import alias in the other.
type packageKey struct {
	dir  string
	name string
}

// newPackageNames returns an empty cache, good for one instrumentation pass.
func newPackageNames() *packageNames {
	return &packageNames{byPackage: make(map[packageKey]map[string]bool)}
}

// namesIn returns every identifier bound in the package block of the package
// called name in dir. The returned map belongs to the cache and must not be
// written to.
func (p *packageNames) namesIn(dir, name string) (map[string]bool, error) {
	key := packageKey{dir: dir, name: name}
	if names, ok := p.byPackage[key]; ok {
		return names, nil
	}
	names, err := readPackageBlock(dir, name)
	if err != nil {
		return nil, err
	}
	p.byPackage[key] = names
	return names, nil
}

// readPackageBlock reads every Go file in dir and returns the identifiers that
// the package block of the package called name binds.
//
// Every .go file in the directory is read, the file being instrumented
// included: its own names are package-block names of the same package, and
// leaving them to the caller's syntax walk would mean two rules where one does.
// Files belonging to another package are dropped by [collectPackageBlock] on
// the package clause, which is what keeps an external `foo_test` package — a
// different scope that cannot collide — from bumping an alias for nothing.
//
// A file the walk cannot read is an error rather than a shrug. The alternative
// is a run that instruments a file with an alias that will not compile, and
// nothing downstream can attribute that build failure back to a sibling this
// pass declined to open.
func readPackageBlock(dir, name string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, &Error{
			Code: CodeSourceUnreadable,
			Message: "cannot read the directory " + strconv.Quote(dir) +
				" to see which names the package in it already binds",
			Err: err,
		}
	}

	names := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file := filepath.Join(dir, entry.Name())
		src, err := os.ReadFile(file)
		if errors.Is(err, fs.ErrNotExist) {
			// The entry was listed and is gone. Nothing in a snapshot removes
			// files, so this is somebody else's tooling looking at the copy;
			// a file that no longer exists binds nothing.
			continue
		}
		if err != nil {
			return nil, &Error{
				Code: CodeSourceUnreadable,
				Message: "cannot read " + strconv.Quote(file) +
					" to see which names the package in it already binds",
				Err: err,
			}
		}
		collectPackageBlock(file, src, name, names)
	}
	return names, nil
}

// collectPackageBlock adds to names every identifier one file binds in the
// package block of the package called pkg.
//
// The package clause decides membership, and only files that declare pkg are
// counted: an external `foo_test` test package is a scope of its own, and a
// `//go:build ignore` generator declaring `package main` is not in the build at
// all. A same-package `_test.go` file does count — it is compiled into the test
// binary alongside everything else, where a clashing alias would fail exactly
// as it would anywhere.
//
// What the package block binds is the top-level const, var, type and func
// names. A method is bound to its receiver's type rather than to the package,
// so `func (t T) __gm()` is not a collision and is not counted as one.
func collectPackageBlock(srcPath string, src []byte, pkg string, names map[string]bool) {
	file, _, err := parseGo(srcPath, src)
	if err != nil {
		// A file this parser cannot read may still be one the compiler accepts
		// — source written against a newer Go syntax than the toolchain that
		// built go-mutants, most plausibly — and aborting a run over a file
		// nobody asked to instrument would trade a rare wrong alias for a
		// frequent wrong abort. Every identifier token is taken instead. That
		// is a superset of the package block, so the alias can only come out
		// more cautious than it had to be, and never less.
		collectIdents(src, names)
		return
	}
	if file.Name == nil || file.Name.Name != pkg {
		return
	}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name != nil {
				names[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, ident := range s.Names {
						names[ident.Name] = true
					}
				case *ast.TypeSpec:
					if s.Name != nil {
						names[s.Name.Name] = true
					}
				}
			}
		}
	}
}

// collectIdents adds every identifier token in src to names, and is the
// fallback [collectPackageBlock] takes for a file that does not parse.
//
// Scanner errors are ignored on purpose: this path is reached because the file
// is already known not to parse, and the scanner keeps going past what it
// cannot read, which is exactly what gathering a superset wants it to do.
func collectIdents(src []byte, names map[string]bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))

	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return
		}
		if tok == token.IDENT {
			names[lit] = true
		}
	}
}
