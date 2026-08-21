// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The half of the discovery tests that needs no toolchain: the decisions this
// package makes on its own, before and after the loader is involved.
package discover

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// lookupEnv returns the values a "KEY=VALUE" environment gives one variable,
// in order. It returns every one of them on purpose: a duplicate is a defect
// this package's environment building has to be caught at, not a detail
// os/exec's last-one-wins rule may paper over.
func lookupEnv(env []string, name string) []string {
	var values []string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && sameEnvKey(key, name) {
			values = append(values, value)
		}
	}
	return values
}

func TestEnvironmentPutsTheToolchainInFrontOfPath(t *testing.T) {
	dir := filepath.Join("opt", "go", "bin")
	t.Setenv("PATH", filepath.Join("usr", "bin"))
	env := environment(gocmd.Toolchain{GoBin: filepath.Join(dir, "go")})

	values := lookupEnv(env, "PATH")
	if len(values) != 1 {
		t.Fatalf("the child environment has %d PATH entries, want 1: %v", len(values), values)
	}
	path := values[0]
	if !strings.HasPrefix(path, dir+string(filepath.ListSeparator)) {
		t.Errorf("PATH = %q, want it to start with %q", path, dir)
	}
	if !strings.Contains(path, filepath.Join("usr", "bin")) {
		t.Errorf("PATH = %q, want the original entries kept", path)
	}
}

func TestEnvironmentLeavesAnAlreadyLeadingToolchainAlone(t *testing.T) {
	dir := filepath.Join("opt", "go", "bin")
	original := dir + string(filepath.ListSeparator) + filepath.Join("usr", "bin")
	t.Setenv("PATH", original)
	env := environment(gocmd.Toolchain{GoBin: filepath.Join(dir, "go")})
	if got := lookupEnv(env, "PATH"); len(got) != 1 || got[0] != original {
		t.Errorf("PATH = %v, want the single unchanged entry %q", got, original)
	}
}

func TestEnvironmentLeavesPathAloneWithoutAToolchain(t *testing.T) {
	original := filepath.Join("usr", "bin")
	t.Setenv("PATH", original)
	env := environment(gocmd.Toolchain{})
	if got := lookupEnv(env, "PATH"); len(got) != 1 || got[0] != original {
		t.Errorf("PATH = %v, want the single unchanged entry %q", got, original)
	}
}

// TestEnvironmentSwitchesWorkspaceModeOff is the guarantee behind
// [CodeWorkspace]: refusing a `go.work` at the snapshot root only settles the
// file that is in the snapshot, and the go command would find one in a parent
// directory or through $GOWORK too. The loader is pinned instead, so whatever
// the caller's environment says about workspaces, discovery resolves the
// snapshot and nothing else.
//
// A zero toolchain is covered because the PATH branch returns early, and the
// pin has to survive that.
func TestEnvironmentSwitchesWorkspaceModeOff(t *testing.T) {
	toolchains := map[string]gocmd.Toolchain{
		"located": {GoBin: filepath.Join("opt", "go", "bin", "go")},
		"absent":  {},
	}
	ambient := map[string]func(t *testing.T){
		"unset": func(t *testing.T) {
			t.Helper()
			// t.Setenv first, so that the cleanup it registers puts the
			// developer's own GOWORK back afterwards.
			t.Setenv("GOWORK", "restored by cleanup")
			if err := os.Unsetenv("GOWORK"); err != nil {
				t.Fatalf("unsetting GOWORK: %v", err)
			}
		},
		"a path": func(t *testing.T) {
			t.Helper()
			t.Setenv("GOWORK", filepath.Join("elsewhere", "go.work"))
		},
		"already off": func(t *testing.T) {
			t.Helper()
			t.Setenv("GOWORK", "off")
		},
	}
	for toolchainName, tc := range toolchains {
		for ambientName, set := range ambient {
			t.Run(toolchainName+"/"+ambientName, func(t *testing.T) {
				set(t)
				got := lookupEnv(environment(tc), "GOWORK")
				if len(got) != 1 || got[0] != "off" {
					t.Errorf("GOWORK = %v, want exactly one entry set to off", got)
				}
			})
		}
	}
}

func TestSetEnvReplacesEveryEntryForTheVariable(t *testing.T) {
	env := []string{"A=1", "GOWORK=first", "B=2", "GOWORK=second"}
	got := setEnv(env, "GOWORK", "off")
	want := []string{"A=1", "GOWORK=off", "B=2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("setEnv = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(env, []string{"A=1", "GOWORK=first", "B=2", "GOWORK=second"}) {
		t.Errorf("setEnv modified its argument: %v", env)
	}
	if got := setEnv([]string{"A=1"}, "GOWORK", "off"); !reflect.DeepEqual(got, []string{"A=1", "GOWORK=off"}) {
		t.Errorf("setEnv on an environment without the variable = %v, want it appended", got)
	}
}

func TestSameEnvKeyFollowsThePlatform(t *testing.T) {
	if got, want := sameEnvKey("Path", "PATH"), runtime.GOOS == "windows"; got != want {
		t.Errorf("sameEnvKey(Path, PATH) = %v, want %v", got, want)
	}
	if !sameEnvKey("PATH", "PATH") {
		t.Error("sameEnvKey does not match a variable with itself")
	}
}

// parseFixture parses one in-memory file the way the loader would, comments
// included.
func parseFixture(t *testing.T, src string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	return file
}

func TestIsGenerated(t *testing.T) {
	cases := map[string]struct {
		src  string
		want bool
	}{
		"marker before the package clause": {
			src:  "// Code generated by stringer. DO NOT EDIT.\n\npackage p\n",
			want: true,
		},
		"marker with CRLF line endings": {
			src:  "// Code generated by stringer. DO NOT EDIT.\r\n\r\npackage p\r\n",
			want: true,
		},
		"marker after the package clause": {
			src:  "package p\n\n// Code generated by stringer. DO NOT EDIT.\nvar x = 1\n",
			want: false,
		},
		"marker with trailing text": {
			src:  "// Code generated by stringer. DO NOT EDIT. really\n\npackage p\n",
			want: false,
		},
		"ordinary comment": {
			src:  "// Package p is written by hand.\npackage p\n",
			want: false,
		},
		"no comment at all": {
			src:  "package p\n",
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isGenerated(parseFixture(t, tc.src)); got != tc.want {
				t.Errorf("isGenerated = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSuppressedReportsTheWidestRegion(t *testing.T) {
	scan := &fileScan{suppressions: []suppression{
		{start: 10, end: 100, reason: SkipTypeParam},
		{start: 20, end: 40, reason: SkipArrayLength},
		{start: 200, end: 300, reason: SkipConstDecl},
	}}
	cases := map[token.Pos]struct {
		reason     SkipReason
		suppressed bool
	}{
		30:  {SkipTypeParam, true}, // inside both: the outer one is the answer
		50:  {SkipTypeParam, true}, // inside only the outer one
		150: {"", false},           // between two regions
		250: {SkipConstDecl, true}, // inside the third
		100: {"", false},           // the end is exclusive
		10:  {SkipTypeParam, true}, // the start is inclusive
	}
	for pos, want := range cases {
		reason, ok := scan.suppressed(pos)
		if ok != want.suppressed || reason != want.reason {
			t.Errorf("suppressed(%d) = (%q, %v), want (%q, %v)", pos, reason, ok, want.reason, want.suppressed)
		}
	}
}

func TestWiderBreaksTiesDeterministically(t *testing.T) {
	same := suppression{start: 1, end: 10, reason: SkipCaseLabel}
	other := suppression{start: 1, end: 10, reason: SkipConstDecl}
	if !wider(other, same) {
		t.Error("two identical regions must resolve by the frozen reason order")
	}
	if wider(same, other) {
		t.Error("the tie-break is not antisymmetric")
	}
}

func TestSelectionAppliesExcludesAfterIncludes(t *testing.T) {
	d := &discovery{
		include: patterns(t, "internal/**", "cmd/**"),
		exclude: patterns(t, "internal/legacy/**"),
	}
	cases := map[string]bool{
		"internal/mutation/span.go": false,
		"cmd/go-mutants/main.go":    false,
		"internal/legacy/old.go":    true,
		"docs/architecture.go":      true,
	}
	for path, want := range cases {
		reason, excluded := d.selection(path)
		if excluded != want {
			t.Errorf("selection(%q) excluded = %v, want %v", path, excluded, want)
		}
		if excluded && reason != SkipExcluded {
			t.Errorf("selection(%q) reason = %q, want %q", path, reason, SkipExcluded)
		}
	}
}

func TestSelectionWithNoIncludesKeepsEverything(t *testing.T) {
	d := &discovery{}
	if _, excluded := d.selection("anything/at/all.go"); excluded {
		t.Error("an empty include set must include everything")
	}
}

func TestRelativePathRefusesToLeaveTheModule(t *testing.T) {
	root := filepath.Join(string(filepath.Separator)+"module", "root")
	inside := filepath.Join(root, "pkg", "file.go")
	if rel, ok := relativePath(root, inside); !ok || rel != "pkg/file.go" {
		t.Errorf("relativePath(inside) = (%q, %v), want (pkg/file.go, true)", rel, ok)
	}
	outside := filepath.Join(string(filepath.Separator)+"module", "other", "file.go")
	if rel, ok := relativePath(root, outside); ok {
		t.Errorf("relativePath(outside) = (%q, true), want false", rel)
	}
	if rel, ok := relativePath(root, root); ok {
		t.Errorf("relativePath(root itself) = (%q, true), want false", rel)
	}
}

// TestFormatPackageErrorIsAlwaysOneLine pins the shape every diagnostic
// go-mutants prints depends on.
//
// A `go list` failure arrives as the command's whole standard error in a single
// [packages.Error.Msg]: a `# import/path` banner, then one line per compiler
// diagnostic. Left as it is, it turns one coded error into several lines of
// which only the first carries "GOM4111:", and the ones that go uncoded are
// precisely the ones naming the file and column — invisible to `grep '^error '`
// and to every CI log parser downstream.
func TestFormatPackageErrorIsAlwaysOneLine(t *testing.T) {
	tests := []struct {
		name string
		pkg  string
		err  packages.Error
		want string
	}{
		{
			name: "a go list stderr blob",
			pkg:  "scratch.example/broken",
			err:  packages.Error{Msg: "# scratch.example/broken\n.\\broken.go:3:39: undefined: undefinedThing\n"},
			want: "scratch.example/broken: # scratch.example/broken; .\\broken.go:3:39: undefined: undefinedThing",
		},
		{
			name: "carriage returns and blank lines",
			pkg:  "scratch.example/broken",
			err:  packages.Error{Msg: "first\r\n\r\nsecond\r\n"},
			want: "scratch.example/broken: first; second",
		},
		{
			name: "an ordinary type error keeps its position",
			pkg:  "scratch.example/broken",
			err:  packages.Error{Pos: "broken.go:3:39", Msg: "undefined: undefinedThing"},
			want: "scratch.example/broken: broken.go:3:39: undefined: undefinedThing",
		},
		{
			name: "no package path",
			err:  packages.Error{Msg: "one\ntwo"},
			want: "one; two",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := formatPackageError(&packages.Package{PkgPath: test.pkg}, test.err)
			if got != test.want {
				t.Errorf("formatPackageError = %q, want %q", got, test.want)
			}
			if strings.ContainsAny(got, "\n\r") {
				t.Errorf("formatPackageError returned more than one line: %q", got)
			}
		})
	}
}

func TestCollapseLinesLeavesASingleLineAlone(t *testing.T) {
	// The common case has to be untouched: a type error is already one line, and
	// folding must not turn its own punctuation into a join.
	for _, s := range []string{"", "undefined: x", "a; b", "  padded  "} {
		want := strings.TrimSpace(s)
		if got := collapseLines(s); got != want {
			t.Errorf("collapseLines(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestIsTestFile(t *testing.T) {
	cases := map[string]bool{
		filepath.Join("pkg", "thing_test.go"): true,
		filepath.Join("pkg", "thing.go"):      false,
		filepath.Join("pkg", "test.go"):       false,
		filepath.Join("pkg", "_test.go"):      true,
	}
	for path, want := range cases {
		if got := isTestFile(path); got != want {
			t.Errorf("isTestFile(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestCgoExemptionCoversTestVariants pins the gate's exemption to a whole
// package rather than to the one variant that happens to own the cgo file.
//
// An external test package owns nothing but test files and the generated test
// main package owns a file in the build cache, so neither can be recognised
// from source — and both fail for exactly one reason when the cgo package
// beside them does.
func TestCgoExemptionCoversTestVariants(t *testing.T) {
	exemption := cgoExemption{
		ids:   map[string]bool{"example.com/m/cgopkg": true},
		bases: map[string]bool{"example.com/m/cgopkg": true},
	}
	covered := []string{
		"example.com/m/cgopkg",
		"example.com/m/cgopkg [example.com/m/cgopkg.test]",
		"example.com/m/cgopkg_test [example.com/m/cgopkg.test]",
		"example.com/m/cgopkg.test",
	}
	for _, path := range covered {
		if !exemption.covers(&packages.Package{ID: path, PkgPath: path}) {
			t.Errorf("%s is not covered by the cgo exemption", path)
		}
	}
	uncovered := []string{
		"example.com/m/other",
		"example.com/m/cgopkgx",
		"example.com/m/cgopkg/inner",
	}
	for _, path := range uncovered {
		if exemption.covers(&packages.Package{ID: path, PkgPath: path}) {
			t.Errorf("%s is covered by the cgo exemption but should not be", path)
		}
	}
}

// scanFor builds a scan over in-memory source, the way the file walk would,
// and hands back the syntax tree so that a test can name the node an edit is
// anchored to.
//
// There is no type information: these tests are about the span invariant and
// the suppression bookkeeping, both of which are decided before any type gate
// is asked anything. The guard resolver works without it — with no types no
// expression can be proved to be the universe bool, so every hint here comes
// out as the statement form.
func scanFor(t *testing.T, src string) (*fileScan, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	tokFile := fset.File(file.Package)
	if tokFile == nil {
		t.Fatal("the parsed fixture has no position information")
	}
	return &fileScan{
		discovery: &discovery{skips: make(map[skipKey]int)},
		rel:       "p.go",
		pkgPath:   "example.com/p",
		src:       []byte(src),
		digest:    mutation.DigestString(src),
		tokFile:   tokFile,
		guard:     newGuardResolver(file, nil, nil, tokFile),
	}, file
}

// firstBinary returns the first binary expression of a parsed fixture, which is
// the node the emit tests anchor their edits to.
func firstBinary(t *testing.T, file *ast.File) *ast.BinaryExpr {
	t.Helper()
	var found *ast.BinaryExpr
	ast.Inspect(file, func(node ast.Node) bool {
		if found != nil {
			return false
		}
		if expr, ok := node.(*ast.BinaryExpr); ok {
			found = expr
		}
		return found == nil
	})
	if found == nil {
		t.Fatal("the fixture holds no binary expression")
	}
	return found
}

// ruleNamed looks up a canonical rule for a test.
func ruleNamed(t *testing.T, name string) mutation.Rule {
	t.Helper()
	rule, ok := mutation.CanonicalRegistry().Lookup(name)
	if !ok {
		t.Fatalf("the canonical registry has no %s", name)
	}
	return rule
}

func TestEmitRecordsTheSpanAndThePosition(t *testing.T) {
	const src = "package p\n\nfunc f(a, b int) bool { return a != b }\n"
	scan, file := scanFor(t, src)
	anchor := firstBinary(t, file)
	offset := strings.Index(src, "!=")
	if err := scan.emit(ruleNamed(t, "neq-to-eq"), anchor, scan.tokFile.Pos(offset), "!=", "=="); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(scan.candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(scan.candidates))
	}
	got := scan.candidates[0]
	if got.Span.StartByte != uint32(offset) || got.Span.EndByte != uint32(offset+2) {
		t.Errorf("span = %s, want [%d,%d)", got.Span, offset, offset+2)
	}
	if got.Line != 3 {
		t.Errorf("line = %d, want 3", got.Line)
	}
	// Column is a byte offset within the line, counted from one.
	line := strings.Split(src, "\n")[2]
	if want := strings.Index(line, "!=") + 1; got.Column != want {
		t.Errorf("column = %d, want %d", got.Column, want)
	}
	if got.Package != "example.com/p" || got.SourceDigest != mutation.DigestString(src) {
		t.Errorf("candidate = %+v, want the scan's package and digest", got)
	}
	// The hint is the statement the edit sits in, because nothing here can
	// prove an expression is the universe bool without type information.
	if got.Guard.Form != GuardFormS {
		t.Errorf("guard form = %q, want %q", got.Guard.Form, GuardFormS)
	}
	if !got.Guard.SiteSpan.Contains(got.Span) {
		t.Errorf("the guard site %s does not contain the edit %s", got.Guard.SiteSpan, got.Span)
	}
	if want := "return a != b"; string(src[got.Guard.SiteSpan.StartByte:got.Guard.SiteSpan.EndByte]) != want {
		t.Errorf("the guard site covers %q, want %q",
			src[got.Guard.SiteSpan.StartByte:got.Guard.SiteSpan.EndByte], want)
	}
}

// TestEmitRefusesASpanThatMissesItsText is the invariant that keeps a wrong
// span from ever reaching the instrumenter: the bytes under the span have to
// be the text the rule claims it is replacing, and a mismatch is an error
// rather than a quietly mutated wrong expression.
func TestEmitRefusesASpanThatMissesItsText(t *testing.T) {
	const src = "package p\n\nfunc f(a, b int) bool { return a != b }\n"
	scan, file := scanFor(t, src)
	anchor := firstBinary(t, file)
	offset := strings.Index(src, "!=")
	err := scan.emit(ruleNamed(t, "eq-to-neq"), anchor, scan.tokFile.Pos(offset), "==", "!=")
	if CodeOf(err) != CodeSpanMismatch {
		t.Fatalf("code = %q, want %s (err %v)", CodeOf(err), CodeSpanMismatch, err)
	}
	if len(scan.candidates) != 0 {
		t.Errorf("a candidate survived the mismatch: %+v", scan.candidates)
	}
}

func TestEmitRefusesASpanPastTheEndOfTheFile(t *testing.T) {
	const src = "package p\n"
	scan, file := scanFor(t, src)
	err := scan.emit(ruleNamed(t, "eq-to-neq"), file, scan.tokFile.Pos(len(src)-1), "==", "!=")
	if CodeOf(err) != CodeSpanMismatch {
		t.Fatalf("code = %q, want %s (err %v)", CodeOf(err), CodeSpanMismatch, err)
	}
}

// TestEmitRefusesAnUnguardableSite is the second thing that removes a
// candidate, and the one this phase added: an edit whose rewrite site none of
// the three forms covers is a recorded skip, not a catalogued mutant an
// instrumenter would have to hand back. The `switch` tag is the shortest such
// site — no form wraps a switch, and no statement further out holds the edit.
func TestEmitRefusesAnUnguardableSite(t *testing.T) {
	const src = "package p\n\nfunc f(a, b int) { switch a != b {\n} }\n"
	scan, file := scanFor(t, src)
	anchor := firstBinary(t, file)
	offset := strings.Index(src, "!=")
	if err := scan.emit(ruleNamed(t, "neq-to-eq"), anchor, scan.tokFile.Pos(offset), "!=", "=="); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(scan.candidates) != 0 {
		t.Errorf("an unguardable candidate was emitted: %+v", scan.candidates)
	}
	if got := scan.skips[skipKey{path: "p.go", reason: SkipUnnameableDeclType}]; got != 1 {
		t.Errorf("unguardable count = %d, want 1", got)
	}
}

func TestEmitRecordsASuppressedCandidateInstead(t *testing.T) {
	const src = "package p\n\nfunc f(a, b int) bool { return a != b }\n"
	scan, file := scanFor(t, src)
	anchor := firstBinary(t, file)
	offset := strings.Index(src, "!=")
	scan.suppressions = []suppression{{start: scan.tokFile.Pos(0), end: scan.tokFile.Pos(len(src)), reason: SkipConstDecl}}
	if err := scan.emit(ruleNamed(t, "neq-to-eq"), anchor, scan.tokFile.Pos(offset), "!=", "=="); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(scan.candidates) != 0 {
		t.Errorf("a suppressed candidate was emitted: %+v", scan.candidates)
	}
	if got := scan.skips[skipKey{path: "p.go", reason: SkipConstDecl}]; got != 1 {
		t.Errorf("suppressed count = %d, want 1", got)
	}
}

func TestNewMatchersDefaultsToEverySupportedRule(t *testing.T) {
	m, err := newMatchers(nil)
	if err != nil {
		t.Fatalf("newMatchers(nil): %v", err)
	}
	tables := map[string]struct {
		got  int
		want int
	}{
		"comparison": {len(m.comparison), len(comparisonSwaps)},
		"connective": {len(m.connective), len(connectiveSwaps)},
		"integer":    {len(m.integer), len(integerSwaps)},
		"float":      {len(m.float), len(floatSwaps)},
		"bitwise":    {len(m.bitwise), len(bitwiseSwaps)},
		"assign":     {len(m.assignOp), len(assignSwaps)},
		"incdec":     {len(m.incDec), len(incDecSwaps)},
		"boolean":    {len(m.boolean), len(booleanSwaps)},
		"positional": {len(m.positional), len(positionalRules)},
	}
	for name, counts := range tables {
		if counts.got != counts.want {
			t.Errorf("got %d %s matchers, want %d", counts.got, name, counts.want)
		}
	}
	if m.empty() {
		t.Error("the default selection is empty")
	}
	// Every operator matcher replaces its operator with a different one, and
	// with the operator's own spelling on both sides.
	for _, table := range []map[token.Token]tokenMatcher{
		m.comparison, m.connective, m.integer, m.float, m.bitwise, m.assignOp, m.incDec,
	} {
		for tok, matcher := range table {
			if matcher.original != tok.String() {
				t.Errorf("%s: original = %q, want %q", tok, matcher.original, tok.String())
			}
			if matcher.replacement == matcher.original {
				t.Errorf("%s: replacement is the original", tok)
			}
		}
	}
}

// TestNewMatchersIgnoresAnUnimplementedRule keeps the "ignore what this phase
// has not built yet" path honest now that there is nothing left to ignore.
//
// Every rule in the canonical registry is implemented here, so the ignoring
// branch has no input any more. It stays in [newMatchers] because a v2 rule
// would arrive in the registry before it arrives in this package, and the first
// thing it must not do is fail every run that selected its family. The test
// therefore asserts the state of the world — the registry and the
// implementation agree exactly — rather than a behaviour nothing can reach.
func TestNewMatchersIgnoresAnUnimplementedRule(t *testing.T) {
	registry := mutation.CanonicalRegistry()
	for _, rule := range registry.Rules() {
		if !implementedNames[rule.Name] {
			m, err := newMatchers([]mutation.Rule{rule})
			if err != nil {
				t.Fatalf("newMatchers(%s): %v", rule, err)
			}
			if !m.empty() {
				t.Errorf("%s is not implemented but produced a matcher", rule)
			}
		}
	}
	if len(implementedNames) != registry.Len() {
		t.Errorf("this phase implements %d rules, the registry holds %d", len(implementedNames), registry.Len())
	}
	for name := range implementedNames {
		if _, ok := registry.Lookup(name); !ok {
			t.Errorf("this phase matches %q, which the canonical registry does not hold", name)
		}
	}
}

// skipReasonConstants returns the value of every SkipReason-typed constant
// this package declares, keyed by the name of the constant.
//
// The set is read out of the package's own sources rather than from a list
// typed into the test, because a list typed into the test is exactly the drift
// this guard exists to catch: it would be written once, agreeing with the
// constants of that day, and never looked at again.
func skipReasonConstants(t *testing.T) map[string]SkipReason {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	found := make(map[string]SkipReason)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			group, ok := decl.(*ast.GenDecl)
			if !ok || group.Tok != token.CONST {
				continue
			}
			collectSkipReasons(t, name, group, found)
		}
	}
	if len(found) == 0 {
		t.Fatal("no SkipReason constant was found in the package sources, so this guard is guarding nothing")
	}
	return found
}

// collectSkipReasons adds the SkipReason constants of one `const` group to
// found. A constant typed SkipReason that this cannot read is a fatal failure
// rather than a quiet skip: a constant the guard cannot see is a constant the
// guard does not guard.
func collectSkipReasons(t *testing.T, file string, group *ast.GenDecl, found map[string]SkipReason) {
	t.Helper()

	// Within a group, a spec with neither a type nor a value repeats the one
	// before it, so the declared type carries over. A spec with a value but no
	// type does not: its type comes from the value.
	declared := ""
	for _, spec := range group.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		switch {
		case value.Type != nil:
			declared = ""
			if ident, isIdent := value.Type.(*ast.Ident); isIdent {
				declared = ident.Name
			}
		case len(value.Values) > 0:
			declared = ""
		}
		if declared != "SkipReason" {
			// A constant named like a reason but written in a form this cannot
			// read — `SkipNinth = SkipReason("ninth")`, say — would slip past
			// the guard silently, which is the very failure this test exists to
			// end. Stop loudly instead.
			for _, name := range value.Names {
				if strings.HasPrefix(name.Name, "Skip") {
					t.Fatalf("%s declares %s, which reads as a skip reason but is not a SkipReason-typed constant this guard can see; teach this guard to read it", file, name.Name)
				}
			}
			continue
		}
		for i, name := range value.Names {
			if name.Name == "_" {
				continue
			}
			if i >= len(value.Values) {
				t.Fatalf("%s declares the SkipReason %s without a value of its own; teach this guard to read it", file, name.Name)
			}
			literal, isLiteral := value.Values[i].(*ast.BasicLit)
			if !isLiteral || literal.Kind != token.STRING {
				t.Fatalf("%s declares the SkipReason %s from something other than a string literal; teach this guard to read it", file, name.Name)
			}
			text, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr != nil {
				t.Fatalf("%s: unquoting the value of %s: %v", file, name.Name, unquoteErr)
			}
			if previous, clash := found[name.Name]; clash {
				t.Fatalf("%s declares %s twice, as %q and %q", file, name.Name, previous, text)
			}
			found[name.Name] = SkipReason(text)
		}
	}
}

// TestAllSkipReasonsListsEverySkipReasonConstant is the drift guard on
// [AllSkipReasons], and through it on everything derived from that list: the
// tie-break ranks here, and the `reason` enumeration of the run report schema
// over in internal/report.
//
// The list is compared against the constants the package really declares, read
// out of these sources, so a ninth Skip* constant fails this test in the commit
// that adds it rather than in the first run that emits it.
func TestAllSkipReasonsListsEverySkipReasonConstant(t *testing.T) {
	declared := skipReasonConstants(t)
	listed := AllSkipReasons()
	if len(listed) == 0 {
		t.Fatal("AllSkipReasons is empty")
	}

	names := make(map[SkipReason]string, len(declared))
	for name, reason := range declared {
		if clash, duplicate := names[reason]; duplicate {
			t.Errorf("the constants %s and %s are both %q", clash, name, reason)
		}
		names[reason] = name
	}
	seen := make(map[SkipReason]bool, len(listed))
	for _, reason := range listed {
		if reason == "" {
			t.Error("AllSkipReasons lists the empty reason")
			continue
		}
		if seen[reason] {
			t.Errorf("AllSkipReasons lists %q twice", reason)
		}
		seen[reason] = true
		if _, ok := names[reason]; !ok {
			t.Errorf("AllSkipReasons lists %q, which no constant in this package declares", reason)
		}
	}
	for name, reason := range declared {
		if !seen[reason] {
			t.Errorf("the constant %s (%q) is missing from AllSkipReasons, so nothing checks that the report schema accepts it", name, reason)
		}
	}
	if len(listed) != len(declared) {
		t.Errorf("AllSkipReasons has %d entries, but the package declares %d SkipReason constants", len(listed), len(declared))
	}
	if fresh := AllSkipReasons(); &fresh[0] == &listed[0] {
		t.Error("AllSkipReasons hands out one shared slice, which a caller could reorder out from under everyone else")
	}
}

// typedFixture type-checks one self-contained file and hands back the guard
// resolver for it.
//
// The Form D refusals below are the only decisions in this package that need
// both syntax and types, so [scanFor]'s untyped resolver cannot ask them: with
// no [types.Info] every declared type is unspellable and every Form D site is
// refused for that reason alone, which would make an accepted case impossible
// to write. A file with no imports type-checks with no importer, which is what
// keeps this a unit test.
func typedFixture(t *testing.T, src string) (*guardResolver, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	var conf types.Config
	pkg, err := conf.Check("example.com/p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("type-checking the fixture: %v", err)
	}
	tokFile := fset.File(file.Package)
	if tokFile == nil {
		t.Fatal("the parsed fixture has no position information")
	}
	return newGuardResolver(file, info, pkg, tokFile), file
}

// lastDeclaringStmt returns the last `:=` or `var` statement in a file.
//
// Every fixture below is written so that the statement under test is the last
// one that declares anything, which is what lets a shadowing case carry the
// declaration it shadows in the same function.
func lastDeclaringStmt(t *testing.T, file *ast.File) ast.Stmt {
	t.Helper()
	var found ast.Stmt
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				found = n
			}
		case *ast.DeclStmt:
			found = n
		}
		return true
	})
	if found == nil {
		t.Fatal("the fixture holds no declaring statement")
	}
	return found
}

// TestStatementGuardRefusesADeclarationItCannotHoist covers the two Form D
// refusals that are properties of the user's own source rather than of its
// types, and the accepted cases each of them has to leave alone.
//
// Both are absences everywhere else in the suite — the fixture module proves
// them by producing no hint — so this is where the distinction between "no
// candidate because the site is refused" and "no candidate at all" is stated
// as two columns of one table.
func TestStatementGuardRefusesADeclarationItCannotHoist(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "a plain short declaration is a Form D site",
			src:  "package p\n\nfunc f(n int) int {\n\tx := n * 2\n\treturn x\n}\n",
			want: true,
		},
		{
			name: "a short declaration reading the name it shadows is refused",
			src: "package p\n\nfunc f(n int) int {\n\tx := n\n\t{\n\t\tx := x * 2\n\t\t" +
				"return x\n\t}\n}\n",
			want: false,
		},
		{
			name: "a var reading the name it shadows is refused",
			src:  "package p\n\nvar L = 1\n\nfunc f(n int) int {\n\t{\n\t\tvar L = L + n\n\t\treturn L\n\t}\n}\n",
			want: false,
		},
		{
			name: "one spec of a var block reading another's name is refused",
			src: "package p\n\nvar L = 1\n\nfunc f(n int) int {\n\tvar (\n\t\ta = L + n\n\t\t" +
				"L = a + 1\n\t)\n\treturn a + L\n}\n",
			want: false,
		},
		{
			name: "a selected field of the same name is not a reference to it",
			src:  "package p\n\ntype point struct{ x int }\n\nfunc f(p point) int {\n\tx := p.x\n\treturn x\n}\n",
			want: true,
		},
		{
			name: "a struct literal key of the same name is not a reference to it",
			src: "package p\n\ntype point struct{ x int }\n\nfunc f(n int) point {\n\t" +
				"x := point{x: n}\n\treturn x\n}\n",
			want: true,
		},
		{
			name: "a map literal key of the same name is a reference to it",
			src: "package p\n\nfunc f(n int) int {\n\tx := n\n\t{\n\t\tx := map[int]int{x: n}\n\t\t" +
				"return x[n]\n\t}\n}\n",
			want: false,
		},
		{
			name: "a declared type on one line is a Form D site",
			src:  "package p\n\nfunc f(n int) int {\n\tvar x int = n * 2\n\treturn x\n}\n",
			want: true,
		},
		{
			name: "a declared type spelled across lines is refused",
			src: "package p\n\nfunc f(n int, mk func(int) func(int) int) int {\n\tvar g func(\n\t\tv int,\n\t) " +
				"int = mk(n)\n\treturn g(n)\n}\n",
			want: false,
		},
		{
			name: "a spec with no initialiser on one line is a Form D site",
			src: "package p\n\nfunc f(n int) int {\n\tvar (\n\t\ts int\n\t\tstart = n\n\t)\n\ts = start\n\t" +
				"return s\n}\n",
			want: true,
		},
		{
			name: "a spec with no initialiser spelled across lines is refused",
			src: "package p\n\nfunc f(n int) int {\n\tvar (\n\t\ts struct {\n\t\t\thi int\n\t\t}\n\t\t" +
				"start = n\n\t)\n\ts.hi = start\n\treturn s.hi\n}\n",
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, file := typedFixture(t, c.src)
			guard, ok := g.statementGuard(lastDeclaringStmt(t, file))
			if ok != c.want {
				t.Fatalf("statementGuard accepted = %v, want %v", ok, c.want)
			}
			if ok && guard.Form != GuardFormD {
				t.Errorf("guard form = %q, want %q", guard.Form, GuardFormD)
			}
		})
	}
}
