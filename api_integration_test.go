// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
)

// TestMain releases the sessions the probe tests share.
//
// They are prepared lazily and once, rather than per test, because preparing
// one is the expensive thing this file does — a snapshot, a discovery pass, two
// instrumented trees, two compile validations and four test binaries — while
// every probe assertion below is about the answers *one* prepared session
// gives. Sharing them means the file cannot use t.Cleanup to release them, so
// the release happens here, after the last test that could still reach one.
func TestMain(m *testing.M) {
	code := m.Run()
	releasePreparedFixtures()
	os.Exit(code)
}

func TestPublicSessionReusesOnePreparedSnapshot(t *testing.T) {
	root := copyFixture(t, "killable")
	extraTest := `package killable

import (
	"os"
	"testing"
	"time"
)

func TestSessionEnvironment(t *testing.T) {
	if os.Getenv("EXPECT_CLEAN") == "yes" && os.Getenv("GO_MUTANTS_ACTIVE") != "" {
		t.Fatalf("baseline inherited GO_MUTANTS_ACTIVE")
	}
	if os.Getenv("EXPECT_CLEAN") == "yes" && os.Getenv("FROZEN_AT_OPEN") != "before" {
		t.Fatalf("FROZEN_AT_OPEN = %q, want the value captured by Open", os.Getenv("FROZEN_AT_OPEN"))
	}
	if os.Getenv("WRITE_SNAPSHOT") == "yes" {
		if err := os.WriteFile("session-artifact.txt", []byte("written by target"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzSessionIdentity(f *testing.F) {
	f.Add("seed")
	f.Fuzz(func(t *testing.T, value string) {
		if string([]byte(value)) != value {
			t.Fatalf("string round trip changed %q", value)
		}
	})
}

func FuzzSessionClamp(f *testing.F) {
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, input []byte) {
		value := 5
		if len(input) > 0 {
			value = 10
		}
		if got := Clamp(value, 0, 10); got != map[bool]int{true: 9, false: 5}[len(input) > 0] {
			t.Fatalf("Clamp(%d, 0, 10) = %d", value, got)
		}
	})
}

func TestSessionBlocks(t *testing.T) {
	time.Sleep(10 * time.Second)
}
`
	if err := os.WriteFile(filepath.Join(root, "session_test.go"), []byte(extraTest), 0o644); err != nil {
		t.Fatal(err)
	}

	parent := t.TempDir()
	env := append(os.Environ(),
		"GO_MUTANTS_ACTIVE=must-be-scrubbed",
		"FROZEN_AT_OPEN=before",
	)
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		Env:           env,
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Setenv("FROZEN_AT_OPEN", "after")
	t.Cleanup(func() {
		if closeErr := workspace.Close(); closeErr != nil {
			t.Errorf("closing workspace: %v", closeErr)
		}
	})

	baseline, err := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "test", "./..."},
		Env:  []string{"EXPECT_CLEAN=yes"},
	})
	if err != nil {
		t.Fatalf("baseline infrastructure: %v", err)
	}
	if baseline.TimedOut || baseline.ExitCode != 0 {
		t.Fatalf("baseline = exit %d timeout=%v:\n%s", baseline.ExitCode, baseline.TimedOut, baseline.Output)
	}
	if _, reservedErr := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "version"},
		Env:  []string{"GO_MUTANTS_ACTIVE=stolen"},
	}); reservedErr == nil {
		t.Fatal("Workspace.Exec accepted a reserved activation variable")
	}

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Operators:     []string{"comparison"},
		MutantTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("preparing session: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("closing session: %v", closeErr)
		}
	})

	catalog := session.Catalog()
	if catalog.WorkspaceDigest == "" || catalog.Digest == "" {
		t.Fatalf("catalog has no frozen digests: %+v", catalog)
	}
	if catalog.ModulePath != "fixture.example/killable" {
		t.Errorf("module path = %q", catalog.ModulePath)
	}
	if !slices.Equal(catalog.TestPackages, []string{"fixture.example/killable"}) {
		t.Errorf("test packages = %v", catalog.TestPackages)
	}
	clamp := findMutant(t, catalog, "clamp.go", "lt-to-le")
	untested := findMutant(t, catalog, "untested.go", "neq-to-eq")
	if changes, changesErr := session.Changes(); changesErr != nil {
		t.Fatalf("checking the freshly prepared snapshot: %v", changesErr)
	} else if len(changes) != 0 {
		t.Fatalf("freshly prepared snapshot already changed: %+v", changes)
	}

	timeoutStarted := time.Now()
	timedOut, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  untested.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
		Timeout: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("executing a bounded target: %v", err)
	}
	if timedOut.Outcome != gomutants.OutcomeTimedOut || timedOut.KilledBy != "fixture.example/killable" {
		t.Errorf("bounded target result = %+v, want a package-local timeout", timedOut)
	}
	if elapsed := time.Since(timeoutStarted); elapsed > 5*time.Second {
		t.Errorf("bounded target returned after %s, want prompt process-tree cleanup", elapsed)
	}

	cancelContext, cancel := context.WithCancel(t.Context())
	cancelTimer := time.AfterFunc(250*time.Millisecond, cancel)
	cancelStarted := time.Now()
	cancelled, cancelErr := session.Exec(cancelContext, gomutants.ExecRequest{
		Mutant:  untested.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
		Timeout: 30 * time.Second,
	})
	cancelTimer.Stop()
	cancel()
	if !errors.Is(cancelErr, context.Canceled) {
		t.Errorf("cancel error = %v, want context.Canceled", cancelErr)
	}
	if cancelled.Outcome != gomutants.OutcomeNotRun {
		t.Errorf("cancelled target result = %+v, want not_run", cancelled)
	}
	if elapsed := time.Since(cancelStarted); elapsed > 5*time.Second {
		t.Errorf("cancelled target returned after %s, want prompt process-tree cleanup", elapsed)
	}

	killed, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  clamp.DisplayID,
		Package: "fixture.example/killable",
		Args:    []string{"-test.run=^TestClamp$"},
	})
	if err != nil {
		t.Fatalf("executing clamp mutant: %v", err)
	}
	if killed.Outcome != gomutants.OutcomeKilled || killed.KilledBy != "fixture.example/killable" {
		t.Errorf("clamp result = %+v, want a package-local kill", killed)
	}
	fuzzKilled, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  clamp.DisplayID,
		Package: "fixture.example/killable",
		Args: []string{
			"-test.run=^$",
			"-test.fuzz=^FuzzSessionClamp$",
			"-test.fuzztime=5s",
		},
	})
	if err != nil {
		t.Fatalf("fuzzing the clamp mutant: %v", err)
	}
	if fuzzKilled.Outcome != gomutants.OutcomeKilled || len(fuzzKilled.Artifacts) == 0 {
		t.Fatalf("fuzz kill = %+v, want a kill with captured standard corpus artifacts", fuzzKilled)
	}
	if !slices.ContainsFunc(fuzzKilled.Artifacts, func(artifact gomutants.Artifact) bool {
		return strings.HasPrefix(string(artifact.Data), "go test fuzz v1\n") && artifact.SHA256 != "" && artifact.Path != ""
	}) {
		t.Errorf("artifacts = %+v, want standard Go fuzz encoding", fuzzKilled.Artifacts)
	}
	if changes, changesErr := session.Changes(); changesErr != nil {
		t.Fatalf("checking snapshot after fuzz: %v", changesErr)
	} else if len(changes) != 0 {
		t.Fatalf("fuzz target changed the prepared snapshot: %+v", changes)
	}
	fuzzed, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  untested.DisplayID,
		Package: "fixture.example/killable",
		Args: []string{
			"-test.run=^$",
			"-test.fuzz=^FuzzSessionIdentity$",
			"-test.fuzztime=100ms",
		},
	})
	if err != nil {
		t.Fatalf("executing fuzz target: %v", err)
	}
	if fuzzed.Outcome != gomutants.OutcomeSurvived {
		t.Errorf("fuzz target result = %+v, want survivor", fuzzed)
	}

	survived, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  untested.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionEnvironment$"},
		Env:     []string{"WRITE_SNAPSHOT=yes"},
	})
	if err != nil {
		t.Fatalf("executing write target: %v", err)
	}
	if survived.Outcome != gomutants.OutcomeSurvived {
		t.Errorf("write target result = %+v, want survivor", survived)
	}
	if _, reservedErr := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant: untested.ID,
		Env:    []string{"GO_MUTANTS_ACTIVE=stolen"},
	}); reservedErr == nil {
		t.Fatal("Session.Exec accepted a reserved activation variable")
	}

	changes, err := session.Changes()
	if err != nil {
		t.Fatalf("checking snapshot changes: %v", err)
	}
	if !slices.ContainsFunc(changes, func(change gomutants.Change) bool {
		return change.Kind == gomutants.ChangeAdded && change.Path == "session-artifact.txt"
	}) {
		t.Errorf("changes = %+v, want the target-created artifact", changes)
	}
	if _, statErr := os.Stat(filepath.Join(root, "session-artifact.txt")); !os.IsNotExist(statErr) {
		t.Errorf("the target wrote into the user's workspace: %v", statErr)
	}
	for i := 1; i < len(changes); i++ {
		if strings.Compare(changes[i-1].Path, changes[i].Path) >= 0 {
			t.Errorf("changes are not strictly path-sorted: %+v", changes)
		}
	}
	if closeErr := session.Close(); closeErr != nil {
		t.Fatalf("closing session explicitly: %v", closeErr)
	}
	if afterClose := session.Catalog(); afterClose.Digest != catalog.Digest {
		t.Errorf("closed session catalog digest = %q, want immutable %q", afterClose.Digest, catalog.Digest)
	}
	if closeErr := workspace.Close(); closeErr != nil {
		t.Fatalf("closing workspace explicitly: %v", closeErr)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("reading temporary parent after close: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("temporary parent still holds %v after Close", entries)
	}
}

// TestWorkspaceExecBarrierHelper is the subprocess body used below. Reusing
// the already-built test executable keeps this concurrency test independent
// of a platform's Go build-cache scheduling and cold compilation speed.
func TestWorkspaceExecBarrierHelper(t *testing.T) {
	if os.Getenv("WORKSPACE_EXEC_BARRIER_HELPER") != "1" {
		return
	}
	temporary := strings.Join([]string{
		os.Getenv("TMP"), os.Getenv("TEMP"), os.Getenv("TMPDIR"),
	}, "\n")
	if err := os.WriteFile(os.Getenv("MARKER"), []byte(temporary), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("RELEASE")); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("release was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestWorkspaceExecRunsConcurrentlyWithPrivateTemporaryDirectories pins the
// contract baseline collectors depend on. Both commands must enter the helper
// before either is released; a serialized Workspace would leave the second
// marker absent. The value in each marker is that command's TMPDIR, which must
// also be distinct so concurrency cannot turn temporary files into shared
// state.
func TestWorkspaceExecRunsConcurrentlyWithPrivateTemporaryDirectories(t *testing.T) {
	root := copyFixture(t, "simple")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	coordination := t.TempDir()
	release := filepath.Join(coordination, "release")
	markers := []string{filepath.Join(coordination, "first"), filepath.Join(coordination, "second")}
	results := make(chan error, len(markers))
	for _, marker := range markers {
		go func() {
			result, execErr := workspace.Exec(t.Context(), gomutants.Command{
				Argv: []string{executable, "-test.run=^TestWorkspaceExecBarrierHelper$"},
				Env: []string{
					"WORKSPACE_EXEC_BARRIER_HELPER=1", "MARKER=" + marker, "RELEASE=" + release,
				},
			})
			if execErr == nil && (result.TimedOut || result.ExitCode != 0) {
				execErr = errors.New("barrier command did not pass: " + string(result.Output))
			}
			results <- execErr
		}()
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		ready := 0
		for _, marker := range markers {
			if _, statErr := os.Stat(marker); statErr == nil {
				ready++
			}
		}
		if ready == len(markers) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d concurrent commands reached the barrier", ready, len(markers))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if writeErr := os.WriteFile(release, []byte("release"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	for range markers {
		if execErr := <-results; execErr != nil {
			t.Error(execErr)
		}
	}
	firstBytes, err := os.ReadFile(markers[0])
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(markers[1])
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Split(string(firstBytes), "\n")
	second := strings.Split(string(secondBytes), "\n")
	keys := []string{"TMP", "TEMP", "TMPDIR"}
	if len(first) != len(keys) || len(second) != len(keys) {
		t.Fatalf("temporary variable markers = %q and %q", firstBytes, secondBytes)
	}
	for index, key := range keys {
		if first[index] == "" || second[index] == "" || first[index] == second[index] {
			t.Errorf("concurrent %s values = %q and %q, want two private directories", key, first[index], second[index])
		}
		if first[index] != first[0] || second[index] != second[0] {
			t.Errorf("temporary variables do not agree within each command: first=%q second=%q", first, second)
		}
	}
}

func TestPrepareRefusesDriftFromAWorkspaceCommand(t *testing.T) {
	root := copyFixture(t, "simple")
	testSource := `package simple

import (
	"os"
	"testing"
)

func TestWriteSnapshot(t *testing.T) {
	if err := os.WriteFile("command-artifact.txt", []byte("drift"), 0o600); err != nil {
		t.Fatal(err)
	}
}
`
	if err := os.WriteFile(filepath.Join(root, "write_test.go"), []byte(testSource), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err := gomutants.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	run, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "test", "-run=^TestWriteSnapshot$", "."}})
	if err != nil || run.TimedOut || run.ExitCode != 0 {
		t.Fatalf("drifting command = (%+v, %v)", run, err)
	}
	if _, err = workspace.Prepare(t.Context(), gomutants.PrepareOptions{}); err == nil ||
		!strings.Contains(err.Error(), "commands changed the frozen snapshot:\nadded command-artifact.txt") {
		t.Fatalf("Prepare after drift = %v", err)
	}
}

func findMutant(t *testing.T, catalog gomutants.Catalog, path, rule string) gomutants.Mutant {
	t.Helper()
	for _, mutant := range catalog.Mutants {
		if mutant.Path == path && mutant.Rule == rule {
			if !mutant.Accepted {
				t.Fatalf("mutant %s/%s was rejected", path, rule)
			}
			return mutant
		}
	}
	t.Fatalf("no %s mutant in %s: %+v", rule, path, catalog.Mutants)
	return gomutants.Mutant{}
}

func copyFixture(t *testing.T, name string) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), name)
	if err := copyFixtureTree(name, destination); err != nil {
		t.Fatalf("copying fixture: %v", err)
	}
	return destination
}

// copyFixtureTree is [copyFixture] without a testing.T, so that a fixture can
// also be copied for a session prepared once for the whole package rather than
// once per test.
func copyFixtureTree(name, destination string) error {
	source := filepath.Join("fixtures", name)
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// The probe session, against fixtures/probeable.
//
// The fixture holds three mutants and nothing else: two return-value mutants
// that a probe tree can speak for, and one boolean literal it cannot. Every
// assertion below is about one of the two directions the layer has to get
// right — a probed mutant a test infected is reported, and everything the
// measurement cannot vouch for is reported as no facts rather than as nothing
// infected.

// probeableModule is the fixture's import path, as ProbeRequest.Package takes
// it.
const probeableModule = "fixture.example/probeable"

// probeableRules names the fixture's three mutants by the rule that produced
// each, which is how every test below picks one out of the catalogue: no two
// functions in the fixture share an operator, so a rule names exactly one
// mutant whatever order the catalogue settles on.
const (
	widthRule = "return-zero-numeric"
	labelRule = "return-empty-string"
	readyRule = "true-to-false"
)

// A preparedFixture is one workspace and session prepared over
// fixtures/probeable, together with the temporary directory holding both
// snapshots.
//
// The parent is kept because two of the tests are about what is *in* it: a
// session prepared with Probe carries a second snapshot beside the mutant one,
// and a session prepared without it must carry no such thing.
type preparedFixture struct {
	parent    string
	workspace *gomutants.Workspace
	session   *gomutants.Session
	catalog   gomutants.Catalog
	err       error
}

var (
	// probedFixture and unprobedFixture are the two sessions this file shares,
	// each prepared at most once and only if a test asks for it.
	probedFixture   = sync.OnceValue(func() *preparedFixture { return prepareProbeable(true) })
	unprobedFixture = sync.OnceValue(func() *preparedFixture { return prepareProbeable(false) })

	// preparedMu guards the register TestMain releases. A sync.OnceValue cannot
	// be asked whether it ever ran, and preparing a session just to close it
	// would cost the suite the very minute the sharing saves.
	preparedMu       sync.Mutex
	preparedFixtures []*preparedFixture
)

// prepareProbeable copies the fixture, opens a workspace over it, and prepares
// one session with or without the probe tree.
//
// It takes no testing.T because it runs under a sync.Once that outlives the
// test that triggered it; a failure is carried in the value and reported by
// whichever test asks for it first.
func prepareProbeable(probe bool) *preparedFixture {
	prepared := &preparedFixture{}
	preparedMu.Lock()
	preparedFixtures = append(preparedFixtures, prepared)
	preparedMu.Unlock()

	parent, err := os.MkdirTemp("", "go-mutants-probe-fixture-")
	if err != nil {
		prepared.err = err
		return prepared
	}
	prepared.parent = parent

	root := filepath.Join(parent, "probeable")
	if err = copyFixtureTree("probeable", root); err != nil {
		prepared.err = err
		return prepared
	}
	workspace, err := gomutants.Open(context.Background(), root, gomutants.OpenOptions{TempDirectory: parent})
	if err != nil {
		prepared.err = err
		return prepared
	}
	prepared.workspace = workspace

	session, err := workspace.Prepare(context.Background(), gomutants.PrepareOptions{
		Probe:         probe,
		MutantTimeout: 30 * time.Second,
	})
	if err != nil {
		prepared.err = err
		return prepared
	}
	prepared.session = session
	prepared.catalog = session.Catalog()
	return prepared
}

// releasePreparedFixtures closes every session this file prepared and removes
// the directories they lived in.
func releasePreparedFixtures() {
	preparedMu.Lock()
	defer preparedMu.Unlock()
	for _, prepared := range preparedFixtures {
		if prepared.workspace != nil {
			_ = prepared.workspace.Close()
		}
		if prepared.parent != "" {
			_ = os.RemoveAll(prepared.parent)
		}
	}
	preparedFixtures = nil
}

// probeable returns the shared session prepared with a probe tree, failing the
// calling test if preparing it did not work.
func probeable(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := probedFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the probeable fixture with a probe tree: %v", prepared.err)
	}
	return prepared
}

// unprobeable returns the shared session prepared without one.
func unprobeable(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := unprobedFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the probeable fixture without a probe tree: %v", prepared.err)
	}
	return prepared
}

// byRule returns the one catalogued mutant a rule produced.
func byRule(t *testing.T, catalog gomutants.Catalog, rule string) gomutants.Mutant {
	t.Helper()
	var found []gomutants.Mutant
	for _, mutant := range catalog.Mutants {
		if mutant.Rule == rule {
			found = append(found, mutant)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the catalogue holds %d mutants of %s, want exactly 1: %+v", len(found), rule, catalog.Mutants)
	}
	if !found[0].Accepted {
		t.Fatalf("mutant %s was rejected during validation", found[0].DisplayID)
	}
	return found[0]
}

// snapshotDirectories counts the snapshot directories under a temporary parent.
// A probe tree is a second snapshot beside the mutant one, so the count is how
// a test says whether one was built without reaching into the session.
func snapshotDirectories(t *testing.T, parent string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("reading %s: %v", parent, err)
	}
	var snapshots []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "go-mutants-snap-") {
			snapshots = append(snapshots, entry.Name())
		}
	}
	return snapshots
}

// probeOf runs one target against the shared probe tree and fails the test if
// the pass could not be made at all.
func probeOf(t *testing.T, session *gomutants.Session, request gomutants.ProbeRequest) gomutants.ProbeResult {
	t.Helper()
	result, err := session.Probe(t.Context(), request)
	if err != nil {
		t.Fatalf("probing %v: %v", request.Args, err)
	}
	if result.Outcome != gomutants.ProbeMeasured {
		t.Fatalf("probing %v = %s, want %s:\n%s",
			request.Args, result.Outcome, gomutants.ProbeMeasured, result.OutputTail)
	}
	if result.Infected == nil {
		t.Fatalf("probing %v measured, but Infected is nil rather than a set", request.Args)
	}
	return result
}

// TestPrepareWithProbeMarksProbedMutants pins which mutants the probe tree
// speaks for.
//
// The distinction is the whole safety of the layer. A return-value mutant has a
// probe form and its site was compiled into the probe tree, so its absence from
// an infection log is a fact. The boolean literal has no form at all: the file
// holding it comes out of the probe pass byte for byte, so it can never be
// recorded, and a consumer reading its absence as "not infected" would skip the
// test that kills it. Probed is what tells the two apart, and a validation that
// merely accepted every mutant would say nothing about it.
func TestPrepareWithProbeMarksProbedMutants(t *testing.T) {
	catalog := probeable(t).catalog
	if len(catalog.Mutants) != 3 {
		t.Fatalf("the fixture catalogues %d mutants, want 3: %+v", len(catalog.Mutants), catalog.Mutants)
	}
	probed := 0
	for _, mutant := range catalog.Mutants {
		want := mutant.Family == "return-replacement"
		if mutant.Probed != want {
			t.Errorf("mutant %s (%s/%s) Probed = %v, want %v",
				mutant.DisplayID, mutant.Family, mutant.Rule, mutant.Probed, want)
		}
		if mutant.Probed {
			probed++
		}
	}
	if probed != 2 {
		t.Errorf("%d mutants are probed, want the fixture's 2 return-value ones", probed)
	}
}

// TestPrepareWithoutProbeBuildsNoProbeTree is the other half of the option: a
// session that did not ask for a probe tree pays for none.
//
// The assertion is about the directory rather than about the clock, because
// "Prepare was not slower" is not something a test can state; a second snapshot
// beside the first is exactly what building a probe tree leaves behind, and no
// mutant may claim to be probed without one.
func TestPrepareWithoutProbeBuildsNoProbeTree(t *testing.T) {
	prepared := unprobeable(t)
	if snapshots := snapshotDirectories(t, prepared.parent); len(snapshots) != 1 {
		t.Errorf("a session prepared without Probe left %d snapshots (%v), want only the mutant tree",
			len(snapshots), snapshots)
	}
	for _, mutant := range prepared.catalog.Mutants {
		if mutant.Probed {
			t.Errorf("mutant %s claims to be probed although no probe tree was built", mutant.DisplayID)
		}
	}
}

// TestProbeWithoutPreparationIsAnError pins the refusal a consumer has to be
// able to recognise: a session with no probe tree cannot answer the question at
// all, and must say so rather than answer it emptily.
func TestProbeWithoutPreparationIsAnError(t *testing.T) {
	prepared := unprobeable(t)
	result, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestWidth$"},
	})
	if !errors.Is(err, gomutants.ErrProbeNotPrepared) {
		t.Fatalf("error = %v, want one carrying ErrProbeNotPrepared", err)
	}
	if result.Infected != nil {
		t.Errorf("the refusal carried %v as infection facts, want none", result.Infected)
	}
	if result.Outcome != "" {
		t.Errorf("the refusal reported outcome %q, want none: an error carries no facts", result.Outcome)
	}
}

// TestProbeReportsTheMutantsATestInfected is the measurement itself: a test
// that reached a probed site with a differing value names that mutant, and a
// test that never called the function does not.
//
// Both halves are needed. A pass that reported every probed mutant for every
// test would satisfy the first on its own and would license nothing, and one
// that reported none would satisfy the second and would license everything.
func TestProbeReportsTheMutantsATestInfected(t *testing.T) {
	prepared := probeable(t)
	width := byRule(t, prepared.catalog, widthRule)
	label := byRule(t, prepared.catalog, labelRule)

	widthRun := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestWidth$"},
	})
	if !slices.Contains(widthRun.Infected, width.Index) {
		t.Errorf("probing TestWidth reported %v, want it to hold %d: Width() returns 3 and its mutant returns 0",
			widthRun.Infected, width.Index)
	}
	if slices.Contains(widthRun.Infected, label.Index) {
		t.Errorf("probing TestWidth reported %v, which holds %d although the test never calls Label",
			widthRun.Infected, label.Index)
	}
	if !slices.IsSorted(widthRun.Infected) {
		t.Errorf("infected = %v, want ascending catalogue indices", widthRun.Infected)
	}
	if len(slices.Compact(slices.Clone(widthRun.Infected))) != len(widthRun.Infected) {
		t.Errorf("infected = %v, want each index once", widthRun.Infected)
	}

	labelRun := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestLabel$"},
	})
	if !slices.Contains(labelRun.Infected, label.Index) {
		t.Errorf("probing TestLabel reported %v, want it to hold %d", labelRun.Infected, label.Index)
	}
	if slices.Contains(labelRun.Infected, width.Index) {
		t.Errorf("probing TestLabel reported %v, which holds %d although the test never calls Width",
			labelRun.Infected, width.Index)
	}
}

// TestProbeNeverReportsAnUnprobedMutant pins the invariant a consumer's
// fallback rests on: an unprobed mutant is absent from every measurement, so
// its absence carries no information and the consumer has to treat it as
// infected by every test.
//
// The generated runtime can only record a site it compiled a call for, so this
// is a statement about the whole pipeline rather than about the reader: a
// version that ever wrote an unprobed index would make the absence of one
// meaningful, and the fallback would silently stop being conservative.
func TestProbeNeverReportsAnUnprobedMutant(t *testing.T) {
	prepared := probeable(t)
	ready := byRule(t, prepared.catalog, readyRule)
	if ready.Probed {
		t.Fatalf("the fixture's boolean literal %s is probed; it is the specimen for the unprobed case",
			ready.DisplayID)
	}

	whole := probeOf(t, prepared.session, gomutants.ProbeRequest{Package: probeableModule})
	if slices.Contains(whole.Infected, ready.Index) {
		t.Errorf("a whole-package probe reported %v, which holds the unprobed mutant %d",
			whole.Infected, ready.Index)
	}
	byIndex := make(map[uint32]gomutants.Mutant, len(prepared.catalog.Mutants))
	for _, mutant := range prepared.catalog.Mutants {
		byIndex[mutant.Index] = mutant
	}
	for _, index := range whole.Infected {
		mutant, known := byIndex[index]
		if !known {
			t.Errorf("the probe reported index %d, which names no catalogued mutant", index)
			continue
		}
		if !mutant.Probed {
			t.Errorf("the probe reported %s, which is not probed", mutant.DisplayID)
		}
	}
}

// TestEveryKillIsPrecededByAnInfection is the soundness statement of the whole
// layer, over every (mutant, test) pair the fixture has.
//
// If a test kills a mutant, then that test observed a value the mutant would
// have changed, so a probe of that test has to name it. The consumer's rule is
// what is checked, which is the rule that licenses skipping an execution: a
// mutant is a candidate for skipping only when it is probed *and* absent from
// the measurement, so an unprobed mutant satisfies it however it was killed. A
// pair failing this is an execution a consumer would have dropped and a kill it
// would then never have found.
func TestEveryKillIsPrecededByAnInfection(t *testing.T) {
	prepared := probeable(t)
	tests := []string{"TestWidth", "TestLabel", "TestReady", "TestFlagged"}

	probedKills := 0
	for _, name := range tests {
		selector := "-test.run=^" + name + "$"
		measured := probeOf(t, prepared.session, gomutants.ProbeRequest{
			Package: probeableModule,
			Args:    []string{selector},
		})
		for _, mutant := range prepared.catalog.Mutants {
			executed, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
				Mutant:  mutant.ID,
				Package: probeableModule,
				Args:    []string{selector},
			})
			if err != nil {
				t.Fatalf("executing %s against %s: %v", mutant.DisplayID, name, err)
			}
			if executed.Outcome != gomutants.OutcomeKilled {
				continue
			}
			if !mutant.Probed {
				continue
			}
			probedKills++
			if !slices.Contains(measured.Infected, mutant.Index) {
				t.Errorf("%s kills %s, but probing it reported %v, which does not hold %d:"+
					" a consumer would have skipped the execution that finds this kill",
					name, mutant.DisplayID, measured.Infected, mutant.Index)
			}
		}
	}
	if probedKills == 0 {
		t.Error("no probed mutant was killed by any test, so the soundness statement held vacuously")
	}
}

// TestProbeOfAFailingTestCarriesNoFacts pins the first of the three no-fact
// outcomes.
//
// The probe tree is semantics-preserving, so a target that fails there is a
// flaky test or a bug in go-mutants, and either way the run it produced cannot
// be trusted to have reached every site it would have reached. Reporting the
// indices it happened to record before it failed is exactly what a smaller,
// wrong answer looks like, and a smaller answer here is a test that is skipped
// when it should have run.
func TestProbeOfAFailingTestCarriesNoFacts(t *testing.T) {
	prepared := probeable(t)
	result, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestFlagged$"},
		Env:     []string{"PROBEABLE_FAIL=yes"},
	})
	if err != nil {
		t.Fatalf("probing a failing target: %v", err)
	}
	if result.Outcome != gomutants.ProbeTestFailed {
		t.Errorf("outcome = %s, want %s:\n%s", result.Outcome, gomutants.ProbeTestFailed, result.OutputTail)
	}
	if result.Infected != nil {
		t.Errorf("infected = %v, want nil: a failing target proves nothing about infection", result.Infected)
	}
	if result.ExitCode == 0 {
		t.Errorf("exit code = 0 for a target reported as failed")
	}
	if !strings.Contains(result.OutputTail, "TestFlagged") {
		t.Errorf("output tail = %q, want the failing target's own output", result.OutputTail)
	}
}

// TestProbeOfATimedOutTestCarriesNoFacts is the second: a target the supervisor
// had to kill did not finish, so the sites it had not reached yet are
// indistinguishable from the sites it would never have reached.
func TestProbeOfATimedOutTestCarriesNoFacts(t *testing.T) {
	prepared := probeable(t)
	started := time.Now()
	result, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestBlocks$"},
		Env:     []string{"PROBEABLE_BLOCK=yes"},
		Timeout: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("probing a blocking target: %v", err)
	}
	if result.Outcome != gomutants.ProbeTimedOut {
		t.Errorf("outcome = %s, want %s:\n%s", result.Outcome, gomutants.ProbeTimedOut, result.OutputTail)
	}
	if result.Infected != nil {
		t.Errorf("infected = %v, want nil: a target that was killed proves nothing", result.Infected)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("the probe returned after %s, want prompt process-tree cleanup", elapsed)
	}
}

// TestProbeOfABinaryWithoutTheRuntimeMeasuresNothing is the one missing-log
// case that is a fact rather than a failure.
//
// The probe runtime writes its header in init, before any test code runs, so a
// log that is not there is a process that never linked a probe — and a process
// that never linked a probe cannot have run a probed site. The empty set is
// therefore the truth about it, and it has to be an empty set rather than nil,
// because nil is what every no-fact outcome above carries.
func TestProbeOfABinaryWithoutTheRuntimeMeasuresNothing(t *testing.T) {
	prepared := probeable(t)
	result := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule + "/isolated",
	})
	if len(result.Infected) != 0 {
		t.Errorf("infected = %v, want the empty set: the package links no instrumented file", result.Infected)
	}
}

// TestProbeRefusesTheSameRequestsAsExec keeps one request vocabulary for the
// two calls. A caller that composed a request for Exec must be able to hand the
// same package, arguments and environment to Probe and be refused for the same
// reasons rather than answered differently.
func TestProbeRefusesTheSameRequestsAsExec(t *testing.T) {
	prepared := probeable(t)
	cases := []struct {
		name    string
		request gomutants.ProbeRequest
	}{
		{
			name:    "a package with no prepared test binary",
			request: gomutants.ProbeRequest{Package: "fixture.example/probeable/absent"},
		},
		{
			name: "a target that overrides the harness timeout",
			request: gomutants.ProbeRequest{
				Package: probeableModule,
				Args:    []string{"-test.timeout=1s"},
			},
		},
		{
			name: "an activation variable the session reserves",
			request: gomutants.ProbeRequest{
				Package: probeableModule,
				Env:     []string{"GO_MUTANTS_ACTIVE=stolen"},
			},
		},
		{
			name:    "a negative timeout",
			request: gomutants.ProbeRequest{Package: probeableModule, Timeout: -time.Second},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, err := prepared.session.Probe(t.Context(), c.request)
			if err == nil {
				t.Fatalf("Probe accepted %+v and answered %+v", c.request, result)
			}
			if result.Infected != nil {
				t.Errorf("the refusal carried %v as infection facts, want none", result.Infected)
			}
		})
	}
}

// TestProbeIsSafeConcurrently pins the property a consumer running eight jobs
// depends on: probing and executing share a session and nothing else, so
// neither can observe the other's scratch directory, environment or log.
//
// Run under -race, which is where the claim is actually established; the
// assertions here only make sure every goroutine really did the work.
func TestProbeIsSafeConcurrently(t *testing.T) {
	prepared := probeable(t)
	width := byRule(t, prepared.catalog, widthRule)

	const workers = 8
	var wait sync.WaitGroup
	failures := make([]error, workers)
	infected := make([]bool, workers)
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if worker%2 == 0 {
				result, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
					Package: probeableModule,
					Args:    []string{"-test.run=^TestWidth$"},
				})
				if err != nil {
					failures[worker] = err
					return
				}
				if result.Outcome != gomutants.ProbeMeasured {
					failures[worker] = errors.New("probe outcome " + string(result.Outcome))
					return
				}
				infected[worker] = slices.Contains(result.Infected, width.Index)
				return
			}
			result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
				Mutant:  width.ID,
				Package: probeableModule,
				Args:    []string{"-test.run=^TestWidth$"},
			})
			if err != nil {
				failures[worker] = err
				return
			}
			if result.Outcome != gomutants.OutcomeKilled {
				failures[worker] = errors.New("exec outcome " + string(result.Outcome))
			}
		}()
	}
	wait.Wait()

	for worker, err := range failures {
		if err != nil {
			t.Errorf("worker %d: %v", worker, err)
		}
	}
	for worker := 0; worker < workers; worker += 2 {
		if !infected[worker] {
			t.Errorf("worker %d probed TestWidth without reporting the mutant it infects", worker)
		}
	}
}

// TestCloseRemovesTheProbeTree pins the probe tree's lifetime: it is a second
// disposable snapshot, and closing the session that owns it removes it exactly
// as closing releases the binaries built from it.
func TestCloseRemovesTheProbeTree(t *testing.T) {
	root := copyFixture(t, "probeable")
	parent := t.TempDir()
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: parent})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{Probe: true})
	if err != nil {
		t.Fatalf("preparing a probe session: %v", err)
	}
	if snapshots := snapshotDirectories(t, parent); len(snapshots) != 2 {
		t.Fatalf("a probe session left %d snapshots (%v), want the mutant tree and the probe tree",
			len(snapshots), snapshots)
	}
	if err = session.Close(); err != nil {
		t.Fatalf("closing the session: %v", err)
	}
	if snapshots := snapshotDirectories(t, parent); len(snapshots) != 1 {
		t.Errorf("closing the session left %d snapshots (%v), want only the workspace's own",
			len(snapshots), snapshots)
	}
	if err = workspace.Close(); err != nil {
		t.Fatalf("closing the workspace: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("reading the temporary parent: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the temporary parent still holds %v after Close", entries)
	}
}
