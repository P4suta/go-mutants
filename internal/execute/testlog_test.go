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

// testLogFlag is the flag the engine adds for itself when a call asks for the
// action log. It is spelled out rather than built from a constant, because what
// these tests pin is the argument a test binary really receives.
const testLogFlag = "-test.testlogfile="

// testLogDir is the subdirectory of a call's scratch the logs are written into.
//
// It is a directory of its own because the scratch is the target's own TMPDIR:
// a test that lists its temporary directory would otherwise find a file
// go-mutants put there, and the whole point of the private scratch is that what
// is in it came from the target.
const testLogDir = "testlogs"

// undefinedFlagRefusal is what the standard flag package prints and the status
// it exits with when a binary does not define the flag it was handed.
func undefinedFlagRefusal() runner.Result {
	return runner.Result{
		ExitCode: 2,
		Output:   []byte("flag provided but not defined: -test.testlogfile\nUsage of a.test:\n"),
	}
}

// A recordedAttempt is the part of an attempt these tests are about, so that
// one table can make the same claim about the three calls that record.
type recordedAttempt struct {
	logs    []execute.TestLog
	err     error
	verdict string
}

// A recordingCall is one of [execute.RunOne], [execute.RunProbe] and
// [execute.RunControl], reduced to that.
//
// The three are tabled together deliberately. A flag one of them inserted and
// another did not, or an unsupported binary one of them called a failing suite,
// would be a difference between a mutant run and the control beside it — which
// is the one difference this layer exists not to have.
type recordingCall struct {
	name string
	// verdict is what this call would report if it mistook a binary that does
	// not know the flag for a suite that failed.
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

// recordingOptions is a fake-backed [execute.Options] with a scratch directory,
// which is where a recorded action log has to live.
func recordingOptions(t *testing.T, f *fake) execute.Options {
	t.Helper()
	opts := options(f, 1)
	opts.ScratchDir = t.TempDir()
	return opts
}

// testLogArgument returns the -test.testlogfile argument one call carried, or
// "" when it carried none.
func testLogArgument(c call) string {
	for _, argument := range c.Argv {
		if strings.HasPrefix(argument, testLogFlag) {
			return argument
		}
	}
	return ""
}

// TestTestLogFlagPrecedesCallerArgs is the placement, which is cmd/go's.
//
// cmd/go writes the binary, then its own -test.testlogfile, then the user's
// arguments, and the order is not cosmetic: a target that repeats a flag the
// engine set would win, because the standard flag package keeps the last
// value seen. Putting the engine's first is what makes a caller's own
// -test.run land after it rather than under it, and it keeps the harness's
// own -test.timeout first, where the paired-timeout guarantee needs it.
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
			if len(argv) != 4 {
				t.Fatalf("argv = %q, want the binary, the timeout, the log and the caller's argument", argv)
			}
			if !strings.HasPrefix(argv[1], "-test.timeout=") {
				t.Errorf("argv[1] = %q, want the harness timeout first", argv[1])
			}
			if !strings.HasPrefix(argv[2], testLogFlag) {
				t.Errorf("argv[2] = %q, want %s, as cmd/go places it", argv[2], testLogFlag)
			}
			if argv[3] != "-test.run=^TestX$" {
				t.Errorf("argv[3] = %q, want the caller's own argument last", argv[3])
			}
		})
	}
}

// TestNoRecordingAddsNoFlag is the other half, and it is what keeps the
// feature opt-in.
//
// A consumer that smuggles its own -test.testlogfile through Args — which is
// what goatest does today — must go on receiving the binary it always
// received, so a call that was not asked to record adds nothing at all and
// reports no logs. Nil rather than an empty slice: nil is "nothing was
// recorded", and an empty slice would read as "nothing was touched".
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

// TestTestLogPathsAreOnePerBinaryUnderTheScratch is the isolation.
//
// Every binary of one call writes its own file, in the private directory that
// call already owns. One shared path would be two processes appending to one
// log — and a log two binaries wrote cannot be told apart, so an entry would
// be attributed to whichever package happened not to have produced it. The
// scratch is where it goes because that is the directory the call removes, so
// a log outlives nothing it was not asked to.
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
				// The fake starts no process, so no binary wrote a log. That is
				// reported as a reason rather than as an empty measurement: a
				// log nobody wrote and a log recording nothing are different
				// answers, and only one of them says a test touched nothing.
				if log.Err == "" || log.Complete || len(log.Entries) != 0 {
					t.Errorf("TestLogs[%d] = %+v, want a reason and no entries for a binary"+
						" that wrote no log", i, log)
				}
			}
		})
	}
}

// TestTestLogUnsupportedIsDetected is the one failure the flag can cause, and
// the one it must never be mistaken for.
//
// A binary that does not define the flag it was handed is refused by the
// standard flag package: it prints "flag provided but not defined" and exits 2.
// Exit 2 is a non-zero status, and a non-zero status is how this layer
// recognises a kill — so without this branch a run that asked for the log would
// report every mutant as detected by a binary that never started a test. It
// comes back as a failure carrying [testlog.ErrUnsupported] instead.
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

// TestTestLogUnsupportedNeedsTheFlagToHaveBeenPassed is the guard on the guard.
//
// The refusal above is recognised from a status and a sentence on stderr, and
// neither belongs to go-mutants: a target that exits 2 having printed that line
// for its own reasons — a test asserting on a flag package's diagnostics, a
// suite that shells out to a binary of its own — is a *kill*. The branch may
// therefore only fire for a binary this call actually handed the flag to, and
// the calls that opt out of recording while still being asked for it are
// precisely where that goes wrong: a fuzz target is given no flag at all, so an
// exit 2 from one is the target's own and nothing else.
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

// TestKilledTargetNeverReportsACompleteLog is the trap a buffer size sets.
//
// The testing package writes the log through a 4096-byte bufio.Writer, which
// flushes whenever it fills — so a chatty target the supervisor tore down
// leaves a log that ends at a line boundary and is nonetheless a fraction of
// what it touched. Read as complete, that is a consumer keying a cache on a
// prefix of a target's inputs, which is worse than having no log at all: it
// looks like an answer.
//
// So the bytes are not the whole test. A binary the supervisor killed — on the
// budget, or through a cancelled context — reports Complete false whatever the
// last byte is.
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
					// Exactly what a mid-run flush leaves: the header, whole
					// lines, and a final newline.
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

// TestTestLogOptOutsCarryAReason pins the two calls that are asked to record
// and cannot.
//
// Both answer with a record whose Err says why, and with no flag on the command
// line. The record is what makes them safe: a call that quietly returned no
// logs at all would be indistinguishable from one that was never asked, and a
// call that returned an empty measurement would say the target touched nothing.
func TestTestLogOptOutsCarryAReason(t *testing.T) {
	for _, test := range recordingCalls() {
		for _, opt := range []struct {
			name    string
			args    []string
			options func(*testing.T, *fake) execute.Options
		}{{
			// The coordinator starts its workers with its own arguments, so a
			// worker would inherit the flag and recreate the coordinator's file.
			name:    "a fuzz target",
			args:    []string{"-test.fuzz=^FuzzX$"},
			options: recordingOptions,
		}, {
			// Nowhere private to write. The log would land in whatever
			// directory happened to be current, which is not this call's to
			// remove.
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

// TestRecordingRefusesACallerSuppliedFlag is this layer's own refusal, and it
// is here for the reason the -test.timeout refusal is.
//
// The public session refuses the flag before it makes a scratch directory, with
// a message about the request. This is the second lock on the same door: two
// -test.testlogfile arguments are not two logs, since the standard flag package
// keeps the last value it sees, and a caller of this package that is not the
// session would otherwise compose a command line whose log the engine then went
// and read as its own.
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
