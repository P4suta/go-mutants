// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
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

This is a pre-release build: the run stops after the baseline and warns that it
did, rather than reporting a mutation score it has not measured.`

// runOptions holds the flag destinations for one `run` invocation. It is a
// struct rather than closure variables so that the command can be built more
// than once in one process, which is what the tests do.
type runOptions struct {
	jobs    int
	timeout time.Duration
	quiet   bool
	noColor bool
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
	flags.BoolVarP(&o.quiet, "quiet", "q", false,
		"print only the baseline summary, warnings, and the closing line")
	flags.BoolVar(&o.noColor, "no-color", false,
		"never colourise output, even on a terminal")
	return cmd
}

// execute is the `run` command's body.
func (o *runOptions) execute(cmd *cobra.Command, args []string) error {
	testArgv, err := passthrough(cmd, args)
	if err != nil {
		return err
	}

	root, err := os.Getwd()
	if err != nil {
		return &Error{
			Code:    CodeWorkingDirectory,
			Message: "the current working directory cannot be read, so there is no workspace to run against",
			Err:     err,
		}
	}

	cfg, err := config.Load(filepath.Join(root, config.FileName), overlay(cmd, o))
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	renderer := console.NewPlain(out, Version, console.ColorEnabled(out, o.noColor), o.quiet)

	ctx, watch, stop := watchSignals(cmd.Context())
	defer stop()

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

	_, runErr := engine.Run(ctx, engine.Options{
		Config:        cfg,
		WorkspaceRoot: root,
		TestArgv:      testArgv,
		Events:        events,
	})
	wg.Wait()

	if runErr != nil {
		return interpret(runErr, watch.Signal())
	}
	return renderErr
}

// overlay turns the flags the user actually typed into a configuration layer.
//
// Only changed flags are carried. A flag's default is not an opinion: `--jobs`
// left alone must lose to `execution.jobs` in the file, and the only way to
// know the difference is pflag's Changed.
//
// The `--` passthrough is deliberately not overlaid here even though
// config.Overlay has a TestCommand field. It travels to the engine as
// [engine.Options.TestArgv] instead, so that the override has exactly one path;
// setting both would apply the same value twice and leave two places to look
// when it is wrong.
func overlay(cmd *cobra.Command, o *runOptions) config.Overlay {
	flags := cmd.Flags()
	return config.Overlay{
		Jobs:    config.When(flags.Changed("jobs"), o.jobs),
		Timeout: config.When(flags.Changed("timeout"), o.timeout),
	}
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
			return nil, usagef("run takes no positional arguments; write the test command after `--`, as in `go-mutants run -- go test ./...` (got %q)", args[0])
		}
		return nil, nil
	}
	if dash > 0 {
		return nil, usagef("run takes no positional arguments before `--` (got %q)", args[0])
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

// interpret decides what an engine failure means for the exit status.
//
// A cancelled run is not an infrastructure failure: the user asked for it, and
// the contract answers 130 for an interrupt and 143 for a termination. The
// signal is what tells the two apart, and a cancellation with no signal behind
// it — an embedding whose context was cancelled — is reported as an interrupt,
// which is the closest true statement available.
func interpret(err error, sig os.Signal) error {
	if !errors.Is(err, context.Canceled) {
		return err
	}
	code := mutation.ExitInterrupted
	if sig == syscall.SIGTERM {
		code = mutation.ExitTerminated
	}
	return &exitError{code: code, err: err}
}
