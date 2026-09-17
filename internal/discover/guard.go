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

type guardResolver struct {
	info     *types.Info
	pkg      *types.Package
	tokFile  *token.File
	parent   map[ast.Node]ast.Node
	imports  map[string]string
	siblings map[string]string
	added    map[string]string
	taken    map[string]bool
}

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

func (g *guardResolver) span(node ast.Node) mutation.Span {
	return mutation.Span{
		StartByte: uint32(g.tokFile.Offset(node.Pos())),
		EndByte:   uint32(g.tokFile.Offset(node.End())),
	}
}

func (g *guardResolver) guardFor(anchor ast.Node) (Guard, bool) {
	guard, ok := g.chooseForm(anchor)
	if !ok {
		return Guard{}, false
	}
	if guard.Probe == nil {
		guard.Probe = g.valueProbe(anchor)
	}
	return guard, true
}

func (g *guardResolver) chooseForm(anchor ast.Node) (Guard, bool) {
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

func (g *guardResolver) formESite(anchor ast.Node) (Guard, bool) {
	for node := anchor; node != nil; node = g.parent[node] {
		expr, ok := node.(ast.Expr)
		if !ok {
			return Guard{}, false
		}
		if !g.wrappableValue(expr) {
			continue
		}
		span := g.span(expr)
		spelled, needs, ok := g.typeString(g.info.Types[expr].Type)
		if !ok {
			continue
		}
		return Guard{Form: GuardFormE, SiteSpan: span, SiteType: spelled, Imports: needs}, true
	}
	return Guard{}, false
}

func (g *guardResolver) valueProbe(anchor ast.Node) *ProbeSite {
	for node := anchor; node != nil; node = g.parent[node] {
		expr, ok := node.(ast.Expr)
		if !ok {
			return nil
		}
		if !g.wrappableValue(expr) {
			continue
		}
		declared := g.info.Types[expr].Type
		if !comparesWithoutPanic(declared) || floatingResult(declared) {
			continue
		}
		if !g.inertContext(expr) || !g.panicFree(expr) {
			continue
		}
		span := g.span(expr)
		spelled, needs, ok := g.typeString(declared)
		if !ok {
			continue
		}
		return &ProbeSite{
			Form:    ProbeFormValue,
			Span:    span,
			Types:   []string{spelled},
			Imports: needs,
		}
	}
	return nil
}

func (g *guardResolver) reachProbe(guard Guard) *ProbeSite {
	if guard.Form != GuardFormS {
		return nil
	}
	return &ProbeSite{Form: ProbeFormReach, Span: guard.SiteSpan}
}

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

func (g *guardResolver) formCPrimeSite(anchor ast.Node) (Guard, bool) {
	for node := anchor; node != nil; node = g.parent[node] {
		expr, ok := node.(ast.Expr)
		if !ok {
			return Guard{}, false
		}
		if !g.wrappableNamedBool(expr) {
			continue
		}
		span := g.span(expr)
		spelled, needs, ok := g.typeString(g.info.Types[expr].Type)
		if !ok {
			return Guard{}, false
		}
		return Guard{Form: GuardFormCPrime, SiteSpan: span, SiteType: spelled, Imports: needs}, true
	}
	return Guard{}, false
}

func (g *guardResolver) wrappableNamedBool(expr ast.Expr) bool {
	if g.info == nil {
		return false
	}
	tv, ok := g.info.Types[expr]
	if !ok || !tv.IsValue() || isUniverseBool(tv.Type) || !isBoolClassed(tv.Type) {
		return false
	}
	return g.wrappablePosition(expr)
}

func (g *guardResolver) formCSite(anchor ast.Node) (Guard, bool) {
	for node := anchor; node != nil; node = g.parent[node] {
		expr, ok := node.(ast.Expr)
		if !ok {
			return Guard{}, false
		}
		if !g.wrappableBool(expr) {
			continue
		}
		span := g.span(expr)
		return Guard{Form: GuardFormC, SiteSpan: span, Probe: g.boolProbe(expr, span)}, true
	}
	return Guard{}, false
}

func (g *guardResolver) boolProbe(expr ast.Expr, span mutation.Span) *ProbeSite {
	if !g.inertContext(expr) || !g.panicFree(expr) {
		return nil
	}
	return &ProbeSite{Form: ProbeFormBool, Span: span}
}

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

func (g *guardResolver) closureSite(stmt ast.Stmt) (Guard, bool) {
	if !FormFStatement(stmt) || !g.simpleStmtSlot(stmt) {
		return Guard{}, false
	}
	span := g.span(stmt)
	return Guard{Form: GuardFormF, SiteSpan: span}, true
}

func (g *guardResolver) simpleStmtSlot(stmt ast.Stmt) bool {
	switch parent := g.parent[stmt].(type) {
	case *ast.ForStmt:
		return parent.Init == stmt || parent.Post == stmt
	case *ast.IfStmt:
		return parent.Init == stmt
	case *ast.SwitchStmt:
		return parent.Init == stmt
	case *ast.TypeSwitchStmt:
		return parent.Init == stmt
	default:
		return false
	}
}

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

func FormSStatement(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt, *ast.ReturnStmt, *ast.IncDecStmt, *ast.SendStmt, *ast.DeferStmt, *ast.GoStmt:
		return true
	case *ast.AssignStmt:
		return s.Tok != token.DEFINE
	case *ast.BranchStmt:
		return s.Tok != token.FALLTHROUGH
	default:
		return false
	}
}

func (g *guardResolver) statementGuard(stmt ast.Stmt) (Guard, bool) {
	span := g.span(stmt)
	if FormSStatement(stmt) {
		return Guard{Form: GuardFormS, SiteSpan: span}, true
	}
	switch s := stmt.(type) {
	case *ast.AssignStmt:
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

func (g *guardResolver) cutIsLineFree(spec *ast.ValueSpec) bool {
	if len(spec.Values) == 0 {
		return g.sameLine(spec.Pos(), spec.End())
	}
	if spec.Type == nil {
		return true
	}
	return g.sameLine(spec.Type.Pos(), spec.Type.End())
}

func (g *guardResolver) sameLine(from, to token.Pos) bool {
	if !from.IsValid() || !to.IsValid() {
		return false
	}
	return g.tokFile.Line(from) == g.tokFile.Line(to)
}

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

func (g *guardResolver) probeSite(stmt *ast.ReturnStmt, results *types.Tuple) *ProbeSite {
	if stmt == nil || results == nil || results.Len() != len(stmt.Results) {
		return nil
	}
	span := g.span(stmt)
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

func (g *guardResolver) probesResult(value ast.Expr, declared types.Type) bool {
	return !floatingResult(declared) && g.panicFree(value)
}

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

func (g *guardResolver) declTypeOf(ident *ast.Ident) (DeclType, []Completion, bool) {
	if g.info == nil {
		return DeclType{}, nil, false
	}
	obj := g.info.Defs[ident]
	if obj == nil {
		return DeclType{}, nil, false
	}
	rendered, needs, ok := g.typeString(obj.Type())
	if !ok {
		return DeclType{}, nil, false
	}
	return DeclType{Name: ident.Name, Type: rendered}, needs, true
}

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

func (g *guardResolver) nameableObj(obj *types.TypeName) bool {
	if obj == nil {
		return false
	}
	pkg := obj.Pkg()
	if pkg == nil || pkg == g.pkg {
		return true
	}
	if !obj.Exported() {
		return false
	}
	return g.reachable(pkg)
}

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
