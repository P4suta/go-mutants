// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// Commands beside a *running* preparation: what `Workspace.Exec` may do while
// `Workspace.Prepare` is still working.
//
// The rule used to be one line — a command waits until `Prepare` returns — and
// it cost a consumer a whole second workspace, a second snapshot and a second
// discovery pass to run `go vet`, `go build` or a baseline of its own beside
// the preparation it was already paying for. It was one line because of one
// worry, that a command would compile the instrumented tree; and that worry is
// true of exactly one stretch of a preparation. `main_validation` rewrites the
// sources in place and `main_restoration` puts them back, so between those two
// the tree is a program nobody wrote. Everywhere else — discovery, the probe
// copy, verification, both builds — the tree is byte for byte the snapshot
// `Open` froze.
//
// So the rule is now the *window*: a command runs beside a preparation, and
// waits only from the integrity gate to the end of restoration. This file is
// one test per claim that makes that safe.
//
//   - Commands and a preparation overlap, and no command's process is inside
//     the window.
//   - A command issued inside the window waits for it, rather than being
//     refused or reading half an instrumented file.
//   - Discovery now reads the tree while a command may write it, so what
//     discovery read is checked against the frozen manifest afterwards: a
//     transient write that is undone before the gate is caught there, and
//     preparation fails rather than cataloguing mutants of bytes nobody has.
//   - `Close` still waits for a preparation, because a workspace that removed
//     its snapshot underneath one would be a use-after-free with a nicer name.
//   - A second `Prepare` is refused immediately rather than queued behind the
//     first, which is the one thing a caller could not do before: the first
//     holds the workspace for minutes and the second used to wait all of it out
//     to be told it was never allowed.

package gomutants_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/trace"
)

// prepareHoldBound is how long a preparation is given to reach a phase one of
// these tests holds it in, and to finish once it is released.
//
// It exists because a preparation that fails *before* the phase a test meant to
// hold — as one with an option this engine does not accept does — emits no
// event at all, and a test blocked on that event would hang until the package's
// own budget killed the whole tier. So the bound's only job is turning a
// deadlock into a sentence, and it is set where only a deadlock can reach it:
// the phases held here are not all the first one, and reaching
// `main_validation` means a discovery pass and a probe copy have already run —
// on a cold machine with a cold build cache, tens of seconds of honest work.
const prepareHoldBound = 3 * time.Minute

// commandBound is how long a command that must *not* be blocked is given.
//
// A `go list ./...` over a three-file fixture is a second of work, and the
// slack is for the machine rather than for the engine: this suite runs the
// preparation's own builds on every core at the same time.
const commandBound = 2 * time.Minute

// overlapWorkers is how many command loops run beside the preparation.
//
// Two rather than one because a single loop proves overlap and not
// concurrency, and rather than eight because each is a `go build ./...`
// competing with the preparation's own compiles for the same cores: the point
// is that commands run *at all* during a preparation, and a machine spending
// its whole capacity on this test measures the scheduler instead.
const overlapWorkers = 2

// overlapPause is how long each loop waits between commands, for the same
// reason there are two of them rather than eight.
const overlapPause = 25 * time.Millisecond

// discoveryDriftMarker is what [TestCommandDriftDuringDiscoveryFailsPrepare]
// appends to a frozen source file and takes off again while discovery is
// reading it.
const discoveryDriftMarker = "\n// a command wrote this during discovery\n"

// A heldPreparation is one preparation running in a goroutine, stopped inside a
// phase event of the caller's choosing until it is released.
//
// It is the shape every test below needs and the shape none of them should
// write twice, because getting it wrong is not a failing test but a hanging
// one. Three things earn their place. The release is idempotent and registered
// as a cleanup before the first thing that can fail, so a `t.Fatalf` unblocks
// the preparation on its way out instead of leaving it holding the workspace
// until the package's own budget kills the tier. The wait for the hold is
// bounded and also watches the preparation itself, because a preparation that
// fails *before* the phase — a refused option emits no event at all — would
// otherwise be waited on forever. And the result is carried out through a
// channel rather than closed by the goroutine, so the session belongs to the
// test that can see whether it is still needed.
type heldPreparation struct {
	held    chan struct{}
	release func()
	done    chan preparedSession
}

// A preparedSession is what the held preparation came back with.
type preparedSession struct {
	session *gomutants.Session
	err     error
}

// holdPreparation starts one preparation and holds it inside the first phase
// event `hold` accepts. Every event, held or not, is handed to `observe` first
// when one is supplied, so a test can record the phase timeline it is going to
// assert about.
//
// The context is the caller's because one test cancels it: a preparation that
// fails *inside* the window is the state a command queued behind that window
// has to be refused for, and cancelling is the cheapest honest way to produce
// one.
func holdPreparation(
	t *testing.T,
	ctx context.Context,
	workspace *gomutants.Workspace,
	options gomutants.PrepareOptions,
	hold func(gomutants.PrepareEvent) bool,
	observe func(gomutants.PrepareEvent),
) *heldPreparation {
	t.Helper()

	var (
		held        = make(chan struct{})
		release     = make(chan struct{})
		holdOnce    sync.Once
		releaseOnce sync.Once
	)
	preparation := &heldPreparation{
		held:    held,
		release: func() { releaseOnce.Do(func() { close(release) }) },
		done:    make(chan preparedSession, 1),
	}
	t.Cleanup(preparation.release)

	options.Trace = func(event gomutants.PrepareEvent) {
		if observe != nil {
			observe(event)
		}
		if hold(event) {
			holdOnce.Do(func() {
				close(held)
				<-release
			})
		}
	}
	go func() {
		session, err := workspace.Prepare(ctx, options)
		preparation.done <- preparedSession{session: session, err: err}
	}()
	return preparation
}

// awaitHeld blocks until the preparation is stopped in the phase that was asked
// for, and fails the test rather than hanging when it never gets there.
func (h *heldPreparation) awaitHeld(t *testing.T) {
	t.Helper()
	select {
	case <-h.held:
	case prepared := <-h.done:
		if prepared.session != nil {
			_ = prepared.session.Close()
		}
		t.Fatalf("Prepare returned (%v) before the phase this test meant to hold it in,"+
			" so nothing below is holding a preparation", prepared.err)
	case <-time.After(prepareHoldBound):
		t.Fatalf("Prepare did not reach the phase this test holds within %s", prepareHoldBound)
	}
}

// await releases nothing and waits for the preparation to finish, bounded, and
// closes the session it produced when the caller has no use for one.
func (h *heldPreparation) await(t *testing.T) preparedSession {
	t.Helper()
	select {
	case prepared := <-h.done:
		return prepared
	case <-time.After(prepareHoldBound):
		t.Fatalf("Prepare did not finish within %s of being released", prepareHoldBound)
		return preparedSession{}
	}
}

// insidePhase reports the event that opens or closes one named phase.
func insidePhase(phase gomutants.PreparePhase, state gomutants.PrepareEventState) func(gomutants.PrepareEvent) bool {
	return func(event gomutants.PrepareEvent) bool {
		return event.Phase == phase && event.State == state
	}
}

// TestWorkspaceExecOverlapsPreparationOutsideTheInstrumentationWindow is the
// feature, measured against the preparation that does the most: the killable
// fixture with verification on, which discovers, validates every mutant,
// instruments the tree, runs the module's whole suite through the instrumented
// build, restores the sources and compiles the test binaries.
//
// Commands run in a loop against the same workspace for as long as that lasts,
// and five things are asserted about what happened.
//
// They *overlapped*: commands finished while the preparation was still
// running, which is the whole point and the thing a second workspace used to be
// opened for.
//
// None of them was inside the window. The account both halves are read out of
// is the workspace's own recording rather than two clocks in this test, and
// that is what makes the assertion exact rather than approximate: a command's
// `exec` event is written by the runner the moment its child is reaped, which
// is *before* `Workspace.Exec` releases the tree, and the phase events are
// written while the preparation holds it. So the recorder's order is the lock's
// order, and a command whose interval touched the window is a command that read
// the tree while it was being rewritten.
//
// Every `go list ./...` agreed. Instrumentation adds a generated runtime
// package to the tree, so a `go list` that ran inside the window would name a
// package the module does not have — the semantic half of the same claim, and
// the one that would still catch a lock that was somehow the right shape and
// the wrong lock.
//
// The tree came out as the snapshot, both by `Session.Changes` and by
// re-freezing it, and every catalogued mutant's `SourceDigest` is the digest of
// the file that is on disk now. That last one is the discovery gate's promise
// from the outside: what discovery read is what the frozen tree holds.
//
// And the session still measures. A mutant that is killable without any of this
// is killable with it, because a preparation that overlapped a dozen commands
// is worth nothing if it prepared something else.
func TestWorkspaceExecOverlapsPreparationOutsideTheInstrumentationWindow(t *testing.T) {
	parent := t.TempDir()
	root := copyFixture(t, "killable")
	// An unbounded sink of this test's own, because the ring a workspace keeps
	// for itself is bounded and a preparation with a hundred commands beside it
	// is exactly the recording that would lose its own first half.
	recorded := trace.NewMemorySink(0)
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		Trace:         recorded,
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	prepareDone := make(chan preparedSession, 1)
	go func() {
		session, prepareErr := workspace.Prepare(context.Background(), gomutants.PrepareOptions{
			Operators:     []string{"comparison"},
			MutantTimeout: 30 * time.Second,
		})
		prepareDone <- preparedSession{session: session, err: prepareErr}
	}()

	stop := make(chan struct{})
	var loops sync.WaitGroup
	var listings sync.Map
	var commands atomic.Int64
	for worker := range overlapWorkers {
		loops.Add(1)
		go func() {
			defer loops.Done()
			for round := 0; ; round++ {
				select {
				case <-stop:
					return
				default:
				}
				argv := []string{"go", "list", "./..."}
				if round%2 == 1 {
					argv = []string{"go", "build", "./..."}
				}
				result, execErr := workspace.Exec(context.Background(), gomutants.Command{
					Argv: argv,
					// The go command searches every parent for a go.work, and
					// the snapshot sits in a temporary directory this test does
					// not own the parents of.
					Env: []string{"GOWORK=off"},
				})
				if execErr != nil || result.TimedOut || result.ExitCode != 0 {
					t.Errorf("a command beside the preparation: %v exit %d timeout=%v\n%s",
						execErr, result.ExitCode, result.TimedOut, result.Output)
					return
				}
				commands.Add(1)
				if argv[1] == "list" {
					listings.Store(fmt.Sprintf("%d/%d", worker, round), string(result.Output))
				}
				// A pause rather than a spin. What is under test is that
				// commands run *during* a preparation, and a loop that starts
				// another `go build` the instant the last one returned would
				// spend the machine the preparation needs and measure the
				// scheduler.
				time.Sleep(overlapPause)
			}
		}()
	}

	prepared := <-prepareDone
	close(stop)
	loops.Wait()
	if prepared.err != nil {
		t.Fatalf("preparing beside %d commands: %v", commands.Load(), prepared.err)
	}
	session := prepared.session
	t.Cleanup(func() { _ = session.Close() })

	assertCommandsRanOutsideTheWindow(t, recorded.Events())
	assertOneListing(t, &listings)

	if changes, changesErr := session.Changes(); changesErr != nil {
		t.Fatalf("checking the prepared snapshot: %v", changesErr)
	} else if len(changes) != 0 {
		t.Errorf("the commands changed the prepared snapshot: %+v", changes)
	}
	catalog := session.Catalog()
	assertTreesAreTheSnapshot(t, &preparedFixture{parent: parent, catalog: catalog})
	assertCatalogDigestsAreTheTree(t, parent, catalog)

	clamp := mutantkit.APIMutantAt(t, catalog, "clamp.go", "lt-to-le")
	killed, execErr := session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  clamp.ID,
		Package: killableModule,
		Args:    []string{"-test.run=^TestClamp$"},
	})
	if execErr != nil {
		t.Fatalf("executing %s after a preparation that overlapped commands: %v", clamp.DisplayID, execErr)
	}
	if killed.Outcome != gomutants.OutcomeKilled {
		t.Errorf("%s = %s, want the kill it gives when nothing ran beside the preparation:\n%s",
			clamp.DisplayID, killed.Outcome, killed.OutputTail)
	}
}

// A commandSpan is one recorded command as an interval on the recorder's clock:
// the event's timestamp is when the child was reaped, and the duration is how
// long it ran.
type commandSpan struct {
	seq   int64
	argv  []string
	start time.Time
	end   time.Time
}

// assertCommandsRanOutsideTheWindow reads the recording and requires that
// commands overlapped the preparation and that none of them was inside the
// instrumentation window.
//
// The duration a recorded command carries is whole milliseconds and is
// truncated, so the start it implies is never earlier than the real one — the
// direction that cannot invent an overlap that did not happen.
func assertCommandsRanOutsideTheWindow(t *testing.T, events []trace.Event) {
	t.Helper()

	windowStart, startSeq := preparePhaseAt(t, events,
		string(gomutants.PreparePhaseMainValidation), string(gomutants.PrepareEventStarted))
	windowEnd, endSeq := preparePhaseAt(t, events,
		string(gomutants.PreparePhaseMainRestoration), string(gomutants.PrepareEventFinished))
	if !windowEnd.After(windowStart) {
		t.Fatalf("the instrumentation window ends (%s) before it starts (%s)", windowEnd, windowStart)
	}
	firstPrepare, lastPrepare := preparationSeqs(t, events)

	spans := commandSpans(t, events)
	if len(spans) < overlapWorkers {
		t.Fatalf("the recording holds %d workspace commands, want at least the %d this test ran:"+
			" nothing overlapped the preparation", len(spans), overlapWorkers)
	}
	overlapped := 0
	for _, span := range spans {
		// Strictly between the preparation's first phase event and its last,
		// which is what "during" has to mean: the loops start before the
		// preparation does, so a command that merely finished before the last
		// phase may have finished before the first one too.
		if span.seq > firstPrepare && span.seq < lastPrepare {
			overlapped++
		}
		if span.start.Before(windowEnd) && span.end.After(windowStart) {
			t.Errorf("%v ran from %s to %s, inside the instrumentation window %s..%s"+
				" (seq %d, window %d..%d): it compiled a tree nobody wrote",
				span.argv, span.start, span.end, windowStart, windowEnd, span.seq, startSeq, endSeq)
		}
	}
	if overlapped == 0 {
		t.Errorf("none of the %d commands finished between the preparation's first phase and its"+
			" last, so every one of them waited the preparation out and this test proves nothing",
			len(spans))
	}
	// The ratio is the feature, in one line a reader of a failing run can act
	// on: the window is how long a command can be made to wait, and the span is
	// how long it used to wait for.
	first, last := preparationSpan(t, events)
	t.Logf("%d of %d commands finished inside the preparation; the window was %s of a %s"+
		" preparation", overlapped, len(spans), windowEnd.Sub(windowStart), last.Sub(first))
}

// preparationSpan is when the preparation's first and last phase events were
// recorded, which is what a command used to wait for in full.
func preparationSpan(t *testing.T, events []trace.Event) (first, last time.Time) {
	t.Helper()
	for _, event := range events {
		if event.Prepare == nil {
			continue
		}
		if first.IsZero() {
			first = eventTime(t, event)
		}
		last = eventTime(t, event)
	}
	return first, last
}

// preparePhaseAt is when one phase event was recorded, and where in the
// recording it sits.
func preparePhaseAt(t *testing.T, events []trace.Event, phase, state string) (time.Time, int64) {
	t.Helper()
	for _, event := range events {
		if event.Prepare == nil || event.Prepare.Phase != phase || event.Prepare.State != state {
			continue
		}
		return eventTime(t, event), event.Seq
	}
	t.Fatalf("the recording holds no %s %s event, so there is no window to compare against", phase, state)
	return time.Time{}, 0
}

// preparationSeqs are where the preparation's first and last phase events sit,
// which is the stretch of the recording a command has to have finished inside
// to have run *during* one.
func preparationSeqs(t *testing.T, events []trace.Event) (first, last int64) {
	t.Helper()
	for _, event := range events {
		if event.Prepare == nil {
			continue
		}
		if first == 0 {
			first = event.Seq
		}
		last = event.Seq
	}
	if first == 0 {
		t.Fatal("the recording holds no preparation phase events at all")
	}
	return first, last
}

// commandSpans are the workspace commands in the recording, as intervals.
func commandSpans(t *testing.T, events []trace.Event) []commandSpan {
	t.Helper()
	spans := make([]commandSpan, 0, len(events))
	for _, event := range events {
		if event.Exec == nil || event.Exec.Kind != trace.ExecKindWorkspaceExec {
			continue
		}
		end := eventTime(t, event)
		spans = append(spans, commandSpan{
			seq:   event.Seq,
			argv:  slices.Clone(event.Exec.Argv),
			start: end.Add(-time.Duration(event.Exec.DurationMS) * time.Millisecond),
			end:   end,
		})
	}
	return spans
}

// eventTime parses one event's timestamp, which the recorder writes in RFC 3339
// with nanosecond precision.
func eventTime(t *testing.T, event trace.Event) time.Time {
	t.Helper()
	stamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		t.Fatalf("event %d has an unparsable timestamp %q: %v", event.Seq, event.Timestamp, err)
	}
	return stamp
}

// assertOneListing requires every `go list ./...` run beside the preparation to
// have named the same packages.
//
// Instrumentation puts a generated runtime package into the tree, so a listing
// taken inside the window would carry a package the module does not have. This
// is the same claim as the interval arithmetic above, made out of what the
// commands *saw* rather than out of when they ran.
func assertOneListing(t *testing.T, listings *sync.Map) {
	t.Helper()
	var first, firstKey string
	listings.Range(func(key, value any) bool {
		listing, _ := value.(string)
		name, _ := key.(string)
		if first == "" {
			first, firstKey = listing, name
			return true
		}
		if listing != first {
			t.Errorf("`go list ./...` in round %s named\n%s\nand in round %s named\n%s",
				firstKey, first, name, listing)
		}
		return true
	})
	if first == "" {
		t.Error("no `go list ./...` output was captured, so the listings prove nothing")
	}
}

// assertCatalogDigestsAreTheTree requires every catalogued mutant's
// SourceDigest to be the digest of the file the prepared tree holds now.
//
// It is the discovery gate seen from outside the engine. A mutant's identity is
// minted from the bytes of its file, so a catalogue built while a command was
// rewriting one would name mutations of a file nobody has — and every later
// answer about that mutant, including a cached one, would be about the wrong
// program.
func assertCatalogDigestsAreTheTree(t *testing.T, parent string, catalog gomutants.Catalog) {
	t.Helper()
	snapshots := snapshotDirectories(t, parent)
	if len(snapshots) == 0 {
		t.Fatalf("no snapshot directories under %s", parent)
	}
	root := filepath.Join(parent, snapshots[0], snapshot.TreeName)
	digests := make(map[string]string)
	for _, mutant := range catalog.Mutants {
		digest, cached := digests[mutant.Path]
		if !cached {
			source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(mutant.Path)))
			if err != nil {
				t.Fatalf("reading the prepared %s: %v", mutant.Path, err)
			}
			sum := sha256.Sum256(source)
			digest = hex.EncodeToString(sum[:])
			digests[mutant.Path] = digest
		}
		if mutant.SourceDigest != digest {
			t.Errorf("%s was catalogued against %s and the prepared tree holds %s:"+
				" discovery read a file a command was writing",
				mutant.DisplayID, mutant.SourceDigest, digest)
		}
	}
}

// TestACommandThatStartsInsideTheWindowWaitsForIt is the half of the old rule
// that survives, and the reason the new one is safe.
//
// A command issued while the tree is being instrumented is not refused and does
// not read what is there: it waits, exactly as every command used to wait for
// the whole preparation, and runs against the restored tree afterwards. The
// preparation is held inside `main_validation` — under the exclusive half of
// the tree lock — while the command is started and watched for
// [execWaitProbe], which catches a command that ran through. Then the
// preparation is released and the command must come back only after
// `main_restoration` has finished: the flag it reads is written from inside the
// window, so observing it set is an ordering fact rather than a timing one.
func TestACommandThatStartsInsideTheWindowWaitsForIt(t *testing.T) {
	root := copyFixture(t, "simple")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	var restored atomic.Bool
	held := holdPreparation(t, context.Background(), workspace,
		gomutants.PrepareOptions{SkipVerify: true},
		insidePhase(gomutants.PreparePhaseMainValidation, gomutants.PrepareEventStarted),
		func(event gomutants.PrepareEvent) {
			if event.Phase == gomutants.PreparePhaseMainRestoration &&
				event.State == gomutants.PrepareEventFinished {
				restored.Store(true)
			}
		})
	held.awaitHeld(t)

	execDone := make(chan error, 1)
	go func() {
		_, execErr := workspace.Exec(context.Background(), gomutants.Command{
			Argv: []string{"go", "list", "./..."},
			Env:  []string{"GOWORK=off"},
		})
		execDone <- execErr
	}()
	select {
	case execErr := <-execDone:
		t.Fatalf("Exec returned (%v) inside the instrumentation window, where the tree holds"+
			" instrumented sources", execErr)
	case <-time.After(execWaitProbe):
	}

	held.release()
	select {
	case execErr := <-execDone:
		if execErr != nil {
			t.Fatalf("Exec once the window had closed: %v", execErr)
		}
	case <-time.After(commandBound):
		t.Fatalf("Exec did not return within %s of the window closing", commandBound)
	}
	if !restored.Load() {
		t.Error("Exec returned before main_restoration finished, so a command can observe a tree" +
			" that is still instrumented")
	}
	prepared := held.await(t)
	if prepared.err != nil {
		t.Fatalf("Prepare: %v", prepared.err)
	}
	if closeErr := prepared.session.Close(); closeErr != nil {
		t.Errorf("closing the session: %v", closeErr)
	}
}

// A queuedCommand is one command run on a goroutine of its own, with both
// halves of what it answered.
type queuedCommand struct {
	result gomutants.CommandResult
	err    error
}

// TestACommandThatWaitedOutAFailedWindowIsRefused is the other end of the
// window, and the dangerous one.
//
// A command issued while the tree is being instrumented waits for the window —
// that is [TestACommandThatStartsInsideTheWindowWaitsForIt]. But a window can
// *fail*: validation can break, the context can be cancelled, a restoration can
// fail to write. The tree then still holds instrumented sources, and the
// commands queued behind that window are woken by the very unlock that gives up
// on it. Waking them into a `go list` of a program nobody wrote — the generated
// runtime package and all — would be the worst outcome this feature could
// produce, because the command *succeeds* and its answer is silently about
// something else.
//
// So the failure is published before the window is unlocked, and a command
// re-asks whether it is still allowed to run once it holds the tree. Both
// halves are needed and this test would pass with either one missing only if
// the other were doing its job.
//
// The window is failed by cancelling the preparation's context while it is held
// at the top of `main_validation`, which is the cheapest honest in-window
// failure: the validation's own builds are the first thing to notice.
func TestACommandThatWaitedOutAFailedWindowIsRefused(t *testing.T) {
	root := copyFixture(t, "simple")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	prepareCtx, cancelPrepare := context.WithCancel(context.Background())
	t.Cleanup(cancelPrepare)
	held := holdPreparation(t, prepareCtx, workspace,
		gomutants.PrepareOptions{SkipVerify: true},
		insidePhase(gomutants.PreparePhaseMainValidation, gomutants.PrepareEventStarted), nil)
	held.awaitHeld(t)

	queued := make(chan queuedCommand, 1)
	go func() {
		result, execErr := workspace.Exec(context.Background(), gomutants.Command{
			Argv: []string{"go", "list", "./..."},
			Env:  []string{"GOWORK=off"},
		})
		queued <- queuedCommand{result: result, err: execErr}
	}()
	select {
	case answered := <-queued:
		t.Fatalf("Exec returned (%+v, %v) inside the instrumentation window", answered.result, answered.err)
	case <-time.After(execWaitProbe):
	}

	cancelPrepare()
	held.release()
	prepared := held.await(t)
	if prepared.session != nil {
		_ = prepared.session.Close()
	}
	if prepared.err == nil {
		t.Fatal("Prepare succeeded although its context was cancelled inside the window, so this" +
			" test never produced the state it is about")
	}

	var answered queuedCommand
	select {
	case answered = <-queued:
	case <-time.After(commandBound):
		t.Fatalf("the queued command did not return within %s of the window failing", commandBound)
	}
	if !errors.Is(answered.err, gomutants.ErrPrepareFailed) {
		t.Errorf("a command that waited out a failed window = %v, want ErrPrepareFailed", answered.err)
	}
	// And it did not run: a preparation that gave up inside the window left the
	// tree instrumented, so a command that had produced any answer at all would
	// have produced it about a program nobody wrote.
	if answered.result.Duration != 0 || len(answered.result.Output) != 0 || answered.result.ExitCode != 0 {
		t.Errorf("the command ran on the instrumented tree: %+v\n%s",
			answered.result, answered.result.Output)
	}
}

// TestACommandThatWritesDuringTheBinaryBuildFailsPrepare closes the last stretch
// of a preparation nothing was checking.
//
// The window ends at `main_restoration` and the session is published after the
// test binaries are built, and in between — the whole binary build, which is
// the longest phase of a real preparation — a command may write into the tree.
// Those bytes are what the binaries are compiled from, and they are also what
// `scanFiles` records as the state the session was prepared in, so the drift
// would be baked into `Session.Changes`'s own baseline and reported by nothing
// at all.
//
// The lifecycle says every write into the frozen tree during a preparation
// fails it. This is the check that makes the sentence true to the end of the
// preparation rather than to the end of the window.
func TestACommandThatWritesDuringTheBinaryBuildFailsPrepare(t *testing.T) {
	root := copyFixture(t, "simple")
	if err := os.WriteFile(filepath.Join(root, "write_test.go"), []byte(writeAfterPrepareTest), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	held := holdPreparation(t, context.Background(), workspace,
		gomutants.PrepareOptions{SkipVerify: true},
		insidePhase(gomutants.PreparePhaseBinaryBuild, gomutants.PrepareEventStarted), nil)
	held.awaitHeld(t)

	// From this goroutine rather than from the callback: the binary build is
	// outside the window, so the command runs — which is the whole point — and
	// a callback that waited for one would be the preparation's own goroutine
	// waiting on a call that a queued Close would deadlock.
	written, execErr := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "test", "-run=^TestWriteAfterPrepare$", "."},
		Env:  []string{"GOWORK=off", writeAfterPrepareEnv + "=yes"},
	})
	if execErr != nil || written.TimedOut || written.ExitCode != 0 {
		t.Fatalf("the writing command = (%+v, %v)", written, execErr)
	}

	held.release()
	prepared := held.await(t)
	if prepared.session != nil {
		_ = prepared.session.Close()
	}
	var drift *gomutants.DriftError
	if !errors.As(prepared.err, &drift) {
		t.Fatalf("Prepare after a command wrote into the tree during the binary build = %v,"+
			" want a *DriftError", prepared.err)
	}
	if drift.Stage != "test binaries" {
		t.Errorf("Stage = %q, want %q: the write landed after the window and before the session",
			drift.Stage, "test binaries")
	}
	if !slices.ContainsFunc(drift.Changes, func(change gomutants.Change) bool {
		return change.Kind == gomutants.ChangeAdded && change.Path == writeAfterPrepareArtifact
	}) {
		t.Errorf("Changes = %+v, want the %s the command wrote", drift.Changes, writeAfterPrepareArtifact)
	}
	if _, execErr := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "version"},
	}); !errors.Is(execErr, gomutants.ErrPrepareFailed) {
		t.Errorf("Exec after the refusal = %v, want ErrPrepareFailed", execErr)
	}
}

// TestCommandDriftDuringDiscoveryFailsPrepare is the hole the new rule opens,
// closed.
//
// Discovery reads the tree while a command may write it, so a command that
// changes a source file *and puts it back* before the integrity gate leaves no
// trace for the gate to find — and the catalogue would be full of mutants
// identified by the digest of bytes that are nowhere on disk. Every later
// answer about one of them, a cached one most of all, would be about a program
// nobody has.
//
// So what discovery read is checked against the manifest once the tree is held
// exclusively, and this is that check with the exact write it exists for: a
// comment is appended to `simple.go` as discovery starts and taken off again as
// discovery finishes. The integrity gate then sees a pristine tree and passes;
// the catalogue is refused, naming the file.
//
// The bytes are written by this test rather than by a `Workspace.Exec` child,
// and that is the honest way round rather than a shortcut. The write it
// simulates is exactly those bytes appearing and disappearing in the frozen
// tree, which is all the check can see — and the callback that has to do the
// timing runs on the *preparation's own goroutine*, which may not call into the
// workspace: a command started there would be that goroutine waiting for a lock
// a queued Close could take first. Writing the file directly keeps the timing
// exact without asking the callback to do the one thing it must not.
func TestCommandDriftDuringDiscoveryFailsPrepare(t *testing.T) {
	parent := t.TempDir()
	root := copyFixture(t, "simple")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: parent})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	snapshots := snapshotDirectories(t, parent)
	if len(snapshots) != 1 {
		t.Fatalf("%s holds %d snapshots, want the one Open froze", parent, len(snapshots))
	}
	frozen := filepath.Join(parent, snapshots[0], snapshot.TreeName, "simple.go")
	pristine, err := os.ReadFile(frozen)
	if err != nil {
		t.Fatalf("reading the frozen simple.go: %v", err)
	}
	// A trailing comment: a change to every byte-level identity the engine mints
	// from the file, and to nothing a compiler can see, so discovery goes on
	// working while it is there.
	rewritten := append(slices.Clone(pristine), []byte(discoveryDriftMarker)...)

	var driftErrs []error
	var driftMu sync.Mutex
	write := func(what string, bytes []byte) {
		if writeErr := os.WriteFile(frozen, bytes, 0o644); writeErr != nil {
			driftMu.Lock()
			driftErrs = append(driftErrs, fmt.Errorf("%s simple.go: %w", what, writeErr))
			driftMu.Unlock()
		}
	}
	prepareDone := make(chan preparedSession, 1)
	go func() {
		session, prepareErr := workspace.Prepare(context.Background(), gomutants.PrepareOptions{
			SkipVerify: true,
			Trace: func(event gomutants.PrepareEvent) {
				if event.Phase != gomutants.PreparePhaseDiscovery {
					return
				}
				if event.State == gomutants.PrepareEventStarted {
					write("rewriting", rewritten)
					return
				}
				write("restoring", pristine)
			},
		})
		prepareDone <- preparedSession{session: session, err: prepareErr}
	}()

	var prepared preparedSession
	select {
	case prepared = <-prepareDone:
	case <-time.After(prepareHoldBound):
		t.Fatalf("Prepare did not finish within %s", prepareHoldBound)
	}
	if prepared.session != nil {
		_ = prepared.session.Close()
	}
	driftMu.Lock()
	for _, writeErr := range driftErrs {
		t.Errorf("the write that was meant to move the tree: %v", writeErr)
	}
	driftMu.Unlock()

	var refused *gomutants.DriftError
	if !errors.As(prepared.err, &refused) {
		t.Fatalf("Prepare after a command rewrote a source file during discovery = %v,"+
			" want a *DriftError", prepared.err)
	}
	if refused.Stage != "discovery" {
		t.Errorf("Stage = %q, want %q: the gate saw a pristine tree, and it is what discovery"+
			" read that did not match", refused.Stage, "discovery")
	}
	if len(refused.Changes) != 1 {
		t.Fatalf("Changes = %+v, want the one file the command rewrote", refused.Changes)
	}
	change := refused.Changes[0]
	if change.Kind != gomutants.ChangeModified || change.Path != "simple.go" {
		t.Errorf("Changes[0] = %+v, want simple.go modified", change)
	}
	if change.BeforeSHA256 == "" || change.AfterSHA256 == "" || change.BeforeSHA256 == change.AfterSHA256 {
		t.Errorf("Changes[0] = %+v, want the frozen digest and the one discovery read", change)
	}
	if !strings.Contains(prepared.err.Error(), "simple.go") {
		t.Errorf("the message does not name the file:\n%v", prepared.err)
	}

	// The workspace is spent, as it is after every preparation that began and
	// failed: this one catalogued a program nobody has.
	if _, execErr := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "version"},
	}); !errors.Is(execErr, gomutants.ErrPrepareFailed) {
		t.Errorf("Exec after the refusal = %v, want ErrPrepareFailed", execErr)
	}
}

// TestCloseWaitsForAConcurrentPrepare is the lock the new rule must not lose.
//
// A preparation no longer holds the workspace against commands, and the obvious
// way to write that is to make it hold nothing — which would let `Close` remove
// the snapshot, the scratch directory and the toolchain's working tree out from
// under a preparation that is still compiling in them. So `Close` still takes
// the workspace exclusively and a preparation still holds it shared, and this
// is the test that says so rather than leaving it to be read out of a mutex.
//
// The ordering is proved by the preparation's own last phase rather than by a
// clock. The flag is written from inside the phase callback, which runs while
// the preparation holds the workspace, so a `Close` that returned without
// seeing it set is a `Close` that did not wait.
func TestCloseWaitsForAConcurrentPrepare(t *testing.T) {
	root := copyFixture(t, "simple")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	var lastPhase atomic.Bool
	held := holdPreparation(t, context.Background(), workspace,
		gomutants.PrepareOptions{SkipVerify: true},
		insidePhase(gomutants.PreparePhaseDiscovery, gomutants.PrepareEventStarted),
		func(event gomutants.PrepareEvent) {
			if event.Phase == gomutants.PreparePhaseBinaryBuild &&
				event.State == gomutants.PrepareEventFinished {
				lastPhase.Store(true)
			}
		})
	held.awaitHeld(t)

	closeDone := make(chan error, 1)
	closeSaw := make(chan bool, 1)
	go func() {
		closeErr := workspace.Close()
		closeSaw <- lastPhase.Load()
		closeDone <- closeErr
	}()
	select {
	case closeErr := <-closeDone:
		t.Fatalf("Close returned (%v) while a preparation was in flight, so it can remove the"+
			" tree the preparation is working in", closeErr)
	case <-time.After(execWaitProbe):
	}

	held.release()
	prepared := held.await(t)
	if prepared.err != nil {
		t.Fatalf("Prepare: %v", prepared.err)
	}
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatalf("closing the workspace: %v", closeErr)
		}
	case <-time.After(commandBound):
		t.Fatalf("Close did not return within %s of the preparation finishing", commandBound)
	}
	if !<-closeSaw {
		t.Error("Close returned before the preparation's last phase finished, so it did not wait" +
			" for the preparation at all")
	}
}

// TestSecondPrepareIsRefusedWithoutWaiting is what a preparation that no longer
// holds the workspace exclusively has to keep promising, and the one refusal
// this change makes *faster*.
//
// A workspace is prepared exactly once. That was enforced by the write lock —
// the second call waited out the whole first preparation, minutes of it, to be
// told it was never allowed — and it is now enforced where it belongs, by the
// state the first call claimed before it started. So the second caller is
// refused while the first is still held, which is the observable difference and
// the reason the bound below is short: a `Prepare` that queued behind the first
// would sit there until this test released it.
func TestSecondPrepareIsRefusedWithoutWaiting(t *testing.T) {
	root := copyFixture(t, "simple")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	held := holdPreparation(t, context.Background(), workspace,
		gomutants.PrepareOptions{SkipVerify: true},
		insidePhase(gomutants.PreparePhaseDiscovery, gomutants.PrepareEventStarted), nil)
	held.awaitHeld(t)

	second := make(chan preparedSession, 1)
	go func() {
		session, prepareErr := workspace.Prepare(context.Background(),
			gomutants.PrepareOptions{SkipVerify: true})
		second <- preparedSession{session: session, err: prepareErr}
	}()
	select {
	case refused := <-second:
		if refused.session != nil {
			_ = refused.session.Close()
		}
		if !errors.Is(refused.err, gomutants.ErrWorkspacePrepared) {
			t.Errorf("a second Prepare = %v, want ErrWorkspacePrepared", refused.err)
		}
		if errors.Is(refused.err, gomutants.ErrWorkspaceClosed) {
			t.Errorf("a second Prepare = %v, which also reads as a closed workspace", refused.err)
		}
	case <-time.After(secondPrepareBound):
		t.Fatalf("a second Prepare had not returned %s after the first was held: it is queued"+
			" behind a preparation it was never allowed to make", secondPrepareBound)
	}

	held.release()
	prepared := held.await(t)
	if prepared.err != nil {
		t.Fatalf("the first Prepare: %v", prepared.err)
	}
	if closeErr := prepared.session.Close(); closeErr != nil {
		t.Errorf("closing the session: %v", closeErr)
	}
}

// secondPrepareBound is how long the refusal above is given.
//
// It is a state check under a mutex and nothing else, so any bound at all
// proves the point — and the point is proved by the *first* preparation being
// held indefinitely, not by the second being quick. So the bound is a minute
// rather than a second: it is the slowest a scheduler could plausibly be and
// still be working, and it costs nothing when the engine is right.
const secondPrepareBound = 60 * time.Second
