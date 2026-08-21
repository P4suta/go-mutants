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
}

// A siteNode is one node of the rewrite forest for a file.
type siteNode = interval.Node[mutation.Mutant]

// render composes every site of one file, children before parents, and returns
// the splices to apply to the pristine bytes.
//
// The composition is bottom-up in parent-relative coordinates: a site's
// original text is its own pristine bytes with each child's finished guard
// spliced in, and only the outermost sites are ever spliced against the file
// itself. [interval.Forest.InnerFirst] supplies the order that makes this
// possible; the [OffsetMap] each nested [Apply] returns is deliberately unused,
// because composing in a child's parent-relative coordinates is the same
// arithmetic done by construction rather than by lookup, and it never leaves
// the file's own coordinate system to begin with.
// The second return value is the number of guards written: one per site,
// nested sites included, which is what a file's guard count means. Several
// mutants of one site are alternatives inside a single guard.
func (r *guardRenderer) render(forest interval.Forest[mutation.Mutant]) ([]Splice, int, error) {
	rendered := make(map[*siteNode][]byte)
	var failure error
	forest.InnerFirst(func(node *siteNode) {
		if failure != nil {
			return
		}
		text, err := r.compose(node, rendered)
		if err != nil {
			failure = err
			return
		}
		rendered[node] = text
	})
	if failure != nil {
		return nil, 0, failure
	}

	roots := forest.Roots()
	splices := make([]Splice, 0, len(roots))
	for _, root := range roots {
		splices = append(splices, Splice{
			Span:        root.Span,
			Original:    r.original(root.Span),
			Replacement: rendered[root],
		})
	}
	return splices, len(rendered), nil
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

	splices := make([]Splice, 0, len(node.Children)+len(s.undeclare))
	for _, child := range node.Children {
		splices = append(splices, Splice{
			Span:        relativeTo(child.Span, node.Span.StartByte),
			Original:    r.original(child.Span),
			Replacement: rendered[child],
		})
	}
	splices = append(splices, s.undeclare...)
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
		err = r.selector(&b, node, s, orig)
	case discover.GuardFormS:
		err = r.chain(&b, node, s, orig)
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
func (r *guardRenderer) selector(b *bytes.Buffer, node *siteNode, s site, orig []byte) error {
	b.WriteByte('(')
	for _, m := range node.Alternatives {
		mutated, err := r.mutated(s, m)
		if err != nil {
			return err
		}
		b.WriteString(r.flag(m))
		b.WriteString(" && (")
		b.Write(mutated)
		b.WriteString(") || ")
	}
	b.WriteString("!(")
	for i, m := range node.Alternatives {
		if i > 0 {
			b.WriteString(" || ")
		}
		b.WriteString(r.flag(m))
	}
	b.WriteString(") && (")
	b.Write(orig)
	b.WriteString("))")
	return nil
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
	splices := make([]Splice, 0, 1+len(s.undeclare))
	splices = append(splices, Splice{
		Span:        relativeTo(m.Span, s.span.StartByte),
		Original:    []byte(m.Original),
		Replacement: []byte(m.Replacement),
	})
	splices = append(splices, s.undeclare...)

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
