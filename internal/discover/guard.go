// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// A guardResolver answers, for one file, the question [Guard] documents: which
// rewrite shape does the instrumenter need for an edit at this node, and over
// which bytes.
//
// It is built once per file and queried once per candidate. The parent links it
// holds are the whole reason it exists: go/ast offers no way from a node to its
// enclosing one, and every decision here is about what a node sits inside.
type guardResolver struct {
	info    *types.Info
	pkg     *types.Package
	tokFile *token.File
	// parent maps a node to the node that owns it. The file itself is absent,
	// which is how every walk outward terminates.
	parent map[ast.Node]ast.Node
	// imports maps an import path to the name this file may spell it with. An
	// empty value means the import is plain and the name is the package's own,
	// which is only knowable from the [types.Package] at qualification time.
	imports map[string]string
}

// newGuardResolver indexes one file.
func newGuardResolver(file *ast.File, info *types.Info, pkg *types.Package, tokFile *token.File) *guardResolver {
	g := &guardResolver{
		info:    info,
		pkg:     pkg,
		tokFile: tokFile,
		parent:  make(map[ast.Node]ast.Node),
		imports: make(map[string]string),
	}
	g.indexParents(file)
	g.indexImports(file)
	return g
}

// indexParents records every node's owner in one walk.
//
// The callback always returns true and the stack is popped on the nil visit
// that closes each node, which is the only shape that stays balanced:
// [ast.Inspect] skips the closing visit for a node whose callback returned
// false, so a walk that pruned anywhere would leave the stack short and give
// every node after it the wrong parent.
func (g *guardResolver) indexParents(file *ast.File) {
	var stack []ast.Node
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) > 0 {
			g.parent[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
}

// indexImports records the local name each imported path has in this file.
//
// Three import forms supply no name a type can be written with: a blank import
// binds nothing, a dot import binds the package's contents rather than the
// package, and there is no third — those two are simply skipped, and a type
// from such a package is unnameable here even though the file does import it.
// When one path is imported more than once the first usable spelling wins, so
// that the answer is the file's own source order and not a map iteration.
func (g *guardResolver) indexImports(file *ast.File) {
	for _, spec := range file.Imports {
		if spec.Path == nil {
			continue
		}
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path == "" {
			continue
		}
		local := ""
		if spec.Name != nil {
			local = spec.Name.Name
			if local == "_" || local == "." {
				continue
			}
		}
		if _, seen := g.imports[path]; !seen {
			g.imports[path] = local
		}
	}
}

// span is the byte range of a node in the file being indexed.
func (g *guardResolver) span(node ast.Node) (mutation.Span, bool) {
	start := g.tokFile.Offset(node.Pos())
	end := g.tokFile.Offset(node.End())
	if start < 0 || end < start {
		return mutation.Span{}, false
	}
	span, err := mutation.NewSpan(uint32(start), uint32(end))
	if err != nil {
		return mutation.Span{}, false
	}
	return span, true
}

// guardFor computes the rewrite site for an edit anchored at one node,
// reporting false when none of the three forms can express it. Every false is
// a [SkipUnnameableDeclType] skip; see [Guard] for the full list of them.
func (g *guardResolver) guardFor(anchor ast.Node) (Guard, bool) {
	if site, ok := g.formCSite(anchor); ok {
		return site, true
	}
	return g.statementSite(anchor)
}

// formCSite looks outward for the nearest bool-valued expression that may be
// wrapped in a selector.
//
// The search stops at the first ancestor that is not an expression, which is
// what keeps it inside one function: the body of a function literal is a
// statement, so an edit inside `(func() bool { return a > b })()` can never
// select the call around the literal as its site.
func (g *guardResolver) formCSite(anchor ast.Node) (Guard, bool) {
	for node := anchor; node != nil; node = g.parent[node] {
		expr, ok := node.(ast.Expr)
		if !ok {
			return Guard{}, false
		}
		if !g.wrappableBool(expr) {
			continue
		}
		span, ok := g.span(expr)
		if !ok {
			return Guard{}, false
		}
		return Guard{Form: GuardFormC, SiteSpan: span}, true
	}
	return Guard{}, false
}

// wrappableBool reports whether an expression is exactly the universe bool and
// sits where `(…)` around it is still legal Go.
func (g *guardResolver) wrappableBool(expr ast.Expr) bool {
	if g.info == nil {
		return false
	}
	tv, ok := g.info.Types[expr]
	if !ok || !tv.IsValue() || !isUniverseBool(tv.Type) {
		return false
	}
	return g.wrappablePosition(expr)
}

// wrappablePosition reports whether an expression may be replaced by a
// parenthesized expression of the same type.
//
// Every answer is decided by the parent, and the refusals come in two kinds.
//
// The first is a position that needs more of the expression than its type: an
// assignment target and the operand of `++`/`--` have to be addressable, `&x`
// has to be addressable, and the field name in `x.ok` is not an expression at
// all — it is the name of a field, and a selector over a guard would select
// from a boolean. A composite literal key is refused unless the literal is a
// map, because a struct's key is a field name for the same reason.
//
// The second is a position that holds no value at all. A bool-valued call is a
// perfectly ordinary expression, and written as a statement — on its own, after
// `defer`, or after `go` — it is a statement that happens to be a call, not a
// value the program uses. Form C renders `(… && (…) || … && (ORIG))`, so a
// guard there would be a bool that is not used and an operand of `defer` and
// `go` that is not a call, and none of the three compiles. Refusing here lets
// the search fall through to [statementSite], which classifies all three as
// Form S — the hint the instrumenter can actually rewrite.
func (g *guardResolver) wrappablePosition(expr ast.Expr) bool {
	switch parent := g.parent[expr].(type) {
	case *ast.ExprStmt, *ast.DeferStmt, *ast.GoStmt:
		return false
	case *ast.SelectorExpr:
		return parent.Sel != expr
	case *ast.UnaryExpr:
		return parent.Op != token.AND
	case *ast.IncDecStmt:
		return parent.X != expr
	case *ast.AssignStmt:
		for _, lhs := range parent.Lhs {
			if lhs == expr {
				return false
			}
		}
		return true
	case *ast.RangeStmt:
		return parent.Key != expr && parent.Value != expr
	case *ast.KeyValueExpr:
		if parent.Key != expr {
			return true
		}
		literal, ok := g.parent[parent].(*ast.CompositeLit)
		if !ok || g.info == nil {
			return false
		}
		tv, known := g.info.Types[literal]
		if !known || tv.Type == nil {
			return false
		}
		_, isMap := tv.Type.Underlying().(*types.Map)
		return isMap
	default:
		return true
	}
}

// statementSite looks outward for the nearest statement and decides which
// statement form, if any, covers it.
//
// The first statement found decides: a candidate whose nearest statement is a
// `switch` tag is not covered by wrapping some statement further out, it is a
// site v1 does not rewrite. The search stops at the enclosing function for the
// same reason [formCSite] does.
func (g *guardResolver) statementSite(anchor ast.Node) (Guard, bool) {
	for node := anchor; node != nil; node = g.parent[node] {
		switch n := node.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			return Guard{}, false
		case ast.Stmt:
			if !g.blockIsLegalFor(n) {
				return Guard{}, false
			}
			return g.statementGuard(n)
		}
	}
	return Guard{}, false
}

// blockIsLegalFor reports whether a statement may be replaced by an `if`
// statement, which every statement form does.
//
// The simple statement slots of `if`, `for`, `switch`, and a type switch guard
// may not: `for i := 0; i < n; if __gm.M[3] { i -= 2 } else { i += 2 }` is not
// Go, and neither is any other block in those positions. A hint the
// instrumenter provably cannot use would be worse than no candidate, so the
// candidate is refused here instead.
func (g *guardResolver) blockIsLegalFor(stmt ast.Stmt) bool {
	switch parent := g.parent[stmt].(type) {
	case *ast.ForStmt:
		return parent.Init != stmt && parent.Post != stmt
	case *ast.IfStmt:
		return parent.Init != stmt
	case *ast.SwitchStmt:
		return parent.Init != stmt
	case *ast.TypeSwitchStmt:
		return parent.Init != stmt && parent.Assign != stmt
	case *ast.CommClause:
		return parent.Comm != stmt
	default:
		return true
	}
}

// statementGuard classifies one statement into Form S or Form D.
//
// The division is exactly "does this statement declare anything": Form S buries
// its site in a block, so a statement that declares a name would take that name
// out of scope for everything after it, and Form D exists to hoist those
// declarations back out. A compound assignment (`x += 1`) declares nothing and
// is Form S; `x := 1` and `var x = 1` declare and are Form D.
func (g *guardResolver) statementGuard(stmt ast.Stmt) (Guard, bool) {
	span, ok := g.span(stmt)
	if !ok {
		return Guard{}, false
	}
	switch s := stmt.(type) {
	case *ast.ExprStmt, *ast.ReturnStmt, *ast.IncDecStmt, *ast.SendStmt, *ast.DeferStmt, *ast.GoStmt:
		return Guard{Form: GuardFormS, SiteSpan: span}, true
	case *ast.AssignStmt:
		if s.Tok != token.DEFINE {
			return Guard{Form: GuardFormS, SiteSpan: span}, true
		}
		declared, ok := g.defineTypes(s)
		if !ok {
			return Guard{}, false
		}
		return Guard{Form: GuardFormD, SiteSpan: span, DeclTypes: declared}, true
	case *ast.DeclStmt:
		declared, ok := g.declTypes(s)
		if !ok {
			return Guard{}, false
		}
		return Guard{Form: GuardFormD, SiteSpan: span, DeclTypes: declared}, true
	default:
		return Guard{}, false
	}
}

// defineTypes names what a `:=` declares.
//
// v1 covers the short declaration in its plain form only: every name on the
// left is an identifier this statement declares. A short declaration is also
// allowed to *re*declare — `a, err := f()` after an earlier `err` assigns the
// existing one — and Form D would then have to declare some names and leave
// others alone, which is a distinction the hint does not carry. Those are
// refused whole rather than half-rewritten. The blank identifier is not a
// redeclaration and needs no declaration of its own, so it is simply passed
// over.
//
// The names are collected before any of them is typed because
// [guardResolver.rebindsOwnInitialiser] has to see the whole left-hand side at
// once; see it for what a partial view would let through.
func (g *guardResolver) defineTypes(assign *ast.AssignStmt) ([]DeclType, bool) {
	idents := make([]*ast.Ident, 0, len(assign.Lhs))
	names := make(map[string]bool, len(assign.Lhs))
	for _, lhs := range assign.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok {
			return nil, false
		}
		if ident.Name == "_" {
			continue
		}
		idents = append(idents, ident)
		names[ident.Name] = true
	}
	if g.rebindsOwnInitialiser(names, assign.Rhs) {
		return nil, false
	}

	out := make([]DeclType, 0, len(idents))
	for _, ident := range idents {
		declared, ok := g.declTypeOf(ident)
		if !ok {
			return nil, false
		}
		out = append(out, declared)
	}
	return out, true
}

// declTypes names what a `var` declaration inside a function body declares.
//
// A `const` or `type` declaration reaches here only if something inside it
// produced a candidate, which the const suppression already prevents for
// constants and which a type declaration has no values to do; both are refused
// rather than guessed at.
//
// The whole declaration is inspected before a single name is typed, and both
// refusals below are the reason. A `var` block is one statement and therefore
// one site: [guardResolver.rebindsOwnInitialiser] has to weigh every
// initialiser in it against every name it declares, and a spec that no
// candidate sits in can still hold the line break that
// [guardResolver.cutIsLineFree] refuses.
func (g *guardResolver) declTypes(decl *ast.DeclStmt) ([]DeclType, bool) {
	gen, ok := decl.Decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.VAR {
		return nil, false
	}
	specs := make([]*ast.ValueSpec, 0, len(gen.Specs))
	names := make(map[string]bool)
	var values []ast.Expr
	for _, spec := range gen.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			return nil, false
		}
		if !g.cutIsLineFree(value) {
			return nil, false
		}
		specs = append(specs, value)
		values = append(values, value.Values...)
		for _, name := range value.Names {
			if name.Name == "_" {
				continue
			}
			names[name.Name] = true
		}
	}
	if g.rebindsOwnInitialiser(names, values) {
		return nil, false
	}

	var out []DeclType
	for _, value := range specs {
		for _, name := range value.Names {
			if name.Name == "_" {
				continue
			}
			declared, ok := g.declTypeOf(name)
			if !ok {
				return nil, false
			}
			out = append(out, declared)
		}
	}
	return out, true
}

// cutIsLineFree reports whether the bytes internal/instrument has to remove
// from one `var` spec to turn it into an assignment hold no line break.
//
// Form D rewrites a declaration by deleting the tokens that make it one, in
// place, and the rewrite is only legal if it leaves every remaining byte on the
// line the user put it on. Two of those deletions are as long as the source
// says they are rather than a fixed token, and each has its own shape:
//
//   - a spec with no initialiser is nothing to assign and goes whole, so the
//     line break in
//     `var ( total struct {\n\thi int\n}\n start = n + 1 )` is inside the cut;
//   - a spec that spells its type out loses the type, so the line break in
//     `var f func(\n\tv int,\n) int = mk(n)` is inside the cut.
//
// Neither can be padded back: writing newlines into the replacement would put
// them where the tokens were, and `f func(\n…\n) int = mk(n)` becomes
// `f \n\n = mk(n)`, which is a different program — the scanner inserts a
// semicolon after `f`. So the site is refused here, where the candidate simply
// is not emitted and the rest of the file still runs, rather than reaching
// internal/instrument as a hint it can only answer with its line-drift error —
// a run-ending internal error over ordinary, gofmt-clean Go.
//
// The other cuts are single fixed tokens — `var`, `(`, `)`, and the `:` of a
// `:=` — and no token holds a line break, so a short declaration never needs
// this and never asks.
func (g *guardResolver) cutIsLineFree(spec *ast.ValueSpec) bool {
	if len(spec.Values) == 0 {
		return g.sameLine(spec.Pos(), spec.End())
	}
	if spec.Type == nil {
		return true
	}
	return g.sameLine(spec.Type.Pos(), spec.Type.End())
}

// sameLine reports whether two positions of this file sit on one line.
func (g *guardResolver) sameLine(from, to token.Pos) bool {
	if !from.IsValid() || !to.IsValid() {
		return false
	}
	return g.tokFile.Line(from) == g.tokFile.Line(to)
}

// rebindsOwnInitialiser reports whether any initialiser of a Form D site
// mentions a name that same site declares.
//
// Go's scoping rule is that the scope of a name declared by a short variable
// declaration or a value specification begins at the *end* of that
// specification. So `total := total * 2` in a block that shadows an outer
// `total` reads the outer one, and `err := fmt.Errorf("step: %w", err)` wraps
// the error that was already there. Form D hoists `var total int;` in front of
// the assignment, which puts the new name in scope before its own initialiser
// runs and silently rebinds those references to a freshly zero-valued variable.
// The program still compiles — that is the danger — and the mutant results
// computed from it are wrong: the instrumented baseline of the `%w` shape type
// checks, passes, and reports kills for mutants that really survive.
//
// The test has to be lexical. Asking [types.Info] whether an initialiser uses
// one of the objects this statement defines silently answers no, always: the
// checker resolved the reference *correctly*, to the outer object, and the
// object Form D would create appears in no Uses entry because it does not exist
// in the source. What the hoist changes is which declaration a *name* binds to,
// so a name is what this compares.
//
// Two kinds of identifier are not references to a variable and are passed over:
// the selected field in `p.x`, and the key of a struct literal, which is a
// field name for the same reason. A composite literal whose keys are not field
// names — a map's — has ordinary expressions there, and they are scanned. The
// walk is otherwise deliberately blunt: it looks at every identifier of an
// initialiser, including ones inside a nested function literal or a struct type
// where a matching name would in fact shadow harmlessly. Each of those costs
// one candidate, recorded as a skip; the reverse mistake costs a wrong verdict.
func (g *guardResolver) rebindsOwnInitialiser(names map[string]bool, values []ast.Expr) bool {
	if len(names) == 0 || len(values) == 0 {
		return false
	}
	found := false
	var visit func(node ast.Node)
	visit = func(node ast.Node) {
		if node == nil || found {
			return
		}
		ast.Inspect(node, func(n ast.Node) bool {
			if found || n == nil {
				return false
			}
			switch x := n.(type) {
			case *ast.Ident:
				if names[x.Name] {
					found = true
				}
				return false
			case *ast.SelectorExpr:
				visit(x.X)
				return false
			case *ast.CompositeLit:
				if x.Type != nil {
					visit(x.Type)
				}
				for _, elt := range x.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						visit(elt)
						continue
					}
					if !g.fieldKeyed(x) {
						visit(kv.Key)
					}
					visit(kv.Value)
				}
				return false
			}
			return true
		})
	}
	for _, value := range values {
		visit(value)
	}
	return found
}

// fieldKeyed reports whether a composite literal's keys are field names rather
// than expressions, which only the type checker can say.
//
// An unknown answer is "no", so the key is scanned and a name matching one the
// site declares refuses it. That is the conservative direction: refusing a
// struct literal costs the candidate, and scanning nothing would let a map
// literal keyed by the variable being declared through.
func (g *guardResolver) fieldKeyed(lit *ast.CompositeLit) bool {
	if g.info == nil {
		return false
	}
	tv, ok := g.info.Types[lit]
	if !ok || tv.Type == nil {
		return false
	}
	_, isStruct := tv.Type.Underlying().(*types.Struct)
	return isStruct
}

// returnSite computes the probe hint of one `return` statement, or nil when
// this file cannot express the rewrite it describes or the statement is not one
// the probe may stand in for.
//
// The result types come from the enclosing function's signature rather than
// from the operands, for the reason [ReturnSite] gives: the declared type is the
// conversion the `return` performs, and it is the conversion the mutant's
// constant would have gone through too. [guardResolver.typeString] is what
// spells them — the same machinery Form D's declarations go through, so a type
// this file cannot name is refused here exactly as it is there, and no second
// spelling rule can drift away from the first.
//
// Every operand has to be effect-free, which is a property of the whole
// statement and so is asked here rather than per result: the mutant does not
// evaluate the operand it replaces, and the rewrite fixes an evaluation order
// the compiler does not use. effects.go argues both. The per-result conditions
// are [guardResolver.probesResult]'s.
//
// [ReturnSite.Index] is left at zero: the caller fills it in per result through
// [ReturnSite.at], so that every candidate of one statement shares one site.
func (g *guardResolver) returnSite(stmt *ast.ReturnStmt, results *types.Tuple) *ReturnSite {
	if stmt == nil || results == nil || results.Len() != len(stmt.Results) {
		return nil
	}
	span, ok := g.span(stmt)
	if !ok {
		return nil
	}
	for _, value := range stmt.Results {
		if !g.effectFree(value) {
			return nil
		}
	}
	spelled := make([]string, 0, results.Len())
	for i := range results.Len() {
		declared := results.At(i).Type()
		if mentionsTypeParam(declared, make(map[types.Type]bool)) {
			return nil
		}
		rendered, spellable := g.typeString(declared)
		if !spellable {
			return nil
		}
		spelled = append(spelled, rendered)
	}
	return &ReturnSite{Span: span, Types: spelled}
}

// probesResult reports whether the probe may stand in for the mutant at one
// result of a statement [guardResolver.returnSite] has already accepted.
//
// Two conditions, both about this result alone. Its operand may not panic,
// because a panic is a divergence between the original and the mutant that the
// comparison is never reached to see. And its declared type may not be a
// floating-point or complex one, because `-0.0 != 0` is false while the two
// values are distinguishable. effects.go argues both, and both leave the other
// results of the same statement probed: the rewrite declares a temporary per
// result and writes an `if` per mutant, so dropping one mutant's `if` is a
// rewrite it already knows how to render.
func (g *guardResolver) probesResult(value ast.Expr, declared types.Type) bool {
	return !floatingResult(declared) && g.panicFree(value)
}

// mentionsTypeParam reports whether a type is, or is built from, a type
// parameter.
//
// The probe compares a temporary of the declared result type against a
// constant, and that comparison is not always legal for a type parameter: a
// constraint may admit types a constant cannot be converted to, or types that
// are not comparable at all. The site is refused whole rather than per result,
// because a statement whose results cannot all be declared is a statement whose
// rewrite cannot be written at all.
//
// [guardResolver.nameable] deliberately accepts a type parameter — it is in
// scope wherever a declaration using it is, and Form D really can declare one —
// so this is a separate question asked for a separate reason, and not a
// tightening of that one. The seen set is for recursive types, which reach
// themselves through a pointer or a slice.
func mentionsTypeParam(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true

	switch typ := types.Unalias(t).(type) {
	case *types.TypeParam:
		return true
	case *types.Named:
		args := typ.TypeArgs()
		for i := 0; args != nil && i < args.Len(); i++ {
			if mentionsTypeParam(args.At(i), seen) {
				return true
			}
		}
		return false
	case *types.Pointer:
		return mentionsTypeParam(typ.Elem(), seen)
	case *types.Slice:
		return mentionsTypeParam(typ.Elem(), seen)
	case *types.Array:
		return mentionsTypeParam(typ.Elem(), seen)
	case *types.Chan:
		return mentionsTypeParam(typ.Elem(), seen)
	case *types.Map:
		return mentionsTypeParam(typ.Key(), seen) || mentionsTypeParam(typ.Elem(), seen)
	case *types.Struct:
		for i := range typ.NumFields() {
			if mentionsTypeParam(typ.Field(i).Type(), seen) {
				return true
			}
		}
		return false
	case *types.Interface:
		for i := range typ.NumExplicitMethods() {
			if mentionsTypeParam(typ.ExplicitMethod(i).Type(), seen) {
				return true
			}
		}
		for i := range typ.NumEmbeddeds() {
			if mentionsTypeParam(typ.EmbeddedType(i), seen) {
				return true
			}
		}
		return false
	case *types.Signature:
		return mentionsTypeParam(typ.Params(), seen) || mentionsTypeParam(typ.Results(), seen)
	case *types.Tuple:
		for i := range typ.Len() {
			if mentionsTypeParam(typ.At(i).Type(), seen) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// declTypeOf renders the type of one declared identifier as this file may
// spell it.
func (g *guardResolver) declTypeOf(ident *ast.Ident) (DeclType, bool) {
	if g.info == nil {
		return DeclType{}, false
	}
	obj := g.info.Defs[ident]
	if obj == nil {
		// Not a definition: the identifier redeclares something declared
		// earlier, or the checker recorded nothing for it.
		return DeclType{}, false
	}
	rendered, ok := g.typeString(obj.Type())
	if !ok {
		return DeclType{}, false
	}
	return DeclType{Name: ident.Name, Type: rendered}, true
}

// typeString renders a type as source this file could hold, or reports false.
//
// Two independent things can go wrong and both have to be caught. The qualifier
// is asked for a name for every package a named type in the type belongs to,
// and it has none for a package the file does not import — that is the
// [strconv.Quote]-shaped failure `types.TypeString` would otherwise paper over
// by printing the full import path. And a type may be perfectly qualifiable and
// still unwritable, because it names something unexported in another package;
// [nameable] walks the type for those.
func (g *guardResolver) typeString(t types.Type) (string, bool) {
	if t == nil {
		return "", false
	}
	reachable := true
	qualifier := func(p *types.Package) string {
		name, ok := g.qualify(p)
		if !ok {
			reachable = false
		}
		return name
	}
	rendered := types.TypeString(t, qualifier)
	if !reachable || rendered == "" {
		return "", false
	}
	if !g.nameable(t, make(map[types.Type]bool)) {
		return "", false
	}
	return rendered, true
}

// qualify is the [types.Qualifier] the declared types are rendered with.
//
// The package under test renders unqualified, which is the whole reason this is
// not [types.RelativeTo] over some other package: a local type written as
// `mini.Buffer` into a file of package mini does not compile. Everything else
// has to be reachable by a name the file already binds; discovery never adds an
// import to make a type spellable.
func (g *guardResolver) qualify(p *types.Package) (string, bool) {
	if p == nil || p == g.pkg {
		return "", true
	}
	local, imported := g.imports[p.Path()]
	if !imported {
		return "", false
	}
	if local == "" {
		return p.Name(), true
	}
	return local, true
}

// nameable reports whether every part of a type can be written in this file.
//
// Qualification is not enough on its own. `foo.New()` may return a `*foo.impl`
// whose name is unexported, `unsafe.Pointer` is a basic type that still needs an
// import, and an untyped kind has no source spelling at all. Each of those
// renders as something plausible and compiles as nothing, so each is refused
// here. The seen set is for recursive types, which reach themselves through a
// pointer or a slice and would otherwise not terminate.
func (g *guardResolver) nameable(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return t != nil
	}
	seen[t] = true

	switch typ := t.(type) {
	case *types.Basic:
		return typ.Kind() != types.Invalid &&
			typ.Kind() != types.UnsafePointer &&
			typ.Info()&types.IsUntyped == 0
	case *types.Named:
		if !g.nameableObj(typ.Obj()) {
			return false
		}
		args := typ.TypeArgs()
		for i := 0; args != nil && i < args.Len(); i++ {
			if !g.nameable(args.At(i), seen) {
				return false
			}
		}
		return true
	case *types.Alias:
		if !g.nameableObj(typ.Obj()) {
			return false
		}
		args := typ.TypeArgs()
		for i := 0; args != nil && i < args.Len(); i++ {
			if !g.nameable(args.At(i), seen) {
				return false
			}
		}
		return true
	case *types.TypeParam:
		// A type parameter is in scope wherever a declaration using it is, and
		// renders as its own name.
		return true
	case *types.Pointer:
		return g.nameable(typ.Elem(), seen)
	case *types.Slice:
		return g.nameable(typ.Elem(), seen)
	case *types.Array:
		return g.nameable(typ.Elem(), seen)
	case *types.Chan:
		return g.nameable(typ.Elem(), seen)
	case *types.Map:
		return g.nameable(typ.Key(), seen) && g.nameable(typ.Elem(), seen)
	case *types.Struct:
		for i := range typ.NumFields() {
			field := typ.Field(i)
			if !field.Exported() && field.Pkg() != nil && field.Pkg() != g.pkg {
				return false
			}
			if !g.nameable(field.Type(), seen) {
				return false
			}
		}
		return true
	case *types.Interface:
		for i := range typ.NumExplicitMethods() {
			method := typ.ExplicitMethod(i)
			if !method.Exported() && method.Pkg() != nil && method.Pkg() != g.pkg {
				return false
			}
			if !g.nameable(method.Type(), seen) {
				return false
			}
		}
		for i := range typ.NumEmbeddeds() {
			if !g.nameable(typ.EmbeddedType(i), seen) {
				return false
			}
		}
		return true
	case *types.Signature:
		return g.nameable(typ.Params(), seen) && g.nameable(typ.Results(), seen)
	case *types.Tuple:
		for i := range typ.Len() {
			if !g.nameable(typ.At(i).Type(), seen) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// nameableObj reports whether a named type's own name can be written here: it
// has to be either this package's or exported from a package the file can
// qualify.
func (g *guardResolver) nameableObj(obj *types.TypeName) bool {
	if obj == nil {
		return false
	}
	pkg := obj.Pkg()
	if pkg == nil || pkg == g.pkg {
		// A universe type, or one of this package's own. A type declared inside
		// a function body is this package's too, and is in scope wherever a
		// declaration of it is.
		return true
	}
	if !obj.Exported() {
		return false
	}
	_, ok := g.qualify(pkg)
	return ok
}
