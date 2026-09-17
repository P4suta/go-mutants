// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

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

func TestProbeSiteNamesEveryResultTypeOfTheStatement(t *testing.T) {
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
			got := candidateWithOriginal(t, candidates, c.rule, c.original).Guard.Probe
			want := &ProbeSite{Form: ProbeFormReturn, Span: stmt, Types: []string{"int", "error"}, Index: c.index}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("return site = %+v, want %+v", got, want)
			}
		})
	}
}

func TestProbeSiteSpellsTheDeclaredResultTypeNotTheOperands(t *testing.T) {
	candidates, src := discoverProbeModule(t, `package sample

// Level is a named integer type, spelled as this file spells it.
type Level int

// Wide returns an untyped constant as an integer wider than its default type.
func Wide() int64 { return 1 }

// Count returns the same constant through a named result of a named type.
func Count() (n Level) { return 1 }
`, nil)

	for _, c := range []struct {
		name    string
		snippet string
		types   []string
	}{
		{"an untyped constant in a wider function", "func Wide() int64 { return 1 }", []string{"int64"}},
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

func returnSiteInside(t *testing.T, candidates []Located, src, snippet string) *ProbeSite {
	t.Helper()

	region := spanOf(t, src, snippet)
	var found []*ProbeSite
	for _, c := range candidates {
		site := c.Guard.Probe
		if site != nil && region.Contains(site.Span) {
			found = append(found, site)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d candidates carry a return site inside %q, want exactly one", len(found), snippet)
	}
	return found[0]
}

func TestProbeSiteIsNilWhenAResultTypeCannotBeSpelled(t *testing.T) {
	for _, c := range []struct {
		name   string
		source string
		extra  map[string]string
		rule   string
		bytes  string
	}{{
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
			site := got.Guard.Probe
			if site != nil && site.Form == ProbeFormReturn {
				t.Errorf("return site = %+v, want none: the file cannot spell every result type", site)
			}
			if site == nil || site.Form != ProbeFormValue {
				t.Errorf("probe site = %+v, want the value form over the operand", site)
			}
			if got.Guard.Form == "" {
				t.Error("the candidate lost its guard form as well, so the refusal cost the mutant and not only the probe")
			}
		})
	}
}

func TestADotImportedResultTypeIsNowSpellable(t *testing.T) {
	candidates, _ := discoverProbeModule(t, `package sample

import . "example.com/probe/kinds"

// Pair returns a value of a type this file imports without naming.
func Pair(a int) (int, Kind) { return a, Zero }
`, map[string]string{
		"kinds/kinds.go": "// Package kinds holds the type sample could not name.\npackage kinds\n\n" +
			"// Kind is a named integer type.\ntype Kind int\n\n" +
			"// Zero is the kind that is nothing.\nconst Zero Kind = 0\n",
	})
	got := candidateWithOriginal(t, candidates, ruleReturnZeroNumeric, "a")
	site := got.Guard.Probe
	if site == nil {
		t.Fatal("the return site is still refused, so the dot import was not completed")
	}
	if len(site.Types) != 2 || site.Types[1] != "kinds.Kind" {
		t.Errorf("Types = %q, want the second result spelled against the completed import", site.Types)
	}
	want := []Completion{{Path: "example.com/probe/kinds", Local: "kinds"}}
	if !slices.Equal(site.Imports, want) {
		t.Errorf("Imports = %+v, want %+v", site.Imports, want)
	}
	if len(got.Guard.Imports) != 0 {
		t.Errorf("Guard.Imports = %+v, and the mutant tree's rewrite spells no type", got.Guard.Imports)
	}
}

func TestTheReturnFormRefusesATypeParameterResult(t *testing.T) {
	candidates, _ := discoverProbeModule(t, `package sample

// Pair returns its own arguments, one of them of a type parameter's type.
func Pair[T any](a int, t T) (int, T) { return a, t }
`, nil)

	got := candidateWithOriginal(t, candidates, ruleReturnZeroNumeric, "a")
	site := got.Guard.Probe
	if site != nil && site.Form == ProbeFormReturn {
		t.Errorf("return site = %+v, want none: one result is a type parameter", site)
	}
	if site == nil || site.Form != ProbeFormValue {
		t.Errorf("probe site = %+v, want the value form over the int operand", site)
	}
	if len(site.Types) != 1 || site.Types[0] != "int" {
		t.Errorf("Types = %q, want the operand's own type", site.Types)
	}
}

func candidatesInside(t *testing.T, candidates []Located, src, snippet string) []Located {
	t.Helper()

	region := spanOf(t, src, snippet)
	var found []Located
	for _, c := range candidates {
		if region.Contains(c.Span) {
			found = append(found, c)
		}
	}
	return found
}

func describe(candidates []Located) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Rule.Name+" "+c.Original)
	}
	return out
}

func TestProbeSiteRefusesAStatementWithAnEffectfulOperand(t *testing.T) {
	for _, c := range []struct {
		name   string
		source string
		stmt   string
		want   []string
	}{{
		name: "a call in the mutated operand",
		source: `package sample

// Compute returns a number a call produces, and no error.
func Compute() (int, error) { return compute(), nil }

// compute is the call the mutant would never make.
func compute() int { return 1 }
`,
		stmt: "return compute(), nil",
		want: []string{"return-zero-numeric compute()"},
	}, {
		name: "a receive beside a plain identifier",
		source: `package sample

// Take returns a number it holds and one it receives.
func Take(n int, ch chan int) (int, int) { return n, <-ch }
`,
		stmt: "return n, <-ch",
		want: []string{"return-zero-numeric n", "return-zero-numeric <-ch"},
	}, {
		name: "an append in the only operand",
		source: `package sample

// Grow returns a longer slice.
func Grow(s []int, x int) []int { return append(s, x) }
`,
		stmt: "return append(s, x)",
		want: []string{"return-nil append(s, x)", "return-empty-slice append(s, x)"},
	}, {
		name: "a call beside the mutated operand",
		source: `package sample

// Order returns a value it holds and one a call produces.
func Order(x int, f func() int) (int, int) { return x, f() }
`,
		stmt: "return x, f()",
		want: []string{"return-zero-numeric x", "return-zero-numeric f()"},
	}, {
		name: "a method call",
		source: `package sample

// counter is a value with a method, so that a method call can be returned.
type counter struct{ n int }

// Load returns the counter's value.
func (c counter) Load() int { return c.n }

// Read returns a counter's value and no error.
func Read(c counter) (int, error) { return c.Load(), nil }
`,
		stmt: "return c.Load(), nil",
		want: []string{"return-zero-numeric c.Load()"},
	}} {
		t.Run(c.name, func(t *testing.T) {
			candidates, src := discoverProbeModule(t, c.source, nil)
			inside := candidatesInside(t, candidates, src, c.stmt)
			if got := describe(inside); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("the statement catalogues %v, want %v", got, c.want)
			}
			for _, got := range inside {
				if got.Guard.Probe != nil {
					t.Errorf("%s over %q carries the return site %+v, want none: an operand of its statement has effects",
						got.Rule.Name, got.Original, got.Guard.Probe)
				}
				if got.Guard.Form == "" {
					t.Errorf("%s over %q lost its guard form as well, so the refusal cost the mutant and not only the probe",
						got.Rule.Name, got.Original)
				}
			}
		})
	}
}

func TestProbeSiteRefusesTheResultWhoseOperandCanPanic(t *testing.T) {
	for _, c := range []struct {
		name   string
		source string
		stmt   string
		rule   string
		bytes  string
		types  []string
	}{{
		name: "a field of a pointer",
		source: `package sample

// node is a struct this fixture reaches through a pointer.
type node struct{ n int }

// Field returns a field of a pointer and the error beside it.
func Field(p *node, err error) (int, error) { return p.n, err }
`,
		stmt: "return p.n, err", rule: ruleReturnZeroNumeric, bytes: "p.n",
		types: []string{"int", "error"},
	}, {
		name: "an index",
		source: `package sample

// First returns the first item of a slice that may hold none.
func First(items []int, err error) (int, error) { return items[0], err }
`,
		stmt: "return items[0], err", rule: ruleReturnZeroNumeric, bytes: "items[0]",
		types: []string{"int", "error"},
	}, {
		name: "a dereference",
		source: `package sample

// Deref returns what a pointer points at.
func Deref(p *int, err error) (int, error) { return *p, err }
`,
		stmt: "return *p, err", rule: ruleReturnZeroNumeric, bytes: "*p",
		types: []string{"int", "error"},
	}, {
		name: "a division by a variable",
		source: `package sample

// Div returns a quotient whose divisor may be zero.
func Div(a, b int, err error) (int, error) { return a / b, err }
`,
		stmt: "return a / b, err", rule: ruleReturnZeroNumeric, bytes: "a / b",
		types: []string{"int", "error"},
	}, {
		name: "a type assertion",
		source: `package sample

// Text returns a value asserted to be a string.
func Text(v any, err error) (string, error) { return v.(string), err }
`,
		stmt: "return v.(string), err", rule: ruleReturnEmptyString, bytes: "v.(string)",
		types: []string{"string", "error"},
	}, {
		name: "a comparison of interface values",
		source: `package sample

// Same reports whether two values are equal, which panics for some of them.
func Same(x, y any, err error) (bool, error) { return x == y, err }
`,
		stmt: "return x == y, err", rule: ruleReturnTrue, bytes: "x == y",
		types: []string{"bool", "error"},
	}, {
		name: "a method value",
		source: `package sample

// counter is a value with a method, so that a method value can be returned.
type counter struct{ n int }

// Load returns the counter's value.
func (c *counter) Load() int { return c.n }

// Loader returns a method value bound to a pointer that may be nil.
func Loader(c *counter, err error) (func() int, error) { return c.Load, err }
`,
		stmt: "return c.Load, err", rule: ruleReturnNil, bytes: "c.Load",
		types: []string{"func() int", "error"},
	}, {
		name: "make with a variable length",
		source: `package sample

// Buffer returns a slice of a length that may be negative.
func Buffer(n int, err error) ([]int, error) { return make([]int, n), err }
`,
		stmt: "return make([]int, n), err", rule: ruleReturnNil, bytes: "make([]int, n)",
		types: []string{"[]int", "error"},
	}, {
		name: "a slice converted to an array pointer",
		source: `package sample

// Head returns the first four bytes of a slice that may be shorter.
func Head(b []byte, err error) (*[4]byte, error) { return (*[4]byte)(b), err }
`,
		stmt: "return (*[4]byte)(b), err", rule: ruleReturnNil, bytes: "(*[4]byte)(b)",
		types: []string{"*[4]byte", "error"},
	}, {
		name: "a map literal keyed by an interface",
		source: `package sample

// Keyed returns a map built around a key that may not be hashable.
func Keyed(k any, err error) (map[any]int, error) { return map[any]int{k: 1}, err }
`,
		stmt: "return map[any]int{k: 1}, err", rule: ruleReturnNil, bytes: "map[any]int{k: 1}",
		types: []string{"map[any]int", "error"},
	}} {
		t.Run(c.name, func(t *testing.T) {
			candidates, src := discoverProbeModule(t, c.source, nil)

			got := candidateWithOriginal(t, candidates, c.rule, c.bytes)
			if got.Guard.Probe != nil {
				t.Errorf("return site = %+v, want none: evaluating %s can panic", got.Guard.Probe, c.bytes)
			}
			if got.Guard.Form == "" {
				t.Error("the candidate lost its guard form as well, so the refusal cost the mutant and not only the probe")
			}

			sibling := candidateWithOriginal(t, candidates, ruleReturnErrToNil, "err").Guard.Probe
			want := &ProbeSite{Form: ProbeFormReturn, Span: spanOf(t, src, c.stmt), Types: c.types, Index: 1}
			if !reflect.DeepEqual(sibling, want) {
				t.Errorf("the error beside it carries %+v, want %+v: one refused result does not refuse the statement",
					sibling, want)
			}
		})
	}
}

func TestProbeSiteAcceptsEveryEffectFreeShape(t *testing.T) {
	candidates, src := discoverProbeModule(t, `package sample

import "time"

// report is the struct these fixtures build by value and through a pointer.
type report struct{ n int }

// Ident returns a variable, the plainest operand there is.
func Ident(count int) int { return count }

// Literal returns a constant written into the source.
func Literal() int { return 42 }

// Absent returns a count beside a map that is nothing, so that the nil is a
// sibling rather than a candidate: a return already spelled as its own
// replacement produces no mutant.
func Absent(seen int) (int, map[string]int) { return seen, nil }

// Field returns a field of a struct value.
func Field(s report) int { return s.n }

// Qualified returns a constant another package declares.
func Qualified() time.Duration { return time.Minute }

// Scaled returns that constant multiplied by another.
func Scaled() time.Duration { return 10 * time.Minute }

// Address returns the address of its own argument.
func Address(x int) *int { return &x }

// Zero returns a count beside a composite literal that is a sibling of it.
func Zero(total int) (int, report) { return total, report{} }

// Built returns a composite literal through a pointer.
func Built(n int) *report { return &report{n: n} }

// Closure returns a function value beside the error that would name what went
// wrong. The literal is a sibling rather than the mutated operand because an
// edit anchored on a function literal has no guard form at all — the mutant
// tree cannot write it, so discovery emits no candidate for it — while creating
// a closure is still an operand the statement beside it may be probed through.
func Closure(a, b int, err error) (func() int, error) { return func() int { return a * b }, err }

// Length returns how long a slice is.
func Length(s []int) int { return len(s) }

// Text returns a string converted from bytes.
func Text(b []byte) string { return string(b) }

// Half returns a value divided by a constant that is not zero.
func Half(x int) int { return x / 2 }

// Shifted returns a value shifted by a constant.
func Shifted(x int) int { return x << 3 }

// Sum returns two values added.
func Sum(a, b int) int { return a + b }

// Missing reports whether a pointer is nil, which comparing cannot panic on.
func Missing(p *report) bool { return p == nil }

// Both reports whether two flags hold.
func Both(ok, found bool) bool { return ok && found }
`, nil)

	for _, c := range []struct {
		name  string
		rule  string
		bytes string
		stmt  string
		types []string
		index int
	}{
		{"an identifier", ruleReturnZeroNumeric, "count", "return count", []string{"int"}, 0},
		{"a literal", ruleReturnZeroNumeric, "42", "return 42", []string{"int"}, 0},
		{"a nil sibling", ruleReturnZeroNumeric, "seen", "return seen, nil", []string{"int", "map[string]int"}, 0},
		{"a field of a struct value", ruleReturnZeroNumeric, "s.n", "return s.n", []string{"int"}, 0},
		{"a qualified constant", ruleReturnZeroNumeric, "time.Minute", "return time.Minute", []string{"time.Duration"}, 0},
		{"a constant expression", ruleReturnZeroNumeric, "10 * time.Minute", "return 10 * time.Minute", []string{"time.Duration"}, 0},
		{"an address", ruleReturnNil, "&x", "return &x", []string{"*int"}, 0},
		{"a composite literal sibling", ruleReturnZeroNumeric, "total", "return total, report{}", []string{"int", "report"}, 0},
		{"a composite literal", ruleReturnNil, "&report{n: n}", "return &report{n: n}", []string{"*report"}, 0},
		{"a function literal sibling", ruleReturnErrToNil, "err",
			"return func() int { return a * b }, err", []string{"func() int", "error"}, 1},
		{"a builtin", ruleReturnZeroNumeric, "len(s)", "return len(s)", []string{"int"}, 0},
		{"a conversion", ruleReturnEmptyString, "string(b)", "return string(b)", []string{"string"}, 0},
		{"a division by a constant", ruleReturnZeroNumeric, "x / 2", "return x / 2", []string{"int"}, 0},
		{"a shift by a constant", ruleReturnZeroNumeric, "x << 3", "return x << 3", []string{"int"}, 0},
		{"an addition", ruleReturnZeroNumeric, "a + b", "return a + b", []string{"int"}, 0},
		{"a pointer comparison", ruleReturnTrue, "p == nil", "return p == nil", []string{"bool"}, 0},
		{"a logical conjunction", ruleReturnTrue, "ok && found", "return ok && found", []string{"bool"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := candidateWithOriginal(t, candidates, c.rule, c.bytes).Guard.Probe
			want := &ProbeSite{Form: ProbeFormReturn, Span: spanOf(t, src, c.stmt), Types: c.types, Index: c.index}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("return site = %+v, want %+v", got, want)
			}
		})
	}
}

func TestProbeSiteRefusesAFloatingResult(t *testing.T) {
	candidates, src := discoverProbeModule(t, `package sample

// Half returns a float, whose negative zero compares equal to zero.
func Half(x float32) float32 { return x }

// Wave returns a complex value beside a count that keeps its hint.
func Wave(n int, z complex128) (int, complex128) { return n, z }
`, nil)

	for _, c := range []struct {
		name  string
		bytes string
	}{
		{"a float result", "x"},
		{"a complex result", "z"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := candidateWithOriginal(t, candidates, ruleReturnZeroNumeric, c.bytes)
			if got.Guard.Probe != nil {
				t.Errorf("return site = %+v, want none: -0.0 != 0 is false", got.Guard.Probe)
			}
			if got.Guard.Form == "" {
				t.Error("the candidate lost its guard form as well, so the refusal cost the mutant and not only the probe")
			}
		})
	}

	t.Run("the integer beside a complex result", func(t *testing.T) {
		got := candidateWithOriginal(t, candidates, ruleReturnZeroNumeric, "n").Guard.Probe
		want := &ProbeSite{Form: ProbeFormReturn, Span: spanOf(t, src, "return n, z"), Types: []string{"int", "complex128"}, Index: 0}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("return site = %+v, want %+v", got, want)
		}
	})
}

func TestTheReturnFormIsKeptToTheFamilyItDescribes(t *testing.T) {
	candidates, src := discoverProbeModule(t, `package sample

// Above reports whether a exceeds b.
func Above(a, b int) bool { return a > b }
`, nil)

	stmt := spanOf(t, src, "return a > b")
	for _, c := range []struct {
		rule string
		want *ProbeSite
	}{
		{ruleReturnTrue, &ProbeSite{Form: ProbeFormReturn, Span: stmt, Types: []string{"bool"}, Index: 0}},
		{ruleReturnFalse, &ProbeSite{Form: ProbeFormReturn, Span: stmt, Types: []string{"bool"}, Index: 0}},
		{"gt-to-ge", &ProbeSite{Form: ProbeFormBool, Span: spanOf(t, src, "a > b")}},
	} {
		t.Run(c.rule, func(t *testing.T) {
			var got Located
			switch c.rule {
			case "gt-to-ge":
				got = candidateWithOriginal(t, candidates, c.rule, ">")
			default:
				got = candidateWithOriginal(t, candidates, c.rule, "a > b")
			}
			if !reflect.DeepEqual(got.Guard.Probe, c.want) {
				t.Errorf("probe site = %+v, want %+v", got.Guard.Probe, c.want)
			}
		})
	}
}

func TestProbeSiteDoesNotDisturbTheMutantSide(t *testing.T) {
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
