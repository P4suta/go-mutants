// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner_test

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/testkit"
)

// The tests in this package need real processes: a process that exits with a
// chosen status, one that prints megabytes, one that hangs, and — the case the
// whole supervisor exists for — one that spawns a grandchild which will
// misbehave later.
//
// Those programs are this test binary. Re-executing ourselves with
// helperEnv set turns the binary into the requested helper instead of a test
// run, which keeps the fixtures in the same file as the assertions that depend
// on them, needs no `go build` at test time, and leaves nothing behind in the
// repository or in TMP that t.TempDir does not clean up.

const (
	// helperEnv switches the binary into helper mode. Its presence, not its
	// value, is what matters.
	helperEnv = "GO_MUTANTS_RUNNER_TEST_HELPER"
	// helperFlag is the first argument of a helper invocation, so that a
	// stray inherited helperEnv cannot turn an ordinary `go test` run into a
	// helper by accident.
	helperFlag = "-gm-helper"
	// helperMisuse is the status a helper exits with when it does not
	// understand its own arguments, or cannot set itself up. It is distinct
	// from every status the tests ask for, so a misuse can never be mistaken
	// for a pass.
	helperMisuse = testkit.HelperMisuse
	// helperDeafMarker is what the "deaf" verb prints once it is ignoring
	// SIGTERM, so a test can tell that outcome apart from the signal having
	// arrived before the disposition was installed.
	helperDeafMarker = "deaf\n"
)

// TestMain turns this binary into the requested helper when helperEnv is set,
// and otherwise runs the suite.
//
// The switch, the private GOCOVERDIR every helper process needs, and the
// removal of the directory they are carved out of all live in
// [testkit.Helper]. The coverage isolation in particular is not a detail: a
// helper is this very test binary re-executed, so under `go test -cover` its
// exit hook writes covmeta.<hash> into the single GOCOVERDIR `go test` exports
// under a name derived from the binary — identical for every helper — and the
// concurrent atomic renames collide. On Windows the loser prints "coverage
// meta-data emit failed: ... Access is denied" onto the very stderr the tests
// here assert the exact bytes of.
func TestMain(m *testing.M) {
	os.Exit(testkit.Helper(m, helperEnv, runHelper))
}

// helperCommand builds the argv that re-executes this binary as a helper.
//
// It is [testkit.HelperArgv]'s sibling rather than a call to it: that one
// selects a *test function* with `-test.run`, and this helper is a program run
// from TestMain, so what follows the binary is the helper flag and a verb. Both
// resolve the binary through [testkit.TestBinary], which prefers os.Executable
// over os.Args[0] — a child started in another directory cannot resolve a
// relative argv[0] against ours.
func helperCommand(t *testing.T, args ...string) []string {
	t.Helper()
	return append([]string{testkit.TestBinary(), helperFlag}, args...)
}

// helperEnviron is the environment a helper child needs: this process's own,
// plus the switch that makes the binary act as a helper.
func helperEnviron(extra ...string) []string {
	env := append(os.Environ(), helperEnv+"=1")
	return append(env, extra...)
}

// spamPayload is the deterministic filler the "spam" verb writes. Every byte
// depends on its offset, so an assertion that the *tail* survived truncation
// is an assertion about position and not just about length.
func spamPayload(n int) []byte {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-_"
	out := make([]byte, n)
	for i := range out {
		out[i] = alphabet[i%len(alphabet)]
	}
	return out
}

// runHelper is the whole helper program.
func runHelper(args []string) int {
	if len(args) < 2 || args[0] != helperFlag {
		fmt.Fprintf(os.Stderr, "helper: bad invocation %q\n", args)
		return helperMisuse
	}
	verb, rest := args[1], args[2:]

	switch verb {
	case "exit":
		// exit CODE
		if len(rest) != 1 {
			return helperMisuse
		}
		code, err := strconv.Atoi(rest[0])
		if err != nil {
			return helperMisuse
		}
		return code

	case "emit":
		// emit STDOUT-TEXT STDERR-TEXT
		if len(rest) != 2 {
			return helperMisuse
		}
		_, _ = fmt.Fprint(os.Stdout, rest[0])
		_, _ = fmt.Fprint(os.Stderr, rest[1])
		return 0

	case "spam":
		// spam BYTES — written in chunks, so the capture is exercised across
		// many Write calls rather than one big one.
		if len(rest) != 1 {
			return helperMisuse
		}
		n, err := strconv.Atoi(rest[0])
		if err != nil {
			return helperMisuse
		}
		payload := spamPayload(n)
		const chunk = 8 << 10
		for len(payload) > 0 {
			size := min(chunk, len(payload))
			if _, err := os.Stdout.Write(payload[:size]); err != nil {
				return helperMisuse
			}
			payload = payload[size:]
		}
		return 0

	case "sleep":
		// sleep MILLISECONDS
		d, ok := helperDuration(rest)
		if !ok {
			return helperMisuse
		}
		time.Sleep(d)
		return 0

	case "cwd":
		dir, err := os.Getwd()
		if err != nil {
			return helperMisuse
		}
		_, _ = fmt.Fprint(os.Stdout, dir)
		return 0

	case "env":
		// env NAME — prints the value, empty when unset.
		if len(rest) != 1 {
			return helperMisuse
		}
		_, _ = fmt.Fprint(os.Stdout, os.Getenv(rest[0]))
		return 0

	case "sentinel":
		// sentinel PATH DELAY-MS — the misbehaving grandchild. If the
		// supervisor does its job this process never reaches the write.
		if len(rest) != 2 {
			return helperMisuse
		}
		d, ok := helperDuration(rest[1:])
		if !ok {
			return helperMisuse
		}
		time.Sleep(d)
		if err := os.WriteFile(rest[0], []byte("sentinel"), 0o600); err != nil {
			return helperMisuse
		}
		return 0

	case "deaf":
		// deaf PATH DELAY-MS — the process the POSIX escalation exists for. It
		// is "sentinel" with one difference: it makes itself deaf to SIGTERM
		// first, so nothing short of SIGKILL stops it writing. A hung test
		// binary behaves this way for less deliberate reasons.
		if len(rest) != 2 {
			return helperMisuse
		}
		d, ok := helperDuration(rest[1:])
		if !ok {
			return helperMisuse
		}
		signal.Ignore(syscall.SIGTERM)
		// Announced only once the disposition is installed, so that a test
		// reading the captured output can tell "it ignored SIGTERM" apart from
		// "SIGTERM arrived before it was ready to".
		_, _ = fmt.Fprint(os.Stdout, helperDeafMarker)
		time.Sleep(d)
		if err := os.WriteFile(rest[0], []byte("sentinel"), 0o600); err != nil {
			return helperMisuse
		}
		return 0

	case "tree":
		// tree PATH SENTINEL-DELAY-MS OWN-SLEEP-MS — spawns the grandchild,
		// announces it on stdout so a test can prove the spawn happened in
		// this very run, then outlives it.
		if len(rest) != 3 {
			return helperMisuse
		}
		return runTreeHelper(rest[0], rest[1], rest[2])

	case "hog":
		// hog TOTAL-BYTES STEP-BYTES STEP-DELAY-MS — the process a memory
		// bound is about. It grows its resident set in visible steps and then
		// exits, so a test can assert both what the peak was and that a bound
		// stopped it before it got there.
		if len(rest) != 3 {
			return helperMisuse
		}
		return runHogHelper(rest[0], rest[1], rest[2])

	case "burst":
		// burst TOTAL-BYTES — the process no sampler can catch: it grows as
		// fast as the machine allows and exits. Whatever ends it, the peak it
		// reached is what the bound has to be judged against.
		if len(rest) != 1 {
			return helperMisuse
		}
		return runHogHelper(rest[0], rest[0], "0")

	case "hogtree":
		// hogtree SENTINEL-PATH SENTINEL-DELAY-MS TOTAL-BYTES STEP-BYTES
		// STEP-DELAY-MS — "tree" and "hog" at once. It spawns the sentinel
		// grandchild, announces it, and only then grows: a bound that killed
		// the process it measured rather than the tree around it would leave
		// the grandchild alive to write.
		if len(rest) != 5 {
			return helperMisuse
		}
		if _, code := spawnGrandchild("sentinel", rest[0], rest[1]); code != 0 {
			return code
		}
		return runHogHelper(rest[2], rest[3], rest[4])

	case "deafhog":
		// deafhog TOTAL-BYTES STEP-BYTES STEP-DELAY-MS — "deaf" and "hog" at
		// once: it makes itself deaf to SIGTERM, says so, and then grows. It is
		// the process a polite kill cannot stop, which is exactly the process a
		// memory bound must not be polite to.
		if len(rest) != 3 {
			return helperMisuse
		}
		signal.Ignore(syscall.SIGTERM)
		// Announced only once the disposition is installed, so a test can tell
		// "it ignored SIGTERM" apart from "SIGTERM arrived before it was ready".
		_, _ = fmt.Fprint(os.Stdout, helperDeafMarker)
		return runHogHelper(rest[0], rest[1], rest[2])

	case "hogchild":
		// hogchild TOTAL-BYTES STEP-BYTES STEP-DELAY-MS OWN-SLEEP-MS — the
		// inverse: the *grandchild* grows and this process only sleeps. A bound
		// measured against the direct child alone never trips on it, which is
		// what makes it a test of the tree rather than of one process.
		if len(rest) != 4 {
			return helperMisuse
		}
		return runHogChildHelper(rest[0], rest[1], rest[2], rest[3])

	default:
		fmt.Fprintf(os.Stderr, "helper: unknown verb %q\n", verb)
		return helperMisuse
	}
}

// runTreeHelper spawns the grandchild and then sleeps.
func runTreeHelper(sentinelPath, sentinelDelay, ownSleep string) int {
	if _, code := spawnGrandchild("sentinel", sentinelPath, sentinelDelay); code != 0 {
		return code
	}
	d, ok := helperDuration([]string{ownSleep})
	if !ok {
		return helperMisuse
	}
	time.Sleep(d)
	return 0
}

// spawnGrandchild re-executes this binary as another helper, announces the pid
// on stdout so a test can prove the spawn happened in this very run, and leaves
// it running.
//
// It is one function rather than one per verb because every test that is about
// a *tree* depends on the same two details: the grandchild inherits our streams
// — sharing the capture pipe is part of what makes an unkilled descendant hold
// the run open — and it is announced before anything else happens, so absence of
// its effect later can be read as "it was killed" rather than "it never
// started".
func spawnGrandchild(verb string, args ...string) (*exec.Cmd, int) {
	exe, err := os.Executable()
	if err != nil {
		return nil, helperMisuse
	}
	grandchild := exec.Command(exe, append([]string{helperFlag, verb}, args...)...)
	grandchild.Env = os.Environ()
	grandchild.Stdout = os.Stdout
	grandchild.Stderr = os.Stderr
	if err := grandchild.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "helper: spawning grandchild: %v\n", err)
		return nil, helperMisuse
	}
	_, _ = fmt.Fprintf(os.Stdout, "grandchild %d\n", grandchild.Process.Pid)
	return grandchild, 0
}

// runHogHelper grows this process's resident set to total bytes in steps of
// step, pausing delay between them, and then exits.
//
// Every page of every step is written to, because an untouched allocation is
// not resident: on every platform go-mutants supports, `make([]byte, n)` is
// address space until something stores into it, and a bound measured against
// resident memory would never see it. One byte per page is enough to fault the
// whole page in and is cheap enough that the pause between steps, rather than
// the work, is what paces the growth.
//
// The pause is what makes the process observable. A sampler cannot see a peak
// that existed between two of its ticks, so a helper that allocated as fast as
// the machine allows would be testing the scheduler; one that climbs in steps
// wider than a sampling interval is a process a bound can be shown to catch.
//
// It exits rather than holding, so a run that was *not* bounded ends by itself
// and the assertion that a bound tripped fails loudly instead of hanging.
func runHogHelper(total, step, stepDelay string) int {
	wanted, err := strconv.Atoi(total)
	if err != nil || wanted <= 0 {
		return helperMisuse
	}
	chunk, err := strconv.Atoi(step)
	if err != nil || chunk <= 0 {
		return helperMisuse
	}
	pause, ok := helperDuration([]string{stepDelay})
	if !ok {
		return helperMisuse
	}

	const pageSize = 4096
	held := make([][]byte, 0, wanted/chunk+1)
	for grown := 0; grown < wanted; grown += chunk {
		block := make([]byte, min(chunk, wanted-grown))
		for i := 0; i < len(block); i += pageSize {
			block[i] = 1
		}
		held = append(held, block)
		// Announced per step, so a test reading the captured output of a
		// process a bound killed can see how far it had got.
		_, _ = fmt.Fprintf(os.Stdout, "hogged %d\n", grown+len(block))
		time.Sleep(pause)
	}
	// Nothing below reads held, and without this the whole loop is dead to the
	// compiler and the collector both.
	runtime.KeepAlive(held)
	return 0
}

// runHogChildHelper spawns a grandchild that grows, and then sleeps through it.
func runHogChildHelper(total, step, stepDelay, ownSleep string) int {
	if _, code := spawnGrandchild("hog", total, step, stepDelay); code != 0 {
		return code
	}
	d, ok := helperDuration([]string{ownSleep})
	if !ok {
		return helperMisuse
	}
	time.Sleep(d)
	return 0
}

// helperDuration parses a single millisecond argument.
func helperDuration(args []string) (time.Duration, bool) {
	if len(args) != 1 {
		return 0, false
	}
	ms, err := strconv.Atoi(args[0])
	if err != nil || ms < 0 {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}
