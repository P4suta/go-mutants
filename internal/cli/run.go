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

// eventBuffer is how many events the channel between the engine and the
// renderer holds.
//
// The renderer drains continuously, so the buffer is not what keeps the engine
// from blocking — draining is. It exists so that a burst of events during a
// phase change does not serialise the engine behind a terminal write, and 64 is
// comfortably more than any phase emits at once.
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

A completed run exits 0 unless a policy gate the user opted into failed. Nothing
here fails a build by default: --strict and policy.minimum_score are how you ask
for one.`

// runOptions holds the flag destinations for one `run` invocation. It is a
// struct rather than closure variables so that the command can be built more
// than once in one process, which is what the tests do.
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
	jobs      int
	timeout   time.Duration
	strict    bool
	noStrict  bool
	json      bool
	explain   bool
	quiet     bool
	noColor   bool
	noTUI     bool
}

// newRunCommand builds the `run` command.
func newRunCommand() *cobra.Command {
	o := &runOptions{}
	cmd := &cobra.Command{
		Use:   "run [flags] [-- test argv ...]",
		Short: "Snapshot the workspace, prove the baseline, and run the mutants",
		Long:  runLong,
		// Positional arguments are accepted here and rejected in execute, so
		// that the rejection can explain the `--` separator instead of cobra
		// reporting "accepts 0 arg(s), received 3".
		Args: cobra.ArbitraryArgs,
		RunE: o.execute,
	}
	flags := cmd.Flags()
	// StringArrayVar, never StringSliceVar: a pattern is a single opaque value.
	// Splitting on commas would make `--include "a,b/**"` mean something the
	// user did not write, and the glob language has no way to escape a comma.
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
	// The value is optional, which pflag expresses with NoOptDefVal — and which
	// also means `--changed REF` with a space is not the same thing as
	// `--changed=REF`: pflag takes the bare form as the flag with its default
	// and leaves REF as a positional argument. That is pflag's rule for every
	// optional-value flag and cannot be turned off, so [passthrough] recognises
	// the mistake and says how to write it instead.
	//
	// The default is git's own notation for the upstream branch, so that a user
	// who writes it out longhand gets exactly what the bare flag does:
	// [gitdiff.UpstreamRef] is a request to resolve the upstream by name rather
	// than a ref to be diffed against, so both spellings record `origin/main`
	// and both reach GOM7712 on a branch that tracks nothing.
	flags.Lookup("changed").NoOptDefVal = gitdiff.UpstreamRef
	flags.StringVar(&o.shard, "shard", "",
		"execute only shard `K/N` of the mutants, 1-based; every shard reports the whole catalogue, and `go-mutants report merge` combines them")
	flags.StringVar(&o.cache, "cache", "",
		"outcome cache `MODE`: auto, on, or off (default: cache.mode, or auto — which reuses outcomes only for the built-in test command)")
	flags.StringVar(&o.report, "report", "",
		"project report `FORMATS` to write into report.directory: none, json, html, or json,html (default: report.formats, or json,html)")
	// The pflag default is zero and the real one is described in the usage
	// text. Printing config.DefaultJobs() as the default would make `--help`
	// say 8 on a laptop and 4 on a CI runner, and help output that depends on
	// the machine cannot be diffed, golden-tested, or quoted in a bug report.
	// Nothing reads the zero: the overlay carries this flag only when pflag
	// says the user typed it, and `--jobs 0` is refused by the configuration
	// validator like any other out-of-range worker count.
	flags.IntVarP(&o.jobs, "jobs", "j", 0,
		"mutants to execute concurrently (default: execution.jobs, or min(CPUs, 8))")
	flags.DurationVar(&o.timeout, "timeout", 0,
		"per-mutant timeout; unset derives max(10s, slowest baseline x 5)")
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
	flags.BoolVar(&o.noColor, "no-color", false,
		"never colourise output, even on a terminal; implies --no-tui")
	flags.BoolVar(&o.noTUI, "no-tui", false,
		"never draw the live dashboard; print the plain lines even on a terminal")
	// Neither is wrong on its own and each is a complete answer, so the pair is
	// refused rather than resolved: silently letting one win would make the
	// meaning of a command line depend on a rule nobody wrote down.
	cmd.MarkFlagsMutuallyExclusive("strict", "no-strict")
	return cmd
}

// execute is the `run` command's body.
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
	// Checked before a workspace is copied and a baseline is measured. A prefix
	// of the wrong shape can never name a mutant, and finding that out after
	// several minutes of work would be a poor way to learn it.
	if err = checkMutantPrefix(o.mutant); err != nil {
		return err
	}
	flags := cmd.Flags()
	if err = checkSelectors(o.mutant, flags.Changed("changed"), o.shard); err != nil {
		return err
	}
	// Parsed here rather than in the engine for the same reason: `--shard 3/2`
	// is a mistake about the invocation, and it costs nothing to say so before
	// the workspace is copied.
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

	// Under --json the document owns standard output, so everything the
	// renderer writes goes to standard error — the same split `list --json`
	// makes. The renderer is never simply dropped: the engine's sends block, so
	// something has to drain them whatever the user asked to see.
	out := cmd.OutOrStdout()
	rendered := out
	if o.json {
		rendered = cmd.ErrOrStderr()
	}
	color := console.ColorEnabled(rendered, o.noColor)

	ctx, watch, stop := watchSignals(cmd.Context())
	defer stop()
	// A second cancellation, downstream of the signal handler's, so that the
	// dashboard's Ctrl-C key can stop the run without owning the signal
	// handling. Both paths end in the same place — this context, cancelled,
	// with the engine unwinding and publishing its partial report — which is
	// what makes the two renderers agree about what interrupting a run means.
	// A keystroke leaves no signal behind, so [interpret] reports it as an
	// interrupt and the process exits 130, exactly as Ctrl-C in plain mode does.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The dashboard is the default on a terminal and is never the only copy of
	// anything: what it draws is erased when it exits, and what it kept is
	// printed underneath by [replayFinal].
	var (
		renderer  console.Renderer
		dashboard *tui.Renderer
	)
	if wantsDashboard(rendered, o, detectTerminal) {
		dashboard = tui.New(rendered, dashboardInput(cmd.InOrStdin()), Version, cancel)
		renderer = dashboard
	} else {
		renderer = console.NewPlain(rendered, Version, color, o.quiet)
	}

	// The renderer starts first and is joined last: the engine's sends block,
	// so a consumer that is not already running is a deadlock, and a consumer
	// that is not waited for can lose the closing line to a racing exit.
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
		Config:        cfg,
		WorkspaceRoot: root,
		TestArgv:      testArgv,
		ToolVersion:   Version,
		MutantPrefix:  o.mutant,
		Changed:       flags.Changed("changed"),
		ChangedRef:    o.changed,
		Shard:         shard,
		Events:        events,
	})
	wg.Wait()

	// Once the alternate screen is gone, the scrollback gets what was on it
	// that still matters. A plain run has already printed all of this.
	var replay func() error
	if dashboard != nil {
		replay = func() error { return replayFinal(rendered, Version, color, dashboard.Final()) }
	}
	renderErr = finishRendering(cmd.ErrOrStderr(), renderErr, replay)

	// The document is written whenever there is one, the interrupted path
	// included: a partial report is still the record of what the run measured,
	// and a consumer that asked for JSON should not have to parse a console to
	// find out that it exists.
	if o.json && outcome.Report != nil {
		if err := writeReportJSON(out, outcome.Report); err != nil {
			return err
		}
	}
	// Underneath the closing summary, and only when there is a document to
	// explain: a run that stopped before it had a report has nothing to say
	// here, and the failure that stopped it has already been reported.
	if o.explain && outcome.Report != nil {
		if err := explainRun(rendered, color, outcome.Report); err != nil {
			return err
		}
	}
	emitGitHub(out, cmd.ErrOrStderr(), o.json, outcome.Report)

	if runErr != nil {
		return interpret(runErr, watch.Signal())
	}
	if renderErr != nil {
		return renderErr
	}
	return policyFailure(outcome.Verdict)
}

// runOverlay turns the flags the user actually typed into a configuration
// layer.
//
// Only changed flags are carried. A flag's default is not an opinion: `--jobs`
// left alone must lose to `execution.jobs` in the file, and the only way to
// know the difference is pflag's Changed.
//
// Two things travel outside it, each for its own reason. The `--` passthrough
// goes to the engine as [engine.Options.TestArgv], so that the override has
// exactly one path; setting both would apply the same value twice and leave two
// places to look when it is wrong. `--mutant` is not a configuration setting at
// all — it narrows one invocation and is never written in a file.
func runOverlay(cmd *cobra.Command, o *runOptions) (config.Overlay, error) {
	flags := cmd.Flags()
	overlay := config.Overlay{
		Include:   config.When(flags.Changed("include"), o.include),
		Exclude:   config.When(flags.Changed("exclude"), o.exclude),
		Operators: config.When(flags.Changed("operator"), o.operators),
		Jobs:      config.When(flags.Changed("jobs"), o.jobs),
		Timeout:   config.When(flags.Changed("timeout"), o.timeout),
	}
	// The two spellings are mutually exclusive, so at most one is Changed. Each
	// carries its own flag's value rather than a constant, so that the explicit
	// `--strict=false` a script may generate means what it says.
	switch {
	case flags.Changed("strict"):
		overlay.Strict = config.Explicit(o.strict)
	case flags.Changed("no-strict"):
		overlay.Strict = config.Explicit(!o.noStrict)
	}
	// The profile is the one flag parsed here rather than in the configuration
	// layer, because the overlay carries a tier and the command line carries a
	// name. Everything else — every glob, every operator name — is validated by
	// internal/config against the same rules the file is held to, so a bad value
	// is reported as the flag the user typed and not as the TOML key they never
	// wrote.
	if flags.Changed("profile") {
		tier, err := config.ParseProfile(o.profile)
		if err != nil {
			return config.Overlay{}, err
		}
		overlay.Profile = config.Explicit(tier)
	}
	// `--cache` is parsed here for the same reason as `--profile`: the overlay
	// carries a mode and the command line carries a name, and the diagnostic for
	// a bad one should name the flag the user typed.
	if flags.Changed("cache") {
		mode, err := config.ParseCacheMode(o.cache)
		if err != nil {
			return config.Overlay{}, err
		}
		overlay.CacheMode = config.Explicit(mode)
	}
	// `--report` is the third of the same kind: the overlay carries a list of
	// formats and the command line carries one comma-separated word. `none` is
	// the reason the parsing cannot be left to the overlay machinery — it means
	// an explicitly empty list, which has to beat the file's `formats` the way
	// any other explicit value does, and an empty flag value could not say that.
	if flags.Changed("report") {
		formats, err := config.ParseReportFormats(o.report)
		if err != nil {
			return config.Overlay{}, err
		}
		overlay.ReportFormats = config.Explicit(formats)
	}
	return overlay, nil
}

// checkMutantPrefix rejects a `--mutant` value that could never name a mutant.
//
// Only the shape is checked here. Whether a well-formed prefix matches one
// mutant, none, or several is a question about the catalogue, which does not
// exist until the run has copied the workspace and discovered it; the engine
// answers it and reports a [engine.SelectionError], which [interpret] turns
// back into a usage error.
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

// checkExplain refuses `--explain` alongside `--json`.
//
// It is a semantic check rather than cobra's MarkFlagsMutuallyExclusive, which
// would render as a bare usage error: neither flag is wrong on its own, the
// remedy is to drop one rather than to fix a value, and the reason is worth a
// sentence. It is the same judgement, and the same code, as `--json` with
// `--quiet`.
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

// checkSelectors refuses `--mutant` alongside a narrowing flag.
//
// `--mutant` is a selector and not a filter: it names the one mutant the run is
// about, and the question it exists to answer — "why did this one survive" —
// has no smaller version. Combining it with `--changed` or `--shard` can only
// take that mutant away, and the run would then execute nothing at all and exit
// 0 having measured nothing, which is the most dangerous kind of green.
//
// `--changed` and `--shard` compose, and deliberately so: a shard of a pull
// request's diff is what a CI matrix asks for, and each narrows a set rather
// than naming a member of it.
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

// emitGitHub writes the GitHub Actions half of a run's output: the survivor
// annotations to out, and the Markdown summary appended to the file
// `$GITHUB_STEP_SUMMARY` names.
//
// The variable is the whole of the detection. It is set by the runner for every
// step of every job and by nothing else, which makes it a far better signal
// than `CI`, and it also names the file that has to be written — so a run
// inside a job that somehow has no summary file is a run that emits nothing,
// rather than one that prints workflow commands into somebody's terminal.
//
// `--json` suppresses both halves. Standard output belongs to the document
// then, and a `::warning` line in front of it would make the one thing `--json`
// promises — that the output is a document a validator can read — false.
//
// A failure to write either half is reported and does not decide the exit
// status, which is [reportDashboardFailure]'s judgement applied to the same
// kind of thing. By the time this runs the mutants have been executed, the
// report is filed, and the closing block is on the screen; the annotations are
// a convenience on top of a run that has already done its work, and letting one
// turn a failed score gate's exit 1 into an exit 2 would tell a CI job "the
// tool broke" where the truth is "your tests missed something".
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

// writeReportJSON writes the run report and nothing else.
//
// The bytes are the report's own: [report.Report.Marshal] is what goes on disk,
// so the document on standard output and the document in the history are the
// same file. Re-encoding it here would be a second encoder to keep in step with
// the schema.
func writeReportJSON(w io.Writer, r interface{ Marshal() ([]byte, error) }) error {
	data, err := r.Marshal()
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// passthrough extracts the argv the user wrote after `--`.
//
// pflag records where the separator was, and everything after it is taken
// verbatim: no splitting, no expansion, no interpretation of a leading dash.
// Anything before it is a positional argument, which `run` does not have.
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

// positional builds the refusal for an argument `run` cannot take, and
// recognises the one way a correct-looking command line produces one.
//
// `--changed` takes an optional value, which pflag can only express as
// `--changed=REF`: written with a space, the ref becomes a positional argument
// and the run is narrowed by the upstream branch instead of by the ref the user
// named — or refused here, which is the better of the two. Both branches of
// [passthrough] can be reached that way, `--changed HEAD -- go test ./...`
// landing in the one about the separator, so both ask this to word the message.
func positional(cmd *cobra.Command, got, what string) error {
	err := usagef("%s (got %q)", what, got)
	if cmd.Flags().Changed("changed") {
		err.Hint = "--changed takes its ref with an equals sign: write `--changed=" + got + "`, not `--changed " + got + "`"
	}
	return err
}

// policyFailure turns a completed run's verdict into the exit status, or nil
// when nothing the user asked to gate on failed.
//
// The failure is deliberately silent: the reason is already in the summary
// block the renderer wrote, and printing "error GOM....: 1 mutant survived
// unexpectedly" underneath it would say the same thing twice and, worse, dress
// a measurement the run made correctly up as something having gone wrong. What
// the exit status means is documented in the help output's exit code table.
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

// finishRendering closes the rendering half of a run and returns the one error
// the exit status should be decided from.
//
// It reports a dashboard failure and then replays the closing block, in that
// order, and the order is the point. Both steps can fail, only one error
// survives, and the two failures are not worth the same: a dashboard that could
// not take the terminal cost the user a picture, while a replay that could not
// write cost them the answer. Reporting the dashboard first is what empties the
// slot, so a replay failure lands in it rather than being dropped behind a
// cosmetic error that had already claimed it.
//
// replay is nil for a plain run, which has printed its closing block as it went
// and has nothing to put back on the screen.
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

// reportDashboardFailure writes a dashboard failure to w and returns the error
// the exit status should be decided from, which is no longer that one.
//
// The live dashboard is decoration over a run that has already done its work.
// By the time one of its failures is in hand the engine has executed, the
// renderer has drained the stream to the end, the report has been written, and
// the closing summary has been replayed to standard output by [replayFinal] —
// so the user has everything a plain run would have given them, minus the
// picture. Letting that decide the exit status would be wrong twice over: it
// would report an exit 2 infrastructure failure for a run that succeeded, and
// it would hide a policy gate that really did fail, because a returned error
// short-circuits [policyFailure] and a CI job would read "the tool broke" where
// the truth is "your score is below the threshold you set".
//
// Every other renderer error is returned unchanged. When the plain renderer's
// writes fail the user got no output at all, which is a failure of the one job
// a run has beyond measuring, and exit 2 is the honest answer to it.
func reportDashboardFailure(w io.Writer, renderErr error) error {
	var dashboardErr *tui.Error
	if !errors.As(renderErr, &dashboardErr) {
		return renderErr
	}
	RenderError(w, renderErr)
	return nil
}

// interpret decides what an engine failure means for the exit status.
//
// A cancelled run is not an infrastructure failure: the user asked for it, and
// the contract answers 130 for an interrupt and 143 for a termination. The
// signal is what tells the two apart, and a cancellation with no signal behind
// it — an embedding whose context was cancelled — is reported as an interrupt,
// which is the closest true statement available.
//
// A `--mutant` that did not select one mutant is the other special case. The
// engine reports it without a code, because the mistake is in how the run was
// invoked rather than in the orchestration and only this package owns that
// vocabulary; it comes out here as one GOM10xx usage error rather than as two
// codes for one condition.
func interpret(err error, sig os.Signal) error {
	var selection *engine.SelectionError
	if errors.As(err, &selection) {
		// The cause is the catalogue's own sentinel rather than the engine's
		// wrapper, whose text already contains it: an error that renders as
		// "did not select one mutant: did not select one mutant: ..." is one
		// nobody reads twice. The sentinel stays reachable through errors.Is.
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
