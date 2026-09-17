// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"bufio"
	"context"
	"fmt"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const integrationTag = "integration"

const AllowlistPath = "internal/testkit/testdata/unit-toolchain-allowlist.txt"

var toolchainNeedles = []string{
	"gomutants." + "Open(",
	"exec." + `LookPath("go")`,
	"exec." + `LookPath("git")`,
	"gocmd." + "Locate(",
	"GoBinary" + "(",
	"GitBinary" + "(",
	"Toolchain" + "(",
	"Git" + "(",
	"GitInit" + "(",
	"GitCommit" + "(",
}

var fakeableNeedles = []string{
	"gomutants." + "Open(",
	"gocmd." + "Locate(",
}

var fakeToolchainNeedle = "mutantkit." + "FakeGo("

var heavyweightRootTests = []string{
	"TestPublicSessionReusesOnePreparedSnapshot",
	"TestCatalogInvariants",
	"TestWorkspaceReportsTheToolchainVersionResolvedByOpen",
	"TestExternalModuleCompilesAgainstTheEngineAPI",
	"TestVerificationFailureIsTyped",
	"TestInstrumentationEnvironmentSupportsOverlayPathWithWhitespace",
}

const listTimeout = 5 * time.Minute

func TestRootPackageUnitTierNeedsNoToolchain(t *testing.T) {
	t.Parallel()

	unit := listRootTests(t, "the unit tier")
	integration := listRootTests(t, "the integration tier", "-tags", integrationTag)

	var leaked, missing []string
	for _, name := range heavyweightRootTests {
		if slices.Contains(unit, name) {
			leaked = append(leaked, name)
		}
		if !slices.Contains(integration, name) {
			missing = append(missing, name)
		}
	}
	if len(leaked) != 0 {
		t.Errorf("the root package's unit tier still contains %d toolchain-driving test(s):\n\t%s\n"+
			"each belongs in a file carrying `//go:build %s`; `go test .` must need no `go` on PATH",
			len(leaked), strings.Join(leaked, "\n\t"), integrationTag)
	}
	if len(missing) != 0 {
		t.Errorf("%d name(s) in heavyweightRootTests are in neither tier:\n\t%s\n"+
			"a renamed or deleted test leaves this gate covering one file fewer, "+
			"so update the list in tiers_test.go to a test that is still there",
			len(missing), strings.Join(missing, "\n\t"))
	}
}

func listRootTests(t *testing.T, tier string, tags ...string) []string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), listTimeout)
	defer cancel()
	argv := append([]string{GoBinary(t), "test", "-count=1", "-run", "^$"}, tags...)
	argv = append(argv, "-list", ".*", ".")
	result := ExecContext(ctx, t, Root(t), nil, argv...)
	RequireExit(t, result, 0, "listing the root package's tests in "+tier)

	listed := listedTests(string(result.Stdout))
	if len(listed) == 0 {
		t.Fatalf("`go test -list` named no tests in %s, so this proves nothing:\n%s", tier, result.Output)
	}
	return listed
}

func listedTests(out string) []string {
	var names []string
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.ContainsAny(line, " \t") {
			continue
		}
		names = append(names, line)
	}
	return names
}

func TestEveryToolchainDrivingTestIsIntegrationTagged(t *testing.T) {
	t.Parallel()

	root := Root(t)
	driving, err := toolchainDrivingTests(root)
	if err != nil {
		t.Fatalf("scanning %s for toolchain-driving tests: %v", root, err)
	}
	allowed, err := readAllowlist(filepath.Join(root, filepath.FromSlash(AllowlistPath)))
	if err != nil {
		t.Fatalf("reading the ledger: %v", err)
	}

	var offenders []string
	for _, file := range driving {
		if file.tagged || slices.Contains(allowed, file.path) {
			continue
		}
		offenders = append(offenders, file.path+" calls "+file.needle+"…")
	}
	if len(offenders) != 0 {
		t.Errorf("%d test file(s) drive the Go toolchain in the unit tier:\n\t%s\n"+
			"add `//go:build %s` to each, or, if it belongs in the unit tier, add its path to %s",
			len(offenders), strings.Join(offenders, "\n\t"), integrationTag, AllowlistPath)
	}

	var stale []string
	for _, path := range allowed {
		index := slices.IndexFunc(driving, func(f drivingFile) bool { return f.path == path })
		switch {
		case index < 0:
			stale = append(stale, path+" no longer drives the toolchain, or no longer exists")
		case driving[index].tagged:
			stale = append(stale, path+" is now `//go:build "+integrationTag+"`-tagged")
		}
	}
	if len(stale) != 0 {
		t.Errorf("%s names %d file(s) that no longer need to be in it:\n\t%s\n"+
			"delete the line(s); the ledger is meant to shrink",
			AllowlistPath, len(stale), strings.Join(stale, "\n\t"))
	}
}

func TestTheTagIsReadAsABuildExpression(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		constraint string
		tagged     bool
	}{
		{constraint: "integration", tagged: true},
		{constraint: "integration && windows", tagged: true},
		{constraint: "windows && integration", tagged: true},
		{constraint: "integration && !race", tagged: true},
		{constraint: "integration || !windows", tagged: false},
		{constraint: "!windows && integration || linux", tagged: false},
		{constraint: "linux", tagged: false},
		{constraint: "!windows", tagged: false},
		{constraint: "ignore", tagged: false},
	} {
		t.Run(test.constraint, func(t *testing.T) {
			t.Parallel()

			source := "//go:build " + test.constraint + "\n\npackage p\n"
			if got := hasIntegrationTag(source); got != test.tagged {
				t.Errorf("hasIntegrationTag(%q) = %v, want %v", test.constraint, got, test.tagged)
			}
		})
	}
}

func TestAnUnsatisfiableTagIsNotTheIntegrationTier(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/tiers")
	m.Source("wide/wide_test.go",
		"//go:build integration || !windows\n\npackage wide\n\nfunc use() { GoBinary(nil) }\n")
	m.Source("narrow/narrow_test.go",
		"//go:build integration && windows\n\npackage narrow\n\nfunc use() { GoBinary(nil) }\n")

	found, err := toolchainDrivingTests(m.Root())
	if err != nil {
		t.Fatalf("scanning the synthesized module: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("the scan found %d files, want both: %+v", len(found), found)
	}
	for _, file := range found {
		wantTagged := file.path == "narrow/narrow_test.go"
		if file.tagged != wantTagged {
			t.Errorf("%s tagged = %v, want %v", file.path, file.tagged, wantTagged)
		}
	}
}

func TestAFileThatScriptsTheToolchainIsNotDrivingOne(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/tiers")
	m.Source("scripted/scripted_test.go",
		"package scripted\n\nfunc use() {\n\tf := "+fakeToolchainNeedle+"nil)\n"+
			"\tgocmd."+"Locate(f.Bin())\n}\n")
	m.Source("real/real_test.go",
		"package real\n\nfunc use() { gocmd."+"Locate(nil) }\n")
	m.Source("both/both_test.go",
		"package both\n\nfunc use() {\n\tf := "+fakeToolchainNeedle+"nil)\n"+
			"\tgocmd."+"Locate(f.Bin())\n"+
			"\tpath, _ := "+"exec."+`LookPath("go")`+"\n\t_ = path\n}\n")

	found, err := toolchainDrivingTests(m.Root())
	if err != nil {
		t.Fatalf("scanning the synthesized module: %v", err)
	}
	reported := map[string]string{}
	for _, file := range found {
		reported[file.path] = file.needle
	}
	want := map[string]string{
		"real/real_test.go": "gocmd." + "Locate(",
		"both/both_test.go": "exec." + `LookPath("go")`,
	}
	if !maps.Equal(reported, want) {
		t.Errorf("the scan reported %v, want %v: a file that scripts the toolchain does not drive "+
			"it, one that reaches for the machine's does, and a file that does both is reported "+
			"for the half a fake cannot stand in for", reported, want)
	}
}

func drivingNeedle(text string) (string, bool) {
	var fakeable string
	for _, needle := range toolchainNeedles {
		if !containsCall(text, needle) {
			continue
		}
		if !slices.Contains(fakeableNeedles, needle) {
			return needle, true
		}
		if fakeable == "" {
			fakeable = needle
		}
	}
	if fakeable == "" {
		return "", false
	}
	return fakeable, !containsCall(text, fakeToolchainNeedle)
}

type drivingFile struct {
	path   string
	needle string
	tagged bool
}

func toolchainDrivingTests(root string) ([]drivingFile, error) {
	var found []drivingFile
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && slices.Contains(skippedDirectories, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		code, blankErr := withoutComments(path, string(source))
		if blankErr != nil {
			return blankErr
		}
		needle, drives := drivingNeedle(code)
		if !drives {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		found = append(found, drivingFile{
			path:   filepath.ToSlash(rel),
			needle: needle,
			tagged: hasIntegrationTag(string(source)),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(found, func(a, b drivingFile) int { return strings.Compare(a.path, b.path) })
	return found, nil
}

func withoutComments(path, text string) (string, error) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, text, parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("parsing %s to blank its comments: %w", path, err)
	}

	blanked := []byte(text)
	base := fileSet.File(parsed.Pos()).Base()
	for _, group := range parsed.Comments {
		for _, comment := range group.List {
			start := int(comment.Pos()) - base
			end := int(comment.End()) - base
			for i := start; i < end && i < len(blanked); i++ {
				if blanked[i] != '\n' {
					blanked[i] = ' '
				}
			}
		}
	}
	return string(blanked), nil
}

func containsCall(text, needle string) bool {
	for offset := 0; ; {
		index := strings.Index(text[offset:], needle)
		if index < 0 {
			return false
		}
		at := offset + index
		if at == 0 || !isIdentifierByte(text[at-1]) {
			return true
		}
		offset = at + len(needle)
	}
}

func isIdentifierByte(b byte) bool {
	return b == '_' ||
		('a' <= b && b <= 'z') ||
		('A' <= b && b <= 'Z') ||
		('0' <= b && b <= '9')
}

func hasIntegrationTag(source string) bool {
	for line := range strings.Lines(source) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			return false
		}
		if !constraint.IsGoBuild(trimmed) {
			continue
		}
		expr, err := constraint.Parse(trimmed)
		if err != nil {
			return false
		}
		return requiresIntegration(expr)
	}
	return false
}

func requiresIntegration(expr constraint.Expr) bool {
	others := otherTags(expr, nil)
	set := make(map[string]bool, len(others)+1)
	for assignment := range 1 << len(others) {
		clear(set)
		for index, tag := range others {
			set[tag] = assignment&(1<<index) != 0
		}
		if expr.Eval(func(tag string) bool { return set[tag] }) {
			return false
		}
	}
	return true
}

func otherTags(expr constraint.Expr, found []string) []string {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		if e.Tag != integrationTag && !slices.Contains(found, e.Tag) {
			found = append(found, e.Tag)
		}
	case *constraint.NotExpr:
		found = otherTags(e.X, found)
	case *constraint.AndExpr:
		found = otherTags(e.Y, otherTags(e.X, found))
	case *constraint.OrExpr:
		found = otherTags(e.Y, otherTags(e.X, found))
	}
	return found
}

func readAllowlist(path string) ([]string, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var allowed []string
	for line := range strings.Lines(string(source)) {
		entry := strings.TrimSpace(line)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		allowed = append(allowed, entry)
	}
	return allowed, nil
}

func TestTheScanReadsCodeAndNotTheProseAboutIt(t *testing.T) {
	t.Parallel()

	const source = `package example

// This paragraph explains the rule by naming testkit.GoBinary( in prose, which
// is what a file describing its own tiering has to do.
func drives() {
	_ = testkit.GoBinary(nil)
	_ = mutantkit.AnyGoBinary(nil)
}
`

	code, err := withoutComments("example_test.go", source)
	if err != nil {
		t.Fatalf("blanking comments: %v", err)
	}

	if strings.Count(code, "testkit.GoBinary(") != 1 {
		t.Errorf("the code holds %d call(s) to the needle after blanking, want the one in code:\n%s",
			strings.Count(code, "testkit.GoBinary("), code)
	}
	if len(code) != len(source) {
		t.Errorf("blanking moved bytes: %d before, %d after; containsCall reads the byte before a "+
			"match, so a shifted offset changes what it decides", len(source), len(code))
	}
	if !containsCall(code, "GoBinary(") {
		t.Error("the call in code stopped counting, which is the failure that would make the scan pass by seeing nothing")
	}
	if strings.Contains(code, "explains the rule") {
		t.Error("the prose survived blanking")
	}
}
