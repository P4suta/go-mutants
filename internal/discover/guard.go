// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"

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
	// siblings maps an import path some *other* file of this package imports to
	// the name to prefer for it. It is what import completion may draw on, and
	// imports.go argues at length why that set and no wider one.
	siblings map[string]string
	// added records the name each completed path has been given in this file,
	// so that two rewrites of one file never bind one package twice under two
	// names. It grows as the file is walked and is never reset.
	added map[string]string
	// taken is every name a completion may not bind: the file's own
	// identifiers, the names its imports already bind, and the package block,
	// which a file-scoped import may not collide with even across files.
	taken map[string]bool
}

// newGuardResolver indexes one file.
//
// siblings is the import index of the package's other files, and may be nil for
// a package of one file — a file with no siblings has nothing to complete from,
// which is a smaller statement than "completion is off".
func newGuardResolver(
	file *ast.File, info *types.Info, pkg *types.Package, tokFile *token.File, siblings map[string]string,
) *guardResolver {
	g := &guardResolver{
		info:     info,
		pkg:      pkg,
		tokFile:  tokFile,
		parent:   make(map[ast.Node]ast.Node),
		imports:  make(map[string]string),
		siblings: siblings,
		added:    make(map[string]string),
		taken:    make(map[string]bool),
	}
	g.indexParents(file)
	g.indexImports(file)
	g.indexTakenNames(file)
	return g
}

// indexTakenNames gathers every identifier a completion may not bind.
//
// Three scopes, and each of them can be wrong in a different way. Every
// identifier in the file counts, because a local variable sharing the name
// would shadow the import for exactly the statements a guard sits in. Every
// name an existing import binds counts, including the implicit one of a plain
// import, which no identifier node spells. And every name the package block
// binds counts, which is not shadowing at all: Go forbids one name appearing in
// a file block and in the package block of the same package, so a `var carrier`
// in a sibling file makes `import carrier "…"` here a hard error.
//
// internal/instrument does the same three scopes for the runtime alias, by
// reading the directory; here the package block arrives free, because the type
// checker has already built it.
func (g *guardResolver) indexTakenNames(file *ast.File) {
	ast.Inspect(file, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok {
			g.taken[ident.Name] = true
		}
		return true
	})
	for importPath, local := range g.imports {
		if local == "" {
			local = defaultLocal(importPath)
		}
		g.taken[local] = true
	}
	if g.pkg != nil && g.pkg.Scope() != nil {
		for _, name := range g.pkg.Scope().Names() {
			g.taken[name] = true
		}
	}
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
// reporting false when no form can express it. Every false is
// a [SkipUnnameableDeclType] skip; see [Guard] for the full list of them.
func (g *guardResolver) guardFor(anchor ast.Node) (Guard, bool) {
	if site, ok := g.formCSite(anchor); ok {
		return site, true
	}
	if site, ok := g.statementSite(anchor); ok {
		return site, true
	}
	if site, ok := g.formCPrimeSite(anchor); ok {
		return site, true
	}
	return g.formESite(anchor)
}

// formESite looks outward for the nearest expression this file can spell the
// type of.
//
// It is the last form and the least demanding one, which is why it is last: a
// site any earlier form covers is covered by that form, and this adds the
// positions none of them reach. A `switch` tag, a `range` clause and a type
// switch guard are expressions with no statement around them a guard can stand
// in; the initialiser of a `:=` in an `if` or `for` header is an expression
// whose statement declares, which Form F cannot move into a closure and Form D
// has nowhere to hoist to.
//
// The walk is [guardResolver.formCSite]'s, and the two conditions are the ones
// the closure needs. The expression has to be a *value* -- a type in a type
// switch case and a package name in a qualified identifier are expressions to
// go/ast and neither is something a function can return. And its type has to be
// spellable, because the closure's result type is written out; that is the last
// refusal `unnameable-decl-type` is left naming.
func (g *guardResolver) formESite(anchor ast.Node) (Guard, bool) {
	for node := anchor; node != nil; node = g.parent[node] {
		expr, ok := node.(ast.Expr)
		if !ok {
			return Guard{}, false
		}
		if !g.wrappableValue(expr) {
			continue
		}
		span, ok := g.span(expr)
		if !ok {
			return Guard{}, false
		}
		spelled, needs, ok := g.typeString(g.info.Types[expr].Type)
		if !ok {
			// A type this file cannot name, which is not the end of the search:
			// an expression around this one may have a type it can. The walk
			// continues for the same reason Form C's does when it meets a
			// non-boolean expression -- the site is the nearest *usable*
			// ancestor, not the nearest one.
			continue
		}
		return Guard{Form: GuardFormE, SiteSpan: span, SiteType: spelled, Imports: needs}, true
	}
	return Guard{}, false
}

// wrappableValue reports whether an expression is a value of a type a closure
// could return, and sits where a call of that type is legal Go.
//
// "Value" is the load-bearing word and go/types answers it: `case int:` in a
// type switch records a *type* rather than a value, `fmt` in `fmt.Println`
// records a package, and `len` records a builtin. None of the three is
// something a function can return, and all three are ast.Expr.
//
// Untyped constants need no special case, and that is worth saying because it
// looks as though they would. The checker records the type an expression
// *settled on*, so the `1` of `var x float64 = 1` records `float64` and the
// closure returns a float64; a constant in a context that keeps it untyped is
// in a constant declaration, which discovery suppresses whole before any of
// this is asked.
func (g *guardResolver) wrappableValue(expr ast.Expr) bool {
	if g.info == nil {
		return false
	}
	tv, ok := g.info.Types[expr]
	if !ok || !tv.IsValue() || tv.Type == nil {
		return false
	}
	return g.wrappablePosition(expr)
}

// formCPrimeSite looks outward for the nearest expression that is boolean
// underneath and whose type this file can spell.
//
// It is [guardResolver.formCSite]'s fallback and shares its whole shape: the
// same outward walk, the same stop at the first ancestor that is not an
// expression, the same [guardResolver.wrappablePosition]. What differs is the
// type gate -- bool *underneath* rather than exactly the universe bool -- and
// that the site has to carry the type, because the selector the instrumenter
// writes is untyped and has to be converted back.
//
// The order matters and is the reason this is a separate function rather than
// a loosened gate in formCSite. Run last, it can only add sites: anything Form
// C or one of the statement forms already covered is still covered by the form
// that covered it, byte for byte and identity for identity.
func (g *guardResolver) formCPrimeSite(anchor ast.Node) (Guard, bool) {
	for node := anchor; node != nil; node = g.parent[node] {
		expr, ok := node.(ast.Expr)
		if !ok {
			return Guard{}, false
		}
		if !g.wrappableNamedBool(expr) {
			continue
		}
		span, ok := g.span(expr)
		if !ok {
			return Guard{}, false
		}
		spelled, needs, ok := g.typeString(g.info.Types[expr].Type)
		if !ok {
			// A boolean type this file cannot name. The same refusal Form D
			// makes about a declared type, for the same reason: go-mutants
			// knows what it would write and cannot say it in Go.
			return Guard{}, false
		}
		return Guard{Form: GuardFormCPrime, SiteSpan: span, SiteType: spelled, Imports: needs}, true
	}
	return Guard{}, false
}

// wrappableNamedBool reports whether an expression is boolean underneath but
// not the universe bool, and sits where a conversion around it is legal Go.
//
// The universe bool is excluded rather than merely unnecessary: an expression
// of that type is a Form C site, and letting this form claim one would change
// which form an existing candidate uses, which is a change to the bytes of the
// instrumented tree for no gain at all.
func (g *guardResolver) wrappableNamedBool(expr ast.Expr) bool {
	if g.info == nil {
		return false
	}
	tv, ok := g.info.Types[expr]
	if !ok || !tv.IsValue() || isUniverseBool(tv.Type) || !isBoolClassed(tv.Type) {
		return false
	}
	// A conversion is an expression, so every position that accepts a
	// parenthesised expression of the site's own type accepts one.
	return g.wrappablePosition(expr)
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
				return g.closureSite(n)
			}
			return g.statementGuard(n)
		}
	}
	return Guard{}, false
}

// closureSite decides whether a statement a block cannot replace may be
// replaced by a call instead.
//
// This is Form F, and it is what the initialiser and post slots were always
// waiting for. Those slots hold a *simple* statement -- an expression
// statement, a send, an `++`/`--`, an assignment, or a short declaration -- and
// a block is not one of them, which is the whole of why `for i := 0; i < n; if
// __gm.M[3] { … }` does not parse. A call is an expression, an expression alone
// is an expression statement, and an expression statement is simple. So the
// guard goes inside a closure and the closure is called where the statement
// was.
//
// Two questions have to agree for that to be sound, and they are asked apart
// because they are about different things. [FormFStatement] asks whether the
// *statement* survives being moved into a function body, which is a question
// about `return`, `defer` and the branch statements. simpleStmtSlot asks
// whether the *slot* accepts a call, which is a question about the grammar: a
// type switch guard is not a simple statement at all, and a communication
// clause has to be a send or a receive, which a call is neither.
func (g *guardResolver) closureSite(stmt ast.Stmt) (Guard, bool) {
	if !FormFStatement(stmt) || !g.simpleStmtSlot(stmt) {
		return Guard{}, false
	}
	span, ok := g.span(stmt)
	if !ok {
		return Guard{}, false
	}
	return Guard{Form: GuardFormF, SiteSpan: span}, true
}

// simpleStmtSlot reports whether a statement sits in a slot that holds a simple
// statement, which is where a call is legal and a block is not.
func (g *guardResolver) simpleStmtSlot(stmt ast.Stmt) bool {
	switch parent := g.parent[stmt].(type) {
	case *ast.ForStmt:
		return parent.Init == stmt || parent.Post == stmt
	case *ast.IfStmt:
		return parent.Init == stmt
	case *ast.SwitchStmt:
		return parent.Init == stmt
	case *ast.TypeSwitchStmt:
		// The initialiser only. The Assign is the type switch guard, which is
		// its own production and not a simple statement.
		return parent.Init == stmt
	default:
		return false
	}
}

// FormFStatement reports whether a statement is one Form F may move into a
// closure.
//
// It is [FormSStatement]'s list minus three kinds, and each exclusion is a
// different fact rather than caution:
//
//   - a `return` inside the closure returns from the *closure*, so the
//     enclosing function would fall through instead;
//   - a `defer` fires when the closure returns, which is immediately, rather
//     than when the enclosing function does;
//   - a `break`, `continue` or `goto` cannot cross a function boundary and
//     would not compile.
//
// A `go` is the one that would be safe and is excluded anyway, because none of
// the four can appear in a slot this form reaches -- the grammar there holds a
// simple statement, and `return`, `defer` and `go` are not simple ones -- so
// the list is exactly the four that can, and a fifth entry nothing could use
// would be an arm nobody could reach.
//
// It is exported for [FormSStatement]'s reason: internal/instrument asks the
// same question again, independently, and a test in that package holds the two
// implementations to each other over every statement kind Go has.
func FormFStatement(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt, *ast.SendStmt, *ast.IncDecStmt:
		return true
	case *ast.AssignStmt:
		return s.Tok != token.DEFINE
	default:
		return false
	}
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

// FormSStatement reports whether a statement is one Form S may bury in a block.
//
// The list is short for one reason: every statement here declares nothing, so
// wrapping it in `if … { … } else { … }` changes no scope and the code after it
// goes on compiling. A `:=` and a `var` do declare, which is what Form D exists
// for.
//
// `defer` and `go` are in the list and are wrapped whole, statement and all,
// rather than having their call rewritten in place. Both are function-scoped
// rather than block-scoped: a `defer` inside the guard's block still runs when
// the enclosing *function* returns, and a `go` still starts its goroutine, so
// the block the guard adds changes nothing about when either fires.
//
// It is exported because internal/instrument asks the same question of the same
// statement and must go on asking it independently — a hint naming a statement
// that package cannot wrap has to be refused there rather than trusted — and
// two implementations of one list can disagree. They are held to each other by
// a test in that package, over a table of every statement kind Go has. Sharing
// the *answer* would be the wrong fix: the second check is the fail-closed one,
// and a check that calls the thing it is checking is not a check.
func FormSStatement(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt, *ast.ReturnStmt, *ast.IncDecStmt, *ast.SendStmt, *ast.DeferStmt, *ast.GoStmt:
		return true
	case *ast.AssignStmt:
		return s.Tok != token.DEFINE
	case *ast.BranchStmt:
		// `break`, `continue` and `goto` declare nothing and bind to the
		// nearest enclosing construct of their own kind, and an `if` is not one
		// -- so burying any of them in the guard's block changes neither scope
		// nor target. `fallthrough` is the exception and is a syntactic one: it
		// has to be the final statement of a case clause, and a statement
		// inside an `if` block is not that.
		return s.Tok != token.FALLTHROUGH
	default:
		return false
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
	if FormSStatement(stmt) {
		return Guard{Form: GuardFormS, SiteSpan: span}, true
	}
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		// Not Form S, so it declares: the only assignment [FormSStatement]
		// refuses is a `:=`.
		declared, needs, ok := g.defineTypes(s)
		if !ok {
			return Guard{}, false
		}
		return Guard{Form: GuardFormD, SiteSpan: span, DeclTypes: declared, Imports: needs}, true
	case *ast.DeclStmt:
		declared, needs, ok := g.declTypes(s)
		if !ok {
			return Guard{}, false
		}
		return Guard{Form: GuardFormD, SiteSpan: span, DeclTypes: declared, Imports: needs}, true
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
func (g *guardResolver) defineTypes(assign *ast.AssignStmt) ([]DeclType, []Completion, bool) {
	idents := make([]*ast.Ident, 0, len(assign.Lhs))
	names := make(map[string]bool, len(assign.Lhs))
	for _, lhs := range assign.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok {
			return nil, nil, false
		}
		if ident.Name == "_" {
			continue
		}
		idents = append(idents, ident)
		names[ident.Name] = true
	}
	if g.rebindsOwnInitialiser(names, assign.Rhs) {
		return nil, nil, false
	}

	out := make([]DeclType, 0, len(idents))
	var needs []Completion
	for _, ident := range idents {
		declared, completed, ok := g.declTypeOf(ident)
		if !ok {
			return nil, nil, false
		}
		out = append(out, declared)
		needs = MergeCompletions(needs, completed)
	}
	return out, needs, true
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
func (g *guardResolver) declTypes(decl *ast.DeclStmt) ([]DeclType, []Completion, bool) {
	gen, ok := decl.Decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.VAR {
		return nil, nil, false
	}
	specs := make([]*ast.ValueSpec, 0, len(gen.Specs))
	names := make(map[string]bool)
	var values []ast.Expr
	for _, spec := range gen.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			return nil, nil, false
		}
		if !g.cutIsLineFree(value) {
			return nil, nil, false
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
		return nil, nil, false
	}

	var out []DeclType
	var needs []Completion
	for _, value := range specs {
		for _, name := range value.Names {
			if name.Name == "_" {
				continue
			}
			declared, completed, ok := g.declTypeOf(name)
			if !ok {
				return nil, nil, false
			}
			out = append(out, declared)
			needs = MergeCompletions(needs, completed)
		}
	}
	return out, needs, true
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
// from the operands, for the reason [ProbeSite] gives: the declared type is the
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
// [ProbeSite.Index] is left at zero: the caller fills it in per result through
// [ProbeSite.at], so that every candidate of one statement shares one site.
func (g *guardResolver) probeSite(stmt *ast.ReturnStmt, results *types.Tuple) *ProbeSite {
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
	var needs []Completion
	for i := range results.Len() {
		declared := results.At(i).Type()
		if mentionsTypeParam(declared, make(map[types.Type]bool)) {
			return nil
		}
		rendered, completed, spellable := g.typeString(declared)
		if !spellable {
			return nil
		}
		spelled = append(spelled, rendered)
		needs = MergeCompletions(needs, completed)
	}
	return &ProbeSite{Form: ProbeFormReturn, Span: span, Types: spelled, Imports: needs}
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
func (g *guardResolver) declTypeOf(ident *ast.Ident) (DeclType, []Completion, bool) {
	if g.info == nil {
		return DeclType{}, nil, false
	}
	obj := g.info.Defs[ident]
	if obj == nil {
		// Not a definition: the identifier redeclares something declared
		// earlier, or the checker recorded nothing for it.
		return DeclType{}, nil, false
	}
	rendered, needs, ok := g.typeString(obj.Type())
	if !ok {
		return DeclType{}, nil, false
	}
	return DeclType{Name: ident.Name, Type: rendered}, needs, true
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
func (g *guardResolver) typeString(t types.Type) (string, []Completion, bool) {
	if t == nil {
		return "", nil, false
	}
	reachable := true
	var completed []Completion
	qualifier := func(p *types.Package) string {
		name, completion, ok := g.qualify(p)
		if !ok {
			reachable = false
			return ""
		}
		if completion != nil && !slices.Contains(completed, *completion) {
			completed = append(completed, *completion)
		}
		return name
	}
	rendered := types.TypeString(t, qualifier)
	if !reachable || rendered == "" {
		return "", nil, false
	}
	if !g.nameable(t, make(map[types.Type]bool)) {
		return "", nil, false
	}
	slices.SortFunc(completed, func(a, b Completion) int { return strings.Compare(a.Path, b.Path) })
	return rendered, completed, true
}

// qualify is the [types.Qualifier] the declared types are rendered with.
//
// The package under test renders unqualified, which is the whole reason this is
// not [types.RelativeTo] over some other package: a local type written as
// `mini.Buffer` into a file of package mini does not compile. Everything else
// has to be reachable by a name the file binds — or by one a *sibling* file
// binds, which this file may then be given; imports.go argues why that and no
// wider set.
//
// The second return is the import the answer depends on, and is nil whenever
// the file could already spell it. A caller that cannot carry an import must
// therefore not merely ignore it: a rendered type whose completion is dropped
// is a type spelled with a name nothing binds.
func (g *guardResolver) qualify(p *types.Package) (string, *Completion, bool) {
	if p == nil || p == g.pkg {
		return "", nil, true
	}
	if local, imported := g.imports[p.Path()]; imported {
		if local == "" {
			return p.Name(), nil, true
		}
		return local, nil, true
	}
	return g.complete(p)
}

// complete gives this file a name for a package a sibling file imports.
//
// The name is chosen once per path per file and remembered, so that every
// rewrite of one file agrees about what the package is called — two names for
// one package would be two imports, and the second would be a redeclaration.
//
// A preferred name already bound in this file is bumped rather than refused.
// Refusing would make the completion depend on whether some unrelated local
// variable happened to share a package's name, which is a rule nobody could
// predict; bumping is what internal/instrument already does for the runtime
// alias, for the same reason.
func (g *guardResolver) complete(p *types.Package) (string, *Completion, bool) {
	importPath := p.Path()
	if chosen, done := g.added[importPath]; done {
		return chosen, &Completion{Path: importPath, Local: chosen}, true
	}
	preferred, sibling := g.siblings[importPath]
	if !sibling {
		return "", nil, false
	}
	if preferred == "" {
		preferred = p.Name()
	}
	if preferred == "" {
		preferred = defaultLocal(importPath)
	}
	chosen := g.freeName(preferred)
	g.added[importPath] = chosen
	g.taken[chosen] = true
	return chosen, &Completion{Path: importPath, Local: chosen}, true
}

// freeName is preferred, or preferred with the lowest number past 1 appended
// that nothing in this file binds.
//
// The counter is bounded because an unbounded search over a set that only grows
// is a loop whose termination depends on the data. A file binding `x`, `x2` …
// `x64` is not one this tool needs to rewrite, and stopping is better than
// spinning.
func (g *guardResolver) freeName(preferred string) string {
	if !g.taken[preferred] {
		return preferred
	}
	for n := 2; n < 64; n++ {
		candidate := preferred + strconv.Itoa(n)
		if !g.taken[candidate] {
			return candidate
		}
	}
	return preferred
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
	return g.reachable(pkg)
}

// reachable reports whether a package can be named in this file, without
// deciding what to call it.
//
// [guardResolver.qualify] would answer the same question and *choose a name* on
// the way, which is a side effect a check should not have: a type refused a
// moment later by [guardResolver.nameable] would leave a package reserved under
// a name nothing ever writes. The reservation is harmless and deterministic,
// and making the check pure is cheaper than explaining it.
func (g *guardResolver) reachable(p *types.Package) bool {
	if p == nil || p == g.pkg {
		return true
	}
	if _, imported := g.imports[p.Path()]; imported {
		return true
	}
	if _, done := g.added[p.Path()]; done {
		return true
	}
	_, sibling := g.siblings[p.Path()]
	return sibling
}
