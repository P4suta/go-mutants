// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testlog"
)

const testLogFlag = "-test.testlogfile="

const testLogDir = "testlogs"

func undefinedFlagRefusal() runner.Result {
	return runner.Result{
		ExitCode: 2,
		Output:   []byte("flag provided but not defined: -test.testlogfile\nUsage of a.test:\n"),
	}
}

type recordedAttempt struct {
	logs    []execute.TestLog
	err     error
	verdict string
}

type recordingCall struct {
	name    string
	verdict string
	run     func(context.Context, execute.Options, []execute.TestBinary, []string, bool) recordedAttempt
}

func recordingCalls() []recordingCall {
	return []recordingCall{{
		name:    "exec",
		verdict: mutation.OutcomeKilled.String(),
		run: func(
			ctx context.Context, opts execute.Options, bins []execute.TestBinary, args []string, record bool,
		) recordedAttempt {
			attempt := execute.RunOne(ctx, opts, execute.MutantRun{
				ID: "abc123", Timeout: mutantTimeout, Args: args, RecordTestLog: record,
			}, bins)
			return recordedAttempt{logs: attempt.TestLogs, err: attempt.Err, verdict: attempt.Outcome.String()}
		},
	}, {
		name:    "probe",
		verdict: string(execute.ProbeTestFailed),
		run: func(
			ctx context.Context, opts execute.Options, bins []execute.TestBinary, args []string, record bool,
		) recordedAttempt {
			attempt := execute.RunProbe(ctx, opts, execute.ProbeRun{
				Timeout: mutantTimeout, Args: args, RecordTestLog: record,
				LogPath: filepath.Join(opts.ScratchDir, "infection.log"),
			}, bins)
			return recordedAttempt{logs: attempt.TestLogs, err: attempt.Err, verdict: string(attempt.Outcome)}
		},
	}, {
		name:    "control",
		verdict: "failed",
		run: func(
			ctx context.Context, opts execute.Options, bins []execute.TestBinary, args []string, record bool,
		) recordedAttempt {
			attempt := execute.RunControl(ctx, opts, execute.ControlRun{
				Timeout: mutantTimeout, Args: args, RecordTestLog: record,
			}, bins)
			verdict := ""
			if attempt.Package != "" {
				verdict = "failed"
			}
			return recordedAttempt{logs: attempt.TestLogs, err: attempt.Err, verdict: verdict}
		},
	}}
}

func recordingOptions(t *testing.T, f *fake) execute.Options {
	t.Helper()
	opts := options(f, 1)
	opts.ScratchDir = t.TempDir()
	return opts
}

func testLogArgument(c call) string {
	for _, argument := range c.Argv {
		if strings.HasPrefix(argument, testLogFlag) {
			return argument
		}
	}
	return ""
}

func TestTestLogFlagPrecedesCallerArgs(t *testing.T) {
	for _, test := range recordingCalls() {
		t.Run(test.name, func(t *testing.T) {
			f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
			bins := testBins("example.com/a")

			test.run(t.Context(), recordingOptions(t, f), bins, []string{"-test.run=^TestX$"}, true)

			seen := f.seen()
			if len(seen) != 1 {
				t.Fatalf("started %d binaries, want 1", len(seen))
			}
			argv := seen[0].Argv
			timeout := slices.IndexFunc(argv, func(a string) bool {
				return strings.HasPrefix(a, "-test.timeout=")
			})
			log := slices.IndexFunc(argv, func(a string) bool { return strings.HasPrefix(a, testLogFlag) })
			caller := slices.Index(argv, "-test.run=^TestX$")
			switch {
			case timeout != 1:
				t.Errorf("argv = %q, want the harness timeout right after the binary", argv)
			case log < 0:
				t.Errorf("argv = %q, want the engine's %s in it", argv, testLogFlag)
			case caller < 0:
				t.Errorf("argv = %q, want the caller's own argument in it", argv)
			case log > caller:
				t.Errorf("argv = %q, want %s before the caller's own argument, as cmd/go places it",
					argv, testLogFlag)
			case caller != len(argv)-1:
				t.Errorf("argv = %q, want the caller's own argument last", argv)
			}
		})
	}
}

func TestNoRecordingAddsNoFlag(t *testing.T) {
	for _, test := range recordingCalls() {
		t.Run(test.name, func(t *testing.T) {
			f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
			bins := testBins("example.com/a", "example.com/b")
			caller := "-test.testlogfile=" + filepath.Join(t.TempDir(), "caller.log")

			attempt := test.run(t.Context(), recordingOptions(t, f), bins,
				[]string{"-test.run=^TestX$", caller}, false)

			if attempt.logs != nil {
				t.Errorf("TestLogs = %+v, want nil for a call that was not asked to record", attempt.logs)
			}
			for _, c := range f.seen() {
				if got := testLogArgument(c); got != caller {
					t.Errorf("argv = %q, want the caller's own %q and no flag of the engine's", c.Argv, caller)
				}
			}
		})
	}
}

func TestTestLogPathsAreOnePerBinaryUnderTheScratch(t *testing.T) {
	for _, test := range recordingCalls() {
		t.Run(test.name, func(t *testing.T) {
			f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
			bins := testBins("example.com/a", "example.com/b", "example.com/c")
			opts := recordingOptions(t, f)
			scratch, err := filepath.Abs(opts.ScratchDir)
			if err != nil {
				t.Fatal(err)
			}

			attempt := test.run(t.Context(), opts, bins, nil, true)

			var paths []string
			for _, c := range f.seen() {
				argument := testLogArgument(c)
				if argument == "" {
					t.Fatalf("the binary %s was started without a log to write: %q", c.program(), c.Argv)
				}
				path := strings.TrimPrefix(argument, testLogFlag)
				if dir, want := filepath.Dir(path), filepath.Join(scratch, testLogDir); dir != want {
					t.Errorf("the log %s is in %s, want %s", path, dir, want)
				}
				if slices.Contains(paths, path) {
					t.Errorf("two binaries were told to write %s; a log two processes wrote"+
						" cannot be attributed to either", path)
				}
				paths = append(paths, path)
			}
			if len(paths) != len(bins) {
				t.Fatalf("started %d binaries, want %d", len(paths), len(bins))
			}
			if len(attempt.logs) != len(bins) {
				t.Fatalf("TestLogs has %d entries, want one per binary started (%d): %+v",
					len(attempt.logs), len(bins), attempt.logs)
			}
			for i, log := range attempt.logs {
				if log.Package != bins[i].ImportPath || log.Dir != bins[i].Dir {
					t.Errorf("TestLogs[%d] = %+v, want the binary %s ran in %s",
						i, log, bins[i].ImportPath, bins[i].Dir)
				}
				if log.Err == "" || log.Complete || len(log.Entries) != 0 {
					t.Errorf("TestLogs[%d] = %+v, want a reason and no entries for a binary"+
						" that wrote no log", i, log)
				}
			}
		})
	}
}

func TestTestLogUnsupportedIsDetected(t *testing.T) {
	for _, test := range recordingCalls() {
		t.Run(test.name, func(t *testing.T) {
			f := &fake{respond: func(context.Context, call) runner.Result {
				return undefinedFlagRefusal()
			}}
			bins := testBins("example.com/a")

			attempt := test.run(t.Context(), recordingOptions(t, f), bins, nil, true)

			if !errors.Is(attempt.err, testlog.ErrUnsupported) {
				t.Fatalf("err = %v, want it to carry testlog.ErrUnsupported", attempt.err)
			}
			if got := execute.CodeOf(attempt.err); got != execute.CodeTestLogUnsupported {
				t.Errorf("code = %q, want %q", got, execute.CodeTestLogUnsupported)
			}
			if attempt.verdict == test.verdict {
				t.Errorf("the call reported %q; a binary that does not know the flag ran no test,"+
					" so it decided nothing", attempt.verdict)
			}
			if len(attempt.logs) != 1 {
				t.Fatalf("TestLogs = %+v, want the one binary that was started", attempt.logs)
			}
		})
	}
}

func TestTestLogUnsupportedNeedsTheFlagToHaveBeenPassed(t *testing.T) {
	for _, test := range recordingCalls() {
		t.Run(test.name, func(t *testing.T) {
			f := &fake{respond: func(context.Context, call) runner.Result {
				return undefinedFlagRefusal()
			}}
			bins := testBins("example.com/a")

			attempt := test.run(t.Context(), recordingOptions(t, f), bins,
				[]string{"-test.fuzz=^FuzzX$"}, true)

			if attempt.err != nil {
				t.Fatalf("err = %v, want none: nothing of go-mutants' was on that command line",
					attempt.err)
			}
			if attempt.verdict != test.verdict {
				t.Errorf("the call reported %q, want %q: a target that exits 2 having been given"+
					" no flag of ours failed on its own", attempt.verdict, test.verdict)
			}
		})
	}
}

func TestKilledTargetNeverReportsACompleteLog(t *testing.T) {
	for _, test := range recordingCalls() {
		for _, torn := range []struct {
			name   string
			result runner.Result
		}{
			{"timed out", timedOut()},
			{"cancelled", cancelled()},
		} {
			t.Run(test.name+"/"+torn.name, func(t *testing.T) {
				f := &fake{respond: func(_ context.Context, c call) runner.Result {
					path := strings.TrimPrefix(testLogArgument(c), testLogFlag)
					if err := os.WriteFile(path, []byte("# test log\ngetenv HOME\n"), 0o600); err != nil {
						t.Errorf("writing the fake's log: %v", err)
					}
					return torn.result
				}}
				bins := testBins("example.com/a")

				attempt := test.run(t.Context(), recordingOptions(t, f), bins, nil, true)

				if len(attempt.logs) != 1 {
					t.Fatalf("TestLogs = %+v, want the one binary that was started", attempt.logs)
				}
				if attempt.logs[0].Complete {
					t.Errorf("TestLogs[0] = %+v, want Complete false: the supervisor killed this"+
						" binary, so the log ends where the last flush did and not where the"+
						" target did", attempt.logs[0])
				}
				if len(attempt.logs[0].Entries) == 0 {
					t.Error("the entries that were flushed were dropped; they are what the target" +
						" is known to have touched")
				}
			})
		}
	}
}

func TestTestLogOptOutsCarryAReason(t *testing.T) {
	for _, test := range recordingCalls() {
		for _, opt := range []struct {
			name    string
			args    []string
			options func(*testing.T, *fake) execute.Options
		}{{
			name:    "a fuzz target",
			args:    []string{"-test.fuzz=^FuzzX$"},
			options: recordingOptions,
		}, {
			name:    "no scratch directory",
			options: func(_ *testing.T, f *fake) execute.Options { return options(f, 1) },
		}} {
			t.Run(test.name+"/"+opt.name, func(t *testing.T) {
				f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
				bins := testBins("example.com/a")

				attempt := test.run(t.Context(), opt.options(t, f), bins, opt.args, true)

				for _, c := range f.seen() {
					if got := testLogArgument(c); got != "" {
						t.Errorf("argv carries %q; this call records nothing and must add nothing", got)
					}
				}
				if len(attempt.logs) != 1 {
					t.Fatalf("TestLogs = %+v, want one record for the binary that was started",
						attempt.logs)
				}
				if attempt.logs[0].Err == "" {
					t.Errorf("TestLogs[0] = %+v, want a reason: a record with neither entries nor"+
						" a reason reads as a target that touched nothing", attempt.logs[0])
				}
			})
		}
	}
}

func TestRecordingRefusesACallerSuppliedFlag(t *testing.T) {
	for _, test := range recordingCalls() {
		t.Run(test.name, func(t *testing.T) {
			f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
			bins := testBins("example.com/a")

			attempt := test.run(t.Context(), recordingOptions(t, f), bins,
				[]string{testLogFlag + filepath.Join(t.TempDir(), "caller.log")}, true)

			if attempt.err == nil {
				t.Fatal("the call accepted a target supplying the flag it was asked to set itself")
			}
			if got := execute.CodeOf(attempt.err); got == "" {
				t.Errorf("err = %v, want a code from this package", attempt.err)
			}
			if len(f.seen()) != 0 {
				t.Errorf("started %q; the refusal happens before any binary runs", f.programs())
			}
		})
	}
}
