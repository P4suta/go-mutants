// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// The return probe hint, which is the second thing a return-value candidate
// carries away from this phase and the first that is allowed to be absent.
//
// [Guard] answers "how does the instrumenter write this mutant into the mutant
// tree". [ReturnSite] answers a different question for a different tree: how
// does it write, into the probe tree, the test that says whether this mutant's
// value would have differed here. The answer needs the declared result type of
// every value the statement returns — not the type of the expression, the type
// the `return` converts it to — which is why it is computed in the phase that
// owns the type checker and travels down as data like everything else.
//
// The fixtures below are whole modules rather than packages of testdata/mainmod
// on purpose. Every one of them is about a shape that has to be *refused*, and
// adding refusal shapes to the shared module would move every entry of its
// pinned candidate and skip tables for a fact that has nothing to do with them.

// probeHintModule wraps one package's source in a module the loader will
// accept, alongside whatever other files the fixture needs.
//
// The `go` directive is deliberately older than anything these fixtures use, so
// they load under whatever toolchain is on PATH rather than only the pinned one.
func probeHintModule(source string, extra map[string]string) map[string]string {
	const header = "// SPDX-FileCopyrightText: 2026 go-mutants contributors\n" +
		"// SPDX-License-Identifier: MIT OR Apache-2.0\n\n"
	files := map[string]string{
		"go.mod": header + "module example.com/probe\n\ngo 1.21\n",
		"sample.go": header + "// Package sample is a return probe fixture.\n" +
			source,
	}
	for name, content := range extra {
		files[name] = header + content
	}
	return files
}

// discoverProbeModule writes a fixture module and discovers it, failing the
// test on anything the loader or the walk refuses.
func discoverProbeModule(t *testing.T, source string, extra map[string]string) ([]Located, string) {
	t.Helper()

	files := probeHintModule(source, extra)
	root := writeModule(t, files)
	result, err := Discover(context.Background(), Options{SnapshotRoot: root, Toolchain: toolchain(t)})
	if err != nil {
		t.Fatalf("Discover over the fixture module: %v", err)
	}
	return result.Candidates, files["sample.go"]
}

// spanOf locates one snippet of a fixture's source, insisting it occurs once so
// that naming it names a site.
func spanOf(t *testing.T, src, snippet string) mutation.Span {
	t.Helper()

	start := strings.Index(src, snippet)
	if start < 0 {
		t.Fatalf("the fixture does not hold %q", snippet)
	}
	if strings.Contains(src[start+1:], snippet) {
		t.Fatalf("the fixture holds %q more than once, so it does not locate a site", snippet)
	}
	return mutation.Span{StartByte: uint32(start), EndByte: uint32(start + len(snippet))}
}

// candidateWithOriginal returns the one candidate of a rule that replaces
// particular bytes, which is what tells two candidates of one rule apart.
func candidateWithOriginal(t *testing.T, candidates []Located, rule, original string) Located {
	t.Helper()

	var found []Located
	for _, c := range candidates {
		if c.Rule.Name == rule && c.Original == original {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d candidates are %s over %q, want exactly one: %v",
			len(found), rule, original, summarize(candidates))
	}
	return found[0]
}

// TestReturnSiteNamesEveryResultTypeOfTheStatement is the hint's central claim:
// the probe has to declare a temporary for *every* value the statement returns,
// not only the one being mutated, because the rewrite evaluates the operands
// once each and hands them all to one `return`.
//
// Two candidates of one statement is also the case where the hint has to say
// two different things about the same bytes: they share a span and a type list
// and differ only in which result the edit replaces.
func TestReturnSiteNamesEveryResultTypeOfTheStatement(t *testing.T) {
	candidates, src := discoverProbeModule(t, `package sample

// Measure reports the length of s and whatever measuring it went wrong with.
func Measure(s string) (int, error) {
	count, err := measure(s)
	return count, err
}

// measure is the callee, and returns the two values the family passes over: a
// zero that is already the replacement, and a nil that is already the nil.
func measure(s string) (int, error) { return 0, nil }
`, nil)

	stmt := spanOf(t, src, "return count, err")
	for _, c := range []struct {
		rule     string
		original string
		index    int
	}{
		{ruleReturnZeroNumeric, "count", 0},
		{ruleReturnErrToNil, "err", 1},
	} {
		t.Run(c.rule, func(t *testing.T) {
			got := candidateWithOriginal(t, candidates, c.rule, c.original).Guard.Return
			want := &ReturnSite{Span: stmt, Types: []string{"int", "error"}, Index: c.index}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("return site = %+v, want %+v", got, want)
			}
		})
	}
}

// TestReturnSiteSpellsTheDeclaredResultTypeNotTheOperands pins the type the
// hint carries.
//
// The rewrite declares `var r0 T = E0`, and T has to be the *result* type: that
// is the conversion the `return` itself performs, and it is the type the mutant
// would have converted its constant to. Taking the operand's type instead would
// declare `var r0 int = 1` in a function returning float32 — a different
// program that happens to compile — and would compare the wrong value against
// the wrong constant.
func TestReturnSiteSpellsTheDeclaredResultTypeNotTheOperands(t *testing.T) {
	candidates, src := discoverProbeModule(t, `package sample

// Level is a named integer type, spelled as this file spells it.
type Level int

// Half returns an untyped constant as a float.
func Half() float32 { return 1 }

// Count returns the same constant through a named result of a named type.
func Count() (n Level) { return 1 }
`, nil)

	for _, c := range []struct {
		name    string
		snippet string
		types   []string
	}{
		{"an untyped constant in a float function", "func Half() float32 { return 1 }", []string{"float32"}},
		{"a named result of a named type", "func Count() (n Level) { return 1 }", []string{"Level"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := returnSiteInside(t, candidates, src, c.snippet)
			if !reflect.DeepEqual(got.Types, c.types) {
				t.Errorf("result types = %v, want %v", got.Types, c.types)
			}
			if got.Index != 0 {
				t.Errorf("result index = %d, want 0", got.Index)
			}
		})
	}
}

// returnSiteInside returns the one return site of a candidate whose statement
// lies inside a snippet of the fixture, which is how two candidates of one rule
// in one file are told apart.
func returnSiteInside(t *testing.T, candidates []Located, src, snippet string) *ReturnSite {
	t.Helper()

	region := spanOf(t, src, snippet)
	var found []*ReturnSite
	for _, c := range candidates {
		site := c.Guard.Return
		if site != nil && region.Contains(site.Span) {
			found = append(found, site)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d candidates carry a return site inside %q, want exactly one", len(found), snippet)
	}
	return found[0]
}

// TestReturnSiteIsNilWhenAResultTypeCannotBeSpelled covers the refusal that
// keeps the hint honest.
//
// The rewrite writes the result types into the file it rewrites, so a type that
// file cannot name is a probe that cannot be written. Both halves of the
// existing spelling machinery are exercised, because they fail for unrelated
// reasons: a dot import binds a package's contents rather than the package, so
// the qualifier has no name for it, and `unsafe.Pointer` is a basic type that
// still needs an import, which is why it is refused whatever the file imports.
//
// A refused hint costs the probe and never the mutant: every candidate here is
// still catalogued, still mutated, and still guarded in the mutant tree.
func TestReturnSiteIsNilWhenAResultTypeCannotBeSpelled(t *testing.T) {
	for _, c := range []struct {
		name   string
		source string
		extra  map[string]string
		rule   string
		bytes  string
	}{{
		name: "a dot-imported result type",
		source: `package sample

import . "example.com/probe/kinds"

// Pair returns a value of a type this file imports without naming.
func Pair(a int) (int, Kind) { return a, Zero }
`,
		extra: map[string]string{
			"kinds/kinds.go": "// Package kinds holds the type sample cannot name.\npackage kinds\n\n" +
				"// Kind is a named integer type.\ntype Kind int\n\n" +
				"// Zero is the kind that is nothing.\nconst Zero Kind = 0\n",
		},
		rule:  ruleReturnZeroNumeric,
		bytes: "a",
	}, {
		name: "an unsafe pointer result",
		source: `package sample

import "unsafe"

// Ptr returns a pointer no source form of the declaration can name.
func Ptr(p unsafe.Pointer) (int, unsafe.Pointer) { return 1, p }
`,
		rule:  ruleReturnZeroNumeric,
		bytes: "1",
	}} {
		t.Run(c.name, func(t *testing.T) {
			candidates, _ := discoverProbeModule(t, c.source, c.extra)
			got := candidateWithOriginal(t, candidates, c.rule, c.bytes)
			if got.Guard.Return != nil {
				t.Errorf("return site = %+v, want none: the file cannot spell every result type",
					got.Guard.Return)
			}
			if got.Guard.Form == "" {
				t.Error("the candidate lost its guard form as well, so the refusal cost the mutant and not only the probe")
			}
		})
	}
}

// TestReturnSiteIsNilForATypeParameterResult refuses the shape the compiler
// might refuse.
//
// The probe compares a temporary against a constant, and a value of a type
// parameter's type need not be comparable with one: the constraint decides, and
// this phase does not reason about constraints. The bisection would find such a
// site and drop that one mutant's probe, which is exactly the mechanism that
// exists for the cases nobody foresaw — spending a build on a case that is
// foreseen is not what it is for.
func TestReturnSiteIsNilForATypeParameterResult(t *testing.T) {
	candidates, _ := discoverProbeModule(t, `package sample

// Pair returns its own arguments, one of them of a type parameter's type.
func Pair[T any](a int, t T) (int, T) { return a, t }
`, nil)

	got := candidateWithOriginal(t, candidates, ruleReturnZeroNumeric, "a")
	if got.Guard.Return != nil {
		t.Errorf("return site = %+v, want none: one result is a type parameter", got.Guard.Return)
	}
}

// TestReturnSiteIsAbsentForOtherRules keeps the hint to the family it describes.
//
// The rewrite it drives replaces a returned value with a constant and tests
// whether the value differs. That is a statement about the return-value rules
// and about nothing else: an operator swap inside the same statement changes a
// value the hint says nothing about, and a probe built from this hint for it
// would report an infection for the wrong mutant.
func TestReturnSiteIsAbsentForOtherRules(t *testing.T) {
	candidates, src := discoverProbeModule(t, `package sample

// Above reports whether a exceeds b.
func Above(a, b int) bool { return a > b }
`, nil)

	stmt := spanOf(t, src, "return a > b")
	for _, c := range []struct {
		rule string
		want *ReturnSite
	}{
		{ruleReturnTrue, &ReturnSite{Span: stmt, Types: []string{"bool"}, Index: 0}},
		{ruleReturnFalse, &ReturnSite{Span: stmt, Types: []string{"bool"}, Index: 0}},
		{"gt-to-ge", nil},
	} {
		t.Run(c.rule, func(t *testing.T) {
			var got Located
			switch c.rule {
			case "gt-to-ge":
				got = candidateWithOriginal(t, candidates, c.rule, ">")
			default:
				got = candidateWithOriginal(t, candidates, c.rule, "a > b")
			}
			if !reflect.DeepEqual(got.Guard.Return, c.want) {
				t.Errorf("return site = %+v, want %+v", got.Guard.Return, c.want)
			}
		})
	}
}

// TestReturnSiteDoesNotDisturbTheMutantSide is the compatibility claim in the
// smallest form that can hold it: the hint rides beside the guard, and the
// guard is what it was.
func TestReturnSiteDoesNotDisturbTheMutantSide(t *testing.T) {
	candidates, src := discoverProbeModule(t, `package sample

// Above reports whether a exceeds b.
func Above(a, b int) bool { return a > b }
`, nil)

	got := candidateWithOriginal(t, candidates, ruleReturnTrue, "a > b")
	if got.Guard.Form != GuardFormC {
		t.Errorf("guard form = %q, want %q", got.Guard.Form, GuardFormC)
	}
	if want := spanOf(t, src, "a > b"); got.Guard.SiteSpan != want {
		t.Errorf("guard site = %s, want %s", got.Guard.SiteSpan, want)
	}
	if len(got.Guard.DeclTypes) != 0 {
		t.Errorf("a Form C site declares %v", got.Guard.DeclTypes)
	}
}
