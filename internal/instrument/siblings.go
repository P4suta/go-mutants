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

type packageNames struct {
	byPackage map[packageKey]map[string]bool
}

type packageKey struct {
	dir  string
	name string
}

func newPackageNames() *packageNames {
	return &packageNames{byPackage: make(map[packageKey]map[string]bool)}
}

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

func collectPackageBlock(srcPath string, src []byte, pkg string, names map[string]bool) {
	file, _, err := parseGo(srcPath, src)
	if err != nil {
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
