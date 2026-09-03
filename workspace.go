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

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/tempowner"
)

const (
	commandTimeout = 10 * time.Minute
	scratchPrefix  = "go-mutants-api-"
	reservedPrefix = "GO_MUTANTS_"
)

var temporaryKeys = []string{"TMP", "TEMP", "TMPDIR"}

// temporaryPrefixes are the names Open creates directly under TempDirectory,
// and so the names its sweep is allowed to collect. Nothing else in that
// directory is go-mutants' to touch.
var temporaryPrefixes = []string{snapshot.DirPrefix, scratchPrefix}

// Workspace is a frozen disposable copy of one module. Its zero value is not
// usable. Open constructs one and Close releases it.
type Workspace struct {
	mu        sync.Mutex
	snapshot  *snapshot.Snapshot
	toolchain gocmd.Toolchain
	scratch   string
	env       []string
	closed    bool
	closeDone chan struct{}
	closeErr  error
	prepared  bool
	session   *Session

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
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("gomutants: open: %w", err)
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
	})
	if err != nil {
		return nil, fmt.Errorf("gomutants: open toolchain: %w", err)
	}

	// Before anything is copied, so that a machine holding the leftovers of a
	// killed run has the disk back before this one asks for hundreds of
	// megabytes of it. Concurrent Opens are safe in one process and across
	// several, because every live directory holds its own lock.
	swept := sweepTemporary(opts.TempDirectory)

	snap, err := snapshot.Create(root, snapshot.Options{
		ReportDir:  opts.ReportDirectory,
		DestParent: opts.TempDirectory,
	})
	if err != nil {
		return nil, fmt.Errorf("gomutants: open snapshot: %w", err)
	}
	// Beside the snapshot rather than inside it: everything under the snapshot
	// root has to be a byte that came from the user's tree, and a test that
	// writes to TMPDIR would otherwise be indistinguishable from one that wrote
	// into the workspace.
	scratch, err := os.MkdirTemp(snap.Parent(), scratchPrefix)
	if err != nil {
		cleanupErr := snap.Cleanup()
		return nil, errors.Join(fmt.Errorf("gomutants: open scratch directory: %w", err), cleanupErr)
	}
	scratchOwner, err := tempowner.Claim(scratch, time.Now())
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("gomutants: open scratch directory: %w", err),
			os.RemoveAll(scratch), snap.Cleanup())
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
	}, nil
}

// sweepTemporary collects the temporary directories of go-mutants runs that
// died before they could remove their own.
//
// A failure is carried in the result rather than returned: a workspace that
// could not tidy up after a previous run is still a perfectly good workspace,
// and refusing to open one would turn somebody else's leftover permission
// problem into this run's failure.
func sweepTemporary(parent string) SweepResult {
	if parent == "" {
		parent = os.TempDir()
	}
	swept, err := tempowner.Sweep(parent, temporaryPrefixes, time.Now())
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
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.swept
}

// Preserved names the directories a [OpenOptions.KeepTemp] workspace left on
// disk, in path order, and is empty before [Workspace.Close] and for every
// workspace that kept nothing.
func (w *Workspace) Preserved() []string {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.preserved)
}

// Exec runs command against the frozen snapshot. It is available before
// Prepare; after instrumentation begins, commands belong to Session targets.
func (w *Workspace) Exec(ctx context.Context, command Command) (CommandResult, error) {
	if w == nil {
		return CommandResult{}, errors.New("gomutants: exec: nil workspace")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return CommandResult{}, errors.New("gomutants: exec: workspace is closed")
	}
	if w.prepared {
		return CommandResult{}, errors.New("gomutants: exec: workspace is already prepared; execute test targets through its session")
	}
	return w.runCommand(ctx, command, w.env)
}

func (w *Workspace) runCommand(ctx context.Context, command Command, base []string) (CommandResult, error) {
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
	})
	result := CommandResult{
		ExitCode: run.ExitCode,
		TimedOut: run.TimedOut,
		Duration: run.Duration,
		Output:   slices.Clone(run.Output),
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
// snapshot. It is idempotent and waits for in-flight Session.Exec calls.
func (w *Workspace) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if w.closeDone == nil {
		w.closeDone = make(chan struct{})
	}
	done := w.closeDone
	if w.closed {
		w.mu.Unlock()
		<-done
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.closeErr
	}
	w.closed = true
	session := w.session
	snap := w.snapshot
	scratch := w.scratch
	scratchOwner := w.scratchOwner
	keep := w.keepTemp
	w.session = nil
	w.snapshot = nil
	w.scratch = ""
	w.scratchOwner = nil
	w.mu.Unlock()

	var closeErr error
	var preserved []string
	if session != nil {
		closeErr = session.Close()
		preserved = append(preserved, session.preservedDirs()...)
	}
	if scratch != "" {
		switch {
		case keep:
			closeErr = errors.Join(closeErr, scratchOwner.Keep())
			preserved = append(preserved, scratch)
		default:
			// The lock is dropped before the removal, because on Windows the
			// open handle inside the directory is what would refuse it.
			closeErr = errors.Join(closeErr, scratchOwner.Release(), os.RemoveAll(scratch))
		}
	}
	if snap != nil {
		switch {
		case keep:
			closeErr = errors.Join(closeErr, snap.Keep())
			preserved = append(preserved, snap.Dir())
		default:
			closeErr = errors.Join(closeErr, snap.Cleanup())
		}
	}
	slices.Sort(preserved)
	var result error
	if closeErr != nil {
		result = fmt.Errorf("gomutants: close workspace: %w", closeErr)
	}
	w.mu.Lock()
	w.preserved = preserved
	w.closeErr = result
	close(done)
	w.mu.Unlock()
	return result
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
			return nil, fmt.Errorf("%s is reserved by go-mutants", key)
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
