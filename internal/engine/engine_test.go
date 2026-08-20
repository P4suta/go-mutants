// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/gocmd"
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
