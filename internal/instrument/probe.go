// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"bytes"
	"fmt"
	"go/ast"
	"strconv"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/interval"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// The probe rewrite of a `return`.
//
// # What it is
//
// A probe tree is the original program with a report attached. For a
// return-value mutant at result position j of `return E0, E1, …`, whose
// replacement is the constant K, the statement becomes
//
//	{ var r0 T0 = E0; var r1 T1 = E1; …; if rj != K { __gm.Infect(i) }; return r0, r1, … }
//
// and the mutant is never active: what runs is the program the user wrote, and
// what is recorded is whether the mutated value would have differed from it.
//
// # Why it is exact
//
// The mutant this stands in for is `return …, K, …`: it returns the constant
// *instead of evaluating* Ej. So the rewrite can only speak for it where
// evaluating Ej is nothing but computing a value, and internal/discover hands a
// hint only where that holds. Three conditions, each argued in full in that
// package's effects.go.
//
// Every operand of the statement is effect-free — no call, no method call, no
// receive, no `append`. Then nothing the mutant skips was going to change what
// the program does, and no evaluation order is observable: the `var` sequence
// evaluates in source order, which is *not* the order gc uses for a plain
// variable read beside a call, and with no effects anywhere every order yields
// the same values. So the block's execution is the original's.
//
// The probed operand cannot panic. A panic there is a divergence — the mutant
// returns its constant and panics at nothing — and it is one the comparison is
// never reached to record, so the log would read exactly as it reads for a site
// that never differed. The *other* operands may still panic: the original and
// the mutant panic there identically, and recording nothing is then the truth.
//
// The probed result is not floating-point or complex, because `-0.0 != 0` is
// false while `math.Signbit` and `1/x` tell those two values apart. NaN needs
// no rule: `NaN != 0` is true, so such a site reports infected, which is only
// ever the safe answer.
//
// This side re-derives none of it. The instrumenter has no type information and
// trusts the hint exactly as it trusts a Form D declared type: a hint present
// is a site discovery proved these three things about.
//
// Tj is the *declared result type* of the enclosing function, not the type of
// the operand: `return 0` in a function returning int64 becomes
// `var r0 int64 = 0`, which is exactly the conversion the `return` performs.
// The mutant's `return K` would have gone through the same conversion, so
// `rj != K` compares the two values the two programs would really have
// returned. internal/discover spells those types, with the machinery a Form D
// declaration already goes through, and refuses the site when it cannot.
//
// The comparison itself is total for every constant this family produces.
// Numbers, strings and booleans compare with `!=` whatever named type they
// wear, and a comparison of an interface, pointer, slice, map, channel or
// function value with the `nil` literal compares against the nil value of that
// very type — no dynamic-type comparison happens, so nothing panics.
//
// Named results and `defer` see what they always saw: `return r0, r1` assigns
// the named results exactly as the original did, before any deferred function
// runs. And a block whose last statement is a `return` is a terminating
// statement, so a function whose body ended in one still does.
//
// # What it is not
//
// It is not a guard. Nothing here reads an activation flag, no mutated copy is
// written, and the two trees never share a snapshot. Several mutants of one
// statement — `return-true` and `return-false` on one boolean result, or one
// rule on each of two results — are several `if` lines inside one block, never
// nested rewrites, because there is only ever one rewrite per statement.

// probeConstants are the replacements the return-value family produces, and the
// only ones this form knows how to compare against.
//
// It is a set rather than a rule-name check on purpose: what the rewrite needs
// is a constant expression it can put on the right of `!=`, and the set of
// those is the thing to be exact about. A rule whose replacement is anything
// else has no probe form yet and is left unprobed rather than guessed at.
var probeConstants = map[string]bool{
	"0":     true,
	`""`:    true,
	"true":  true,
	"false": true,
	"nil":   true,
}

// A probeEdit is what one mutant contributes to its site: which result it
// replaces, the constant it would have replaced it with, and the dense index it
// reports under.
type probeEdit struct {
	// index is the catalogue's dense index, which is what Infect records.
	index uint32
	// result is the position of the result the mutant replaces.
	result int
	// constant is the replacement, as it is written on the right of the
	// comparison.
	constant string
}

// probeFor returns what one mutant contributes to a probe tree, or nil when
// this version has no probe form for it.
//
// The nil is the dispatch point for every form still to come. A bool-valued
// site, an arithmetic operand, a deleted statement: each will need a shape of
// its own, and until it has one the honest answer is that the mutant is not
// probed — which costs a run the executions it could have skipped and costs it
// nothing else.
func probeFor(m mutation.Mutant, guard discover.Guard) *probeEdit {
	site := guard.Return
	if site == nil || !probeConstants[m.Replacement] {
		return nil
	}
	return &probeEdit{index: m.Index, result: site.Index, constant: m.Replacement}
}

// A probeSite is one `return` statement as the rewrite needs it: the bytes it
// replaces, where each returned value sits inside them, and the type each
// result has to be declared as.
type probeSite struct {
	// span is the byte range of the whole statement, in file coordinates.
	span mutation.Span
	// operands are the byte ranges of the returned values, in order.
	operands []mutation.Span
	// types is one spelled type per operand, from the hint.
	types []string
}

// probeSiteFor turns one mutant's probe hint into the site the renderer works
// from, checking everything the hint claims against the file that is there.
//
// The checks are the same discipline [siteIndex.siteFor] applies and are here
// for the same reason: a hint that no longer describes the file must produce a
// refusal rather than an edit. The statement has to be a `return`, it has to
// return as many values as the hint spelled types for, and the edit has to sit
// inside the result the hint says it does — because the whole meaning of the
// rewrite is that this temporary holds that value.
func (x *siteIndex) probeSiteFor(m mutation.Mutant, hint *discover.ReturnSite, srcPath string) (probeSite, error) {
	stmt, ok := x.stmts[hint.Span]
	if !ok {
		return probeSite{}, x.notFound(m, srcPath, hint.Span, "no statement covers these bytes")
	}
	ret, ok := stmt.(*ast.ReturnStmt)
	if !ok {
		return probeSite{}, x.unsupported(m, srcPath, hint.Span,
			fmt.Sprintf("a %T is not the return statement its probe hint names", stmt))
	}
	if len(ret.Results) != len(hint.Types) {
		return probeSite{}, x.unsupported(m, srcPath, hint.Span,
			fmt.Sprintf("the statement returns %d values and its probe hint spells %d result types",
				len(ret.Results), len(hint.Types)))
	}
	if hint.Index < 0 || hint.Index >= len(ret.Results) {
		return probeSite{}, x.unsupported(m, srcPath, hint.Span,
			fmt.Sprintf("the probe hint names result %d of a statement with %d", hint.Index, len(ret.Results)))
	}

	operands := make([]mutation.Span, len(ret.Results))
	for i, value := range ret.Results {
		operands[i] = x.span(value)
	}
	if !operands[hint.Index].Contains(m.Span) {
		return probeSite{}, x.notFound(m, srcPath, hint.Span,
			fmt.Sprintf("result %d of it covers %s, which does not hold the edit", hint.Index, operands[hint.Index]))
	}
	return probeSite{span: hint.Span, operands: operands, types: hint.Types}, nil
}

// buildProbeSites arranges one file's probed mutants into the forest of `return`
// statements they occupy, what each of those statements is, and what each mutant
// contributes to it.
//
// Mutants with no probe form are skipped rather than refused: they are still
// catalogued, still mutated in the other tree, and simply not measured here. A
// mutant with no *hint at all* is refused exactly as the mutant tree refuses
// one, because that is not a candidate this phase declined to probe — it is a
// catalogue and a hint index that were built from different discovery passes.
//
// The widest statement in the file comes back with them, because the
// temporaries are named once per file and have to be free for every site in it.
func buildProbeSites(
	index *siteIndex,
	srcPath string,
	mutants []mutation.Mutant,
	hints Hints,
) (interval.Forest[mutation.Mutant], map[mutation.Span]probeSite, map[string]probeEdit, int, error) {
	items := make([]interval.Item[mutation.Mutant], 0, len(mutants))
	sites := make(map[mutation.Span]probeSite, len(mutants))
	edits := make(map[string]probeEdit, len(mutants))
	widest := 0

	fail := func(err error) (interval.Forest[mutation.Mutant], map[mutation.Span]probeSite, map[string]probeEdit, int, error) {
		return interval.Forest[mutation.Mutant]{}, nil, nil, 0, err
	}
	for _, m := range mutants {
		guard, err := hints.guardFor(m, srcPath)
		if err != nil {
			return fail(err)
		}
		edit := probeFor(m, guard)
		if edit == nil {
			continue
		}
		resolved, err := index.probeSiteFor(m, guard.Return, srcPath)
		if err != nil {
			return fail(err)
		}
		if previous, seen := sites[resolved.span]; seen {
			if err := probesAgree(previous, resolved, m, srcPath); err != nil {
				return fail(err)
			}
		}
		sites[resolved.span] = resolved
		edits[m.ID] = *edit
		items = append(items, interval.Item[mutation.Mutant]{Span: resolved.span, Payload: m})
		widest = max(widest, len(resolved.operands))
	}

	forest, err := placeSites(srcPath, items)
	if err != nil {
		return fail(err)
	}
	return forest, sites, edits, widest, nil
}

// probesAgree refuses two hints that name one statement and disagree about what
// it returns.
//
// The operands and their types are properties of the statement rather than of
// the mutant, so two candidates in one `return` must produce the same answer.
// Rendering one rewrite from two contradictory hints would mean declaring a
// temporary of one candidate's type and comparing the other candidate's
// constant against it.
func probesAgree(previous, current probeSite, m mutation.Mutant, srcPath string) error {
	if slicesEqual(previous.operands, current.operands) && stringsEqual(previous.types, current.types) {
		return nil
	}
	return &Error{
		Code: CodeSiteConflict,
		Message: fmt.Sprintf(
			"internal error: %s: the return statement %s is described one way for mutant %s and another for another mutant of the same bytes",
			srcPath, current.span, m.DisplayID),
	}
}

// slicesEqual and stringsEqual compare the two halves of a probe site. They are
// spelled out rather than reached for through generics so that this file
// depends on nothing a byte rewriter would not already have.
func slicesEqual(a, b []mutation.Span) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// probeTemps names the temporaries one file's probe rewrites declare.
//
// The names are derived from the runtime alias, which [aliasIn] already chose to
// be one nothing in the file or its package block spells — but "__gm is free"
// says nothing about "__gm_r0 is free", so each one is checked against the same
// set, and a family with any name taken is abandoned whole rather than one name
// at a time. That matters because the names are used together: a rewrite that
// took __gm_r0 from one family and __gm_r1 from another would still be correct,
// and would be much harder to read in a diff.
//
// The bumped families end in "_" so that no two of them can ever produce one
// name: family 0 is "__gm_r" followed by digits, and family n is "__gm_rn_"
// followed by digits, which the first can never spell.
type probeTemps struct{ prefix string }

// newProbeTemps chooses the family, given how many results the widest `return`
// in the file has.
func newProbeTemps(alias string, taken map[string]bool, widest int) probeTemps {
	for n := 0; ; n++ {
		prefix := alias + "_r"
		if n > 0 {
			prefix = alias + "_r" + strconv.Itoa(n) + "_"
		}
		if familyIsFree(prefix, widest, taken) {
			return probeTemps{prefix: prefix}
		}
	}
}

// familyIsFree reports whether every name a family would use is unbound.
func familyIsFree(prefix string, widest int, taken map[string]bool) bool {
	for i := range widest {
		if taken[prefix+strconv.Itoa(i)] {
			return false
		}
	}
	return true
}

// at is the temporary holding result i.
func (p probeTemps) at(i int) string { return p.prefix + strconv.Itoa(i) }

// A probeRenderer turns one file's probe sites into the rewrite above.
type probeRenderer struct {
	// path is the module-relative path, for diagnostics only.
	path string
	// src is the pristine file, the coordinate system every span is in.
	src []byte
	// alias is the local name this file imports the runtime package under, and
	// so the name Infect is called through.
	alias string
	// temps names the temporaries every site in this file declares.
	temps probeTemps
	// sites is what each node of the forest turned out to be, by site span.
	sites map[mutation.Span]probeSite
	// edits is what each mutant contributes, by mutant id.
	edits map[string]probeEdit
}

// render composes every site of one file, children before parents, exactly as
// the guard renderer does and for the same reason: a `return` inside a function
// literal inside another `return`'s operand has to be rewritten before the
// operand holding it is folded onto a line.
func (r *probeRenderer) render(forest interval.Forest[mutation.Mutant]) ([]Splice, int, error) {
	return renderSites(forest, r.src, r.compose)
}

// compose renders one `return` as its probe.
//
// The whole rewrite is written on one line and the line breaks the statement
// held are appended after the closing brace. That is what keeps every byte
// after the statement on the line it started on, and putting them outside the
// block rather than inside it is deliberate: the block is then exactly one line
// in a diff, followed by the emptied remainder of the statement, which is what
// the rewrite actually did. A newline written between the `return` and the `}`
// would spread the block over the same lines while meaning the same thing, and
// would read as though the rewrite had preserved a structure it has not.
func (r *probeRenderer) compose(node *siteNode, rendered map[*siteNode][]byte) ([]byte, error) {
	s, ok := r.sites[node.Span]
	if !ok {
		return nil, &Error{
			Code: CodeSiteConflict,
			Message: fmt.Sprintf("internal error: %s: the probe site %s was placed in the forest without being resolved",
				r.path, node.Span),
		}
	}
	operands, err := r.operands(node, s, rendered)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	b.WriteByte('{')
	for i, operand := range operands {
		b.WriteString(" var ")
		b.WriteString(r.temps.at(i))
		b.WriteByte(' ')
		b.WriteString(s.types[i])
		b.WriteString(" = ")
		b.Write(operand)
		b.WriteByte(';')
	}
	for _, m := range node.Alternatives {
		edit, known := r.edits[m.ID]
		if !known || edit.result >= len(operands) {
			return nil, &Error{
				Code: CodeSiteConflict,
				Message: fmt.Sprintf("internal error: %s: mutant %s was placed at the probe site %s without a result to report on",
					r.path, m.DisplayID, node.Span),
			}
		}
		fmt.Fprintf(&b, " if %s != %s { %s.Infect(%d) };",
			r.temps.at(edit.result), edit.constant, r.alias, edit.index)
	}
	b.WriteString(" return ")
	for i := range operands {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(r.temps.at(i))
	}
	b.WriteString(" }")

	// Asserted rather than assumed: the operands come back from [Flatten],
	// which proves its own output holds no line break, and everything else
	// written here is an identifier, a spelled type or a constant. A line break
	// reaching this buffer would move every line after the statement.
	if got := CountLines(b.Bytes()); got != 0 {
		return nil, r.lineDrift(fmt.Sprintf("the probe at %s holds %d line breaks before its statement's were added",
			node.Span, got))
	}
	for range CountLines(r.original(node.Span)) {
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

// operands renders each returned value: its own pristine bytes, carrying
// whatever the probe sites nested inside it produced, folded onto one line.
//
// Composing per operand rather than over the whole statement is what makes the
// fold possible at all — the rewrite needs each value on its own, and the
// statement's other bytes (the `return`, the commas) are not reproduced. Every
// nested site lies inside exactly one operand, because a statement nested in a
// `return` can only be inside a function literal and a function literal can
// only be inside one of its values; a child that lands in none of them means
// the forest and this file's syntax have stopped describing each other, and it
// is refused rather than silently dropped.
func (r *probeRenderer) operands(node *siteNode, s probeSite, rendered map[*siteNode][]byte) ([][]byte, error) {
	placed := make([]bool, len(node.Children))
	out := make([][]byte, len(s.operands))

	for i, span := range s.operands {
		splices := make([]Splice, 0, len(node.Children))
		for k, child := range node.Children {
			if !span.Contains(child.Span) {
				continue
			}
			placed[k] = true
			splices = append(splices, Splice{
				Span:        relativeTo(child.Span, span.StartByte),
				Original:    r.original(child.Span),
				Replacement: rendered[child],
			})
		}
		patched, _, err := Apply(r.original(span), splices)
		if err != nil {
			return nil, err
		}
		flat, err := Flatten(patched)
		if err != nil {
			return nil, err
		}
		out[i] = flat
	}

	for k, ok := range placed {
		if ok {
			continue
		}
		return nil, &Error{
			Code: CodeSiteConflict,
			Message: fmt.Sprintf("internal error: %s: the probe site %s encloses %s, which is in none of its returned values",
				r.path, node.Span, node.Children[k].Span),
		}
	}
	return out, nil
}

// original returns the pristine bytes a span covers.
func (r *probeRenderer) original(span mutation.Span) []byte {
	return r.src[span.StartByte:span.EndByte]
}

// lineDrift builds the line-preservation failure.
func (r *probeRenderer) lineDrift(detail string) error {
	return &Error{
		Code:    CodeLineDrift,
		Message: "internal error: instrumenting " + strconv.Quote(r.path) + " would move a line: " + detail,
	}
}
