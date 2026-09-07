// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The engine API's toolchain-backed suite, and the shared sessions the rest of
// the tagged root files read.
//
// Every test in this file opens a workspace, which snapshots a module, probes a
// `go` toolchain and compiles it; most of them go on to prepare a session,
// which instruments two trees, validates both and builds four test binaries.
// None of it can run on a machine without Go, and all of it is measured in tens
// of seconds — so it is the integration tier, and `go test .` is the compiler
// alone.
//
// TestMain lives here and moves with the tag, which is correct rather than
// convenient: the only thing it does is release the sessions this file prepares,
// so a unit tier without those sessions needs no TestMain at all.

package gomutants_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// TestMain releases the sessions the probe tests share.
//
// They are prepared lazily and once, rather than per test, because preparing
// one is the expensive thing this file does — a snapshot, a discovery pass, two
// instrumented trees, two compile validations and four test binaries — while
// every probe assertion below is about the answers *one* prepared session
// gives. Sharing them means the file cannot use t.Cleanup to release them, so
// the release happens here, after the last test that could still reach one.
// workspaceBarrierEnv switches the barrier subprocess on. Its presence, not its
// value, is what [testkit.HelperEnabled] reads.
const workspaceBarrierEnv = "WORKSPACE_EXEC_BARRIER_HELPER"

func TestMain(m *testing.M) {
	code := m.Run()
	releasePreparedFixtures()
	os.Exit(code)
}

// sessionBlockEnv switches the injected fixture's blocking test on. Its value,
// not merely its presence, is what the test reads, so that an inherited
// SESSION_BLOCK= from some other tool cannot turn it on by accident.
//
// The gate is the same pattern fixtures/probeable uses for its own TestBlocks,
// and for the same reason. Two of the executions below want a target that
// outlives its timeout, and the only way to write one is a sleep — but an
// ungated sleep is paid by everything else that runs the package: the baseline
// `go test ./...` at the top of this file, the verification run inside Prepare,
// and every whole-package target. Ten seconds bought twice over, so that two
// assertions about a timeout could have a target to time out.
//
// The gate is also what lets the sleep be a full minute rather than the ten
// seconds it was. Nothing pays for it now except the two executions that ask,
// and both of those kill it inside a second — so the length is free, and a long
// one is what gives [cleanupBound] somewhere to sit.
const sessionBlockEnv = "SESSION_BLOCK"

// sessionBlockPIDFileEnv names a file the blocking target writes its own pid
// into, which is how this file proves a target was killed rather than merely
// waited for.
//
// It is written from the fixture's init() rather than from the test function,
// because init() is the earliest point at which a Go program can do anything at
// all: it runs before the testing package parses a flag, so the pid is on disk
// well before the supervisor's budget expires. Recording it inside
// TestSessionBlocks would race the very timeout the target exists to overrun.
//
// The variable is only set by the two executions that need the proof, so every
// other run of this package — the baseline, the verification, every
// whole-package target — writes nothing.
const sessionBlockPIDFileEnv = "SESSION_BLOCK_PIDFILE"

// targetDeathBound is how long a killed target is given to actually be gone.
//
// A kill is asynchronous with respect to the wait that returned: internal/runner
// signals a process group, or ends a job object, and the operating system tears
// the tree down on its own schedule. Five seconds is far more than that takes
// and far less than the minute the target would otherwise sleep, so a survivor
// is still caught with room to spare.
const targetDeathBound = 5 * time.Second

// targetDeathPoll is how often the probe asks. Fifty milliseconds is short
// enough that the ordinary case costs one or two reads and long enough not to
// spin.
const targetDeathPoll = 50 * time.Millisecond

// cleanupBound is how long a target that was cut off — by its own timeout, or
// by its caller walking away — may take to come back.
//
// It is a promptness check and not a cleanup proof, and the difference is worth
// stating because the two used to be confused here. What this bound rules out is
// a call that *returned late*: a supervisor that waited for a tree it should
// have killed, or a cancellation that stopped being delivered. What it cannot
// rule out is the tree surviving. If SIGKILL or TerminateJobObject silently
// stopped working, Exec would still come back inside
// [runner.TerminationGrace] + [runner.IODrainGrace] — Cmd.WaitDelay closes the
// pipe whatever the child is doing — while the target went on sleeping for the
// rest of its minute, and every elapsed check below would pass. That is what
// [requireTargetIsGone] is for: it asks the operating system whether the pid the
// target recorded still exists.
//
// It is the supervisor's own worst case plus room to start a process, written
// as that sum rather than as a round number, because a round number cannot say
// what it rules out. internal/runner gives a process group
// [runner.TerminationGrace] after SIGTERM before it sends SIGKILL, and then
// bounds the wait for the output pipe to reach EOF by [runner.IODrainGrace] —
// which is Cmd.WaitDelay, so it is enforced rather than hoped for. A target
// that came back later than those two together did not come back late because
// the supervisor let it: it came back because nothing killed it and it ran to
// its own end.
//
// The two numbers are the two failures it has to sit between, and both of them
// moved to make room:
//
//	twenty seconds of margin  A process start is not free on a loaded Windows
//	                          runner — Defender reads a fresh binary before it
//	                          runs — and this bound is not about how fast a
//	                          process starts. Three seconds put it near enough
//	                          to that noise for one Windows job to fail on it
//	                          and the next to pass.
//	a sixty-second sleep      The failure being ruled out is a target nothing
//	                          killed, which runs to its own end. Ten seconds
//	                          left no room above the noise, so the injected
//	                          TestSessionBlocks now sleeps for a minute — free,
//	                          because it is gated and nothing else runs it.
//
// So 24 seconds is far above any process start and far below the minute an
// unkilled target costs, which is the gap this assertion lives in. It is also
// below the 30-second budget the cancelled execution below carries, so a
// cancellation that stopped working is caught by the same line.
//
// Every number this replaces was wrong in one direction or the other. Fifteen
// seconds was past the old ten-second sleep, so it ruled out nothing whatsoever;
// five was 750 ms above the escalation ceiling, so it measured the runner's load
// whenever SIGTERM was actually ignored.
const cleanupBound = runner.TerminationGrace + runner.IODrainGrace + 20*time.Second

// fixtureGateEnv are the variables the injected fixture reads to switch a
// deliberately badly-behaved target on: [sessionBlockEnv] for the minute-long
// sleeper, [controlFailEnv] for the test that is red on the original program.
//
// They are listed in one place so that adding a gate to [killableExtraTests]
// and forgetting to strip it is a change to this slice rather than a failure
// three tests away. A gate is only a gate while nothing else can set it.
var fixtureGateEnv = []string{sessionBlockEnv, controlFailEnv}

// hostEnvWithoutFixtureGates is this process's environment with every
// [fixtureGateEnv] variable taken out of it.
//
// gomutants.Open freezes an environment — the one it is handed, or os.Environ()
// when it is handed none — and every command and target the workspace goes on to
// run inherits that frozen copy. So a developer who exported one of these once,
// or a runner that inherited it from some other tool, would have the gated
// target run in every execution that did not ask for it: the baseline
// `go test ./...`, the verification inside Prepare, and every whole-package
// target. SESSION_BLOCK costs a minute each time and turns the ungated half of
// TestSessionBlocksOnlyWhenAsked into the gated one; CONTROL_FAIL is worse,
// because it turns the fixture's suite *red* — the baseline fails, Prepare's
// verification fails, and nothing in the output would mention why.
//
// Removing the entry rather than appending an empty one, because "the last
// duplicate wins" is a rule about os/exec that this file should not have to
// rely on. The comparison folds case because the Windows environment does: a
// `session_block` set in a shell there is the same variable os.Getenv finds.
func hostEnvWithoutFixtureGates() []string {
	return slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.ContainsFunc(fixtureGateEnv, func(gate string) bool {
			return strings.EqualFold(name, gate)
		})
	})
}

// requireTargetIsGone is the cleanup proof: the process the target recorded is
// no longer running.
//
// It polls rather than asking once, because the kill is asynchronous with
// respect to the wait that returned — internal/runner signals a process group or
// ends a job object, and the tree comes down on the operating system's schedule,
// not before Exec's last statement.
//
// A missing pid file is reported rather than passed over. It means the target
// was cut off before it executed a single line of Go, which is not a failure of
// cleanup — but a proof that quietly skipped itself is the exact shape of defect
// this whole file is about, so it says so instead.
func requireTargetIsGone(t *testing.T, pidFile, what string) {
	t.Helper()

	recorded, err := os.ReadFile(pidFile)
	if err != nil {
		t.Errorf("the %s target recorded no pid (%v), so its cleanup is unproven: "+
			"it was cut off before it ran, or %s never reached it", what, err, sessionBlockPIDFileEnv)
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(recorded)))
	if err != nil {
		t.Errorf("the %s target wrote %q into its pid file, which is not a pid: %v", what, recorded, err)
		return
	}

	started := time.Now()
	for {
		gone, probeErr := processIsGone(pid)
		if probeErr != nil {
			t.Errorf("asking whether the %s target (pid %d) is still running: %v", what, pid, probeErr)
			return
		}
		if gone {
			return
		}
		if elapsed := time.Since(started); elapsed > targetDeathBound {
			t.Errorf("the %s target (pid %d) was still running %s after Exec returned, "+
				"so the call was bounded but the process tree was not killed", what, pid, elapsed)
			return
		}
		time.Sleep(targetDeathPoll)
	}
}

// blockGateBound is how long the same target may take when nothing asked it to
// block.
//
// Five seconds rather than a tighter number for the same reason: what this
// rules out is a target that slept when nothing asked it to, and any bound
// comfortably between a process start and the session's own ten-second default
// budget proves the gate held. A two-second bound would additionally assert
// that the runner was not busy, which is not a claim about go-mutants. It is
// deliberately unmoved by the minute the gated sleep now lasts: an ungated
// target that slept would be cut off at that default long before the minute
// was up, and would fail this line either way.
const blockGateBound = 5 * time.Second

// killableExtraTests is the source fixtures/killable is extended with for the
// tests below.
//
// It lives here rather than in the fixture because these targets are about the
// *API* — the environment a session composes, the fuzz artefacts it captures,
// the output budget it honours — and not about the mutants the fixture exists
// to prove killable. It is one string rather than one per test so that the
// package a session prepares is the same package whichever test prepared it.
const killableExtraTests = `package killable

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func init() {
	if os.Getenv("SESSION_BLOCK") != "yes" {
		return
	}
	if path := os.Getenv("SESSION_BLOCK_PIDFILE"); path != "" {
		_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
}

// TestPrintsALot is the chatty target the output-budget tests need: 2000 lines
// of 64 characters, which is over a hundred kilobytes and thirty times the
// smallest limit those tests set.
//
// Many short lines rather than one long one, so that the fifty-line rule the
// retained tail is trimmed by actually bites. One line would make OutputTail
// and the whole kept capture the same string, and a test comparing the two
// would then pass whatever that rule was.
//
// It prints to stdout directly rather than through t.Log because what is under
// test is what the engine captured from the process, and t.Log's buffer only
// reaches the stream when the test fails.
func TestPrintsALot(t *testing.T) {
	for range 2000 {
		fmt.Println(strings.Repeat("x", 64))
	}
}

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
	if os.Getenv("SESSION_BLOCK") != "yes" {
		return
	}
	time.Sleep(60 * time.Second)
}

// TestControlFails is red on the *original* program when the environment asks
// it to be, which is the one thing a control run has to be able to report and
// the one thing this fixture otherwise has no target for.
//
// The module is called killable because everything in it passes; without this
// there is nothing here whose failure belongs to the repository rather than to
// a mutant, and "a control reports a failing suite" would be a claim nothing
// exercises. The gate is what the sleeper's is for — the baseline, the
// verification inside Prepare and every whole-package target would otherwise be
// red — and it reads a value rather than a presence, so an inherited empty
// variable cannot turn it on.
func TestControlFails(t *testing.T) {
	if os.Getenv("CONTROL_FAIL") != "yes" {
		return
	}
	t.Fatal("CONTROL_FAIL asked this test to fail on the original program")
}
`

// killableRoot copies fixtures/killable into a directory of the test's own and
// adds [killableExtraTests] to it.
func killableRoot(t *testing.T) string {
	t.Helper()
	root := copyFixture(t, "killable")
	if err := os.WriteFile(filepath.Join(root, "session_test.go"), []byte(killableExtraTests), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPublicSessionReusesOnePreparedSnapshot(t *testing.T) {
	root := killableRoot(t)

	parent := t.TempDir()
	// Without the fixture's gate variables, for the reason spelled out on
	// hostEnvWithoutFixtureGates: an inherited SESSION_BLOCK would be paid a
	// minute at a time by the baseline below and by the verification inside
	// Prepare, and an inherited CONTROL_FAIL would make both of them red.
	env := append(hostEnvWithoutFixtureGates(),
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
	clamp := mutantkit.APIMutantAt(t, catalog, "clamp.go", "lt-to-le")
	untested := mutantkit.APIMutantAt(t, catalog, "untested.go", "neq-to-eq")
	if changes, changesErr := session.Changes(); changesErr != nil {
		t.Fatalf("checking the freshly prepared snapshot: %v", changesErr)
	} else if len(changes) != 0 {
		t.Fatalf("freshly prepared snapshot already changed: %+v", changes)
	}

	// Each blocking execution gets a pid file of its own, so that the second
	// proof cannot be satisfied by what the first target wrote.
	//
	// Two seconds rather than the quarter of one this used to allow, because the
	// target now has something to do before it is cut off: a freshly written test
	// binary on a Windows runner is scanned before it runs, and a budget that
	// expired during the loader would leave the pid unrecorded and the proof
	// vacuous. It is still two orders below the minute the target sleeps for.
	const blockingBudget = 2 * time.Second
	timeoutPIDFile := filepath.Join(t.TempDir(), "timed-out.pid")
	cancelPIDFile := filepath.Join(t.TempDir(), "cancelled.pid")

	timeoutStarted := time.Now()
	timedOut, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  untested.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
		Timeout: blockingBudget,
		Env: []string{
			sessionBlockEnv + "=yes",
			sessionBlockPIDFileEnv + "=" + timeoutPIDFile,
		},
	})
	if err != nil {
		t.Fatalf("executing a bounded target: %v", err)
	}
	if timedOut.Outcome != gomutants.OutcomeTimedOut || timedOut.KilledBy != "fixture.example/killable" {
		t.Errorf("bounded target result = %+v, want a package-local timeout", timedOut)
	}
	if elapsed := time.Since(timeoutStarted); elapsed > cleanupBound {
		t.Errorf("bounded target returned after %s, want prompt process-tree cleanup", elapsed)
	}
	requireTargetIsGone(t, timeoutPIDFile, "bounded")

	cancelContext, cancel := context.WithCancel(t.Context())
	cancelTimer := time.AfterFunc(blockingBudget, cancel)
	cancelStarted := time.Now()
	cancelled, cancelErr := session.Exec(cancelContext, gomutants.ExecRequest{
		Mutant:  untested.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
		Timeout: 30 * time.Second,
		Env: []string{
			sessionBlockEnv + "=yes",
			sessionBlockPIDFileEnv + "=" + cancelPIDFile,
		},
	})
	cancelTimer.Stop()
	cancel()
	if !errors.Is(cancelErr, context.Canceled) {
		t.Errorf("cancel error = %v, want context.Canceled", cancelErr)
	}
	if cancelled.Outcome != gomutants.OutcomeNotRun {
		t.Errorf("cancelled target result = %+v, want not_run", cancelled)
	}
	if elapsed := time.Since(cancelStarted); elapsed > cleanupBound {
		t.Errorf("cancelled target returned after %s, want prompt process-tree cleanup", elapsed)
	}
	requireTargetIsGone(t, cancelPIDFile, "cancelled")

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
		// Six times the fuzz budget, stated here rather than inherited from the
		// session, because this is the one execution in the file whose target
		// has a budget of its own. `-test.fuzztime=5s` is five seconds of
		// *fuzzing*, and the binary still has to start, seed the corpus,
		// schedule its workers and write the crasher it finds; on a loaded CI
		// runner that overhead put the whole thing past a tighter bound and the
		// step failed with `context deadline exceeded` rather than with
		// anything about mutation (PR #21, run 34030895957). A timeout whose
		// margin is a guess belongs next to the number it is a margin over.
		Timeout: 30 * time.Second,
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

// TestWorkspaceExecReportsTruncation pins the fact a consumer had to guess at:
// whether a command's output is all of it.
//
// Before this, the only sign was the notice line, so a consumer that cared had
// to match a string go-mutants formats for humans — which turns a diagnostic
// into a wire format that cannot be reworded. `Truncated` is the fact and
// `TotalBytes` is the size; the prefix stays exported for the renderers and for
// the consumers that were matching it, but nothing has to.
func TestWorkspaceExecReportsTruncation(t *testing.T) {
	root := killableRoot(t)
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := workspace.Close(); closeErr != nil {
			t.Errorf("closing workspace: %v", closeErr)
		}
	})

	const limit = 4096
	result, err := workspace.Exec(t.Context(), gomutants.Command{
		Argv:        []string{"go", "test", "-run=^TestPrintsALot$", "-v", "."},
		OutputLimit: limit,
	})
	if err != nil {
		t.Fatalf("running the chatty target: %v", err)
	}
	if result.TimedOut || result.ExitCode != 0 {
		t.Fatalf("chatty target = exit %d timeout=%v:\n%s", result.ExitCode, result.TimedOut, result.Output)
	}
	if !result.Truncated {
		t.Errorf("Truncated = false although the target prints 64 KiB into a %d-byte budget", limit)
	}
	if result.TotalBytes <= limit {
		t.Errorf("TotalBytes = %d, want more than the %d-byte budget", result.TotalBytes, limit)
	}
	if len(result.Output) > limit {
		t.Errorf("len(Output) = %d, want at most %d: the notice is paid for out of the budget",
			len(result.Output), limit)
	}
	if !bytes.HasPrefix(result.Output, []byte(gomutants.OutputTruncatedPrefix)) {
		t.Errorf("Output begins %q, want the exported prefix %q",
			string(result.Output[:min(len(result.Output), 120)]), gomutants.OutputTruncatedPrefix)
	}
}

// TestSessionExecHonoursOutputLimit is the same claim for a mutant execution,
// plus the one that made this worth an API change: a caller can now say how
// much output it is willing to hold.
//
// A mutant run silently used the runner's one-mebibyte default, which is both
// far more than a console wants and far less than a consumer archiving the
// evidence of a kill might. `OutputTail` is unchanged and stays the fifty-line
// summary; `Output` is the whole of what the budget kept, and the two agree.
func TestSessionExecHonoursOutputLimit(t *testing.T) {
	root := killableRoot(t)
	parent := t.TempDir()
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := workspace.Close(); closeErr != nil {
			t.Errorf("closing workspace: %v", closeErr)
		}
	})
	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Operators:     []string{"comparison"},
		SkipVerify:    true,
		MutantTimeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("preparing session: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("closing session: %v", closeErr)
		}
	})

	const limit = 4096
	clamp := mutantkit.APIMutantAt(t, session.Catalog(), "clamp.go", "lt-to-le")
	// The two targets together: one prints far past the budget and the other is
	// what turns the suite red, so the capture that comes back is a *deciding*
	// binary's and not merely a chatty one's.
	result, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:      clamp.ID,
		Package:     "fixture.example/killable",
		Args:        []string{"-test.run=^TestPrintsALot$|^TestClamp$"},
		OutputLimit: limit,
	})
	if err != nil {
		t.Fatalf("executing the chatty target: %v", err)
	}
	if result.Outcome != gomutants.OutcomeKilled {
		t.Fatalf("outcome = %s, want %s:\n%s", result.Outcome, gomutants.OutcomeKilled, result.OutputTail)
	}
	if !result.Truncated {
		t.Errorf("Truncated = false although the target prints 64 KiB into a %d-byte budget", limit)
	}
	if result.TotalBytes <= limit {
		t.Errorf("TotalBytes = %d, want more than the %d-byte budget", result.TotalBytes, limit)
	}
	if len(result.Output) > limit {
		t.Errorf("len(Output) = %d, want at most %d", len(result.Output), limit)
	}
	if want := lastLines(result.Output, 50); result.OutputTail != want {
		t.Errorf("OutputTail = %q, want the last 50 lines of Output with the carriage returns stripped: %q",
			result.OutputTail, want)
	}
}

// lastLines is the rule docs/library.md documents OutputTail by: the last n
// lines of a capture, with the carriage returns stripped.
//
// It is written from that sentence rather than transcribed from the trimming
// helper inside internal/execute, which is the whole point of having it. A copy
// of the implementation would agree with the implementation by construction and
// would go on agreeing with it through any change to either; this states what a
// consumer was promised, and disagreeing with it is the failure worth having.
//
// The trailing newline a stream ends with is a terminator and not an empty last
// line, which is the one place the sentence needs reading carefully.
func lastLines(output []byte, n int) string {
	text := strings.TrimRight(string(output), "\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}

// TestSessionBlocksOnlyWhenAsked is the gate on the injected sleep, as a fact
// rather than as a comment.
//
// fixtures/killable gets a test that sleeps for a minute, because two of the
// assertions above need a target that outlives its timeout and a sleep is the
// only way to write one. Every other execution of that package would pay for it
// unless it is gated — the baseline, the verification inside Prepare, every
// whole-package target — which is where a third of this file's minutes used to
// go, and a deleted `if` would put them straight back without failing anything.
//
// So both directions are asserted. Without the variable the target returns
// promptly and the mutant survives; with it, the same target and the same
// mutant time out. A gate that was removed fails the first half, and a gate
// whose variable was renamed on one side fails the second.
//
// The session is prepared without verification, which is what keeps this test
// affordable: nothing here needs the fixture's own suite to have been run
// against the instrumented tree, only a binary to execute.
func TestSessionBlocksOnlyWhenAsked(t *testing.T) {
	root := killableRoot(t)
	// A hostile host environment, set on purpose. Open freezes an environment
	// at the moment it is called and every execution below inherits it, so a
	// developer or a runner with SESSION_BLOCK already exported would turn the
	// ungated half of this test into the gated one — and it would read as the
	// engine failing to kill a target rather than as an inherited variable.
	// Setting it here is what makes hostEnvWithoutFixtureGates' removal a
	// claim this test can fail rather than a precaution nobody exercises.
	t.Setenv(sessionBlockEnv, "yes")

	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Operators:  []string{"comparison"},
		SkipVerify: true,
	})
	if err != nil {
		t.Fatalf("preparing session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	mutant := mutantkit.APIMutantAt(t, session.Catalog(), "untested.go", "neq-to-eq")

	started := time.Now()
	quiet, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  mutant.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
	})
	if err != nil {
		t.Fatalf("executing the ungated target: %v", err)
	}
	if quiet.Outcome != gomutants.OutcomeSurvived {
		t.Errorf("ungated target = %+v, want a survivor: the injected TestSessionBlocks slept without being asked to", quiet)
	}
	if elapsed := time.Since(started); elapsed > blockGateBound {
		t.Errorf("ungated target returned after %s, want under %s: the sleep is no longer gated on %s",
			elapsed, blockGateBound, sessionBlockEnv)
	}

	blocked, err := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  mutant.ID,
		Package: ".",
		Args:    []string{"-test.run=^TestSessionBlocks$"},
		Timeout: 250 * time.Millisecond,
		Env:     []string{sessionBlockEnv + "=yes"},
	})
	if err != nil {
		t.Fatalf("executing the gated target: %v", err)
	}
	if blocked.Outcome != gomutants.OutcomeTimedOut {
		t.Errorf("gated target = %+v, want a timeout: %s no longer reaches the target that reads it",
			blocked, sessionBlockEnv)
	}
}

// TestWorkspaceExecBarrierHelper is the subprocess body used below. Reusing
// the already-built test executable keeps this concurrency test independent
// of a platform's Go build-cache scheduling and cold compilation speed.
func TestWorkspaceExecBarrierHelper(t *testing.T) {
	if !testkit.HelperEnabled(workspaceBarrierEnv) {
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
				Argv: testkit.HelperArgv("TestWorkspaceExecBarrierHelper"),
				Env: []string{
					workspaceBarrierEnv + "=1", "MARKER=" + marker, "RELEASE=" + release,
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
	_, err = workspace.Prepare(t.Context(), gomutants.PrepareOptions{})
	if err == nil || !strings.Contains(err.Error(), "commands changed the frozen snapshot:\nadded command-artifact.txt") {
		t.Fatalf("Prepare after drift = %v", err)
	}
	// The message is the half a user reads; this is the half a consumer acts
	// on. A tree that moved under the engine is the one preparation failure
	// whose remedy belongs to the caller — it wrote the file — so the paths and
	// the kinds are carried rather than only printed.
	var drift *gomutants.DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("Prepare after drift = %v, want a *DriftError", err)
	}
	if drift.Stage != "commands" {
		t.Errorf("Stage = %q, want %q", drift.Stage, "commands")
	}
	if len(drift.Changes) != 1 {
		t.Fatalf("Changes = %+v, want the one file the command wrote", drift.Changes)
	}
	change := drift.Changes[0]
	if change.Kind != gomutants.ChangeAdded || change.Path != "command-artifact.txt" {
		t.Errorf("Changes[0] = %+v, want command-artifact.txt added", change)
	}
	if change.AfterSHA256 == "" {
		t.Errorf("Changes[0].AfterSHA256 is empty, so the added file cannot be identified: %+v", change)
	}
	if change.BeforeSHA256 != "" {
		t.Errorf("Changes[0].BeforeSHA256 = %q, want empty for a file that was not there", change.BeforeSHA256)
	}
}

// copyFixture copies one corpus module into a directory of the test's own.
//
// It is [testkit.Copy]: the fixtures are checked in and `git status --porcelain
// fixtures/` is a CI gate, while a workspace opened here writes a snapshot, a
// report directory and scratch beside the module it was pointed at. The harness
// also ages the copy, which is not cosmetic — cmd/go indexes a package directory
// only when every file in it is at least two seconds old, so a tree copied a
// moment ago is a different input from the same tree on a user's disk.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	return testkit.Copy(t, name)
}

// copyFixtureTree is [copyFixture] without a testing.T, so that a fixture can
// also be copied for a session prepared once for the whole package rather than
// once per test. It stays hand-written for exactly that reason: every helper in
// the harness takes a [testing.TB], and the shared session has none.
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
	events    []gomutants.PrepareEvent
	err       error
}

var (
	// probedFixture and unprobedFixture are the two probeable sessions this file
	// shares, each prepared at most once and only if a test asks for it.
	probedFixture   = sync.OnceValue(func() *preparedFixture { return prepareProbeable(true) })
	unprobedFixture = sync.OnceValue(func() *preparedFixture { return prepareProbeable(false) })

	// rejectedFixture is the third: fixtures/rejectable, whose candidates
	// include three whose mutated copy is not a program. It is the only shared
	// session with a non-empty Catalog.Rejections, and every contract claim
	// about a rejection is vacuous without one. It asks for no probe tree and no
	// verification, because nothing needs this session to measure anything —
	// only to have rejected something.
	rejectedFixture = sync.OnceValue(func() *preparedFixture {
		return prepareFixture("rejectable", gomutants.PrepareOptions{SkipVerify: true})
	})

	// preparedMu guards the register TestMain releases. A sync.OnceValue cannot
	// be asked whether it ever ran, and preparing a session just to close it
	// would cost the suite the very minute the sharing saves.
	preparedMu       sync.Mutex
	preparedFixtures []*preparedFixture
)

// prepareProbeable prepares one probeable session with or without the probe
// tree.
func prepareProbeable(probe bool) *preparedFixture {
	return prepareFixture("probeable", gomutants.PrepareOptions{
		Probe:              probe,
		ProbeCoverPackages: []string{probeableModule + "/..."},
		MutantTimeout:      30 * time.Second,
		SkipVerify:         !probe,
	})
}

// prepareFixture copies the named fixture, opens a workspace over it, and
// prepares one session with the given options. PrepareOptions.Trace is supplied
// here rather than by the caller, because the recorded events are one of the
// things the shared value carries.
//
// It takes no testing.T because it runs under a sync.Once that outlives the
// test that triggered it; a failure is carried in the value and reported by
// whichever test asks for it first.
func prepareFixture(name string, options gomutants.PrepareOptions) *preparedFixture {
	return prepareFixtureWith(name, nil, gomutants.OpenOptions{}, options)
}

// prepareFixtureWith is [prepareFixture] for a caller that also has something
// to say about how the workspace is opened. TempDirectory is not among those
// things: the parent is this helper's, because it is what the value carries and
// what releasing one removes.
//
// inject is source written into the copy before the workspace is opened, keyed
// by module-relative path. It is how a shared session gets the targets a test
// needs without those targets living in `fixtures/`, which is a checked-in tree
// a CI gate requires to stay clean — and without any test being able to add a
// file to a session that is already prepared.
func prepareFixtureWith(
	name string, inject map[string]string, open gomutants.OpenOptions, options gomutants.PrepareOptions,
) *preparedFixture {
	prepared := &preparedFixture{}
	preparedMu.Lock()
	preparedFixtures = append(preparedFixtures, prepared)
	preparedMu.Unlock()

	parent, err := os.MkdirTemp("", "go-mutants-"+name+"-fixture-")
	if err != nil {
		prepared.err = err
		return prepared
	}
	prepared.parent = parent

	root := filepath.Join(parent, name)
	if err = copyFixtureTree(name, root); err != nil {
		prepared.err = err
		return prepared
	}
	for path, source := range inject {
		if err = os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(source), 0o644); err != nil {
			prepared.err = err
			return prepared
		}
	}
	open.TempDirectory = parent
	workspace, err := gomutants.Open(context.Background(), root, open)
	if err != nil {
		prepared.err = err
		return prepared
	}
	prepared.workspace = workspace

	options.Trace = func(event gomutants.PrepareEvent) {
		prepared.events = append(prepared.events, event)
	}
	session, err := workspace.Prepare(context.Background(), options)
	if err != nil {
		prepared.err = err
		return prepared
	}
	prepared.session = session
	prepared.catalog = session.Catalog()
	return prepared
}

func TestPrepareTraceReportsEveryPhaseInOrder(t *testing.T) {
	serialPhases := []gomutants.PreparePhase{
		gomutants.PreparePhaseDiscovery,
		gomutants.PreparePhaseProbeSnapshot,
		gomutants.PreparePhaseMainValidation,
		gomutants.PreparePhaseMainRestoration,
		gomutants.PreparePhaseVerification,
	}
	probePhases := []gomutants.PreparePhase{
		gomutants.PreparePhaseProbeValidation,
		gomutants.PreparePhaseProbeCoverageBuild,
		gomutants.PreparePhaseProbeRestoration,
	}
	type eventKey struct {
		phase gomutants.PreparePhase
		state gomutants.PrepareEventState
	}
	var order []eventKey
	for _, phase := range serialPhases {
		order = append(order,
			eventKey{phase: phase, state: gomutants.PrepareEventStarted},
			eventKey{phase: phase, state: gomutants.PrepareEventFinished},
		)
	}
	order = append(order, eventKey{phase: gomutants.PreparePhaseBinaryBuild, state: gomutants.PrepareEventStarted})
	for _, phase := range probePhases {
		order = append(order,
			eventKey{phase: phase, state: gomutants.PrepareEventStarted},
			eventKey{phase: phase, state: gomutants.PrepareEventFinished},
		)
	}
	order = append(order, eventKey{phase: gomutants.PreparePhaseBinaryBuild, state: gomutants.PrepareEventFinished})
	for _, test := range []struct {
		name    string
		fixture *preparedFixture
		skipped map[gomutants.PreparePhase]bool
	}{
		{name: "probe", fixture: probeable(t)},
		{
			name:    "without probe",
			fixture: unprobeable(t),
			skipped: map[gomutants.PreparePhase]bool{
				gomutants.PreparePhaseProbeSnapshot:      true,
				gomutants.PreparePhaseVerification:       true,
				gomutants.PreparePhaseProbeValidation:    true,
				gomutants.PreparePhaseProbeCoverageBuild: true,
				gomutants.PreparePhaseProbeRestoration:   true,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, want := len(test.fixture.events), len(order); got != want {
				t.Fatalf("event count = %d, want %d: %+v", got, want, test.fixture.events)
			}
			for index, want := range order {
				event := test.fixture.events[index]
				if event.Phase != want.phase || event.State != want.state {
					t.Errorf("event %d = %+v, want phase %s state %s", index, event, want.phase, want.state)
					continue
				}
				if want.state == gomutants.PrepareEventStarted {
					if event.Result != "" || event.Duration != 0 {
						t.Errorf("phase %s start = %+v", want.phase, event)
					}
					continue
				}
				wantResult := gomutants.PreparePhaseSucceeded
				if test.skipped[want.phase] {
					wantResult = gomutants.PreparePhaseSkipped
				}
				if event.Result != wantResult || event.Duration < 0 {
					t.Errorf("phase %s finish = %+v, want result %s", want.phase, event, wantResult)
				}
			}
		})
	}
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

// rejectable returns the shared session over the fixture whose validation
// rejects, which is the only one whose catalogue carries rejections.
func rejectable(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := rejectedFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the rejectable fixture: %v", prepared.err)
	}
	return prepared
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
			request.Args, result.Outcome, gomutants.ProbeMeasured, result.Output)
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
	width := mutantkit.APIByRule(t, prepared.catalog, widthRule)
	label := mutantkit.APIByRule(t, prepared.catalog, labelRule)

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
	ready := mutantkit.APIByRule(t, prepared.catalog, readyRule)
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
		t.Errorf("outcome = %s, want %s:\n%s", result.Outcome, gomutants.ProbeTestFailed, result.Output)
	}
	if result.Infected != nil {
		t.Errorf("infected = %v, want nil: a failing target proves nothing about infection", result.Infected)
	}
	if result.ExitCode == 0 {
		t.Errorf("exit code = 0 for a target reported as failed")
	}
	if !strings.Contains(string(result.Output), "TestFlagged") {
		t.Errorf("output = %q, want the failing target's own output", result.Output)
	}
}

func TestProbeReturnsSuccessfulOutputAndCoverage(t *testing.T) {
	prepared := probeable(t)
	profile := filepath.Join(t.TempDir(), "coverage.out")
	result := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule,
		Args: []string{
			"-test.run=^TestWidth$",
			"-test.coverprofile=" + profile,
		},
	})
	if !bytes.Contains(result.Output, []byte("PASS")) {
		t.Fatalf("output = %q", result.Output)
	}
	if info, err := os.Stat(profile); err != nil || info.Size() == 0 {
		t.Fatalf("coverage profile = (%v, %v)", info, err)
	}
}

// TestProbeHonoursOutputLimit is [TestSessionExecHonoursOutputLimit] for the
// probe tree. The two calls take one request vocabulary, so a caller that
// bounded an execution has to be able to bound a pass the same way and be told
// the same thing about what was dropped.
func TestProbeHonoursOutputLimit(t *testing.T) {
	prepared := probeable(t)
	const limit = 4096
	result, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
		Package:     probeableModule,
		Args:        []string{"-test.run=^TestPrintsALot$"},
		OutputLimit: limit,
	})
	if err != nil {
		t.Fatalf("probing the chatty target: %v", err)
	}
	if result.Outcome != gomutants.ProbeMeasured {
		t.Fatalf("outcome = %s, want %s:\n%s", result.Outcome, gomutants.ProbeMeasured, result.Output)
	}
	if !result.Truncated {
		t.Errorf("Truncated = false although the target prints 64 KiB into a %d-byte budget", limit)
	}
	if result.TotalBytes <= limit {
		t.Errorf("TotalBytes = %d, want more than the %d-byte budget", result.TotalBytes, limit)
	}
	if len(result.Output) > limit {
		t.Errorf("len(Output) = %d, want at most %d", len(result.Output), limit)
	}
	if !bytes.HasPrefix(result.Output, []byte(gomutants.OutputTruncatedPrefix)) {
		t.Errorf("Output begins %q, want the exported prefix %q",
			string(result.Output[:min(len(result.Output), 120)]), gomutants.OutputTruncatedPrefix)
	}
}

func TestPreparedExecutionsExposeOnlyPristineSource(t *testing.T) {
	prepared := probeable(t)
	width := mutantkit.APIMutantAt(t, prepared.catalog, "probeable.go", widthRule)
	probe := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestSourceTreeIsPristine$"},
	})
	if len(probe.Infected) != 0 {
		t.Fatalf("source audit infected mutants = %v", probe.Infected)
	}
	result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  width.ID,
		Package: probeableModule,
		Args:    []string{"-test.run=^TestSourceTreeIsPristine$"},
	})
	if err != nil || result.Outcome != gomutants.OutcomeSurvived {
		t.Fatalf("source audit against mutant = (%+v, %v)", result, err)
	}
}

func TestPreparedExecutionsPropagateOverlayToChildGoTest(t *testing.T) {
	prepared := probeable(t)
	width := mutantkit.APIMutantAt(t, prepared.catalog, "probeable.go", widthRule)
	probe := probeOf(t, prepared.session, gomutants.ProbeRequest{
		Package: probeableModule,
		Args:    []string{"-test.run=^TestChildGoTestUsesSessionOverlay$"},
		Env:     []string{"PROBEABLE_CHILD_GO_TEST=parent"},
	})
	if !slices.Contains(probe.Infected, width.Index) {
		t.Fatalf("child go test infected mutants = %v, want %d", probe.Infected, width.Index)
	}
	result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  width.ID,
		Package: probeableModule,
		Args:    []string{"-test.run=^TestChildGoTestUsesSessionOverlay$"},
		Env:     []string{"PROBEABLE_CHILD_GO_TEST=parent"},
	})
	if err != nil || result.Outcome != gomutants.OutcomeKilled {
		t.Fatalf("child go test against mutant = (%+v, %v)", result, err)
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
		t.Errorf("outcome = %s, want %s:\n%s", result.Outcome, gomutants.ProbeTimedOut, result.Output)
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
	width := mutantkit.APIByRule(t, prepared.catalog, widthRule)

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
