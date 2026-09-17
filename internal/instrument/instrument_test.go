// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
)

const (
	testModule = "example.com/mini"
	sampleFile = "sample.go"
)

func TestInstrumentGolden(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		candidates func(t *testing.T, src []byte) []mutation.Candidate
		hints      hintOptions
		sibling    string
		guards     int
		extra      func(t *testing.T, in, out []byte)
	}{{
		name:   "comparison",
		guards: 1,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, `import (__gm "example.com/mini/gomutants_rt";`)
			assertContains(t, out, "\n\t\"fmt\"\n\t\"strings\"\n)")
		},
	}, {
		name:   "boolliteral",
		guards: 2,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, `import ("strings"; __gm "example.com/mini/gomutants_rt")`)
		},
	}, {
		name:       "alternatives",
		candidates: everyAlternative,
		guards:     1,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, `package sample; import __gm "example.com/mini/gomutants_rt"`)
			for i := range 5 {
				assertContains(t, out, fmt.Sprintf("__gm.M[%d] && (", i))
			}
		},
	}, {
		name:   "nested",
		guards: 6,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "&& ((a>b)!=(c>d))")
			assertContains(t, out, "&& (((__gm.M[")
		},
	}, {
		name:   "multiline",
		guards: 2,
		extra: func(t *testing.T, in, out []byte) {
			assertLinesUntouched(t, in, out, 5, 10, 12, 18, 19)
			assertContains(t, out, "(x<limit)")
			assertContains(t, out, "(x <= // the limit is inclusive")
		},
	}, {
		name:   "unicode",
		guards: 2,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "(größe>=grenze)")
			assertContains(t, out, `("日本語"!=s)`)
		},
	}, {
		name:   "aliascollision",
		guards: 2,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, `import __gm2 "example.com/mini/gomutants_rt"`)
			assertContains(t, out, "__gm2.M[0]")
			if bytes.Contains(out, []byte("__gm.M[")) || bytes.Contains(out, []byte("__gm1.M[")) {
				t.Error("a guard used an alias the file had already bound")
			}
		},
	}, {
		name:       "statement",
		candidates: statementEdits,
		guards:     4,
		extra: func(t *testing.T, in, out []byte) {
			assertContains(t, out, "if __gm.M[0] { return 0,err } else if __gm.M[1] { return count,nil } else { return count, err }")
			assertContains(t, out, "if __gm.M[4] { } else if __gm.M[5] { *counter= *counter-2 } else { *counter = *counter + 2 }")
			assertContains(t, out, "if __gm.M[3] { defer done(*counter-1) } else { defer done(*counter + 1) }")
			assertContains(t, out, "{ total=total-step*2-1 } else { total = total +\n")
			assertContains(t, out, "__gm_n0, __gm_k0 := uint64(0), __gm.Limit[0]; for _, step := range steps {")
			assertLinesUntouched(t, in, out, 6, 16, 28, 29, 31, 44, 45)
		},
	}, {
		name:       "declaration",
		candidates: declarationEdits,
		hints:      hintOptions{declared: declaredTypes()},
		guards:     7,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "var lo int; var hi int; if __gm.M[")
			assertContains(t, out, "else { lo, hi = n/2, n-n/2 }")
			assertContains(t, out, "var scaled int; if __gm.M[")
			assertContains(t, out, "else {  scaled  = v * 3 }")
			assertContains(t, out, "var head int; if __gm.M[")
			assertContains(t, out, "else { head, _ = values[0], len(values)-1 }")
			assertContains(t, out, "var low int; var high int; if __gm.M[")
			assertContains(t, out, "{ low=values[0]+1;high=values[len(values)-1]+1 }")
			assertContains(t, out, "else {  \n\t\tlow  = values[0] - 1\n\t\thigh = values[len(values)-1] + 1\n\t }")
			assertContains(t, out, "{ weight=cost(a>b)-1 } else { weight = cost((__gm.M[")
		},
	}, {
		name:       "mixedforms",
		candidates: mixedFormEdits,
		guards:     4,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "if (__gm.M[0] && (v>=limit) || !(__gm.M[0]) && (v > limit)) {")
			assertContains(t, out, "if __gm.M[1] { v=limit+1 } else { v = limit - 1 }")
			assertContains(t, out, "if __gm.M[3] { return a>b,a+b } else { return (__gm.M[2] && (a>=b) || !(__gm.M[2]) && (a > b)), a - b }")
		},
	}, {
		name:       "namedbool",
		candidates: namedBoolEdits,
		hints:      hintOptions{namedBool: namedBoolExprs()},
		guards:     2,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "if __gm.M[0] { return x>=y } else { return x > y }")
			assertContains(t, out, "if __gm.M[1] { return false } else { return true }")
			if bytes.Contains(out, []byte("&& (")) {
				t.Errorf("a named boolean type was guarded with a Form C selector:\n%s", out)
			}
		},
	}, {
		name:    "siblingalias",
		sibling: "sibling.go",
		guards:  1,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, `import __gm2 "example.com/mini/gomutants_rt"`)
			assertContains(t, out, "__gm2.M[0]")
			if bytes.Contains(out, []byte("__gm.M[")) || bytes.Contains(out, []byte("__gm1.M[")) {
				t.Error("a guard used an alias the package block had already bound")
			}
		},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := testkit.ReadFile(t, filepath.Join("testdata", c.name+".input"))
			root := t.TempDir()
			testkit.WriteFile(t, filepath.Join(root, sampleFile), in)

			var sibling []byte
			if c.sibling != "" {
				sibling = testkit.ReadFile(t, filepath.Join("testdata", c.name+".sibling"))
				testkit.WriteFile(t, filepath.Join(root, c.sibling), sibling)
			}

			catalog := catalogOf(t, candidatesFor(t, c.candidates, in))
			result := instrumentSnapshotWith(t, root, catalog, c.hints)
			out := testkit.ReadFile(t, filepath.Join(root, sampleFile))

			if c.sibling != "" {
				if got := testkit.ReadFile(t, filepath.Join(root, c.sibling)); !bytes.Equal(got, sibling) {
					t.Errorf("the sibling holds no candidate and was rewritten anyway:\n%s", got)
				}
			}

			testkit.Golden(t, c.name+".golden", out)

			assertWellFormed(t, in, out, catalog)
			if got := result.GuardsByFile[sampleFile]; got != c.guards {
				t.Errorf("GuardsByFile[%s] = %d, want %d", sampleFile, got, c.guards)
			}
			if got := result.FilesInstrumented; len(got) != 1 || got[0] != sampleFile {
				t.Errorf("FilesInstrumented = %v, want [%s]", got, sampleFile)
			}
			if c.extra != nil {
				c.extra(t, in, out)
			}
		})
	}
}

func TestInstrumentPreservesCRLFOutsideTheGuards(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name       string
		candidates func(*testing.T, []byte) []mutation.Candidate
	}{
		{name: "multiline"},
		{name: "statement", candidates: statementEdits},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := toCRLF(testkit.ReadFile(t, filepath.Join("testdata", c.name+".input")))
			root := t.TempDir()
			testkit.WriteFile(t, filepath.Join(root, sampleFile), in)

			catalog := catalogOf(t, candidatesFor(t, c.candidates, in))
			instrumentSnapshot(t, root, catalog)
			out := testkit.ReadFile(t, filepath.Join(root, sampleFile))

			if want := toCRLF(testkit.ReadFile(t, filepath.Join("testdata", c.name+".golden"))); !bytes.Equal(out, want) {
				t.Errorf("instrumented CRLF output does not match the converted fixture\n--- got ---\n%q\n--- want ---\n%q",
					out, want)
			}
			assertWellFormed(t, in, out, catalog)

			if bytes.Count(out, []byte("\r\n")) != bytes.Count(out, []byte("\n")) {
				t.Error("the instrumented file lost a carriage return: not every line break is a CRLF")
			}
		})
	}
}

func TestInstrumentPreservesCRLF(t *testing.T) {
	t.Parallel()

	inputs, err := filepath.Glob(filepath.Join("testdata", "*.input"))
	if err != nil {
		t.Fatalf("listing the fixtures: %v", err)
	}
	if len(inputs) == 0 {
		t.Fatal("no fixture inputs were found, so this proves nothing")
	}

	for _, input := range inputs {
		name := strings.TrimSuffix(filepath.Base(input), ".input")
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			lf := testkit.ReadFile(t, input)
			crlf := toCRLF(lf)

			out := instrumentOne(t, sampleFile, lf)
			converted := instrumentOne(t, sampleFile, crlf)

			if bytes.Count(converted, []byte("\r\n")) != bytes.Count(converted, []byte("\n")) {
				t.Errorf("the instrumented file lost a carriage return: not every line break is a CRLF\n%q", converted)
			}
			if want := toCRLF(out); !bytes.Equal(converted, want) {
				t.Errorf("instrumenting the CRLF file gave something other than the CRLF form of the "+
					"instrumented LF file\n--- got ---\n%q\n--- want ---\n%q", converted, want)
			}
		})
	}
}

func instrumentOne(t *testing.T, name string, source []byte) []byte {
	t.Helper()
	root := t.TempDir()
	testkit.WriteFile(t, filepath.Join(root, name), source)
	instrumentSnapshot(t, root, catalogOf(t, candidatesIn(t, source)))
	return testkit.ReadFile(t, filepath.Join(root, name))
}

func TestInstrumentLeavesUncatalogedFilesAlone(t *testing.T) {
	t.Parallel()

	const untouched = "other.go"
	in := testkit.ReadFile(t, filepath.Join("testdata", "comparison.input"))
	other := toCRLF(testkit.ReadFile(t, filepath.Join("testdata", "unicode.input")))

	root := t.TempDir()
	testkit.WriteFile(t, filepath.Join(root, sampleFile), in)
	testkit.WriteFile(t, filepath.Join(root, untouched), other)

	result := instrumentSnapshot(t, root, catalogOf(t, candidatesFor(t, nil, in)))

	if got := testkit.ReadFile(t, filepath.Join(root, untouched)); !bytes.Equal(got, other) {
		t.Errorf("a file with no catalogued mutants was rewritten:\n%s", got)
	}
	if _, ok := result.GuardsByFile[untouched]; ok {
		t.Errorf("GuardsByFile mentions %s, which has no mutants", untouched)
	}
	if len(result.FilesInstrumented) != 1 {
		t.Errorf("FilesInstrumented = %v, want only %s", result.FilesInstrumented, sampleFile)
	}
}

func TestInstrumentReplacesAReadOnlyFile(t *testing.T) {
	t.Parallel()

	in := testkit.ReadFile(t, filepath.Join("testdata", "comparison.input"))
	root := t.TempDir()
	target := filepath.Join(root, sampleFile)
	testkit.WriteFile(t, target, in)

	if err := os.Chmod(target, 0o444); err != nil {
		t.Skipf("this filesystem does not take a read-only mode: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o600) })

	result := instrumentSnapshot(t, root, catalogOf(t, candidatesFor(t, nil, in)))
	if got := result.GuardsByFile[sampleFile]; got != 1 {
		t.Errorf("GuardsByFile[%s] = %d, want 1", sampleFile, got)
	}

	out := testkit.ReadFile(t, target)
	if want := testkit.ReadFile(t, filepath.Join("testdata", "comparison.golden")); !bytes.Equal(out, want) {
		t.Errorf("a read-only file instrumented to different bytes than a writable one\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o200 != 0 {
		t.Errorf("the instrumented file has mode %v, want the read-only mode it was given", info.Mode().Perm())
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading the snapshot root: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != sampleFile && entry.Name() != result.RuntimeDir {
			t.Errorf("the rewrite left %q behind in the snapshot", entry.Name())
		}
	}
}

func TestInstrumentIsDeterministic(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name       string
		candidates func(*testing.T, []byte) []mutation.Candidate
		hints      hintOptions
	}{
		{name: "nested"},
		{name: "statement", candidates: statementEdits},
		{name: "declaration", candidates: declarationEdits, hints: hintOptions{declared: declaredTypes()}},
		{name: "mixedforms", candidates: mixedFormEdits},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := testkit.ReadFile(t, filepath.Join("testdata", c.name+".input"))
			catalog := catalogOf(t, candidatesFor(t, c.candidates, in))

			run := func() (string, []byte, []byte) {
				root := t.TempDir()
				testkit.WriteFile(t, filepath.Join(root, sampleFile), in)
				result := instrumentSnapshotWith(t, root, catalog, c.hints)
				runtime := testkit.ReadFile(t, filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go"))
				return result.RuntimeImport, testkit.ReadFile(t, filepath.Join(root, sampleFile)), runtime
			}

			firstImport, firstSource, firstRuntime := run()
			secondImport, secondSource, secondRuntime := run()

			if firstImport != secondImport {
				t.Errorf("runtime import differs between runs: %q and %q", firstImport, secondImport)
			}
			if !bytes.Equal(firstSource, secondSource) {
				t.Errorf("instrumented source differs between runs\n--- first ---\n%s\n--- second ---\n%s",
					firstSource, secondSource)
			}
			if !bytes.Equal(firstRuntime, secondRuntime) {
				t.Errorf("generated runtime differs between runs\n--- first ---\n%s\n--- second ---\n%s",
					firstRuntime, secondRuntime)
			}
		})
	}
}

func TestInstrumentReportsWhatItDid(t *testing.T) {
	t.Parallel()

	const second = "pkg/second.go"
	first := testkit.ReadFile(t, filepath.Join("testdata", "comparison.input"))
	other := testkit.ReadFile(t, filepath.Join("testdata", "nested.input"))

	root := t.TempDir()
	testkit.WriteFile(t, filepath.Join(root, sampleFile), first)
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash(second)), other)

	candidates := candidatesFor(t, nil, first)
	for _, c := range candidatesFor(t, nil, other) {
		c.Path = second
		candidates = append(candidates, c)
	}
	result := instrumentSnapshot(t, root, catalogOf(t, candidates))

	if got, want := result.RuntimeDir, "gomutants_rt"; got != want {
		t.Errorf("RuntimeDir = %q, want %q", got, want)
	}
	if got, want := result.RuntimeImport, testModule+"/gomutants_rt"; got != want {
		t.Errorf("RuntimeImport = %q, want %q", got, want)
	}
	if got, want := result.FilesInstrumented, []string{second, sampleFile}; !equalStrings(got, want) {
		t.Errorf("FilesInstrumented = %v, want %v", got, want)
	}
	if got, want := result.GuardsByFile, map[string]int{second: 6, sampleFile: 1}; !equalCounts(got, want) {
		t.Errorf("GuardsByFile = %v, want %v", got, want)
	}
}

func candidatesFor(t *testing.T, override func(*testing.T, []byte) []mutation.Candidate, src []byte) []mutation.Candidate {
	t.Helper()
	if override != nil {
		return override(t, src)
	}
	return candidatesIn(t, src)
}

var comparisonRules = map[token.Token]struct{ rule, replacement string }{
	token.EQL: {"eq-to-neq", "!="},
	token.NEQ: {"neq-to-eq", "=="},
	token.LSS: {"lt-to-le", "<="},
	token.LEQ: {"le-to-lt", "<"},
	token.GTR: {"gt-to-ge", ">="},
	token.GEQ: {"ge-to-gt", ">"},
}

var booleanRules = map[string]struct{ rule, replacement string }{
	"true":  {"true-to-false", "false"},
	"false": {"false-to-true", "true"},
}

func candidatesIn(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, sampleFile, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	tok := fset.File(file.Package)
	digest := mutation.Digest(src)

	var out []mutation.Candidate
	add := func(start uint32, rule, original, replacement string) {
		out = append(out, mutation.Candidate{
			Path:         sampleFile,
			Rule:         lookupRule(t, rule),
			Span:         mutation.Span{StartByte: start, EndByte: start + uint32(len(original))},
			Original:     original,
			Replacement:  replacement,
			SourceDigest: digest,
		})
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.BinaryExpr:
			if swap, ok := comparisonRules[n.Op]; ok {
				add(uint32(tok.Offset(n.OpPos)), swap.rule, n.Op.String(), swap.replacement)
			}
		case *ast.Ident:
			if swap, ok := booleanRules[n.Name]; ok {
				add(uint32(tok.Offset(n.Pos())), swap.rule, n.Name, swap.replacement)
			}
		}
		return true
	})
	return out
}

func everyAlternative(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()

	start := uint32(bytes.IndexByte(src, '<'))
	digest := mutation.Digest(src)
	var out []mutation.Candidate
	for _, swap := range []struct{ rule, replacement string }{
		{"eq-to-neq", "!="},
		{"neq-to-eq", "=="},
		{"lt-to-le", "<="},
		{"gt-to-ge", ">="},
		{"ge-to-gt", ">"},
	} {
		out = append(out, mutation.Candidate{
			Path:         sampleFile,
			Rule:         lookupRule(t, swap.rule),
			Span:         mutation.Span{StartByte: start, EndByte: start + 1},
			Original:     "<",
			Replacement:  swap.replacement,
			SourceDigest: digest,
		})
	}
	return out
}

func lookupRule(t *testing.T, name string) mutation.Rule {
	t.Helper()
	rule, ok := mutation.CanonicalRegistry().Lookup(name)
	if !ok {
		t.Fatalf("unknown rule %q", name)
	}
	return rule
}

func catalogOf(t *testing.T, candidates []mutation.Candidate) *mutation.Catalog {
	t.Helper()
	builder := mutation.NewBuilder()
	if err := builder.AddAll(candidates); err != nil {
		t.Fatalf("cataloguing: %v", err)
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	return catalog
}

func instrumentSnapshot(t *testing.T, root string, catalog *mutation.Catalog) instrument.Result {
	t.Helper()
	return instrumentSnapshotWith(t, root, catalog, hintOptions{})
}

func instrumentSnapshotWith(
	t *testing.T,
	root string,
	catalog *mutation.Catalog,
	opts hintOptions,
) instrument.Result {
	t.Helper()
	return instrumentSnapshotHinted(t, root, catalog, hintsFor(t, root, catalog, opts))
}

func instrumentSnapshotHinted(
	t *testing.T,
	root string,
	catalog *mutation.Catalog,
	hints instrument.Hints,
) instrument.Result {
	t.Helper()
	result, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: root,
		ModulePath:   testModule,
		Catalog:      catalog,
		Hints:        hints,
	})
	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}
	return result
}

func declaredTypes() map[string]string {
	return map[string]string{
		"lo": "int", "hi": "int", "scaled": "int", "step": "int", "head": "int",
		"low": "int", "high": "int", "weight": "int",
	}
}

func namedBoolExprs() []string { return []string{"x > y", "true"} }

func statementEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-zero-numeric", in: "return count, err", find: "count", with: "0"},
		editSpec{rule: "return-err-to-nil", in: "return count, err", find: "err", with: "nil"},
		editSpec{rule: "add-to-sub", in: "total = total +", find: "+", with: "-"},
		editSpec{rule: "add-to-sub", in: "done(*counter + 1)", find: "+", with: "-"},
		editSpec{rule: "add-to-sub", in: "*counter = *counter + 2", find: "+", with: "-"},
		editSpec{rule: "delete-assignment", in: "*counter = *counter + 2"},
	)
}

func declarationEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "div-to-mul", in: "lo, hi := n/2, n-n/2", find: "/", with: "*"},
		editSpec{rule: "sub-to-add", in: "n-n/2", find: "-", with: "+"},
		editSpec{rule: "mul-to-div", in: "var scaled int = v * 3", find: "*", with: "/"},
		editSpec{rule: "add-to-sub", in: "step := base + 1", find: "+", with: "-"},
		editSpec{rule: "sub-to-add", in: "head, _ := values[0], len(values)-1", find: "-", with: "+"},
		editSpec{rule: "gt-to-ge", in: "cost(a > b) + 1", find: ">", with: ">="},
		editSpec{rule: "add-to-sub", in: "cost(a > b) + 1", find: "+", with: "-"},
		editSpec{rule: "sub-to-add", in: "low  = values[0] - 1", find: "-", with: "+"},
		editSpec{rule: "add-to-sub", in: "values[len(values)-1] + 1", find: "+", with: "-"},
	)
}

func mixedFormEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "gt-to-ge", in: "v > limit", find: ">", with: ">="},
		editSpec{rule: "sub-to-add", in: "v = limit - 1", find: "-", with: "+"},
		editSpec{rule: "gt-to-ge", in: "return a > b, a - b", find: ">", with: ">="},
		editSpec{rule: "sub-to-add", in: "a - b", find: "-", with: "+"},
	)
}

func namedBoolEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "gt-to-ge", in: "return x > y", find: ">", with: ">="},
		editSpec{rule: "true-to-false", in: "return true", find: "true", with: "false"},
	)
}

func assertWellFormed(t *testing.T, in, out []byte, catalog *mutation.Catalog) {
	t.Helper()

	if _, err := parser.ParseFile(token.NewFileSet(), sampleFile, out, parser.SkipObjectResolution); err != nil {
		t.Errorf("the instrumented file does not parse: %v\n%s", err, out)
	}
	if got, want := instrument.CountLines(out), instrument.CountLines(in); got != want {
		t.Errorf("the instrumented file holds %d line breaks, the original holds %d", got, want)
	}
	whole := []instrument.Splice{{
		Span:        mutation.Span{StartByte: 0, EndByte: uint32(len(in))},
		Original:    in,
		Replacement: out,
	}}
	if !instrument.LinePreserving(whole) {
		t.Error("the rewrite is not line-preserving")
	}

	inLines, outLines := lines(in), lines(out)
	for _, m := range catalog.Mutants() {
		line := instrument.CountLines(in[:m.Span.StartByte])
		if line >= len(outLines) {
			t.Fatalf("mutant %s starts past the end of the file", m.DisplayID)
		}
		head := m.Original
		if cut := strings.IndexByte(head, '\n'); cut >= 0 {
			head = head[:cut]
		}
		if !strings.Contains(outLines[line], head) {
			t.Errorf("mutant %s: line %d was %q and is now %q, which no longer holds %q",
				m.DisplayID, line+1, inLines[line], outLines[line], head)
		}
		if flag := fmt.Sprintf(".M[%d]", m.Index); !bytes.Contains(out, []byte(flag)) {
			t.Errorf("mutant %s: no guard reads %s", m.DisplayID, flag)
		}
	}
}

func assertLinesUntouched(t *testing.T, in, out []byte, touched ...int) {
	t.Helper()

	written := make(map[int]bool, len(touched))
	for _, line := range touched {
		written[line] = true
	}
	inLines, outLines := lines(in), lines(out)
	if len(inLines) != len(outLines) {
		t.Fatalf("the instrumented file has %d lines, the original has %d", len(outLines), len(inLines))
	}
	for i := range inLines {
		if written[i] || inLines[i] == outLines[i] {
			continue
		}
		t.Errorf("line %d was not expected to change: %q became %q", i+1, inLines[i], outLines[i])
	}
	for line := range written {
		if inLines[line] == outLines[line] {
			t.Errorf("line %d was expected to carry instrumentation and is unchanged: %q", line+1, inLines[line])
		}
	}
}

func assertContains(t *testing.T, out []byte, want string) {
	t.Helper()
	if !bytes.Contains(out, []byte(want)) {
		t.Errorf("the instrumented file does not contain %q:\n%s", want, out)
	}
}

func lines(b []byte) []string {
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
}

func toCRLF(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalCounts(got, want map[string]int) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
