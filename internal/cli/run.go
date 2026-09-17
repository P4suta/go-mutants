// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/gitdiff"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/tui"
)

const eventBuffer = 64

const runLong = `Snapshot the workspace, prove the baseline, and run the mutants.

The workspace root is the current directory, and .go-mutants.toml is read from
there. Flags override the file; the file overrides the built-in defaults.

Everything after ` + "`--`" + ` replaces test.command verbatim. It is never passed
through a shell, so no element is word-split, glob-expanded, or substituted:

  go-mutants run -- go test -run TestParser ./internal/...

Your own tree is only ever read. The run works in a disposable snapshot: it
builds it, proves its tests pass unmutated, rewrites it so that every mutant is
present at once behind a guard, proves the rewritten tree still passes with
nothing activated, and then measures one mutant per test process.

--mutant is a selector here, not the filter it is in ` + "`list`" + `: it must name
exactly one mutant, because "why did this one survive" is not a question two
mutants can answer. Everything else is still catalogued and still reported, as
not-run. ` + "`list`" + ` does not compile what it prints, so a prefix from it can name a
mutant that turns out not to build; the run then executes nothing and warns,
quoting the compiler, rather than exiting 0 in silence.

--changed and --shard narrow what is executed and nothing else: discovery,
validation, and the report still cover the whole module, so the ids and the
rejections match a full run's and two reports can be compared mutant for mutant.
Everything left out is reported as not-run with the reason it was left out.

  go-mutants run --changed=origin/main
  go-mutants run --shard 2/4

--changed diffs against the merge base of the ref and HEAD, so a branch is
measured against the commit it left rather than against whatever has landed on
the target since; bare --changed follows the upstream of HEAD and reports it by
name. Its ref needs an equals sign, because the value is optional. What counts
as changed is the working tree, uncommitted edits and never-added files alike.
--shard assigns each mutant from its id alone, so editing one file never
reshuffles the rest, and every shard reports the whole catalogue —
` + "`go-mutants report merge`" + ` proves the shards describe one run and combines them.

Outcomes go-mutants has already proven are reused between runs, so a second run
over unchanged code measures only what has moved. --cache off turns that off and
--cache on asks for it even for a test command go-mutants cannot reason about;
` + "`go-mutants cache status`" + ` says what is stored.

--json writes the run-report-v1 document to standard output and nothing else;
the progress lines, warnings, and errors go to standard error, so the document
can be piped straight into a validator. --explain is the opposite half and the
two are refused together: it prints, underneath the summary, every rejected
mutant with the compiler's own words and every suppressed site by reason.

A run that fails writes a diagnostics bundle: the rendered error and its typed
chain, this run's trace, the environment's variable names, the doctor table, and
the report if there was one. It goes into the run's trace directory when the run
was traced and into report.directory/diagnostics/<run-id>/ otherwise, and the
newest 10 are kept. --no-diagnostics turns it off, and GO_MUTANTS_DIAGNOSTICS=0
does the same for an invocation nobody can add a flag to. An interrupted run
writes none: nothing went wrong.

--keep-temp leaves the run's snapshot and scratch directory on disk instead of
removing them, which is the only way to answer "what did the tree this mutant
ran in look like". A bare --keep-temp keeps them whatever happened;
--keep-temp=on-failure keeps them only when the run failed, which is the mode a
CI job can leave on. The value takes an equals sign, and
GO_MUTANTS_KEEP_TEMP=1|true|always|on-failure asks for the same. It is off by
default because a kept snapshot is a whole copy of your module and nothing will
ever remove it.

-v prints the same run in more detail: how long each phase took, which test
binary killed each mutant, how many attempts it took, and which suites cover a
survivor — or that none does. -vv adds one line for every event the run records,
which is the trace printed as it happens rather than read back afterwards, so
two runs can be diffed without either of them writing a file. Both print lines
rather than the dashboard, and neither can be combined with --quiet or --json.

A completed run exits 0 unless a policy gate the user opted into failed. Nothing
here fails a build by default: --strict and policy.minimum_score are how you ask
for one.`

type runOptions struct {
	include   []string
	exclude   []string
	operators []string
	profile   string
	mutant    string
	changed   string
	shard     string
	cache     string
	report    string
	trace     string
	keepTemp  string
	jobs      int
	isolate   bool
	timeout   time.Duration
	memory    string
	verbose   int
	strict    bool
	noStrict  bool
	json      bool
	explain   bool
	quiet     bool
	noColor   bool
	noTUI     bool
	noDiags   bool

	recording *traceRecording
}

func (o *runOptions) verbosity() int { return min(o.verbose, console.MaxVerbosity) }

func (o *runOptions) publishTrace() bool { return o.verbosity() >= console.VerbosityDetail }

const (
	keepTempNever     = "never"
	keepTempAlways    = "always"
	keepTempOnFailure = "on-failure"
)

func parseKeepTemp(value string) (engine.KeepTemp, error) {
	switch strings.TrimSpace(value) {
	case "", keepTempNever:
		return engine.KeepTempNever, nil
	case keepTempAlways:
		return engine.KeepTempAlways, nil
	case keepTempOnFailure:
		return engine.KeepTempOnFailure, nil
	default:
		return engine.KeepTempNever, &Error{
			Code:    CodeUsage,
			Message: strconv.Quote(value) + " is not a --keep-temp mode",
			Hint: "write `--keep-temp` or `--keep-temp=" + keepTempAlways +
				"` to keep the run's directories whatever happens, `--keep-temp=" + keepTempOnFailure +
				"` to keep them only when the run fails, or `--keep-temp=" + keepTempNever + "` to keep nothing",
		}
	}
}

func newRunCommand() *cobra.Command { return newRunCommandWith(&runOptions{}) }

func newRunCommandWith(o *runOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [flags] [-- test argv ...]",
		Short: "Snapshot the workspace, prove the baseline, and run the mutants",
		Long:  runLong,
		Args:  cobra.ArbitraryArgs,
		RunE:  o.execute,
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&o.include, "include", nil,
		"`GLOB` a file must match to be mutated; repeat for more (default: mutation.include, or **/*.go)")
	flags.StringArrayVar(&o.exclude, "exclude", nil,
		"`GLOB` that removes a file again; repeat for more (default: mutation.exclude)")
	flags.StringArrayVar(&o.operators, "operator", nil,
		"`NAME` of an operator family or rule, from the whole catalogue rather than from the profile; repeat for more")
	flags.StringVar(&o.profile, "profile", "",
		"operator tier `NAME`: balanced, strong, or all (default: mutation.profile, or balanced)")
	flags.StringVar(&o.mutant, "mutant", "",
		"run only the mutant whose id starts with `ID_PREFIX`; it must select exactly one")
	flags.StringVar(&o.changed, "changed", "",
		"execute only the mutants on lines changed since `GIT_REF` (default: the upstream of HEAD); write it as --changed=REF")
	flags.Lookup("changed").NoOptDefVal = gitdiff.UpstreamRef
	flags.StringVar(&o.shard, "shard", "",
		"execute only shard `K/N` of the mutants, 1-based; every shard reports the whole catalogue, and `go-mutants report merge` combines them")
	flags.StringVar(&o.cache, "cache", "",
		"outcome cache `MODE`: auto, on, or off (default: cache.mode, or auto — which reuses outcomes only for the built-in test command)")
	flags.StringVar(&o.report, "report", "",
		"project report `FORMATS` to write into report.directory: none, json, html, or json,html (default: report.formats, or json,html)")
	flags.StringVar(&o.trace, "trace", "",
		"record this run's diagnostic account into `DIR`; a bare --trace records under report.directory/trace, "+
			"the value takes an equals sign, and GO_MUTANTS_TRACE=1|true|DIR asks for the same")
	flags.Lookup("trace").NoOptDefVal = traceDefaultDirectory
	flags.StringVar(&o.keepTemp, "keep-temp", "",
		"leave the run's snapshot and scratch directory on disk instead of removing them: `MODE` is "+
			keepTempAlways+" or "+keepTempOnFailure+", a bare --keep-temp is "+keepTempAlways+
			", and the value takes an equals sign (also GO_MUTANTS_KEEP_TEMP)")
	flags.Lookup("keep-temp").NoOptDefVal = keepTempAlways
	flags.BoolVar(&o.noDiags, "no-diagnostics", false,
		"do not write a diagnostics bundle when the run fails (also GO_MUTANTS_DIAGNOSTICS=0)")
	flags.IntVarP(&o.jobs, "jobs", "j", 0,
		"mutants to execute concurrently (default: execution.jobs, or min(CPUs, 8))")
	flags.DurationVar(&o.timeout, "timeout", 0,
		"per-mutant timeout; unset derives max(10s, slowest baseline x 5)")
	flags.BoolVar(&o.isolate, "isolate", false,
		"give every worker its own copy of the instrumented tree (default: execution.isolate)")
	flags.StringVar(&o.memory, "memory", "",
		"per-mutant memory bound, e.g. 2GiB; unset derives max(1GiB, largest baseline peak x 4)")
	flags.BoolVar(&o.strict, "strict", false,
		"exit 1 when any mutant survives unexpectedly (default: policy.strict)")
	flags.BoolVar(&o.noStrict, "no-strict", false,
		"never exit 1 for survivors, overriding policy.strict")
	flags.BoolVar(&o.json, "json", false,
		"write the run-report-v1 document to standard output and nothing else")
	flags.BoolVar(&o.explain, "explain", false,
		"after the summary, print every rejected mutant with the compiler's own words, and the suppressed sites by reason")
	flags.BoolVarP(&o.quiet, "quiet", "q", false,
		"print only the baseline summary, warnings, and the closing summary block")
	flags.CountVarP(&o.verbose, "verbose", "v",
		"print more: -v adds phase durations, what killed each mutant, and which suites cover a survivor; "+
			"-vv adds one line per recorded event. Implies --no-tui")
	flags.BoolVar(&o.noColor, "no-color", false,
		"never colourise output, even on a terminal; implies --no-tui")
	flags.BoolVar(&o.noTUI, "no-tui", false,
		"never draw the live dashboard; print the plain lines even on a terminal")
	cmd.MarkFlagsMutuallyExclusive("strict", "no-strict")
	return cmd
}

func (o *runOptions) execute(cmd *cobra.Command, args []string) error {
	testArgv, err := passthrough(cmd, args)
	if err != nil {
		return err
	}
	if o.json && o.quiet {
		return &Error{
			Code: CodeConflictingFlags,
			Message: "--json and --quiet cannot be combined: the run report is the whole of what --json writes, " +
				"and there is no shorter version of it",
			Hint: "drop --quiet for the document, or drop --json for the shortened text output",
		}
	}
	if err = checkExplain(o.explain, o.json); err != nil {
		return err
	}
	if err = checkVerbose(o.verbose, o.quiet, o.json); err != nil {
		return err
	}
	if err = checkMutantPrefix(o.mutant); err != nil {
		return err
	}
	flags := cmd.Flags()
	if err = checkSelectors(o.mutant, flags.Changed("changed"), o.shard); err != nil {
		return err
	}
	if err = checkTraceDirectory(flags.Changed("trace"), o.trace); err != nil {
		return err
	}
	keepTemp, err := parseKeepTemp(o.keepTemp)
	if err != nil {
		return err
	}
	var shard report.Shard
	if o.shard != "" {
		if shard, err = report.ParseShard(o.shard); err != nil {
			return err
		}
	}

	root, err := os.Getwd()
	if err != nil {
		return &Error{
			Code:    CodeWorkingDirectory,
			Message: "the current working directory cannot be read, so there is no workspace to run against",
			Err:     err,
		}
	}

	overlay, err := runOverlay(cmd, o)
	if err != nil {
		return err
	}
	cfg, err := config.Load(filepath.Join(root, config.FileName), overlay)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	rendered := out
	if o.json {
		rendered = cmd.ErrOrStderr()
	}
	color := console.ColorEnabled(rendered, o.noColor)

	runID := engine.NewRunID(time.Now())
	recording, traceErr := openTrace(traceRequest{
		workspace:       root,
		reportDirectory: cfg.Report.Directory,
		requested:       o.trace,
		runID:           runID,
		hooks:           traceFilesystem,
	})
	o.recording = recording
	defer func() { _ = recording.close() }()
	if traceErr != nil {
		renderWarning(cmd.ErrOrStderr(), traceErr)
	}

	ctx, watch, stop := watchSignals(cmd.Context())
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		renderer  console.Renderer
		dashboard *tui.Renderer
	)
	if wantsDashboard(rendered, o, detectTerminal) {
		dashboard = tui.New(rendered, dashboardInput(cmd.InOrStdin()), Version, cancel)
		renderer = dashboard
	} else {
		plain := console.NewPlain(rendered, Version, color, o.quiet)
		plain.Verbosity = o.verbosity()
		renderer = plain
	}

	events := make(chan engine.Event, eventBuffer)
	var (
		wg        sync.WaitGroup
		renderErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		renderErr = renderer.Run(ctx, events)
	}()

	outcome, runErr := engine.Run(ctx, engine.Options{
		Config:         cfg,
		WorkspaceRoot:  root,
		TestArgv:       testArgv,
		ToolVersion:    Version,
		MutantPrefix:   o.mutant,
		Changed:        flags.Changed("changed"),
		ChangedRef:     o.changed,
		Shard:          shard,
		Events:         events,
		RunID:          runID,
		TraceSink:      recording.sink,
		TraceDirectory: recording.directory,
		PublishTrace:   o.publishTrace(),
		Notes:          recording.notes,
		KeepTemp:       keepTemp,
	})
	wg.Wait()

	if err := recording.close(); err != nil {
		renderWarning(cmd.ErrOrStderr(), &Error{
			Code:    CodeTraceUnavailable,
			Message: "the recording of this run could not be closed cleanly",
			Err:     err,
		})
	}

	var replay func() error
	if dashboard != nil {
		replay = func() error { return replayFinal(rendered, Version, color, dashboard.Final()) }
	}
	renderErr = finishRendering(cmd.ErrOrStderr(), renderErr, replay)

	if o.json {
		if document := publishedDocument(outcome); document != nil {
			if err := writeReportJSON(out, document); err != nil {
				return err
			}
		}
	}
	if o.explain {
		for _, rep := range publishedReports(outcome) {
			if err := explainRun(rendered, color, rep); err != nil {
				return err
			}
		}
	}
	for _, rep := range publishedReports(outcome) {
		emitGitHub(out, cmd.ErrOrStderr(), o.json, rep)
	}

	if runErr != nil {
		return o.withDiagnostics(cmd, root, cfg.Report.Directory, runID, outcome,
			interpret(runErr, watch.Signal()), runErr)
	}
	if renderErr != nil {
		return renderErr
	}
	return policyFailure(outcome.Verdict)
}

func (o *runOptions) withDiagnostics(
	cmd *cobra.Command,
	workspace, reportDirectory, runID string,
	outcome engine.RunOutcome,
	reported, runErr error,
) error {
	if o.noDiags || engine.Interrupted(runErr) {
		return reported
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), diagnosticsBudget)
	defer cancel()
	directory, err := writeDiagnostics(ctx, diagnosticsRequest{
		workspace:       workspace,
		reportDirectory: reportDirectory,
		runID:           runID,
		traceDirectory:  o.recording.directory,
		events:          o.recording.Events(),
		err:             reported,
		outcome:         outcome,
		environ:         os.Environ(),
		hooks:           traceFilesystem,
	})
	if err != nil {
		renderWarning(cmd.ErrOrStderr(), &Error{
			Code:    CodeDiagnosticsUnavailable,
			Message: "the diagnostics bundle for this failed run could not be written, so the failure below is all there is",
			Err:     err,
			Hint:    "the run's own result is unaffected; --no-diagnostics stops go-mutants trying",
		})
		return reported
	}
	return &diagnosticsError{err: reported, directory: directory}
}

func runOverlay(cmd *cobra.Command, o *runOptions) (config.Overlay, error) {
	flags := cmd.Flags()
	overlay := config.Overlay{
		Include:   config.When(flags.Changed("include"), o.include),
		Exclude:   config.When(flags.Changed("exclude"), o.exclude),
		Operators: config.When(flags.Changed("operator"), o.operators),
		Jobs:      config.When(flags.Changed("jobs"), o.jobs),
		Isolate:   config.When(flags.Changed("isolate"), o.isolate),
		Timeout:   config.When(flags.Changed("timeout"), o.timeout),
	}
	switch {
	case flags.Changed("strict"):
		overlay.Strict = config.Explicit(o.strict)
	case flags.Changed("no-strict"):
		overlay.Strict = config.Explicit(!o.noStrict)
	}
	if flags.Changed("profile") {
		tier, err := config.ParseProfile(o.profile)
		if err != nil {
			return config.Overlay{}, err
		}
		overlay.Profile = config.Explicit(tier)
	}
	if flags.Changed("cache") {
		mode, err := config.ParseCacheMode(o.cache)
		if err != nil {
			return config.Overlay{}, err
		}
		overlay.CacheMode = config.Explicit(mode)
	}
	if flags.Changed("memory") {
		size, err := config.ParseMemory(o.memory)
		if err != nil {
			return config.Overlay{}, err
		}
		overlay.Memory = config.Explicit(size)
	}
	if flags.Changed("report") {
		formats, err := config.ParseReportFormats(o.report)
		if err != nil {
			return config.Overlay{}, err
		}
		overlay.ReportFormats = config.Explicit(formats)
	}
	return overlay, nil
}

func checkMutantPrefix(value string) error {
	if value == "" {
		return nil
	}
	if len(value) < mutation.MinPrefixLength || len(value) > mutation.IDHexLength || !isLowerHex(value) {
		return &Error{
			Code: CodeInvalidMutantPrefix,
			Message: fmt.Sprintf("%q is not a mutant id prefix: expected between %d and %d lowercase hex characters",
				value, mutation.MinPrefixLength, mutation.IDHexLength),
			Hint: "copy the id from `go-mutants list` or from the JSON report; the short form printed in a listing is a prefix of the full one",
		}
	}
	return nil
}

func checkExplain(explain, asJSON bool) error {
	if !explain || !asJSON {
		return nil
	}
	return &Error{
		Code: CodeConflictingFlags,
		Message: "--explain and --json cannot be combined: everything --explain prints is already in the document, " +
			"and mixing prose into it would make the output neither readable nor parsable",
		Hint: "drop --explain and read `rejected[]` and `skips[]` out of the document, or drop --json for the prose",
	}
}

func checkVerbose(verbose int, quiet, asJSON bool) error {
	if verbose == 0 {
		return nil
	}
	switch {
	case quiet:
		return &Error{
			Code: CodeConflictingFlags,
			Message: "--verbose and --quiet cannot be combined: they are the two directions of the same dial, " +
				"and a run cannot print both more and less than it usually does",
			Hint: "drop --quiet for the detail, or drop -v for the shortened output",
		}
	case asJSON:
		return &Error{
			Code: CodeConflictingFlags,
			Message: "--verbose and --json cannot be combined: the run report is the whole of what --json writes, " +
				"and the verbose lines are prose about how it was arrived at",
			Hint: "drop -v for the document, or drop --json and add --trace to keep the account this prints on disk",
		}
	default:
		return nil
	}
}

func checkSelectors(mutant string, changed bool, shard string) error {
	if mutant == "" {
		return nil
	}
	var other string
	switch {
	case changed:
		other = "--changed"
	case shard != "":
		other = "--shard"
	default:
		return nil
	}
	return &Error{
		Code: CodeConflictingFlags,
		Message: "--mutant and " + other + " cannot be combined: --mutant names one mutant to measure, and " +
			other + " can only take it away, leaving a run that executes nothing and exits 0",
		Hint: "drop " + other + " to answer a question about the one mutant, or drop --mutant to narrow the whole run",
	}
}

func checkTraceDirectory(changed bool, directory string) error {
	if !changed || strings.TrimSpace(directory) != "" {
		return nil
	}
	return &Error{
		Code:    CodeUsage,
		Message: "--trace was given an empty directory",
		Hint: "write `--trace` on its own to record under report.directory/trace, or `--trace=DIR` to name one; " +
			"an unset shell variable expands to nothing",
	}
}

func emitGitHub(out, errOut io.Writer, asJSON bool, r *report.Report) {
	if asJSON || r == nil {
		return
	}
	summary := os.Getenv(console.GitHubSummaryEnv)
	if summary == "" {
		return
	}
	if err := console.EmitGitHub(out, summary, r); err != nil {
		RenderError(errOut, &Error{
			Code:    CodeGitHubSummary,
			Message: "the GitHub Actions summary could not be written to " + summary,
			Err:     err,
		})
	}
}

func publishedDocument(outcome engine.RunOutcome) interface{ Marshal() ([]byte, error) } {
	switch {
	case outcome.WorkspaceReport != nil:
		return outcome.WorkspaceReport
	case outcome.Report != nil:
		return outcome.Report
	}
	return nil
}

func publishedReports(outcome engine.RunOutcome) []*report.Report {
	if outcome.WorkspaceReport != nil {
		return outcome.WorkspaceReport.Reports()
	}
	if outcome.Report == nil {
		return nil
	}
	return []*report.Report{outcome.Report}
}

func writeReportJSON(w io.Writer, r interface{ Marshal() ([]byte, error) }) error {
	data, err := r.Marshal()
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func passthrough(cmd *cobra.Command, args []string) ([]string, error) {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 {
		if len(args) > 0 {
			return nil, positional(cmd, args[0],
				"run takes no positional arguments; write the test command after `--`, as in `go-mutants run -- go test ./...`")
		}
		return nil, nil
	}
	if dash > 0 {
		return nil, positional(cmd, args[0], "run takes no positional arguments before `--`")
	}
	argv := args[dash:]
	if len(argv) == 0 {
		return nil, &Error{
			Code:    CodeTestArgv,
			Message: "`--` was given with no test command after it",
			Hint:    "write the command to run, as in `go-mutants run -- go test ./...`, or drop the `--` to use test.command",
		}
	}
	if strings.TrimSpace(argv[0]) == "" {
		return nil, &Error{
			Code:    CodeTestArgv,
			Message: fmt.Sprintf("the test command's program name is empty (argv is %q)", argv),
			Hint:    "an unset shell variable expands to nothing; quote it or give the program's name",
		}
	}
	return argv, nil
}

func positional(cmd *cobra.Command, got, what string) error {
	err := usagef("%s (got %q)", what, got)
	for _, flag := range []struct{ name, noun string }{
		{"changed", "ref"}, {"trace", "directory"}, {"keep-temp", "mode"},
	} {
		if cmd.Flags().Changed(flag.name) {
			err.Hint = "--" + flag.name + " takes its " + flag.noun + " with an equals sign: write `--" +
				flag.name + "=" + got + "`, not `--" + flag.name + " " + got + "`"
			return err
		}
	}
	return err
}

func policyFailure(verdict mutation.Verdict) error {
	if verdict.OK() {
		return nil
	}
	detail := "a policy gate failed"
	if len(verdict.Failures) > 0 {
		detail = verdict.Failures[0].Detail
	}
	return &exitError{code: verdict.Code, err: errors.New(detail), silent: true}
}

func finishRendering(w io.Writer, renderErr error, replay func() error) error {
	renderErr = reportDashboardFailure(w, renderErr)
	if replay == nil {
		return renderErr
	}
	if err := replay(); err != nil && renderErr == nil {
		renderErr = err
	}
	return renderErr
}

func reportDashboardFailure(w io.Writer, renderErr error) error {
	var dashboardErr *tui.Error
	if !errors.As(renderErr, &dashboardErr) {
		return renderErr
	}
	RenderError(w, renderErr)
	return nil
}

func interpret(err error, sig os.Signal) error {
	var selection *engine.SelectionError
	if errors.As(err, &selection) {
		return &Error{
			Code:    CodeMutantUnresolved,
			Message: "--mutant " + strconv.Quote(selection.Prefix) + " did not select one mutant",
			Hint:    "run `go-mutants list --mutant " + selection.Prefix + "` to see what that prefix matches",
			Err:     selection.Err,
		}
	}
	if !errors.Is(err, context.Canceled) {
		return err
	}
	code := mutation.ExitInterrupted
	if sig == syscall.SIGTERM {
		code = mutation.ExitTerminated
	}
	return &exitError{code: code, err: err}
}
