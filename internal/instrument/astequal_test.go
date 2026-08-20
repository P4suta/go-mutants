// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"testing"
)

// This file implements the correctness oracle for the flattener: two syntax
// trees are compared for structural equality modulo everything flattening is
// allowed to change, and nothing else.
//
// What is deliberately ignored, and why each is a change flattening is
// entitled to make:
//
//   - Positions. Every byte moves when a fragment is folded onto one line;
//     that is the entire point.
//   - Comments. Flatten drops the comments that cannot survive the fold, and
//     go/parser attaches the survivors to different nodes once the line breaks
//     are gone. Comment text carries no meaning to the compiler.
//   - ast.EmptyStmt.Implicit. This field records nothing but whether a
//     semicolon was written by the author or inserted by the scanner at a line
//     break — precisely the distinction Flatten converts, by design, and one
//     that no compiler behaviour depends on.
//   - The spelling of a string or rune literal, as opposed to its value. A raw
//     literal spanning lines is re-rendered as an interpreted literal, so the
//     comparison unquotes both sides and compares the values. This is the
//     stronger check of the two: it is what proves the rewrite preserved the
//     string rather than merely producing some literal.
//
// Everything else — node types, tree shape, operators, identifier names,
// numeric literal spelling, field presence and nil-ness — must match exactly.

// astDiff returns a description of the first structural difference between two
// syntax trees, or the empty string when they are structurally equal.
func astDiff(a, b ast.Node) string {
	return diffValue("", reflect.ValueOf(a), reflect.ValueOf(b))
}

// The types the walk stops at. ast.Object and ast.Scope are deprecated and
// named here for exactly that reason: go/parser still fills those fields in
// when object resolution is on, ast.Object.Decl points back up the tree, and a
// comparator that followed it would recurse forever. Naming the type is how
// the walk refuses to follow it.
var (
	posType          = reflect.TypeOf(token.NoPos)
	objectType       = reflect.TypeOf((*ast.Object)(nil)) //nolint:staticcheck // deprecated, still present in ast.Ident, and cyclic
	scopeType        = reflect.TypeOf((*ast.Scope)(nil))  //nolint:staticcheck // deprecated, still present in ast.File
	commentType      = reflect.TypeOf((*ast.Comment)(nil))
	commentsType     = reflect.TypeOf([]*ast.Comment(nil))
	commentGroupType = reflect.TypeOf((*ast.CommentGroup)(nil))
	commentGroupsTyp = reflect.TypeOf([]*ast.CommentGroup(nil))
	basicLitType     = reflect.TypeOf((*ast.BasicLit)(nil))
	emptyStmtType    = reflect.TypeOf(ast.EmptyStmt{})
)

func ignoredType(t reflect.Type) bool {
	switch t {
	case posType, objectType, scopeType, commentType, commentsType, commentGroupType, commentGroupsTyp:
		return true
	default:
		return false
	}
}

func diffValue(path string, a, b reflect.Value) string {
	if !a.IsValid() || !b.IsValid() {
		if a.IsValid() != b.IsValid() {
			return fmt.Sprintf("%s: one side is absent", path)
		}
		return ""
	}
	if a.Type() != b.Type() {
		return fmt.Sprintf("%s: type %s != %s", path, a.Type(), b.Type())
	}
	if ignoredType(a.Type()) {
		return ""
	}

	switch a.Kind() {
	case reflect.Pointer:
		if a.IsNil() != b.IsNil() {
			return fmt.Sprintf("%s: nil-ness differs (%v vs %v)", path, a.IsNil(), b.IsNil())
		}
		if a.IsNil() {
			return ""
		}
		if a.Type() == basicLitType {
			return basicLitDiff(path, a.Interface().(*ast.BasicLit), b.Interface().(*ast.BasicLit))
		}
		return diffValue(path, a.Elem(), b.Elem())

	case reflect.Interface:
		if a.IsNil() != b.IsNil() {
			return fmt.Sprintf("%s: nil-ness differs (%v vs %v)", path, a.IsNil(), b.IsNil())
		}
		if a.IsNil() {
			return ""
		}
		return diffValue(path, a.Elem(), b.Elem())

	case reflect.Slice:
		// A nil slice and an empty slice mean the same thing in a syntax tree.
		if a.Len() != b.Len() {
			return fmt.Sprintf("%s: length %d != %d", path, a.Len(), b.Len())
		}
		for i := range a.Len() {
			if d := diffValue(fmt.Sprintf("%s[%d]", path, i), a.Index(i), b.Index(i)); d != "" {
				return d
			}
		}
		return ""

	case reflect.Struct:
		t := a.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue // unexported; no ast node has one, but do not panic if that changes
			}
			if t == emptyStmtType && f.Name == "Implicit" {
				continue
			}
			if d := diffValue(path+"."+f.Name, a.Field(i), b.Field(i)); d != "" {
				return d
			}
		}
		return ""

	case reflect.Map:
		if a.Len() != b.Len() {
			return fmt.Sprintf("%s: map length %d != %d", path, a.Len(), b.Len())
		}
		return ""

	default:
		if a.Interface() != b.Interface() {
			return fmt.Sprintf("%s: %v != %v", path, a.Interface(), b.Interface())
		}
		return ""
	}
}

// basicLitDiff compares two literals by kind and by value, so that a raw
// string re-rendered as an interpreted string compares equal exactly when it
// still denotes the same string.
func basicLitDiff(path string, a, b *ast.BasicLit) string {
	if a.Kind != b.Kind {
		return fmt.Sprintf("%s: literal kind %s != %s", path, a.Kind, b.Kind)
	}
	switch a.Kind {
	case token.STRING, token.CHAR:
		av, aerr := strconv.Unquote(a.Value)
		bv, berr := strconv.Unquote(b.Value)
		if aerr != nil || berr != nil {
			// Unparsable literals are compared verbatim rather than silently
			// treated as equal.
			if a.Value != b.Value {
				return fmt.Sprintf("%s: unquotable literal %s != %s", path, a.Value, b.Value)
			}
			return ""
		}
		if av != bv {
			return fmt.Sprintf("%s: literal value %q != %q (%s vs %s)", path, av, bv, a.Value, b.Value)
		}
		return ""
	default:
		if a.Value != b.Value {
			return fmt.Sprintf("%s: literal %s != %s", path, a.Value, b.Value)
		}
		return ""
	}
}

// A fragmentKind records how a fragment had to be parsed, so that the original
// and the flattened copy are always parsed the same way. Comparing an
// expression tree against a statement tree would report a difference that is
// an artefact of the harness.
type fragmentKind int

const (
	kindExpr fragmentKind = iota
	kindStmts
)

func (k fragmentKind) String() string {
	if k == kindExpr {
		return "expression"
	}
	return "statement list"
}

// parseFragment parses src the way the instrumenter would have to: as an
// expression if it is one, and otherwise as the body of a function.
func parseFragment(src string, kind fragmentKind) (ast.Node, error) {
	if kind == kindExpr {
		return parser.ParseExpr(src)
	}
	const mode = parser.ParseComments | parser.SkipObjectResolution
	wrapped := "package p\nfunc _() {\n" + src + "\n}\n"
	file, err := parser.ParseFile(token.NewFileSet(), "fragment.go", wrapped, mode)
	if err != nil {
		return nil, err
	}
	// A fragment containing an unbalanced "}" would close the wrapper and turn
	// the rest of itself into further declarations, leaving this function
	// comparing two empty bodies and reporting every such pair as equal.
	if len(file.Decls) != 1 {
		return nil, fmt.Errorf("fragment does not stay inside the function body (%d declarations)", len(file.Decls))
	}
	fn, ok := file.Decls[0].(*ast.FuncDecl)
	if !ok {
		return nil, fmt.Errorf("fragment produced a %T rather than a function", file.Decls[0])
	}
	return fn.Body, nil
}

// classify reports how src parses, preferring the expression reading.
func classify(src string) (fragmentKind, error) {
	if _, err := parser.ParseExpr(src); err == nil {
		return kindExpr, nil
	}
	if _, err := parseFragment(src, kindStmts); err != nil {
		return kindStmts, err
	}
	return kindStmts, nil
}

// assertMeaningPreserved is the correctness property every flattener test
// funnels through: the flattened copy parses the same way the original did and
// yields a structurally identical tree.
func assertMeaningPreserved(t *testing.T, original string, flattened []byte) {
	t.Helper()

	kind, err := classify(original)
	if err != nil {
		t.Fatalf("input does not parse as an expression or a statement list: %v", err)
	}
	before, err := parseFragment(original, kind)
	if err != nil {
		t.Fatalf("parsing the original as a %s: %v", kind, err)
	}
	after, err := parseFragment(string(flattened), kind)
	if err != nil {
		t.Fatalf("flattened output does not parse as a %s: %v\ninput:     %q\nflattened: %q",
			kind, err, original, flattened)
	}
	if d := astDiff(before, after); d != "" {
		t.Fatalf("flattening changed the tree at %s\ninput:     %q\nflattened: %q", d, original, flattened)
	}
}

// TestASTDiffDetectsDifferences guards the oracle itself. A comparator that
// always returned "equal" would make every flattener test pass.
func TestASTDiffDetectsDifferences(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		a, b     string
		wantSame bool
	}{
		{name: "identical", a: "a + b", b: "a + b", wantSame: true},
		{name: "whitespace only", a: "a  +\tb", b: "a+b", wantSame: true},
		{name: "comment dropped", a: "a /* c */ + b", b: "a + b", wantSame: true},
		{name: "raw string re-rendered", a: "`a\nb`", b: `"a\nb"`, wantSame: true},
		{name: "different operator", a: "a + b", b: "a - b"},
		{name: "different identifier", a: "a + b", b: "a + c"},
		{name: "different literal value", a: `"x"`, b: `"y"`},
		{name: "raw string value differs", a: "`a\nb`", b: `"a\rb"`},
		{name: "different numeric spelling", a: "1", b: "1.0"},
		{name: "extra argument", a: "f(a)", b: "f(a, b)"},
		{name: "different nesting", a: "a + b*c", b: "(a + b)*c"},
		{name: "different node type", a: "a[b]", b: "a(b)"},
		{name: "nil-ness differs", a: "a[:]", b: "a[b:]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, err := parser.ParseExpr(tc.a)
			if err != nil {
				t.Fatalf("parsing %q: %v", tc.a, err)
			}
			b, err := parser.ParseExpr(tc.b)
			if err != nil {
				t.Fatalf("parsing %q: %v", tc.b, err)
			}
			d := astDiff(a, b)
			if tc.wantSame && d != "" {
				t.Fatalf("astDiff(%q, %q) = %q, want equal", tc.a, tc.b, d)
			}
			if !tc.wantSame && d == "" {
				t.Fatalf("astDiff(%q, %q) reported equal, want a difference", tc.a, tc.b)
			}
		})
	}
}

// TestASTDiffIgnoresImplicitSemicolons pins the one ignored field that is not
// a position or a comment.
func TestASTDiffIgnoresImplicitSemicolons(t *testing.T) {
	t.Parallel()

	explicit, err := parseFragment("a();;", kindStmts)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	block, ok := explicit.(*ast.BlockStmt)
	if !ok || len(block.List) != 2 {
		t.Fatalf("expected two statements, got %#v", explicit)
	}
	if _, ok := block.List[1].(*ast.EmptyStmt); !ok {
		t.Fatalf("expected an empty statement, got %T", block.List[1])
	}

	implicit := &ast.BlockStmt{List: []ast.Stmt{&ast.EmptyStmt{Implicit: true}}}
	other := &ast.BlockStmt{List: []ast.Stmt{&ast.EmptyStmt{Implicit: false}}}
	if d := astDiff(implicit, other); d != "" {
		t.Fatalf("astDiff on EmptyStmt.Implicit = %q, want it ignored", d)
	}
}
