// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// execute drives the whole command tree with captured streams.
func execute(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = ExecuteContext(t.Context(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCodesAreUniqueAndInBlock(t *testing.T) {
	seen := map[Code]bool{}
	for _, code := range Codes() {
		if seen[code] {
			t.Errorf("code %s is listed twice", code)
		}
		seen[code] = true
		if !strings.HasPrefix(string(code), "GOM10") {
			t.Errorf("code %s is outside the GOM10xx block this package owns", code)
		}
	}
	if !slices.IsSortedFunc(Codes(), func(a, b Code) int { return strings.Compare(string(a), string(b)) }) {
		t.Error("Codes() is not in numeric order")
	}
}

func TestBareInvocationPrintsHelpAndSucceeds(t *testing.T) {
	code, stdout, stderr := execute(t)
	if code != int(mutation.ExitOK) {
		t.Errorf("exit = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
	for _, needle := range []string{"Usage:", "go-mutants [command]", "run "} {
		if !strings.Contains(stdout, needle) {
			t.Errorf("help does not contain %q", needle)
		}
	}
}

func TestHelpCarriesTheExitCodeTable(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"run", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, _ := execute(t, args...)
			if code != int(mutation.ExitOK) {
				t.Errorf("exit = %d, want 0", code)
			}
			for _, needle := range []string{"Exit codes:", "  0 ", "  1 ", "  2 ", "  130 ", "  143 "} {
				if !strings.Contains(stdout, needle) {
					t.Errorf("help does not document %q", needle)
				}
			}
		})
	}
}

func TestHelpDoesNotDependOnTheMachine(t *testing.T) {
	// The worker default is min(NumCPU, 8), so printing it as pflag's default
	// would make `run --help` say a different number on a laptop and on a CI
	// runner. Help output has to be diffable between two machines.
	_, stdout, _ := execute(t, "run", "--help")
	if strings.Contains(stdout, "(default ") {
		t.Errorf("run --help prints a pflag default, which may vary by machine:\n%s", stdout)
	}
	if !strings.Contains(stdout, "min(CPUs, 8)") {
		t.Errorf("run --help does not describe the worker default:\n%s", stdout)
	}
}

func TestOutOfRangeJobsIsRefusedByTheConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, stderr := execute(t, "run", "--jobs", "0")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "error GOM3030") {
		t.Errorf("stderr = %q, want the worker-count range error", stderr)
	}
}

func TestVersionPrintsOneLineAndKeepsTheShorthandFree(t *testing.T) {
	code, stdout, _ := execute(t, "--version")
	if code != int(mutation.ExitOK) {
		t.Errorf("exit = %d, want 0", code)
	}
	if stdout != "go-mutants "+Version+"\n" {
		t.Errorf("stdout = %q, want %q", stdout, "go-mutants "+Version+"\n")
	}
	// -v is reserved for verbosity; cobra would have claimed it for --version
	// if the flag had not been registered explicitly.
	if flag := NewRootCommand().Flags().ShorthandLookup("v"); flag != nil {
		t.Errorf("-v is bound to --%s; it must stay free for verbosity", flag.Name)
	}
}

func TestUnknownCommandSuggestsAndFails(t *testing.T) {
	code, _, stderr := execute(t, "ru")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.HasPrefix(stderr, "error "+string(CodeUsage)+": unknown command") {
		t.Errorf("stderr = %q, want a coded usage error", stderr)
	}
	if !strings.Contains(stderr, "run") {
		t.Error("no did-you-mean suggestion for a one-edit typo")
	}
}

func TestFarTypoGetsNoSuggestion(t *testing.T) {
	// Three edits from "run" is past SuggestionsMinimumDistance.
	_, _, stderr := execute(t, "cache")
	if strings.Contains(stderr, "Did you mean") {
		t.Errorf("a distant typo was given a suggestion: %q", stderr)
	}
}

func TestUnknownFlagIsAUsageError(t *testing.T) {
	code, _, stderr := execute(t, "run", "--nope")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "error "+string(CodeUsage)+": unknown flag: --nope") {
		t.Errorf("stderr = %q, want a coded usage error", stderr)
	}
}

func TestPositionalArgumentsAreRefusedWithAnExplanation(t *testing.T) {
	code, _, stderr := execute(t, "run", "./...")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeUsage)) || !strings.Contains(stderr, "`--`") {
		t.Errorf("stderr = %q, want the separator explained", stderr)
	}
	if !strings.Contains(stderr, "hint: ") {
		t.Errorf("stderr = %q, want a hint line", stderr)
	}
}

func TestEmptyPassthroughIsRefused(t *testing.T) {
	code, _, stderr := execute(t, "run", "--")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeTestArgv)) {
		t.Errorf("stderr = %q, want %s", stderr, CodeTestArgv)
	}
}

func TestPassthroughIsCapturedVerbatim(t *testing.T) {
	cmd := newRunCommand()
	cmd.SetArgs([]string{"--jobs", "2", "--", "go", "test", "-run", "TestX", "./..."})
	cmd.RunE = func(c *cobra.Command, args []string) error {
		got, err := passthrough(c, args)
		if err != nil {
			return err
		}
		want := []string{"go", "test", "-run", "TestX", "./..."}
		if !slices.Equal(got, want) {
			t.Errorf("passthrough = %q, want %q", got, want)
		}
		if !c.Flags().Changed("jobs") {
			t.Error("--jobs before the separator was not parsed")
		}
		return nil
	}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestOverlayCarriesOnlyChangedFlags(t *testing.T) {
	cmd := newRunCommand()
	o := &runOptions{}
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		// The flag destinations belong to the command's own runOptions, so the
		// overlay is read from the command rather than from o.
		layer, err := runOverlay(c, o)
		if err != nil {
			return err
		}
		if layer.Jobs.IsSet() {
			t.Error("jobs was carried without being typed")
		}
		if layer.Timeout.IsSet() {
			t.Error("timeout was carried without being typed")
		}
		return nil
	}
	cmd.SetArgs(nil)
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	cmd = newRunCommand()
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		jobs, err := c.Flags().GetInt("jobs")
		if err != nil {
			return err
		}
		layer, err := runOverlay(c, &runOptions{jobs: jobs})
		if err != nil {
			return err
		}
		got, ok := layer.Jobs.Get()
		if !ok || got != 3 {
			t.Errorf("jobs overlay = %v/%t, want 3/true", got, ok)
		}
		return nil
	}
	cmd.SetArgs([]string{"-j", "3"})
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestInvalidConfigurationIsReportedWithItsOwnCode(t *testing.T) {
	dir := t.TempDir()
	document := "version = 1\n[report]\nlow = 90\nhigh = 10\n"
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(document), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Chdir(dir)

	code, _, stderr := execute(t, "run")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "error GOM3064") {
		t.Errorf("stderr = %q, want the configuration's own code", stderr)
	}
	// The code must appear once per line, not twice: every configuration error
	// already renders itself with its code.
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if strings.Count(line, "GOM3064") > 1 {
			t.Errorf("the code is printed twice: %q", line)
		}
	}
}

func TestRenderErrorLiftsTheCodeOutOfEveryLine(t *testing.T) {
	var b bytes.Buffer
	RenderError(&b, errors.New("GOM3003: .go-mutants.toml:4:1: mutation.foo: unknown key\nGOM3003: .go-mutants.toml:5:1: mutation.bar: unknown key"))
	want := "error GOM3003: .go-mutants.toml:4:1: mutation.foo: unknown key\n" +
		"error GOM3003: .go-mutants.toml:5:1: mutation.bar: unknown key\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

// TestRenderErrorCodesAContinuationLine covers the shape the all-lines-coded
// test above cannot reach: a single error whose message carries a newline.
//
// internal/discover folds a multi-line loader blob before it gets here, so this
// is the second line of defence rather than the first. It is still a line
// go-mutants must never print, because a line with no "error GOM####: " on it is
// a line `grep '^error '` and every CI log parser drop on the floor — and the
// half of a compile failure that names the file and the column is exactly the
// half that would go missing.
func TestRenderErrorCodesAContinuationLine(t *testing.T) {
	var b bytes.Buffer
	RenderError(&b, errors.New("GOM4111: discovery needs a tree that compiles, and 1 package error stopped it: "+
		"scratch.example/broken: # scratch.example/broken\r\n\n.\\broken.go:3:39: undefined: undefinedThing"))
	want := "error GOM4111: discovery needs a tree that compiles, and 1 package error stopped it: " +
		"scratch.example/broken: # scratch.example/broken\n" +
		"error GOM4111: .\\broken.go:3:39: undefined: undefinedThing\n"
	got := b.String()
	if got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
	// The property the two lines above are one instance of: nothing this
	// function writes may stand on its own.
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if !strings.HasPrefix(line, "error GOM") {
			t.Errorf("line %q is not greppable as an error", line)
		}
	}
}

// TestRenderErrorKeepsAnUncodedErrorGreppableToo is the same property on the
// other branch. cobra and pflag produce one line, so this cannot happen through
// the command line; the loop exists so that it cannot happen at all.
func TestRenderErrorKeepsAnUncodedErrorGreppableToo(t *testing.T) {
	var b bytes.Buffer
	RenderError(&b, errors.New("unknown flag: --nope\ndid you mean --note?"))
	want := "error GOM1001: unknown flag: --nope\n" +
		"error GOM1001: did you mean --note?\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

func TestRenderErrorIndentsACommandTail(t *testing.T) {
	var b bytes.Buffer
	RenderError(&b, &engine.Error{
		Code:    engine.CodeBaselineTestFailed,
		Message: "baseline run 1 of 3 failed: exited with status 1",
		Output:  "--- FAIL: TestX\nFAIL",
	})
	want := "error GOM4011: baseline run 1 of 3 failed: exited with status 1\n" +
		"    --- FAIL: TestX\n" +
		"    FAIL\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

func TestRenderErrorFallsBackToTheUsageCode(t *testing.T) {
	var b bytes.Buffer
	RenderError(&b, errors.New("unknown flag: --nope"))
	if got := b.String(); got != "error GOM1001: unknown flag: --nope\n" {
		t.Errorf("RenderError = %q", got)
	}
}

func TestSplitCode(t *testing.T) {
	cases := []struct {
		line     string
		wantCode string
		wantRest string
		wantOK   bool
	}{
		{"GOM4011: message", "GOM4011", "message", true},
		{"GOM0001: message", "GOM0001", "message", true},
		{"GOMX011: message", "", "", false},
		{"GOM401: message", "", "", false},
		{"GOM4011:no space", "", "", false},
		{"", "", "", false},
		{"just a message", "", "", false},
	}
	for _, c := range cases {
		code, rest, ok := splitCode(c.line)
		if code != c.wantCode || rest != c.wantRest || ok != c.wantOK {
			t.Errorf("splitCode(%q) = (%q, %q, %t), want (%q, %q, %t)",
				c.line, code, rest, ok, c.wantCode, c.wantRest, c.wantOK)
		}
	}
}

func TestExitCodeMapping(t *testing.T) {
	if got := ExitCode(nil); got != mutation.ExitOK {
		t.Errorf("ExitCode(nil) = %d, want 0", got)
	}
	if got := ExitCode(errors.New("anything")); got != mutation.ExitInfrastructure {
		t.Errorf("ExitCode(plain) = %d, want 2", got)
	}
	interrupted := &exitError{code: mutation.ExitInterrupted, err: context.Canceled}
	if got := ExitCode(interrupted); got != mutation.ExitInterrupted {
		t.Errorf("ExitCode(interrupt) = %d, want 130", got)
	}
	terminated := &exitError{code: mutation.ExitTerminated, err: context.Canceled}
	if got := ExitCode(terminated); got != mutation.ExitTerminated {
		t.Errorf("ExitCode(terminate) = %d, want 143", got)
	}
}

func TestInterpretDistinguishesTheSignals(t *testing.T) {
	cancelled := &engine.Error{Code: engine.CodeInterrupted, Message: "the run was interrupted", Err: context.Canceled}

	if got := ExitCode(interpret(cancelled, nil)); got != mutation.ExitInterrupted {
		t.Errorf("no signal: exit = %d, want 130", got)
	}
	if got := ExitCode(interpret(cancelled, os.Interrupt)); got != mutation.ExitInterrupted {
		t.Errorf("SIGINT: exit = %d, want 130", got)
	}
	if got := ExitCode(interpret(cancelled, syscall.SIGTERM)); got != mutation.ExitTerminated {
		t.Errorf("SIGTERM: exit = %d, want 143", got)
	}

	other := &engine.Error{Code: engine.CodeBaselineTestFailed, Message: "failed"}
	if got := ExitCode(interpret(other, syscall.SIGTERM)); got != mutation.ExitInfrastructure {
		t.Errorf("an ordinary failure with a signal recorded: exit = %d, want 2", got)
	}
}
