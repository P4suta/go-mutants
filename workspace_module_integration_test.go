// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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

const untestedModule = "fixture.example/untested"

const taggedModule = "fixture.example/tagged"

const fixtureGoDirective = "1.26"

const moduleAnswerBound = 2 * time.Minute

const moduleInterestPoll = time.Millisecond

const modulePrefix = "gomutants: module: "

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
		if narrow.Path != whole.Path || narrow.GoVersion != whole.GoVersion {
			t.Errorf("a narrowed listing changed the module identity: %q/%q, want %q/%q",
				narrow.Path, narrow.GoVersion, whole.Path, whole.GoVersion)
		}
	})
}

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
	if _, err = workspace.Module(t.Context(), gomutants.ModuleQuery{Tags: []string{"special", "special"}}); err != nil {
		t.Fatalf("the repeated-tag listing: %v", err)
	}
	if got := moduleListings(workspace.Recording()); got != 2 {
		t.Errorf("a repeated tag listed again: %d `go-list` events in total, want two", got)
	}

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

func TestModuleIgnoresToolchainNoiseOnStderr(t *testing.T) {
	root := copyFixture(t, "untested")
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

func (g *gatedSink) awaitHeld(t *testing.T) {
	t.Helper()
	select {
	case <-g.held:
	case <-time.After(moduleAnswerBound):
		t.Fatalf("no %s event was recorded within %s, so nothing is being held", g.kind, moduleAnswerBound)
	}
}

func (g *gatedSink) release() { close(g.pass) }

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

	before, err := workspace.Module(t.Context(), gomutants.ModuleQuery{})
	if err != nil {
		t.Fatalf("listing before Prepare: %v", err)
	}

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

func importPathsOf(module gomutants.Module) []string {
	paths := make([]string, 0, len(module.Packages))
	for _, pkg := range module.Packages {
		paths = append(paths, pkg.ImportPath)
	}
	return paths
}

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

func moduleListings(events []trace.Event) int {
	count := 0
	for _, event := range events {
		if event.Exec != nil && event.Exec.Kind == trace.ExecKindGoList && event.Exec.Subject == "module" {
			count++
		}
	}
	return count
}

func onlySnapshotRoot(t *testing.T, parent string) string {
	t.Helper()
	snapshots := snapshotDirectories(t, parent)
	if len(snapshots) != 1 {
		t.Fatalf("found %v under %s, want exactly one snapshot directory", snapshots, parent)
	}
	return filepath.Join(parent, snapshots[0])
}

func underRoot(t *testing.T, root, path string) bool {
	t.Helper()
	relative, err := filepath.Rel(resolved(t, root), resolved(t, path))
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func resolved(t *testing.T, path string) string {
	t.Helper()
	followed, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return followed
}

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
