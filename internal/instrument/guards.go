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

// A guardRenderer turns one file's rewrite sites into guards.
type guardRenderer struct {
	// path is the module-relative path, for diagnostics only.
	path string
	// src is the pristine file, the coordinate system every span in the forest
	// is expressed in.
	src []byte
	// alias is the local name the runtime package is imported under in this
	// file, and the guards written into it have to spell whatever that turned
	// out to be. It varies from file to file because "__gm" may already be
	// taken — by something this file spells, or by something the package block
	// binds anywhere in the package — in which case [aliasFor] bumps it.
	alias string
	// sites is what each node of the forest turned out to be, by site span.
	sites map[mutation.Span]site
	// loops is every counted-loop insertion in this file, in the file's own
	// coordinates, and it is here because a loop can be *inside* a rewrite
	// site.
	//
	// That is an ordinary shape -- `return each(func() error { for ... } })`
	// puts one there -- and a file-level insertion into bytes a guard replaces
	// is an overlap, which is what GOM7312 said when the whole file then
	// refused to instrument. A loop inside a site belongs to that site's text,
	// exactly as a nested guard does, so it is folded in here and never
	// reaches the file's own splice list.
	loops []Splice
}

// A siteNode is one node of the rewrite forest for a file.
type siteNode = interval.Node[mutation.Mutant]

// render composes every guard of one file through [renderSites], which settles
// the order and the splices for both trees. The count that comes back is the
// file's guard count: one per site, nested sites included, since several
// mutants of one site are alternatives inside a single guard.
func (r *guardRenderer) render(forest interval.Forest[mutation.Mutant]) ([]Splice, int, error) {
	return renderSites(forest, r.src, r.compose)
}

// compose renders one node: its children are folded into its original text,
// a declaration is turned into assignments in the same pass, and the guard is
// wrapped around the result.
//
// The two kinds of splice belong in one [Apply] because both are expressed
// against the same pristine bytes. A Form D site's cuts sit in its declaring
// tokens and a child guard sits in an expression inside it, so the two can
// never overlap; if they somehow did, Apply says so rather than producing bytes
// whose meaning depends on which was applied first.
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
	// The loops this site holds directly. A loop inside a child is already in
	// the bytes that child rendered -- it was folded in when the child was
	// composed, which is what makes one pass over the forest enough.
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

// guard renders one site in whichever form its hint chose.
//
// Whatever the form, every byte written before the original is on the
// original's first line and every byte written after it is on its last: the
// prefixes hold no line break, and each mutated copy is folded onto one line by
// [Flatten]. That is what keeps a rewritten multi-line site line-preserving,
// and it is asserted here rather than assumed.
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
		// The closure and its call are written around exactly the chain Form S
		// writes, which is what keeps the two forms one renderer: what differs
		// is the slot the result is legal in, not the guard inside it.
		b.WriteString("func() { ")
		if err = r.chain(&b, node, s, orig); err == nil {
			b.WriteString(" }()")
		}
	case discover.GuardFormD:
		r.declarations(&b, s)
		err = r.chain(&b, node, s, orig)
	default:
		// Unreachable: siteFor refuses any other form before a site exists.
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

// selector renders the Form C guard: a bool expression that picks one branch.
//
// The shape, for alternatives i1..in with mutated renderings m1..mn and the
// site's current text ORIG, is
//
//	(A.M[i1] && (m1) || … || !(A.M[i1] || … || A.M[in]) && (ORIG))
//
// where A is this file's alias for the runtime package. Both branches are
// ordinary expressions in the site's own type context, so the compiler settles
// typing, evaluation order, and short-circuiting; exactly one of them is ever
// evaluated, and with every flag false that one is ORIG, byte for byte the
// source the user wrote.
//
// Form C' is the same selector with a conversion at each end, for a site whose
// type is a named boolean rather than the universe one:
//
//	T(A.M[i1] && bool(m1) || … || !(…) && bool(ORIG))
//
// The selector itself is an untyped boolean expression either way; what changes
// is that where the site's type is not `bool`, the expression has to be
// converted back to it, and each operand has to be converted *to* `bool` first
// because `&&` and `||` need operands of one boolean type. Both conversions are
// between a defined type and its underlying type, which is always legal.
//
// Nothing about evaluation changes. A conversion of a boolean expression
// evaluates that expression and nothing else, so the short-circuiting, the
// order, and the "exactly one operand is evaluated" property are the selector's
// as before.
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

// writeOperand writes one operand of a selector, in parentheses, and converted
// to `bool` when the site's own type is not.
//
// `bool(x)` rather than `(x)`: `&&` and `||` require both operands to have one
// boolean type, and a named boolean and an untyped constant do not mix the way
// two untyped constants do. Converting each operand rather than relying on
// assignability is what keeps the shape the same for every alternative,
// whatever the mutated text turned out to be.
func writeOperand(b *bytes.Buffer, text []byte, convert bool) {
	if convert {
		b.WriteString("bool(")
	} else {
		b.WriteByte('(')
	}
	b.Write(text)
	b.WriteByte(')')
}

// chain renders the branch chain both statement forms share:
//
//	if A.M[i1] { m1 } else if A.M[i2] { m2 } else { ORIG }
//
// Exactly one branch runs, and with every flag false it is ORIG — the
// statement's own bytes, interior line breaks and all, carrying whatever guards
// the sites nested inside it produced. A mutant whose replacement deletes the
// statement renders as an empty branch, `if A.M[i] { }`, which is the whole of
// what "this statement does not run" means.
//
// A guarded `return` still compiles where the function needed one. Go's rule
// for a terminating statement covers an `if` with an `else` whose every branch
// terminates, so a chain of returns is itself a terminating statement and the
// function does not lose its final one. That is why the `else` is always
// written, even for a single alternative.
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

// returningClosure renders the Form E guard: a closure that returns the site's
// own type, called where the expression stood.
//
//	func() T { if A.M[i1] { return m1 } else { return ORIG } }()
//
// It is the branch chain with every branch returning rather than executing, and
// the `else` is always written for the reason [guardRenderer.chain] gives: a
// function whose body is an `if` chain needs every branch to terminate, or the
// closing brace is reachable without a return.
//
// The closure is written *where the expression was*, which is the whole of what
// makes this form sound. A call is evaluated where it is written, so the
// expression is evaluated in the same order and the same number of times; every
// name in scope at the expression is in scope inside the closure; and no
// identifier is invented, so nothing can collide.
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

// declarations writes the `var` statements a Form D guard hoists out in front
// of itself, one per name the site declared.
//
// This is the whole reason the form exists. A `x := f()` buried in a block
// would take x out of scope for everything after it, so the declaration is
// lifted out and both branches of the guard assign to it instead. The types
// come from the hint, spelled as the file itself may write them; discovery
// refused the candidate outright if it could not spell one.
//
// Hoisting moves the declaration in front of its own initialiser, and Go's
// scoping rule is what makes that a question rather than a formality: a
// declared name's scope begins at the *end* of its specification, so
// `total := total * 2` reads the enclosing `total` and the hoisted form would
// read the one being declared. Discovery refuses such a site outright, for the
// reason its own documentation gives — the rebound program usually still
// compiles, and computes something else. What is left here is safe by that
// refusal rather than by construction: the same names are declared in the same
// block, every remaining use still refers to what it did, and Go counts an
// assignment as a use in neither form, so a program that compiled before
// compiles now.
func (r *guardRenderer) declarations(b *bytes.Buffer, s site) {
	for _, declared := range s.declare {
		b.WriteString("var ")
		b.WriteString(declared.Name)
		b.WriteByte(' ')
		b.WriteString(declared.Type)
		b.WriteString("; ")
	}
}

// writeBranch writes one brace-delimited branch body.
func writeBranch(b *bytes.Buffer, body []byte) {
	if len(body) == 0 {
		b.WriteString("{ }")
		return
	}
	b.WriteString("{ ")
	b.Write(body)
	b.WriteString(" }")
}

// flag renders one mutant's activation lookup, "A.M[7]".
func (r *guardRenderer) flag(m mutation.Mutant) string {
	return r.alias + ".M[" + strconv.FormatUint(uint64(m.Index), 10) + "]"
}

// mutated renders one alternative: the site as it reads with that single
// candidate's edit applied, folded onto one line.
//
// It is rendered from the pristine bytes and never from the site's current
// text. A mutant is one edit to the program the user wrote, so the copy that
// runs when its flag is set must not carry the guards of the sites nested
// inside it — those would make it a different mutant, and one whose identity
// nothing in the catalogue describes. A Form D site's cuts are applied here as
// well as to the original branch, because both branches have to be assignments
// to the names the guard declared.
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
	// Every loop this copy still holds is counted in it too. A mutant is one
	// edit to the program the user wrote, and a loop the edit did not touch is
	// part of that program -- if anything the ceiling matters more here, since
	// the mutant is the reason a loop that terminated might not. The ones
	// inside the edit are dropped rather than counted: those bytes are gone,
	// replaced by whatever the mutant writes, and a counter for a loop that is
	// no longer there would not compile.
	splices = append(splices, loopsOutside(r.loopsWithin(s.span, nil), m.Span, s.span.StartByte)...)

	patched, _, err := Apply(r.original(s.span), splices)
	if err != nil {
		return nil, err
	}
	return Flatten(patched)
}

// original returns the pristine bytes a span covers. The span came out of the
// forest, which was built from spans this package checked against these very
// bytes, so it fits by construction.
func (r *guardRenderer) original(span mutation.Span) []byte {
	return r.src[span.StartByte:span.EndByte]
}

// lineDrift builds the line-preservation failure.
func (r *guardRenderer) lineDrift(detail string) error {
	return &Error{
		Code:    CodeLineDrift,
		Message: "internal error: instrumenting " + strconv.Quote(r.path) + " would move a line: " + detail,
	}
}

// loopsWithin is the loop insertions that belong to one site's own text: the
// ones inside its span, less the ones any child already carries, expressed
// relative to the site.
//
// Containment is by insertion point, because every one of these is an empty
// span. A point exactly at a child's start is the parent's -- it writes in
// front of the child rather than into it -- and one strictly inside the child
// is the child's.
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

// loopsOutside drops the insertions that fall inside one mutant's edit, which
// is bytes the mutated copy does not have. The spans handed in are already
// relative to base; cut is not.
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
