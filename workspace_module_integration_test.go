// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// What the frozen module turns out to hold: `Workspace.Module` against a real
// toolchain.
//
// The call answers the question every consumer used to answer for itself by
// running `go list -json` through `Workspace.Exec` and parsing the stream — the
// recipe docs/library.md carried, with each consumer's own conventions for
// patterns, tags, output limits and where the module's Go version comes from.
// The workspace already froze the tree, already probed the toolchain and
// already records every child it starts, so these tests are about the answer
// being the *same* one under all of that: before a preparation, beside one, and
// after one that succeeded.

package gomutants_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/trace"
)

// untestedModule is the module path fixtures/untested declares.
const untestedModule = "fixture.example/untested"

// taggedModule is the module path fixtures/tagged declares.
const taggedModule = "fixture.example/tagged"

// fixtureGoDirective is the `go` line every fixture's go.mod carries. Module
// reads it with golang.org/x/mod/modfile rather than from `go list`, so this is
// the value that proves the parse and not merely that the field is filled in.
const fixtureGoDirective = "1.26"

// moduleAnswerBound is how long a Module call that must not be blocked is
// given. It is the bound workspace_prepare_overlap_integration_test.go uses for
// the same reason: turning a deadlock into a sentence, on a machine that is
// running a preparation's own compiles at the same time.
const moduleAnswerBound = 2 * time.Minute

// moduleInterestPoll is how often [awaitInterest] looks. It is a poll rather
// than a wait because what it is watching for is a map entry rather than an
// event, and it is short because what it is waiting for is a caller reaching
// its next few statements.
const moduleInterestPoll = time.Millisecond

// modulePrefix is how a failure of this call names itself. Every one of them
// carries it exactly once, whichever layer the failure came from.
const modulePrefix = "gomutants: module: "

// TestModuleListsTheFrozenSnapshot is the answer itself, against a fixture that
// holds both halves of every claim in one listing.
//
// fixtures/untested has two packages, one with a test file and one without, so
// `HasTests` is proved in both directions by the same call rather than by two
// fixtures that differ in other ways too. The module path and the Go version
// come from go.mod — read in this process, with no child — and the toolchain is
// the one `Open` located, which is what lets a consumer key evidence on the
// three together.
//
// The directories are the point of the whole call. Every `Dir` is inside the
// snapshot and none is inside the tree the user handed `Open`, because a
// consumer that listed the module and then read a file at the path it was given
// would be reading the repository go-mutants promised not to touch.
func TestModuleListsTheFrozenSnapshot(t *testing.T) {
	root := copyFixture(t, "untested")
	parent := t.TempDir()
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	module, err := workspace.Module(t.Context(), gomutants.ModuleQuery{})
	if err != nil {
		t.Fatalf("listing the frozen module: %v", err)
	}
	if module.Path != untestedModule {
		t.Errorf("Path = %q, want %q", module.Path, untestedModule)
	}
	if module.GoVersion != fixtureGoDirective {
		t.Errorf("GoVersion = %q, want the go directive %q", module.GoVersion, fixtureGoDirective)
	}
	if got, want := module.Toolchain, workspace.ToolchainVersion(); got != want {
		t.Errorf("Toolchain = %q, want the workspace's %q", got, want)
	}
	if module.TraceSeq == 0 {
		t.Error("TraceSeq is zero, so nothing joins this listing to the workspace's recording")
	}

	paths := importPathsOf(module)
	want := []string{untestedModule + "/lib", untestedModule + "/orphan"}
	if !slices.Equal(paths, want) {
		t.Fatalf("Packages = %v, want %v in that order", paths, want)
	}
	if !slices.IsSorted(paths) {
		t.Errorf("Packages are not sorted by ImportPath: %v", paths)
	}

	snapshotRoot := onlySnapshotRoot(t, parent)
	for _, pkg := range module.Packages {
		if !filepath.IsAbs(pkg.Dir) {
			t.Errorf("%s has Dir %q, which is not absolute", pkg.ImportPath, pkg.Dir)
		}
		if !underRoot(t, snapshotRoot, pkg.Dir) {
			t.Errorf("%s has Dir %q, which is not inside the snapshot %q", pkg.ImportPath, pkg.Dir, snapshotRoot)
		}
		if underRoot(t, root, pkg.Dir) {
			t.Errorf("%s has Dir %q, which is inside the tree Open promised not to touch (%q)",
				pkg.ImportPath, pkg.Dir, root)
		}
		hasFiles := len(pkg.TestGoFiles)+len(pkg.XTestGoFiles) != 0
		if pkg.HasTests != hasFiles {
			t.Errorf("%s HasTests = %v, want %v for TestGoFiles=%v XTestGoFiles=%v",
				pkg.ImportPath, pkg.HasTests, hasFiles, pkg.TestGoFiles, pkg.XTestGoFiles)
		}
		if len(pkg.GoFiles) == 0 {
			t.Errorf("%s names no Go files at all", pkg.ImportPath)
		}
	}

	lib := packageAt(t, module, untestedModule+"/lib")
	if lib.Name != "lib" {
		t.Errorf("lib Name = %q, want %q", lib.Name, "lib")
	}
	if !lib.HasTests {
		t.Error("lib has no tests, but fixtures/untested/lib holds lib_test.go")
	}
	if !slices.Equal(lib.GoFiles, []string{"lib.go"}) || !slices.Equal(lib.TestGoFiles, []string{"lib_test.go"}) {
		t.Errorf("lib GoFiles=%v TestGoFiles=%v, want [lib.go] and [lib_test.go]", lib.GoFiles, lib.TestGoFiles)
	}

	orphan := packageAt(t, module, untestedModule+"/orphan")
	if orphan.HasTests {
		t.Errorf("orphan has tests %v/%v, but the fixture's whole specimen is that it has none",
			orphan.TestGoFiles, orphan.XTestGoFiles)
	}
}

// TestModuleHonoursPatternsAndTags is the query half: the two fields of
// [gomutants.ModuleQuery] each change the answer, and change it the way `go
// list` would.
//
// The tags are checked against fixtures/tagged, whose whole subject is that a
// file set is the file set the environment asked for: `special.go` exists only
// under `-tags special`, so a listing that named it either way would be a
// listing of a module nobody compiles. The patterns are checked against
// fixtures/untested, because a narrower pattern can only return fewer packages
// in a module that has more than one.
func TestModuleHonoursPatternsAndTags(t *testing.T) {
	t.Run("tags", func(t *testing.T) {
		workspace, err := gomutants.Open(t.Context(), copyFixture(t, "tagged"), gomutants.OpenOptions{
			TempDirectory: t.TempDir(),
			Env:           hostEnvWithoutFixtureGates(),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = workspace.Close() })

		plain, err := workspace.Module(t.Context(), gomutants.ModuleQuery{})
		if err != nil {
			t.Fatalf("listing without the tag: %v", err)
		}
		untagged := packageAt(t, plain, taggedModule)
		if slices.Contains(untagged.GoFiles, "special.go") {
			t.Errorf("an untagged listing named special.go: GoFiles=%v", untagged.GoFiles)
		}
		if !slices.Contains(untagged.GoFiles, "plain.go") {
			t.Errorf("an untagged listing did not name plain.go: GoFiles=%v", untagged.GoFiles)
		}

		special, err := workspace.Module(t.Context(), gomutants.ModuleQuery{Tags: []string{"special"}})
		if err != nil {
			t.Fatalf("listing with the tag: %v", err)
		}
		tagged := packageAt(t, special, taggedModule)
		if !slices.Contains(tagged.GoFiles, "special.go") {
			t.Errorf("a `special` listing did not name special.go: GoFiles=%v", tagged.GoFiles)
		}
		if !slices.Contains(tagged.TestGoFiles, "special_test.go") {
			t.Errorf("a `special` listing did not name special_test.go: TestGoFiles=%v", tagged.TestGoFiles)
		}
	})

	t.Run("patterns", func(t *testing.T) {
		workspace, err := gomutants.Open(t.Context(), copyFixture(t, "untested"), gomutants.OpenOptions{
			TempDirectory: t.TempDir(),
			Env:           hostEnvWithoutFixtureGates(),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = workspace.Close() })

		whole, err := workspace.Module(t.Context(), gomutants.ModuleQuery{})
		if err != nil {
			t.Fatalf("listing the whole module: %v", err)
		}
		narrow, err := workspace.Module(t.Context(), gomutants.ModuleQuery{Packages: []string{"./lib/..."}})
		if err != nil {
			t.Fatalf("listing ./lib/...: %v", err)
		}
		if got := importPathsOf(narrow); !slices.Equal(got, []string{untestedModule + "/lib"}) {
			t.Errorf("./lib/... = %v, want only the lib package", got)
		}
		if len(narrow.Packages) >= len(whole.Packages) {
			t.Errorf("./lib/... named %d packages and ./... named %d; a narrower pattern must return fewer",
				len(narrow.Packages), len(whole.Packages))
		}
		// The module's own identity does not narrow with the query: it is a
		// fact about go.mod, and a consumer that listed one package must still
		// be told which module it is in.
		if narrow.Path != whole.Path || narrow.GoVersion != whole.GoVersion {
			t.Errorf("a narrowed listing changed the module identity: %q/%q, want %q/%q",
				narrow.Path, narrow.GoVersion, whole.Path, whole.GoVersion)
		}
	})
}

// TestModuleIsMemoised is what makes the call cheap enough to be asked twice.
//
// Nothing but a command of the caller's own can change the frozen tree, so the
// answer to one query stands until one has run, and a consumer that wants the
// package list in three places should not pay three `go list` passes for it.
// The account is the workspace's own recording rather than a counter in this
// test, which is the same evidence a consumer reading a trace would have: one
// `go-list` event with the subject `module`, whatever the caller asked.
//
// The second query differs only in its tags, and that is the half a memo can
// get wrong in the dangerous direction: a key that collapsed two tag sets would
// answer the second query with the first one's file set, which is exactly the
// mistake fixtures/tagged exists to catch.
func TestModuleIsMemoised(t *testing.T) {
	workspace, err := gomutants.Open(t.Context(), copyFixture(t, "tagged"), gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	query := gomutants.ModuleQuery{Packages: []string{"./..."}}
	first, err := workspace.Module(t.Context(), query)
	if err != nil {
		t.Fatalf("the first listing: %v", err)
	}
	second, err := workspace.Module(t.Context(), query)
	if err != nil {
		t.Fatalf("the second listing: %v", err)
	}
	if got := moduleListings(workspace.Recording()); got != 1 {
		t.Errorf("two identical queries produced %d `go-list` events, want one", got)
	}
	if first.TraceSeq != second.TraceSeq {
		t.Errorf("the memoised answer carries TraceSeq %d, want the first listing's %d",
			second.TraceSeq, first.TraceSeq)
	}
	if !slices.Equal(importPathsOf(first), importPathsOf(second)) {
		t.Errorf("two identical queries answered %v and %v", importPathsOf(first), importPathsOf(second))
	}

	// An empty Packages means `./...`, so this is the same query spelled the
	// other way and must not list again.
	if _, err = workspace.Module(t.Context(), gomutants.ModuleQuery{}); err != nil {
		t.Fatalf("the defaulted listing: %v", err)
	}
	if got := moduleListings(workspace.Recording()); got != 1 {
		t.Errorf("an empty Packages listed again: %d `go-list` events, want one", got)
	}

	if _, err = workspace.Module(t.Context(), gomutants.ModuleQuery{Tags: []string{"special"}}); err != nil {
		t.Fatalf("the tagged listing: %v", err)
	}
	if got := moduleListings(workspace.Recording()); got != 2 {
		t.Errorf("a different tag set produced %d `go-list` events in total, want two", got)
	}
	// And the tags are normalised rather than compared as written: the same set
	// in another order, or twice over, is the same question.
	if _, err = workspace.Module(t.Context(), gomutants.ModuleQuery{Tags: []string{"special", "special"}}); err != nil {
		t.Fatalf("the repeated-tag listing: %v", err)
	}
	if got := moduleListings(workspace.Recording()); got != 2 {
		t.Errorf("a repeated tag listed again: %d `go-list` events in total, want two", got)
	}

	// A failed query is not remembered, so the next caller gets the toolchain's
	// answer rather than a cached refusal.
	bad := gomutants.ModuleQuery{Packages: []string{"./nope"}}
	if _, err = workspace.Module(t.Context(), bad); err == nil {
		t.Fatal("listing a package that does not exist succeeded")
	}
	if _, err = workspace.Module(t.Context(), bad); err == nil {
		t.Fatal("the second listing of a package that does not exist succeeded")
	}
	if got := moduleListings(workspace.Recording()); got != 4 {
		t.Errorf("a failed query produced %d `go-list` events in total, want four: it was memoised", got)
	}
}

// TestACommandInvalidatesEveryListing is the memo's premise stated honestly.
//
// A listing is reused because the frozen tree does not change — and
// `Workspace.Exec` is the one call that can change it. Nothing refuses a
// command that writes into the snapshot: a `go generate`, a `-run` that
// rewrites a golden file, a fuzz target the go command files a crasher for.
// A memo that outlived one would answer with a file set that is no longer on
// disk, which is the one failure a listing must not produce, so every listing
// is dropped when a command completes.
//
// It is dropped whatever the command did, because "did this one write" is not a
// question the workspace can answer without re-freezing the tree — which costs
// more than the listing it would save.
func TestACommandInvalidatesEveryListing(t *testing.T) {
	workspace, err := gomutants.Open(t.Context(), copyFixture(t, "untested"), gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	if _, err = workspace.Module(t.Context(), gomutants.ModuleQuery{}); err != nil {
		t.Fatalf("the first listing: %v", err)
	}
	if _, err = workspace.Module(t.Context(), gomutants.ModuleQuery{}); err != nil {
		t.Fatalf("the second listing: %v", err)
	}
	if got := moduleListings(workspace.Recording()); got != 1 {
		t.Fatalf("two listings with no command between them produced %d `go-list` events, want one", got)
	}

	result, err := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "env", "GOVERSION"},
		Env:  []string{"GOWORK=off"},
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("running a command beside the listing: %v exit %d\n%s", err, result.ExitCode, result.Output)
	}

	if _, err = workspace.Module(t.Context(), gomutants.ModuleQuery{}); err != nil {
		t.Fatalf("the listing after a command: %v", err)
	}
	if got := moduleListings(workspace.Recording()); got != 2 {
		t.Errorf("the listing after a command produced %d `go-list` events in total, want two:"+
			" a command that may have written the tree left a memo standing", got)
	}
}

// TestModuleIgnoresToolchainNoiseOnStderr is the stream split, seen from the
// call that needed it.
//
// The go command writes its document to stdout and everything else to stderr —
// `go: warning: "./docs/..." matched no packages` for a pattern that matched
// none, `go: downloading …` for a cold module cache, a toolchain switch — and
// every one of those is a command that exits *zero* and did what it was asked.
// A decoder handed the combined capture fails on the first byte of the warning,
// so the advertised "call it before Prepare, on a machine that has not built
// this module yet" case would have been a listing that never worked.
//
// A pattern that matches nothing is the cheap, deterministic form of the same
// noise, and the answer is an empty listing rather than an error: `go list`
// exited zero, the module holds no package under `./docs/...`, and that is a
// fact rather than a failure.
func TestModuleIgnoresToolchainNoiseOnStderr(t *testing.T) {
	root := copyFixture(t, "untested")
	// A directory with no Go file in it, added to the *copy* rather than to
	// fixtures/: what is under test is the go command's warning, and a fixture
	// that carried an empty directory would be a fixture git cannot hold.
	docs := filepath.Join(root, "docs")
	if err := os.Mkdir(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "notes.md"), []byte("# notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	module, err := workspace.Module(t.Context(), gomutants.ModuleQuery{Packages: []string{"./docs/..."}})
	if err != nil {
		t.Fatalf("listing a pattern the go command warns about: %v", err)
	}
	if len(module.Packages) != 0 {
		t.Errorf("./docs/... named %v, want no package at all", importPathsOf(module))
	}
	if module.Path != untestedModule {
		t.Errorf("Path = %q, want %q: an empty package set is still this module", module.Path, untestedModule)
	}
}

// A gatedSink is a recording sink that stops the workspace inside one event.
//
// It is how these tests hold a listing open. `Workspace.Module` records its
// `go-list` after the child has been reaped and before it settles the answer,
// on the listing goroutine and synchronously, so a sink that blocks there
// freezes a workspace at exactly the moment a second caller can still join the
// listing the first one started. Everything is passed through to a memory sink
// underneath, so the recording is still readable afterwards.
type gatedSink struct {
	events *trace.MemorySink
	kind   string

	mu     sync.Mutex
	caught bool
	held   chan struct{}
	pass   chan struct{}
}

func newGatedSink(kind string) *gatedSink {
	return &gatedSink{
		events: trace.NewMemorySink(0),
		kind:   kind,
		held:   make(chan struct{}),
		pass:   make(chan struct{}),
	}
}

func (g *gatedSink) Emit(event trace.Event) error {
	if event.Exec != nil && event.Exec.Kind == g.kind && g.claim() {
		close(g.held)
		<-g.pass
	}
	return g.events.Emit(event)
}

// claim reports whether this is the first event of the gated kind, which is the
// only one that is held: a second listing must not deadlock a test that has
// already let the first one through.
func (g *gatedSink) claim() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.caught {
		return false
	}
	g.caught = true
	return true
}

func (g *gatedSink) Close() error   { return g.events.Close() }
func (g *gatedSink) Dropped() int64 { return g.events.Dropped() }

// awaitHeld blocks until the workspace has stopped inside the gated event.
func (g *gatedSink) awaitHeld(t *testing.T) {
	t.Helper()
	select {
	case <-g.held:
	case <-time.After(moduleAnswerBound):
		t.Fatalf("no %s event was recorded within %s, so nothing is being held", g.kind, moduleAnswerBound)
	}
}

// release lets the held event, and the listing behind it, go on.
func (g *gatedSink) release() { close(g.pass) }

// awaitInterest blocks until the workspace has exactly the given number of
// callers waiting on listings it has not settled — two of them arriving, or the
// last of them leaving.
//
// It polls rather than sleeps, and it is the whole reason [Workspace] exports
// ModuleInterest to its own tests: the moment a second caller joins a listing
// is not observable from outside the package, and a fixed pause in its place
// would make these tests pass on a fast machine and report a bug on a loaded
// one.
func awaitInterest(t *testing.T, workspace *gomutants.Workspace, want int) {
	t.Helper()
	deadline := time.Now().Add(moduleAnswerBound)
	for time.Now().Before(deadline) {
		if workspace.ModuleInterest() == want {
			return
		}
		time.Sleep(moduleInterestPoll)
	}
	t.Fatalf("%d caller(s) are interested in a listing after %s, want %d",
		workspace.ModuleInterest(), moduleAnswerBound, want)
}

// TestModuleSharesOneListingBetweenConcurrentCallers is the memo under the
// concurrency the workspace promises, and the three things it has to get right.
//
// Two callers asking the same question run one `go list` between them, which is
// the claim a per-key memo makes about goroutines as much as about statements.
//
// The listing belongs to nobody. It runs under a context detached from every
// caller's, cancelled only when the *last* interested caller has left — so the
// caller that started it may go away and the one still waiting gets the whole
// answer rather than the first caller's `context canceled`. That was the defect
// this test was written for: a leader that ran the listing under its own
// context handed its cancellation to a waiter that had asked with a live one.
//
// And a listing nobody wants is stopped. When every caller leaves, the child is
// cut off by the listing's own context and the entry is forgotten, so the
// workspace holds no memo of a listing that never finished and the next caller
// asks the toolchain again.
//
// Both halves are set up through the recording rather than through a sleep. The
// workspace is stopped inside the `go-list` event it records — after the child,
// before the answer is settled — which is precisely the window a second caller
// can join, and the test waits for that caller to have joined before it cancels
// anything.
func TestModuleSharesOneListingBetweenConcurrentCallers(t *testing.T) {
	t.Run("the leader leaves and the waiter is answered", func(t *testing.T) {
		gate := newGatedSink(trace.ExecKindGoList)
		workspace, err := gomutants.Open(t.Context(), copyFixture(t, "untested"), gomutants.OpenOptions{
			TempDirectory: t.TempDir(),
			Trace:         gate,
			Env:           hostEnvWithoutFixtureGates(),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = workspace.Close() })

		leaderCtx, cancelLeader := context.WithCancel(t.Context())
		defer cancelLeader()
		query := gomutants.ModuleQuery{}

		leaderDone := make(chan error, 1)
		go func() {
			_, listErr := workspace.Module(leaderCtx, query)
			leaderDone <- listErr
		}()
		gate.awaitHeld(t)

		type answer struct {
			module gomutants.Module
			err    error
		}
		waiterDone := make(chan answer, 1)
		go func() {
			module, listErr := workspace.Module(t.Context(), query)
			waiterDone <- answer{module: module, err: listErr}
		}()
		// Both callers are now on the one listing, which is the state the claim
		// is about. Only then does the caller that started it go away.
		awaitInterest(t, workspace, 2)
		cancelLeader()
		gate.release()

		select {
		case got := <-waiterDone:
			if got.err != nil {
				t.Fatalf("the waiter was handed the leader's cancellation: %v", got.err)
			}
			if paths := importPathsOf(got.module); len(paths) == 0 {
				t.Errorf("the waiter was answered with no packages: %+v", got.module)
			}
		case <-time.After(moduleAnswerBound):
			t.Fatalf("the waiter did not answer within %s", moduleAnswerBound)
		}
		<-leaderDone

		if got := moduleListings(gate.events.Events()); got != 1 {
			t.Errorf("two concurrent identical queries produced %d `go-list` events, want one", got)
		}
		// And the answer stands for whoever asks next, without another listing:
		// a caller going away does not un-memoise what it paid for.
		if _, err = workspace.Module(t.Context(), query); err != nil {
			t.Fatalf("the listing after the shared one: %v", err)
		}
		if got := moduleListings(gate.events.Events()); got != 1 {
			t.Errorf("a shared listing was not memoised: %d `go-list` events, want one", got)
		}
	})

	t.Run("both leave and the listing is forgotten", func(t *testing.T) {
		gate := newGatedSink(trace.ExecKindGoList)
		workspace, err := gomutants.Open(t.Context(), copyFixture(t, "untested"), gomutants.OpenOptions{
			TempDirectory: t.TempDir(),
			Trace:         gate,
			Env:           hostEnvWithoutFixtureGates(),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = workspace.Close() })

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		query := gomutants.ModuleQuery{}
		errs := make(chan error, 2)
		go func() {
			_, listErr := workspace.Module(ctx, query)
			errs <- listErr
		}()
		gate.awaitHeld(t)
		go func() {
			_, listErr := workspace.Module(ctx, query)
			errs <- listErr
		}()
		awaitInterest(t, workspace, 2)

		cancel()
		// Both callers have to have *left* before the listing is let go: the
		// leaving and the settling race, and this test is about the branch
		// where the leaving wins. Releasing the gate first would let the
		// listing settle a perfectly good answer into an entry nobody had
		// dropped yet, which is a different — and equally correct — outcome,
		// and asserting one while allowing the other is how this test failed on
		// three CI runners.
		awaitInterest(t, workspace, 0)
		gate.release()
		for range 2 {
			select {
			case listErr := <-errs:
				if listErr == nil {
					t.Error("a cancelled caller was answered, want a context cancellation")
					continue
				}
				if !errors.Is(listErr, context.Canceled) {
					t.Errorf("a cancelled caller got %v, want a context cancellation", listErr)
				}
				// The call names itself once. Both callers reach this through a
				// different path — one was inside the listing and one was
				// waiting on it — and a wrap applied at both would read
				// `gomutants: module: gomutants: module: context canceled`.
				if got := strings.Count(listErr.Error(), modulePrefix); got != 1 {
					t.Errorf("%q names the call %d times, want once", listErr.Error(), got)
				}
			case <-time.After(moduleAnswerBound):
				t.Fatalf("a cancelled caller did not return within %s", moduleAnswerBound)
			}
		}
		if got := workspace.ModuleInterest(); got != 0 {
			t.Errorf("%d caller(s) still interested after both left, want none", got)
		}

		// Nothing was remembered: the next caller lists again rather than
		// waiting on an entry no goroutine will ever complete.
		before := moduleListings(gate.events.Events())
		if _, err = workspace.Module(t.Context(), query); err != nil {
			t.Fatalf("the listing after both callers left: %v", err)
		}
		if got := moduleListings(gate.events.Events()); got <= before {
			t.Errorf("the listing after both callers left produced no new `go-list` event"+
				" (%d, was %d): a listing nobody completed was memoised", got, before)
		}
	})
}

// TestModuleFailuresAreAlwaysTypedAsTheModuleCall is the promise docs/library.md
// and the CHANGELOG make about *every* way a listing can fail, rather than about
// the one that goes through `go list`'s exit status.
//
// A child that could not be started, a context cancelled before it was, a go.mod
// that could not be read: all of them used to arrive as whatever the layer
// underneath said, which for a command is a sentence beginning
// `gomutants: exec:` — a call the consumer never made. A consumer that catches
// `*ExecutionError` to report infrastructure separately from a measurement has
// to be able to catch this one too.
func TestModuleFailuresAreAlwaysTypedAsTheModuleCall(t *testing.T) {
	workspace, err := gomutants.Open(t.Context(), copyFixture(t, "untested"), gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = workspace.Module(cancelled, gomutants.ModuleQuery{})
	if err == nil {
		t.Fatal("listing with a cancelled context succeeded")
	}
	var execution *gomutants.ExecutionError
	if !errors.As(err, &execution) {
		t.Fatalf("a cancelled listing failed with %T (%v), want an *gomutants.ExecutionError", err, err)
	}
	if execution.Call != "module" {
		t.Errorf("ExecutionError.Call = %q, want %q", execution.Call, "module")
	}
	if !strings.HasPrefix(err.Error(), modulePrefix) {
		t.Errorf("the message is %q, want one that begins with the call that failed", err.Error())
	}
	if got := strings.Count(err.Error(), modulePrefix); got != 1 {
		t.Errorf("%q names the call %d times, want once", err.Error(), got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the cancellation does not survive the wrap: %v", err)
	}
}

// TestModuleAfterPrepareStillAnswers is the lifecycle rule [Workspace.Exec]
// already follows, applied to the listing: it runs before a preparation, beside
// one, and after one that succeeded, and it is refused only once the workspace
// is closed.
//
// The "beside one" half is the one worth arranging. The preparation is held at
// the end of its discovery phase — which reads the frozen tree under the tree's
// *shared* half, exactly as this call does — and the listing is issued from a
// goroutine of the test's own rather than from the callback, because a command
// started on the preparation's goroutine is the one deadlock docs/library.md
// spells out in full.
//
// The *end* of discovery rather than its start, and the reason is the recording
// rather than the locks: newPrepareTrace hands each event to the caller's
// callback first and to the recorder second, so a preparation held in its very
// first callback has not recorded a phase event yet and there would be no
// interval to place the listing inside.
//
// What proves it overlapped is the recorder's order rather than a clock. The
// listing's `go-list` event sits between the preparation's first phase event
// and its last, and every one of those is written by the same recorder under
// the locks themselves, so the account is the lock's own and not two stopwatches
// in this test.
func TestModuleAfterPrepareStillAnswers(t *testing.T) {
	root := copyFixture(t, "untested")
	recorded := trace.NewMemorySink(0)
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Trace:         recorded,
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	// Before the preparation.
	before, err := workspace.Module(t.Context(), gomutants.ModuleQuery{})
	if err != nil {
		t.Fatalf("listing before Prepare: %v", err)
	}

	// Beside it. The tags make this a second, distinct query, so the memo
	// cannot answer it without a child and the assertion below is about a
	// `go list` that really ran while the preparation was in flight.
	held := holdPreparation(t, t.Context(), workspace, gomutants.PrepareOptions{
		Operators:  []string{"comparison"},
		SkipVerify: true,
	}, insidePhase(gomutants.PreparePhaseDiscovery, gomutants.PrepareEventFinished), nil)
	held.awaitHeld(t)

	beside := make(chan error, 1)
	var during gomutants.Module
	go func() {
		var listErr error
		during, listErr = workspace.Module(t.Context(), gomutants.ModuleQuery{Tags: []string{"special"}})
		beside <- listErr
	}()
	select {
	case listErr := <-beside:
		if listErr != nil {
			t.Fatalf("listing beside a held preparation: %v", listErr)
		}
	case <-time.After(moduleAnswerBound):
		t.Fatalf("Module did not answer within %s while a preparation was held at discovery,"+
			" so it waited for the preparation rather than running beside it", moduleAnswerBound)
	}
	if !slices.Equal(importPathsOf(during), importPathsOf(before)) {
		t.Errorf("the listing beside the preparation named %v, want %v",
			importPathsOf(during), importPathsOf(before))
	}
	held.release()
	prepared := held.await(t)
	if prepared.err != nil {
		t.Fatalf("preparing beside a listing: %v", prepared.err)
	}
	t.Cleanup(func() { _ = prepared.session.Close() })

	// After it. The tree a successful preparation leaves is byte for byte the
	// one Open froze, so the answer is the one from before it.
	after, err := workspace.Module(t.Context(), gomutants.ModuleQuery{})
	if err != nil {
		t.Fatalf("listing after Prepare: %v", err)
	}
	if !slices.Equal(importPathsOf(after), importPathsOf(before)) {
		t.Errorf("the listing after the preparation named %v, want %v",
			importPathsOf(after), importPathsOf(before))
	}

	assertListedInsideThePreparation(t, recorded.Events(), during.TraceSeq)

	if err = workspace.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	if _, err = workspace.Module(t.Context(), gomutants.ModuleQuery{}); !errors.Is(err, gomutants.ErrWorkspaceClosed) {
		t.Errorf("Module after Close = %v, want ErrWorkspaceClosed", err)
	}
}

// TestModuleFailureCarriesTheToolchainOutput is what a consumer reads when the
// query named something the module does not hold.
//
// It is an [gomutants.ExecutionError] with `Call: "module"` rather than a new
// type, and the toolchain's own words are in `Output`: a listing that refused
// and said only "go list failed" would send whoever is reading it to run the
// command by hand, which is the whole thing this call exists to save.
func TestModuleFailureCarriesTheToolchainOutput(t *testing.T) {
	workspace, err := gomutants.Open(t.Context(), copyFixture(t, "untested"), gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	module, err := workspace.Module(t.Context(), gomutants.ModuleQuery{Packages: []string{"./nope"}})
	if err == nil {
		t.Fatalf("listing ./nope succeeded with %+v", module)
	}
	var execution *gomutants.ExecutionError
	if !errors.As(err, &execution) {
		t.Fatalf("Module failed with %T (%v), want an *gomutants.ExecutionError", err, err)
	}
	if execution.Call != "module" {
		t.Errorf("ExecutionError.Call = %q, want %q", execution.Call, "module")
	}
	if !strings.Contains(execution.Output, "nope") {
		t.Errorf("ExecutionError.Output does not name the package that is missing:\n%s", execution.Output)
	}
	if !strings.Contains(err.Error(), "gomutants: module:") {
		t.Errorf("the message does not say which call failed: %q", err.Error())
	}
	if len(module.Packages) != 0 {
		t.Errorf("a failed listing still answered with %+v", module)
	}
}

// importPathsOf is the listing reduced to what most assertions above are about.
func importPathsOf(module gomutants.Module) []string {
	paths := make([]string, 0, len(module.Packages))
	for _, pkg := range module.Packages {
		paths = append(paths, pkg.ImportPath)
	}
	return paths
}

// packageAt is the one package an assertion is about, or a failure naming what
// the listing did hold.
func packageAt(t *testing.T, module gomutants.Module, importPath string) gomutants.Package {
	t.Helper()
	for _, pkg := range module.Packages {
		if pkg.ImportPath == importPath {
			return pkg
		}
	}
	t.Fatalf("the listing holds no %s, only %v", importPath, importPathsOf(module))
	return gomutants.Package{}
}

// moduleListings counts the `go-list` commands one workspace recorded on its
// own account, which is what a memo has to keep at one per distinct query.
func moduleListings(events []trace.Event) int {
	count := 0
	for _, event := range events {
		if event.Exec != nil && event.Exec.Kind == trace.ExecKindGoList && event.Exec.Subject == "module" {
			count++
		}
	}
	return count
}

// onlySnapshotRoot is the frozen tree one workspace made under parent.
func onlySnapshotRoot(t *testing.T, parent string) string {
	t.Helper()
	snapshots := snapshotDirectories(t, parent)
	if len(snapshots) != 1 {
		t.Fatalf("found %v under %s, want exactly one snapshot directory", snapshots, parent)
	}
	return filepath.Join(parent, snapshots[0])
}

// underRoot reports whether path is root or sits inside it, after both have
// been resolved: a temporary directory reached through a symlink — /var on
// macOS — is the same directory under another name, and a prefix comparison
// made on the unresolved spellings would answer no.
func underRoot(t *testing.T, root, path string) bool {
	t.Helper()
	relative, err := filepath.Rel(resolved(t, root), resolved(t, path))
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// resolved is a path with every symlink in it followed, or the path itself when
// it cannot be resolved.
func resolved(t *testing.T, path string) string {
	t.Helper()
	followed, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return followed
}

// assertListedInsideThePreparation requires the listing recorded at seq to sit
// between the preparation's first phase event and its last.
//
// That is what "beside a preparation" has to mean in a recording: the listing
// was issued after the preparation had started and its child was reaped before
// the preparation finished, and every one of those events was written by the
// same recorder under the locks themselves.
func assertListedInsideThePreparation(t *testing.T, events []trace.Event, seq int64) {
	t.Helper()
	if seq == 0 {
		t.Fatal("the listing beside the preparation carries no TraceSeq, so there is nothing to place")
	}
	first, last := preparationSeqs(t, events)
	if seq <= first || seq >= last {
		t.Errorf("the listing was recorded at seq %d, outside the preparation's %d..%d:"+
			" it waited the preparation out rather than running beside it", seq, first, last)
	}
}
