// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// The tests that kill a process tree share one hazard, and these constants and
// the helpers under them are the whole answer to it.
//
// Such a test is meaningful only if the tree exists by the time the kill lands.
// A child here is a whole Go test binary, coverage-instrumented under
// `go test -cover`, booting on a machine where twenty-odd sibling packages are
// booting at the same moment: measured on this one it announces its grandchild
// within a few tens of milliseconds, and a single load spike pushed one past
// 400ms. Whenever it loses that race the test has proved nothing, and it says
// so and fails rather than passing quietly — which is right, and which is also
// what made these tests flaky under whole-suite load.
//
// The fix is not a bigger number. The deadline is doing two jobs at once and
// only one of them is any test's subject: it is the moment the kill lands, and
// it is unavoidably also the tolerance for how long a child takes to boot. A
// single constant serving both cannot be right — generous enough for the second
// job makes every run pay for the worst machine, because a child that sleeps for
// a minute never exits early and the run always takes the whole deadline. So the
// deadline starts small and doubles only on the attempts that lost the race,
// and the delay a grandchild waits before writing is *derived* from the deadline
// in use rather than asserted against it in a comment.

const (
	// firstKillDeadline is the deadline the first attempt uses. It is already
	// an order of magnitude above the typical boot-and-fork, so an unloaded
	// machine never reaches a second attempt.
	firstKillDeadline = 1500 * time.Millisecond

	// spawnBudget is the ceiling the deadline doubles towards before
	// [underDoublingDeadline] gives up and fails.
	//
	// It is a ceiling on waiting, not a cost: reaching it takes four attempts,
	// and an attempt only happens because the one before it caught a child that
	// had not yet got where it was going.
	spawnBudget = 15 * time.Second

	// killSettle is the gap between the moment a kill has certainly finished —
	// the deadline plus [runner.TerminationGrace] — and the moment a grandchild
	// that survived it would write. The grace is counted on both platforms
	// although only POSIX has one, because Windows terminating immediately only
	// widens the gap.
	killSettle = 1500 * time.Millisecond

	// sentinelWriteMargin is added on top of a grandchild's own delay before
	// absence is read as proof. The delay is measured from the grandchild's
	// start and a test can only measure from the run's, and the difference is
	// the child booting, forking, and then the grandchild booting: two whole Go
	// test binaries, not one.
	//
	// It is three seconds rather than the one this used to be because the error
	// here is silent and in the wrong direction. Looking too early at a
	// grandchild that escaped and is merely late reports a pass, so the margin
	// has to cover the same load spike the deadline above is sized against —
	// and a margin the same size as a single measured boot did not.
	sentinelWriteMargin = 3 * time.Second
)

// sentinelDelayFor is how long a grandchild waits before writing its sentinel,
// derived from the deadline the attempt is actually using.
//
// It is a function rather than a constant because that inequality — the write
// must fall after the kill has finished, or "the file is not there" would mean
// "we looked too early" rather than "the kill worked" — used to live in a
// comment two constants apart from the number that could break it. Here it
// cannot be broken by changing a deadline.
func sentinelDelayFor(deadline time.Duration) time.Duration {
	return deadline + runner.TerminationGrace + killSettle
}

// underDoublingDeadline calls attempt with a kill deadline that starts at
// [firstKillDeadline] and doubles until attempt reports it caught what it was
// trying to catch, or until the next doubling would pass [spawnBudget].
//
// The loudness the flakiness came with is kept and sharpened: a genuinely
// broken spawn reports nothing at any deadline, and this fails by name rather
// than letting a test that proved nothing pass.
func underDoublingDeadline(t *testing.T, attempt func(deadline, sentinelDelay time.Duration) bool) {
	t.Helper()

	for deadline := firstKillDeadline; ; deadline *= 2 {
		if attempt(deadline, sentinelDelayFor(deadline)) {
			return
		}
		if 2*deadline > spawnBudget {
			t.Fatalf("no attempt caught a child that had reached the thing under test, at any kill "+
				"deadline from %v up to %v: that is not load, it is a child that never gets there",
				firstKillDeadline, deadline)
		}
		t.Logf("a %v kill deadline landed before the child was ready; retrying at %v", deadline, 2*deadline)
	}
}

// milliseconds renders a duration the way every helper verb parses one.
func milliseconds(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10)
}

// waitForFile polls until path exists, and reports how it gave up if it does
// not appear inside within.
//
// A single os.Stat is only sound when something else has already proved the
// writer finished. Where the writer is another freshly booting Go test binary,
// watching is what keeps a positive control from becoming a second timing race.
func waitForFile(t *testing.T, path string, within time.Duration) error {
	t.Helper()

	const poll = 20 * time.Millisecond
	deadline := time.Now().Add(within)
	for {
		switch _, err := os.Stat(path); {
		case err == nil:
			return nil
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("watching for %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never appeared within %v", path, within)
		}
		time.Sleep(poll)
	}
}

// noSentinelSurvived asserts that no attempt's grandchild ever wrote.
//
// Every attempt is checked, not only the one that finally caught the fork: a
// grandchild that escaped the kill on the first attempt is exactly the bug
// these tests exist for, whichever attempt ended up proving the spawn.
func noSentinelSurvived(t *testing.T, sentinels []string, outlived string) {
	t.Helper()

	for i, sentinel := range sentinels {
		if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("attempt %d: os.Stat(sentinel) = %v, want os.ErrNotExist: %s", i, err, outlived)
		}
	}
}

// TestExitCodePropagation pins the contract the engine classifies mutants
// with: whatever the child exits with comes back untouched, and none of it is
// an error. 97 is in the table because the generated runtime package uses it
// to refuse a stale catalogue, and the engine has to be able to see it.
func TestExitCodePropagation(t *testing.T) {
	t.Parallel()

	for _, want := range []int{0, 1, 2, 3, 42, 97, 255} {
		t.Run(strconv.Itoa(want), func(t *testing.T) {
			t.Parallel()

			result := runner.Run(t.Context(), runner.Spec{
				Argv: helperCommand(t, "exit", strconv.Itoa(want)),
				Env:  helperEnviron(),
			})
			if result.Err != nil {
				t.Fatalf("Err = %v, want nil: a child that ran and failed is not an error", result.Err)
			}
			if result.TimedOut {
				t.Error("TimedOut = true, want false")
			}
			if result.ExitCode != want {
				t.Errorf("ExitCode = %d, want %d", result.ExitCode, want)
			}
			if got := result.OK(); got != (want == 0) {
				t.Errorf("OK() = %v, want %v", got, want == 0)
			}
			if result.Duration <= 0 {
				t.Errorf("Duration = %v, want a positive measurement", result.Duration)
			}
		})
	}
}

// TestCombinedOutputKeepsWriteOrder proves that stdout and stderr share one
// pipe. The helper writes to stdout and then to stderr, so anything other than
// exact concatenation would mean two readers were racing and the captured
// evidence for a failure could be reordered.
func TestCombinedOutputKeepsWriteOrder(t *testing.T) {
	t.Parallel()

	result := runner.Run(t.Context(), runner.Spec{
		Argv: helperCommand(t, "emit", "OUT-alpha", "ERR-beta"),
		Env:  helperEnviron(),
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if got, want := string(result.Output), "OUT-alphaERR-beta"; got != want {
		t.Errorf("Output = %q, want %q", got, want)
	}
}

// TestOutputUnderLimitIsExact checks the uncapped path: no notice, no copy of
// somebody else's buffer, byte-for-byte what the child wrote.
func TestOutputUnderLimitIsExact(t *testing.T) {
	t.Parallel()

	const size = 4000
	result := runner.Run(t.Context(), runner.Spec{
		Argv:        helperCommand(t, "spam", strconv.Itoa(size)),
		Env:         helperEnviron(),
		OutputLimit: 1 << 16,
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if !bytes.Equal(result.Output, spamPayload(size)) {
		t.Errorf("Output (%d bytes) is not the payload the child wrote (%d bytes)",
			len(result.Output), size)
	}
	if strings.Contains(string(result.Output), runner.OutputTruncatedPrefix) {
		t.Error("Output carries a truncation notice although nothing was dropped")
	}
}

// TestOutputIsTailCapped covers the three things a cap has to get right at
// once: the total stays inside the budget the caller set, the notice says
// bytes went missing, and the bytes that survived are the *last* ones — the
// end of a test's output is where the assertion failure is.
func TestOutputIsTailCapped(t *testing.T) {
	t.Parallel()

	const (
		produced = 300_000
		limit    = 4096
	)
	result := runner.Run(t.Context(), runner.Spec{
		Argv:        helperCommand(t, "spam", strconv.Itoa(produced)),
		Env:         helperEnviron(),
		OutputLimit: limit,
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if len(result.Output) > limit {
		t.Errorf("len(Output) = %d, want at most OutputLimit = %d: the notice is paid for out of the budget",
			len(result.Output), limit)
	}

	notice := runner.TruncationNotice(produced)
	if !strings.HasPrefix(string(result.Output), notice) {
		t.Fatalf("Output does not begin with the truncation notice %q; it begins %q",
			notice, string(result.Output[:min(len(result.Output), 120)]))
	}
	if !strings.HasPrefix(notice, runner.OutputTruncatedPrefix) {
		t.Errorf("notice %q does not begin with the documented prefix %q",
			notice, runner.OutputTruncatedPrefix)
	}

	tail := result.Output[len(notice):]
	payload := spamPayload(produced)
	if !bytes.Equal(tail, payload[len(payload)-len(tail):]) {
		t.Errorf("the kept %d bytes are not the tail of what the child wrote", len(tail))
	}
	if len(tail) != limit-len(notice) {
		t.Errorf("kept %d bytes, want %d: the whole remaining budget should be used",
			len(tail), limit-len(notice))
	}
}

// TestOutputIsTailCappedAcrossManyWrites covers the other half of the capture,
// and it is the half with the arithmetic in it.
//
// A capture has two ways to reach the same answer. When one write is at least
// as large as the limit, the tail of that write is the tail of everything and
// the buffer is simply replaced — that is the path TestOutputIsTailCapped
// takes, because its 4096-byte limit is smaller than the chunks the helper
// writes. When every write is smaller than the limit, the bytes accumulate and
// the buffer has to be compacted back down as it grows, sliding the kept window
// forward. Getting that index wrong would not fail loudly; it would silently
// hand a report the wrong bytes of a failing test's output.
//
// The limit is deliberately larger than the 32 KiB buffer os/exec copies the
// child's pipe with, so that no single write can reach it and the accumulating
// path is the one taken — measured against the coverage profile, not assumed.
func TestOutputIsTailCappedAcrossManyWrites(t *testing.T) {
	t.Parallel()

	const (
		produced = 300_000
		limit    = 1 << 16
	)
	result := runner.Run(t.Context(), runner.Spec{
		Argv:        helperCommand(t, "spam", strconv.Itoa(produced)),
		Env:         helperEnviron(),
		OutputLimit: limit,
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if len(result.Output) > limit {
		t.Errorf("len(Output) = %d, want at most OutputLimit = %d", len(result.Output), limit)
	}

	notice := runner.TruncationNotice(produced)
	if !strings.HasPrefix(string(result.Output), notice) {
		t.Fatalf("Output does not begin with the truncation notice %q; it begins %q",
			notice, string(result.Output[:min(len(result.Output), 120)]))
	}

	tail := result.Output[len(notice):]
	if len(tail) != limit-len(notice) {
		t.Errorf("kept %d bytes, want %d: the whole remaining budget should be used",
			len(tail), limit-len(notice))
	}
	payload := spamPayload(produced)
	if !bytes.Equal(tail, payload[len(payload)-len(tail):]) {
		t.Errorf("the kept %d bytes are not the tail of what the child wrote; "+
			"the compaction slid the window to the wrong offset", len(tail))
	}
}

// TestEffectiveOutputLimit pins the defaulting rule, including the floor that
// keeps len(Output) <= OutputLimit satisfiable at all.
func TestEffectiveOutputLimit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   int
		want int
	}{
		{name: "zero takes the default", in: 0, want: runner.DefaultOutputLimit},
		{name: "negative takes the default", in: -1, want: runner.DefaultOutputLimit},
		{name: "below the floor is raised", in: 1, want: runner.MinOutputLimit},
		{name: "the floor itself is kept", in: runner.MinOutputLimit, want: runner.MinOutputLimit},
		{name: "above the floor is kept", in: 5000, want: 5000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := runner.EffectiveOutputLimit(tc.in); got != tc.want {
				t.Errorf("EffectiveOutputLimit(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestRunReportsTotalBytesAndTruncation pins the two facts a capped capture
// has to hand back beside the bytes it kept: how much the child actually
// produced, and whether anything was dropped.
//
// Both are here because the notice alone cannot carry them. A consumer that
// wanted to know whether output was lost had to match the first line against a
// string go-mutants formats, which makes a diagnostic line into a wire format —
// and a consumer that wanted the total had to parse a number back out of that
// same sentence. Truncated is the fact; the notice is presentation.
//
// The uncapped half is the one worth stating outright: OutputBytes is not "the
// number that appears when something was lost". It is always what the child
// wrote, so a caller can report a size without first asking whether the cap bit.
func TestRunReportsTotalBytesAndTruncation(t *testing.T) {
	t.Parallel()

	t.Run("a capture that lost bytes", func(t *testing.T) {
		t.Parallel()

		const (
			produced = 100 << 10
			limit    = 4096
		)
		result := runner.Run(t.Context(), runner.Spec{
			Argv:        helperCommand(t, "spam", strconv.Itoa(produced)),
			Env:         helperEnviron(),
			OutputLimit: limit,
		})
		if result.Err != nil {
			t.Fatalf("Err = %v, want nil", result.Err)
		}
		if !result.Truncated {
			t.Errorf("Truncated = false for a child that wrote %d bytes into a %d-byte budget",
				produced, limit)
		}
		if result.OutputBytes != produced {
			t.Errorf("OutputBytes = %d, want %d: the total is what the child produced, not what was kept",
				result.OutputBytes, produced)
		}
		if len(result.Output) > limit {
			t.Errorf("len(Output) = %d, want at most %d", len(result.Output), limit)
		}
		if !bytes.HasPrefix(result.Output, []byte(runner.OutputTruncatedPrefix)) {
			t.Errorf("Output begins %q, want the documented prefix %q",
				string(result.Output[:min(len(result.Output), 120)]), runner.OutputTruncatedPrefix)
		}
	})

	// The boundary, which is the one value a reader cannot infer from the cases
	// either side of it. A child that produced exactly the budget lost nothing:
	// the cap is on what is kept, and a notice here would be paid for out of the
	// same budget, dropping bytes that had no need to go.
	t.Run("a capture that filled the budget exactly", func(t *testing.T) {
		t.Parallel()

		const limit = 4096
		result := runner.Run(t.Context(), runner.Spec{
			Argv:        helperCommand(t, "spam", strconv.Itoa(limit)),
			Env:         helperEnviron(),
			OutputLimit: limit,
		})
		if result.Err != nil {
			t.Fatalf("Err = %v, want nil", result.Err)
		}
		if result.Truncated {
			t.Errorf("Truncated = true although the child produced exactly the %d-byte budget", limit)
		}
		if result.OutputBytes != limit {
			t.Errorf("OutputBytes = %d, want %d", result.OutputBytes, limit)
		}
		if len(result.Output) != limit {
			t.Errorf("len(Output) = %d, want all %d bytes: nothing was dropped", len(result.Output), limit)
		}
	})

	t.Run("a capture that lost nothing", func(t *testing.T) {
		t.Parallel()

		const (
			produced = 400
			limit    = 4096
		)
		result := runner.Run(t.Context(), runner.Spec{
			Argv:        helperCommand(t, "spam", strconv.Itoa(produced)),
			Env:         helperEnviron(),
			OutputLimit: limit,
		})
		if result.Err != nil {
			t.Fatalf("Err = %v, want nil", result.Err)
		}
		if result.Truncated {
			t.Errorf("Truncated = true although %d bytes fit inside the %d-byte budget", produced, limit)
		}
		if result.OutputBytes != int64(len(result.Output)) {
			t.Errorf("OutputBytes = %d, want len(Output) = %d when nothing was dropped",
				result.OutputBytes, len(result.Output))
		}
		if result.OutputBytes != produced {
			t.Errorf("OutputBytes = %d, want %d", result.OutputBytes, produced)
		}
	})
}

// TestTimeoutReportsTimedOut checks the timeout path itself: the run comes
// back near the deadline rather than near the child's own lifetime, TimedOut
// says why, and the exit status is withheld because a killed tree has none
// worth reporting.
func TestTimeoutReportsTimedOut(t *testing.T) {
	t.Parallel()

	const timeout = 300 * time.Millisecond
	start := time.Now()
	result := runner.Run(t.Context(), runner.Spec{
		Argv:    helperCommand(t, "sleep", "60000"),
		Env:     helperEnviron(),
		Timeout: timeout,
	})
	elapsed := time.Since(start)

	if result.Err != nil {
		t.Fatalf("Err = %v, want nil: a timeout is a result, not a failure to run", result.Err)
	}
	if !result.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	if result.ExitCode != runner.ExitCodeUnavailable {
		t.Errorf("ExitCode = %d, want ExitCodeUnavailable (%d)", result.ExitCode, runner.ExitCodeUnavailable)
	}
	if result.OK() {
		t.Error("OK() = true for a timed-out run")
	}
	if result.Duration < timeout {
		t.Errorf("Duration = %v, want at least the timeout %v", result.Duration, timeout)
	}
	if elapsed > 30*time.Second {
		t.Errorf("Run took %v; it waited for the child instead of killing it", elapsed)
	}
}

// TestNoTimeoutRunsToCompletion pins that a zero Timeout means no deadline
// rather than an immediate one.
func TestNoTimeoutRunsToCompletion(t *testing.T) {
	t.Parallel()

	result := runner.Run(t.Context(), runner.Spec{
		Argv: helperCommand(t, "sleep", "150"),
		Env:  helperEnviron(),
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if result.TimedOut {
		t.Error("TimedOut = true with Timeout unset")
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
}

// TestTimeoutKillsTheProcessTree is the test this package exists for.
//
// The child spawns a grandchild that will write a sentinel file well after the
// deadline, then outlives it. Killing only the child leaves the grandchild
// running and the sentinel appears; killing the tree means it never does.
//
// Absence proves nothing on its own — a grandchild that failed to start would
// also leave no file — so this test does two things about that. The negative
// case asserts the child announced the grandchild's pid on the captured
// output, which can only have happened in this very run, and the positive
// control runs the same helper with no deadline and requires the sentinel to
// appear.
func TestTimeoutKillsTheProcessTree(t *testing.T) {
	t.Parallel()

	t.Run("positive control: the sentinel appears when nothing kills it", func(t *testing.T) {
		t.Parallel()

		sentinel := filepath.Join(t.TempDir(), "sentinel")
		result := runner.Run(t.Context(), runner.Spec{
			// The grandchild writes at 200ms, the child leaves at 600ms.
			Argv: helperCommand(t, "tree", sentinel, "200", "600"),
			Env:  helperEnviron(),
		})
		if result.Err != nil {
			t.Fatalf("Err = %v, want nil", result.Err)
		}
		if result.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0; output: %s", result.ExitCode, result.Output)
		}
		// Watched for rather than stat'ed once. The grandchild is a whole Go
		// test binary too, and on a busy machine it can still be booting when
		// its parent's own 600ms are up and Run has returned; a control that
		// looked exactly once would be a second timing race rather than a
		// control.
		if err := waitForFile(t, sentinel, spawnBudget); err != nil {
			t.Fatalf("the sentinel is missing after an unkilled run: %v.\n"+
				"The negative cases below cannot mean anything until this passes; output: %s",
				err, result.Output)
		}
	})

	t.Run("the sentinel never appears after a timeout", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		var sentinels []string
		var conclusive time.Time

		underDoublingDeadline(t, func(deadline, sentinelDelay time.Duration) bool {
			sentinel := filepath.Join(dir, "sentinel-"+strconv.Itoa(len(sentinels)))
			sentinels = append(sentinels, sentinel)

			start := time.Now()
			result := runner.Run(t.Context(), runner.Spec{
				Argv:    helperCommand(t, "tree", sentinel, milliseconds(sentinelDelay), "60000"),
				Env:     helperEnviron(),
				Timeout: deadline,
			})
			// Attempts only ever start later and wait longer, so the last one
			// sets the moment after which absence is conclusive for all of them.
			conclusive = start.Add(sentinelDelay + sentinelWriteMargin)

			if result.Err != nil {
				t.Fatalf("Err = %v, want nil", result.Err)
			}
			if !result.TimedOut {
				t.Fatalf("TimedOut = false, want true; output: %s", result.Output)
			}
			// A child that never reported a grandchild proves nothing about
			// killing a tree, so it is a retry rather than a verdict.
			return strings.Contains(string(result.Output), "grandchild ")
		})

		// Wait past the moment the grandchild would have written, so that
		// "the file is not there" means "it is never going to be there".
		time.Sleep(time.Until(conclusive))
		noSentinelSurvived(t, sentinels, "the grandchild outlived the timeout")
	})

	// This is the regression test. The single-run case above passes even with
	// a supervisor that adopts the child too late, because on an idle machine
	// the adoption still lands before the child has booted far enough to fork.
	// Load is what exposes it: measured on this machine, sixteen concurrent
	// children pushed the gap between CreateProcess and adoption from under a
	// millisecond to tens of milliseconds, which is longer than a Go binary
	// takes to start and spawn a helper. A grandchild created inside that gap
	// is outside the supervisor and survives the kill.
	t.Run("the sentinel never appears under concurrent load", func(t *testing.T) {
		t.Parallel()

		const concurrency = 8
		dir := t.TempDir()
		var sentinels []string
		var conclusive time.Time
		attempts := 0

		underDoublingDeadline(t, func(deadline, sentinelDelay time.Duration) bool {
			attempt := attempts
			attempts++

			var (
				wg      sync.WaitGroup
				spawned atomic.Int64
			)
			start := time.Now()
			for i := range concurrency {
				sentinel := filepath.Join(dir, fmt.Sprintf("sentinel-%d-%d", attempt, i))
				sentinels = append(sentinels, sentinel)
				argv := helperCommand(t, "tree", sentinel, milliseconds(sentinelDelay), "60000")

				wg.Add(1)
				go func() {
					defer wg.Done()
					result := runner.Run(t.Context(), runner.Spec{
						Argv:    argv,
						Env:     helperEnviron(),
						Timeout: deadline,
					})
					switch {
					case result.Err != nil:
						t.Errorf("attempt %d run %d: Err = %v, want nil", attempt, i, result.Err)
					case !result.TimedOut:
						t.Errorf("attempt %d run %d: TimedOut = false, want true; output: %s",
							attempt, i, result.Output)
					case strings.Contains(string(result.Output), "grandchild "):
						spawned.Add(1)
					}
				}()
			}
			wg.Wait()
			conclusive = start.Add(sentinelDelay + sentinelWriteMargin)

			// The whole burst is retried, never the individual runs that lost
			// the race, because concurrency is this case's subject: a lone
			// retry would not reproduce the adoption gap it was written for.
			return spawned.Load() == concurrency
		})

		time.Sleep(time.Until(conclusive))
		noSentinelSurvived(t, sentinels,
			"a grandchild escaped the supervisor and outlived the kill")
	})
}

// TestTerminationEscalatesToSIGKILL exercises the escalation the POSIX
// supervisor is specified around: SIGTERM, then [runner.TerminationGrace], then
// SIGKILL.
//
// Every other timeout test dies on the first signal, because Go's default
// disposition for SIGTERM terminates the process — so the grace timer and the
// SIGKILL that follows it never run, and the one case the escalation exists for
// is the one nothing covered. That case is a test binary which cannot be asked
// to leave: a deadlocked one, a cgo call that will not return, or the
// deliberately deaf helper used here.
//
// Two assertions make it a test of the escalation rather than of the timeout.
// The child announces itself only after installing the ignore, so a run where
// SIGTERM won the race is reported as such instead of passing quietly; and the
// run must take at least the grace, because coming back sooner would mean the
// polite signal ended it after all.
func TestTerminationEscalatesToSIGKILL(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("TerminateJobObject is immediate by construction; Windows has no grace phase to escalate out of")
	}

	dir := t.TempDir()
	var sentinels []string
	var conclusive time.Time

	underDoublingDeadline(t, func(deadline, sentinelDelay time.Duration) bool {
		sentinel := filepath.Join(dir, "sentinel-"+strconv.Itoa(len(sentinels)))
		sentinels = append(sentinels, sentinel)

		start := time.Now()
		result := runner.Run(t.Context(), runner.Spec{
			Argv:    helperCommand(t, "deaf", sentinel, milliseconds(sentinelDelay)),
			Env:     helperEnviron(),
			Timeout: deadline,
		})
		elapsed := time.Since(start)
		conclusive = start.Add(sentinelDelay + sentinelWriteMargin)

		if result.Err != nil {
			t.Fatalf("Err = %v, want nil: a timeout is a result, not a failure to run", result.Err)
		}
		if !result.TimedOut {
			t.Fatalf("TimedOut = false, want true; output: %s", result.Output)
		}
		if result.ExitCode != runner.ExitCodeUnavailable {
			t.Errorf("ExitCode = %d, want ExitCodeUnavailable (%d)", result.ExitCode, runner.ExitCodeUnavailable)
		}
		// SIGTERM won the race to the disposition, so this run says nothing
		// about the escalation. Retried rather than judged — and the assertions
		// below are skipped with it, because a child that took the polite
		// signal legitimately comes back before the grace has elapsed.
		if !strings.Contains(string(result.Output), helperDeafMarker) {
			return false
		}
		if elapsed < deadline+runner.TerminationGrace {
			t.Errorf("Run returned after %v, before the deadline and the %v grace had both elapsed: "+
				"the child was ended by SIGTERM and SIGKILL was never needed",
				elapsed, runner.TerminationGrace)
		}
		if elapsed > deadline+runner.TerminationGrace+sentinelDelay {
			t.Errorf("Run took %v; it waited for the child's own lifetime instead of killing it", elapsed)
		}
		return true
	})

	// The tree really is gone, not merely unresponsive: wait past the moment
	// the child would have written, so absence means it is never going to.
	time.Sleep(time.Until(conclusive))
	noSentinelSurvived(t, sentinels, "the SIGTERM-deaf child outlived the escalation")
}

// TestCancellationKillsTheProcessTree covers the Ctrl-C path. It is the same
// mechanism as the timeout, and it has to report itself differently: the
// engine drains a cancellation into not-run mutants, and would turn every one
// of them into a reported timeout if TimedOut were set here.
func TestCancellationKillsTheProcessTree(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var sentinels []string
	var conclusive time.Time

	underDoublingDeadline(t, func(deadline, sentinelDelay time.Duration) bool {
		sentinel := filepath.Join(dir, "sentinel-"+strconv.Itoa(len(sentinels)))
		sentinels = append(sentinels, sentinel)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		start := time.Now()
		timer := time.AfterFunc(deadline, cancel)
		defer timer.Stop()

		result := runner.Run(ctx, runner.Spec{
			Argv: helperCommand(t, "tree", sentinel, milliseconds(sentinelDelay), "60000"),
			Env:  helperEnviron(),
		})
		conclusive = start.Add(sentinelDelay + sentinelWriteMargin)

		if result.Err != nil {
			t.Fatalf("Err = %v, want nil: cancellation is not a failure to run", result.Err)
		}
		if result.TimedOut {
			t.Error("TimedOut = true after a cancellation; only Spec.Timeout may set it")
		}
		if result.ExitCode != runner.ExitCodeUnavailable {
			t.Errorf("ExitCode = %d, want ExitCodeUnavailable (%d)", result.ExitCode, runner.ExitCodeUnavailable)
		}
		return strings.Contains(string(result.Output), "grandchild ")
	})

	time.Sleep(time.Until(conclusive))
	noSentinelSurvived(t, sentinels, "the grandchild outlived the cancellation")
}

// TestAlreadyCancelledContextStartsNothing pins the cheap path: no process, no
// error, and a result that reads as "did not run" rather than as "exited 0".
func TestAlreadyCancelledContextStartsNothing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	sentinel := filepath.Join(t.TempDir(), "sentinel")
	result := runner.Run(ctx, runner.Spec{
		Argv: helperCommand(t, "sentinel", sentinel, "0"),
		Env:  helperEnviron(),
	})
	if result.Err != nil {
		t.Errorf("Err = %v, want nil", result.Err)
	}
	if result.TimedOut {
		t.Error("TimedOut = true, want false")
	}
	if result.ExitCode != runner.ExitCodeUnavailable {
		t.Errorf("ExitCode = %d, want ExitCodeUnavailable (%d)", result.ExitCode, runner.ExitCodeUnavailable)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("os.Stat(sentinel) = %v, want os.ErrNotExist: the child ran despite a cancelled context", err)
	}
}

// TestSpecValidation covers the two ways a caller can hand over something that
// is not a command. Both are the caller's bug, so both name a code.
func TestSpecValidation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		argv []string
	}{
		{name: "nil argv", argv: nil},
		{name: "empty argv", argv: []string{}},
		{name: "blank executable", argv: []string{"   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := runner.Run(t.Context(), runner.Spec{Argv: tc.argv})
			if got := runner.CodeOf(result.Err); got != runner.CodeSpecInvalid {
				t.Fatalf("CodeOf(Err) = %q (err %v), want %q", got, result.Err, runner.CodeSpecInvalid)
			}
			if result.ExitCode != runner.ExitCodeUnavailable {
				t.Errorf("ExitCode = %d, want ExitCodeUnavailable (%d)", result.ExitCode, runner.ExitCodeUnavailable)
			}
		})
	}
}

// TestStartFailure covers a command that cannot be started at all. It must
// come back as an error with a code rather than as some invented exit status,
// because "the binary is missing" is an infrastructure fault and a mutant that
// exits non-zero is a kill.
func TestStartFailure(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "no-such-executable")
	result := runner.Run(t.Context(), runner.Spec{Argv: []string{missing}})

	if got := runner.CodeOf(result.Err); got != runner.CodeProcessStartFailed {
		t.Fatalf("CodeOf(Err) = %q (err %v), want %q", got, result.Err, runner.CodeProcessStartFailed)
	}
	if !strings.Contains(result.Err.Error(), runner.CodeProcessStartFailed) {
		t.Errorf("Err = %q, want the code in the rendered message", result.Err)
	}
	// The cause has to survive unwrapping, and which cause it is differs by
	// platform: Windows resolves the executable extension even for a path with
	// separators and reports exec.ErrNotFound, while POSIX hands the path
	// straight to the kernel and reports ENOENT.
	if !errors.Is(result.Err, os.ErrNotExist) && !errors.Is(result.Err, exec.ErrNotFound) {
		t.Errorf("Err = %v, want the operating system cause to survive unwrapping", result.Err)
	}
	if result.ExitCode != runner.ExitCodeUnavailable {
		t.Errorf("ExitCode = %d, want ExitCodeUnavailable (%d)", result.ExitCode, runner.ExitCodeUnavailable)
	}
}

// TestDirBecomesChildWorkingDirectory matters because Go test binaries resolve
// testdata relative to where they run, and the execution phase runs them from
// inside the snapshot rather than from the repository.
func TestDirBecomesChildWorkingDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	result := runner.Run(t.Context(), runner.Spec{
		Argv: helperCommand(t, "cwd"),
		Dir:  dir,
		Env:  helperEnviron(),
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}

	// Resolve both sides: temporary directories are reached through a symlink
	// on macOS and through an 8.3 alias on some Windows configurations.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving %q: %v", dir, err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(result.Output)))
	if err != nil {
		t.Fatalf("resolving %q: %v", result.Output, err)
	}
	if !strings.EqualFold(got, want) {
		t.Errorf("child working directory = %q, want %q", got, want)
	}
}

// TestEnvIsTheWholeEnvironment pins both halves of the Env contract: what the
// caller composes is exactly what the child gets, and nil means inherit.
//
// The first half is the one that matters for correctness. Activation happens
// through GO_MUTANTS_ACTIVE, so a runner that merged the caller's environment
// with its own would let a stale variable from the parent shell decide which
// mutant a test binary runs.
func TestEnvIsTheWholeEnvironment(t *testing.T) {
	// No t.Parallel: t.Setenv is what makes "inherited" meaningful, and the
	// two are mutually exclusive.
	const (
		marker  = "GO_MUTANTS_RUNNER_TEST_MARKER"
		unwant  = "GO_MUTANTS_RUNNER_TEST_LEAK"
		leaking = "this must not reach the child"
	)
	// A child that inherits has to inherit the helper switch too, or it would
	// come up as a second copy of this whole test suite.
	t.Setenv(helperEnv, "1")
	t.Setenv(marker, "inherited")
	t.Setenv(unwant, leaking)

	t.Run("nil inherits this process's environment", func(t *testing.T) {
		result := runner.Run(t.Context(), runner.Spec{
			Argv: helperCommand(t, "env", marker),
			Env:  nil,
		})
		if result.Err != nil {
			t.Fatalf("Err = %v, want nil", result.Err)
		}
		if got := string(result.Output); got != "inherited" {
			t.Errorf("child saw %s=%q, want %q", marker, got, "inherited")
		}
	})

	t.Run("an explicit environment replaces rather than merges", func(t *testing.T) {
		// Everything except the variable the parent is deliberately hiding.
		var composed []string
		for _, entry := range helperEnviron(marker + "=composed") {
			if strings.HasPrefix(entry, unwant+"=") {
				continue
			}
			composed = append(composed, entry)
		}

		result := runner.Run(t.Context(), runner.Spec{
			Argv: helperCommand(t, "env", marker),
			Env:  composed,
		})
		if result.Err != nil {
			t.Fatalf("Err = %v, want nil", result.Err)
		}
		if got := string(result.Output); got != "composed" {
			t.Errorf("child saw %s=%q, want %q", marker, got, "composed")
		}

		result = runner.Run(t.Context(), runner.Spec{
			Argv: helperCommand(t, "env", unwant),
			Env:  composed,
		})
		if result.Err != nil {
			t.Fatalf("Err = %v, want nil", result.Err)
		}
		if got := string(result.Output); got != "" {
			t.Errorf("child saw %s=%q, want it unset: the parent's environment leaked through", unwant, got)
		}
	})
}

// TestConcurrentRunsAreIndependent is the property the worker pool will be
// built on. Sixteen runs at once, each with its own exit status, its own
// output, and its own deadline, and none of them may observe another's.
func TestConcurrentRunsAreIndependent(t *testing.T) {
	t.Parallel()

	const runs = 16
	type outcome struct {
		exitCode int
		output   string
		timedOut bool
	}
	results := make([]outcome, runs)

	var wg sync.WaitGroup
	for i := range runs {
		// The argv is built on the test's own goroutine: helperCommand may
		// call t.Fatalf, which only the test goroutine may do.
		spec := runner.Spec{Env: helperEnviron()}
		if i%4 == 3 {
			// Every fourth run is a timeout, so the kill path runs
			// concurrently with the ordinary one.
			spec.Argv = helperCommand(t, "sleep", "60000")
			spec.Timeout = 250 * time.Millisecond
		} else {
			spec.Argv = helperCommand(t, "emit", fmt.Sprintf("run-%02d", i), "")
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			result := runner.Run(t.Context(), spec)
			if result.Err != nil {
				t.Errorf("run %d: Err = %v, want nil", i, result.Err)
				return
			}
			results[i] = outcome{
				exitCode: result.ExitCode,
				output:   string(result.Output),
				timedOut: result.TimedOut,
			}
		}()
	}
	wg.Wait()

	for i, got := range results {
		if i%4 == 3 {
			if !got.timedOut || got.exitCode != runner.ExitCodeUnavailable {
				t.Errorf("run %d: %+v, want a timed-out result", i, got)
			}
			continue
		}
		want := fmt.Sprintf("run-%02d", i)
		if got.timedOut || got.exitCode != 0 || got.output != want {
			t.Errorf("run %d: %+v, want exit 0 and output %q", i, got, want)
		}
	}
}

// TestSupervisionPathIsTheOneDocumented proves the platform mechanism was
// actually used, which no behavioural assertion can: a Windows build that
// silently fell back to killing a single process would still pass every test
// above on a machine where the grandchild happens to die with its parent.
//
// It deliberately does not call t.Parallel. The counters are process-global,
// and Go runs the sequential part of every test before resuming the parallel
// ones, so a sequential test is the only place a delta of exactly one can be
// asserted.
func TestSupervisionPathIsTheOneDocumented(t *testing.T) {
	wantKind := "process-group"
	if runtime.GOOS == "windows" {
		wantKind = "job-object"
	}
	if runner.SupervisorKind != wantKind {
		t.Fatalf("SupervisorKind = %q, want %q on %s", runner.SupervisorKind, wantKind, runtime.GOOS)
	}

	adoptedBefore, terminatedBefore := runner.AdoptedCount(), runner.TerminatedCount()

	result := runner.Run(t.Context(), runner.Spec{
		Argv:    helperCommand(t, "sleep", "60000"),
		Env:     helperEnviron(),
		Timeout: 250 * time.Millisecond,
	})
	if !result.TimedOut {
		t.Fatalf("TimedOut = false, want true; output: %s", result.Output)
	}

	if got := runner.AdoptedCount() - adoptedBefore; got != 1 {
		t.Errorf("adopted %d process trees, want 1: the %s path was not taken", got, wantKind)
	}
	if got := runner.TerminatedCount() - terminatedBefore; got != 1 {
		t.Errorf("terminated %d process trees, want 1: the %s path was not taken", got, wantKind)
	}
	// A run that finishes on its own must be adopted and must not be killed,
	// so the counters cannot be passing above by counting everything.
	adoptedBefore, terminatedBefore = runner.AdoptedCount(), runner.TerminatedCount()
	if result := runner.Run(t.Context(), runner.Spec{
		Argv: helperCommand(t, "exit", "0"),
		Env:  helperEnviron(),
	}); result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if got := runner.AdoptedCount() - adoptedBefore; got != 1 {
		t.Errorf("adopted %d process trees, want 1", got)
	}
	if got := runner.TerminatedCount() - terminatedBefore; got != 0 {
		t.Errorf("terminated %d process trees, want 0: a child that exited on its own was killed", got)
	}
}

// TestChildDoesNotRunBeforeItIsAdopted is the deterministic half of the
// process-tree guarantee, and the only test that can see the moment the tree
// tests can only infer.
//
// The escape it guards against is a grandchild created before the supervisor
// owns the child. Watching for a leaked sentinel catches that only when the
// scheduling happens to go the wrong way, which is why the bug it was written
// for showed up under load and nowhere else. This asserts the mechanism
// instead: half a second after the child was started, and before it has been
// adopted, it must not have produced a single byte — because it has not been
// allowed to execute at all.
func TestChildDoesNotRunBeforeItIsAdopted(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "windows" {
		t.Skip("POSIX puts the child in its process group during fork, so there is no window and nothing to suspend")
	}

	argv := helperCommand(t, "emit", "STARTED", "")
	var captured lockedBuffer
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = helperEnviron()
	cmd.Stdout = &captured
	cmd.Stderr = &captured

	err := runner.StartSuspendedForTest(cmd, func() {
		time.Sleep(500 * time.Millisecond)
		if got := captured.String(); got != "" {
			t.Errorf("the child printed %q before it was adopted; it was not created suspended, "+
				"so a grandchild it spawned would land outside the supervisor", got)
		}
	})
	if err != nil {
		t.Fatalf("StartSuspendedForTest = %v, want nil", err)
	}
	if got := captured.String(); got != "STARTED" {
		t.Errorf("captured %q after adoption, want %q: the child was never resumed", got, "STARTED")
	}
}

// lockedBuffer is a writer that can be read while os/exec's copier is still
// writing into it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestErrorRendering pins the shape of the user-facing error: the code first,
// so that it is greppable, and the cause reachable through errors.Is.
func TestErrorRendering(t *testing.T) {
	t.Parallel()

	cause := errors.New("underlying failure")
	err := &runner.Error{Code: runner.CodeSupervisionUnavailable, Message: "could not supervise", Err: cause}

	if got, want := err.Error(), "GOM7201: could not supervise: underlying failure"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is does not reach the cause")
	}
	if got := runner.CodeOf(err); got != runner.CodeSupervisionUnavailable {
		t.Errorf("CodeOf = %q, want %q", got, runner.CodeSupervisionUnavailable)
	}

	bare := &runner.Error{Code: runner.CodeSpecInvalid, Message: "no argument vector"}
	if got, want := bare.Error(), "GOM7203: no argument vector"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got := runner.CodeOf(errors.New("not ours")); got != "" {
		t.Errorf("CodeOf(foreign error) = %q, want an empty string", got)
	}
}

// TestErrorCodesAreDistinct guards against the copy-paste that would make two
// different failures report the same GOM code.
func TestErrorCodesAreDistinct(t *testing.T) {
	t.Parallel()

	seen := map[string]string{}
	for name, code := range map[string]string{
		"CodeSupervisionUnavailable": runner.CodeSupervisionUnavailable,
		"CodeProcessStartFailed":     runner.CodeProcessStartFailed,
		"CodeSpecInvalid":            runner.CodeSpecInvalid,
		"CodeProcessWaitFailed":      runner.CodeProcessWaitFailed,
	} {
		if !strings.HasPrefix(code, "GOM72") {
			t.Errorf("%s = %q, want a code in this package's GOM72xx range", name, code)
		}
		if other, ok := seen[code]; ok {
			t.Errorf("%s and %s share the code %q", name, other, code)
		}
		seen[code] = name
	}
}

// The tests below are about the one promise this package makes on behalf of
// the whole tool: every subprocess go-mutants starts is recorded here, at the
// choke point, so no call site can forget to record one. A call site can only
// forget to *label* one, and the schema's `kind` enum is what catches that.

// newRecording opens a recorder over an unbounded ring and returns both, so a
// test can assert on exactly what the runner handed the recorder.
func newRecording(t *testing.T) (*trace.Recorder, *trace.MemorySink) {
	t.Helper()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, time.Now, trace.StartRecord{
		Kind:        trace.StartKindRun,
		ToolVersion: "test",
		PID:         os.Getpid(),
		Root:        t.TempDir(),
	})
	if recorder == nil {
		t.Fatal("trace.New returned no recorder for a real sink")
	}
	return recorder, sink
}

// onlyExec returns the one exec event of a recording, and fails when there is
// any other number of them: "exactly one per call" is the claim being made.
func onlyExec(t *testing.T, sink *trace.MemorySink) trace.Event {
	t.Helper()
	found := execEvents(sink)
	if len(found) != 1 {
		t.Fatalf("the recording holds %d exec events, want exactly one per Run", len(found))
	}
	if found[0].Exec == nil {
		t.Fatalf("the exec event carries no exec payload: %+v", found[0])
	}
	return found[0]
}

// execEvents is every exec event the sink kept, in order.
func execEvents(sink *trace.MemorySink) []trace.Event {
	var found []trace.Event
	for _, event := range sink.Events() {
		if event.Type == trace.TypeExec {
			found = append(found, event)
		}
	}
	return found
}

// TestRunRecordsOneExecEventPerProcessWithItsKindSubjectAndDigest is the happy
// path of the whole recording contract: what ran, where, under which label,
// what it produced, and the sequence the caller can point at it by.
func TestRunRecordsOneExecEventPerProcessWithItsKindSubjectAndDigest(t *testing.T) {
	t.Parallel()

	recorder, sink := newRecording(t)
	dir := t.TempDir()
	const (
		marker  = "GO_MUTANTS_RUNNER_TRACE_MARKER"
		value   = "a-value-no-event-may-carry"
		subject = "8f14e45fceea167a"
		stdout  = "recorded stdout\n"
		stderr  = "recorded stderr\n"
	)

	spec := runner.Spec{
		Argv:    helperCommand(t, "emit", stdout, stderr),
		Dir:     dir,
		Env:     helperEnviron(marker + "=" + value),
		Trace:   recorder,
		Kind:    trace.ExecKindMutantRun,
		Subject: subject,
	}
	result := runner.Run(t.Context(), spec)
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}

	event := onlyExec(t, sink)
	rec := event.Exec
	if rec.Kind != trace.ExecKindMutantRun {
		t.Errorf("kind = %q, want %q", rec.Kind, trace.ExecKindMutantRun)
	}
	if rec.Subject != subject {
		t.Errorf("subject = %q, want %q", rec.Subject, subject)
	}
	if !slices.Equal(rec.Argv, spec.Argv) {
		t.Errorf("argv = %q, want %q", rec.Argv, spec.Argv)
	}
	if rec.Dir != dir {
		t.Errorf("dir = %q, want %q", rec.Dir, dir)
	}
	if rec.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", rec.ExitCode)
	}
	if rec.TimedOut {
		t.Error("timed_out is set for a child that ran to completion")
	}

	// The digest is over the bytes the runner captured, which is the join key a
	// reader uses to pair an event with the output preserved beside the stream.
	if !strings.Contains(string(result.Output), stdout) {
		t.Fatalf("Output = %q, want it to contain the child's stdout %q", result.Output, stdout)
	}
	digest := sha256.Sum256(result.Output)
	if want := hex.EncodeToString(digest[:]); rec.OutputSHA256 != want {
		t.Errorf("output_sha256 = %q, want %q, the digest of the captured bytes", rec.OutputSHA256, want)
	}
	if rec.OutputBytes != len(result.Output) {
		t.Errorf("output_bytes = %d, want %d", rec.OutputBytes, len(result.Output))
	}

	if !slices.Contains(rec.EnvNames, marker) {
		t.Errorf("env_names = %q, want it to name %q, which the child could see", rec.EnvNames, marker)
	}
	if slices.Contains(rec.EnvNames, marker+"="+value) {
		t.Errorf("env_names = %q, want names alone and never an entry", rec.EnvNames)
	}

	if result.TraceSeq != event.Seq {
		t.Errorf("TraceSeq = %d, want the recorded sequence %d", result.TraceSeq, event.Seq)
	}
}

// TestRunRecordsATimeoutAsTimedOutWithTheKilledExitCode pins the one outcome a
// reader cannot infer from an exit status: a killed tree leaves a status that
// says nothing, so the recording has to say it instead.
func TestRunRecordsATimeoutAsTimedOutWithTheKilledExitCode(t *testing.T) {
	t.Parallel()

	recorder, sink := newRecording(t)
	const timeout = 250 * time.Millisecond
	result := runner.Run(t.Context(), runner.Spec{
		Argv:    helperCommand(t, "sleep", "60000"),
		Env:     helperEnviron(),
		Timeout: timeout,
		Trace:   recorder,
		Kind:    trace.ExecKindBaselineTest,
	})
	if !result.TimedOut {
		t.Fatalf("TimedOut = false (exit %d, err %v), want the child to have run out of time",
			result.ExitCode, result.Err)
	}

	rec := onlyExec(t, sink).Exec
	if !rec.TimedOut {
		t.Error("timed_out = false, want true")
	}
	if rec.ExitCode != runner.ExitCodeUnavailable {
		t.Errorf("exit_code = %d, want ExitCodeUnavailable (%d)", rec.ExitCode, runner.ExitCodeUnavailable)
	}
	if rec.TimeoutMS != timeout.Milliseconds() {
		t.Errorf("timeout_ms = %d, want %d", rec.TimeoutMS, timeout.Milliseconds())
	}
	if rec.Kind != trace.ExecKindBaselineTest {
		t.Errorf("kind = %q, want %q", rec.Kind, trace.ExecKindBaselineTest)
	}
}

// TestRunRecordsAStartFailureWithTheErrorAndNoOutput covers the command that
// never became a process. It is recorded because "it was never started" is one
// of the things a reader of a recording most needs to be told, and it is the
// one case where a missing event would look exactly like a missing command.
func TestRunRecordsAStartFailureWithTheErrorAndNoOutput(t *testing.T) {
	t.Parallel()

	recorder, sink := newRecording(t)
	missing := filepath.Join(t.TempDir(), "no-such-executable")
	result := runner.Run(t.Context(), runner.Spec{
		Argv:  []string{missing},
		Trace: recorder,
		Kind:  trace.ExecKindGoTestC,
	})
	if got := runner.CodeOf(result.Err); got != runner.CodeProcessStartFailed {
		t.Fatalf("CodeOf(Err) = %q (err %v), want %q", got, result.Err, runner.CodeProcessStartFailed)
	}

	rec := onlyExec(t, sink).Exec
	if rec.ExitCode != runner.ExitCodeUnavailable {
		t.Errorf("exit_code = %d, want ExitCodeUnavailable (%d)", rec.ExitCode, runner.ExitCodeUnavailable)
	}
	if rec.Error != result.Err.Error() {
		t.Errorf("error = %q, want the failure the caller was given, %q", rec.Error, result.Err)
	}
	if rec.OutputBytes != 0 || rec.OutputSHA256 != "" {
		t.Errorf("output_bytes = %d and output_sha256 = %q, want nothing: the process never wrote",
			rec.OutputBytes, rec.OutputSHA256)
	}
}

// TestRunRecordsAnInvalidSpecBeforeRefusingIt is the refusal path. A spec the
// runner will not run is still a command somebody meant to issue, and a
// recording that dropped it would leave the caller's own event pointing at
// nothing.
func TestRunRecordsAnInvalidSpecBeforeRefusingIt(t *testing.T) {
	t.Parallel()

	recorder, sink := newRecording(t)
	result := runner.Run(t.Context(), runner.Spec{
		Argv:  nil,
		Trace: recorder,
		Kind:  trace.ExecKindGoList,
	})
	if got := runner.CodeOf(result.Err); got != runner.CodeSpecInvalid {
		t.Fatalf("CodeOf(Err) = %q (err %v), want %q", got, result.Err, runner.CodeSpecInvalid)
	}

	event := onlyExec(t, sink)
	rec := event.Exec
	if rec.Argv == nil || len(rec.Argv) != 0 {
		t.Errorf("argv = %#v, want the vector as it was given, which is the empty one", rec.Argv)
	}
	if rec.ExitCode != runner.ExitCodeUnavailable {
		t.Errorf("exit_code = %d, want ExitCodeUnavailable (%d)", rec.ExitCode, runner.ExitCodeUnavailable)
	}
	if !strings.Contains(rec.Error, runner.CodeSpecInvalid) {
		t.Errorf("error = %q, want it to name the refusal %q", rec.Error, runner.CodeSpecInvalid)
	}
	if result.TraceSeq != event.Seq {
		t.Errorf("TraceSeq = %d, want the recorded sequence %d", result.TraceSeq, event.Seq)
	}

	var failure *runner.Error
	if !errors.As(result.Err, &failure) {
		t.Fatalf("Err = %v, want a *runner.Error", result.Err)
	}
	if failure.Command() == nil {
		t.Fatal("Command() = nil, want the refused invocation")
	}
	if failure.Command().TraceSeq != event.Seq {
		t.Errorf("Command().TraceSeq = %d, want the recorded sequence %d", failure.Command().TraceSeq, event.Seq)
	}
}

// TestRunWithoutARecorderRecordsNothingAndReturnsSeqZero pins the disabled
// trace: a spec that names no recorder records nothing anywhere, and says so
// with a zero sequence rather than with a number pointing at another run's
// event.
func TestRunWithoutARecorderRecordsNothingAndReturnsSeqZero(t *testing.T) {
	t.Parallel()

	// A recording exists; this spec simply does not name it.
	_, sink := newRecording(t)
	result := runner.Run(t.Context(), runner.Spec{
		Argv: helperCommand(t, "exit", "0"),
		Env:  helperEnviron(),
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if result.TraceSeq != 0 {
		t.Errorf("TraceSeq = %d, want 0 for an untraced run", result.TraceSeq)
	}
	if got := execEvents(sink); len(got) != 0 {
		t.Errorf("the recording holds %d exec events, want none: the runner records only into Spec.Trace", len(got))
	}
}

// TestRunNeverHandsTheRecorderAnEnvironmentValue is the leak test.
//
// An environment carries credentials, tokens and paths a developer would not
// attach to a bug report, and a trace is meant to be attached to bug reports.
// The reduction to names happens inside the recorder, so this test watches what
// the runner hands it *and* what the sink is asked to serialise: a name with an
// `=` in it, or the value appearing anywhere in the marshalled event, is a leak
// however it got there.
func TestRunNeverHandsTheRecorderAnEnvironmentValue(t *testing.T) {
	t.Parallel()

	const (
		marker = "GO_MUTANTS_RUNNER_TRACE_SECRET"
		value  = "s3cr3t-value-that-must-not-be-recorded"
	)
	spy := &spySink{}
	recorder := trace.New(spy, time.Now, trace.StartRecord{Kind: trace.StartKindRun, ToolVersion: "test"})

	result := runner.Run(t.Context(), runner.Spec{
		Argv:  helperCommand(t, "exit", "0"),
		Env:   helperEnviron(marker + "=" + value),
		Trace: recorder,
		Kind:  trace.ExecKindMutantRun,
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}

	events, lines, err := spy.recorded()
	if err != nil {
		t.Fatalf("marshalling a recorded event: %v", err)
	}
	var named bool
	for _, event := range events {
		if event.Exec == nil {
			continue
		}
		for _, name := range event.Exec.EnvNames {
			if strings.Contains(name, "=") {
				t.Errorf("env_names holds %q, which is an entry rather than a name", name)
			}
			if name == marker {
				named = true
			}
		}
	}
	if !named {
		t.Errorf("no exec event named %q, so this test would pass on a recorder that recorded no environment at all", marker)
	}
	for _, line := range lines {
		if strings.Contains(line, value) {
			t.Errorf("a recorded event carries the environment value %q:\n%s", value, line)
		}
	}
}

// TestRunWithAFailingSinkStillReturnsTheChildResult is the invariant that makes
// tracing safe to leave on: a diagnostic that could change a verdict would
// invert the point of having one.
func TestRunWithAFailingSinkStillReturnsTheChildResult(t *testing.T) {
	t.Parallel()

	spec := runner.Spec{
		Argv: helperCommand(t, "emit", "out\n", "err\n"),
		Dir:  t.TempDir(),
		Env:  helperEnviron(),
	}
	untraced := runner.Run(t.Context(), spec)

	traced := spec
	traced.Kind = trace.ExecKindMutantRun
	traced.Trace = trace.New(refusingSink{}, time.Now, trace.StartRecord{Kind: trace.StartKindRun})
	got := runner.Run(t.Context(), traced)

	if got.TraceSeq == 0 {
		t.Error("TraceSeq = 0, want the sequence the recorder assigned: the sink refused the event, not the recording")
	}
	// The whole Result is compared, rather than the fields somebody thought to
	// list, so that a field added to Result later cannot quietly fall outside
	// the invariant. Exactly two are exempt and both by construction: Duration
	// is a measurement two runs of the same command are never expected to agree
	// on, and TraceSeq is the one field recording is meant to change.
	if !reflect.DeepEqual(comparableResult(got), comparableResult(untraced)) {
		t.Errorf("a traced run produced %+v with output %q, want the untraced %+v with output %q: "+
			"recording changed the result",
			comparableResult(got), got.Output, comparableResult(untraced), untraced.Output)
	}
}

// comparableResult clears the two fields a traced and an untraced run of the
// same command are allowed to disagree on. Every other field, including any
// added later, is compared as it stands.
func comparableResult(result runner.Result) runner.Result {
	result.Duration = 0
	result.TraceSeq = 0
	return result
}

// spySink keeps every event and the bytes it would have written, so a test can
// assert about what a sink is asked to serialise rather than only about what
// the recorder was handed.
type spySink struct {
	mu     sync.Mutex
	events []trace.Event
	lines  []string
	err    error
}

func (s *spySink) Emit(event trace.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	line, err := json.Marshal(event)
	if err != nil && s.err == nil {
		s.err = err
	}
	s.events = append(s.events, event.Clone())
	s.lines = append(s.lines, string(line))
	return nil
}

func (s *spySink) Close() error { return nil }

func (s *spySink) recorded() ([]trace.Event, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events), slices.Clone(s.lines), s.err
}

// refusingSink is the full disk: every event costs nothing but itself.
type refusingSink struct{}

func (refusingSink) Emit(trace.Event) error { return errors.New("this sink keeps nothing") }
func (refusingSink) Close() error           { return nil }

// TestRunRecordsACommandItWasTooLateToStart is the fourth way out of Run, and
// the one whose event carries no failure: a run that was cancelled before the
// child started reports no error, because a drained Ctrl-C is not an
// infrastructure fault. It is still recorded, so a reader of an interrupted
// run's recording can see which commands were abandoned rather than infer their
// absence.
func TestRunRecordsACommandItWasTooLateToStart(t *testing.T) {
	t.Parallel()

	recorder, sink := newRecording(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result := runner.Run(ctx, runner.Spec{
		Argv:  helperCommand(t, "exit", "0"),
		Env:   helperEnviron(),
		Trace: recorder,
		Kind:  trace.ExecKindMutantRun,
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil: a cancellation is not this package's failure", result.Err)
	}

	event := onlyExec(t, sink)
	if event.Exec.ExitCode != runner.ExitCodeUnavailable {
		t.Errorf("exit_code = %d, want ExitCodeUnavailable (%d)", event.Exec.ExitCode, runner.ExitCodeUnavailable)
	}
	if event.Exec.Error != "" {
		t.Errorf("error = %q, want none", event.Exec.Error)
	}
	if result.TraceSeq != event.Seq {
		t.Errorf("TraceSeq = %d, want the recorded sequence %d", result.TraceSeq, event.Seq)
	}
}

// TestSeparateStdoutSplitsTheStreamsWithoutLosingTheCombinedOne is the whole of
// [runner.Spec.SeparateStdout].
//
// It exists because one caller needs a stream a parser can read. `go list
// -json` writes a document to stdout and writes warnings, module downloads and
// toolchain switches to stderr, and a decoder handed the combined capture fails
// on the first byte of `go: warning: …` — for a command that exited zero and
// did exactly what it was asked. The combined stream is still what a *person*
// needs, so both are kept: `Output` is unchanged and is what the recording
// digests, and `Stdout` is the half a machine reads.
//
// The interleaving is the price and is asserted as such. Two writers mean two
// pipes, so `Output` holds both halves in an order the operating system chose;
// the default path keeps one pipe and TestCombinedOutputKeepsWriteOrder still
// pins that exact concatenation.
func TestSeparateStdoutSplitsTheStreamsWithoutLosingTheCombinedOne(t *testing.T) {
	t.Parallel()

	const (
		out = "{\"ImportPath\":\"example.com/x\"}\n"
		err = "go: warning: \"./docs/...\" matched no packages\n"
	)
	result := runner.Run(t.Context(), runner.Spec{
		Argv:           helperCommand(t, "emit", out, err),
		Env:            helperEnviron(),
		SeparateStdout: true,
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if got := string(result.Stdout); got != out {
		t.Errorf("Stdout = %q, want the child's stdout alone %q", got, out)
	}
	combined := string(result.Output)
	if !strings.Contains(combined, out) || !strings.Contains(combined, err) {
		t.Errorf("Output = %q, want both halves of what the child wrote", combined)
	}
	if want := int64(len(out) + len(err)); result.OutputBytes != want {
		t.Errorf("OutputBytes = %d, want %d: both streams count", result.OutputBytes, want)
	}
}

// TestStdoutIsNilWithoutSeparateStdout keeps the field opt-in.
//
// Every other caller in this repository wants the combined stream and nothing
// else, and a second capture allocated for all of them would be a second copy
// of every mutant's output for the one call site that reads a document.
func TestStdoutIsNilWithoutSeparateStdout(t *testing.T) {
	t.Parallel()

	result := runner.Run(t.Context(), runner.Spec{
		Argv: helperCommand(t, "emit", "OUT", "ERR"),
		Env:  helperEnviron(),
	})
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if result.Stdout != nil {
		t.Errorf("Stdout = %q, want nil for a spec that did not ask for the split", result.Stdout)
	}
}
