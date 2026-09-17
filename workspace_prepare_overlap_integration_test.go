// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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

const prepareHoldBound = 3 * time.Minute

const commandBound = 2 * time.Minute

const overlapWorkers = 2

const overlapPause = 25 * time.Millisecond

const discoveryDriftMarker = "\n// a command wrote this during discovery\n"

type heldPreparation struct {
	held    chan struct{}
	release func()
	done    chan preparedSession
}

type preparedSession struct {
	session *gomutants.Session
	err     error
}

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

func insidePhase(phase gomutants.PreparePhase, state gomutants.PrepareEventState) func(gomutants.PrepareEvent) bool {
	return func(event gomutants.PrepareEvent) bool {
		return event.Phase == phase && event.State == state
	}
}

func TestWorkspaceExecOverlapsPreparationOutsideTheInstrumentationWindow(t *testing.T) {
	parent := t.TempDir()
	root := copyFixture(t, "killable")
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
					Env:  []string{"GOWORK=off"},
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

type commandSpan struct {
	seq   int64
	argv  []string
	start time.Time
	end   time.Time
}

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
	first, last := preparationSpan(t, events)
	t.Logf("%d of %d commands finished inside the preparation; the window was %s of a %s"+
		" preparation", overlapped, len(spans), windowEnd.Sub(windowStart), last.Sub(first))
}

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

func eventTime(t *testing.T, event trace.Event) time.Time {
	t.Helper()
	stamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		t.Fatalf("event %d has an unparsable timestamp %q: %v", event.Seq, event.Timestamp, err)
	}
	return stamp
}

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

type queuedCommand struct {
	result gomutants.CommandResult
	err    error
}

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
	if answered.result.Duration != 0 || len(answered.result.Output) != 0 || answered.result.ExitCode != 0 {
		t.Errorf("the command ran on the instrumented tree: %+v\n%s",
			answered.result, answered.result.Output)
	}
}

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

	if _, execErr := workspace.Exec(t.Context(), gomutants.Command{
		Argv: []string{"go", "version"},
	}); !errors.Is(execErr, gomutants.ErrPrepareFailed) {
		t.Errorf("Exec after the refusal = %v, want ErrPrepareFailed", execErr)
	}
}

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

const secondPrepareBound = 60 * time.Second
