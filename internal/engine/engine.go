// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
)

// Fixed budgets and the timeout derivation constants.
//
// Each is a decision rather than a technical limit, and each is stated once
// here so that the engine, the help text, and the documentation cannot drift.
const (
	// BaselineCap bounds one baseline command — the build, or one measured
	// test run. It is fixed and generous on purpose: it exists before there is
	// any measurement to derive a budget from, so its only job is to stop a
	// hung toolchain from hanging the run forever. A project whose tests
	// legitimately take longer than this has told go-mutants nothing it can
	// work with anyway.
	BaselineCap = 10 * time.Minute

	// MinDerivedTimeout is the floor under a derived per-mutant timeout. Five
	// times a very fast suite is still a very small number, and a mutant that
	// makes a fast test slow — an infinite loop, a retry that never gives up —
	// is exactly the mutant a timeout is meant to catch rather than to
	// misreport as scheduling noise.
	MinDerivedTimeout = 10 * time.Second

	// TimeoutFactor multiplies the slowest baseline run.
	TimeoutFactor = 5

	// scratchPrefix names the per-run scratch directory. It sits beside the
	// snapshot rather than inside it, so that a test writing to the temporary
	// directory neither shows up as workspace drift nor is deleted out from
	// under itself when the snapshot is cleaned up.
	scratchPrefix = "go-mutants-tmp-"

	// envPrefix is the variable prefix a child process never inherits.
	// Activation is the engine's to set; a GO_MUTANTS_ACTIVE left in a user's
	// shell would otherwise silently turn a mutant on inside the baseline.
	envPrefix = "GO_MUTANTS_"

	// digestDisplay is how many hex characters of the workspace digest are
	// shown. It is a display convenience only; reports carry the full digest.
	digestDisplay = 16
)

// tempKeys are the environment variables redirected at the scratch directory.
// All three are set on every platform: TMPDIR is the POSIX spelling, TMP and
// TEMP the Windows ones, and a cross-compiling toolchain or a test helper may
// read any of them.
var tempKeys = []string{"TMP", "TEMP", "TMPDIR"}

// Options is everything [Run] needs. Every field is supplied by the caller;
// the engine reads no global state beyond the process environment it hands to
// its children.
type Options struct {
	// Config is the fully resolved configuration: defaults, file, and flags
	// already merged and validated.
	Config config.Config

	// WorkspaceRoot is the directory to mutate, normally the current working
	// directory. It is resolved to an absolute path and is only ever read.
	WorkspaceRoot string

	// TestArgv overrides `test.command` with the argv the user wrote after
	// `--`. It is the single authoritative override: the command line does not
	// also push it through a config.Overlay, because two paths for one value
	// is one path too many. Empty means "use the configured command".
	TestArgv []string

	// Events receives every [Event]. The engine closes it on return. A nil
	// channel publishes nothing and is not closed; see the package
	// documentation for the draining contract.
	Events chan<- Event
}

// RunOutcome is everything one run learned. It is returned even when [Run]
// fails, filled in as far as the run got, so that a caller can report what was
// established before the failure.
type RunOutcome struct {
	// RunID is the identifier published in [RunPlanned].
	RunID string
	// Status is how the run ended.
	Status Status
	// Started is when [Run] was entered, in the local clock.
	Started time.Time
	// Duration is the wall-clock time the whole run took.
	Duration time.Duration

	// WorkspaceRoot is the absolute path of the tree that was copied.
	WorkspaceRoot string
	// Workers is the resolved worker count.
	Workers int
	// Toolchain is the Go toolchain the run used.
	Toolchain gocmd.Toolchain

	// SnapshotRoot is where the disposable copy lived. It is already removed
	// by the time Run returns; it is retained for diagnostics, and for the
	// tests that prove the cleanup happened.
	SnapshotRoot string
	// SnapshotFiles is how many regular files the snapshot held.
	SnapshotFiles int
	// WorkspaceDigest is the frozen digest of the snapshot manifest.
	WorkspaceDigest string

	// TestCommand is the argv the baseline was measured with, as the user
	// wrote it — before the toolchain path was substituted for a bare `go`.
	// It is the spelling that belongs in a report and in a message.
	TestCommand []string
	// BaselineRuns holds every baseline observation, in measurement order.
	BaselineRuns []time.Duration
	// AverageBaseline and SlowestBaseline summarise BaselineRuns.
	AverageBaseline time.Duration
	SlowestBaseline time.Duration
	// Timeout is the per-mutant timeout, and TimeoutSource says where it came
	// from.
	Timeout       time.Duration
	TimeoutSource TimeoutSource

	// Warnings are the warnings published during the run, in order.
	Warnings []Warning
	// Summary is the closing line published in [RunCompleted].
	Summary string
}

// Run executes one mutation run.
//
// The pre-release pipeline is: locate the toolchain, snapshot the workspace,
// build the snapshot, measure the baseline, derive the timeout, and stop —
// with the [CodeMutationPhasesPending] warning saying so. The snapshot is
// removed on every path.
//
// The returned error is nil exactly when the run completed. It always carries a
// stable GOM#### code, either this package's or that of the package that
// failed, and the outcome is returned alongside it.
func Run(ctx context.Context, opts Options) (RunOutcome, error) {
	started := time.Now()
	s := &session{events: opts.Events}
	// Registered before anything else, so that it runs after everything else.
	// Every deferred step below — the snapshot cleanup in particular — may
	// still publish a warning, and a send on a closed channel panics.
	defer s.close()

	out := RunOutcome{
		RunID:   NewRunID(started),
		Status:  StatusFailed,
		Started: started,
	}

	err := s.pipeline(ctx, opts, &out)

	out.Duration = time.Since(started)
	out.Warnings = slices.Clone(s.warnings)
	switch {
	case err == nil:
		out.Status = StatusOK
	case interrupted(err):
		out.Status = StatusInterrupted
	default:
		out.Status = StatusFailed
	}
	if err != nil {
		out.Summary = firstLine(err.Error())
	}
	// Terminal on every path, including this one: a renderer that never sees
	// RunCompleted cannot tell a finished run from a crashed one.
	s.emit(RunCompleted{Status: out.Status, Summary: out.Summary})
	return out, err
}

// pipeline is the run proper, split out so that [Run] owns exactly two things:
// the terminal event and the channel close, in that order.
func (s *session) pipeline(ctx context.Context, opts Options, out *RunOutcome) error {
	cfg := opts.Config

	root, err := workspaceRoot(opts.WorkspaceRoot)
	if err != nil {
		return err
	}
	out.WorkspaceRoot = root

	command, err := testCommand(cfg, opts.TestArgv)
	if err != nil {
		return err
	}
	out.TestCommand = command
	out.Workers = cfg.Execution.Jobs

	s.emit(RunPlanned{RunID: out.RunID, Workers: cfg.Execution.Jobs})
	s.emit(PhaseChanged{
		Phase:  PhaseDiscover,
		Detail: "locating the Go toolchain and copying the workspace",
	})

	toolchain, err := gocmd.LocateContext(ctx, gocmd.Options{})
	if err != nil {
		return err
	}
	out.Toolchain = toolchain

	// `.git`, the build and module caches, and the report directory are
	// excluded by internal/snapshot unconditionally, so they are deliberately
	// not repeated here: one place decides what a snapshot never contains.
	//
	// `mutation.exclude` is deliberately *not* among them, and no future edit
	// should route it here. It selects which files are worth mutating, and the
	// snapshot is the whole workspace as the compiler sees it. Feeding a
	// selection setting into the copy breaks two things at once:
	//
	//   - A file that is not copied is not built and not tested, so the
	//     commonest exclude of all — `**/*_test.go` — deletes the test suite
	//     from the snapshot and the baseline passes by running nothing. That
	//     is precisely the flattering green [CodeBaselineTestFailed] exists to
	//     refuse, and the [CodeBaselineBuildFailed] gate does not catch it
	//     either, because `go build ./...` never compiles a _test.go file.
	//   - [snapshot.Snapshot.WorkspaceDigest] is taken over the manifest, and
	//     it is what makes an outcome cache trustworthy and what proves two
	//     shards read the same code. A digest that moves when a pure selection
	//     setting changes is a cache-poisoning and shard-congruence bug.
	//
	// [snapshot.Options.Exclude] stays available as the escape hatch for a
	// symlink or junction the copy refuses, but it has to come from a
	// snapshot-scoped setting when one exists — never from a selection one.
	snap, err := snapshot.Create(root, snapshot.Options{ReportDir: cfg.Report.Directory})
	if err != nil {
		return err
	}
	out.SnapshotRoot = snap.Root
	out.SnapshotFiles = len(snap.Manifest)
	out.WorkspaceDigest = snap.WorkspaceDigest
	defer func() {
		if removeErr := snap.Cleanup(); removeErr != nil {
			s.warn(CodeSnapshotNotRemoved, "the snapshot directory could not be removed: "+removeErr.Error())
		}
	}()

	scratch, err := os.MkdirTemp(filepath.Dir(snap.Root), scratchPrefix)
	if err != nil {
		return &Error{
			Code:    CodeScratchDir,
			Message: "the per-run temporary directory could not be created",
			Err:     err,
		}
	}
	defer func() {
		if removeErr := os.RemoveAll(scratch); removeErr != nil {
			s.warn(CodeScratchNotRemoved, "the per-run temporary directory could not be removed: "+removeErr.Error())
		}
	}()
	env := childEnv(scratch)

	runs := cfg.Test.BaselineRuns
	s.emit(PhaseChanged{
		Phase: PhaseBaseline,
		Detail: fmt.Sprintf("building the snapshot, then %s of %s",
			countNoun(runs, "timed run"), strings.Join(command, " ")),
	})

	build := toolchain.Command("build", "./...")
	build.Dir = snap.Root
	build.Env = env
	build.Timeout = BaselineCap
	if buildErr := check(ctx, runner.Run(ctx, build), CodeBaselineBuildFailed,
		"the snapshot does not build"); buildErr != nil {
		return buildErr
	}

	// The program is resolved to the toolchain that was just located and
	// reported. A bare `go` would otherwise be resolved through the child's
	// PATH, which need not be — and under a toolchain manager usually is not —
	// the same `go` the run says it is using.
	argv := resolveProgram(command, toolchain)
	durations := make([]time.Duration, 0, runs)
	for i := 1; i <= runs; i++ {
		result := runner.Run(ctx, runner.Spec{
			Argv:    argv,
			Dir:     snap.Root,
			Env:     env,
			Timeout: BaselineCap,
		})
		if runErr := check(ctx, result, CodeBaselineTestFailed,
			fmt.Sprintf("baseline run %d of %d failed", i, runs)); runErr != nil {
			return runErr
		}
		durations = append(durations, result.Duration)
		s.emit(BaselineProgress{Run: i, Of: runs, Duration: result.Duration})
	}
	out.BaselineRuns = durations
	out.AverageBaseline = mean(durations)
	out.SlowestBaseline = slices.Max(durations)

	timeout, source, err := deriveTimeout(cfg.Test.Timeout, out.SlowestBaseline)
	if err != nil {
		return err
	}
	out.Timeout = timeout
	out.TimeoutSource = source
	s.emit(BaselineCompleted{
		Runs:          durations,
		Average:       out.AverageBaseline,
		Slowest:       out.SlowestBaseline,
		Timeout:       timeout,
		TimeoutSource: source,
	}.clone())

	s.warn(CodeMutationPhasesPending,
		"mutation phases not yet implemented — run ends after baseline (pre-release)")
	out.Summary = fmt.Sprintf("baseline only: %s snapshotted, workspace digest %s",
		countNoun(out.SnapshotFiles, "file"), displayDigest(out.WorkspaceDigest))
	return nil
}

// A session owns the event channel for the length of one run.
type session struct {
	events   chan<- Event
	warnings []Warning
	closed   bool
}

// emit publishes one event. A nil channel is the documented "publish nothing"
// case; the send is otherwise blocking, which is what makes the caller's
// obligation to drain a real one rather than a suggestion.
func (s *session) emit(e Event) {
	if s.events == nil {
		return
	}
	s.events <- e
}

// warn records a warning and publishes it. Warnings are kept as well as sent so
// that [RunOutcome] carries them into the report, where a renderer that was not
// listening cannot lose them.
func (s *session) warn(code Code, message string) {
	w := Warning{Code: string(code), Message: message}
	s.warnings = append(s.warnings, w)
	s.emit(w)
}

// close closes the event channel exactly once. The idempotence is not
// defensive tidiness: it is what lets the deferred close sit at the top of Run
// without any path having to reason about whether something else closed first.
func (s *session) close() {
	if s.events == nil || s.closed {
		return
	}
	s.closed = true
	close(s.events)
}

// check turns one [runner.Result] into an error, or nil if the command
// succeeded.
//
// The order of the cases is the contract. A cancelled run comes back from the
// runner as exit code [runner.ExitCodeUnavailable] with no error and no
// timeout, which is indistinguishable from a failure unless the context is
// asked first — so it is asked before the exit status is judged, and after the
// two conditions that are definitely not cancellations.
func check(ctx context.Context, result runner.Result, code Code, what string) error {
	switch {
	case result.Err != nil:
		return &Error{
			Code:    code,
			Message: what + ": the command could not be run",
			Output:  tail(result.Output),
			Err:     result.Err,
		}
	case result.TimedOut:
		return &Error{
			Code:    CodeBaselineTimedOut,
			Message: what + ": no answer within " + BaselineCap.String(),
			Output:  tail(result.Output),
		}
	case ctx.Err() != nil:
		return &Error{
			Code:    CodeInterrupted,
			Message: "the run was interrupted",
			Err:     ctx.Err(),
		}
	case result.ExitCode != 0:
		return &Error{
			Code:    code,
			Message: what + ": exited with status " + strconv.Itoa(result.ExitCode),
			Output:  tail(result.Output),
		}
	}
	return nil
}

// interrupted reports whether err is the run being cancelled rather than the
// run going wrong.
//
// Both spellings count. This package raises [CodeInterrupted] when it notices
// the cancellation itself, but a cancellation that lands inside another package
// comes back with that package's code — internal/gocmd reports a cancelled
// version probe as a probe failure — and the only thing the two have in common
// is context.Canceled somewhere in the chain. Asking for both is what keeps a
// Ctrl-C during toolchain location from being reported as a broken toolchain.
func interrupted(err error) bool {
	return CodeOf(err) == CodeInterrupted || errors.Is(err, context.Canceled)
}

// deriveTimeout resolves the per-mutant timeout.
//
// The rejection boundary is `<=`, not `<`, and it is frozen. A timeout exactly
// equal to the slowest baseline run is not a tight budget, it is a budget that
// the unmutated tests have already been observed to reach: every mutant would
// be reported as a timeout, and the run would measure the scheduler rather than
// the test suite.
func deriveTimeout(explicit, slowest time.Duration) (time.Duration, TimeoutSource, error) {
	if explicit > 0 {
		if explicit <= slowest {
			return 0, "", &Error{
				Code: CodeTimeoutTooSmall,
				Message: fmt.Sprintf(
					"test.timeout %s is not above the slowest baseline run (%s): every mutant would time out",
					explicit, slowest),
			}
		}
		return explicit, TimeoutExplicit, nil
	}
	return max(MinDerivedTimeout, TimeoutFactor*slowest), TimeoutDerived, nil
}

// NewRunID mints the identifier for one run: a UTC timestamp to the second and
// four random hex digits.
//
// The timestamp makes a directory listing of past runs read in order; the
// random suffix keeps two runs started in the same second apart. It is not a
// security boundary and does not need to be — nothing authenticates a run id —
// so two bytes of randomness is the right amount of collision resistance for a
// name a human has to be able to retype.
func NewRunID(t time.Time) string {
	var suffix [2]byte
	// crypto/rand.Read is documented never to fail; it panics internally on a
	// system without a usable entropy source rather than returning an error.
	_, _ = rand.Read(suffix[:])
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix[:])
}

// workspaceRoot resolves the tree to mutate.
func workspaceRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", &Error{Code: CodeWorkspaceRoot, Message: "no workspace root was given"}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", &Error{
			Code:    CodeWorkspaceRoot,
			Message: "the workspace root " + strconv.Quote(root) + " cannot be resolved",
			Err:     err,
		}
	}
	return abs, nil
}

// testCommand picks the argv the baseline is measured with: the `--`
// passthrough when the user wrote one, and `test.command` otherwise.
func testCommand(cfg config.Config, override []string) ([]string, error) {
	command := cfg.Test.Command
	if len(override) > 0 {
		command = override
	}
	if len(command) == 0 {
		return nil, &Error{Code: CodeTestCommand, Message: "the test command is empty"}
	}
	if strings.TrimSpace(command[0]) == "" {
		return nil, &Error{Code: CodeTestCommand, Message: "the test command's program name is empty"}
	}
	return slices.Clone(command), nil
}

// resolveProgram substitutes the located toolchain for a bare `go`, and leaves
// every other program alone.
//
// The substitution is exactly one string deep on purpose. A project whose test
// command is `./scripts/test.sh` or `gotestsum` has chosen a program, and
// second-guessing it would be wrong; a project whose command starts with `go`
// has said "the Go toolchain", and this run has already found out which one
// that is.
func resolveProgram(command []string, toolchain gocmd.Toolchain) []string {
	argv := slices.Clone(command)
	if argv[0] == "go" && toolchain.GoBin != "" {
		argv[0] = toolchain.GoBin
	}
	return argv
}

// childEnv builds the environment every child process of this run receives:
// this process's environment, minus every GO_MUTANTS_ variable, with the three
// temporary-directory variables pointed at the run's own scratch directory.
//
// Inheriting the rest is deliberate. GOFLAGS, GOMODCACHE, GOPROXY, a private
// module's credentials, and the PATH that makes a project's test command work
// are all part of what "the tests pass here" means, and a run that stripped
// them would be measuring a different project.
func childEnv(scratch string) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+len(tempKeys))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), envPrefix) || isTempKey(key) {
			continue
		}
		env = append(env, entry)
	}
	for _, key := range tempKeys {
		env = append(env, key+"="+scratch)
	}
	return env
}

// isTempKey reports whether key is one of the temporary-directory variables.
// The comparison is case-insensitive because Windows environment names are.
func isTempKey(key string) bool {
	return slices.ContainsFunc(tempKeys, func(k string) bool { return strings.EqualFold(key, k) })
}

// mean returns the average of the observations, or zero for none.
func mean(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	return total / time.Duration(len(durations))
}

// displayDigest shortens a digest for a one-line summary. The full digest is
// what reports and cache keys carry; this is only for reading.
func displayDigest(digest string) string {
	if len(digest) <= digestDisplay {
		return digest
	}
	return digest[:digestDisplay]
}

// countNoun renders "1 file" or "3 files".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// firstLine returns everything before the first newline, so that a multi-line
// error still yields a one-line summary.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
