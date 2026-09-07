// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"bufio"
	"context"
	"go/build/constraint"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The two tiers, as the build tag that separates them.
//
// `go test ./...` is the unit tier: everything that needs nothing but a
// compiler, which is what a developer runs on every save and what CI runs on
// three operating systems. `go test -tags integration ./...` adds the suites
// that drive a real toolchain — a `go build`, a `go test -c`, a mutation run —
// and costs tens of minutes.
const integrationTag = "integration"

// AllowlistPath is the ledger of test files that drive the toolchain in the
// unit tier on purpose, relative to the module root.
const AllowlistPath = "internal/testkit/testdata/unit-toolchain-allowlist.txt"

// toolchainNeedles are the calls that mean "this file starts a `go` command".
//
// They are the spellings this repository actually uses rather than a general
// analysis: gomutants.Open probes a toolchain and snapshots a module,
// gocmd.Locate runs `go version` and `go env`, an os/exec lookup of the go
// binary is the hand-rolled form that predates the harness, and the three
// harness helpers are how everything migrated onto it asks for a toolchain or a
// git. A file containing any of them cannot run on a machine without Go, and —
// far more to the point — it costs a process tree rather than a function call.
//
// The last three are matched *unqualified*, which is the difference between a
// rule and a rule with a hole in it. Written as `testkit.GoBinary(` they would
// never match a call inside package testkit, so the one package whose whole
// subject is which tools a test may reach would be exempt from the tier policy
// by construction — and it is not: internal/testkit runs real `go` and `git`
// children, and it is in the ledger below like everything else that does.
//
// Each is written as two pieces joined at compile time, so that this file is
// not an offender *because it defines the rule*. It is in the ledger, but for
// the `go test -list` below rather than for a string constant — which is what
// keeps that ledger entry able to go stale when the listing goes away.
var toolchainNeedles = []string{
	"gomutants." + "Open(",
	"exec." + `LookPath("go")`,
	"gocmd." + "Locate(",
	"GoBinary" + "(",
	"GitBinary" + "(",
	"Toolchain" + "(",
}

// fakeableNeedles are the calls that can be handed a toolchain instead of
// reaching for one, and are therefore the only ones the fake exemption covers.
//
// The split is the whole content of that exemption. [gocmd.Locate] takes
// Options.Explicit and gomutants.Open takes OpenOptions.GoBinary, so a test
// that builds a scripted `go` and passes its path is not driving the machine's
// toolchain however the call is spelled — that is exactly how every consumer
// test in this repository uses the fake. The other four take nothing: an
// os/exec lookup of `go` reads the machine's PATH, and testkit.GoBinary,
// testkit.GitBinary and mutantkit.Toolchain resolve the real tool and apply the
// skip-or-fail policy. A file that calls one of those is reaching for the
// machine's toolchain, and building a fake somewhere else in the same file does
// not change that.
//
// Without the split the exemption was a hole rather than a rule: one
// `mutantkit.FakeGo(` anywhere in a file excused every other toolchain call in
// it, so a file that scripted one `go` and ran a real one would leave the tier
// silently.
var fakeableNeedles = []string{
	"gomutants." + "Open(",
	"gocmd." + "Locate(",
}

// fakeToolchainNeedle is the call that means "this file supplies the toolchain
// rather than reaching for one".
//
// internal/testkit/mutantkit's scripted `go` is an executable that re-executes
// the test binary and answers from a table, so a file that builds one and then
// hands its path to gocmd.Locate is not driving a toolchain: it is driving a
// process it wrote itself, on a machine that need not have Go installed at all.
// Several of the needles above still match such a file — a fake-driven test
// calls gocmd.Locate like any other — and without this the ledger would grow an
// entry for each test that made it shorter.
//
// The exemption is for the *file* rather than for the call, because the two
// halves are always written together and a rule that tried to pair them up
// would be a parser rather than a scan. It is narrow in two ways in exchange.
// Constructing a fake is the only thing that grants it, so a file that wants
// both a scripted toolchain and a real one has to be two files — which is what
// internal/gocmd now is, and why it is no longer in the ledger. And it covers
// only the calls a fake can be handed, [fakeableNeedles]: a file that builds a
// fake and also calls exec.LookPath("go") or testkit.GoBinary is reported like
// any other, because those reach for the machine's toolchain and no fake is in
// their way.
//
// Like every needle above it is matched as text and not as a call the compiler
// resolved, so a file that names the helper in a comment is exempted too. That
// is the same trade the rest of this scan makes — a parse would need the module
// loaded, with a toolchain, in the tier this test belongs to — and it costs a
// conversation at review rather than a silent hole, because a file has to
// mention the fake on purpose to get there.
//
// It is written as two pieces joined at compile time for the same reason the
// needles above are: this file would otherwise exempt *itself* by defining the
// rule, and its own ledger entry — which it earns by running two `go test
// -list` commands — would go stale without anybody meaning it to.
var fakeToolchainNeedle = "mutantkit." + "FakeGo("

// heavyweightRootTests is one toolchain-driving test out of each root file the
// tiering moved.
//
// One per file rather than all of them, and named rather than counted, because
// the question this asks is whether the *tag* took effect: a file that lost its
// build constraint puts its whole suite back into the unit tier, so one name
// out of each file is as much evidence as forty and says which file went wrong.
//
// Every name is required to be absent from the unit tier *and present in the
// integration one*, which is what stops the list rotting into a tautology. A
// renamed or deleted test would otherwise satisfy the only assertion that
// mattered — it is certainly not in the unit tier — and the gate would go on
// passing while covering one file fewer.
var heavyweightRootTests = []string{
	// api_integration_test.go: opens a workspace, prepares a session, compiles
	// four test binaries and runs mutants against them.
	"TestPublicSessionReusesOnePreparedSnapshot",
	// api_contract_test.go: reads the invariants off the shared prepared
	// sessions, so it is tagged with the file that prepares them.
	"TestCatalogInvariants",
	// workspace_integration_test.go: eleven tests, every one of them a
	// gomutants.Open over a copied fixture.
	"TestWorkspaceReportsTheToolchainVersionResolvedByOpen",
	// external_contract_integration_test.go: writes a consumer module and runs
	// a real `go test` inside it.
	"TestExternalModuleCompilesAgainstTheEngineAPI",
	// errors_integration_test.go: prepares a session over a fixture whose suite
	// is red on purpose.
	"TestVerificationFailureIsTyped",
	// session_integration_test.go: the one test split out of session_test.go,
	// and so the one most likely to be quietly merged back into it.
	"TestInstrumentationEnvironmentSupportsOverlayPathWithWhitespace",
}

// listTimeout bounds the child below.
//
// It is not [DefaultTimeout], because what the child does first is *build* the
// root package's test binary — the engine, the instrumenter, the reporter and
// everything they import — and on a cold cache on a Windows runner that is
// minutes rather than the seconds every other child this package starts takes.
// A minute here would be a flake that reads like the failure the test exists to
// report.
const listTimeout = 5 * time.Minute

// TestRootPackageUnitTierNeedsNoToolchain is the tiering as a fact about the
// binaries rather than about the source.
//
// A build tag is easy to write and just as easy to lose: a merge that drops the
// `//go:build` line leaves a file that still compiles, still passes, and quietly
// puts twenty-four toolchain-driving tests back into the tier that runs on
// every push on three operating systems. Nothing about that shows up as a
// failure — only as a suite that takes six minutes where it used to take five,
// which is how the root package got this way in the first place.
//
// So the assertion is on the lists of tests the two binaries actually contain.
// `go test -list` compiles the package for the tier it was asked about and
// prints the test functions in it, which is the one answer that cannot be given
// by a file that was not compiled.
//
// Both tiers are listed, and the second one is not a formality. Absence from
// the unit tier is satisfied just as well by a test that no longer exists, so a
// rename would silently retire whichever file that name stood for and leave the
// gate green over one file fewer. Requiring the same name in the integration
// tier makes a rename a failure, which is the only moment anybody would think
// to update the list. It costs the unit tier one compilation of the root
// package's integration binary — seconds, against the minutes this whole gate
// exists to keep out.
//
// The children inherit this process's environment rather than composing a
// hermetic one, and that is deliberate: the thing being compiled is go-mutants'
// own test binary, which belongs in the developer's own build cache like every
// other `go test` they run — the same rule `mise run test` states — and
// inheriting it means the children reuse what the run already built instead of
// recompiling the standard library into the harness's cache.
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

// listRootTests compiles the root package for one tier and returns the test
// functions in it.
//
// A `-list` that matched nothing is fatal rather than an empty answer. Every
// assertion above is about membership, so an empty list makes half of them
// vacuously true — and a gate that cannot fail is worse than no gate.
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

// listedTests reads the names `go test -list` printed.
//
// The output is one name per line followed by the ordinary `ok <package>
// <elapsed>` result line, so anything that is not a bare identifier is dropped
// rather than parsed.
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

// TestEveryToolchainDrivingTestIsIntegrationTagged is the rule the tiering
// rests on, applied to every test file in the repository rather than to the
// four that were in the wrong tier when it was written.
//
// The cost of getting this wrong is not a red build, which is why it needs a
// test: a toolchain-driving test in the unit tier passes everywhere a toolchain
// exists, and the whole loss — minutes per push, three times over, on the
// suite a developer runs most often — is invisible until somebody times it.
// This repository lost about half of `mise run test` that way.
//
// The ledger is the interesting half. Some packages drive the toolchain in the
// unit tier on purpose: internal/gocmd's subject *is* the `go` command, and
// internal/instrument has to compile what it generates to know that it is a
// program at all. Those are named in a file, one path per line, and the test
// reports a stale entry as loudly as it reports an offender — so the list can
// shrink when a suite is tagged and cannot grow without somebody writing the
// path down.
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

// TestTheTagIsReadAsABuildExpression is the difference between a constraint
// that puts a file in the integration tier and one that merely mentions the
// word.
//
// A scan that accepted any constraint containing `integration` as a term
// accepts `integration || !windows`, and that file is built by every unit-tier
// run on Linux and macOS — so the one shape that would slip a toolchain call
// past this gate is the one a term match cannot see. Exact string equality is
// not the answer either: `integration && windows` is a perfectly good
// Windows-only integration test and would be reported as an offender.
//
// The rule is the one the tier actually implies: the file must be excluded
// whenever `integration` is off, whatever the other tags are.
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
		// Built by every unit-tier run that is not on Windows.
		{constraint: "integration || !windows", tagged: false},
		{constraint: "!windows && integration || linux", tagged: false},
		// Nothing to do with the tier at all.
		{constraint: "linux", tagged: false},
		{constraint: "!windows", tagged: false},
		// A file the unit tier does not build, but not because of this tag.
		// Reporting it is the safe direction: it is a conversation, not a hole.
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

// TestAnUnsatisfiableTagIsNotTheIntegrationTier is the same rule pointed at a
// whole file, through the scan a reader would actually run.
//
// The unit test above is about one line; this is about the answer the ledger is
// computed from, and it is the assertion that would have caught the term match:
// a file whose constraint the unit tier satisfies is an offender however the
// word `integration` appears in it.
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

// TestAFileThatScriptsTheToolchainIsNotDrivingOne pins the one exemption the
// scan has, and pins that it is an exemption rather than a hole.
//
// A fake-driven test calls gocmd.Locate exactly like a real one — that is the
// point of the fake, since the code under test must not be able to tell — so
// the scan cannot separate them by the call. It separates them by the *other*
// call: a file that constructs a scripted `go` is supplying the toolchain, and
// a file that does not is reaching for the machine's.
//
// The second file here is what makes this a test rather than a restatement:
// the same gocmd.Locate call, no fake, and it has to be reported.
func TestAFileThatScriptsTheToolchainIsNotDrivingOne(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/tiers")
	m.Source("scripted/scripted_test.go",
		"package scripted\n\nfunc use() {\n\tf := "+fakeToolchainNeedle+"nil)\n"+
			"\tgocmd."+"Locate(f.Bin())\n}\n")
	m.Source("real/real_test.go",
		"package real\n\nfunc use() { gocmd."+"Locate(nil) }\n")
	// The hole the exemption used to have: one fake anywhere in the file
	// excused every other toolchain call in it, so a file that scripted one
	// `go` and reached for the machine's would leave the tier in silence.
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

// drivingNeedle reports whether a file drives the toolchain, and which call
// says so.
//
// The call it names is the one a reader has to look at, which is why an
// independent driver is preferred over a fakeable one when a file has both: a
// file that scripts a `go` for gocmd.Locate *and* calls testkit.GoBinary is
// reported for the second, since the first is not what puts it in the wrong
// tier.
func drivingNeedle(text string) (string, bool) {
	var fakeable string
	for _, needle := range toolchainNeedles {
		if !containsCall(text, needle) {
			continue
		}
		if !slices.Contains(fakeableNeedles, needle) {
			// Reaches for the machine's toolchain by itself. No fake in the
			// file changes that, so the answer is settled.
			return needle, true
		}
		if fakeable == "" {
			fakeable = needle
		}
	}
	if fakeable == "" {
		return "", false
	}
	// Only calls a scripted toolchain can be handed, so a file that builds one
	// is supplying the toolchain rather than reaching for it.
	return fakeable, !containsCall(text, fakeToolchainNeedle)
}

// drivingFile is one test file that starts a `go` command, and whether it is in
// the integration tier.
type drivingFile struct {
	// path is relative to the module root, spelled with forward slashes, which
	// is how the ledger spells it too.
	path string
	// needle is the first call found, so a report says what to look for rather
	// than only which file to open.
	needle string
	tagged bool
}

// toolchainDrivingTests finds every `_test.go` under root that starts a `go`
// command, sorted by path.
//
// A text scan rather than a parse, because what is being looked for is a call
// spelled a particular way rather than a construct: `go/types` would resolve
// the four helpers properly and would need the whole module loaded, with a
// toolchain, in the tier this test belongs to.
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
		text := string(source)
		needle, drives := drivingNeedle(text)
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
			tagged: hasIntegrationTag(text),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(found, func(a, b drivingFile) int { return strings.Compare(a.path, b.path) })
	return found, nil
}

// containsCall reports whether text calls needle, as opposed to merely ending a
// longer identifier with it.
//
// The unqualified needles are what make this necessary. `Toolchain(` occurs in
// `func TestEnvironmentLeavesPathAloneWithoutAToolchain(t *testing.T)` and
// `GoBinary(` would occur in any test named after the helper — both of which
// are the opposite of a file that runs one, since a test *about* a lookup is
// usually the test that avoids doing it. So the byte before the match has to be
// something that cannot continue an identifier, which admits `mutantkit.`,
// `= `, `\t` and a line start, and rejects `...AToolchain(`.
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

// isIdentifierByte reports whether a byte can be part of a Go identifier.
//
// ASCII only, which is exact for every spelling in [toolchainNeedles] and for
// the identifiers that could run into one: a needle can only be preceded by a
// non-ASCII rune inside a comment or a string, where a false match costs a
// ledger line rather than a missed offender.
func isIdentifierByte(b byte) bool {
	return b == '_' ||
		('a' <= b && b <= 'z') ||
		('A' <= b && b <= 'Z') ||
		('0' <= b && b <= '9')
}

// hasIntegrationTag reports whether a file's build constraint keeps it out of
// the unit tier.
//
// Only the lines above the package clause are read, because that is the only
// place a `//go:build` line is a build constraint: the same text further down is
// a comment, and a rule that accepted one would be satisfied by a file that
// mentioned the tag in a sentence about it.
//
// The constraint is parsed and evaluated rather than matched as text, and the
// two obvious shortcuts are both wrong in a way that matters. Accepting any
// expression that mentions `integration` as a term accepts
// `integration || !windows`, which every unit-tier run outside Windows builds —
// so the one constraint that could hide a toolchain call from this gate is
// exactly the one a term match cannot see. Demanding the literal string
// `integration` rejects `integration && windows`, which is a perfectly good
// Windows-only integration test.
//
// So the question asked is the one the tier actually poses: is the file
// excluded whenever `integration` is off? [requiresIntegration] answers it by
// evaluating the expression over every assignment of the other tags in it, with
// `integration` pinned false.
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
			// A constraint the go command itself would refuse. It is not this
			// gate's business to report the syntax error — `go build` says it
			// far better — and calling an unparsable line "tagged" would be
			// the one answer that hides something.
			return false
		}
		return requiresIntegration(expr)
	}
	return false
}

// requiresIntegration reports whether every assignment of build tags that
// satisfies expr has [integrationTag] set.
//
// It is a truth table rather than anything cleverer, because the expressions
// are three terms at the outside: the other tags mentioned in the constraint are
// enumerated over both values with `integration` held false, and the file counts
// as tagged only if the expression is false in all of them. That is exact for
// the whole grammar `//go:build` has — tags, `!`, `&&`, `||` — rather than for
// the shapes somebody thought of.
//
// A constraint naming no other tag is one evaluation: `integration` is tagged,
// `linux` and `ignore` are not. `ignore` is worth stating, because it is the
// case where a true answer would be a lie of the useful kind — the file is
// certainly not in the unit tier, but not because of this tag, and reporting it
// starts a conversation instead of quietly exempting it.
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

// otherTags collects the distinct tags in expr apart from [integrationTag], in
// the order they appear.
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

// readAllowlist reads the ledger: one module-relative path per line, `#`
// comments and blank lines ignored.
//
// A missing file is an error rather than an empty list. The ledger existing is
// what makes an empty one mean "nothing is exempt"; a gate that treated a
// deleted ledger as "nothing is exempt" and a deleted ledger as "everything
// passed" would give the same answer to both, and only one of them is true.
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
