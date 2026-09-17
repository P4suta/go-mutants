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
	commandTimeout           = 10 * time.Minute
	scratchPrefix            = "go-mutants-api-"
	workspaceExecPrefix      = "exec-"
	reservedPrefix           = "GO_MUTANTS_"
	execCall                 = "exec"
	reservedEnvironmentOwner = "go-mutants"
	verdictClosed            = "closed"
	verdictFailed            = "failed"
)

var temporaryKeys = []string{"TMP", "TEMP", "TMPDIR"}

var temporaryPrefixes = []string{snapshot.DirPrefix, scratchPrefix}

type Workspace struct {
	mu sync.RWMutex

	tree sync.RWMutex

	snapshot  *snapshot.Snapshot
	toolchain gocmd.Toolchain
	scratch   string
	env       []string
	closeDone chan struct{}
	closeErr  error

	stateMu        sync.Mutex
	closed         bool
	prepareStarted bool
	prepareFailed  bool
	session        *Session

	scratchOwner *tempowner.Owner
	keepTemp     bool
	swept        SweepResult
	preserved    []string
	keepMu       sync.Mutex
	keptExec     []string

	moduleMu sync.Mutex
	modules  map[string]*moduleAnswer

	recording
}

type recording struct {
	recorder *trace.Recorder
	ring     *trace.MemorySink
}

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

func (w *Workspace) Recording() []trace.Event {
	if w == nil || w.ring == nil {
		return nil
	}
	return w.ring.Events()
}

type keptDirectory struct {
	kind string
	path string
}

func Open(ctx context.Context, root string, options ...OpenOptions) (*Workspace, error) {
	if len(options) > 1 {
		return nil, fmt.Errorf("gomutants: open: got %d option values, want at most one", len(options))
	}
	var opts OpenOptions
	if len(options) == 1 {
		opts = options[0]
	}
	record := newRecording(root, opts.Trace)
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

func (w *Workspace) Swept() SweepResult {
	if w == nil {
		return SweepResult{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.swept
}

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

func (w *Workspace) underTreeRead(work func() error) error {
	w.tree.RLock()
	defer w.tree.RUnlock()
	return work()
}

func (w *Workspace) underTreeWrite(work func() error) error {
	w.tree.Lock()
	defer w.tree.Unlock()
	return work()
}

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

func (w *Workspace) abandonPrepare() {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.prepareStarted = false
}

func (w *Workspace) prepareDidFail() {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.prepareFailed = true
}

func (w *Workspace) publishSession(session *Session) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.session = session
}

func (w *Workspace) allowed(call string) error {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.closed {
		return fmt.Errorf("gomutants: %s: %w", call, ErrWorkspaceClosed)
	}
	if w.prepareFailed {
		return fmt.Errorf("gomutants: %s: %w", call, ErrPrepareFailed)
	}
	return nil
}

func (w *Workspace) execAllowed() error { return w.allowed(execCall) }

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

func (w *Workspace) Exec(ctx context.Context, command Command) (CommandResult, error) {
	if w == nil {
		return CommandResult{}, errors.New("gomutants: exec: nil workspace")
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if err := w.execAllowed(); err != nil {
		return CommandResult{}, err
	}
	defer w.forgetModules()
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
	var result CommandResult
	err = w.underTreeRead(func() error {
		if allowedErr := w.execAllowed(); allowedErr != nil {
			return allowedErr
		}
		run, runErr := w.runCommand(ctx, command, sanitiseEnvironment(w.env, scratch),
			commandLabel{kind: trace.ExecKindWorkspaceExec})
		result = run.CommandResult
		return runErr
	})
	if w.keepTemp {
		w.keepExecScratch(scratch)
		kept = true
	}
	return result, err
}

func (w *Workspace) keepExecScratch(scratch string) {
	w.recorder.Artifact(trace.ArtifactKeptExecScratch, scratch)
	w.keepMu.Lock()
	defer w.keepMu.Unlock()
	w.keptExec = append(w.keptExec, scratch)
}

func (w *Workspace) keptExecScratch() []string {
	w.keepMu.Lock()
	defer w.keepMu.Unlock()
	return slices.Clone(w.keptExec)
}

type commandLabel struct {
	kind        string
	subject     string
	splitStdout bool
}

type commandRun struct {
	CommandResult
	stdout []byte
}

func (w *Workspace) runCommand(
	ctx context.Context, command Command, base []string, label commandLabel,
) (commandRun, error) {
	if len(command.Argv) == 0 || strings.TrimSpace(command.Argv[0]) == "" {
		return commandRun{}, errors.New("gomutants: exec: command has no executable")
	}
	if command.Timeout < 0 {
		return commandRun{}, errors.New("gomutants: exec: timeout is negative")
	}
	if command.MemoryLimit < 0 {
		return commandRun{}, errors.New("gomutants: exec: memory limit is negative")
	}
	dir, err := moduleDirectory(w.snapshot.Root, command.Dir)
	if err != nil {
		return commandRun{}, fmt.Errorf("gomutants: exec directory: %w", err)
	}
	env, err := overlayEnvironment(base, command.Env)
	if err != nil {
		return commandRun{}, fmt.Errorf("gomutants: exec environment: %w", err)
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
		Argv:           argv,
		Dir:            dir,
		Env:            env,
		Timeout:        timeout,
		MemoryLimit:    command.MemoryLimit,
		OutputLimit:    command.OutputLimit,
		SeparateStdout: label.splitStdout,
		Trace:          w.recorder,
		Kind:           label.kind,
		Subject:        label.subject,
	})
	result := commandRun{
		CommandResult: CommandResult{
			ExitCode:       run.ExitCode,
			TimedOut:       run.TimedOut,
			Duration:       run.Duration,
			Output:         slices.Clone(run.Output),
			Truncated:      run.Truncated,
			TotalBytes:     run.OutputBytes,
			PeakMemory:     run.PeakMemory,
			MemoryExceeded: run.MemoryExceeded,
			TraceSeq:       run.TraceSeq,
		},
		stdout: slices.Clone(run.Stdout),
	}
	if run.Err != nil {
		return result, fmt.Errorf("gomutants: exec process: %w", run.Err)
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("gomutants: exec: %w", err)
	}
	return result, nil
}

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
	w.forgetModules()
	w.mu.Unlock()

	var closeErr error
	var durable []keptDirectory
	if session != nil {
		closeErr = session.Close()
		durable = append(durable, session.preservedDirs()...)
	}
	if scratch != "" {
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
	preserved = append(preserved, w.keptExecScratch()...)
	if session != nil {
		preserved = append(preserved, session.keptScratchDirs()...)
	}
	slices.Sort(preserved)
	var result error
	if closeErr != nil {
		result = fmt.Errorf("gomutants: close workspace: %w", closeErr)
	}
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
