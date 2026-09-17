// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/P4suta/go-mutants/internal/glob"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
)

func toolchain(t *testing.T) gocmd.Toolchain {
	t.Helper()
	testkit.GoBinary(t)
	located, err := gocmd.Locate(gocmd.Options{})
	if err != nil {
		t.Fatalf("locating the Go toolchain go/packages will load the fixtures with: %v", err)
	}
	return located
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	path, err := fixturePath(name)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return path
}

func fixturePath(name string) (string, error) {
	switch {
	case name == "", name == ".", name == "..":
		return "", fmt.Errorf("%q is not a name inside testdata/", name)
	case name != filepath.Base(name), name != filepath.Clean(name):
		return "", fmt.Errorf("%q is not a name directly inside testdata/", name)
	}
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		return "", fmt.Errorf("resolving the fixture path: %w", err)
	}
	for _, marker := range []string{"go.mod", "go.work"} {
		if _, statErr := os.Stat(filepath.Join(path, marker)); statErr == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("is not a tree a `go` command can be pointed at: "+
		"%s holds neither a go.mod nor a go.work", path)
}

func TestFixturePathRefusesWhatIsNotAFixtureModule(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", ".", "..", "nested/name", "discover.go"} {
		if path, err := fixturePath(name); err == nil {
			t.Errorf("fixturePath(%q) resolved %s, and that is not a fixture module", name, path)
		}
	}
	for _, name := range []string{"mainmod", "workspace"} {
		if _, err := fixturePath(name); err != nil {
			t.Errorf("fixturePath refused %q, a fixture this package has: %v", name, err)
		}
	}
}

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

func TestDiscoverLoadsOnlySelectedPackages(t *testing.T) {
	t.Parallel()
	result := discoverFixture(t, "mainmod", Options{Packages: []string{"./arith"}})
	if len(result.Candidates) == 0 {
		t.Fatal("selected package produced no candidates")
	}
	for _, candidate := range result.Candidates {
		if !strings.HasPrefix(candidate.Path, "arith/") {
			t.Fatalf("selected discovery returned %q", candidate.Path)
		}
	}
	for _, skipped := range result.Skips {
		if !strings.HasPrefix(skipped.Path, "arith/") {
			t.Fatalf("selected discovery skipped %q", skipped.Path)
		}
	}
}

func patterns(t *testing.T, sources ...string) []glob.Pattern {
	t.Helper()
	compiled, err := CompilePatterns(sources)
	if err != nil {
		t.Fatalf("compiling %v: %v", sources, err)
	}
	return compiled
}

func summarize(candidates []Located) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Path+" "+c.Rule.Name+" "+c.Original+"->"+c.Replacement)
	}
	return out
}

func summarizeSkips(skips []Skip) []string {
	out := make([]string, 0, len(skips))
	for _, s := range skips {
		out = append(out, s.Path+" "+string(s.Reason)+" "+strconv.Itoa(s.Count))
	}
	return out
}

func equalStrings(t *testing.T, got, want []string) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	t.Errorf("got %d entries, want %d\n got: %s\nwant: %s",
		len(got), len(want), strings.Join(got, "\n      "), strings.Join(want, "\n      "))
}

var wantCandidates = []string{
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
	"arith/arith.go delete-assignment out[0] = a + b->",
	"arith/arith.go delete-assignment out[0] = a + b->",
	"arith/arith.go delete-assignment out[1] = a * b->",
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
	"carrier/carrier.go return-zero-numeric Extent(b.n)->0",
	"carrier/carrier.go return-zero-numeric int(e)->0",
	"carrier/carrier.go return-zero-numeric deeper.Of(n)->0",
	"compare/compare.go negate-condition a == b->!(a == b)",
	"compare/compare.go condition-to-true a == b->true",
	"compare/compare.go condition-to-false a == b->false",
	"compare/compare.go eq-to-neq ==->!=",
	"compare/compare.go return-empty-string \"eq\"->\"\"",
	"compare/compare.go negate-condition a != b->!(a != b)",
	"compare/compare.go condition-to-true a != b->true",
	"compare/compare.go condition-to-false a != b->false",
	"compare/compare.go neq-to-eq !=->==",
	"compare/compare.go return-empty-string \"ne\"->\"\"",
	"compare/compare.go negate-condition a < b->!(a < b)",
	"compare/compare.go condition-to-true a < b->true",
	"compare/compare.go condition-to-false a < b->false",
	"compare/compare.go lt-to-le <-><=",
	"compare/compare.go return-empty-string \"lt\"->\"\"",
	"compare/compare.go negate-condition a <= b->!(a <= b)",
	"compare/compare.go condition-to-true a <= b->true",
	"compare/compare.go condition-to-false a <= b->false",
	"compare/compare.go le-to-lt <=-><",
	"compare/compare.go return-empty-string \"le\"->\"\"",
	"compare/compare.go negate-condition a > b->!(a > b)",
	"compare/compare.go condition-to-true a > b->true",
	"compare/compare.go condition-to-false a > b->false",
	"compare/compare.go gt-to-ge >->>=",
	"compare/compare.go return-empty-string \"gt\"->\"\"",
	"compare/compare.go negate-condition a >= b->!(a >= b)",
	"compare/compare.go condition-to-true a >= b->true",
	"compare/compare.go condition-to-false a >= b->false",
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
	"deeper/deeper.go return-zero-numeric Thing(n)->0",
	"deeper/deeper.go return-zero-numeric int(t)->0",
	"deletion/deletion.go delete-call-statement Log(\"start\")->",
	"deletion/deletion.go delete-assignment total = total + n->",
	"deletion/deletion.go add-to-sub +->-",
	"deletion/deletion.go delete-incdec total++->",
	"deletion/deletion.go incr-to-decr ++->--",
	"deletion/deletion.go delete-assignment m[key] = n->",
	"deletion/deletion.go delete-assignment out[0] = total->",
	"deletion/deletion.go delete-assignment xs = append(xs, n)->",
	"deletion/deletion.go return-nil xs->nil",
	"deletion/deletion.go return-empty-slice xs->[]int{}",
	"deletion/deletion.go negate-condition n < 0->!(n < 0)",
	"deletion/deletion.go condition-to-true n < 0->true",
	"deletion/deletion.go condition-to-false n < 0->false",
	"deletion/deletion.go lt-to-le <-><=",
	"deletion/deletion.go return-zero-numeric n->0",
	"errs/errs.go return-empty-string w.Op->\"\"",
	"errs/errs.go return-err-to-nil err->nil",
	"errs/errs.go return-nil &Wrapped{Op: op}->nil",
	"errs/errs.go negate-condition err != nil->!(err != nil)",
	"errs/errs.go nil-error-branch err != nil->false",
	"errs/errs.go condition-to-true err != nil->true",
	"errs/errs.go condition-to-false err != nil->false",
	"errs/errs.go neq-to-eq !=->==",
	"errs/errs.go delete-assignment out[0] = 1->",
	"errs/errs.go negate-condition nil != err->!(nil != err)",
	"errs/errs.go nil-error-branch nil != err->false",
	"errs/errs.go condition-to-true nil != err->true",
	"errs/errs.go condition-to-false nil != err->false",
	"errs/errs.go neq-to-eq !=->==",
	"errs/errs.go delete-assignment out[1] = 2->",
	"errs/errs.go negate-condition p != nil->!(p != nil)",
	"errs/errs.go condition-to-true p != nil->true",
	"errs/errs.go condition-to-false p != nil->false",
	"errs/errs.go neq-to-eq !=->==",
	"errs/errs.go delete-assignment out[0] = 1->",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go return-zero-numeric sum->0",
	"forms/forms.go mul-to-div *->/",
	"forms/forms.go return-zero-numeric product->0",
	"forms/forms.go negate-condition err != nil->!(err != nil)",
	"forms/forms.go nil-error-branch err != nil->false",
	"forms/forms.go condition-to-true err != nil->true",
	"forms/forms.go condition-to-false err != nil->false",
	"forms/forms.go neq-to-eq !=->==",
	"forms/forms.go return-err-to-nil err->nil",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go return-zero-numeric second->0",
	"forms/forms.go return-err-to-nil err->nil",
	"forms/forms.go return-zero-numeric a->0",
	"forms/forms.go negate-loop-condition i < n->!(i < n)",
	"forms/forms.go loop-condition-to-false i < n->false",
	"forms/forms.go lt-to-le <-><=",
	"forms/forms.go add-assign-to-sub-assign +=->-=",
	"forms/forms.go add-assign-to-sub-assign +=->-=",
	"forms/forms.go delete-assignment half = n / 2->",
	"forms/forms.go div-to-mul /->*",
	"forms/forms.go negate-condition half > 0->!(half > 0)",
	"forms/forms.go condition-to-true half > 0->true",
	"forms/forms.go condition-to-false half > 0->false",
	"forms/forms.go gt-to-ge >->>=",
	"forms/forms.go delete-assignment out[0] = half->",
	"forms/forms.go return-zero-numeric half->0",
	"forms/forms.go div-to-mul /->*",
	"forms/forms.go negate-condition half > 0->!(half > 0)",
	"forms/forms.go condition-to-true half > 0->true",
	"forms/forms.go condition-to-false half > 0->false",
	"forms/forms.go gt-to-ge >->>=",
	"forms/forms.go return-zero-numeric half->0",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go return-empty-string \"zero\"->\"\"",
	"forms/forms.go return-empty-string \"other\"->\"\"",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go sub-to-add -->+",
	"forms/forms.go mul-to-div *->/",
	"forms/forms.go delete-call-statement ok(a + b)->",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go sub-to-add -->+",
	"forms/forms.go mul-to-div *->/",
	"forms/forms.go return-true n > 0->true",
	"forms/forms.go return-false n > 0->false",
	"forms/forms.go gt-to-ge >->>=",
	"forms/forms.go mul-to-div *->/",
	"forms/forms.go delete-assignment n = total->",
	"forms/forms.go return-zero-numeric n->0",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go mul-to-div *->/",
	"forms/forms.go return-zero-numeric Limit->0",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go mul-to-div *->/",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go return-zero-numeric a + Limit->0",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go return-zero-numeric scale(n)->0",
	"forms/forms.go add-to-sub +->-",
	"forms/forms.go delete-assignment total.hi = start->",
	"forms/forms.go return-zero-numeric total.hi->0",
	"forms/forms.go negate-condition f->!(f)",
	"forms/forms.go condition-to-true f->true",
	"forms/forms.go condition-to-false f->false",
	"forms/forms.go return-zero-numeric n->0",
	"generics/generics.go negate-condition a > b->!(a > b)",
	"generics/generics.go condition-to-true a > b->true",
	"generics/generics.go condition-to-false a > b->false",
	"generics/generics.go gt-to-ge >->>=",
	"generics/generics.go return-zero-numeric sized[[len([1]bool{false})]byte](v)[0]->0",
	"generics/generics.go return-zero-numeric b.v[0]->0",
	"generics/generics.go return-zero-numeric len(p.key) + len(p.value)->0",
	"generics/generics.go add-to-sub +->-",
	"hidden/hidden.go return-nil &counter{n: n}->nil",
	"hidden/hidden.go return-zero-numeric c.n->0",
	"hidden/hidden.go return-zero-numeric tally(n)->0",
	"hidden/hidden.go return-zero-numeric int(t)->0",
	"labels/labels.go negate-condition v == want->!(v == want)",
	"labels/labels.go condition-to-true v == want->true",
	"labels/labels.go condition-to-false v == want->false",
	"labels/labels.go eq-to-neq ==->!=",
	"labels/labels.go drop-break-label break outer->break",
	"labels/labels.go false-to-true false->true",
	"labels/labels.go return-true false->true",
	"labels/labels.go negate-condition v == bad->!(v == bad)",
	"labels/labels.go condition-to-true v == bad->true",
	"labels/labels.go condition-to-false v == bad->false",
	"labels/labels.go eq-to-neq ==->!=",
	"labels/labels.go drop-continue-label continue outer->continue",
	"labels/labels.go add-assign-to-sub-assign +=->-=",
	"labels/labels.go return-zero-numeric total->0",
	"labels/labels.go lt-to-le <-><=",
	"labels/labels.go drop-break-label break loop->break",
	"labels/labels.go eq-to-neq ==->!=",
	"labels/labels.go add-assign-to-sub-assign +=->-=",
	"labels/labels.go return-zero-numeric total->0",
	"labels/labels.go negate-condition v == want->!(v == want)",
	"labels/labels.go condition-to-true v == want->true",
	"labels/labels.go condition-to-false v == want->false",
	"labels/labels.go eq-to-neq ==->!=",
	"labels/labels.go false-to-true false->true",
	"labels/labels.go return-true false->true",
	"labels/labels.go delete-incdec n++->",
	"labels/labels.go incr-to-decr ++->--",
	"labels/labels.go negate-condition n < attempts->!(n < attempts)",
	"labels/labels.go condition-to-true n < attempts->true",
	"labels/labels.go condition-to-false n < attempts->false",
	"labels/labels.go lt-to-le <-><=",
	"labels/labels.go return-zero-numeric n->0",
	"labels/labels.go return-empty-string out->\"\"",
	"legacy/legacy.go return-true a == b->true",
	"legacy/legacy.go return-false a == b->false",
	"legacy/legacy.go eq-to-neq ==->!=",
	"negate/negate.go negate-condition ok && a > b->!(ok && a > b)",
	"negate/negate.go condition-to-true ok && a > b->true",
	"negate/negate.go condition-to-false ok && a > b->false",
	"negate/negate.go and-to-or &&->||",
	"negate/negate.go gt-to-ge >->>=",
	"negate/negate.go delete-assignment out[0] = 1->",
	"negate/negate.go negate-condition ok || a < b->!(ok || a < b)",
	"negate/negate.go condition-to-true ok || a < b->true",
	"negate/negate.go condition-to-false ok || a < b->false",
	"negate/negate.go or-to-and ||->&&",
	"negate/negate.go lt-to-le <-><=",
	"negate/negate.go delete-assignment out[1] = 2->",
	"negate/negate.go negate-condition !ok->!(!ok)",
	"negate/negate.go remove-negation !ok->ok",
	"negate/negate.go condition-to-true !ok->true",
	"negate/negate.go condition-to-false !ok->false",
	"negate/negate.go delete-assignment out[0] = 1->",
	"negate/negate.go negate-loop-condition a < b->!(a < b)",
	"negate/negate.go loop-condition-to-false a < b->false",
	"negate/negate.go lt-to-le <-><=",
	"negate/negate.go delete-incdec a++->",
	"negate/negate.go incr-to-decr ++->--",
	"negate/negate.go delete-assignment out[0] = a->",
	"negate/negate.go negate-condition f->!(f)",
	"negate/negate.go condition-to-true f->true",
	"negate/negate.go condition-to-false f->false",
	"negate/negate.go delete-assignment out[0] = 1->",
	"negate/negate.go ge-to-gt >=->>",
	"negate/negate.go return-true f->true",
	"negate/negate.go return-false f->false",
	"returns/returns.go return-zero-numeric a->0",
	"returns/returns.go return-zero-numeric a->0",
	"returns/returns.go return-empty-string a->\"\"",
	"returns/returns.go return-empty-string a->\"\"",
	"returns/returns.go return-zero-numeric a->0",
	"returns/returns.go return-true a->true",
	"returns/returns.go return-false a->false",
	"returns/returns.go return-nil p->nil",
	"returns/returns.go return-nil s->nil",
	"returns/returns.go return-empty-slice s->[]int{}",
	"returns/returns.go return-nil m->nil",
	"returns/returns.go return-empty-map m->map[string]int{}",
	"returns/returns.go return-nil c->nil",
	"returns/returns.go return-nil f->nil",
	"returns/returns.go return-nil v->nil",
	"returns/returns.go delete-assignment n = 1->",
	"returns/returns.go return-zero-numeric 1->0",
	"returns/returns.go return-zero-numeric 2->0",
	"runes/runes.go negate-condition a > b->!(a > b)",
	"runes/runes.go condition-to-true a > b->true",
	"runes/runes.go condition-to-false a > b->false",
	"runes/runes.go gt-to-ge >->>=",
	"runes/runes.go return-empty-string label->\"\"",
	"runes/runes.go negate-condition a < b->!(a < b)",
	"runes/runes.go condition-to-true a < b->true",
	"runes/runes.go condition-to-false a < b->false",
	"runes/runes.go lt-to-le <-><=",
	"runes/runes.go return-empty-string label->\"\"",
	"runes/runes.go return-empty-string \"…\"->\"\"",
	"shadow/shadow.go return-zero-numeric true->0",
	"shadow/shadow.go add-to-sub +->-",
	"shadow/shadow.go return-zero-numeric true->0",
	"shadow/shadow.go false-to-true false->true",
	"shadow/shadow.go return-true false->true",
	"split/sayable.go return-zero-numeric carrier.Count(boxed(a).Size()) + b->0",
	"split/sayable.go add-to-sub +->-",
	"split/unsayable.go add-to-sub +->-",
	"split/unsayable.go return-zero-numeric a->0",
	"split/unsayable.go return-zero-numeric a->0",
	"suppressed/suppressed.go return-zero-numeric len(Buffer{})->0",
	"suppressed/suppressed.go negate-condition limit->!(limit)",
	"suppressed/suppressed.go condition-to-false limit->false",
	"suppressed/suppressed.go return-zero-numeric a->0",
	"suppressed/suppressed.go eq-to-neq ==->!=",
	"suppressed/suppressed.go negate-condition ok == true->!(ok == true)",
	"suppressed/suppressed.go condition-to-true ok == true->true",
	"suppressed/suppressed.go condition-to-false ok == true->false",
	"suppressed/suppressed.go eq-to-neq ==->!=",
	"suppressed/suppressed.go true-to-false true->false",
	"suppressed/suppressed.go return-empty-string \"equal and ok\"->\"\"",
	"suppressed/suppressed.go eq-to-neq ==->!=",
	"suppressed/suppressed.go false-to-true false->true",
	"suppressed/suppressed.go return-empty-string \"not ok\"->\"\"",
	"suppressed/suppressed.go add-to-sub +->-",
	"suppressed/suppressed.go return-empty-string \"one more\"->\"\"",
	"suppressed/suppressed.go mul-to-div *->/",
	"suppressed/suppressed.go return-empty-string \"twice\"->\"\"",
	"suppressed/suppressed.go negate-condition v > b->!(v > b)",
	"suppressed/suppressed.go condition-to-true v > b->true",
	"suppressed/suppressed.go condition-to-false v > b->false",
	"suppressed/suppressed.go gt-to-ge >->>=",
	"suppressed/suppressed.go return-empty-string \"greater\"->\"\"",
	"suppressed/suppressed.go return-empty-string v->\"\"",
	"suppressed/suppressed.go return-empty-string \"none\"->\"\"",
	"suppressed/suppressed.go lt-to-le <-><=",
	"suppressed/suppressed.go return-empty-string \"sent\"->\"\"",
	"suppressed/suppressed.go negate-condition v == true->!(v == true)",
	"suppressed/suppressed.go condition-to-true v == true->true",
	"suppressed/suppressed.go condition-to-false v == true->false",
	"suppressed/suppressed.go eq-to-neq ==->!=",
	"suppressed/suppressed.go true-to-false true->false",
	"suppressed/suppressed.go return-empty-string \"received\"->\"\"",
	"suppressed/suppressed.go return-empty-string \"none\"->\"\"",
	"unnameable/unnameable.go add-to-sub +->-",
	"unnameable/unnameable.go return-zero-numeric c.Value()->0",
	"unnameable/unnameable.go return-zero-numeric a->0",
}

var wantSkips = []string{
	"cgopkg/cgo.go cgo 1",
	"cgopkg/pure.go cgo 1",
	"generated/generated.go generated 1",
	"generics/generics.go type-param 5",
	"labels/labels.go label-or-goto 1",
	"split/unsayable.go unnameable-decl-type 1",
	"suppressed/suppressed.go array-length 2",
	"suppressed/suppressed.go const-decl 4",
	"suppressed/suppressed.go package-var-init 4",
	"unnameable/unnameable.go unnameable-decl-type 1",
}

func TestDiscoverFindsEveryImplementedRule(t *testing.T) {
	result := wholeFixture(t)
	equalStrings(t, summarize(result.Candidates), wantCandidates)
}

func TestTheFixtureModuleFiresEveryRule(t *testing.T) {
	result := wholeFixture(t)
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
	result := wholeFixture(t)
	equalStrings(t, summarizeSkips(result.Skips), wantSkips)
}

func TestDiscoverReportsTheModule(t *testing.T) {
	result := wholeFixture(t)
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

		line, ok := sourceLine(t, src, c)
		if !ok {
			continue
		}
		if got := line[c.Column-1 : c.Column-1+len(c.Original)]; got != c.Original {
			t.Errorf("%s:%d:%d: line holds %q, want %q", c.Path, c.Line, c.Column, got, c.Original)
		}
	}
}

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

func TestDiscoverSpansCoverTheOriginalText(t *testing.T) {
	result := wholeFixture(t)
	if len(result.Candidates) == 0 {
		t.Fatal("no candidates to check")
	}
	assertCandidatesMatchTheFile(t, fixture(t, "mainmod"), result.Candidates)
}

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

var wantFormsGuards = []string{
	"add-to-sub + | D var sum = a + b [sum int]",
	"return-zero-numeric sum | S return sum []",
	"mul-to-div * | D product := a * b [product int]",
	"return-zero-numeric product | S return product []",
	"negate-condition err != nil | C err != nil []",
	"nil-error-branch err != nil | C err != nil []",
	"condition-to-true err != nil | C err != nil []",
	"condition-to-false err != nil | C err != nil []",
	"neq-to-eq != | C err != nil []",
	"return-err-to-nil err | S return 0, err []",
	"add-to-sub + | E first + 1 []",
	"return-zero-numeric second | S return second, err []",
	"return-err-to-nil err | S return second, err []",
	"return-zero-numeric a | S return a, nil []",
	"negate-loop-condition i < n | C i < n []",
	"loop-condition-to-false i < n | C i < n []",
	"lt-to-le < | C i < n []",
	"add-assign-to-sub-assign += | F i += 2 []",
	"add-assign-to-sub-assign += | S out[0] += i []",
	"delete-assignment half = n / 2 | F half = n / 2 []",
	"div-to-mul / | F half = n / 2 []",
	"negate-condition half > 0 | C half > 0 []",
	"condition-to-true half > 0 | C half > 0 []",
	"condition-to-false half > 0 | C half > 0 []",
	"gt-to-ge > | C half > 0 []",
	"delete-assignment out[0] = half | S out[0] = half []",
	"return-zero-numeric half | S return half []",
	"div-to-mul / | E n / 2 []",
	"negate-condition half > 0 | C half > 0 []",
	"condition-to-true half > 0 | C half > 0 []",
	"condition-to-false half > 0 | C half > 0 []",
	"gt-to-ge > | C half > 0 []",
	"return-zero-numeric half | S return half []",
	"add-to-sub + | E a + b []",
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
	"mul-to-div * | E total * 2 []",
	"delete-assignment n = total | S n = total []",
	"return-zero-numeric n | S return n []",
	"add-to-sub + | E Limit + n*2 []",
	"mul-to-div * | E n*2 []",
	"return-zero-numeric Limit | S return Limit []",
	"add-to-sub + | E Limit + n*2 []",
	"mul-to-div * | E n*2 []",
	"add-to-sub + | E a + 1 []",
	"return-zero-numeric a + Limit | S return a + Limit []",
	"add-to-sub + | S return a + Limit []",
	"add-to-sub + | E n + 1 []",
	"return-zero-numeric scale(n) | S return scale(n) []",
	"add-to-sub + | E n + 1 []",
	"delete-assignment total.hi = start | S total.hi = start []",
	"return-zero-numeric total.hi | S return total.hi []",
	"negate-condition f | C' f []",
	"condition-to-true f | C' f []",
	"condition-to-false f | C' f []",
	"return-zero-numeric n | S return n []",
}

func TestDiscoverEmitsTheGuardHints(t *testing.T) {
	root := fixture(t, "mainmod")
	result := discoverFixture(t, "mainmod", Options{Include: patterns(t, "forms/**")})
	equalStrings(t, summarizeGuards(t, root, result.Candidates), wantFormsGuards)
}

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

func TestEveryCandidateCarriesAUsableGuard(t *testing.T) {
	root := fixture(t, "mainmod")
	result := wholeFixture(t)
	if len(result.Candidates) == 0 {
		t.Fatal("no candidates to check")
	}
	forms := make(map[GuardForm]int)
	for _, c := range result.Candidates {
		switch c.Guard.Form {
		case GuardFormC, GuardFormS, GuardFormD, GuardFormCPrime, GuardFormF, GuardFormE:
			forms[c.Guard.Form]++
		default:
			t.Errorf("%s %s: guard form %q is not one this build emits", c.Path, c.Span, c.Guard.Form)
			continue
		}
		carriesType := c.Guard.Form == GuardFormCPrime || c.Guard.Form == GuardFormE
		if (c.Guard.SiteType != "") != carriesType {
			t.Errorf("%s %s: a Form %s site carries SiteType %q",
				c.Path, c.Span, c.Guard.Form, c.Guard.SiteType)
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
	for _, form := range []GuardForm{GuardFormC, GuardFormS, GuardFormD} {
		if forms[form] == 0 {
			t.Errorf("no candidate in the fixture module carries a Form %s hint", form)
		}
	}
}

func TestDiscoverIsDeterministic(t *testing.T) {
	first := discoverFixture(t, "mainmod", Options{})
	second := discoverFixture(t, "mainmod", Options{})
	if !reflect.DeepEqual(first, second) {
		t.Error("two discoveries over the same tree disagree")
	}
}

func TestDiscoverNeverMutatesTestFiles(t *testing.T) {
	result := wholeFixture(t)
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

const (
	crlfCandidateLine   = 12
	crlfCandidateColumn = 11
)

func lines(text ...string) string { return strings.Join(text, "\r\n") + "\r\n" }

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

func TestDiscoverReadsCRLFSource(t *testing.T) {
	root := writeModule(t, crlfModule)
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
	if len(result.Candidates) != 39 {
		t.Errorf("got %d candidates, want the 39 in compare: %v", len(result.Candidates), summarize(result.Candidates))
	}
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
	if len(result.Candidates) != 10 {
		t.Errorf("got %d eq-to-neq candidates, want 10: %v", len(result.Candidates), summarize(result.Candidates))
	}
}

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
	if !strings.Contains(err.Error(), "a single-module discovery cannot measure one") {
		t.Errorf("message does not say what is unsupported: %v", err)
	}
}

func TestDiscoverIgnoresAWorkspaceOutsideTheSnapshot(t *testing.T) {
	workspaceRoot := fixture(t, "workspace")
	root := filepath.Join(workspaceRoot, "first")
	located := toolchain(t)

	ways := []struct {
		name   string
		gowork string
	}{
		{"found by walking up", ""},
		{"named by $GOWORK", filepath.Join(workspaceRoot, WorkspaceFile)},
	}
	codes := make([]Code, 0, len(ways))
	for _, way := range ways {
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

func TestDiscoverSkipsCgoPackages(t *testing.T) {
	for _, enabled := range []string{"0", "1"} {
		t.Run("CGO_ENABLED="+enabled, func(t *testing.T) {
			t.Setenv("CGO_ENABLED", enabled)
			result := wholeFixture(t)
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

func TestBuildCatalogAcceptsEveryCandidate(t *testing.T) {
	result := wholeFixture(t)
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

func TestResultCarriesTheDigestOfEveryScannedFile(t *testing.T) {
	t.Parallel()

	var narrow []mutation.Rule
	for _, rule := range SupportedRules() {
		if rule.Name == "true-to-false" {
			narrow = append(narrow, rule)
		}
	}
	if len(narrow) != 1 {
		t.Fatalf("the registry holds %d rules named true-to-false, want exactly one", len(narrow))
	}
	root := fixture(t, "mainmod")
	result := discoverFixture(t, "mainmod", Options{Rules: narrow})

	if len(result.SourceDigests) == 0 {
		t.Fatal("discovery recorded no source digests at all")
	}
	catalogued := make(map[string]bool)
	for _, candidate := range result.Candidates {
		catalogued[candidate.Path] = true
		digest, scanned := result.SourceDigests[candidate.Path]
		if !scanned {
			t.Errorf("%s has a candidate and no recorded digest", candidate.Path)
			continue
		}
		if digest != candidate.SourceDigest {
			t.Errorf("%s: recorded digest %s, candidate digest %s", candidate.Path, digest, candidate.SourceDigest)
		}
	}
	for path, digest := range result.SourceDigests {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("recorded digest for %s, which cannot be read: %v", path, err)
			continue
		}
		if want := mutation.Digest(src); digest != want {
			t.Errorf("%s: recorded digest %s, file digests %s", path, digest, want)
		}
	}
	if len(result.SourceDigests) <= len(catalogued) {
		t.Errorf("discovery recorded %d digests for %d catalogued files, want a digest for the"+
			" files it read and found nothing in", len(result.SourceDigests), len(catalogued))
	}
}

func TestTheLoaderParsesEachSourceFileOnceAndNoTestFile(t *testing.T) {
	t.Parallel()
	root := fixture(t, "mainmod")
	loaded, err := load(context.Background(), root, toolchain(t), nil, false, nil)
	if err != nil {
		t.Fatalf("loading the fixture module: %v", err)
	}
	seen := make(map[string]string)
	var tests, twice []string
	for _, pkg := range loaded.packages {
		for _, file := range pkg.Syntax {
			tokFile := loaded.fset.File(file.Package)
			if tokFile == nil {
				continue
			}
			name := tokFile.Name()
			if isTestFile(name) {
				tests = append(tests, pkg.ID+": "+name)
			}
			if first, ok := seen[name]; ok {
				twice = append(twice, name+" in "+first+" and in "+pkg.ID)
				continue
			}
			seen[name] = pkg.ID
		}
	}
	if len(tests) != 0 {
		t.Errorf("the loader parsed %d test files discovery will not walk:\n\t%s",
			len(tests), strings.Join(tests, "\n\t"))
	}
	if len(twice) != 0 {
		t.Errorf("the loader parsed %d files more than once:\n\t%s",
			len(twice), strings.Join(twice, "\n\t"))
	}
}

func TestDiscoverReadsATreeWhoseTestFilesDoNotCompile(t *testing.T) {
	t.Parallel()
	result := discoverFixture(t, "brokentests", Options{})
	if len(result.Candidates) == 0 {
		t.Fatal("no candidate came out of the package beside the broken test file")
	}
	for _, candidate := range result.Candidates {
		if candidate.Path != "count.go" {
			t.Errorf("candidate in %q, which is not the file the fixture mutates", candidate.Path)
		}
	}
}

func TestAWorkspaceMayUseItsOwnRootAsAModule(t *testing.T) {
	t.Parallel()

	results, err := DiscoverWorkspace(t.Context(), Options{
		SnapshotRoot: testkit.Fixture(t, "rootmodule"),
		Toolchain:    toolchain(t),
		Rules:        []mutation.Rule{{Family: mutation.FamilyComparison, Name: "gt-to-ge", Version: 1, Tier: mutation.TierBalanced}},
	})
	if err != nil {
		t.Fatalf("DiscoverWorkspace over a workspace whose root is a module: %v", err)
	}
	paths := make([]string, 0, len(results))
	for _, result := range results {
		paths = append(paths, result.Module.Path)
	}
	slices.Sort(paths)
	want := []string{"fixture.example/rootmodule", "fixture.example/rootmodule/inner"}
	if !slices.Equal(paths, want) {
		t.Errorf("discovered %v, want %v", paths, want)
	}
	for _, result := range results {
		if len(result.Result.Candidates) == 0 {
			t.Errorf("module %s contributed no candidate", result.Module.Path)
		}
	}
}
