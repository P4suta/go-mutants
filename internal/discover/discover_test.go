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
var wantCandidates = []string{
	"compare/compare.go eq-to-neq ==->!=",
	"compare/compare.go neq-to-eq !=->==",
	"compare/compare.go lt-to-le <-><=",
	"compare/compare.go le-to-lt <=-><",
	"compare/compare.go gt-to-ge >->>=",
	"compare/compare.go ge-to-gt >=->>",
	"compare/compare.go true-to-false true->false",
	"compare/compare.go false-to-true false->true",
	"compare/compare.go true-to-false true->false",
	"generics/generics.go gt-to-ge >->>=",
	"legacy/legacy.go eq-to-neq ==->!=",
	"runes/runes.go gt-to-ge >->>=",
	"runes/runes.go lt-to-le <-><=",
	// The two shadowed `true`s in this file are absent on purpose: one is a
	// package-level constant of the package's own, the other a local variable,
	// and neither is the universe constant the rule is about. The `false`
	// beside them still is.
	"shadow/shadow.go false-to-true false->true",
	"suppressed/suppressed.go eq-to-neq ==->!=",
	"suppressed/suppressed.go true-to-false true->false",
	"suppressed/suppressed.go gt-to-ge >->>=",
	"suppressed/suppressed.go eq-to-neq ==->!=",
	"suppressed/suppressed.go true-to-false true->false",
}

// wantSkips is every recorded reason for the same run.
var wantSkips = []string{
	"cgopkg/cgo.go cgo 1",
	"cgopkg/pure.go cgo 1",
	"generated/generated.go generated 1",
	// One for the generic function's constraint, one for the generic type's,
	// one for the single explicit type argument, and two for the list form.
	"generics/generics.go type-param 5",
	"suppressed/suppressed.go array-length 2",
	"suppressed/suppressed.go case-label 4",
	"suppressed/suppressed.go const-decl 4",
	"suppressed/suppressed.go package-var-init 3",
}

func TestDiscoverFindsEveryImplementedRule(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{})
	equalStrings(t, summarize(result.Candidates), wantCandidates)
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
	equalStrings(t, summarize(result.Candidates), []string{"plain.go gt-to-ge >->>="})
	assertCandidatesMatchTheFile(t, root, result.Candidates)
	if len(result.Candidates) != 1 {
		return
	}
	got := result.Candidates[0]
	if got.Line != crlfCandidateLine || got.Column != crlfCandidateColumn {
		t.Errorf("the operator is reported at %d:%d, want %d:%d",
			got.Line, got.Column, crlfCandidateLine, crlfCandidateColumn)
	}
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
	if len(result.Candidates) != 9 {
		t.Errorf("got %d candidates, want the 9 in compare: %v", len(result.Candidates), summarize(result.Candidates))
	}
	// Everything else becomes an excluded skip rather than disappearing.
	for _, path := range []string{
		"legacy/legacy.go", "generics/generics.go", "suppressed/suppressed.go",
		"cgopkg/cgo.go", "cgopkg/pure.go", "generated/generated.go",
		"runes/runes.go", "shadow/shadow.go",
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
	// A rule this phase has not implemented yet is ignored rather than
	// refused, so a caller may pass a whole profile.
	unimplemented, ok := registry.Lookup("add-to-sub")
	if !ok {
		t.Fatal("the canonical registry has no add-to-sub")
	}
	result := discoverFixture(t, "mainmod", Options{Rules: []mutation.Rule{rule, unimplemented}})
	for _, c := range result.Candidates {
		if c.Rule.Name != "eq-to-neq" {
			t.Errorf("unselected rule produced a candidate: %s at %s", c.Rule.Name, c.Path)
		}
	}
	if len(result.Candidates) != 4 {
		t.Errorf("got %d eq-to-neq candidates, want 4: %v", len(result.Candidates), summarize(result.Candidates))
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

func TestBuildCatalogAcceptsEveryCandidate(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{})
	catalog, err := BuildCatalog(result)
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	if catalog.Len() != len(result.Candidates) {
		t.Errorf("catalogue holds %d mutants, want %d candidates", catalog.Len(), len(result.Candidates))
	}
	if len(catalog.Duplicates()) != 0 {
		t.Errorf("discovery produced duplicate edits: %v", catalog.Duplicates())
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
	if len(rules) != 8 {
		t.Fatalf("got %d supported rules, want the 6 comparison and 2 boolean-literal rules", len(rules))
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
