// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"bytes"
	"fmt"
	"strconv"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/interval"
	"github.com/P4suta/go-mutants/internal/mutation"
)

type guardRenderer struct {
	path  string
	src   []byte
	alias string
	sites map[mutation.Span]site
	loops []Splice
}

type siteNode = interval.Node[mutation.Mutant]

func (r *guardRenderer) render(forest interval.Forest[mutation.Mutant]) ([]Splice, int, error) {
	return renderSites(forest, r.src, r.compose)
}

func (r *guardRenderer) compose(node *siteNode, rendered map[*siteNode][]byte) ([]byte, error) {
	s, ok := r.sites[node.Span]
	if !ok {
		return nil, &Error{
			Code: CodeSiteConflict,
			Message: fmt.Sprintf("internal error: %s: the rewrite site %s was placed in the forest without being resolved",
				r.path, node.Span),
		}
	}
	base := r.original(node.Span)

	splices := make([]Splice, 0, len(node.Children)+len(s.undeclare)+len(r.loops))
	for _, child := range node.Children {
		splices = append(splices, Splice{
			Span:        relativeTo(child.Span, node.Span.StartByte),
			Original:    r.original(child.Span),
			Replacement: rendered[child],
			Origin:      "the rewrite site at " + child.Span.String(),
		})
	}
	splices = append(splices, s.undeclare...)
	splices = append(splices, r.loopsWithin(node.Span, node.Children)...)
	if !LinePreserving(splices) {
		return nil, r.lineDrift("folding nested guards into the site at " + node.Span.String())
	}
	orig, _, err := Apply(base, splices)
	if err != nil {
		return nil, err
	}
	return r.guard(node, s, orig)
}

func (r *guardRenderer) guard(node *siteNode, s site, orig []byte) ([]byte, error) {
	var b bytes.Buffer
	var err error
	switch s.form {
	case discover.GuardFormC:
		err = r.selector(&b, node, s, orig, "")
	case discover.GuardFormCPrime:
		err = r.selector(&b, node, s, orig, s.siteType)
	case discover.GuardFormS:
		err = r.chain(&b, node, s, orig)
	case discover.GuardFormE:
		err = r.returningClosure(&b, node, s, orig)
	case discover.GuardFormF:
		b.WriteString("func() { ")
		if err = r.chain(&b, node, s, orig); err == nil {
			b.WriteString(" }()")
		}
	case discover.GuardFormD:
		r.declarations(&b, s)
		err = r.chain(&b, node, s, orig)
	default:
		err = &Error{
			Code:    CodeUnsupportedGuard,
			Message: r.path + ": the site at " + node.Span.String() + " has no guard form",
		}
	}
	if err != nil {
		return nil, err
	}

	if got, want := CountLines(b.Bytes()), CountLines(orig); got != want {
		return nil, r.lineDrift(fmt.Sprintf("the guard at %s spans %d lines, its site spans %d",
			node.Span, got+1, want+1))
	}
	return b.Bytes(), nil
}

func (r *guardRenderer) selector(b *bytes.Buffer, node *siteNode, s site, orig []byte, convertTo string) error {
	opening := "("
	if convertTo != "" {
		opening = convertTo + "("
	}
	b.WriteString(opening)
	for _, m := range node.Alternatives {
		mutated, err := r.mutated(s, m)
		if err != nil {
			return err
		}
		b.WriteString(r.flag(m))
		b.WriteString(" && ")
		writeOperand(b, mutated, convertTo != "")
		b.WriteString(" || ")
	}
	b.WriteString("!(")
	for i, m := range node.Alternatives {
		if i > 0 {
			b.WriteString(" || ")
		}
		b.WriteString(r.flag(m))
	}
	b.WriteString(") && ")
	writeOperand(b, orig, convertTo != "")
	b.WriteByte(')')
	return nil
}

func writeOperand(b *bytes.Buffer, text []byte, convert bool) {
	if convert {
		b.WriteString("bool(")
	} else {
		b.WriteByte('(')
	}
	b.Write(text)
	b.WriteByte(')')
}

func (r *guardRenderer) chain(b *bytes.Buffer, node *siteNode, s site, orig []byte) error {
	for i, m := range node.Alternatives {
		if i > 0 {
			b.WriteString(" else ")
		}
		b.WriteString("if ")
		b.WriteString(r.flag(m))
		b.WriteByte(' ')
		mutated, err := r.mutated(s, m)
		if err != nil {
			return err
		}
		writeBranch(b, mutated)
	}
	b.WriteString(" else ")
	writeBranch(b, orig)
	return nil
}

func (r *guardRenderer) returningClosure(b *bytes.Buffer, node *siteNode, s site, orig []byte) error {
	b.WriteString("func() ")
	b.WriteString(s.siteType)
	b.WriteString(" { ")
	for i, m := range node.Alternatives {
		if i > 0 {
			b.WriteString(" else ")
		}
		b.WriteString("if ")
		b.WriteString(r.flag(m))
		b.WriteString(" { return ")
		mutated, err := r.mutated(s, m)
		if err != nil {
			return err
		}
		b.Write(mutated)
		b.WriteString(" }")
	}
	b.WriteString(" else { return ")
	b.Write(orig)
	b.WriteString(" } }()")
	return nil
}

func (r *guardRenderer) declarations(b *bytes.Buffer, s site) {
	for _, declared := range s.declare {
		b.WriteString("var ")
		b.WriteString(declared.Name)
		b.WriteByte(' ')
		b.WriteString(declared.Type)
		b.WriteString("; ")
	}
}

func writeBranch(b *bytes.Buffer, body []byte) {
	if len(body) == 0 {
		b.WriteString("{ }")
		return
	}
	b.WriteString("{ ")
	b.Write(body)
	b.WriteString(" }")
}

func (r *guardRenderer) flag(m mutation.Mutant) string {
	return r.alias + ".M[" + strconv.FormatUint(uint64(m.Index), 10) + "]"
}

func (r *guardRenderer) mutated(s site, m mutation.Mutant) ([]byte, error) {
	if !s.span.Contains(m.Span) {
		return nil, &Error{
			Code: CodeSiteConflict,
			Message: fmt.Sprintf("%s: mutant %s at %s is not inside its own site %s",
				r.path, m.DisplayID, m.Span, s.span),
		}
	}
	splices := make([]Splice, 0, 1+len(s.undeclare)+len(r.loops))
	splices = append(splices, Splice{
		Span:        relativeTo(m.Span, s.span.StartByte),
		Original:    []byte(m.Original),
		Replacement: []byte(m.Replacement),
		Origin:      "the mutant " + m.DisplayID,
	})
	splices = append(splices, s.undeclare...)
	splices = append(splices, loopsOutside(r.loopsWithin(s.span, nil), m.Span, s.span.StartByte)...)

	patched, _, err := Apply(r.original(s.span), splices)
	if err != nil {
		return nil, err
	}
	return Flatten(patched)
}

func (r *guardRenderer) original(span mutation.Span) []byte {
	return r.src[span.StartByte:span.EndByte]
}

func (r *guardRenderer) lineDrift(detail string) error {
	return &Error{
		Code:    CodeLineDrift,
		Message: "internal error: instrumenting " + strconv.Quote(r.path) + " would move a line: " + detail,
	}
}

func (r *guardRenderer) loopsWithin(span mutation.Span, children []*siteNode) []Splice {
	if len(r.loops) == 0 {
		return nil
	}
	out := make([]Splice, 0, len(r.loops))
	for _, loop := range r.loops {
		at := loop.Span.StartByte
		if at < span.StartByte || at > span.EndByte {
			continue
		}
		nested := false
		for _, child := range children {
			if at > child.Span.StartByte && at < child.Span.EndByte {
				nested = true
				break
			}
		}
		if nested {
			continue
		}
		out = append(out, Splice{
			Span:        relativeTo(loop.Span, span.StartByte),
			Original:    loop.Original,
			Replacement: loop.Replacement,
			Origin:      loop.Origin,
		})
	}
	return out
}

func loopsOutside(relative []Splice, cut mutation.Span, base uint32) []Splice {
	out := relative[:0:0]
	for _, loop := range relative {
		at := loop.Span.StartByte + base
		if at > cut.StartByte && at < cut.EndByte {
			continue
		}
		if cut.Len() == 0 && at == cut.StartByte {
			continue
		}
		out = append(out, loop)
	}
	return out
}
