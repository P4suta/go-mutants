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

const (
	firstKillDeadline = 1500 * time.Millisecond

	spawnBudget = 15 * time.Second

	killSettle = 1500 * time.Millisecond

	sentinelWriteMargin = 3 * time.Second
)

func sentinelDelayFor(deadline time.Duration) time.Duration {
	return deadline + runner.TerminationGrace + killSettle
}

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

func milliseconds(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10)
}

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

func noSentinelSurvived(t *testing.T, sentinels []string, outlived string) {
	t.Helper()

	for i, sentinel := range sentinels {
		if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("attempt %d: os.Stat(sentinel) = %v, want os.ErrNotExist: %s", i, err, outlived)
		}
	}
}

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

func TestTimeoutKillsTheProcessTree(t *testing.T) {
	t.Parallel()

	t.Run("positive control: the sentinel appears when nothing kills it", func(t *testing.T) {
		t.Parallel()

		sentinel := filepath.Join(t.TempDir(), "sentinel")
		result := runner.Run(t.Context(), runner.Spec{
			Argv: helperCommand(t, "tree", sentinel, "200", "600"),
			Env:  helperEnviron(),
		})
		if result.Err != nil {
			t.Fatalf("Err = %v, want nil", result.Err)
		}
		if result.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0; output: %s", result.ExitCode, result.Output)
		}
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
			conclusive = start.Add(sentinelDelay + sentinelWriteMargin)

			if result.Err != nil {
				t.Fatalf("Err = %v, want nil", result.Err)
			}
			if !result.TimedOut {
				t.Fatalf("TimedOut = false, want true; output: %s", result.Output)
			}
			return strings.Contains(string(result.Output), "grandchild ")
		})

		time.Sleep(time.Until(conclusive))
		noSentinelSurvived(t, sentinels, "the grandchild outlived the timeout")
	})

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

			return spawned.Load() == concurrency
		})

		time.Sleep(time.Until(conclusive))
		noSentinelSurvived(t, sentinels,
			"a grandchild escaped the supervisor and outlived the kill")
	})
}

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

	time.Sleep(time.Until(conclusive))
	noSentinelSurvived(t, sentinels, "the SIGTERM-deaf child outlived the escalation")
}

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
	if !errors.Is(result.Err, os.ErrNotExist) && !errors.Is(result.Err, exec.ErrNotFound) {
		t.Errorf("Err = %v, want the operating system cause to survive unwrapping", result.Err)
	}
	if result.ExitCode != runner.ExitCodeUnavailable {
		t.Errorf("ExitCode = %d, want ExitCodeUnavailable (%d)", result.ExitCode, runner.ExitCodeUnavailable)
	}
}

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

func TestEnvIsTheWholeEnvironment(t *testing.T) {
	const (
		marker  = "GO_MUTANTS_RUNNER_TEST_MARKER"
		unwant  = "GO_MUTANTS_RUNNER_TEST_LEAK"
		leaking = "this must not reach the child"
	)
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
		spec := runner.Spec{Env: helperEnviron()}
		if i%4 == 3 {
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

func execEvents(sink *trace.MemorySink) []trace.Event {
	var found []trace.Event
	for _, event := range sink.Events() {
		if event.Type == trace.TypeExec {
			found = append(found, event)
		}
	}
	return found
}

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

func TestRunWithoutARecorderRecordsNothingAndReturnsSeqZero(t *testing.T) {
	t.Parallel()

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
	if !reflect.DeepEqual(comparableResult(got), comparableResult(untraced)) {
		t.Errorf("a traced run produced %+v with output %q, want the untraced %+v with output %q: "+
			"recording changed the result",
			comparableResult(got), got.Output, comparableResult(untraced), untraced.Output)
	}
}

func comparableResult(result runner.Result) runner.Result {
	result.Duration = 0
	result.PeakMemory = 0
	result.TraceSeq = 0
	return result
}

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

type refusingSink struct{}

func (refusingSink) Emit(trace.Event) error { return errors.New("this sink keeps nothing") }
func (refusingSink) Close() error           { return nil }

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
