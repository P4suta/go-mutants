// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

func TestCodesAreUniqueAndInBlock(t *testing.T) {
	seen := make(map[Code]bool, len(codes))
	for _, code := range Codes() {
		if seen[code] {
			t.Errorf("code %s is listed twice", code)
		}
		seen[code] = true
		if !strings.HasPrefix(string(code), "GOM40") {
			t.Errorf("code %s is outside the GOM40xx block this package owns", code)
		}
	}
	if !slices.IsSortedFunc(Codes(), func(a, b Code) int { return strings.Compare(string(a), string(b)) }) {
		t.Error("Codes() is not in numeric order")
	}
	// GOM0001 was the pre-release warning that a run stopped after the
	// baseline, and its own documentation promised it would disappear when the
	// mutation phases landed. They have. A code means one thing forever, so the
	// number stays spent rather than being reused for something else.
	for _, code := range Codes() {
		if code == "GOM0001" {
			t.Error("GOM0001 is retired and must not be reused")
		}
	}
}

func TestDeriveTimeout(t *testing.T) {
	cases := []struct {
		name       string
		explicit   time.Duration
		slowest    time.Duration
		want       time.Duration
		wantSource TimeoutSource
		wantErr    bool
	}{
		{
			name:       "a fast suite gets the floor",
			slowest:    100 * time.Millisecond,
			want:       10 * time.Second,
			wantSource: TimeoutDerived,
		},
		{
			name:       "the floor is reached at exactly two seconds",
			slowest:    2 * time.Second,
			want:       10 * time.Second,
			wantSource: TimeoutDerived,
		},
		{
			name:       "a slow suite gets five times its slowest run",
			slowest:    4 * time.Second,
			want:       20 * time.Second,
			wantSource: TimeoutDerived,
		},
		{
			name:       "an explicit timeout above the baseline is taken as written",
			explicit:   90 * time.Second,
			slowest:    4 * time.Second,
			want:       90 * time.Second,
			wantSource: TimeoutExplicit,
		},
		{
			name:     "an explicit timeout below the baseline is refused",
			explicit: time.Second,
			slowest:  4 * time.Second,
			wantErr:  true,
		},
		{
			name:     "an explicit timeout equal to the baseline is refused too",
			explicit: 4 * time.Second,
			slowest:  4 * time.Second,
			wantErr:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, source, err := deriveTimeout(c.explicit, c.slowest)
			if c.wantErr {
				if CodeOf(err) != CodeTimeoutTooSmall {
					t.Fatalf("deriveTimeout(%s, %s) error = %v, want %s", c.explicit, c.slowest, err, CodeTimeoutTooSmall)
				}
				return
			}
			if err != nil {
				t.Fatalf("deriveTimeout(%s, %s) = unexpected error %v", c.explicit, c.slowest, err)
			}
			if got != c.want || source != c.wantSource {
				t.Errorf("deriveTimeout(%s, %s) = %s (%s), want %s (%s)",
					c.explicit, c.slowest, got, source, c.want, c.wantSource)
			}
		})
	}
}

func TestTestCommandPrefersTheOverride(t *testing.T) {
	cfg := config.Defaults()

	got, err := testCommand(cfg, nil)
	if err != nil {
		t.Fatalf("testCommand with no override: %v", err)
	}
	if !slices.Equal(got, config.DefaultTestCommand()) {
		t.Errorf("testCommand with no override = %q, want the configured command", got)
	}

	override := []string{"gotestsum", "--", "./..."}
	got, err = testCommand(cfg, override)
	if err != nil {
		t.Fatalf("testCommand with an override: %v", err)
	}
	if !slices.Equal(got, override) {
		t.Errorf("testCommand with an override = %q, want %q", got, override)
	}
	// The result must not alias the caller's slice: the engine hands it to a
	// child process and reports it afterwards.
	got[0] = "mutated"
	if override[0] != "gotestsum" {
		t.Error("testCommand aliased the override slice")
	}
}

func TestTestCommandRejectsAnUnrunnableCommand(t *testing.T) {
	cfg := config.Defaults()
	cfg.Test.Command = nil
	if _, err := testCommand(cfg, nil); CodeOf(err) != CodeTestCommand {
		t.Errorf("empty command: error = %v, want %s", err, CodeTestCommand)
	}
	cfg.Test.Command = []string{"   ", "test"}
	if _, err := testCommand(cfg, nil); CodeOf(err) != CodeTestCommand {
		t.Errorf("blank program name: error = %v, want %s", err, CodeTestCommand)
	}
}

func TestResolveProgramSubstitutesOnlyABareGo(t *testing.T) {
	toolchain := gocmd.Toolchain{GoBin: "/opt/go/bin/go"}
	cases := []struct {
		name    string
		command []string
		want    []string
	}{
		{"a bare go is resolved", []string{"go", "test", "./..."}, []string{"/opt/go/bin/go", "test", "./..."}},
		{"another program is left alone", []string{"gotestsum", "./..."}, []string{"gotestsum", "./..."}},
		{"an explicit path is left alone", []string{"./scripts/test.sh"}, []string{"./scripts/test.sh"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveProgram(c.command, toolchain); !slices.Equal(got, c.want) {
				t.Errorf("resolveProgram(%q) = %q, want %q", c.command, got, c.want)
			}
		})
	}
}

func TestChildEnvRedirectsTempAndDropsActivation(t *testing.T) {
	t.Setenv("GO_MUTANTS_ACTIVE", "deadbeef")
	t.Setenv("go_mutants_lowercase", "1")
	t.Setenv("TMP", "should-not-survive")
	t.Setenv("GOFLAGS", "-mod=mod")

	env := childEnv("/scratch")
	seen := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		seen[strings.ToUpper(key)] = value
	}

	if _, ok := seen["GO_MUTANTS_ACTIVE"]; ok {
		t.Error("GO_MUTANTS_ACTIVE was inherited by the child environment")
	}
	if _, ok := seen["GO_MUTANTS_LOWERCASE"]; ok {
		t.Error("a lowercase go_mutants_ variable was inherited by the child environment")
	}
	if got := seen["GOFLAGS"]; got != "-mod=mod" {
		t.Errorf("GOFLAGS = %q, want it inherited unchanged", got)
	}
	for _, key := range tempKeys {
		if got := seen[key]; got != "/scratch" {
			t.Errorf("%s = %q, want the scratch directory", key, got)
		}
	}
	// Exactly one entry per temporary variable: the inherited TMP must have
	// been dropped rather than shadowed, since a child reading the first match
	// would otherwise get the wrong one.
	for _, key := range tempKeys {
		count := 0
		for _, entry := range env {
			name, _, _ := strings.Cut(entry, "=")
			if strings.EqualFold(name, key) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("%s appears %d times in the child environment, want 1", key, count)
		}
	}
}

func TestWorkspaceRootRejectsNothing(t *testing.T) {
	if _, err := workspaceRoot("   "); CodeOf(err) != CodeWorkspaceRoot {
		t.Errorf("workspaceRoot(blank) = %v, want %s", err, CodeWorkspaceRoot)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	got, err := workspaceRoot(".")
	if err != nil {
		t.Fatalf("workspaceRoot(.): %v", err)
	}
	if got != wd {
		t.Errorf("workspaceRoot(.) = %q, want %q", got, wd)
	}
}

func TestTailKeepsTheLastLines(t *testing.T) {
	var b strings.Builder
	for i := range OutputTailLines + 20 {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", 1))
		b.WriteByte(byte('0' + i%10))
		b.WriteString("\r\n")
	}
	got := tail([]byte(b.String()))
	lines := strings.Split(got, "\n")
	if len(lines) != OutputTailLines {
		t.Fatalf("tail kept %d lines, want %d", len(lines), OutputTailLines)
	}
	if strings.Contains(got, "\r") {
		t.Error("tail left carriage returns behind")
	}
	if tail([]byte("   \r\n\r\n")) != "" {
		t.Error("tail of blank output is not empty")
	}
}

func TestErrorRendersTheCodeAndNotTheOutput(t *testing.T) {
	cause := errors.New("underlying")
	err := &Error{Code: CodeBaselineTestFailed, Message: "baseline run 1 of 3 failed", Output: "--- FAIL: TestX", Err: cause}
	const want = "GOM4011: baseline run 1 of 3 failed: underlying"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("the cause is not reachable through errors.Is")
	}
	if got := OutputOf(err); got != "--- FAIL: TestX" {
		t.Errorf("OutputOf = %q, want the retained tail", got)
	}
	if OutputOf(errors.New("plain")) != "" {
		t.Error("OutputOf reported a tail for an error from elsewhere")
	}
}

func TestNewRunIDIsSortableAndDistinct(t *testing.T) {
	at := time.Date(2026, 8, 19, 10, 11, 12, 0, time.UTC)
	id := NewRunID(at)
	if !strings.HasPrefix(id, "20260819T101112Z-") {
		t.Fatalf("NewRunID = %q, want a UTC timestamp prefix", id)
	}
	if len(id) != len("20260819T101112Z-")+4 {
		t.Errorf("NewRunID = %q, want a four hex digit suffix", id)
	}
	seen := map[string]bool{}
	for range 64 {
		seen[NewRunID(at)] = true
	}
	if len(seen) < 2 {
		t.Error("NewRunID produced one value 64 times; the suffix is not random")
	}
}

func TestRunWithoutEventsDoesNotPanic(t *testing.T) {
	// A nil channel is the documented "publish nothing" case, and close(nil)
	// panics: the run has to fail on its own terms instead.
	out, err := Run(t.Context(), Options{Config: config.Defaults(), WorkspaceRoot: ""})
	if CodeOf(err) != CodeWorkspaceRoot {
		t.Fatalf("Run with no workspace root = %v, want %s", err, CodeWorkspaceRoot)
	}
	if out.Status != StatusFailed {
		t.Errorf("status = %s, want %s", out.Status, StatusFailed)
	}
	if out.RunID == "" {
		t.Error("the outcome carries no run id")
	}
}

func TestRunClosesTheEventChannelOnFailure(t *testing.T) {
	events := make(chan Event, 8)
	done := make(chan []Event)
	go func() {
		var collected []Event
		for e := range events {
			collected = append(collected, e)
		}
		done <- collected
	}()

	if _, err := Run(t.Context(), Options{Config: config.Defaults(), WorkspaceRoot: "", Events: events}); err == nil {
		t.Fatal("Run succeeded with no workspace root")
	}
	collected := <-done

	if len(collected) != 1 {
		t.Fatalf("collected %d events, want just the terminal one: %+v", len(collected), collected)
	}
	completed, ok := collected[0].(RunCompleted)
	if !ok {
		t.Fatalf("the only event is %T, want RunCompleted", collected[0])
	}
	if completed.Status != StatusFailed {
		t.Errorf("status = %s, want %s", completed.Status, StatusFailed)
	}
	if !strings.HasPrefix(completed.Summary, string(CodeWorkspaceRoot)) {
		t.Errorf("summary = %q, want it to start with the code", completed.Summary)
	}
}

func TestMean(t *testing.T) {
	if got := mean(nil); got != 0 {
		t.Errorf("mean(nil) = %s, want 0", got)
	}
	got := mean([]time.Duration{time.Second, 3 * time.Second})
	if got != 2*time.Second {
		t.Errorf("mean = %s, want 2s", got)
	}
}

// TestCheckCarriesTheInvocationOnEveryFailure is the whole of what [check] owes
// the renderer beyond a code.
//
// A baseline failure that says "the snapshot does not build" and nothing else
// is a sentence about a directory the user has never seen, in a snapshot that
// is deleted before they can look at it. Every branch here therefore names the
// command it judged — including the cancellation, which is the one place a
// reader most wants to know what was still running when the signal arrived.
func TestCheckCarriesTheInvocationOnEveryFailure(t *testing.T) {
	t.Parallel()

	spec := runner.Spec{
		Argv: []string{"/usr/bin/go", "test", "./..."},
		Dir:  "/tmp/go-mutants-snapshot",
		Kind: trace.ExecKindBaselineTest,
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	cases := []struct {
		name   string
		ctx    context.Context
		result runner.Result
		code   Code
	}{
		{
			name:   "the command could not be run",
			ctx:    t.Context(),
			result: runner.Result{ExitCode: runner.ExitCodeUnavailable, Err: errors.New("fork/exec: no such file")},
			code:   CodeBaselineTestFailed,
		},
		{
			name:   "the command timed out",
			ctx:    t.Context(),
			result: runner.Result{ExitCode: runner.ExitCodeUnavailable, TimedOut: true},
			code:   CodeBaselineTimedOut,
		},
		{
			name:   "the run was cancelled",
			ctx:    cancelled,
			result: runner.Result{ExitCode: runner.ExitCodeUnavailable},
			code:   CodeInterrupted,
		},
		{
			name:   "the command exited non-zero",
			ctx:    t.Context(),
			result: runner.Result{ExitCode: 1, Output: []byte("--- FAIL: TestX\n")},
			code:   CodeBaselineTestFailed,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := check(c.ctx, spec, c.result, CodeBaselineTestFailed, "baseline run 1 of 1 failed")
			if err == nil {
				t.Fatal("check accepted a failed command")
			}
			if got := CodeOf(err); got != c.code {
				t.Errorf("code = %s, want %s (%v)", got, c.code, err)
			}
			var failure *Error
			if !errors.As(err, &failure) {
				t.Fatalf("err = %v, want an *engine.Error", err)
			}
			command := failure.Command()
			if command == nil {
				t.Fatal("Command() = nil, want the command that failed")
			}
			if !slices.Equal(command.Argv, spec.Argv) || command.Dir != spec.Dir {
				t.Errorf("Command() = %+v, want the spec's argv and directory", command)
			}
		})
	}

	// A failure the runner noticed itself already named its command, argv copy
	// and all. That one is carried up rather than rebuilt, so the error the user
	// reads and the event the recording holds cannot disagree about what ran.
	named := &runner.Invocation{Argv: []string{"/usr/bin/go", "test", "./..."}, TraceSeq: 12}
	inner := &runner.Error{Code: runner.CodeProcessStartFailed, Message: "could not start it", Invocation: named}
	err := check(t.Context(), spec, runner.Result{ExitCode: runner.ExitCodeUnavailable, Err: inner},
		CodeBaselineBuildFailed, "the snapshot does not build")
	var failure *Error
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want an *engine.Error", err)
	}
	if failure.Command() != named {
		t.Errorf("Command() = %+v, want the invocation the runner had already attached (%+v)",
			failure.Command(), named)
	}
}

// TestCoverageBuildFallbackKeepsTheWholeFailure is about what the fallback
// throws away.
//
// A coverage build that will not compile is given up on and retried without
// coverage, and the warning that says so is one line — which is right for a
// console during a run that is going to succeed anyway. The compiler's own
// diagnostics were dropped entirely, though, and they are the only evidence
// there is that go-mutants' `-coverpkg` build is what broke. The whole failure
// is kept beside the warning instead, for the reporting that follows.
func TestCoverageBuildFallbackKeepsTheWholeFailure(t *testing.T) {
	t.Parallel()

	// The failure as it arrives from internal/execute: one coded line and a
	// compiler blob underneath it.
	failure := &execute.Error{
		Code:    execute.CodeTestBuildFailed,
		Message: "the test binary for example.com/m/pkg could not be built: exited with status 2",
		Output:  "./a_test.go:9:2: undefined: Missing\n./a_test.go:12:2: undefined: AlsoMissing",
	}
	// Written out exactly: the error's own text, and the output on the lines
	// under it. A containment check would pass for a composition that dropped a
	// line or ran two together, which is the only thing this value has to get
	// right.
	kept := fallbackText(failure)
	if want := failure.Error() + "\n" + failure.Output; kept != want {
		t.Errorf("fallbackText =\n%s\nwant\n%s", kept, want)
	}

	// And the fallback path files it. The options are deliberately unusable, so
	// both builds fail; what is asserted is that the *coverage* build's failure
	// was kept whole while the warning kept its first line.
	s := &session{}
	opts := execute.Options{CoverPkg: "example.com/m/..."}
	var cov coverageResult

	if _, err := s.buildTestBinaries(t.Context(), &opts, &cov); err == nil {
		t.Fatal("buildTestBinaries succeeded against unusable options")
	}
	if cov.coverageFallback == "" {
		t.Fatal("the coverage build's failure was not kept")
	}
	if len(s.warnings) != 1 {
		t.Fatalf("published %d warnings, want the one that says coverage was given up", len(s.warnings))
	}
	// One direction, and it is the one the fallback actually promises: the
	// warning is the kept failure's first line wrapped in a sentence, so the
	// console's summary and the record cannot end up describing two different
	// failures.
	if !strings.Contains(s.warnings[0].Message, firstLine(cov.coverageFallback)) {
		t.Errorf("the warning does not quote the kept failure's first line:\n%s\n%s",
			s.warnings[0].Message, cov.coverageFallback)
	}
	if strings.Contains(s.warnings[0].Message, "\n") {
		t.Errorf("the warning is no longer one line:\n%s", s.warnings[0].Message)
	}
}
