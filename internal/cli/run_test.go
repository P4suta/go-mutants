// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/gitdiff"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/tui"
)

func runWith(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newRunCommand()
	if args == nil {
		args = []string{}
	}
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
	if err := checkMutantPrefix("beef"); err != nil {
		t.Errorf("checkMutantPrefix(\"beef\") = %v, want it accepted", err)
	}
	if err := checkMutantPrefix(""); err != nil {
		t.Errorf("an absent --mutant was refused: %v", err)
	}
}

func TestRunOverlayCarriesOnlyChangedFlags(t *testing.T) {
	untyped := overlayFrom(t, nil)
	for name, set := range map[string]bool{
		"include":   untyped.Include.IsSet(),
		"exclude":   untyped.Exclude.IsSet(),
		"operators": untyped.Operators.IsSet(),
		"profile":   untyped.Profile.IsSet(),
		"jobs":      untyped.Jobs.IsSet(),
		"timeout":   untyped.Timeout.IsSet(),
		"memory":    untyped.Memory.IsSet(),
		"strict":    untyped.Strict.IsSet(),
	} {
		if set {
			t.Errorf("%s was carried without being typed", name)
		}
	}

	typed := overlayFrom(t, []string{
		"--include", "internal/**", "--exclude", "**/gen/**",
		"--operator", "comparison", "--profile", "all",
		"-j", "3", "--timeout", "45s", "--memory", "2GiB", "--strict",
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
	if got, ok := typed.Memory.Get(); !ok || got != 2<<30 {
		t.Errorf("memory = %v/%t, want 2 GiB in bytes", got, ok)
	}
	if got, ok := typed.Strict.Get(); !ok || !got {
		t.Errorf("strict = %v/%t, want true", got, ok)
	}
}

func TestNoStrictOverridesTheFile(t *testing.T) {
	overlay := overlayFrom(t, []string{"--no-strict"})
	got, ok := overlay.Strict.Get()
	if !ok || got {
		t.Fatalf("strict = %v/%t, want an explicit false", got, ok)
	}
	strictFile := config.Overlay{Strict: config.Explicit(true)}
	cfg := config.MergeOverlays(config.Defaults(), strictFile, overlay)
	if cfg.Policy.Strict {
		t.Error("--no-strict did not turn policy.strict off")
	}
}

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

const (
	gitTestAuthor    = "go-mutants tests"
	gitTestEmail     = "tests@go-mutants.invalid"
	gitTestTimestamp = "2026-02-18T09:15:00+00:00"
)

func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", dir}, args...)
	command := exec.Command("git", argv...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

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

func TestBareChangedAsksForTheUpstreamAndSaysSoWhenThereIsNone(t *testing.T) {
	_ = testkit.GitBinary(t)
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
			if !strings.Contains(stderr, "--set-upstream-to") {
				t.Errorf("stderr = %q, want the remedy for a branch with no upstream", stderr)
			}
		})
	}
}

func overlayFrom(t *testing.T, args []string) config.Overlay {
	t.Helper()
	cmd := newRunCommand()
	var (
		layer config.Overlay
		fail  error
	)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		o := &runOptions{}
		flags := c.Flags()
		o.include, _ = flags.GetStringArray("include")
		o.exclude, _ = flags.GetStringArray("exclude")
		o.operators, _ = flags.GetStringArray("operator")
		o.profile, _ = flags.GetString("profile")
		o.jobs, _ = flags.GetInt("jobs")
		o.timeout, _ = flags.GetDuration("timeout")
		o.memory, _ = flags.GetString("memory")
		o.strict, _ = flags.GetBool("strict")
		o.noStrict, _ = flags.GetBool("no-strict")
		o.cache, _ = flags.GetString("cache")
		o.report, _ = flags.GetString("report")
		layer, fail = runOverlay(c, o)
		return fail
	}
	if args == nil {
		args = []string{}
	}
	cmd.SetArgs(args)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(%q): %v", args, err)
	}
	return layer
}

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

	infrastructure := mutation.Decide(mutation.Tally{Errored: 1}, mutation.DefaultPolicy(), mutation.Signals{})
	loud := policyFailure(infrastructure)
	if got := ExitCode(loud); got != mutation.ExitInfrastructure {
		t.Errorf("ExitCode = %d, want %d", got, mutation.ExitInfrastructure)
	}
}

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
	for _, want := range []string{"GOM7701", "raw mode refused"} {
		if !strings.Contains(reported.String(), want) {
			t.Errorf("the failure was not reported on standard error: %q does not contain %q", reported.String(), want)
		}
	}
	verdict := mutation.Decide(
		mutation.Tally{Killed: 1, UnexpectedSurvivors: 1},
		mutation.Policy{Strict: true, RequireMutants: true},
		mutation.Signals{},
	)
	if got := ExitCode(policyFailure(verdict)); got != mutation.ExitPolicyFailure {
		t.Errorf("ExitCode = %d, want the policy failure's %d", got, mutation.ExitPolicyFailure)
	}

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
	if !strings.Contains(reported.String(), string(tui.CodeProgram)) {
		t.Errorf("the dashboard failure was dropped rather than reported: %q", reported.String())
	}

	reported.Reset()
	replayed := 0
	if err := finishRendering(&reported, dashboard, func() error { replayed++; return nil }); err != nil {
		t.Errorf("finishRendering = %v after a successful replay, want nil", err)
	}
	if replayed != 1 {
		t.Errorf("the closing block was replayed %d times, want once", replayed)
	}

	if err := finishRendering(&reported, nil, nil); err != nil {
		t.Errorf("finishRendering = %v for a plain run that rendered cleanly, want nil", err)
	}
	if err := finishRendering(&reported, lost, nil); !errors.Is(err, lost) {
		t.Errorf("finishRendering = %v, want a plain renderer's own failure kept", err)
	}
}

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

func TestReportFlagCarriesTheFormatsTheUserTyped(t *testing.T) {
	if overlayFrom(t, nil).ReportFormats.IsSet() {
		t.Error("--report was carried without being typed")
	}
	for _, tc := range []struct {
		value string
		want  []config.ReportFormat
	}{
		{value: "none", want: []config.ReportFormat{}},
		{value: "json", want: []config.ReportFormat{config.FormatJSON}},
		{value: "html", want: []config.ReportFormat{config.FormatHTML}},
		{value: "json,html", want: []config.ReportFormat{config.FormatJSON, config.FormatHTML}},
		{value: " html , json ", want: []config.ReportFormat{config.FormatHTML, config.FormatJSON}},
	} {
		overlay := overlayFrom(t, []string{"--report", tc.value})
		got, ok := overlay.ReportFormats.Get()
		if !ok {
			t.Errorf("--report %q carried nothing", tc.value)
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("--report %q carried %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestReportFlagRefusesAFormatNobodyWrites(t *testing.T) {
	cmd := newRunCommand()
	o := &runOptions{report: "json,pdf"}
	if err := cmd.Flags().Set("report", o.report); err != nil {
		t.Fatalf("setting --report: %v", err)
	}
	_, err := runOverlay(cmd, o)
	if err == nil {
		t.Fatal("--report json,pdf was accepted")
	}
	if !strings.Contains(err.Error(), "--report") {
		t.Errorf("the diagnostic does not name the flag: %v", err)
	}
}

func TestEmitGitHubIsGatedOnTheEnvironmentAndOnJSON(t *testing.T) {
	score := 50.0
	document := &report.Report{
		DocumentType:  report.DocumentType,
		SchemaVersion: report.SchemaVersion,
		Summary:       report.Summary{Total: 2, Killed: 1, Survived: 1, ScorePercent: &score},
		Mutants: []report.Mutant{{
			ID: strings.Repeat("11", 32), DisplayID: "11223344",
			Path: "internal/alpha/alpha.go", Line: 10, Column: 7,
			Family: "comparison", Rule: "eq-to-neq",
			Original: "==", Replacement: "!=", Outcome: report.OutcomeSurvived,
		}},
	}

	for name, tc := range map[string]struct {
		set        bool
		asJSON     bool
		report     *report.Report
		wantStdout bool
		wantFile   bool
	}{
		"inside a job":             {set: true, report: document, wantStdout: true, wantFile: true},
		"outside a job":            {set: false, report: document},
		"inside a job with --json": {set: true, asJSON: true, report: document},
		"with no report":           {set: true, report: nil},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "summary.md")
			if tc.set {
				t.Setenv(console.GitHubSummaryEnv, path)
			} else {
				t.Setenv(console.GitHubSummaryEnv, "")
			}
			var out, errOut bytes.Buffer
			emitGitHub(&out, &errOut, tc.asJSON, tc.report)

			if got := strings.Contains(out.String(), "::warning "); got != tc.wantStdout {
				t.Errorf("standard output = %q, want an annotation: %v", out.String(), tc.wantStdout)
			}
			data, err := os.ReadFile(path)
			switch {
			case tc.wantFile && err != nil:
				t.Errorf("the summary file was not written: %v", err)
			case tc.wantFile && !strings.Contains(string(data), "## go-mutants"):
				t.Errorf("the summary file holds %q", data)
			case !tc.wantFile && err == nil:
				t.Errorf("a summary file was written when it should not have been: %q", data)
			}
			if errOut.Len() != 0 {
				t.Errorf("something was reported on standard error: %q", errOut.String())
			}
		})
	}
}

func TestEmitGitHubReportsAFailureAndDoesNotReturnIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "summary.md")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("staging the failure: %v", err)
	}
	t.Setenv(console.GitHubSummaryEnv, dir)

	score := 50.0
	var out, errOut bytes.Buffer
	emitGitHub(&out, &errOut, false, &report.Report{
		Summary: report.Summary{Total: 1, Survived: 1, ScorePercent: &score},
		Mutants: []report.Mutant{{
			ID: strings.Repeat("11", 32), DisplayID: "11223344",
			Path: "a.go", Line: 1, Column: 1, Rule: "eq-to-neq",
			Original: "==", Replacement: "!=", Outcome: report.OutcomeSurvived,
		}},
	})
	if !strings.Contains(errOut.String(), string(CodeGitHubSummary)) {
		t.Errorf("the failure was not reported: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "::warning ") {
		t.Errorf("the annotations were lost with the summary: %q", out.String())
	}
}

func TestMemoryFlagIsRefusedWithTheSameSentenceTheFileGets(t *testing.T) {
	for _, bad := range []string{"2GB", "plenty", "0"} {
		cmd := newRunCommand()
		var fail error
		cmd.RunE = func(c *cobra.Command, _ []string) error {
			o := &runOptions{}
			o.memory, _ = c.Flags().GetString("memory")
			_, fail = runOverlay(c, o)
			return fail
		}
		cmd.SetArgs([]string{"--memory", bad})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Errorf("--memory %q was accepted", bad)
		} else if !strings.Contains(err.Error(), "--memory") {
			t.Errorf("--memory %q was refused without naming the flag: %v", bad, err)
		}
	}
}

func TestRunHelpMentionsMemory(t *testing.T) {
	code, stdout, stderr := execute(t, "run", "--help")
	if code != 0 {
		t.Fatalf("run --help exited %d: %s", code, stderr)
	}
	for _, needle := range []string{"--memory", "2GiB", "largest baseline peak"} {
		if !strings.Contains(stdout, needle) {
			t.Errorf("run --help does not mention %q:\n%s", needle, stdout)
		}
	}
}
