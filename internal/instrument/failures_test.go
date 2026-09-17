// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"go/ast"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
)

type failure struct {
	name string
	code instrument.Code
	err  error
}

func TestInstrumentRefusesWhatItCannotInstrument(t *testing.T) {
	t.Parallel()

	for _, f := range instrumentationFailures(t) {
		if f.err == nil {
			t.Errorf("%s: no error", f.name)
			continue
		}
		if got := instrument.CodeOf(f.err); got != f.code {
			t.Errorf("%s: code %q, want %q (%v)", f.name, got, f.code, f.err)
		}
	}
}

func instrumentationFailures(t *testing.T) []failure {
	t.Helper()

	empty := catalogOf(t, nil)
	root := t.TempDir()
	notADirectory := filepath.Join(root, "file.txt")
	testkit.WriteFile(t, notADirectory, []byte("not a directory"))

	fail := func(name string, code instrument.Code, err error) failure {
		return failure{name: name, code: code, err: err}
	}
	refuse := func(name string, code instrument.Code, opts instrument.Options) failure {
		_, err := instrument.Instrument(opts)
		return fail(name, code, err)
	}

	out := []failure{
		refuse("no snapshot root", instrument.CodeOptions, instrument.Options{
			ModulePath: testModule, Catalog: empty,
		}),
		refuse("missing snapshot root", instrument.CodeOptions, instrument.Options{
			SnapshotRoot: filepath.Join(root, "nowhere"), ModulePath: testModule, Catalog: empty,
		}),
		refuse("snapshot root is a file", instrument.CodeOptions, instrument.Options{
			SnapshotRoot: notADirectory, ModulePath: testModule, Catalog: empty,
		}),
		refuse("no module path", instrument.CodeOptions, instrument.Options{
			SnapshotRoot: root, Catalog: empty,
		}),
		refuse("no catalogue", instrument.CodeOptions, instrument.Options{
			SnapshotRoot: root, ModulePath: testModule,
		}),
		fail("source is missing", instrument.CodeSourceUnreadable,
			instrumentCandidate(t, lessSource, mutation.Candidate{
				Path:        "gone.go",
				Rule:        lookupRule(t, "lt-to-le"),
				Span:        spanOf(t, lessSource, "<"),
				Original:    "<",
				Replacement: "<=",
			})),
		fail("source does not parse", instrument.CodeUnparsable,
			instrumentCorrupted(t, lessSource, "package sample\n\nthis is not Go\n")),
		fail("a catalogued mutant with no hint", instrument.CodeMissingGuard,
			instrumentHinted(t, lessSource, lessCandidate(t), nil)),
		fail("a hint whose site is no expression", instrument.CodeSiteNotFound,
			instrumentHinted(t, lessSource, lessCandidate(t), &discover.Guard{
				Form:     discover.GuardFormC,
				SiteSpan: spanOf(t, lessSource, "return a < b"),
			})),
		fail("a hint naming a statement Form S may not wrap", instrument.CodeUnsupportedGuard,
			instrumentHinted(t, branchSource, branchCandidate(t), &discover.Guard{
				Form:     discover.GuardFormS,
				SiteSpan: spanOf(t, branchSource, ifStatement),
			})),
		fail("a hint naming a statement Form D cannot declare", instrument.CodeUnsupportedGuard,
			instrumentHinted(t, branchSource, assignCandidate(t), &discover.Guard{
				Form:     discover.GuardFormD,
				SiteSpan: spanOf(t, branchSource, "a += b"),
			})),
		fail("sites partially overlap", instrument.CodeSiteConflict,
			instrument.PlaceSites(sampleFile, []mutation.Span{{StartByte: 0, EndByte: 10}, {StartByte: 5, EndByte: 15}})),
		fail("the rewrite moved a line", instrument.CodeLineDrift,
			instrument.CheckLineCount(sampleFile, []byte("a\nb\n"), []byte("a\nb\nc\n"))),
		fail("no package clause to import from", instrument.CodeImportInjection,
			injectInto(t, "package sample\n", func(file *ast.File) { file.Name = nil })),
		fail("an import declaration no parse produces", instrument.CodeImportInjection,
			injectInto(t, "package sample\n\nimport \"fmt\"\n", func(file *ast.File) {
				decl := file.Decls[0].(*ast.GenDecl)
				decl.Specs = append(decl.Specs, decl.Specs[0])
			})),
	}
	out = append(out, fail("the runtime package cannot be written", instrument.CodeWriteFailed,
		instrument.WriteRuntime(notADirectory, "gomutants_rt", empty)))
	return out
}

const (
	lessSource   = "package sample\n\nfunc Less(a, b int) bool {\n\treturn a < b\n}\n"
	branchSource = "package sample\n\nfunc F(a, b int) int {\n\tif a < b {\n\t\ta += b\n\t}\n\treturn a\n}\n"
	ifStatement  = "if a < b {\n\t\ta += b\n\t}"
)

func lessCandidate(t *testing.T) mutation.Candidate {
	t.Helper()
	return mutation.Candidate{
		Rule:        lookupRule(t, "lt-to-le"),
		Span:        spanOf(t, lessSource, "<"),
		Original:    "<",
		Replacement: "<=",
	}
}

func branchCandidate(t *testing.T) mutation.Candidate {
	t.Helper()
	return mutation.Candidate{
		Rule:        lookupRule(t, "lt-to-le"),
		Span:        spanOf(t, branchSource, "<"),
		Original:    "<",
		Replacement: "<=",
	}
}

func assignCandidate(t *testing.T) mutation.Candidate {
	t.Helper()
	return mutation.Candidate{
		Rule:        lookupRule(t, "add-assign-to-sub-assign"),
		Span:        spanOf(t, branchSource, "+="),
		Original:    "+=",
		Replacement: "-=",
	}
}

func instrumentCandidate(t *testing.T, src string, candidate mutation.Candidate) error {
	t.Helper()
	return instrumentWith(t, src, candidate, func(catalog *mutation.Catalog) instrument.Hints {
		return hintsInSource(t, []byte(src), catalog, hintOptions{})
	})
}

func instrumentHinted(t *testing.T, src string, candidate mutation.Candidate, guard *discover.Guard) error {
	t.Helper()
	return instrumentWith(t, src, candidate, func(catalog *mutation.Catalog) instrument.Hints {
		hints := make(instrument.Hints, catalog.Len())
		for _, m := range catalog.Mutants() {
			if guard != nil {
				hints[m.ID] = *guard
			}
		}
		return hints
	})
}

func instrumentWith(
	t *testing.T,
	src string,
	candidate mutation.Candidate,
	hints func(*mutation.Catalog) instrument.Hints,
) error {
	t.Helper()

	root := t.TempDir()
	testkit.WriteFile(t, filepath.Join(root, sampleFile), []byte(src))
	if candidate.Path == "" {
		candidate.Path = sampleFile
	}
	if candidate.SourceDigest == "" {
		candidate.SourceDigest = mutation.Digest([]byte(src))
	}
	catalog := catalogOf(t, []mutation.Candidate{candidate})
	_, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: root,
		ModulePath:   testModule,
		Catalog:      catalog,
		Hints:        hints(catalog),
	})
	return err
}

func instrumentCorrupted(t *testing.T, src, replacement string) error {
	t.Helper()

	root := t.TempDir()
	target := filepath.Join(root, sampleFile)
	testkit.WriteFile(t, target, []byte(src))
	catalog := catalogOf(t, candidatesIn(t, []byte(src)))
	hints := hintsInSource(t, []byte(src), catalog, hintOptions{})
	testkit.WriteFile(t, target, []byte(replacement))

	_, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: root,
		ModulePath:   testModule,
		Catalog:      catalog,
		Hints:        hints,
	})
	return err
}

func injectInto(t *testing.T, src string, mangle func(*ast.File)) error {
	t.Helper()

	file, tok, err := instrument.ParseSnapshot(sampleFile, []byte(src))
	if err != nil {
		t.Fatalf("parsing %q: %v", src, err)
	}
	mangle(file)
	_, err = instrument.ImportSplices(file, tok, sampleFile, "__gm", testModule+"/gomutants_rt")
	return err
}

func spanOf(t *testing.T, src, text string) mutation.Span {
	t.Helper()
	i := strings.Index(src, text)
	if i < 0 {
		t.Fatalf("%q does not occur in the fixture", text)
	}
	return mutation.Span{StartByte: uint32(i), EndByte: uint32(i + len(text))}
}

func TestAliasAvoidsEveryNameInScope(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name     string
		src      string
		reserved []string
		want     string
	}{{
		name: "nothing in the way",
		src:  "package sample\n\nfunc F() {}\n",
		want: "__gm",
	}, {
		name: "a file-scope declaration",
		src:  "package sample\n\nvar __gm int\n",
		want: "__gm1",
	}, {
		name: "a local variable",
		src:  "package sample\n\nfunc F() { __gm := 1; _ = __gm }\n",
		want: "__gm1",
	}, {
		name: "an implicit import name",
		src:  "package sample\n\nimport \"example.com/__gm\"\n",
		want: "__gm1",
	}, {
		name: "a run of collisions",
		src:  "package sample\n\nvar (__gm, __gm1, __gm2 int)\n",
		want: "__gm3",
	}, {
		name:     "a sibling file's declaration",
		src:      "package sample\n\nfunc F() {}\n",
		reserved: []string{"__gm"},
		want:     "__gm1",
	}, {
		name:     "a name each scope contributes",
		src:      "package sample\n\nfunc F() { __gm := 1; _ = __gm }\n",
		reserved: []string{"__gm1"},
		want:     "__gm2",
	}, {
		name:     "a sibling that binds the bumped name and not the base",
		src:      "package sample\n\nfunc F() {}\n",
		reserved: []string{"__gm1", "__gm2"},
		want:     "__gm",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			file, _, err := instrument.ParseSnapshot(sampleFile, []byte(c.src))
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}
			if got := instrument.AliasFor(file, c.reserved); got != c.want {
				t.Errorf("AliasFor = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPackageBlockNamesSpanTheWholeDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testkit.WriteFile(t, filepath.Join(dir, "a.go"), []byte(
		"package sample\n\nvar Declared = 1\n\nfunc F() {}\n\ntype T struct{}\n\nfunc (T) method() {}\n\nconst K = 2\n"))
	testkit.WriteFile(t, filepath.Join(dir, "b.go"), []byte(
		"package sample\n\nfunc Sibling() { local := 1; _ = local }\n"))
	testkit.WriteFile(t, filepath.Join(dir, "a_test.go"), []byte(
		"package sample\n\nvar inTest = 3\n"))
	testkit.WriteFile(t, filepath.Join(dir, "b_test.go"), []byte(
		"package sample_test\n\nvar inExternalTest = 4\n"))
	testkit.WriteFile(t, filepath.Join(dir, "gen.go"), []byte(
		"//go:build ignore\n\npackage main\n\nvar inIgnoredFile = 5\n"))
	testkit.WriteFile(t, filepath.Join(dir, "notes.txt"), []byte("var notGo = 6\n"))

	got, err := instrument.PackageNames(dir, "sample")
	if err != nil {
		t.Fatalf("PackageNames: %v", err)
	}
	want := []string{"Declared", "F", "K", "Sibling", "T", "inTest"}
	if !equalStrings(got, want) {
		t.Errorf("PackageNames = %v, want %v", got, want)
	}
}

func TestPackageBlockNamesFallBackToTokens(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testkit.WriteFile(t, filepath.Join(dir, "a.go"), []byte("package sample\n\nfunc F() {}\n"))
	testkit.WriteFile(t, filepath.Join(dir, "broken.go"), []byte(
		"package sample\n\nfunc Broken() bool { return __gm & } \n"))

	got, err := instrument.PackageNames(dir, "sample")
	if err != nil {
		t.Fatalf("PackageNames: %v", err)
	}
	if !slices.Contains(got, "__gm") {
		t.Errorf("PackageNames = %v, want it to hold %q from the file that does not parse", got, "__gm")
	}
}

func TestPackageBlockNamesReportsAnUnreadableDirectory(t *testing.T) {
	t.Parallel()

	notADirectory := filepath.Join(t.TempDir(), "file.txt")
	testkit.WriteFile(t, notADirectory, []byte("not a directory"))

	if _, err := instrument.PackageNames(notADirectory, "sample"); err == nil {
		t.Error("scanning a directory that is a file returned no error")
	} else if got := instrument.CodeOf(err); got != instrument.CodeSourceUnreadable {
		t.Errorf("code %q, want %q (%v)", got, instrument.CodeSourceUnreadable, err)
	}
}

func TestImportInjectionForms(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		want string
	}{{
		name: "a parenthesized list",
		src:  "package sample\n\nimport (\n\t\"fmt\"\n)\n",
		want: "package sample\n\nimport (__gm \"example.com/mini/gomutants_rt\";\n\t\"fmt\"\n)\n",
	}, {
		name: "a parenthesized list with a comment after the paren",
		src:  "package sample\n\nimport ( // dependencies\n\t\"fmt\"\n)\n",
		want: "package sample\n\nimport (__gm \"example.com/mini/gomutants_rt\"; // dependencies\n\t\"fmt\"\n)\n",
	}, {
		name: "a single import",
		src:  "package sample\n\nimport \"fmt\"\n",
		want: "package sample\n\nimport (\"fmt\"; __gm \"example.com/mini/gomutants_rt\")\n",
	}, {
		name: "a single import with a trailing comment",
		src:  "package sample\n\nimport \"fmt\" // printing\n",
		want: "package sample\n\nimport (\"fmt\"; __gm \"example.com/mini/gomutants_rt\") // printing\n",
	}, {
		name: "a single import across lines",
		src:  "package sample\n\nimport\n\t\"fmt\"\n",
		want: "package sample\n\nimport\n\t(\"fmt\"; __gm \"example.com/mini/gomutants_rt\")\n",
	}, {
		name: "a blank import",
		src:  "package sample\n\nimport _ \"embed\"\n",
		want: "package sample\n\nimport (_ \"embed\"; __gm \"example.com/mini/gomutants_rt\")\n",
	}, {
		name: "no imports at all",
		src:  "package sample\n\nfunc F() {}\n",
		want: "package sample; import __gm \"example.com/mini/gomutants_rt\"\n\nfunc F() {}\n",
	}, {
		name: "an empty list",
		src:  "package sample\n\nimport ()\n",
		want: "package sample\n\nimport (__gm \"example.com/mini/gomutants_rt\";)\n",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			file, tok, err := instrument.ParseSnapshot(sampleFile, []byte(c.src))
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}
			splices, err := instrument.ImportSplices(file, tok, sampleFile, "__gm", testModule+"/gomutants_rt")
			if err != nil {
				t.Fatalf("ImportSplices: %v", err)
			}
			if !instrument.LinePreserving(splices) {
				t.Error("the injected import is not line-preserving")
			}
			out, _, err := instrument.Apply([]byte(c.src), splices)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if string(out) != c.want {
				t.Errorf("injected import:\n got %q\nwant %q", out, c.want)
			}
			if _, _, err := instrument.ParseSnapshot(sampleFile, out); err != nil {
				t.Errorf("the file no longer parses: %v", err)
			}
			if got, want := instrument.CountLines(out), instrument.CountLines([]byte(c.src)); got != want {
				t.Errorf("the file holds %d line breaks, it held %d", got, want)
			}
		})
	}
}

func TestCompletedImportsAreSplicedBesideTheRuntime(t *testing.T) {
	t.Parallel()

	runtimePath := testModule + "/gomutants_rt"
	completions := []discover.Completion{
		{Path: "time", Local: "time"},
		{Path: "example.com/mini/carrier", Local: "carrier"},
	}
	const added = `time "time";carrier "example.com/mini/carrier"`
	const spaced = `time "time"; carrier "example.com/mini/carrier"`
	for _, c := range []struct {
		name string
		src  string
		want string
	}{{
		name: "a parenthesized list",
		src:  "package sample\n\nimport (\n\t\"fmt\"\n)\n",
		want: "package sample\n\nimport (__gm \"" + runtimePath + "\";" + added + ";\n\t\"fmt\"\n)\n",
	}, {
		name: "a single unparenthesized import",
		src:  "package sample\n\nimport \"fmt\"\n",
		want: "package sample\n\nimport (\"fmt\"; __gm \"" + runtimePath + "\"; " + spaced + ")\n",
	}, {
		name: "no imports at all",
		src:  "package sample\n\nfunc F() {}\n",
		want: "package sample; import (__gm \"" + runtimePath + "\"; " + spaced + ")\n\nfunc F() {}\n",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			file, tok, err := instrument.ParseSnapshot(sampleFile, []byte(c.src))
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}
			splices, err := instrument.ImportSplices(
				file, tok, sampleFile, "__gm", runtimePath, completions...)
			if err != nil {
				t.Fatalf("ImportSplices: %v", err)
			}
			if !instrument.LinePreserving(splices) {
				t.Error("the completed imports are not line-preserving")
			}
			out, _, err := instrument.Apply([]byte(c.src), splices)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if string(out) != c.want {
				t.Errorf("injected imports:\n got %q\nwant %q", out, c.want)
			}
			if _, _, err := instrument.ParseSnapshot(sampleFile, out); err != nil {
				t.Errorf("the file no longer parses: %v", err)
			}
			if got, want := instrument.CountLines(out), instrument.CountLines([]byte(c.src)); got != want {
				t.Errorf("the file holds %d line breaks, it held %d", got, want)
			}
		})
	}
}

func TestACompletionThatCollidesWithTheRuntimeAliasIsRefused(t *testing.T) {
	t.Parallel()

	src := "package sample\n\nfunc F() {}\n"
	file, tok, err := instrument.ParseSnapshot(sampleFile, []byte(src))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	for _, c := range []struct {
		name        string
		completions []discover.Completion
	}{
		{"the runtime alias", []discover.Completion{{Path: "time", Local: "__gm"}}},
		{"another completion", []discover.Completion{
			{Path: "time", Local: "clock"},
			{Path: "example.com/mini/carrier", Local: "clock"},
		}},
		{"a completion with no name", []discover.Completion{{Path: "time"}}},
		{"a completion with no path", []discover.Completion{{Local: "time"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			_, err := instrument.ImportSplices(
				file, tok, sampleFile, "__gm", testModule+"/gomutants_rt", c.completions...)
			if err == nil {
				t.Fatal("the injection was accepted, so the file would declare one name twice")
			}
			if got := instrument.CodeOf(err); got != instrument.CodeImportInjection {
				t.Errorf("code = %q, want %q", got, instrument.CodeImportInjection)
			}
		})
	}
}

func TestImportGoesOnlyToInstrumentedFiles(t *testing.T) {
	t.Parallel()

	in := testkit.ReadFile(t, filepath.Join("testdata", "comparison.input"))
	root := t.TempDir()
	testkit.WriteFile(t, filepath.Join(root, sampleFile), in)
	testkit.WriteFile(t, filepath.Join(root, "quiet.go"), []byte("package sample\n\nvar Quiet = 1\n"))

	instrumentSnapshot(t, root, catalogOf(t, candidatesFor(t, nil, in)))

	if got := testkit.ReadFile(t, filepath.Join(root, "quiet.go")); strings.Contains(string(got), "gomutants_rt") {
		t.Errorf("an uninstrumented file imported the runtime:\n%s", got)
	}
}
