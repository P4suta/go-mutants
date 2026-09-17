// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/buildcache"
	"github.com/P4suta/go-mutants/goatest/internal/cli"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const signalHandlingDeadline = 5 * time.Second

type mainServiceFunc func(context.Context, cli.Command, cli.Request, string) (report.Report, error)

func (function mainServiceFunc) Execute(ctx context.Context, command cli.Command, request cli.Request, id string) (report.Report, error) {
	return function(ctx, command, request, id)
}

type syntheticSignal string

func (signal syntheticSignal) String() string { return string(signal) }
func (syntheticSignal) Signal()               {}

func TestTheCLIServiceNamesWhatBelongsToTheMachine(t *testing.T) {
	service := cliService()
	if service.Executable != executablePath(os.Executable) {
		t.Fatalf("cliService named %q, want the path os.Executable reports", service.Executable)
	}
	if service.UserCacheDir == nil {
		t.Fatal("cliService named no user cache directory, want the machine's")
	}

	if service.TempDirectory == "" {
		t.Fatal("cliService named no temporary directory, want the machine's")
	}
}

func TestRealMainHandlesOnlyTheExactVersionFlagAndDelegatesHelp(t *testing.T) {
	service := mainServiceFunc(func(context.Context, cli.Command, cli.Request, string) (report.Report, error) {
		t.Fatal("version/help unexpectedly executed the service")
		return report.Report{}, nil
	})
	for _, testCase := range []struct {
		name       string
		arguments  []string
		wantExit   int
		wantOutput string
		wantError  string
	}{
		{name: "version", arguments: []string{"--version"}, wantExit: 0, wantOutput: "goatest "},
		{name: "help", arguments: []string{"--help"}, wantExit: 0, wantOutput: "Usage:"},
		{name: "bare", arguments: nil, wantExit: 0, wantOutput: "Usage:"},
		{name: "version-extra", arguments: []string{"--version", "extra"}, wantExit: cli.ExitError, wantError: "unknown flag"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := realMainWith(testCase.arguments, &stdout, &stderr, service)
			if exit != testCase.wantExit || !bytes.Contains(stdout.Bytes(), []byte(testCase.wantOutput)) || !bytes.Contains(stderr.Bytes(), []byte(testCase.wantError)) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func TestOperatingSystemWriterWrappersReturnDelegatedExitCodes(t *testing.T) {
	if got := realMain([]string{"--definitely-unknown"}); got != cli.ExitError {
		t.Fatalf("realMain exit = %d, want %d", got, cli.ExitError)
	}
	service := mainServiceFunc(func(context.Context, cli.Command, cli.Request, string) (report.Report, error) {
		return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictDefect}, nil
	})
	if got := runWithService([]string{"verify"}, service); got != cli.ExitDefect {
		t.Fatalf("runWithService exit = %d, want %d", got, cli.ExitDefect)
	}
}

func TestRunWithSignalsMapsTerminationAndNonSyscallInterrupt(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		signal os.Signal
		want   int
	}{
		{name: "termination", signal: syscall.SIGTERM, want: cli.ExitTerminated},
		{name: "synthetic-interrupt", signal: syntheticSignal("interrupt"), want: cli.ExitInterrupted},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			started := make(chan struct{})
			service := mainServiceFunc(func(ctx context.Context, _ cli.Command, _ cli.Request, _ string) (report.Report, error) {
				close(started)
				<-ctx.Done()
				return report.Report{}, ctx.Err()
			})
			signals := make(chan os.Signal, 1)
			result := make(chan int, 1)
			go func() {
				result <- runWithSignals([]string{"verify"}, service, signals, &bytes.Buffer{}, &bytes.Buffer{})
			}()
			select {
			case <-started:
			case <-time.After(signalHandlingDeadline):
				t.Fatal("service did not start")
			}
			signals <- testCase.signal
			select {
			case got := <-result:
				if got != testCase.want {
					t.Fatalf("exit = %d, want %d", got, testCase.want)
				}
			case <-time.After(signalHandlingDeadline):
				t.Fatal("signal did not stop service")
			}
		})
	}
}

func TestEnvironmentTraceBecomesTheFlagTheCommandLayerParses(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		value     string
		arguments []string
		want      []string
	}{
		{name: "unset", value: "", arguments: []string{"verify"}, want: []string{"verify"}},
		{name: "disabled", value: "0", arguments: []string{"verify"}, want: []string{"verify"}},
		{name: "false", value: "false", arguments: []string{"verify"}, want: []string{"verify"}},
		{name: "enabled", value: "1", arguments: []string{"verify"}, want: []string{"verify", "--trace"}},

		{name: "true-bare", value: "true", arguments: nil, want: nil},
		{name: "true", value: "true", arguments: []string{"verify"}, want: []string{"verify", "--trace"}},
		{name: "directory", value: "/tmp/goatest-trace", arguments: []string{"verify"}, want: []string{"verify", "--trace=/tmp/goatest-trace"}},
		{name: "explicit-flag-wins", value: "/tmp/env", arguments: []string{"verify", "--trace=/tmp/flag"}, want: []string{"verify", "--trace=/tmp/flag"}},
		{name: "explicit-default-wins", value: "/tmp/env", arguments: []string{"verify", "--trace"}, want: []string{"verify", "--trace"}},
		{name: "before-test-arguments", value: "1", arguments: []string{"verify", "--", "-short"}, want: []string{"verify", "--trace", "--", "-short"}},
		{name: "help", value: "1", arguments: []string{"--help"}, want: []string{"--help"}},
		{name: "help-short", value: "1", arguments: []string{"-h"}, want: []string{"-h"}},
		{name: "version", value: "1", arguments: []string{"--version"}, want: []string{"--version"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := withTraceEnvironment(testCase.arguments, testCase.value); !slices.Equal(got, testCase.want) {
				t.Fatalf("arguments = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestEnvironmentTraceReachesTheServiceWithoutDisturbingVersionOrHelp(t *testing.T) {
	t.Setenv("GOATEST_TRACE", "/tmp/goatest-environment-trace")
	var requested cli.Request
	service := mainServiceFunc(func(_ context.Context, _ cli.Command, request cli.Request, _ string) (report.Report, error) {
		requested = request
		return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
	})
	var stdout, stderr bytes.Buffer
	if exit := realMainWith([]string{"verify"}, &stdout, &stderr, service); exit != cli.ExitAssured {
		t.Fatalf("verify exit = %d stderr = %q", exit, stderr.String())
	}
	if !requested.Trace || requested.TraceDirectory != "/tmp/goatest-environment-trace" {
		t.Fatalf("request = %+v", requested)
	}
	stdout.Reset()
	if exit := realMainWith([]string{"--version"}, &stdout, &stderr, service); exit != 0 || !bytes.Contains(stdout.Bytes(), []byte("goatest ")) {
		t.Fatalf("version exit = %d stdout = %q", exit, stdout.String())
	}
	stdout.Reset()
	if exit := realMainWith([]string{"--help"}, &stdout, &stderr, service); exit != 0 || !bytes.Contains(stdout.Bytes(), []byte("Usage:")) {
		t.Fatalf("help exit = %d stdout = %q", exit, stdout.String())
	}
}

func TestTheCacheProgramIsDispatchedBeforeTheCommandLayer(t *testing.T) {
	t.Setenv("GOATEST_TRACE", "1")
	t.Setenv("GOATEST_KEEP_TEMP", "1")
	service := mainServiceFunc(func(context.Context, cli.Command, cli.Request, string) (report.Report, error) {
		t.Fatal("the cache program reached the service")
		return report.Report{}, nil
	})
	var stdout, stderr bytes.Buffer
	scratch := t.TempDir()
	exit := realMainStreams([]string{"cacheprog", "--scratch", scratch}, strings.NewReader(""), &stdout, &stderr, service)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("cacheprog exit = %d stderr = %q", exit, stderr.String())
	}

	if !bytes.Contains(stdout.Bytes(), []byte(`"KnownCommands"`)) {
		t.Fatalf("cacheprog stdout = %q, want the protocol", stdout.String())
	}
	stdout.Reset()
	if exit := realMainStreams([]string{"cacheprog"}, strings.NewReader(""), &stdout, &stderr, service); exit != buildcache.CacheProgramUsageExitCode {
		t.Fatalf("cacheprog without a scratch layer = %d, want %d", exit, buildcache.CacheProgramUsageExitCode)
	}
	stdout.Reset()
	if exit := realMainWith([]string{"--help"}, &stdout, &stderr, service); exit != 0 || bytes.Contains(stdout.Bytes(), []byte("cacheprog")) {
		t.Fatalf("help = %q, want the hidden command absent from it", stdout.String())
	}
}

func TestEnvironmentKeepTempBecomesTheFlagTheCommandLayerParses(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		value     string
		arguments []string
		want      []string
	}{
		{name: "unset", value: "", arguments: []string{"verify"}, want: []string{"verify"}},
		{name: "disabled", value: "0", arguments: []string{"verify"}, want: []string{"verify"}},
		{name: "false", value: "false", arguments: []string{"verify"}, want: []string{"verify"}},
		{name: "enabled", value: "1", arguments: []string{"verify"}, want: []string{"verify", "--keep-temp"}},

		{name: "true-bare", value: "true", arguments: nil, want: nil},
		{name: "true", value: "true", arguments: []string{"verify"}, want: []string{"verify", "--keep-temp"}},

		{name: "unknown", value: "maybe", arguments: []string{"verify"}, want: []string{"verify", "--keep-temp=maybe"}},
		{name: "explicit-flag-wins", value: "maybe", arguments: []string{"verify", "--keep-temp"}, want: []string{"verify", "--keep-temp"}},
		{name: "before-test-arguments", value: "1", arguments: []string{"verify", "--", "-short"}, want: []string{"verify", "--keep-temp", "--", "-short"}},
		{name: "help", value: "1", arguments: []string{"--help"}, want: []string{"--help"}},
		{name: "help-short", value: "1", arguments: []string{"-h"}, want: []string{"-h"}},
		{name: "version", value: "1", arguments: []string{"--version"}, want: []string{"--version"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := withKeepTempEnvironment(testCase.arguments, testCase.value); !slices.Equal(got, testCase.want) {
				t.Fatalf("arguments = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestEnvironmentKeepTempReachesTheServiceBesideAnEnvironmentTrace(t *testing.T) {
	t.Setenv("GOATEST_KEEP_TEMP", "1")
	t.Setenv("GOATEST_TRACE", "1")
	var requested cli.Request
	service := mainServiceFunc(func(_ context.Context, _ cli.Command, request cli.Request, _ string) (report.Report, error) {
		requested = request
		return report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}, nil
	})
	var stdout, stderr bytes.Buffer
	if exit := realMainWith([]string{"verify"}, &stdout, &stderr, service); exit != cli.ExitAssured {
		t.Fatalf("verify exit = %d stderr = %q", exit, stderr.String())
	}
	if !requested.KeepTemp || !requested.Trace {
		t.Fatalf("request = %+v", requested)
	}
	t.Setenv("GOATEST_KEEP_TEMP", "maybe")
	stderr.Reset()
	if exit := realMainWith([]string{"verify"}, &stdout, &stderr, service); exit != cli.ExitError ||
		!bytes.Contains(stderr.Bytes(), []byte("keep-temp")) {
		t.Fatalf("unknown value exit = %d stderr = %q", exit, stderr.String())
	}
}

func TestInterruptedExitDistinguishesInterruptAndTermination(t *testing.T) {
	if got := interruptedExit(cli.ExitInterrupted, syscall.SIGINT); got != cli.ExitInterrupted {
		t.Fatalf("interrupt exit = %d", got)
	}
	if got := interruptedExit(cli.ExitInterrupted, 0); got != cli.ExitInterrupted {
		t.Fatalf("exit with no signal at all = %d", got)
	}
	if got := interruptedExit(cli.ExitInterrupted, syscall.SIGTERM); got != cli.ExitTerminated {
		t.Fatalf("termination exit = %d", got)
	}
	if got := interruptedExit(cli.ExitAssured, syscall.SIGTERM); got != cli.ExitAssured {
		t.Fatalf("completed exit was changed to %d", got)
	}
}

func TestExecutablePathIsEmptyWhenTheProcessCannotNameItsOwnBinary(t *testing.T) {
	t.Parallel()
	if got := executablePath(func() (string, error) { return "/stale/goatest", errors.New("no executable") }); got != "" {
		t.Fatalf("executablePath of a failed lookup = %q, want no path at all", got)
	}
	if got := executablePath(func() (string, error) { return "/opt/goatest", nil }); got != "/opt/goatest" {
		t.Fatalf("executablePath = %q", got)
	}
}

func TestTerminalInteractivityNeedsATerminalThatTakesEscapeCodesAndAnEnvironmentThatAllowsThem(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name              string
		environment       map[string]string
		isTerminal        bool
		acceptsEscapeCode bool
		want              bool
	}{
		{name: "a terminal that takes escape codes", isTerminal: true, acceptsEscapeCode: true, want: true},
		{name: "a dumb terminal", environment: map[string]string{"TERM": "dumb"}, isTerminal: true, acceptsEscapeCode: true},
		{name: "colour refused", environment: map[string]string{"NO_COLOR": "1"}, isTerminal: true, acceptsEscapeCode: true},
		{name: "not a terminal", acceptsEscapeCode: true},
		{name: "a terminal that refuses escape codes", isTerminal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			probe := terminalProbe{
				isTerminal:        func(io.Writer) bool { return test.isTerminal },
				acceptsEscapeCode: func(io.Writer) bool { return test.acceptsEscapeCode },
			}
			got := terminalInteractivity(&bytes.Buffer{}, func(name string) string { return test.environment[name] }, probe)
			if got != test.want {
				t.Fatalf("terminalInteractivity = %t, want %t", got, test.want)
			}
		})
	}
}

func TestTheRealTerminalProbesAnswerForAWriterThatIsNotATerminal(t *testing.T) {
	t.Parallel()
	probe := terminalProbes()
	if probe.isTerminal(&bytes.Buffer{}) {
		t.Fatal("a buffer was reported as a terminal")
	}
	if !probe.acceptsEscapeCode(&bytes.Buffer{}) {
		t.Fatal("a writer that is not a console was reported as refusing escape codes")
	}
	if interactiveTerminal(&bytes.Buffer{}) {
		t.Fatal("a buffer was reported as an interactive terminal")
	}
}

func TestAnEnvironmentFlagIsPlacedBeforeTheArgumentSeparator(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		arguments []string
		want      []string
	}{
		{
			name:      "a separator ahead of a name that would otherwise match",
			arguments: []string{"verify", "--", "--trace"},
			want:      []string{"verify", "--trace", "--", "--trace"},
		},
		{
			name:      "a separator first",
			arguments: []string{"--", "verify"},
			want:      []string{"--trace", "--", "verify"},
		},
		{
			name:      "no separator at all",
			arguments: []string{"verify"},
			want:      []string{"verify", "--trace"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := withEnvironmentFlag(test.arguments, "--trace", true)
			if !slices.Equal(got, test.want) {
				t.Fatalf("withEnvironmentFlag(%v) = %v, want %v", test.arguments, got, test.want)
			}
		})
	}
}
