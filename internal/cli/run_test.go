// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/gitdiff"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/tui"
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

// The identity the scripted repositories commit under, set through the
// environment so that go-mutants' own git — which runs with this process's
// environment — reads exactly what the test wrote.
const (
	gitTestAuthor    = "go-mutants tests"
	gitTestEmail     = "tests@go-mutants.invalid"
	gitTestTimestamp = "2026-02-18T09:15:00+00:00"
)

// gitCommand runs one git command in dir, failing the test if it does not
// succeed.
func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// neutralGitEnvironment points git at configuration files that do not exist and
// pins the commit identity, so that a developer's own `~/.gitconfig` cannot
// change what these tests observe. It is set for the process, because the
// command under test runs git itself.
func neutralGitEnvironment(t *testing.T) {
	t.Helper()
	absent := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(absent, "absent-global-config"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(absent, "absent-system-config"))
	t.Setenv("GIT_AUTHOR_NAME", gitTestAuthor)
	t.Setenv("GIT_AUTHOR_EMAIL", gitTestEmail)
	t.Setenv("GIT_AUTHOR_DATE", gitTestTimestamp)
	t.Setenv("GIT_COMMITTER_NAME", gitTestAuthor)
	t.Setenv("GIT_COMMITTER_EMAIL", gitTestEmail)
	t.Setenv("GIT_COMMITTER_DATE", gitTestTimestamp)
}

// TestBareChangedAsksForTheUpstreamAndSaysSoWhenThereIsNone is the CLI-level
// half of the upstream rule, and it exists because the package-level half
// cannot reach this path.
//
// internal/gitdiff's own tests can ask for the upstream by passing an empty
// ref, which is a value this command never produces: the bare flag carries
// [gitdiff.UpstreamRef], and a resolver that took that notation for a ref would
// look up no upstream at all. The branch with none would then fail at the merge
// base — GOM7713, "the ref may not exist here" — about a ref nobody typed,
// leaving GOM7712 unreachable from the command line and its remedy unread. So
// the flag is driven for real, in a repository that really has no upstream,
// which is the only arrangement that proves the value the flag produces means
// what its help says.
//
// The run stops before a workspace is copied or a toolchain is located, because
// the diff is resolved first on purpose; this needs git and nothing else.
func TestBareChangedAsksForTheUpstreamAndSaysSoWhenThereIsNone(t *testing.T) {
	// No t.Parallel and no parallel subtests: t.Chdir refuses to run in one,
	// and the working directory is where the command finds its workspace.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH, so --changed cannot be exercised here: %v", err)
	}
	// Both spellings of the same request, driven through one body: the bare
	// flag, and the notation a user writes out longhand because the help says
	// the value takes an equals sign.
	for _, flag := range []string{"--changed", "--changed=" + gitdiff.UpstreamRef} {
		t.Run(flag, func(t *testing.T) {
			neutralGitEnvironment(t)
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "alpha.go"), []byte("package alpha\n"), 0o600); err != nil {
				t.Fatalf("writing the fixture file: %v", err)
			}
			gitCommand(t, root, "init", "--quiet")
			gitCommand(t, root, "add", "--all")
			gitCommand(t, root, "commit", "--quiet", "--message", "a branch with nowhere to compare against")
			t.Chdir(root)

			code, _, stderr := execute(t, "run", flag, "--no-color", "--no-tui")
			if code != int(mutation.ExitInfrastructure) {
				t.Errorf("exit = %d, want %d\n%s", code, mutation.ExitInfrastructure, stderr)
			}
			if !strings.Contains(stderr, string(gitdiff.CodeNoUpstream)) {
				t.Errorf("stderr = %q, want %s: the upstream was never looked up",
					stderr, gitdiff.CodeNoUpstream)
			}
			// The remedy is the reason this code exists rather than the merge
			// base's: it says what to do about a branch that tracks nothing.
			if !strings.Contains(stderr, "--set-upstream-to") {
				t.Errorf("stderr = %q, want the remedy for a branch with no upstream", stderr)
			}
		})
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

// TestADashboardFailureDoesNotDecideTheExitStatus pins the asymmetry between
// the two renderers. The dashboard is decoration over a run that has already
// measured everything and already printed its summary, so a terminal it could
// not drive is news and nothing more; the plain renderer's writes are the
// output itself, and losing them is a failure of the run.
func TestADashboardFailureDoesNotDecideTheExitStatus(t *testing.T) {
	var reported bytes.Buffer
	dashboard := &tui.Error{
		Code:    tui.CodeProgram,
		Message: "the live dashboard stopped before the run did",
		Err:     errors.New("raw mode refused"),
	}
	if got := reportDashboardFailure(&reported, dashboard); got != nil {
		t.Errorf("reportDashboardFailure returned %v, want nil so that the run's own verdict decides", got)
	}
	// Nothing else would ever tell the user, so it is not simply dropped.
	for _, want := range []string{"GOM7701", "raw mode refused"} {
		if !strings.Contains(reported.String(), want) {
			t.Errorf("the failure was not reported on standard error: %q does not contain %q", reported.String(), want)
		}
	}
	// The exit status a run with a broken dashboard and a failing gate reports
	// is the gate's, which is the whole point of not returning the first one.
	verdict := mutation.Decide(
		mutation.Tally{Killed: 1, UnexpectedSurvivors: 1},
		mutation.Policy{Strict: true, RequireMutants: true},
		mutation.Signals{},
	)
	if got := ExitCode(policyFailure(verdict)); got != mutation.ExitPolicyFailure {
		t.Errorf("ExitCode = %d, want the policy failure's %d", got, mutation.ExitPolicyFailure)
	}

	// Every other renderer failure is returned untouched and unprinted: it is
	// reported once, by the caller that returns it.
	var quiet bytes.Buffer
	other := errors.New("write /dev/stdout: broken pipe")
	if got := reportDashboardFailure(&quiet, other); !errors.Is(got, other) {
		t.Errorf("reportDashboardFailure(%v) = %v, want it returned unchanged", other, got)
	}
	if quiet.Len() != 0 {
		t.Errorf("a plain-renderer failure was printed early: %q", quiet.String())
	}
	if got := reportDashboardFailure(&quiet, nil); got != nil {
		t.Errorf("reportDashboardFailure(nil) = %v, want nil", got)
	}
}

// TestALostClosingBlockOutranksALostDashboard is the ordering half of the same
// decision. Both halves of the rendering can fail at once, and the one that
// survives has to be the one that cost the user something: a dashboard failure
// costs a picture over a summary that was still printed, and a replay failure
// costs the summary itself.
func TestALostClosingBlockOutranksALostDashboard(t *testing.T) {
	var reported bytes.Buffer
	dashboard := &tui.Error{Code: tui.CodeProgram, Message: "the live dashboard stopped before the run did"}
	lost := errors.New("write /dev/stdout: broken pipe")

	err := finishRendering(&reported, dashboard, func() error { return lost })
	if !errors.Is(err, lost) {
		t.Fatalf("finishRendering = %v, want the replay failure %v", err, lost)
	}
	if got := ExitCode(err); got != mutation.ExitInfrastructure {
		t.Errorf("ExitCode = %d, want %d: the closing block never reached the user", got, mutation.ExitInfrastructure)
	}
	// The dashboard failure is still news, and is still reported.
	if !strings.Contains(reported.String(), string(tui.CodeProgram)) {
		t.Errorf("the dashboard failure was dropped rather than reported: %q", reported.String())
	}

	// A replay that worked leaves nothing behind, whatever the dashboard did.
	reported.Reset()
	replayed := 0
	if err := finishRendering(&reported, dashboard, func() error { replayed++; return nil }); err != nil {
		t.Errorf("finishRendering = %v after a successful replay, want nil", err)
	}
	if replayed != 1 {
		t.Errorf("the closing block was replayed %d times, want once", replayed)
	}

	// A plain run has no block to put back and is asked to replay nothing.
	if err := finishRendering(&reported, nil, nil); err != nil {
		t.Errorf("finishRendering = %v for a plain run that rendered cleanly, want nil", err)
	}
	if err := finishRendering(&reported, lost, nil); !errors.Is(err, lost) {
		t.Errorf("finishRendering = %v, want a plain renderer's own failure kept", err)
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
