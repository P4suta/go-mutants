// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner_test

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
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
	helperMisuse = 97
	// helperCoverRootEnv names the directory each helper process carves its own
	// coverage output directory out of. See isolateCoverageOutput.
	helperCoverRootEnv = "GO_MUTANTS_RUNNER_TEST_COVERDIR_ROOT"
	// coverDirEnv is the variable the Go coverage runtime reads at exit.
	coverDirEnv = "GOCOVERDIR"
	// helperDeafMarker is what the "deaf" verb prints once it is ignoring
	// SIGTERM, so a test can tell that outcome apart from the signal having
	// arrived before the disposition was installed.
	helperDeafMarker = "deaf\n"
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "" {
		os.Exit(runTests(m))
	}
	if err := isolateCoverageOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "helper: %v\n", err)
		os.Exit(helperMisuse)
	}
	os.Exit(runHelper(os.Args[1:]))
}

// runTests runs the suite proper.
//
// It is a function rather than the body of [TestMain] because the coverage root
// has to be removed on the way out and TestMain ends in os.Exit, which runs no
// deferred function.
func runTests(m *testing.M) int {
	root, err := os.MkdirTemp("", "go-mutants-runner-cover-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating the helper coverage root: %v\n", err)
		return helperMisuse
	}
	defer func() { _ = os.RemoveAll(root) }()

	// Published into this process's own environment rather than only into the
	// one helperEnviron composes, so that a child which deliberately inherits
	// — TestEnvIsTheWholeEnvironment runs one with a nil Spec.Env — finds it
	// too.
	if err := os.Setenv(helperCoverRootEnv, root); err != nil {
		fmt.Fprintf(os.Stderr, "publishing the helper coverage root: %v\n", err)
		return helperMisuse
	}
	return m.Run()
}

// isolateCoverageOutput points this helper's coverage output at a directory
// nothing else writes to.
//
// A helper is this very test binary re-executed, so under `go test -cover` it
// is coverage-instrumented — and because it exits from [TestMain] without ever
// running the testing package's "the profile is already written" call, the
// coverage runtime's exit hook fires. Left alone that hook writes
// covmeta.<hash> into the single GOCOVERDIR that `go test` exports, under a
// name derived from the binary and therefore identical for every helper. The
// concurrent atomic renames then collide: on Windows the loser prints
// "error: coverage meta-data emit failed: ... Access is denied" on stderr, the
// runner faithfully captures it, and every assertion about exact captured bytes
// fails. Unsetting the variable is not the fix — measured, the hook then prints
// "warning: GOCOVERDIR not set, no coverage data emitted" to the same stderr.
// A private directory is the only quiet answer, and it has the second virtue of
// keeping helper counters out of the parent's own coverage profile.
//
// This runs in the helper rather than in helperEnviron so that it covers every
// helper process there is: the ones handed a composed environment, the one that
// inherits, and the grandchild the "tree" verb spawns.
func isolateCoverageOutput() error {
	root := os.Getenv(helperCoverRootEnv)
	if root == "" {
		return fmt.Errorf("%s is unset, so this helper has nowhere private to write coverage output", helperCoverRootEnv)
	}
	// The pid is unique among the processes that are alive at the same time,
	// which is exactly the set that could collide.
	dir := filepath.Join(root, strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Setenv(coverDirEnv, dir)
}

// helperCommand builds the argv that re-executes this binary as a helper.
func helperCommand(t *testing.T, args ...string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	return append([]string{exe, helperFlag}, args...)
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

	default:
		fmt.Fprintf(os.Stderr, "helper: unknown verb %q\n", verb)
		return helperMisuse
	}
}

// runTreeHelper spawns the grandchild and then sleeps.
func runTreeHelper(sentinelPath, sentinelDelay, ownSleep string) int {
	exe, err := os.Executable()
	if err != nil {
		return helperMisuse
	}
	grandchild := exec.Command(exe, helperFlag, "sentinel", sentinelPath, sentinelDelay)
	grandchild.Env = os.Environ()
	// The grandchild inherits our streams on purpose: sharing the capture pipe
	// is part of what makes an unkilled descendant hold the run open.
	grandchild.Stdout = os.Stdout
	grandchild.Stderr = os.Stderr
	if err := grandchild.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "helper: spawning grandchild: %v\n", err)
		return helperMisuse
	}
	_, _ = fmt.Fprintf(os.Stdout, "grandchild %d\n", grandchild.Process.Pid)

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
