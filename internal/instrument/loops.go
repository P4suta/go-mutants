// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// A loopSite is one `for` statement of an instrumented file, reduced to what
// the counter rewrite needs: the two offsets it writes at, and where the loop
// is, so that a divergence can name it.
//
// See [ADR 0013]: a mutant that does not return is told apart from a mutant
// that is merely slow by the work it does rather than by a stopwatch, and this
// is where the work is counted.
//
// [ADR 0013]: https://github.com/P4suta/go-mutants/blob/main/docs/adr/0013-a-mutant-that-does-not-return-is-decided-by-work.md
type loopSite struct {
	// declareAt is where the two locals are declared: in front of the loop, or
	// in front of its label when it has one. In front of the *label* matters:
	// written between the label and the `for`, the declaration would be what
	// the label names, and every `break` and `continue` naming that label would
	// stop compiling.
	declareAt uint32
	// testAt is just past the opening brace of the loop's body, which is where
	// the counter is tested — before anything the body does, so that a body
	// that returns on its first statement is still counted.
	testAt uint32
	// Position is the loop's own coordinate, as a report reads it.
	Position string
}

// loopSites finds the loops of one parsed file that can carry a counter, in
// source order.
//
// A function holding a `goto` is refused whole. Go forbids a jump that brings a
// variable into scope which was not in scope at the jump, so a declaration
// spliced in front of a loop that a forward `goto` jumps over is a tree that
// does not compile — and which loops those are is a question about the label's
// position rather than about the loop, so the function is the unit that is
// answered. A `goto` cannot cross a function boundary, so a function literal
// inside a function holding one keeps its own loops.
func loopSites(file *ast.File, tok *token.File, srcPath string) []loopSite {
	var out []loopSite
	for _, body := range functionBodies(file) {
		if holdsGoto(body) {
			continue
		}
		out = append(out, loopsDirectlyIn(body, tok, srcPath)...)
	}
	// Source order, which is the order the sites are numbered in: the index a
	// limit table is read with has to be a function of the file's bytes and not
	// of the order a walk happened to reach two nested functions in.
	slicesSortByOffset(out)
	return out
}

// functionBodies is every function body in a file, the literals inside other
// functions included, in the order a walk reaches them.
func functionBodies(file *ast.File) []*ast.BlockStmt {
	var out []*ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			if node.Body != nil {
				out = append(out, node.Body)
			}
		case *ast.FuncLit:
			if node.Body != nil {
				out = append(out, node.Body)
			}
		}
		return true
	})
	return out
}

// holdsGoto reports whether one function body holds a `goto` of its own.
//
// Function literals inside it are not searched, because a `goto` names a label
// in the function it is written in and cannot reach out of one.
func holdsGoto(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BranchStmt:
			if node.Tok == token.GOTO {
				found = true
			}
		}
		return true
	})
	return found
}

// loopsDirectlyIn is the loops of one function body, the literals inside it
// excluded: each of those is a body of its own with a `goto` answer of its own.
func loopsDirectlyIn(body *ast.BlockStmt, tok *token.File, srcPath string) []loopSite {
	labels := make(map[ast.Stmt]token.Pos)
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.LabeledStmt:
			if loopBody(node.Stmt) != nil {
				labels[node.Stmt] = node.Pos()
			}
		}
		return true
	})

	var out []loopSite
	ast.Inspect(body, func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		stmt, isStmt := n.(ast.Stmt)
		if !isStmt {
			return true
		}
		block := loopBody(stmt)
		if block == nil || !block.Lbrace.IsValid() {
			return true
		}
		at := stmt.Pos()
		if label, ok := labels[stmt]; ok {
			at = label
		}
		out = append(out, loopSite{
			declareAt: uint32(tok.Offset(at)),
			testAt:    uint32(tok.Offset(block.Lbrace)) + 1,
			Position:  srcPath + ":" + strconv.Itoa(tok.Position(stmt.Pos()).Line),
		})
		return true
	})
	return out
}

// loopBody is a `for` statement's body, and nil for anything that is not one.
func loopBody(s ast.Stmt) *ast.BlockStmt {
	switch loop := s.(type) {
	case *ast.ForStmt:
		return loop.Body
	case *ast.RangeStmt:
		return loop.Body
	}
	return nil
}

// slicesSortByOffset orders sites by where their declaration goes, which is
// their order in the file.
func slicesSortByOffset(sites []loopSite) {
	for i := 1; i < len(sites); i++ {
		for j := i; j > 0 && sites[j].declareAt < sites[j-1].declareAt; j-- {
			sites[j], sites[j-1] = sites[j-1], sites[j]
		}
	}
}

// loopSplices renders the counters of one file, numbering them from base.
//
// Two splices per loop and no newline in either, which is what keeps the
// rewrite line-preserving like every other one this package makes:
//
//	__gm_n7, __gm_k7 := uint64(0), __gm.Limit[7]; for i := 0; i < n; i++ { if __gm_n7++; __gm_n7 > __gm_k7 { __gm_k7 = __gm.Over(7, __gm_n7) };
//
// The counter is a local. Nothing is shared between goroutines, so there is no
// atomic, no allocation and nothing for the race detector to find, and what is
// counted is one *entry* to the loop — which is the granularity the question
// is asked at.
//
// [runtimeOver] is what the test calls when the counter passes the ceiling, and
// it is one call rather than two because the two things that can happen there
// are the same shape: a run enforcing a limit never returns from it, and a run
// taking the census records the count and hands back a higher ceiling for the
// local to adopt.
func loopSplices(sites []loopSite, alias string, base uint32) []Splice {
	out := make([]Splice, 0, 2*len(sites))
	for i, site := range sites {
		index := base + uint32(i)
		n, k := counterName(alias, index), ceilingName(alias, index)
		declare := insertSplice(site.declareAt, fmt.Sprintf(
			"%s, %s := uint64(0), %s.%s[%d]; ", n, k, alias, runtimeLimit, index))
		test := insertSplice(site.testAt, fmt.Sprintf(
			" if %s++; %s > %s { %s = %s.%s(%d, %s) };", n, n, k, k, alias, runtimeOver, index, n))
		// Both halves say which loop they are for, because an overlap reported
		// by index alone is what sent somebody reading bytes in a kept snapshot.
		declare.Origin = fmt.Sprintf("the ceiling for the loop at %s", site.Position)
		test.Origin = declare.Origin
		out = append(out, declare, test)
	}
	return out
}

// counterName and ceilingName are the two locals one loop gets.
//
// Both are built from the import alias the file was given, which is the name
// already chosen against everything that file's package binds — so a package
// that has taken `__gm` gets `__gm2_n0` here for the same reason its guards
// read `__gm2.M[0]`, and neither name can shadow anything.
func counterName(alias string, index uint32) string {
	return alias + "_n" + strconv.FormatUint(uint64(index), 10)
}

func ceilingName(alias string, index uint32) string {
	return alias + "_k" + strconv.FormatUint(uint64(index), 10)
}

// insertSplice is an insertion at one offset: an empty span, which [Apply]
// writes the replacement at without covering anything.
func insertSplice(at uint32, text string) Splice {
	return Splice{
		Span:        mutation.Span{StartByte: at, EndByte: at},
		Original:    []byte{},
		Replacement: []byte(text),
	}
}
