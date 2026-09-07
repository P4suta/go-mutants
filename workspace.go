// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/P4suta/go-mutants/internal/glob"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/tempowner"
	"github.com/P4suta/go-mutants/trace"
)

const (
	commandTimeout      = 10 * time.Minute
	scratchPrefix       = "go-mutants-api-"
	workspaceExecPrefix = "exec-"
	reservedPrefix      = "GO_MUTANTS_"
	// reservedEnvironmentOwner is who an environment overlay's refusal names.
	// The engine sets these variables for itself in every child it starts, so
	// the owner is the tool rather than one of its calls.
	reservedEnvironmentOwner = "go-mutants"
	// The verdicts a workspace's recording ends with. A workspace has no exit
	// status and no policy gate, so the only two things that can become of one
	// are that it was closed and that closing it reported a failure.
	verdictClosed = "closed"
	verdictFailed = "failed"
)

var temporaryKeys = []string{"TMP", "TEMP", "TMPDIR"}

// temporaryPrefixes are the names Open creates directly under TempDirectory,
// and so the names its sweep is allowed to collect. Nothing else in that
// directory is go-mutants' to touch.
var temporaryPrefixes = []string{snapshot.DirPrefix, scratchPrefix}

// Workspace is a frozen disposable copy of one module. Its zero value is not
// usable. Open constructs one and Close releases it.
//
// Exec calls may run concurrently with one another and with Prepare: a
// preparation holds the tree against commands only while it is rewriting it,
// which is the window [Workspace.Exec] describes. Close waits for every call in
// flight, preparation included.
type Workspace struct {
	// mu is this workspace's lifetime. Exec and Prepare hold it *shared* for
	// the whole of their calls and Close holds it exclusively, which is what
	// makes Close the one call that may take the snapshot, the scratch
	// directory and the toolchain away: nothing else can be running while it
	// does.
	mu sync.RWMutex

	// tree is the snapshot's bytes. Exec holds it shared while its command
	// runs; Prepare holds it exclusively from the integrity gate through source
	// restoration, which is the one stretch of a preparation where the files on
	// disk are not the program anybody wrote.
	//
	// It is a second lock rather than a second use of mu because the two ask
	// different questions — "may this workspace still be used" and "is the tree
	// readable right now" — and one lock answering both is what used to make a
	// command wait out a preparation's compiles, its verification and its
	// builds for the sake of the seconds it spends instrumenting.
	//
	// The order is mu, then tree, then stateMu, and no call takes them the
	// other way round. Three locks in one order is the whole of the discipline.
	tree sync.RWMutex

	snapshot  *snapshot.Snapshot
	toolchain gocmd.Toolchain
	scratch   string
	env       []string
	closeDone chan struct{}
	closeErr  error

	// stateMu guards the four fields below, which are what has become of this
	// workspace. They have a lock of their own because Exec and Prepare read
	// and write them while holding only the shared half of mu — several of
	// those running at once is the point of that half — so mu is not what keeps
	// them consistent.
	stateMu sync.Mutex
	// closed is set by Close, and it wins over everything below: Close leaves a
	// workspace that was prepared and holds no session, and a consumer whose
	// workspace is gone has to be told that rather than sent to open another
	// one for a preparation that succeeded.
	closed bool
	// prepareStarted is claimed by the one Prepare a workspace gets. It is
	// claimed before the call reads anything and handed back only by the
	// refusals that happen before the tree is touched — an option this engine
	// does not accept — so a second Prepare is refused immediately rather than
	// queued behind minutes of a preparation it was never going to be allowed
	// to make.
	prepareStarted bool
	// prepareFailed is set by a preparation that began and did not finish. Such
	// a tree may still hold instrumented sources, so every command after one is
	// refused with [ErrPrepareFailed].
	prepareFailed bool
	// session is published by a preparation that succeeded, and dropped by
	// Close.
	session *Session

	// scratchOwner holds the scratch directory's lock and marker for as long as
	// the workspace is open. The snapshot carries its own.
	scratchOwner *tempowner.Owner
	// keepTemp is OpenOptions.KeepTemp, remembered because Close is where it
	// takes effect.
	keepTemp bool
	// swept is what Open collected before it copied anything.
	swept SweepResult
	// preserved names the directories a KeepTemp Close left behind, in path
	// order. It is written once, by Close.
	preserved []string
	// keptExec names the per-call scratch directories a KeepTemp Exec left
	// behind, for Close to mark and report.
	//
	// It has a lock of its own because the calls that write it hold the read
	// half of the workspace's — concurrent Execs are the point of that half —
	// and a reader cannot take the writer without deadlocking itself.
	keepMu   sync.Mutex
	keptExec []string

	// recording is where this workspace records, and the ring behind it when
	// nobody supplied a sink. Both are written once, by Open, and read without
	// the lock: a recorder that could be replaced would be a second recording
	// for a caller to reconcile.
	recording
}

// A recording is a workspace's recorder together with the ring it records into
// when [OpenOptions.Trace] was nil.
//
// The two are one value because they are one decision. A caller either supplies
// a sink — and owns everything about the recording, this workspace included —
// or supplies none, in which case the workspace keeps a bounded ring of its own
// and [Workspace.Recording] is how the caller reads it. There is no third case,
// and no state in which both are true.
type recording struct {
	recorder *trace.Recorder
	// ring is nil exactly when a caller supplied a sink, which is what makes
	// [Workspace.Recording] answer nil rather than a second, shorter copy of
	// what that sink already holds.
	ring *trace.MemorySink
}

// newRecording opens the recording of one workspace over root.
//
// A nil sink is not "no trace": it selects the bounded ring an untraced
// `go-mutants run` also records into, wrapped in [trace.Digested] because
// captured output grows with the run rather than with the ring. The run-start
// carries neither a run id nor an argument vector — a workspace is opened by a
// program rather than by a command line, and the schema refuses either field on
// a workspace recording.
//
// `tool_version` is [Version] verbatim, whatever it says: a tag, a
// pseudo-version, "(devel)" for a build the go command could not stamp — a test
// binary, or this project's own worktree — a "+dirty" suffix for an edited
// tree, or "unknown" when build information does not name this module at all.
// None of those is repaired here. A recording says what the build calls itself,
// and a diagnostic that substituted a nicer string would be describing a
// different program from the one a consumer's own recording names.
func newRecording(root string, sink trace.Sink) recording {
	var ring *trace.MemorySink
	if sink == nil {
		ring = trace.NewMemorySink(trace.DefaultRingCapacity)
		sink = trace.Digested(ring)
	}
	return recording{
		ring: ring,
		recorder: trace.New(sink, time.Now, trace.StartRecord{
			Kind:        trace.StartKindWorkspace,
			ToolVersion: Version(),
			PID:         os.Getpid(),
			Root:        root,
		}),
	}
}

// Recording is what this workspace recorded, oldest event first, or nil when
// [OpenOptions.Trace] supplied a sink of its own.
//
// It is safe to call at any point in a workspace's life, including while
// executions are running, and it stays readable after [Workspace.Close] — which
// is the moment it is meant for, because the recording's last event is the
// run-end Close wrote, so a caller reading it then has the whole account of the
// workspace. Before Close it is the account so far, with no run-end on the end
// of it.
//
// The ring is bounded. A long-lived workspace holds its last
// [trace.DefaultRingCapacity] events and the run-end says how many fell out, so
// a `TraceSeq` from early in a very long session can name an event that is no
// longer there. A caller that must keep every event supplies its own sink.
//
// Each call returns a fresh slice of cloned events: a recording is the account
// of what happened, not a value a caller may edit out from under the next one.
func (w *Workspace) Recording() []trace.Event {
	if w == nil || w.ring == nil {
		return nil
	}
	return w.ring.Events()
}

// A keptDirectory is one directory a [OpenOptions.KeepTemp] close left behind,
// with the artifact kind that says what it was. The kind is carried rather than
// derived from the path, because a name prefix is a convention and what a
// reader of a recording needs is the fact.
type keptDirectory struct {
	kind string
	path string
}

// Open locates the Go toolchain and copies root into a disposable snapshot.
// At most one OpenOptions value may be supplied.
func Open(ctx context.Context, root string, options ...OpenOptions) (*Workspace, error) {
	if len(options) > 1 {
		return nil, fmt.Errorf("gomutants: open: got %d option values, want at most one", len(options))
	}
	var opts OpenOptions
	if len(options) == 1 {
		opts = options[0]
	}
	// Before anything else that can fail, so that every refusal below is in the
	// recording rather than only in the returned error — including the ones a
	// caller reaching for a trace is likeliest to be investigating, a toolchain
	// that is not there and a tree that cannot be frozen.
	record := newRecording(root, opts.Trace)
	// A workspace that was never returned still ends its recording, so a
	// caller's sink sees a run-end rather than a stream that stops mid-sentence.
	// RunEnd is once-guarded by the recorder, so the one Close writes later on
	// the successful path cannot become a second last line.
	fail := func(err error) (*Workspace, error) {
		record.recorder.RunEnd(verdictFailed, 0, err)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return fail(fmt.Errorf("gomutants: open: %w", err))
	}
	exclude, err := compileSnapshotExclusions(opts.SnapshotExclude)
	if err != nil {
		return fail(err)
	}

	base := opts.Env
	if base == nil {
		base = os.Environ()
	} else {
		base = slices.Clone(base)
	}
	toolchain, err := gocmd.LocateContext(ctx, gocmd.Options{
		Explicit: opts.GoBinary,
		Env:      slices.Clone(base),
		Trace:    record.recorder,
	})
	if err != nil {
		return fail(fmt.Errorf("gomutants: open toolchain: %w", err))
	}

	// Before anything is copied, so that a machine holding the leftovers of a
	// killed run has the disk back before this one asks for hundreds of
	// megabytes of it. Concurrent Opens are safe in one process and across
	// several, because every live directory holds its own lock.
	swept := sweepTemporary(record.recorder, opts.TempDirectory)

	started := time.Now()
	snap, err := snapshot.Create(root, snapshot.Options{
		ReportDir:  opts.ReportDirectory,
		DestParent: opts.TempDirectory,
		Exclude:    exclude,
	})
	recordSnapshot(record.recorder, trace.SnapshotKindWorkspace, root, snap, time.Since(started), err)
	if err != nil {
		return fail(fmt.Errorf("gomutants: open snapshot: %w", err))
	}
	// Beside the snapshot rather than inside it: everything under the snapshot
	// root has to be a byte that came from the user's tree, and a test that
	// writes to TMPDIR would otherwise be indistinguishable from one that wrote
	// into the workspace.
	scratch, err := os.MkdirTemp(snap.Parent(), scratchPrefix)
	if err != nil {
		cleanupErr := snap.Cleanup()
		return fail(errors.Join(fmt.Errorf("gomutants: open scratch directory: %w", err), cleanupErr))
	}
	scratchOwner, err := tempowner.Claim(scratch, time.Now())
	if err != nil {
		return fail(errors.Join(
			fmt.Errorf("gomutants: open scratch directory: %w", err),
			os.RemoveAll(scratch), snap.Cleanup()))
	}

	return &Workspace{
		snapshot:     snap,
		toolchain:    toolchain,
		scratch:      scratch,
		env:          sanitiseEnvironment(base, scratch),
		closeDone:    make(chan struct{}),
		scratchOwner: scratchOwner,
		keepTemp:     opts.KeepTemp,
		swept:        swept,
		recording:    record,
	}, nil
}

// recordSnapshot files what one frozen tree turned out to be.
//
// A failure is recorded too, and with the same event: "the tree could not be
// frozen" is a fact about the snapshot, and a phase whose only account is a
// missing event is one a reader has to guess about. internal/engine records its
// own snapshots the same way, so one kind of line means one thing wherever a
// recording came from.
func recordSnapshot(
	recorder *trace.Recorder, kind, source string, snap *snapshot.Snapshot, took time.Duration, err error,
) {
	record := trace.SnapshotRecord{Kind: kind, Source: source, DurationMS: took.Milliseconds()}
	if err != nil {
		record.Error = err.Error()
	}
	if snap != nil {
		record.Dir = snap.Root
		record.Stable = snap.StableDir
		record.Files = len(snap.Manifest)
		record.Digest = snap.WorkspaceDigest
	}
	recorder.Snapshot(record)
}

func compileSnapshotExclusions(patterns []string) ([]glob.Pattern, error) {
	result := make([]glob.Pattern, 0, len(patterns))
	for _, pattern := range patterns {
		compiled, err := glob.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("gomutants: open snapshot exclusion %q: %w", pattern, err)
		}
		result = append(result, compiled)
	}
	return result, nil
}

// sweepTemporary collects the temporary directories of go-mutants runs that
// died before they could remove their own.
//
// A failure is carried in the result rather than returned: a workspace that
// could not tidy up after a previous run is still a perfectly good workspace,
// and refusing to open one would turn somebody else's leftover permission
// problem into this run's failure.
// What it collected also goes into the recording, which is where a workspace
// that paused to delete four gigabytes explains the pause. Housekeeping is
// reported there and nowhere else, and it can never change a result.
func sweepTemporary(recorder *trace.Recorder, parent string) SweepResult {
	if parent == "" {
		parent = os.TempDir()
	}
	swept, err := tempowner.Sweep(parent, temporaryPrefixes, time.Now())
	record := trace.SweepRecord{
		Parent:       parent,
		Removed:      swept.Removed,
		RemovedBytes: swept.RemovedBytes,
		Live:         swept.Live,
		Kept:         swept.Kept,
	}
	if err != nil {
		record.Error = err.Error()
	}
	recorder.Sweep(record)
	return SweepResult{
		Removed:      swept.Removed,
		RemovedBytes: swept.RemovedBytes,
		Live:         swept.Live,
		Kept:         swept.Kept,
		Err:          err,
	}
}

// Swept is what Open collected before it copied anything. It is the zero value
// for a workspace that was never opened.
func (w *Workspace) Swept() SweepResult {
	if w == nil {
		return SweepResult{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.swept
}

// Preserved names the directories a [OpenOptions.KeepTemp] workspace left on
// disk, in path order, and is empty before [Workspace.Close] and for every
// workspace that kept nothing.
//
// All of them: the snapshot, the probe tree, the workspace scratch, and the
// per-call scratch of every execution and probe pass.
//
// Only the first three carry a lock and a marker, and only they need one. A
// sweep looks at the direct children of a temporary parent, and collects one
// only if it wears a go-mutants name prefix; a per-call scratch is nested
// inside the workspace scratch, so it is never a candidate and survives for
// exactly as long as the marked parent above it does. A durable directory whose
// keep could not be recorded is not among these — it was removed instead,
// rather than left for the next run to collect as an orphan, and Close reported
// why.
//
// Nothing is kept by a process that dies. KeepTemp is decided at Close, so a
// workspace killed before it closes leaves directories that are still locked
// and unmarked, and the next run's sweep collects them once the lock is free.
// That is the intended behaviour and not a gap: the escape hatch is for looking
// at what a run produced, and a run that never finished has an owner who is
// still there to ask.
func (w *Workspace) Preserved() []string {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return slices.Clone(w.preserved)
}

func (w *Workspace) ToolchainVersion() string {
	if w == nil {
		return ""
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.toolchain.Version.Raw
}

// underTreeRead runs work while the snapshot's bytes are held against the one
// thing that rewrites them, which is a preparation's instrumentation window.
// Any number of readers hold it at once, so two commands, or a command and a
// preparation's discovery pass, are never each other's problem.
func (w *Workspace) underTreeRead(work func() error) error {
	w.tree.RLock()
	defer w.tree.RUnlock()
	return work()
}

// underTreeWrite runs work with the snapshot's bytes held exclusively: nothing
// else may read the tree until it returns.
//
// It is the instrumentation window, and it is a function rather than a pair of
// calls because every path out of that window — a validation that failed, a
// restoration that could not write, a drift check that refused — has to release
// it. A window left locked is a workspace no command and no Close can ever get
// back.
func (w *Workspace) underTreeWrite(work func() error) error {
	w.tree.Lock()
	defer w.tree.Unlock()
	return work()
}

// beginPrepare claims the workspace for the one preparation it gets.
//
// The claim is taken before anything is read, so that a second caller is
// refused now rather than after the first preparation's ten minutes, and it is
// handed back by [Workspace.abandonPrepare] for the refusals that spend
// nothing.
func (w *Workspace) beginPrepare() error {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.closed {
		return fmt.Errorf("gomutants: prepare: %w", ErrWorkspaceClosed)
	}
	if w.prepareStarted {
		return fmt.Errorf("gomutants: prepare: %w", ErrWorkspacePrepared)
	}
	w.prepareStarted = true
	return nil
}

// abandonPrepare hands the claim back, for a preparation refused before it read
// or wrote anything: a cancelled context, an option this engine does not
// accept. Charging a caller a fresh workspace and a fresh snapshot for a typo
// in a line number would be charging it for damage nothing did.
func (w *Workspace) abandonPrepare() {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.prepareStarted = false
}

// prepareDidFail records a preparation that began and did not finish, which is
// what refuses every later command.
func (w *Workspace) prepareDidFail() {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.prepareFailed = true
}

// publishSession hands the finished session to the workspace, which is the
// last thing a successful preparation does.
func (w *Workspace) publishSession(session *Session) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.session = session
}

// execAllowed is the lifecycle's answer to one command, or nil when the command
// is the workspace's own business to judge.
//
// Closed is asked first, and the order is a contract rather than a habit: a
// workspace that was closed after a preparation failed carries both facts, and
// the consumer holding it has to be told that its workspace is gone rather than
// sent to open another one. TestClosedWorkspaceErrorsAreSentinels pins it.
func (w *Workspace) execAllowed() error {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.closed {
		return fmt.Errorf("gomutants: exec: %w", ErrWorkspaceClosed)
	}
	if w.prepareFailed {
		return fmt.Errorf("gomutants: exec: %w", ErrPrepareFailed)
	}
	return nil
}

// claimClose marks the workspace closed and hands over the session to close
// with it, or reports that somebody else claimed it first.
func (w *Workspace) claimClose() (*Session, bool) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.closed {
		return nil, false
	}
	w.closed = true
	session := w.session
	w.session = nil
	return session, true
}

// Exec runs command against the frozen snapshot: a build, a vet, a `go list`, a
// baseline of the caller's own.
//
// Its relationship with Prepare is three rules, one per state of the
// preparation.
//
//   - While a Prepare is *in flight* a command runs beside it, and waits only
//     for the *instrumentation window*: the stretch between preparation's
//     integrity gate and the end of `main_restoration`, during which the
//     sources on disk have been rewritten and are not the program anybody
//     wrote. A command issued inside the window blocks until the window ends
//     and then runs against the restored tree; a command already running when
//     a preparation reaches the gate makes the preparation wait for it, so a
//     long baseline delays the window and can never corrupt it. Everything
//     else a preparation does — discovery, the probe copy, verification, the
//     two builds — reads the same frozen bytes a command does, so the two
//     overlap. That is minutes of a preparation a consumer used to open a
//     second workspace to make use of.
//   - After a Prepare that *succeeded* a command runs. A returned session is
//     the proof that main_restoration put the pristine sources back and that
//     the tree re-digested as the snapshot Open froze; the instrumented
//     sources live only in the overlay manifest the session owns, which
//     nothing but Session.Exec and Session.Probe put in a child's environment.
//     So a consumer may run `go vet`, `go build` or a control of its own
//     beside a live session rather than opening a second workspace for them.
//   - After a Prepare that *failed* a command is refused with
//     [ErrPrepareFailed]. A preparation that stopped part-way promises nothing
//     about the tree, which may still hold instrumented sources.
//
// A closed workspace is [ErrWorkspaceClosed] whatever its preparation did, and
// a closed *session* changes nothing: it releases the binaries and the probe
// tree, not the snapshot, so commands go on running against the same tree.
//
// Nothing stops a command run after a successful Prepare from writing into the
// tree, and nothing invalidates the session when one does: the overlay names
// the frozen sources, the binaries are already built, and executions go on
// answering. What such a write changes is the tree every later target runs in,
// and [Session.Changes] reports it against the manifest preparation captured —
// the same call, and the same answer, as for a write a target made. What it
// does *not* change is [Catalog.WorkspaceDigest] and
// [Catalog.PreparedDigest]: both were frozen by Prepare and neither moves for
// anything a caller does afterwards. So evidence keyed on them — which is what
// those digests are for — must not be carried across a write a consumer made,
// and [Session.Changes] is the only thing that can tell it there was one. A
// consumer that writes into the tree owns that.
//
// Fuzzing is the write most easily made by accident. [Session.Exec] and
// [Session.Control] run a fuzz target in a copy of the tree and reserve
// `-test.fuzzcachedir` so that neither the snapshot nor another call's corpus
// can be written; a `go test -fuzz=…` run through this call has neither, so the
// go command writes any crasher it finds into `testdata/fuzz/` *in the frozen
// tree*, where it becomes a seed for every later target and a change
// [Session.Changes] reports.
//
// Before Prepare the rule is stricter, because the tree is about to become the
// source of a mutation catalogue: a command that changed it is refused by
// Prepare's integrity gate. A command that changes it *during* a preparation is
// refused by the same checks, which now simply notice later — the gate, or the
// comparison between what discovery read and the frozen manifest, or the drift
// check after restoration or verification. All of them fail the preparation,
// and none of them will catalogue mutants of bytes nobody has.
//
// Exec is safe to call concurrently. Every call receives a private temporary
// directory, so commands cannot observe one another through TMP, TEMP or
// TMPDIR. They deliberately share the frozen working tree.
//
// One thing it may not be called from: a [PrepareOptions.Trace] callback. That
// callback runs on the preparation's own goroutine. Inside the instrumentation
// window the reason is immediate — the goroutine holds the tree exclusively and
// the command would wait for it — and outside the window it is worse for being
// less obvious: the command runs, until the day a [Workspace.Close] queues for
// the workspace. Go's RWMutex hands out no more read locks once a writer is
// waiting, so the command then waits for the Close, the Close waits for the
// preparation, and the preparation waits for the callback.
func (w *Workspace) Exec(ctx context.Context, command Command) (CommandResult, error) {
	if w == nil {
		return CommandResult{}, errors.New("gomutants: exec: nil workspace")
	}
	// Shared for the whole call, which is what makes Close — the one caller
	// that takes it exclusively — wait for this command rather than remove the
	// tree it is running in.
	w.mu.RLock()
	defer w.mu.RUnlock()
	if err := w.execAllowed(); err != nil {
		return CommandResult{}, err
	}
	scratch, err := os.MkdirTemp(w.scratch, workspaceExecPrefix)
	if err != nil {
		return CommandResult{}, fmt.Errorf("gomutants: exec scratch: %w", err)
	}
	kept := false
	defer func() {
		if !kept {
			_ = os.RemoveAll(scratch)
		}
	}()
	// The tree only while the child runs. A command holds it against a
	// preparation's instrumentation window and against nothing else, so a
	// preparation waits for a running command and a command waits for a running
	// window.
	var result CommandResult
	err = w.underTreeRead(func() error {
		// Asked again, now that the tree is held. The first answer was given
		// before this call queued behind an instrumentation window, and a
		// window can *fail*: the unlock that wakes this command is then the
		// unlock of a preparation that gave up with the instrumented sources
		// still in the tree. A command that ran on those would exit zero and
		// answer about a program nobody wrote, which is the one outcome this
		// whole feature must not produce.
		if allowedErr := w.execAllowed(); allowedErr != nil {
			return allowedErr
		}
		var runErr error
		result, runErr = w.runCommand(ctx, command, sanitiseEnvironment(w.env, scratch), trace.ExecKindWorkspaceExec)
		return runErr
	})
	if w.keepTemp {
		w.keepExecScratch(scratch)
		kept = true
	}
	return result, err
}

// keepExecScratch keeps one per-call scratch directory a [OpenOptions.KeepTemp]
// workspace is preserving: it records the artifact and remembers the path for
// [Workspace.Preserved].
//
// The artifact is recorded here, the moment the directory is kept, and never
// again at Close. Both halves matter. A reader finds the directory beside the
// execution it belonged to rather than in a block of housekeeping thousands of
// events later — and a session that keeps ten thousand executions cannot push
// its own run-start, its preparation timeline and every one of its attempts out
// of a bounded ring at the very end, which is what a Close that recorded them
// all in one burst would do.
//
// Nothing is written *into* the directory. It carries no lock and no marker, on
// purpose: it is the child's TMPDIR, and two files of go-mutants' own in the
// one place the engine promises is the test's own would be two files in the
// tree a developer kept the directory in order to inspect. It needs none — see
// [Workspace.Preserved] for why the sweep can never reach it.
func (w *Workspace) keepExecScratch(scratch string) {
	w.recorder.Artifact(trace.ArtifactKeptExecScratch, scratch)
	w.keepMu.Lock()
	defer w.keepMu.Unlock()
	w.keptExec = append(w.keptExec, scratch)
}

// keptExecScratch is what [Workspace.Exec] left behind.
func (w *Workspace) keptExecScratch() []string {
	w.keepMu.Lock()
	defer w.keepMu.Unlock()
	return slices.Clone(w.keptExec)
}

// runCommand runs one command against the frozen snapshot, recording it under
// the kind the caller says it is. The kind is a parameter because it is the one
// thing about a command this function cannot know: an embedder's baseline and
// the verification run inside Prepare are the same syscall from here, and an
// unlabelled command is a recording nobody can read.
func (w *Workspace) runCommand(
	ctx context.Context, command Command, base []string, kind string,
) (CommandResult, error) {
	if len(command.Argv) == 0 || strings.TrimSpace(command.Argv[0]) == "" {
		return CommandResult{}, errors.New("gomutants: exec: command has no executable")
	}
	if command.Timeout < 0 {
		return CommandResult{}, errors.New("gomutants: exec: timeout is negative")
	}
	dir, err := moduleDirectory(w.snapshot.Root, command.Dir)
	if err != nil {
		return CommandResult{}, fmt.Errorf("gomutants: exec directory: %w", err)
	}
	env, err := overlayEnvironment(base, command.Env)
	if err != nil {
		return CommandResult{}, fmt.Errorf("gomutants: exec environment: %w", err)
	}
	argv := slices.Clone(command.Argv)
	if argv[0] == "go" {
		argv[0] = w.toolchain.GoBin
	}
	timeout := command.Timeout
	if timeout == 0 {
		timeout = commandTimeout
	}
	run := runner.Run(ctx, runner.Spec{
		Argv:        argv,
		Dir:         dir,
		Env:         env,
		Timeout:     timeout,
		OutputLimit: command.OutputLimit,
		Trace:       w.recorder,
		Kind:        kind,
	})
	result := CommandResult{
		ExitCode:   run.ExitCode,
		TimedOut:   run.TimedOut,
		Duration:   run.Duration,
		Output:     slices.Clone(run.Output),
		Truncated:  run.Truncated,
		TotalBytes: run.OutputBytes,
		TraceSeq:   run.TraceSeq,
	}
	if run.Err != nil {
		return result, fmt.Errorf("gomutants: exec process: %w", run.Err)
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("gomutants: exec: %w", err)
	}
	return result, nil
}

// Close stops accepting work and removes the session scratch directory and
// snapshot. It is idempotent.
//
// It waits for every call in flight. [Workspace.Exec] and [Workspace.Prepare]
// hold the workspace shared for the whole of their calls and this one takes it
// exclusively, so a command that is running and a preparation that is still
// discovering, instrumenting or compiling both finish first: a workspace that
// removed its snapshot underneath one of them would be a use-after-free with a
// friendlier name. An in-flight [Session.Exec] is waited for by the session's
// own Close, which this one calls.
func (w *Workspace) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if w.closeDone == nil {
		w.closeDone = make(chan struct{})
	}
	done := w.closeDone
	session, first := w.claimClose()
	if !first {
		w.mu.Unlock()
		<-done
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.closeErr
	}
	snap := w.snapshot
	scratch := w.scratch
	scratchOwner := w.scratchOwner
	keep := w.keepTemp
	w.snapshot = nil
	w.scratch = ""
	w.scratchOwner = nil
	w.mu.Unlock()

	var closeErr error
	// The durable directories, which are the only ones Close records: the
	// per-call scratch of every execution was recorded where it was kept, so
	// that a reader finds it beside the execution and a long session cannot
	// evict its own timeline by reporting ten thousand of them at once.
	var durable []keptDirectory
	if session != nil {
		closeErr = session.Close()
		durable = append(durable, session.preservedDirs()...)
	}
	if scratch != "" {
		// The lock is dropped before a removal, because on Windows the open
		// handle inside the directory is what would refuse it.
		remove := func() error { return errors.Join(scratchOwner.Release(), os.RemoveAll(scratch)) }
		preserve, err := keepOrRemove(keep, scratchOwner.Keep, remove)
		closeErr = errors.Join(closeErr, err)
		if preserve {
			durable = append(durable, keptDirectory{kind: trace.ArtifactKeptScratch, path: scratch})
		}
	}
	if snap != nil {
		preserve, err := keepOrRemove(keep, snap.Keep, snap.Cleanup)
		closeErr = errors.Join(closeErr, err)
		if preserve {
			durable = append(durable, keptDirectory{kind: trace.ArtifactKeptSnapshot, path: snap.Dir()})
		}
	}
	slices.SortFunc(durable, func(a, b keptDirectory) int { return strings.Compare(a.path, b.path) })
	preserved := make([]string, 0, len(durable))
	for _, directory := range durable {
		preserved = append(preserved, directory.path)
		w.recorder.Artifact(directory.kind, directory.path)
	}
	// The per-call directories are named as well as the durable ones, because
	// Preserved is what a caller looks at to find them; they are simply not
	// recorded twice.
	preserved = append(preserved, w.keptExecScratch()...)
	if session != nil {
		preserved = append(preserved, session.keptScratchDirs()...)
	}
	slices.Sort(preserved)
	var result error
	if closeErr != nil {
		result = fmt.Errorf("gomutants: close workspace: %w", closeErr)
	}
	// Last, and after the artifacts, because a recording has one final line and
	// a reader who found it has read the whole workspace. RunEnd is
	// once-guarded, so the second Close of an idempotent pair writes nothing.
	if result != nil {
		w.recorder.RunEnd(verdictFailed, 0, result)
	} else {
		w.recorder.RunEnd(verdictClosed, 0, nil)
	}
	w.mu.Lock()
	w.preserved = preserved
	w.closeErr = result
	close(done)
	w.mu.Unlock()
	return result
}

// keepOrRemove settles a temporary directory on the way out: kept when the
// caller asked for that and the keep was recorded, removed otherwise. A keep
// the marker did not record is not a keep — the next run's sweep would collect
// the directory as an orphan — so it is removed now, its error reported, and
// it is not among the directories the caller says it preserved.
func keepOrRemove(keep bool, record, remove func() error) (kept bool, err error) {
	if !keep {
		return false, remove()
	}
	if err = record(); err != nil {
		return false, errors.Join(err, remove())
	}
	return true, nil
}

func moduleDirectory(root, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" {
		relative = "."
	}
	native := filepath.FromSlash(relative)
	if filepath.IsAbs(native) || filepath.VolumeName(native) != "" {
		return "", fmt.Errorf("%q is absolute; directories must be module-relative", relative)
	}
	clean := filepath.Clean(native)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q escapes the module", relative)
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q cannot be resolved inside the module", relative)
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", fmt.Errorf("cannot read %q: %w", relative, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", relative)
	}
	return full, nil
}

func sanitiseEnvironment(source []string, scratch string) []string {
	out := make([]string, 0, len(source)+len(temporaryKeys))
	for _, entry := range source {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || reservedEnvironment(key) || temporaryEnvironment(key) {
			continue
		}
		out = append(out, entry)
	}
	for _, key := range temporaryKeys {
		out = append(out, key+"="+scratch)
	}
	return out
}

func overlayEnvironment(base, overlay []string) ([]string, error) {
	out := slices.Clone(base)
	for _, entry := range overlay {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%q is not KEY=VALUE", entry)
		}
		if reservedEnvironment(key) || temporaryEnvironment(key) {
			return nil, &ReservedError{Variable: key, Owner: reservedEnvironmentOwner}
		}
		replaced := false
		for i, existing := range out {
			existingKey, _, valid := strings.Cut(existing, "=")
			if valid && environmentKeyEqual(existingKey, key) {
				out[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, entry)
		}
	}
	return out, nil
}

func reservedEnvironment(key string) bool {
	return strings.HasPrefix(strings.ToUpper(key), reservedPrefix)
}

func temporaryEnvironment(key string) bool {
	for _, reserved := range temporaryKeys {
		if strings.EqualFold(key, reserved) {
			return true
		}
	}
	return false
}

func environmentKeyEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func prependEnvironmentPath(env []string, directory string) []string {
	if directory == "" || directory == "." {
		return env
	}
	for i, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !environmentKeyEqual(key, "PATH") {
			continue
		}
		if value == directory || strings.HasPrefix(value, directory+string(filepath.ListSeparator)) {
			return env
		}
		env[i] = key + "=" + directory + string(filepath.ListSeparator) + value
		return env
	}
	return append(env, "PATH="+directory)
}
