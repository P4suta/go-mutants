// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"go/ast"
	"go/token"
	"path"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
)

const aliasBase = "__gm"

func aliasFor(file *ast.File, reserved map[string]bool) string {
	return aliasIn(takenNames(file, reserved))
}

func takenNames(file *ast.File, reserved map[string]bool) map[string]bool {
	taken := make(map[string]bool, len(reserved))
	for name := range reserved {
		taken[name] = true
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok {
			taken[ident.Name] = true
		}
		return true
	})
	for _, spec := range file.Imports {
		if spec.Name != nil || spec.Path == nil {
			continue
		}
		if unquoted, err := strconv.Unquote(spec.Path.Value); err == nil {
			taken[path.Base(unquoted)] = true
		}
	}
	return taken
}

func aliasIn(taken map[string]bool) string {
	if !taken[aliasBase] {
		return aliasBase
	}
	for n := 1; ; n++ {
		candidate := aliasBase + strconv.Itoa(n)
		if !taken[candidate] {
			return candidate
		}
	}
}

func importSplices(
	file *ast.File, tok *token.File, srcPath, alias, importPath string, completions []discover.Completion,
) ([]Splice, error) {
	specs, err := importSpecs(srcPath, alias, importPath, completions)
	if err != nil {
		return nil, err
	}
	offset := func(pos token.Pos) uint32 { return uint32(tok.Offset(pos)) }
	insertion := func(at uint32, text string) Splice {
		return Splice{
			Span:        mutation.Span{StartByte: at, EndByte: at},
			Original:    []byte{},
			Replacement: []byte(text),
		}
	}

	decl := firstImportDecl(file)
	if decl == nil {
		if file.Name == nil {
			return nil, &Error{
				Code:    CodeImportInjection,
				Message: "internal error: " + strconv.Quote(srcPath) + " parsed without a package clause to import from",
			}
		}
		if len(specs) == 1 {
			return []Splice{insertion(offset(file.Name.End()), "; import "+specs[0])}, nil
		}
		return []Splice{insertion(offset(file.Name.End()), "; import ("+strings.Join(specs, "; ")+")")}, nil
	}

	if decl.Lparen.IsValid() {
		return []Splice{insertion(offset(decl.Lparen)+1, strings.Join(specs, ";")+";")}, nil
	}

	if len(decl.Specs) != 1 {
		return nil, &Error{
			Code: CodeImportInjection,
			Message: "internal error: " + strconv.Quote(srcPath) + " has an unparenthesized import declaration with " +
				strconv.Itoa(len(decl.Specs)) + " specs",
		}
	}
	only := decl.Specs[0]
	return []Splice{
		insertion(offset(only.Pos()), "("),
		insertion(offset(only.End()), "; "+strings.Join(specs, "; ")+")"),
	}, nil
}

func importSpecs(srcPath, alias, importPath string, completions []discover.Completion) ([]string, error) {
	specs := []string{alias + " " + strconv.Quote(importPath)}
	bound := map[string]string{alias: importPath}
	for _, completion := range completions {
		if completion.Local == "" || completion.Path == "" {
			return nil, &Error{
				Code: CodeImportInjection,
				Message: "internal error: instrumenting " + strconv.Quote(srcPath) +
					" was given a completion with no " + either(completion.Local == "", "name", "path"),
			}
		}
		if taken, clash := bound[completion.Local]; clash {
			return nil, &Error{
				Code: CodeImportInjection,
				Message: "internal error: instrumenting " + strconv.Quote(srcPath) + " would bind " +
					strconv.Quote(completion.Local) + " to both " + strconv.Quote(taken) + " and " +
					strconv.Quote(completion.Path),
			}
		}
		bound[completion.Local] = completion.Path
		specs = append(specs, completion.Local+" "+strconv.Quote(completion.Path))
	}
	return specs, nil
}

func either(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

func firstImportDecl(file *ast.File) *ast.GenDecl {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if ok && gen.Tok == token.IMPORT {
			return gen
		}
	}
	return nil
}
