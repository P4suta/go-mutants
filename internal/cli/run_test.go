// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// runWith executes the `run` command with args and returns the error, without
// letting it reach the engine.
//
// Every check it exercises happens before the working directory is read, which
// is deliberate: a user who typed two contradictory flags should not wait for a
// workspace to be copied before being told.
func runWith(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newRunCommand()
	cmd.SetArgs(args)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

func TestRunRefusesJSONWithQuiet(t *testing.T) {
	err := runWith(t, "--json", "--quiet")
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeConflictingFlags {
		t.Fatalf("run --json --quiet = %v, want %s", err, CodeConflictingFlags)
	}
	if coded.Hint == "" {
		t.Error("the conflict names no remedy")
	}
}

func TestRunRefusesBothStrictSpellings(t *testing.T) {
	// cobra enforces this rather than the command body: neither flag is wrong
	// on its own and each is a complete answer, so silently letting one win
	// would make the meaning of a command line depend on a rule nobody wrote
	// down.
	err := runWith(t, "--strict", "--no-strict")
	if err == nil {
		t.Fatal("run --strict --no-strict was accepted")
	}
	if !strings.Contains(err.Error(), "strict") {
		t.Errorf("error = %v, want it to name the flags", err)
	}
}

func TestRunRefusesAMutantPrefixThatCouldNeverMatch(t *testing.T) {
	for _, prefix := range []string{"xyz", "ab", "ABCD", strings.Repeat("a", mutation.IDHexLength+1)} {
		err := runWith(t, "--mutant", prefix)
		var coded *Error
		if !errors.As(err, &coded) || coded.Code != CodeInvalidMutantPrefix {
			t.Errorf("run --mutant %q = %v, want %s", prefix, err, CodeInvalidMutantPrefix)
		}
	}
	// A well-formed prefix is not refused here: whether it matches one mutant,
	// none, or several is a question about a catalogue that does not exist yet.
	if err := checkMutantPrefix("beef"); err != nil {
		t.Errorf("checkMutantPrefix(\"beef\") = %v, want it accepted", err)
	}
	if err := checkMutantPrefix(""); err != nil {
		t.Errorf("an absent --mutant was refused: %v", err)
	}
}

// TestRunOverlayCarriesOnlyChangedFlags is the precedence contract: a flag's
// default is not an opinion, so an untyped flag must lose to the configuration
// file.
func TestRunOverlayCarriesOnlyChangedFlags(t *testing.T) {
	untyped := overlayFrom(t, nil)
	for name, set := range map[string]bool{
		"include":   untyped.Include.IsSet(),
		"exclude":   untyped.Exclude.IsSet(),
		"operators": untyped.Operators.IsSet(),
		"profile":   untyped.Profile.IsSet(),
		"jobs":      untyped.Jobs.IsSet(),
		"timeout":   untyped.Timeout.IsSet(),
		"strict":    untyped.Strict.IsSet(),
	} {
		if set {
			t.Errorf("%s was carried without being typed", name)
		}
	}

	typed := overlayFrom(t, []string{
		"--include", "internal/**", "--exclude", "**/gen/**",
		"--operator", "comparison", "--profile", "all",
		"-j", "3", "--timeout", "45s", "--strict",
	})
	if got, _ := typed.Include.Get(); !strings.Contains(strings.Join(got, " "), "internal/**") {
		t.Errorf("include = %v", got)
	}
	if got, _ := typed.Exclude.Get(); !strings.Contains(strings.Join(got, " "), "**/gen/**") {
		t.Errorf("exclude = %v", got)
	}
	if got, _ := typed.Operators.Get(); !strings.Contains(strings.Join(got, " "), "comparison") {
		t.Errorf("operators = %v", got)
	}
	if got, ok := typed.Profile.Get(); !ok || got != mutation.TierAll {
		t.Errorf("profile = %v/%t, want the all tier", got, ok)
	}
	if got, ok := typed.Jobs.Get(); !ok || got != 3 {
		t.Errorf("jobs = %v/%t, want 3", got, ok)
	}
	if got, ok := typed.Strict.Get(); !ok || !got {
		t.Errorf("strict = %v/%t, want true", got, ok)
	}
}

// TestNoStrictOverridesTheFile is the half of the strict pair that only matters
// when a file already said yes.
func TestNoStrictOverridesTheFile(t *testing.T) {
	overlay := overlayFrom(t, []string{"--no-strict"})
	got, ok := overlay.Strict.Get()
	if !ok || got {
		t.Fatalf("strict = %v/%t, want an explicit false", got, ok)
	}
	// And it really reaches the policy the run gates on, over a file that had
	// asked for strict.
	strictFile := config.Overlay{Strict: config.Explicit(true)}
	cfg := config.MergeOverlays(config.Defaults(), strictFile, overlay)
	if cfg.Policy.Strict {
		t.Error("--no-strict did not turn policy.strict off")
	}
}

// TestRepeatedPatternFlagsAreNotSplitOnCommas pins the StringArrayVar choice: a
// glob is one opaque value, and the pattern language has no way to escape a
// comma.
func TestRepeatedPatternFlagsAreNotSplitOnCommas(t *testing.T) {
	overlay := overlayFrom(t, []string{"--include", "a,b/**", "--include", "c/**"})
	got, ok := overlay.Include.Get()
	if !ok {
		t.Fatal("include was not carried")
	}
	want := []string{"a,b/**", "c/**"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("include = %q, want %q", got, want)
	}
}

// overlayFrom parses args with the real `run` command and returns the layer its
// flags produce.
func overlayFrom(t *testing.T, args []string) config.Overlay {
	t.Helper()
	cmd := newRunCommand()
	var (
		layer config.Overlay
		fail  error
	)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		// The flag destinations belong to the command's own runOptions, which
		// RunE cannot reach, so they are read back out of the flag set.
		o := &runOptions{}
		flags := c.Flags()
		o.include, _ = flags.GetStringArray("include")
		o.exclude, _ = flags.GetStringArray("exclude")
		o.operators, _ = flags.GetStringArray("operator")
		o.profile, _ = flags.GetString("profile")
		o.jobs, _ = flags.GetInt("jobs")
		o.timeout, _ = flags.GetDuration("timeout")
		o.strict, _ = flags.GetBool("strict")
		o.noStrict, _ = flags.GetBool("no-strict")
		layer, fail = runOverlay(c, o)
		return fail
	}
	cmd.SetArgs(args)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(%q): %v", args, err)
	}
	return layer
}

// TestPolicyFailureIsSilentAndCarriesItsCode pins the one error the command
// line decides not to print: the run's own summary already named the survivors
// and the score, and repeating a shortened version on standard error would
// dress a correct measurement up as something having gone wrong.
func TestPolicyFailureIsSilentAndCarriesItsCode(t *testing.T) {
	if err := policyFailure(mutation.Verdict{Code: mutation.ExitOK}); err != nil {
		t.Fatalf("a passing verdict produced %v", err)
	}

	verdict := mutation.Decide(
		mutation.Tally{Killed: 1, UnexpectedSurvivors: 1},
		mutation.Policy{Strict: true, RequireMutants: true},
		mutation.Signals{},
	)
	err := policyFailure(verdict)
	if err == nil {
		t.Fatal("a failing verdict produced no error")
	}
	if got := ExitCode(err); got != mutation.ExitPolicyFailure {
		t.Errorf("ExitCode = %d, want %d", got, mutation.ExitPolicyFailure)
	}
	var rendered bytes.Buffer
	RenderError(&rendered, err)
	if rendered.Len() != 0 {
		t.Errorf("a policy failure printed %q, want nothing", rendered.String())
	}

	// An infrastructure failure is not silent: nothing else has told the user
	// about it.
	infrastructure := mutation.Decide(mutation.Tally{Errored: 1}, mutation.DefaultPolicy(), mutation.Signals{})
	loud := policyFailure(infrastructure)
	if got := ExitCode(loud); got != mutation.ExitInfrastructure {
		t.Errorf("ExitCode = %d, want %d", got, mutation.ExitInfrastructure)
	}
}

// TestInterpretCodesAnUnresolvedMutantAsAUsageError is the boundary between the
// engine's vocabulary and this package's: the engine reports the fact without a
// code, and exactly one GOM number reaches the user.
func TestInterpretCodesAnUnresolvedMutantAsAUsageError(t *testing.T) {
	selection := &engine.SelectionError{Prefix: "beef", Err: mutation.ErrAmbiguousPrefix}
	err := interpret(selection, nil)

	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeMutantUnresolved {
		t.Fatalf("interpret = %v, want %s", err, CodeMutantUnresolved)
	}
	if !errors.Is(err, mutation.ErrAmbiguousPrefix) {
		t.Error("the catalogue's sentinel is not reachable through the coded error")
	}
	if !strings.Contains(coded.Hint, "list --mutant beef") {
		t.Errorf("hint = %q, want it to name the listing that shows the matches", coded.Hint)
	}

	// One code, once. An error whose message repeated its own cause would be
	// rendered as "GOM1009: ... : ..." with the same sentence twice.
	var rendered bytes.Buffer
	RenderError(&rendered, err)
	line := strings.SplitN(rendered.String(), "\n", 2)[0]
	if strings.Count(line, "did not select one mutant") != 1 {
		t.Errorf("the rendered error repeats itself: %q", line)
	}
	if got := ExitCode(err); got != mutation.ExitInfrastructure {
		t.Errorf("ExitCode = %d, want %d: a run that never started measured nothing", got, mutation.ExitInfrastructure)
	}
}
