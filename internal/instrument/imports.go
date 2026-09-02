// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"go/ast"
	"go/token"
	"path"
	"strconv"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// aliasBase is the local name every guard would like to spell the generated
// runtime package with.
//
// Two leading underscores are not a Go convention, and that is the point: the
// name has to be one no human wrote, because a name anything in the package
// already binds gets a bumped alias and a diff a reader has to reconcile. It
// stays a valid, exported-nothing identifier — a package alias is file-scoped
// and never escapes into the package's API.
const aliasBase = "__gm"

// aliasFor returns the name this file will import the runtime package under,
// given the names its package already binds.
//
// Two scopes are consulted, because an alias can be wrong in two different
// ways, and the quieter of the two is the one that reaches further than this
// file.
//
// Every identifier in the file is considered taken, not merely the file-scope
// declarations. A local variable named "__gm" inside one function would shadow
// a file-scoped import for exactly the statements a guard is most likely to sit
// in, and the resulting failure — "__gm.M undefined (type int has no field M)"
// — would name the generated runtime rather than the collision that caused it.
// Scanning every identifier costs one walk and rules the whole class out.
//
// Every identifier the package block binds is taken too, and that one is not
// shadowing at all: Go forbids the same name appearing in a file block and in
// the package block of the same package, so a `var __gm` in a sibling file
// makes `import __gm "…"` here a hard error — "__gm already declared through
// import of package …" — no matter that the two are in different files. Those
// names arrive through reserved, gathered from the directory by [packageNames],
// because a file cannot see them by looking at itself.
//
// The implicit local name of an unaliased import counts too, since no
// identifier node spells it: `import "example.com/__gm"` binds "__gm" as surely
// as an explicit alias does.
func aliasFor(file *ast.File, reserved map[string]bool) string {
	return aliasIn(takenNames(file, reserved))
}

// takenNames is the set of identifiers a rewrite of this file may not bind: the
// two scopes [aliasFor] documents, gathered once.
//
// It is a set rather than an answer because the alias is not the only name a
// rewrite invents. The probe form declares a temporary per result of a `return`
// and has to dodge exactly the same identifiers for exactly the same reasons —
// a temporary shadowing something the file spells would change what an operand
// reads — so the set is computed once per file and both choices are made
// against it.
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

// aliasIn picks the runtime import alias against a set of taken names.
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

// importSplices returns the edits that make the runtime package reachable from
// one file, under the local name alias.
//
// All three forms are insertions rather than replacements, and that is a
// deliberate constraint rather than an accident of the shapes involved. An
// insertion of text holding no line break is line-preserving whatever the file
// around it looks like, while replacing a declaration would have to prove that
// the bytes replaced hold no line break either — and `import` followed by a
// newline and then the path is legal Go, so that proof would fail on source a
// user is entitled to write.
//
// The three shapes an import section can take:
//
//   - A parenthesized list gets the new import inserted just inside the "(", so
//     it lands on the same line as the "import (" the file already had.
//   - A single unparenthesized import is parenthesized in place, by inserting
//     "(" before the existing spec and "; alias path)" after it. The spec's own
//     bytes — alias, blank import, comment position — are never touched.
//   - A file with no imports at all gets one appended to its package clause,
//     after an explicit semicolon. A package clause is a single line by
//     construction, so this too preserves the file's line numbering.
//
// Only the first import declaration is considered. A file may hold several, and
// picking the first one keeps the choice a function of the file rather than of
// anything this package remembers between runs.
func importSplices(file *ast.File, tok *token.File, srcPath, alias, importPath string) ([]Splice, error) {
	spec := strconv.Quote(importPath)
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
		return []Splice{insertion(offset(file.Name.End()), "; import "+alias+" "+spec)}, nil
	}

	if decl.Lparen.IsValid() {
		return []Splice{insertion(offset(decl.Lparen)+1, alias+" "+spec+";")}, nil
	}

	if len(decl.Specs) != 1 {
		// Unreachable: only a parenthesized declaration can hold anything other
		// than exactly one spec.
		return nil, &Error{
			Code: CodeImportInjection,
			Message: "internal error: " + strconv.Quote(srcPath) + " has an unparenthesized import declaration with " +
				strconv.Itoa(len(decl.Specs)) + " specs",
		}
	}
	only := decl.Specs[0]
	return []Splice{
		insertion(offset(only.Pos()), "("),
		insertion(offset(only.End()), "; "+alias+" "+spec+")"),
	}, nil
}

// firstImportDecl returns the file's first import declaration, or nil when it
// has none.
func firstImportDecl(file *ast.File) *ast.GenDecl {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if ok && gen.Tok == token.IMPORT {
			return gen
		}
	}
	return nil
}
