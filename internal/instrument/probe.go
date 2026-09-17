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

var probeConstants = map[string]bool{
	"0":     true,
	`""`:    true,
	"true":  true,
	"false": true,
	"nil":   true,
}

type probeEdit struct {
	index    uint32
	result   int
	constant string
}

func probeFor(m mutation.Mutant, guard discover.Guard) *probeEdit {
	site := guard.Probe
	if site == nil {
		return nil
	}
	switch site.Form {
	case discover.ProbeFormReturn:
		if !probeConstants[m.Replacement] {
			return nil
		}
		return &probeEdit{index: m.Index, result: site.Index, constant: m.Replacement}
	case discover.ProbeFormBool, discover.ProbeFormValue, discover.ProbeFormReach:
		return &probeEdit{index: m.Index}
	default:
		return nil
	}
}

func (h Hints) Probes(m mutation.Mutant) bool {
	guard, ok := h[m.ID]
	if !ok {
		return false
	}
	return probeFor(m, guard) != nil
}

type probeSite struct {
	form     discover.ProbeForm
	span     mutation.Span
	operands []mutation.Span
	types    []string
}

func (x *siteIndex) probeSiteFor(m mutation.Mutant, hint *discover.ProbeSite, srcPath string) (probeSite, error) {
	switch hint.Form {
	case discover.ProbeFormBool, discover.ProbeFormValue:
		return x.expressionSiteFor(m, hint, srcPath)
	case discover.ProbeFormReach:
		return x.reachSiteFor(m, hint, srcPath)
	case discover.ProbeFormReturn:
	}
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
	return probeSite{form: hint.Form, span: hint.Span, operands: operands, types: hint.Types}, nil
}

func (x *siteIndex) expressionSiteFor(
	m mutation.Mutant, hint *discover.ProbeSite, srcPath string,
) (probeSite, error) {
	if _, ok := x.exprs[hint.Span]; !ok {
		return probeSite{}, x.notFound(m, srcPath, hint.Span, "no expression covers these bytes")
	}
	if !hint.Span.Contains(m.Span) {
		return probeSite{}, x.notFound(m, srcPath, hint.Span, "the edit is not inside it")
	}
	if hint.Form == discover.ProbeFormValue && len(hint.Types) != 1 {
		return probeSite{}, x.unsupported(m, srcPath, hint.Span,
			fmt.Sprintf("a value probe hint spells %d types and its closure writes exactly one", len(hint.Types)))
	}
	return probeSite{form: hint.Form, span: hint.Span, types: hint.Types}, nil
}

func (x *siteIndex) reachSiteFor(
	m mutation.Mutant, hint *discover.ProbeSite, srcPath string,
) (probeSite, error) {
	if _, ok := x.stmts[hint.Span]; !ok {
		return probeSite{}, x.notFound(m, srcPath, hint.Span, "no statement covers these bytes")
	}
	if !hint.Span.Contains(m.Span) {
		return probeSite{}, x.notFound(m, srcPath, hint.Span, "the edit is not inside it")
	}
	return probeSite{form: hint.Form, span: hint.Span}, nil
}

func buildProbeSites(
	index *siteIndex,
	srcPath string,
	mutants []mutation.Mutant,
	hints Hints,
) (
	interval.Forest[mutation.Mutant], map[mutation.Span]probeSite, map[string]probeEdit,
	int, []discover.Completion, error,
) {
	items := make([]interval.Item[mutation.Mutant], 0, len(mutants))
	sites := make(map[mutation.Span]probeSite, len(mutants))
	edits := make(map[string]probeEdit, len(mutants))
	widest := 0
	var completions []discover.Completion

	fail := func(err error) (
		interval.Forest[mutation.Mutant], map[mutation.Span]probeSite, map[string]probeEdit,
		int, []discover.Completion, error,
	) {
		return interval.Forest[mutation.Mutant]{}, nil, nil, 0, nil, err
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
		resolved, err := index.probeSiteFor(m, guard.Probe, srcPath)
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
		completions = discover.MergeCompletions(completions, guard.Probe.Imports)
		items = append(items, interval.Item[mutation.Mutant]{Span: resolved.span, Payload: m})
		widest = max(widest, len(resolved.operands))
	}

	forest, err := placeSites(srcPath, items)
	if err != nil {
		return fail(err)
	}
	return forest, sites, edits, widest, completions, nil
}

func probesAgree(previous, current probeSite, m mutation.Mutant, srcPath string) error {
	if previous.form == current.form &&
		slicesEqual(previous.operands, current.operands) &&
		stringsEqual(previous.types, current.types) {
		return nil
	}
	return &Error{
		Code: CodeSiteConflict,
		Message: fmt.Sprintf(
			"internal error: %s: the return statement %s is described one way for mutant %s and another for another mutant of the same bytes",
			srcPath, current.span, m.DisplayID),
	}
}

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

type probeTemps struct{ prefix string }

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

func familyIsFree(prefix string, widest int, taken map[string]bool) bool {
	for i := range widest {
		if taken[prefix+strconv.Itoa(i)] {
			return false
		}
	}
	return true
}

func (p probeTemps) at(i int) string { return p.prefix + strconv.Itoa(i) }

type probeRenderer struct {
	path  string
	src   []byte
	alias string
	temps probeTemps
	sites map[mutation.Span]probeSite
	edits map[string]probeEdit
}

func (r *probeRenderer) render(forest interval.Forest[mutation.Mutant]) ([]Splice, int, error) {
	return renderSites(forest, r.src, r.compose)
}

func (r *probeRenderer) compose(node *siteNode, rendered map[*siteNode][]byte) ([]byte, error) {
	s, ok := r.sites[node.Span]
	if !ok {
		return nil, &Error{
			Code: CodeSiteConflict,
			Message: fmt.Sprintf("internal error: %s: the probe site %s was placed in the forest without being resolved",
				r.path, node.Span),
		}
	}
	switch s.form {
	case discover.ProbeFormBool:
		return r.composeBool(node, s, rendered)
	case discover.ProbeFormValue:
		return r.composeValue(node, s, rendered)
	case discover.ProbeFormReach:
		return r.composeReach(node, s, rendered)
	case discover.ProbeFormReturn:
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

	if got := CountLines(b.Bytes()); got != 0 {
		return nil, r.lineDrift(fmt.Sprintf("the probe at %s holds %d line breaks before its statement's were added",
			node.Span, got))
	}
	for range CountLines(r.original(node.Span)) {
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

func (r *probeRenderer) composeBool(
	node *siteNode, s probeSite, rendered map[*siteNode][]byte,
) ([]byte, error) {
	orig, err := r.withChildren(node, rendered)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	b.WriteByte('(')
	b.Write(orig)
	b.WriteByte(')')
	for _, m := range node.Alternatives {
		edit, known := r.edits[m.ID]
		if !known {
			return nil, &Error{
				Code: CodeSiteConflict,
				Message: fmt.Sprintf("internal error: %s: mutant %s was placed at the probe site %s without an edit",
					r.path, m.DisplayID, node.Span),
			}
		}
		mutated, mutErr := r.mutated(s.span, m)
		if mutErr != nil {
			return nil, mutErr
		}
		var wrapped bytes.Buffer
		fmt.Fprintf(&wrapped, "%s.Differs(%d, ", r.alias, edit.index)
		wrapped.Write(b.Bytes())
		wrapped.WriteString(", (")
		wrapped.Write(mutated)
		wrapped.WriteString("))")
		b = wrapped
	}

	if got, want := CountLines(b.Bytes()), CountLines(r.original(s.span)); got != want {
		return nil, &Error{
			Code: CodeLineDrift,
			Message: fmt.Sprintf(
				"internal error: instrumenting %s would move a line: the probe at %s spans %d lines, its site spans %d",
				strconv.Quote(r.path), node.Span, got+1, want+1),
		}
	}
	return b.Bytes(), nil
}

func (r *probeRenderer) composeValue(
	node *siteNode, s probeSite, rendered map[*siteNode][]byte,
) ([]byte, error) {
	orig, err := r.withChildren(node, rendered)
	if err != nil {
		return nil, err
	}
	temp := r.temps.at(0)

	var b bytes.Buffer
	fmt.Fprintf(&b, "func() %s { var %s %s = (", s.types[0], temp, s.types[0])
	b.Write(orig)
	b.WriteString(");")
	for _, m := range node.Alternatives {
		edit, known := r.edits[m.ID]
		if !known {
			return nil, &Error{
				Code: CodeSiteConflict,
				Message: fmt.Sprintf("internal error: %s: mutant %s was placed at the probe site %s without an edit",
					r.path, m.DisplayID, node.Span),
			}
		}
		mutated, mutErr := r.mutated(s.span, m)
		if mutErr != nil {
			return nil, mutErr
		}
		fmt.Fprintf(&b, " if %s != (", temp)
		b.Write(mutated)
		fmt.Fprintf(&b, ") { %s.Infect(%d) };", r.alias, edit.index)
	}
	fmt.Fprintf(&b, " return %s }()", temp)

	if got, want := CountLines(b.Bytes()), CountLines(r.original(s.span)); got != want {
		return nil, &Error{
			Code: CodeLineDrift,
			Message: fmt.Sprintf(
				"internal error: instrumenting %s would move a line: the probe at %s spans %d lines, its site spans %d",
				strconv.Quote(r.path), node.Span, got+1, want+1),
		}
	}
	return b.Bytes(), nil
}

func (r *probeRenderer) composeReach(
	node *siteNode, s probeSite, rendered map[*siteNode][]byte,
) ([]byte, error) {
	orig, err := r.withChildren(node, rendered)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	b.WriteByte('{')
	for _, m := range node.Alternatives {
		edit, known := r.edits[m.ID]
		if !known {
			return nil, &Error{
				Code: CodeSiteConflict,
				Message: fmt.Sprintf("internal error: %s: mutant %s was placed at the probe site %s without an edit",
					r.path, m.DisplayID, node.Span),
			}
		}
		fmt.Fprintf(&b, " %s.Infect(%d);", r.alias, edit.index)
	}
	b.WriteByte(' ')
	b.Write(orig)
	b.WriteString(" }")

	if got, want := CountLines(b.Bytes()), CountLines(r.original(s.span)); got != want {
		return nil, &Error{
			Code: CodeLineDrift,
			Message: fmt.Sprintf(
				"internal error: instrumenting %s would move a line: the probe at %s spans %d lines, its site spans %d",
				strconv.Quote(r.path), node.Span, got+1, want+1),
		}
	}
	return b.Bytes(), nil
}

func (r *probeRenderer) withChildren(node *siteNode, rendered map[*siteNode][]byte) ([]byte, error) {
	splices := make([]Splice, 0, len(node.Children))
	for _, child := range node.Children {
		splices = append(splices, Splice{
			Span:        relativeTo(child.Span, node.Span.StartByte),
			Original:    r.original(child.Span),
			Replacement: rendered[child],
		})
	}
	patched, _, err := Apply(r.original(node.Span), splices)
	return patched, err
}

func (r *probeRenderer) mutated(span mutation.Span, m mutation.Mutant) ([]byte, error) {
	if !span.Contains(m.Span) {
		return nil, &Error{
			Code: CodeSiteConflict,
			Message: fmt.Sprintf("%s: mutant %s at %s is not inside its own probe site %s",
				r.path, m.DisplayID, m.Span, span),
		}
	}
	patched, _, err := Apply(r.original(span), []Splice{{
		Span:        relativeTo(m.Span, span.StartByte),
		Original:    []byte(m.Original),
		Replacement: []byte(m.Replacement),
	}})
	if err != nil {
		return nil, err
	}
	return Flatten(patched)
}

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

func (r *probeRenderer) original(span mutation.Span) []byte {
	return r.src[span.StartByte:span.EndByte]
}

func (r *probeRenderer) lineDrift(detail string) error {
	return &Error{
		Code:    CodeLineDrift,
		Message: "internal error: instrumenting " + strconv.Quote(r.path) + " would move a line: " + detail,
	}
}
