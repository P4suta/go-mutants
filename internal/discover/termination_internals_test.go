// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// The readers a termination proof is built out of, asked as questions about
// loops.
//
// termination_test.go drives the whole phase over fixtures and asserts which
// mutants are proved to run away. That is the contract, and it is the right
// test for it. It is the wrong test for these: every refusal here is a loop
// shape the proof declines to reason about, and a fixture holds only the shapes
// somebody thought to write. A reader that admitted one shape too many would
// publish `unbounded` for a loop that terminates -- a claim about somebody's
// code, published in two JSON documents -- and the way to keep that from
// happening is to state every refusal.

// loopIn parses a function body and returns the `for` statement in it.
//
// Nothing here consults the type checker: an induction loop is recognised from
// the syntax, which is the whole point of deciding termination before anything
// runs. So the fixture is parsed and not checked, and may name anything.
func loopIn(t *testing.T, body string) *ast.ForStmt {
	t.Helper()

	file := parseProbe(t, "package pkg\n\nfunc probe() {\n"+body+"\n}\n")
	var found *ast.ForStmt
	ast.Inspect(file, func(node ast.Node) bool {
		if loop, isLoop := node.(*ast.ForStmt); isLoop && found == nil {
			found = loop
		}
		return found == nil
	})
	if found == nil {
		t.Fatalf("the fixture holds no for statement:\n%s", body)
	}
	return found
}

// TestWhichLoopsHaveAMeasureThisPhaseCanRead is [readInductionLoop], which is
// the gate every proof passes through.
//
// The shape it recognises is `for …; v OP bound; v STEP`, and each of the five
// ways a loop can fail to be one is a different fact: no step at all, a step
// that does not move, a condition that is not an ordering of the variable
// against something else, a body that moves the variable itself, and a bound
// the body assigns. A loop it declines yields no proof, which is the silence
// the phase is designed around.
func TestWhichLoopsHaveAMeasureThisPhaseCanRead(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name       string
		body       string
		want       bool
		variable   string
		step       int
		comparison token.Token
	}{
		{
			name: "counting up", body: "\tfor i := 0; i < n; i++ {\n\t}",
			want: true, variable: "i", step: 1, comparison: token.LSS,
		},
		{
			name: "counting down", body: "\tfor i := n; i > 0; i-- {\n\t}",
			want: true, variable: "i", step: -1, comparison: token.GTR,
		},
		{
			name: "striding", body: "\tfor i := 0; i <= n; i += 2 {\n\t}",
			want: true, variable: "i", step: 2, comparison: token.LEQ,
		},
		{
			name: "striding backwards", body: "\tfor i := n; i >= 0; i -= 3 {\n\t}",
			want: true, variable: "i", step: -3, comparison: token.GEQ,
		},
		{
			// The bound written first. It is normalised so that the variable is
			// always on the left, which is what lets one rule decide the
			// direction rather than two.
			name: "the bound on the left", body: "\tfor i := 0; n > i; i++ {\n\t}",
			want: true, variable: "i", step: 1, comparison: token.LSS,
		},
		{
			name: "the bound on the left counting down", body: "\tfor i := n; 0 < i; i-- {\n\t}",
			want: true, variable: "i", step: -1, comparison: token.GTR,
		},
		{
			// A bound that is an expression rather than a name, which is fine as
			// long as the body does not move what it is made of.
			name: "a computed bound", body: "\tfor i := 0; i < len(xs)-1; i++ {\n\t}",
			want: true, variable: "i", step: 1, comparison: token.LSS,
		},

		{name: "a bare loop", body: "\tfor {\n\t}"},
		{name: "a loop with a condition and no step", body: "\tfor i < n {\n\t}"},
		{name: "a range loop's desugaring has no post", body: "\tfor ; i < n; {\n\t}"},
		{name: "a step of zero", body: "\tfor i := 0; i < n; i += 0 {\n\t}"},
		{name: "a step that is not a literal", body: "\tfor i := 0; i < n; i += k {\n\t}"},
		{
			// A literal the parser accepts and strconv will not. This phase
			// reads syntax before anything has type-checked it, so a step of
			// more than nine quintillion arrives here as an ordinary
			// `*ast.BasicLit` rather than as the compile error it would be.
			name: "a step larger than an int",
			body: "\tfor i := 0; i < n; i += 99999999999999999999 {\n\t}",
		},
		{name: "a step written in hexadecimal", body: "\tfor i := 0; i < n; i += 0x2 {\n\t}", want: true, variable: "i", step: 2, comparison: token.LSS},
		{name: "a step that multiplies", body: "\tfor i := 1; i < n; i *= 2 {\n\t}"},
		{name: "a step that assigns rather than moves", body: "\tfor i := 0; i < n; i = f() {\n\t}"},
		{name: "a step of two variables", body: "\tfor i, j := 0, 0; i < n; i, j = i+1, j+1 {\n\t}"},
		{name: "a step of something that is not a name", body: "\tfor xs[0] = 0; xs[0] < n; xs[0]++ {\n\t}"},
		{name: "a condition that is not a comparison", body: "\tfor i := 0; ok; i++ {\n\t}"},
		{name: "a condition comparing something else", body: "\tfor i := 0; j < n; i++ {\n\t}"},
		{name: "a condition that is not an ordering", body: "\tfor i := 0; i != n; i++ {\n\t}"},
		{name: "a variable on both sides", body: "\tfor i := 0; i < n-i; i++ {\n\t}"},
		{name: "a variable on both sides, mirrored", body: "\tfor i := 0; n-i > i; i++ {\n\t}"},
		{name: "a body that moves the variable", body: "\tfor i := 0; i < n; i++ {\n\t\ti = 0\n\t}"},
		{name: "a body that steps the variable", body: "\tfor i := 0; i < n; i++ {\n\t\ti++\n\t}"},
		{name: "a closure in the body that moves it", body: "\tfor i := 0; i < n; i++ {\n\t\tdefer func() { i = 0 }()\n\t}"},
		{name: "a body that moves the bound", body: "\tfor i := 0; i < n; i++ {\n\t\tn = f()\n\t}"},
		{name: "a body that moves part of a computed bound", body: "\tfor i := 0; i < len(xs)-1; i++ {\n\t\txs = nil\n\t}"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, ok := readInductionLoop(loopIn(t, c.body))
			if ok != c.want {
				t.Fatalf("readInductionLoop = %v, want %v", ok, c.want)
			}
			if !c.want {
				return
			}
			if got.variable != c.variable || got.step != c.step || got.comparison != c.comparison {
				t.Errorf("readInductionLoop = {%s %s %d}, want {%s %s %d}",
					got.variable, got.comparison, got.step, c.variable, c.comparison, c.step)
			}
		})
	}
}

// TestAMeasureProgressesWhenTheStepAgreesWithTheComparison is the lemma itself,
// stated over the four orderings and the two directions of travel.
//
// The proof rests on one claim: the loop stops because each iteration moves the
// variable towards the bound the condition tests. A step that moves away never
// reaches it, and that is what `unbounded` means.
func TestAMeasureProgressesWhenTheStepAgreesWithTheComparison(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		comparison token.Token
		step       int
		want       bool
	}{
		{token.LSS, 1, true},
		{token.LEQ, 1, true},
		{token.LSS, -1, false},
		{token.LEQ, -1, false},
		{token.GTR, -1, true},
		{token.GEQ, -1, true},
		{token.GTR, 1, false},
		{token.GEQ, 1, false},
		// Neither direction: a comparison that is not an ordering orders
		// nothing, so no step agrees with it.
		{token.EQL, 1, false},
		{token.NEQ, -1, false},
		{token.ILLEGAL, 1, false},
		// And a step of zero, which moves the variable nowhere however the
		// condition is written.
		{token.LSS, 0, false},
		{token.GTR, 0, false},
	} {
		t.Run(c.comparison.String()+" with "+stepName(c.step), func(t *testing.T) {
			t.Parallel()

			loop := inductionLoop{comparison: c.comparison, step: c.step}
			if got := loop.progresses(); got != c.want {
				t.Errorf("a %s loop stepping by %d progresses = %v, want %v",
					c.comparison, c.step, got, c.want)
			}
		})
	}
}

// stepName names a step for a subtest.
func stepName(step int) string {
	switch {
	case step > 0:
		return "a forward step"
	case step < 0:
		return "a backward step"
	default:
		return "no step"
	}
}

// TestTheReadersUnderneathTheLoopReader is each small question on its own, for
// the answers the loop shapes above reach only in combination.
func TestTheReadersUnderneathTheLoopReader(t *testing.T) {
	t.Parallel()

	t.Run("a name is a bare identifier and nothing else", func(t *testing.T) {
		t.Parallel()

		if name, ok := identName(ast.NewIdent("i")); !ok || name != "i" {
			t.Errorf("identName(i) = (%q, %v), want (\"i\", true)", name, ok)
		}
		if name, ok := identName(&ast.BasicLit{Kind: token.INT, Value: "1"}); ok || name != "" {
			t.Errorf("identName(a literal) = (%q, %v), want (\"\", false)", name, ok)
		}
		if name, ok := identName(nil); ok || name != "" {
			t.Errorf("identName(nothing) = (%q, %v), want (\"\", false)", name, ok)
		}
	})

	t.Run("a name is mentioned wherever it appears", func(t *testing.T) {
		t.Parallel()

		loop := loopIn(t, "\tfor i := 0; i < len(xs)-j; i++ {\n\t}")
		for _, c := range []struct {
			name string
			want bool
		}{
			{name: "i", want: true},
			{name: "xs", want: true},
			{name: "j", want: true},
			{name: "len", want: true},
			{name: "k"},
		} {
			if got := mentions(loop.Cond, c.name); got != c.want {
				t.Errorf("mentions(the condition, %q) = %v, want %v", c.name, got, c.want)
			}
		}
	})

	t.Run("an ordering is one of four operators", func(t *testing.T) {
		t.Parallel()

		for op, want := range map[token.Token]bool{
			token.LSS: true, token.LEQ: true, token.GTR: true, token.GEQ: true,
			token.EQL: false, token.NEQ: false, token.ADD: false, token.ILLEGAL: false,
		} {
			if got := isOrdering(op); got != want {
				t.Errorf("isOrdering(%s) = %v, want %v", op, got, want)
			}
		}
	})

	t.Run("a block assigns a name however it is written", func(t *testing.T) {
		t.Parallel()

		for _, c := range []struct {
			name string
			body string
			want bool
		}{
			{name: "an increment", body: "\t\tn++", want: true},
			{name: "a decrement", body: "\t\tn--", want: true},
			{name: "an assignment", body: "\t\tn = 1", want: true},
			{name: "a declaration that shadows", body: "\t\tn := 1\n\t\t_ = n", want: true},
			{name: "a second target", body: "\t\tj, n = 1, 2", want: true},
			{name: "inside a nested block", body: "\t\tif ok {\n\t\t\tn = 1\n\t\t}", want: true},
			{name: "inside a closure", body: "\t\tgo func() { n = 1 }()", want: true},
			{name: "a read", body: "\t\t_ = n"},
			{name: "an index of it", body: "\t\txs[n] = 1"},
			{name: "another name entirely", body: "\t\tj = 1"},
			{name: "nothing at all", body: ""},
		} {
			loop := loopIn(t, "\tfor i := 0; i < 1; i++ {\n"+c.body+"\n\t}")
			if got := assignsWithin(loop.Body, "n"); got != c.want {
				t.Errorf("assignsWithin(%s, n) = %v, want %v", c.name, got, c.want)
			}
		}
		if assignsWithin(nil, "n") {
			t.Error("assignsWithin(no block, n) = true, want false")
		}
	})

	t.Run("a condition that is not a comparison is a bound that moves", func(t *testing.T) {
		t.Parallel()

		// The refusal is written that way round on purpose: boundMoves is asked
		// only about loops whose measure has been read, and a condition it
		// cannot take apart is one it cannot vouch for.
		loop := loopIn(t, "\tfor i := 0; ok; i++ {\n\t}")
		if !boundMoves(loop) {
			t.Error("boundMoves(a condition that is not a comparison) = false, want true")
		}
	})
}

// terminationProbe is a file scan over a parsed fixture, which is everything a
// termination proof reads: the parent index the walk outward uses, and the
// token file the loop's position comes from. No type information at all, which
// is the point -- whether a loop stops is decided from the syntax, before
// anything is built.
func terminationProbe(t *testing.T, src string) *fileScan {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "probe.go", src, 0)
	if err != nil {
		t.Fatalf("parsing:\n%s\n%v", src, err)
	}
	tokFile := fset.File(file.Package)
	return &fileScan{tokFile: tokFile, guard: newGuardResolver(file, nil, nil, tokFile, nil)}
}

// firstNode finds the first node of the fixture that satisfies a predicate,
// which is how a test names the thing an edit is anchored to without counting.
func firstNode(t *testing.T, s *fileScan, want func(ast.Node) bool) ast.Node {
	t.Helper()

	var found ast.Node
	for node := range s.guard.parent {
		if want(node) && (found == nil || node.Pos() < found.Pos()) {
			found = node
		}
	}
	if found == nil {
		t.Fatal("the fixture holds no node of the kind the test asked for")
	}
	return found
}

// TestWhatOneEditDoesToALoopsMeasure is [fileScan.applyToLoop], which is where
// a rule name becomes a claim about termination.
//
// Three groups, and the third is the one worth stating: a rule that touches
// neither the condition nor the step leaves the measure alone, and a loop whose
// measure is untouched still stops. Saying nothing there would lose every
// arithmetic mutant in a loop body, which is most of them.
func TestWhatOneEditDoesToALoopsMeasure(t *testing.T) {
	t.Parallel()

	const src = "package pkg\n\nfunc probe(n int) int {\n" +
		"\ttotal := 0\n" +
		"\tfor i := 0; i < n; i++ {\n" +
		"\t\ttotal += i * 2\n" +
		"\t}\n" +
		"\treturn total\n" +
		"}\n"

	for _, c := range []struct {
		name    string
		rule    string
		anchor  func(ast.Node) bool
		want    bool
		bounded bool
		reason  string
	}{
		{
			name: "a negated condition runs away",
			rule: "negate-loop-condition",
			anchor: func(n ast.Node) bool {
				binary, ok := n.(*ast.BinaryExpr)
				return ok && binary.Op == token.LSS
			},
			want:   true,
			reason: "negated",
		},
		{
			name: "a moved comparison still ends",
			rule: "lt-to-le",
			anchor: func(n ast.Node) bool {
				binary, ok := n.(*ast.BinaryExpr)
				return ok && binary.Op == token.LSS
			},
			want:    true,
			bounded: true,
			reason:  "moves by one",
		},
		{
			name: "a condition settled false runs zero times",
			rule: ruleLoopConditionToFalse,
			anchor: func(n ast.Node) bool {
				binary, ok := n.(*ast.BinaryExpr)
				return ok && binary.Op == token.LSS
			},
			want:    true,
			bounded: true,
			reason:  "zero times",
		},
		{
			name: "an equality edit says nothing",
			rule: "lt-to-gt",
			anchor: func(n ast.Node) bool {
				binary, ok := n.(*ast.BinaryExpr)
				return ok && binary.Op == token.LSS
			},
		},
		{
			name:   "a reversed step runs away",
			rule:   "incr-to-decr",
			anchor: func(n ast.Node) bool { _, ok := n.(*ast.IncDecStmt); return ok },
			want:   true,
			reason: "reversed",
		},
		{
			name:   "a deleted step never arrives",
			rule:   "delete-incdec",
			anchor: func(n ast.Node) bool { _, ok := n.(*ast.IncDecStmt); return ok },
			want:   true,
			reason: "deleted",
		},
		{
			name:   "a rule the step reader does not know says nothing",
			rule:   "mul-to-div",
			anchor: func(n ast.Node) bool { _, ok := n.(*ast.IncDecStmt); return ok },
		},
		{
			name: "an edit in the body leaves the measure alone",
			rule: "mul-to-div",
			anchor: func(n ast.Node) bool {
				binary, ok := n.(*ast.BinaryExpr)
				return ok && binary.Op == token.MUL
			},
			want:    true,
			bounded: true,
			reason:  "outside the loop's condition and step",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			s := terminationProbe(t, src)
			anchor := firstNode(t, s, c.anchor)
			loop, read := s.enclosingInductionLoop(anchor)
			if !read {
				t.Fatal("the fixture's loop is not an induction loop")
			}
			mutated, reason, ok := s.applyToLoop(loop, mutation.Rule{Name: c.rule}, anchor)
			if ok != c.want {
				t.Fatalf("applyToLoop(%s) = %v, want %v", c.rule, ok, c.want)
			}
			if !c.want {
				if reason != "" {
					t.Errorf("a refusal carries the reason %q, want none", reason)
				}
				return
			}
			if !strings.Contains(reason, c.reason) {
				t.Errorf("the reason %q does not say %q", reason, c.reason)
			}
			if got := mutated.progresses(); got != c.bounded {
				t.Errorf("the mutated loop progresses = %v, want %v", got, c.bounded)
			}
		})
	}
}

// TestAnEditIsInsideTheNodeItsPositionsLieWithin pins [fileScan.editsNode],
// which is how a rule name is matched to the part of the loop it touches.
//
// The absent node is the case that matters: `for i := 0; i < n; ` has no post
// statement, and asking whether an edit is inside one has to be answered rather
// than dereferenced.
func TestAnEditIsInsideTheNodeItsPositionsLieWithin(t *testing.T) {
	t.Parallel()

	s := terminationProbe(t, "package pkg\n\nfunc probe(n int) {\n\tfor i := 0; i < n; i++ {\n\t}\n}\n")
	loop := firstNode(t, s, func(n ast.Node) bool { _, ok := n.(*ast.ForStmt); return ok }).(*ast.ForStmt)
	inside := firstNode(t, s, func(n ast.Node) bool { _, ok := n.(*ast.IncDecStmt); return ok })

	if !s.editsNode(inside, loop.Post) {
		t.Error("the step statement is not inside the post statement it is")
	}
	if s.editsNode(inside, loop.Cond) {
		t.Error("the step statement is inside the condition, which it is not")
	}
	if s.editsNode(inside, nil) {
		t.Error("an edit is inside a node that is not there")
	}
	if s.editsNode(nil, loop.Post) {
		t.Error("an edit that is not there is inside the post statement")
	}
}

// TestTheWalkOutwardStopsAtTheFunctionItIsIn pins
// [fileScan.enclosingInductionLoop]: the nearest enclosing `for`, and nothing
// past the function boundary.
//
// A function literal is a boundary as much as a declaration is. A loop outside
// a closure does not bound what runs inside it — the closure may be called
// anywhere, any number of times — so an edit in the closure's body is an edit
// the loop says nothing about.
func TestTheWalkOutwardStopsAtTheFunctionItIsIn(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "inside the loop",
			src: "package pkg\n\nfunc probe(n int) {\n\tfor i := 0; i < n; i++ {\n" +
				"\t\t_ = i * 2\n\t}\n}\n",
			want: true,
		},
		{
			name: "inside a closure the loop creates",
			src: "package pkg\n\nfunc probe(n int) {\n\tfor i := 0; i < n; i++ {\n" +
				"\t\tgo func() { _ = i * 2 }()\n\t}\n}\n",
		},
		{
			name: "in a function of its own",
			src: "package pkg\n\nfunc probe(n int) {\n\tfor i := 0; i < n; i++ {\n\t}\n}\n" +
				"\nfunc other() {\n\t_ = 1 * 2\n}\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			s := terminationProbe(t, c.src)
			anchor := firstNode(t, s, func(n ast.Node) bool {
				binary, ok := n.(*ast.BinaryExpr)
				return ok && binary.Op == token.MUL
			})
			if _, got := s.enclosingInductionLoop(anchor); got != c.want {
				t.Errorf("enclosingInductionLoop = %v, want %v", got, c.want)
			}
		})
	}
}

// TestEveryBoundaryMovingRuleNamesTheOperatorItProduces pins [movedComparison]
// against the rule names the registry holds, in both directions of each pair.
//
// The four are written out rather than derived, so a rule renamed in the
// registry and not here answers ILLEGAL — which the caller reads as a loop
// whose measure it cannot follow, rather than as a wrong answer.
func TestEveryBoundaryMovingRuleNamesTheOperatorItProduces(t *testing.T) {
	t.Parallel()

	for rule, want := range map[string]token.Token{
		"lt-to-le":         token.LEQ,
		"le-to-lt":         token.LSS,
		"gt-to-ge":         token.GEQ,
		"ge-to-gt":         token.GTR,
		"negate-condition": token.ILLEGAL,
		"":                 token.ILLEGAL,
	} {
		if got := movedComparison(rule); got != want {
			t.Errorf("movedComparison(%q) = %s, want %s", rule, got, want)
		}
	}
}

// TestNoProofIsMadeAboutALoopThisPhaseCannotRead is
// [fileScan.terminationProof]'s refusal, from the side that has no loop at all.
//
// Every refusal here is silent, which is the whole design: a proof is an
// optimisation a consumer may use, so its absence is not a decision anybody
// looks up. What the silence must not be is a proof about a loop that was never
// read -- an edit outside any loop has no measure, and reasoning about the zero
// value of one would be reasoning about a loop that does not exist.
func TestNoProofIsMadeAboutALoopThisPhaseCannotRead(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
	}{
		{
			name: "an edit in no loop at all",
			src:  "package pkg\n\nfunc probe(a, b int) int {\n\treturn a * b\n}\n",
		},
		{
			name: "an edit in a loop with no measure",
			src:  "package pkg\n\nfunc probe(a, b int) {\n\tfor {\n\t\t_ = a * b\n\t}\n}\n",
		},
		{
			name: "an edit in a loop whose variable the body moves",
			src: "package pkg\n\nfunc probe(n int) {\n\tfor i := 0; i < n; i++ {\n" +
				"\t\ti = n * 2\n\t}\n}\n",
		},
		{
			name: "an edit in a loop that counts away from its bound",
			src:  "package pkg\n\nfunc probe(n int) {\n\tfor i := 0; i > n; i++ {\n\t\t_ = n * 2\n\t}\n}\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			s := terminationProbe(t, c.src)
			anchor := firstNode(t, s, func(n ast.Node) bool {
				binary, ok := n.(*ast.BinaryExpr)
				return ok && binary.Op == token.MUL
			})
			if got := s.terminationProof(mutation.Rule{Name: "mul-to-div"}, anchor); got != nil {
				t.Errorf("terminationProof = %+v, want none", got)
			}
		})
	}
}

// TestAProofNamesTheLoopsOwnCoordinates is the other half of the same call, and
// the half a `//line` directive can move.
//
// The coordinate published is the unadjusted one, exactly as every other
// coordinate this package reports: a directive relocates a *compiler*
// diagnostic, and what a consumer of this proof has in front of it is the file
// the snapshot holds. A proof pointing at the generator's input would name a
// line nobody can open.
func TestAProofNamesTheLoopsOwnCoordinates(t *testing.T) {
	t.Parallel()

	const src = "package pkg\n\n" +
		"//line generated.go:100\n" +
		"func probe(n int) {\n\tfor i := 0; i < n; i++ {\n\t\t_ = n * 2\n\t}\n}\n"

	s := terminationProbe(t, src)
	anchor := firstNode(t, s, func(n ast.Node) bool {
		binary, ok := n.(*ast.BinaryExpr)
		return ok && binary.Op == token.MUL
	})
	proof := s.terminationProof(mutation.Rule{Name: "mul-to-div"}, anchor)
	if proof == nil {
		t.Fatal("terminationProof made no proof about a counted loop")
	}
	if proof.Verdict != TerminationBounded {
		t.Errorf("verdict = %q, want %q", proof.Verdict, TerminationBounded)
	}
	// The `for` is the fifth line of the file as it is written, and the
	// directive claims the file is a different one starting at 100.
	if proof.LoopLine != 5 {
		t.Errorf("the proof names line %d, want the line the snapshot holds", proof.LoopLine)
	}
}
