// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The discovery tests run the real Go toolchain against the fixture module in
// testdata. There is no build tag on them and no mock underneath them: every
// interesting thing this package does — telling the universe's `true` from a
// shadowed one, telling a type argument from a map index, knowing that a cgo
// file exists on a machine where cgo is switched off — is a fact about
// go/packages and go/types that a fake would have to invent.
package discover

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/P4suta/go-mutants/internal/glob"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// toolchain locates the Go toolchain the fixtures are loaded with.
func toolchain(t *testing.T) gocmd.Toolchain {
	t.Helper()
	located, err := gocmd.Locate(gocmd.Options{})
	if err != nil {
		t.Skipf("no Go toolchain on PATH, so go/packages cannot run: %v", err)
	}
	return located
}

// fixture returns the absolute path of a testdata module.
func fixture(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture %s is missing: %v", name, err)
	}
	return path
}

// discoverFixture runs a discovery over a testdata module, failing the test on
// any error.
func discoverFixture(t *testing.T, name string, opts Options) Result {
	t.Helper()
	opts.SnapshotRoot = fixture(t, name)
	opts.Toolchain = toolchain(t)
	result, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatalf("Discover(%s): %v", name, err)
	}
	return result
}

// patterns compiles test patterns, which are fixed at authoring time.
func patterns(t *testing.T, sources ...string) []glob.Pattern {
	t.Helper()
	compiled, err := CompilePatterns(sources)
	if err != nil {
		t.Fatalf("compiling %v: %v", sources, err)
	}
	return compiled
}

// summarize renders candidates in the compact form the expectation tables are
// written in: everything that identifies the edit, nothing that would have to
// be recounted whenever the fixture gains a line.
func summarize(candidates []Located) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Path+" "+c.Rule.Name+" "+c.Original+"->"+c.Replacement)
	}
	return out
}

// summarizeSkips renders skips the same way.
func summarizeSkips(skips []Skip) []string {
	out := make([]string, 0, len(skips))
	for _, s := range skips {
		out = append(out, s.Path+" "+string(s.Reason)+" "+strconv.Itoa(s.Count))
	}
	return out
}

// equalStrings compares two lists element by element and reports the whole of
// both when they differ, because a discovery bug is much easier to read as two
// tables than as one diff line.
func equalStrings(t *testing.T, got, want []string) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	t.Errorf("got %d entries, want %d\n got: %s\nwant: %s",
		len(got), len(want), strings.Join(got, "\n      "), strings.Join(want, "\n      "))
}

// wantCandidates is every candidate the fixture module holds, in the order
// discovery promises: path, then span, then registry position.
//
// It is one line per candidate and it is long, because the whole catalogue is
// implemented and the whole catalogue is applied to every fixture: a package
// written to exercise the bitwise rules also has assignments to delete and
// results to zero. That is the point of an exact table — a rule that starts
// firing somewhere new shows up here rather than in a count.
var wantCandidates = []string{
	// arith: every arithmetic rule, and the two operand types that keep them
	// away from operators they must not claim.
	"arith/arith.go delete-assignment out[0] = a + b->",
	"arith/arith.go add-to-sub +->-",
	"arith/arith.go delete-assignment out[1] = a - b->",
	"arith/arith.go sub-to-add -->+",
	"arith/arith.go delete-assignment out[2] = a * b->",
	"arith/arith.go mul-to-div *->/",
	"arith/arith.go delete-assignment out[3] = a / b->",
	"arith/arith.go div-to-mul /->*",
	"arith/arith.go delete-assignment out[4] = a % b->",
	"arith/arith.go rem-to-mul %->*",
	"arith/arith.go delete-assignment out[0] = a + b->",
	"arith/arith.go fadd-to-fsub +->-",
	"arith/arith.go delete-assignment out[1] = a - b->",
	"arith/arith.go fsub-to-fadd -->+",
	"arith/arith.go delete-assignment out[2] = a * b->",
	"arith/arith.go fmul-to-fdiv *->/",
	"arith/arith.go delete-assignment out[3] = a / b->",
	"arith/arith.go fdiv-to-fmul /->*",
	"arith/arith.go delete-assignment counts[0] = a + b->",
	"arith/arith.go add-to-sub +->-",
	"arith/arith.go delete-assignment temps[0] = c * d->",
	"arith/arith.go fmul-to-fdiv *->/",
	// The three statements below hold a `+` and a `*` each and no arithmetic
	// rule claims either: their operands are strings and complex numbers.
	// Only the assignment deletion remains.
	"arith/arith.go delete-assignment out[0] = a + b->",
	"arith/arith.go delete-assignment out[0] = a + b->",
	"arith/arith.go delete-assignment out[1] = a * b->",
	// assign: the arithmetic-assignment family, and `s += "!"` excluded.
	"assign/assign.go add-assign-to-sub-assign +=->-=",
	"assign/assign.go sub-assign-to-add-assign -=->+=",
	"assign/assign.go delete-incdec n++->",
	"assign/assign.go incr-to-decr ++->--",
	"assign/assign.go delete-incdec n--->",
	"assign/assign.go decr-to-incr --->++",
	"assign/assign.go add-assign-to-sub-assign +=->-=",
	"assign/assign.go delete-assignment out[0] = n->",
	"assign/assign.go delete-assignment out[1] = int(f)->",
	"assign/assign.go delete-assignment out[0] = s->",
	// bits: the bitwise family. The shift rules move the operator and leave
	// the count alone, which is why `n` never appears as an edit here.
	"bits/bits.go delete-assignment out[0] = a & b->",
	"bits/bits.go band-to-bor &->|",
	"bits/bits.go delete-assignment out[1] = a | b->",
	"bits/bits.go bor-to-band |->&",
	"bits/bits.go delete-assignment out[2] = a ^ b->",
	"bits/bits.go xor-to-band ^->&",
	"bits/bits.go delete-assignment out[3] = a &^ b->",
	"bits/bits.go andnot-to-band &^->&",
	"bits/bits.go delete-assignment out[0] = a << n->",
	"bits/bits.go shl-to-shr <<->>>",
	"bits/bits.go delete-assignment out[1] = a >> n->",
	"bits/bits.go shr-to-shl >>-><<",
	"bits/bits.go delete-assignment out[0] = a & b->",
	"bits/bits.go band-to-bor &->|",
	// compare: the comparison family, now with the condition negation that
	// sits on the same conditions and the empty strings the returns admit.
	"compare/compare.go negate-condition a == b->!(a == b)",
	"compare/compare.go eq-to-neq ==->!=",
	"compare/compare.go return-empty-string \"eq\"->\"\"",
	"compare/compare.go negate-condition a != b->!(a != b)",
	"compare/compare.go neq-to-eq !=->==",
	"compare/compare.go return-empty-string \"ne\"->\"\"",
	"compare/compare.go negate-condition a < b->!(a < b)",
	"compare/compare.go lt-to-le <-><=",
	"compare/compare.go return-empty-string \"lt\"->\"\"",
	"compare/compare.go negate-condition a <= b->!(a <= b)",
	"compare/compare.go le-to-lt <=-><",
	"compare/compare.go return-empty-string \"le\"->\"\"",
	"compare/compare.go negate-condition a > b->!(a > b)",
	"compare/compare.go gt-to-ge >->>=",
	"compare/compare.go return-empty-string \"gt\"->\"\"",
	"compare/compare.go negate-condition a >= b->!(a >= b)",
	"compare/compare.go ge-to-gt >=->>",
	"compare/compare.go return-empty-string \"ge\"->\"\"",
	"compare/compare.go return-empty-string \"none\"->\"\"",
	"compare/compare.go true-to-false true->false",
	"compare/compare.go false-to-true false->true",
	"compare/compare.go return-true on->true",
	"compare/compare.go return-false on->false",
	"compare/compare.go return-true off->true",
	"compare/compare.go return-false off->false",
	"compare/compare.go return-zero-numeric m[true]->0",
	"compare/compare.go true-to-false true->false",
	// deletion: the statement-deletion family. `panic("negative")` is absent
	// and is the one call this family refuses, and `(panic)(reason)` is absent
	// for the same reason written the one way a parenthesis hides.
	"deletion/deletion.go delete-call-statement Log(\"start\")->",
	"deletion/deletion.go delete-assignment total = total + n->",
	"deletion/deletion.go add-to-sub +->-",
	"deletion/deletion.go delete-incdec total++->",
	"deletion/deletion.go incr-to-decr ++->--",
	"deletion/deletion.go delete-assignment m[key] = n->",
	"deletion/deletion.go delete-assignment out[0] = total->",
	"deletion/deletion.go delete-assignment xs = append(xs, n)->",
	"deletion/deletion.go return-nil xs->nil",
	"deletion/deletion.go negate-condition n < 0->!(n < 0)",
	"deletion/deletion.go lt-to-le <-><=",
	"deletion/deletion.go return-zero-numeric n->0",
	// errs: the error-swallowing family, and the line it draws. `err` is an
	// error value and goes to return-err-to-nil; `&Wrapped{Op: op}` is a
	// concrete pointer and goes to return-nil; `p != nil` is not an error
	// comparison and gets no nil-error-branch.
	"errs/errs.go return-empty-string w.Op->\"\"",
	"errs/errs.go return-err-to-nil err->nil",
	"errs/errs.go return-nil &Wrapped{Op: op}->nil",
	"errs/errs.go negate-condition err != nil->!(err != nil)",
	"errs/errs.go nil-error-branch err != nil->false",
	"errs/errs.go neq-to-eq !=->==",
	"errs/errs.go delete-assignment out[0] = 1->",
	"errs/errs.go negate-condition nil != err->!(nil != err)",
	"errs/errs.go nil-error-branch nil != err->false",
	"errs/errs.go neq-to-eq !=->==",
	"errs/errs.go delete-assignment out[1] = 2->",
	"errs/errs.go negate-condition p != nil->!(p != nil)",
	"errs/errs.go neq-to-eq !=->==",
	"errs/errs.go delete-assignment out[0] = 1->",
	// forms: the guard-form fixture. Four of its edits are refused outright
	// and appear in wantSkips instead — see the package's own documentation.
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go return-zero-numeric sum->0",
	"forms/forms.go mul-to-div *->/",
	"forms/forms.go return-zero-numeric product->0",
	"forms/forms.go negate-condition err != nil->!(err != nil)",
	"forms/forms.go nil-error-branch err != nil->false",
	"forms/forms.go neq-to-eq !=->==",
	"forms/forms.go return-err-to-nil err->nil",
	"forms/forms.go return-zero-numeric second->0",
	"forms/forms.go return-err-to-nil err->nil",
	"forms/forms.go return-zero-numeric a->0",
	"forms/forms.go negate-loop-condition i < n->!(i < n)",
	"forms/forms.go lt-to-le <-><=",
	"forms/forms.go add-assign-to-sub-assign +=->-=",
	"forms/forms.go negate-condition half > 0->!(half > 0)",
	"forms/forms.go gt-to-ge >->>=",
	"forms/forms.go return-zero-numeric half->0",
	"forms/forms.go return-empty-string \"zero\"->\"\"",
	"forms/forms.go return-empty-string \"other\"->\"\"",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go sub-to-add -->+",
	"forms/forms.go mul-to-div *->/",
	// The same three statements again, around a call whose result is the
	// universe bool. The edits are the same edits; only the guard form the
	// next table pins is different.
	"forms/forms.go delete-call-statement ok(a + b)->",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go sub-to-add -->+",
	"forms/forms.go mul-to-div *->/",
	"forms/forms.go return-true n > 0->true",
	"forms/forms.go return-false n > 0->false",
	"forms/forms.go gt-to-ge >->>=",
	// The five Form D refusals at the end of the file. Every edit inside one of
	// those declarations is a skip; what is left here is the ordinary code
	// around them, which is still mutated — a refused site removes its own
	// candidates and nothing else in the function.
	"forms/forms.go delete-assignment n = total->",
	"forms/forms.go return-zero-numeric n->0",
	"forms/forms.go return-zero-numeric Limit->0",
	"forms/forms.go return-zero-numeric a + Limit->0",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go return-zero-numeric scale(n)->0",
	"forms/forms.go delete-assignment total.hi = start->",
	"forms/forms.go return-zero-numeric total.hi->0",
	// generics: neither `return a` nor `return b` in Max is a candidate. A
	// type parameter's underlying type is its constraint, which is an
	// interface, and `return nil` would not compile for it.
	"generics/generics.go negate-condition a > b->!(a > b)",
	"generics/generics.go gt-to-ge >->>=",
	"generics/generics.go return-zero-numeric sized[[len([1]bool{false})]byte](v)[0]->0",
	"generics/generics.go return-zero-numeric b.v[0]->0",
	"generics/generics.go return-zero-numeric len(p.key) + len(p.value)->0",
	"generics/generics.go add-to-sub +->-",
	"hidden/hidden.go return-nil &counter{n: n}->nil",
	"hidden/hidden.go return-zero-numeric c.n->0",
	"legacy/legacy.go return-true a == b->true",
	"legacy/legacy.go return-false a == b->false",
	"legacy/legacy.go eq-to-neq ==->!=",
	// negate: the condition-negation and boolean-connective families. `if f`
	// is missing because a named boolean condition has no guard form.
	"negate/negate.go negate-condition ok && a > b->!(ok && a > b)",
	"negate/negate.go and-to-or &&->||",
	"negate/negate.go gt-to-ge >->>=",
	"negate/negate.go delete-assignment out[0] = 1->",
	"negate/negate.go negate-condition ok || a < b->!(ok || a < b)",
	"negate/negate.go or-to-and ||->&&",
	"negate/negate.go lt-to-le <-><=",
	"negate/negate.go delete-assignment out[1] = 2->",
	"negate/negate.go negate-condition !ok->!(!ok)",
	"negate/negate.go remove-negation !ok->ok",
	"negate/negate.go delete-assignment out[0] = 1->",
	"negate/negate.go negate-loop-condition a < b->!(a < b)",
	"negate/negate.go lt-to-le <-><=",
	"negate/negate.go delete-incdec a++->",
	"negate/negate.go incr-to-decr ++->--",
	"negate/negate.go delete-assignment out[0] = a->",
	"negate/negate.go delete-assignment out[0] = 1->",
	"negate/negate.go ge-to-gt >=->>",
	"negate/negate.go return-true f->true",
	"negate/negate.go return-false f->false",
	// returns: the return-replacement family. Zero, None, Bare, and Multi
	// contribute no return candidate at all, each for its own reason.
	"returns/returns.go return-zero-numeric a->0",
	"returns/returns.go return-zero-numeric a->0",
	"returns/returns.go return-empty-string a->\"\"",
	"returns/returns.go return-empty-string a->\"\"",
	"returns/returns.go return-zero-numeric a->0",
	"returns/returns.go return-true a->true",
	"returns/returns.go return-false a->false",
	"returns/returns.go return-nil p->nil",
	"returns/returns.go return-nil s->nil",
	"returns/returns.go return-nil m->nil",
	"returns/returns.go return-nil c->nil",
	"returns/returns.go return-nil f->nil",
	"returns/returns.go return-nil v->nil",
	"returns/returns.go delete-assignment n = 1->",
	"returns/returns.go return-zero-numeric 1->0",
	"returns/returns.go return-zero-numeric 2->0",
	"runes/runes.go negate-condition a > b->!(a > b)",
	"runes/runes.go gt-to-ge >->>=",
	"runes/runes.go return-empty-string label->\"\"",
	"runes/runes.go negate-condition a < b->!(a < b)",
	"runes/runes.go lt-to-le <-><=",
	"runes/runes.go return-empty-string label->\"\"",
	"runes/runes.go return-empty-string \"…\"->\"\"",
	// The two shadowed `true`s in this file are absent as boolean literals on
	// purpose: one is a package-level constant of the package's own, the other
	// a local variable, and neither is the universe constant the rule is
	// about. Both are still integers being returned, and the return family
	// reads the declared result rather than the spelling.
	"shadow/shadow.go return-zero-numeric true->0",
	"shadow/shadow.go add-to-sub +->-",
	"shadow/shadow.go return-zero-numeric true->0",
	"shadow/shadow.go false-to-true false->true",
	"shadow/shadow.go return-true false->true",
	"suppressed/suppressed.go return-zero-numeric len(Buffer{})->0",
	"suppressed/suppressed.go negate-condition limit->!(limit)",
	"suppressed/suppressed.go return-zero-numeric a->0",
	"suppressed/suppressed.go negate-condition ok == true->!(ok == true)",
	"suppressed/suppressed.go eq-to-neq ==->!=",
	"suppressed/suppressed.go true-to-false true->false",
	"suppressed/suppressed.go return-empty-string \"equal and ok\"->\"\"",
	"suppressed/suppressed.go return-empty-string \"not ok\"->\"\"",
	"suppressed/suppressed.go negate-condition v > b->!(v > b)",
	"suppressed/suppressed.go gt-to-ge >->>=",
	"suppressed/suppressed.go return-empty-string \"greater\"->\"\"",
	"suppressed/suppressed.go return-empty-string v->\"\"",
	"suppressed/suppressed.go return-empty-string \"none\"->\"\"",
	"suppressed/suppressed.go return-empty-string \"sent\"->\"\"",
	"suppressed/suppressed.go negate-condition v == true->!(v == true)",
	"suppressed/suppressed.go eq-to-neq ==->!=",
	"suppressed/suppressed.go true-to-false true->false",
	"suppressed/suppressed.go return-empty-string \"received\"->\"\"",
	"suppressed/suppressed.go return-empty-string \"none\"->\"\"",
	"unnameable/unnameable.go return-zero-numeric c.Value()->0",
}

// wantSkips is every recorded reason for the same run.
var wantSkips = []string{
	"cgopkg/cgo.go cgo 1",
	"cgopkg/pure.go cgo 1",
	// Four for the sites no form covers: the `for` post statement, the `if`
	// initialiser, the `switch` tag, and the short declaration that redeclares.
	// Then eight for the declaration form's own refusals — one in the `:=`
	// that shadows and reads what it shadows, two in the `var` that does the
	// same, three across the `var` block whose specs refer to each other, and
	// one each for the two multi-line cuts.
	"forms/forms.go unnameable-decl-type 12",
	"generated/generated.go generated 1",
	// One for the generic function's constraint, one for the generic type's,
	// one for the single explicit type argument, and two for the list form.
	"generics/generics.go type-param 5",
	// The condition of a named boolean type: negatable Go, and no guard form.
	"negate/negate.go unnameable-decl-type 1",
	"suppressed/suppressed.go array-length 2",
	"suppressed/suppressed.go case-label 4",
	"suppressed/suppressed.go const-decl 4",
	"suppressed/suppressed.go package-var-init 5",
	// The reason the reserved name was chosen for: a Form D site whose
	// declared type is another package's unexported one.
	"unnameable/unnameable.go unnameable-decl-type 1",
}

func TestDiscoverFindsEveryImplementedRule(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{})
	equalStrings(t, summarize(result.Candidates), wantCandidates)
}

// TestTheFixtureModuleFiresEveryRule is the guard on the fixtures rather than
// on the code: an exact table proves what discovery found, and only this proves
// that what it found covers the whole catalogue.
//
// It reads the rules out of [SupportedRules] rather than out of a list here, so
// a rule that lands without a fixture fails in the commit that lands it.
func TestTheFixtureModuleFiresEveryRule(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{})
	fired := make(map[string]bool, len(result.Candidates))
	for _, c := range result.Candidates {
		fired[c.Rule.Name] = true
	}
	for _, rule := range SupportedRules() {
		if !fired[rule.Name] {
			t.Errorf("no fixture in testdata/mainmod fires %s", rule)
		}
	}
}

func TestDiscoverRecordsEverySkippedContext(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{})
	equalStrings(t, summarizeSkips(result.Skips), wantSkips)
}

func TestDiscoverReportsTheModule(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{})
	if result.ModulePath != "example.com/mini" {
		t.Errorf("module path = %q, want example.com/mini", result.ModulePath)
	}
	if result.GoVersion != "1.26" {
		t.Errorf("go version = %q, want the module's go directive 1.26", result.GoVersion)
	}
	for _, c := range result.Candidates {
		wantPackage := "example.com/mini/" + filepath.ToSlash(filepath.Dir(c.Path))
		if c.Package != wantPackage {
			t.Errorf("%s: package = %q, want %q", c.Path, c.Package, wantPackage)
		}
	}
}

// assertCandidatesMatchTheFile re-derives everything a candidate claims about
// a file from the bytes on disk: the digest, the text under the span, and the
// line and column counted the way a reader would count them.
//
// It reads the file rather than the syntax tree the candidate came from on
// purpose. The tree is the thing under test; the file is what the instrumenter,
// the diff, and the editor a user jumps from will all see.
func assertCandidatesMatchTheFile(t *testing.T, root string, candidates []Located) {
	t.Helper()
	for _, c := range candidates {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(c.Path)))
		if err != nil {
			t.Fatalf("reading %s: %v", c.Path, err)
		}
		if digest := mutation.Digest(src); digest != c.SourceDigest {
			t.Errorf("%s: source digest = %s, want %s", c.Path, c.SourceDigest, digest)
		}
		covered, err := c.Span.Slice(src)
		if err != nil {
			t.Fatalf("%s %s: %v", c.Path, c.Span, err)
		}
		if string(covered) != c.Original {
			t.Errorf("%s %s: covers %q, want %q", c.Path, c.Span, covered, c.Original)
		}
		if c.Original == c.Replacement {
			t.Errorf("%s %s: replacement is the original", c.Path, c.Span)
		}

		// Line and column are recomputed the same way: count the newlines,
		// then step Column-1 bytes into the line.
		line, ok := sourceLine(t, src, c)
		if !ok {
			continue
		}
		if got := line[c.Column-1 : c.Column-1+len(c.Original)]; got != c.Original {
			t.Errorf("%s:%d:%d: line holds %q, want %q", c.Path, c.Line, c.Column, got, c.Original)
		}
	}
}

// sourceLine returns the line a candidate sits on, reporting false — after
// failing the test — when the position does not address the file at all.
func sourceLine(t *testing.T, src []byte, c Located) (string, bool) {
	t.Helper()
	lines := strings.Split(string(src), "\n")
	if c.Line < 1 || c.Line > len(lines) {
		t.Errorf("%s: line %d is outside the file", c.Path, c.Line)
		return "", false
	}
	line := lines[c.Line-1]
	if c.Column < 1 || c.Column-1+len(c.Original) > len(line) {
		t.Errorf("%s:%d: column %d does not fit the line", c.Path, c.Line, c.Column)
		return "", false
	}
	return line, true
}

// TestDiscoverSpansCoverTheOriginalText is the invariant everything downstream
// rests on, checked here against the bytes on disk rather than against the
// syntax tree the candidate came from.
func TestDiscoverSpansCoverTheOriginalText(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{})
	if len(result.Candidates) == 0 {
		t.Fatal("no candidates to check")
	}
	assertCandidatesMatchTheFile(t, fixture(t, "mainmod"), result.Candidates)
}

// TestDiscoverColumnsAreBytesNotRunes pins what [Located.Column] means, and
// pins the fixture that makes the question answerable at all.
//
// The test above would now catch a rune column too, because the runes package
// is part of the module it walks — but only for as long as that package keeps
// a multi-byte character ahead of a candidate, and nothing in it says so. This
// one says so: the moment no candidate's byte column and rune column disagree,
// the fixture has stopped testing the contract, and that is a failure here
// rather than a test that silently proves nothing.
func TestDiscoverColumnsAreBytesNotRunes(t *testing.T) {
	root := fixture(t, "mainmod")
	result := discoverFixture(t, "mainmod", Options{Include: patterns(t, "runes/**")})
	if len(result.Candidates) == 0 {
		t.Fatal("the runes fixture produced no candidates")
	}
	assertCandidatesMatchTheFile(t, root, result.Candidates)

	diverged := 0
	for _, c := range result.Candidates {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(c.Path)))
		if err != nil {
			t.Fatalf("reading %s: %v", c.Path, err)
		}
		line, ok := sourceLine(t, src, c)
		if !ok {
			continue
		}
		if runeColumn := utf8.RuneCountInString(line[:c.Column-1]) + 1; runeColumn != c.Column {
			diverged++
		}
	}
	if diverged == 0 {
		t.Error("no candidate's byte column differs from its rune column, so this fixture no longer tests the contract")
	}
}

// summarizeGuards renders the site hint of every candidate the way the guard
// expectation table is written: the edit, then the form, the bytes the guard
// replaces, and the types a Form D site has to declare.
func summarizeGuards(t *testing.T, root string, candidates []Located) []string {
	t.Helper()
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(c.Path)))
		if err != nil {
			t.Fatalf("reading %s: %v", c.Path, err)
		}
		site, err := c.Guard.SiteSpan.Slice(src)
		if err != nil {
			t.Fatalf("%s %s: %v", c.Path, c.Guard.SiteSpan, err)
		}
		declared := make([]string, 0, len(c.Guard.DeclTypes))
		for _, decl := range c.Guard.DeclTypes {
			declared = append(declared, decl.Name+" "+decl.Type)
		}
		out = append(out, c.Rule.Name+" "+c.Original+" | "+string(c.Guard.Form)+
			" "+string(site)+" ["+strings.Join(declared, ", ")+"]")
	}
	return out
}

// wantFormsGuards is the exact site hint of every candidate in the guard
// fixture, which is the contract internal/instrument consumes.
//
// The three forms are all here and each one is here for a reason the fixture
// spells out in prose beside it: a bool selector where the edit sits in a
// bool-valued expression, a statement guard where it does not, and a
// declaration rewrite where the statement declares. The four sites this file
// refuses do not appear — they are the unnameable-decl-type skips in
// [wantSkips].
//
// The last block is the one place bool-valued and Form C part company. A call
// returning the universe bool is a Form C site everywhere a value is wanted —
// `return n > 0` below is one — and in none of the three statement positions,
// where a guard would be a bool nothing uses or a `defer` operand that is not a
// call. Those read `| S` with the whole statement as the site, and an entry
// there that reads `| C` is a hint no instrumenter could rewrite into Go.
var wantFormsGuards = []string{
	"add-to-sub + | D var sum = a + b [sum int]",
	"return-zero-numeric sum | S return sum []",
	"mul-to-div * | D product := a * b [product int]",
	"return-zero-numeric product | S return product []",
	"negate-condition err != nil | C err != nil []",
	"nil-error-branch err != nil | C err != nil []",
	"neq-to-eq != | C err != nil []",
	"return-err-to-nil err | S return 0, err []",
	"return-zero-numeric second | S return second, err []",
	"return-err-to-nil err | S return second, err []",
	"return-zero-numeric a | S return a, nil []",
	"negate-loop-condition i < n | C i < n []",
	"lt-to-le < | C i < n []",
	"add-assign-to-sub-assign += | S out[0] += i []",
	"negate-condition half > 0 | C half > 0 []",
	"gt-to-ge > | C half > 0 []",
	"return-zero-numeric half | S return half []",
	"return-empty-string \"zero\" | S return \"zero\" []",
	"return-empty-string \"other\" | S return \"other\" []",
	"add-to-sub + | S ch <- a + b []",
	"sub-to-add - | S defer sink(a - b) []",
	"mul-to-div * | S go sink(a * b) []",
	"delete-call-statement ok(a + b) | S ok(a + b) []",
	"add-to-sub + | S ok(a + b) []",
	"sub-to-add - | S defer ok(a - b) []",
	"mul-to-div * | S go ok(a * b) []",
	"return-true n > 0 | C n > 0 []",
	"return-false n > 0 | C n > 0 []",
	"gt-to-ge > | C n > 0 []",
	// The tail of the file is five declarations Form D refuses, and not one of
	// their sites is here: every hint below belongs to the ordinary code beside
	// them. That is the claim those functions exist to make — a refusal removes
	// its own candidates and leaves the rest of the function mutable — and it is
	// only visible as an absence, so the entries that would be here if a
	// refusal stopped working are `mul-to-div * | D total := total * 2`,
	// `add-to-sub + | D var Limit = Limit + n*2`, the `var` block of CrossSpec,
	// and the two multi-line cuts in Widen and Widest.
	"delete-assignment n = total | S n = total []",
	"return-zero-numeric n | S return n []",
	"return-zero-numeric Limit | S return Limit []",
	"return-zero-numeric a + Limit | S return a + Limit []",
	"add-to-sub + | S return a + Limit []",
	"return-zero-numeric scale(n) | S return scale(n) []",
	"delete-assignment total.hi = start | S total.hi = start []",
	"return-zero-numeric total.hi | S return total.hi []",
}

// TestDiscoverEmitsTheGuardHints pins the Form D site hint contract on the
// fixture written for it.
func TestDiscoverEmitsTheGuardHints(t *testing.T) {
	root := fixture(t, "mainmod")
	result := discoverFixture(t, "mainmod", Options{Include: patterns(t, "forms/**")})
	equalStrings(t, summarizeGuards(t, root, result.Candidates), wantFormsGuards)
}

// TestDiscoverNamesADeclaredTypeFromItsOwnPackage is the qualifier's own case,
// separated from the table above because it is the one that would be wrong in
// a way a table could not show: a type of the package under test has to render
// unqualified, and `negate.Flag` spliced into package negate does not compile.
func TestDiscoverNamesADeclaredTypeFromItsOwnPackage(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{Include: patterns(t, "negate/**")})
	got, ok := candidateOf(t, result.Candidates, "ge-to-gt")
	if !ok {
		t.Fatal("the negate fixture produced no ge-to-gt candidate")
	}
	if got.Guard.Form != GuardFormD {
		t.Fatalf("guard form = %q, want %q: the comparison's result has to be a Flag, "+
			"so a bool selector cannot hold it", got.Guard.Form, GuardFormD)
	}
	want := []DeclType{{Name: "f", Type: "Flag"}}
	if !reflect.DeepEqual(got.Guard.DeclTypes, want) {
		t.Errorf("declared types = %v, want %v", got.Guard.DeclTypes, want)
	}
}

// TestEveryCandidateCarriesAUsableGuard is the invariant the hint has to hold
// everywhere, checked over the whole fixture module rather than over the one
// package written to exercise it.
func TestEveryCandidateCarriesAUsableGuard(t *testing.T) {
	root := fixture(t, "mainmod")
	result := discoverFixture(t, "mainmod", Options{})
	if len(result.Candidates) == 0 {
		t.Fatal("no candidates to check")
	}
	forms := make(map[GuardForm]int)
	for _, c := range result.Candidates {
		switch c.Guard.Form {
		case GuardFormC, GuardFormS, GuardFormD:
			forms[c.Guard.Form]++
		default:
			t.Errorf("%s %s: guard form %q is not one of the three", c.Path, c.Span, c.Guard.Form)
			continue
		}
		if !c.Guard.SiteSpan.Contains(c.Span) {
			t.Errorf("%s: the guard site %s does not contain the edit %s", c.Path, c.Guard.SiteSpan, c.Span)
		}
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(c.Path)))
		if err != nil {
			t.Fatalf("reading %s: %v", c.Path, err)
		}
		if _, err := c.Guard.SiteSpan.Slice(src); err != nil {
			t.Errorf("%s: the guard site %s is not inside the file: %v", c.Path, c.Guard.SiteSpan, err)
		}
		if c.Guard.Form != GuardFormD && len(c.Guard.DeclTypes) != 0 {
			t.Errorf("%s %s: a Form %s site declares %v", c.Path, c.Span, c.Guard.Form, c.Guard.DeclTypes)
		}
		for _, decl := range c.Guard.DeclTypes {
			if decl.Name == "" || decl.Type == "" {
				t.Errorf("%s %s: incomplete declared type %+v", c.Path, c.Span, decl)
			}
		}
	}
	// All three forms have to be reachable, or the fixtures have stopped
	// covering the contract whatever the counts above say.
	for _, form := range []GuardForm{GuardFormC, GuardFormS, GuardFormD} {
		if forms[form] == 0 {
			t.Errorf("no candidate in the fixture module carries a Form %s hint", form)
		}
	}
}

// TestDiscoverIsDeterministic is the property the whole catalogue depends on:
// two passes over the same bytes agree field for field, maps and directory
// order included.
func TestDiscoverIsDeterministic(t *testing.T) {
	first := discoverFixture(t, "mainmod", Options{})
	second := discoverFixture(t, "mainmod", Options{})
	if !reflect.DeepEqual(first, second) {
		t.Error("two discoveries over the same tree disagree")
	}
}

// TestDiscoverNeverMutatesTestFiles covers the one exclusion that is
// structural: a test file is built, type-checked, and run, is never mutated,
// and is never recorded as a skip either, because it was never a decision.
func TestDiscoverNeverMutatesTestFiles(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{})
	for _, c := range result.Candidates {
		if strings.HasSuffix(c.Path, "_test.go") {
			t.Errorf("test file produced a candidate: %s", c.Path)
		}
	}
	for _, s := range result.Skips {
		if strings.HasSuffix(s.Path, "_test.go") {
			t.Errorf("test file was recorded as a skip: %s %s", s.Path, s.Reason)
		}
	}
}

// crlfModule is a whole module written with CRLF line endings.
//
// It is spelled out here instead of being checked into testdata for two
// reasons. `gofmt -l .` walks testdata and lists any Go file whose line endings
// are not LF, so a checked-in CRLF fixture would fail this repository's own
// format gate; and a CRLF file on disk is one editor, one `gofmt -w`, one
// helpful tool away from being normalised to LF without anybody noticing that
// the fixture had stopped testing anything. Written as `\r\n`, the line endings
// are visible in the source and cannot drift.
var crlfModule = map[string]string{
	"go.mod": lines(
		"// SPDX-FileCopyrightText: 2026 go-mutants contributors",
		"// SPDX-License-Identifier: MIT OR Apache-2.0",
		"",
		"module example.com/crlf",
		"",
		"go 1.26",
	),
	"gen.go": lines(
		"// SPDX-FileCopyrightText: 2026 go-mutants contributors",
		"// SPDX-License-Identifier: MIT OR Apache-2.0",
		"",
		"// Code generated by mini-gen. DO NOT EDIT.",
		"",
		"package crlf",
		"",
		"// Always would be a candidate in a file anybody was allowed to edit.",
		"func Always() bool { return true }",
	),
	"plain.go": lines(
		"// SPDX-FileCopyrightText: 2026 go-mutants contributors",
		"// SPDX-License-Identifier: MIT OR Apache-2.0",
		"",
		"package crlf",
		"",
		"// Greater holds the live candidate beside the generated file, and holds",
		"// it ten lines down on purpose: every line above it carries a carriage",
		"// return, so an offset that counted a line ending as one byte would put",
		"// the span ten bytes short of the operator and the reported line with",
		"// it. A candidate on the first line would prove none of that.",
		"func Greater(a, b int) bool {",
		"	return a > b",
		"}",
	),
}

// The coordinates of the one candidate in crlfModule's plain.go, counted off
// the fixture above: the operator is on the twelfth line, and the eleventh
// byte of it — one tab, then "return a ".
const (
	crlfCandidateLine   = 12
	crlfCandidateColumn = 11
)

// lines joins fixture lines with CRLF, including a trailing one.
func lines(text ...string) string { return strings.Join(text, "\r\n") + "\r\n" }

// writeModule materialises a module into a fresh temporary directory.
//
// The root is resolved through [filepath.EvalSymlinks] because the go command
// reports the module directory in its own spelling — a temporary directory is
// behind a symlink on macOS and can be behind a short name on Windows — and
// discovery insists the two name the same directory.
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temporary module root: %v", err)
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("creating the directory for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return root
}

// TestDiscoverReadsCRLFSource drives CRLF source through the real loader, which
// is the only way to find out what go/scanner hands back for a comment on a
// Windows checkout — the in-memory [TestIsGenerated] case asserts the same
// thing about a string this package parsed itself.
//
// Both halves matter. The generated marker has to be recognised through the
// carriage return, or every generated file in a Windows checkout would be
// mutated; and the candidate beside it has to carry a span, a digest, and a
// line and column measured in the file's own bytes, carriage returns included.
//
// The line and column are asserted against literals rather than recomputed.
// Re-deriving them from the same file the discovery read would agree with any
// consistent miscount of the line endings, and eleven carriage returns ahead of
// the operator is exactly the drift that would hide there.
func TestDiscoverReadsCRLFSource(t *testing.T) {
	root := writeModule(t, crlfModule)
	// The whole test rests on the fixture's line endings, and nothing else
	// here would notice if they were quietly normalised.
	if src, err := os.ReadFile(filepath.Join(root, "plain.go")); err != nil {
		t.Fatalf("reading the fixture back: %v", err)
	} else if !strings.Contains(string(src), "\r\n") {
		t.Fatal("the fixture no longer has CRLF line endings, so this test proves nothing")
	}
	result, err := Discover(context.Background(), Options{SnapshotRoot: root, Toolchain: toolchain(t)})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !hasSkip(result.Skips, "gen.go", SkipGenerated, 1) {
		t.Errorf("the CRLF generated file was not skipped: %v", summarizeSkips(result.Skips))
	}
	// The comparison is also the whole of a boolean result, so the return
	// family offers both of its boolean replacements over the same bytes; they
	// sort ahead of the operator because their span starts earlier.
	equalStrings(t, summarize(result.Candidates), []string{
		"plain.go return-true a > b->true",
		"plain.go return-false a > b->false",
		"plain.go gt-to-ge >->>=",
	})
	assertCandidatesMatchTheFile(t, root, result.Candidates)
	got, ok := candidateOf(t, result.Candidates, "gt-to-ge")
	if !ok {
		t.Fatal("the CRLF fixture produced no gt-to-ge candidate")
	}
	if got.Line != crlfCandidateLine || got.Column != crlfCandidateColumn {
		t.Errorf("the operator is reported at %d:%d, want %d:%d",
			got.Line, got.Column, crlfCandidateLine, crlfCandidateColumn)
	}
}

// candidateOf returns the one candidate of a named rule, and insists there is
// only one: a caller naming a rule to single out a site would otherwise get
// whichever of several sorted first, and would keep getting it after the
// fixture grew a second one.
func candidateOf(t *testing.T, candidates []Located, rule string) (Located, bool) {
	t.Helper()
	var found []Located
	for _, c := range candidates {
		if c.Rule.Name == rule {
			found = append(found, c)
		}
	}
	if len(found) > 1 {
		t.Fatalf("%d candidates carry %s, so naming the rule does not name a site: %v",
			len(found), rule, summarize(found))
	}
	if len(found) == 0 {
		return Located{}, false
	}
	return found[0], true
}

func TestDiscoverHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Discover(ctx, Options{SnapshotRoot: fixture(t, "mainmod"), Toolchain: toolchain(t)})
	if CodeOf(err) != CodeLoadFailed {
		t.Fatalf("code = %q, want %s (err %v)", CodeOf(err), CodeLoadFailed, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the cancellation is not reachable with errors.Is: %v", err)
	}
}

func TestDiscoverExcludesByPattern(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{Exclude: patterns(t, "legacy/**")})
	for _, c := range result.Candidates {
		if strings.HasPrefix(c.Path, "legacy/") {
			t.Errorf("excluded file produced a candidate: %s", c.Path)
		}
	}
	if !hasSkip(result.Skips, "legacy/legacy.go", SkipExcluded, 1) {
		t.Errorf("no excluded skip for legacy/legacy.go: %v", summarizeSkips(result.Skips))
	}
}

func TestDiscoverIncludeNarrowsToOnePackage(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{Include: patterns(t, "compare/**")})
	for _, c := range result.Candidates {
		if !strings.HasPrefix(c.Path, "compare/") {
			t.Errorf("candidate outside the include set: %s", c.Path)
		}
	}
	if len(result.Candidates) != 27 {
		t.Errorf("got %d candidates, want the 27 in compare: %v", len(result.Candidates), summarize(result.Candidates))
	}
	// Everything else becomes an excluded skip rather than disappearing.
	for _, path := range []string{
		"legacy/legacy.go", "generics/generics.go", "suppressed/suppressed.go",
		"cgopkg/cgo.go", "cgopkg/pure.go", "generated/generated.go",
		"runes/runes.go", "shadow/shadow.go", "arith/arith.go", "assign/assign.go",
		"bits/bits.go", "deletion/deletion.go", "errs/errs.go", "forms/forms.go",
		"hidden/hidden.go", "negate/negate.go", "returns/returns.go",
		"unnameable/unnameable.go",
	} {
		if !hasSkip(result.Skips, path, SkipExcluded, 1) {
			t.Errorf("no excluded skip for %s: %v", path, summarizeSkips(result.Skips))
		}
	}
}

// hasSkip reports whether the exact skip is present.
func hasSkip(skips []Skip, path string, reason SkipReason, count int) bool {
	for _, s := range skips {
		if s.Path == path && s.Reason == reason && s.Count == count {
			return true
		}
	}
	return false
}

func TestDiscoverAppliesOnlyTheSelectedRules(t *testing.T) {
	registry := mutation.CanonicalRegistry()
	rule, ok := registry.Lookup("eq-to-neq")
	if !ok {
		t.Fatal("the canonical registry has no eq-to-neq")
	}
	result := discoverFixture(t, "mainmod", Options{Rules: []mutation.Rule{rule}})
	for _, c := range result.Candidates {
		if c.Rule.Name != "eq-to-neq" {
			t.Errorf("unselected rule produced a candidate: %s at %s", c.Rule.Name, c.Path)
		}
	}
	if len(result.Candidates) != 4 {
		t.Errorf("got %d eq-to-neq candidates, want 4: %v", len(result.Candidates), summarize(result.Candidates))
	}
}

// TestDiscoverSelectsWithinAFamily narrows to one rule of a family that has
// several, which is where a table keyed by operator token could quietly select
// the whole table instead of the one entry that was asked for.
func TestDiscoverSelectsWithinAFamily(t *testing.T) {
	rule, ok := mutation.CanonicalRegistry().Lookup("add-to-sub")
	if !ok {
		t.Fatal("the canonical registry has no add-to-sub")
	}
	result := discoverFixture(t, "mainmod", Options{Rules: []mutation.Rule{rule}})
	if len(result.Candidates) == 0 {
		t.Fatal("selecting add-to-sub found nothing")
	}
	for _, c := range result.Candidates {
		if c.Rule.Name != "add-to-sub" {
			t.Errorf("unselected rule produced a candidate: %s at %s", c.Rule.Name, c.Path)
		}
	}
}

func TestDiscoverRefusesAnUnknownRule(t *testing.T) {
	_, err := Discover(context.Background(), Options{
		SnapshotRoot: fixture(t, "mainmod"),
		Toolchain:    toolchain(t),
		Rules:        []mutation.Rule{{Family: mutation.FamilyComparison, Name: "eq-to-neq", Version: 99, Tier: mutation.TierBalanced}},
	})
	if CodeOf(err) != CodeUnknownRule {
		t.Fatalf("code = %q, want %s (err %v)", CodeOf(err), CodeUnknownRule, err)
	}
}

func TestDiscoverRefusesAWorkspace(t *testing.T) {
	_, err := Discover(context.Background(), Options{
		SnapshotRoot: fixture(t, "workspace"),
		Toolchain:    toolchain(t),
	})
	if CodeOf(err) != CodeWorkspace {
		t.Fatalf("code = %q, want %s (err %v)", CodeOf(err), CodeWorkspace, err)
	}
	if !strings.Contains(err.Error(), "multi-module workspaces are not yet supported") {
		t.Errorf("message does not say what is unsupported: %v", err)
	}
}

// TestDiscoverIgnoresAWorkspaceOutsideTheSnapshot is the other half of the
// workspace guarantee, and the half a `go.work` at the snapshot root cannot
// state: the go command finds a workspace file by walking up from the module
// and by being pointed at one with $GOWORK, and neither of those files is part
// of the snapshot whose digest the whole run is keyed on.
//
// The fixture is the discriminator. testdata/workspace/first imports
// example.com/second and requires it nowhere, so the module loads if and only
// if the workspace one directory above it is in effect — which makes "the same
// failure under both ways of reaching that file" a fact about the loader's
// environment rather than about the fixture.
func TestDiscoverIgnoresAWorkspaceOutsideTheSnapshot(t *testing.T) {
	workspaceRoot := fixture(t, "workspace")
	root := filepath.Join(workspaceRoot, "first")
	located := toolchain(t)

	// The two ways the go command reaches a workspace file that is not in the
	// snapshot, in a fixed order so that the two outcomes can be compared.
	ways := []struct {
		name   string
		gowork string
	}{
		{"found by walking up", ""},
		{"named by $GOWORK", filepath.Join(workspaceRoot, WorkspaceFile)},
	}
	codes := make([]Code, 0, len(ways))
	for _, way := range ways {
		// t.Setenv either way, so that the cleanup it registers puts the
		// developer's own GOWORK back afterwards.
		t.Setenv("GOWORK", way.gowork)
		if way.gowork == "" {
			if err := os.Unsetenv("GOWORK"); err != nil {
				t.Fatalf("unsetting GOWORK: %v", err)
			}
		}
		result, err := Discover(context.Background(), Options{SnapshotRoot: root, Toolchain: located})
		code := CodeOf(err)
		if code == "" {
			t.Fatalf("%s: the module loaded, so the workspace outside the snapshot was obeyed", way.name)
		}
		if !strings.Contains(err.Error(), "example.com/second") {
			t.Errorf("%s: the message does not name the module the workspace would have supplied: %v", way.name, err)
		}
		if len(result.Candidates) != 0 {
			t.Errorf("%s: candidates survived: %v", way.name, summarize(result.Candidates))
		}
		codes = append(codes, code)
	}
	// Which failure it is, is the go command's business: today it is
	// [CodePackageErrors], because `go list` reports the unresolvable import
	// against the package, and a caller running with a different module mode
	// could see the loader itself give up instead. That both ways of reaching
	// the same workspace file fail identically is this package's business.
	if codes[0] != codes[1] {
		t.Errorf("%s reports %s but %s reports %s, so the workspace file still decides something",
			ways[0].name, codes[0], ways[1].name, codes[1])
	}
}

func TestDiscoverRequiresATreeThatCompiles(t *testing.T) {
	_, err := Discover(context.Background(), Options{
		SnapshotRoot: fixture(t, "broken"),
		Toolchain:    toolchain(t),
	})
	if CodeOf(err) != CodePackageErrors {
		t.Fatalf("code = %q, want %s (err %v)", CodeOf(err), CodePackageErrors, err)
	}
	if !strings.Contains(err.Error(), "broken.go") {
		t.Errorf("message does not name the file that fails: %v", err)
	}
}

// TestDiscoverSkipsCgoPackages runs the same assertion under both settings of
// CGO_ENABLED, which is what makes it deterministic on a machine with no C
// compiler — and what covers the branch the local machine would otherwise
// never take.
//
// The two settings reach the same verdict along different paths. With cgo off,
// the go command excludes cgo.go by build constraint and it survives only in
// the package's ignored files; with cgo on, cgo.go is a cgo file whose compiled
// form lives in the build cache, and here, with no C compiler installed, the
// package fails to build at all. Discovery recognises the package from the
// import in the source in both cases, skips every file it owns, and never lets
// its build failure reach the load gate.
//
// The fixture's plain pure.go is what makes the cgo-off case testable at all:
// a directory whose *every* file is excluded by build constraints is not
// matched by `./...`, so a pure cgo package simply does not exist as far as the
// go command is concerned when cgo is off. Discovery follows the build
// configuration there and reports nothing, because nothing was in the build.
func TestDiscoverSkipsCgoPackages(t *testing.T) {
	for _, enabled := range []string{"0", "1"} {
		t.Run("CGO_ENABLED="+enabled, func(t *testing.T) {
			t.Setenv("CGO_ENABLED", enabled)
			result := discoverFixture(t, "mainmod", Options{})
			for _, path := range []string{"cgopkg/cgo.go", "cgopkg/pure.go"} {
				if !hasSkip(result.Skips, path, SkipCgo, 1) {
					t.Errorf("no cgo skip for %s: %v", path, summarizeSkips(result.Skips))
				}
			}
			for _, c := range result.Candidates {
				if strings.HasPrefix(c.Path, "cgopkg/") {
					t.Errorf("cgo package produced a candidate: %s", c.Path)
				}
			}
		})
	}
}

func TestDiscoverRejectsAnUnusableRoot(t *testing.T) {
	cases := map[string]string{
		"empty":   "",
		"missing": filepath.Join(t.TempDir(), "nowhere"),
		"file":    filepath.Join(fixture(t, "mainmod"), "go.mod"),
	}
	for name, root := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Discover(context.Background(), Options{SnapshotRoot: root})
			if CodeOf(err) != CodeSnapshotRoot {
				t.Fatalf("code = %q, want %s (err %v)", CodeOf(err), CodeSnapshotRoot, err)
			}
		})
	}
}

func TestDiscoverRejectsARootThatIsNotAModuleRoot(t *testing.T) {
	_, err := Discover(context.Background(), Options{
		SnapshotRoot: filepath.Join(fixture(t, "mainmod"), "compare"),
		Toolchain:    toolchain(t),
	})
	if CodeOf(err) != CodeModuleNotFound {
		t.Fatalf("code = %q, want %s (err %v)", CodeOf(err), CodeModuleNotFound, err)
	}
}

// TestBuildCatalogAcceptsEveryCandidate holds the whole of discovery's output
// to what the catalogue will take: every candidate is either catalogued or
// deduplicated against one that was, and nothing is lost between the two.
//
// Duplicates are expected now, and are not a defect. Two families can propose
// the same bytes at the same span — `return-true` over a `false` literal is the
// same edit `false-to-true` makes — and the catalogue resolves that in favour
// of the more local rule. That resolution is documented in docs/operators.md
// and asserted here, because a duplicate arising for any *other* reason would
// mean two rules quietly doing one rule's work.
func TestBuildCatalogAcceptsEveryCandidate(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{})
	catalog, err := BuildCatalog(result)
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	duplicates := catalog.Duplicates()
	if catalog.Len()+len(duplicates) != len(result.Candidates) {
		t.Errorf("catalogue holds %d mutants and %d duplicates, want %d candidates between them",
			catalog.Len(), len(duplicates), len(result.Candidates))
	}
	for _, duplicate := range duplicates {
		if duplicate.Reason != mutation.DuplicateShadowed {
			t.Errorf("%s lost deduplication for %q, want %q",
				duplicate.Dropped.Rule, duplicate.Reason, mutation.DuplicateShadowed)
		}
	}
	for _, m := range catalog.Mutants() {
		if !mutation.IsID(m.ID) {
			t.Errorf("%s is not a mutant id", m.ID)
		}
	}
}

func TestSupportedRulesAreRegisteredAndComplete(t *testing.T) {
	registry := mutation.CanonicalRegistry()
	rules := SupportedRules()
	if len(rules) != mutation.CanonicalRuleCount {
		t.Fatalf("got %d supported rules, want the whole catalogue of %d", len(rules), mutation.CanonicalRuleCount)
	}
	for _, rule := range rules {
		if err := registry.Verify(rule); err != nil {
			t.Errorf("%s is not the registered rule: %v", rule, err)
		}
	}
	// Registry order, which is what the candidate ordering leans on.
	positions := make([]int, 0, len(rules))
	for _, rule := range rules {
		position, ok := registry.Position(rule.Name)
		if !ok {
			t.Fatalf("%s has no registry position", rule)
		}
		positions = append(positions, position)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] >= positions[i] {
			t.Fatalf("supported rules are not in registry order: %v", positions)
		}
	}
}

func TestCompilePatternsReportsBadSyntax(t *testing.T) {
	compiled, err := CompilePatterns([]string{"internal/**", "*.go"})
	if err != nil {
		t.Fatalf("compiling valid patterns: %v", err)
	}
	if len(compiled) != 2 {
		t.Fatalf("got %d patterns, want 2", len(compiled))
	}
	_, err = CompilePatterns([]string{"internal/**", "a//b"})
	if CodeOf(err) != CodePattern {
		t.Fatalf("code = %q, want %s (err %v)", CodeOf(err), CodePattern, err)
	}
	var syntax *glob.SyntaxError
	if !errors.As(err, &syntax) {
		t.Errorf("the glob syntax error is not reachable: %v", err)
	}
}

func TestCodesAreUniqueAndInBlock(t *testing.T) {
	seen := make(map[Code]bool)
	for _, code := range Codes() {
		if seen[code] {
			t.Errorf("%s is defined twice", code)
		}
		seen[code] = true
		if !strings.HasPrefix(string(code), "GOM41") || len(code) != 7 {
			t.Errorf("%s is outside the GOM41xx block this package owns", code)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no codes are registered")
	}
}
