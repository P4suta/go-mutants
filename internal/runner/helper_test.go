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

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
)

const (
	helperEnv        = "GO_MUTANTS_RUNNER_TEST_HELPER"
	helperFlag       = "-gm-helper"
	helperMisuse     = testkit.HelperMisuse
	helperDeafMarker = "deaf\n"
)

func TestMain(m *testing.M) {
	os.Exit(testkit.Helper(m, helperEnv, runHelper))
}

func helperCommand(t *testing.T, args ...string) []string {
	t.Helper()
	return append([]string{testkit.TestBinary(), helperFlag}, args...)
}

func helperEnviron(extra ...string) []string {
	env := append(os.Environ(), helperEnv+"=1")
	return append(env, extra...)
}

func spamPayload(n int) []byte {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-_"
	out := make([]byte, n)
	for i := range out {
		out[i] = alphabet[i%len(alphabet)]
	}
	return out
}

func runHelper(args []string) int {
	if len(args) < 2 || args[0] != helperFlag {
		fmt.Fprintf(os.Stderr, "helper: bad invocation %q\n", args)
		return helperMisuse
	}
	verb, rest := args[1], args[2:]

	switch verb {
	case "exit":
		if len(rest) != 1 {
			return helperMisuse
		}
		code, err := strconv.Atoi(rest[0])
		if err != nil {
			return helperMisuse
		}
		return code

	case "emit":
		if len(rest) != 2 {
			return helperMisuse
		}
		_, _ = fmt.Fprint(os.Stdout, rest[0])
		_, _ = fmt.Fprint(os.Stderr, rest[1])
		return 0

	case "spam":
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
		if len(rest) != 1 {
			return helperMisuse
		}
		_, _ = fmt.Fprint(os.Stdout, os.Getenv(rest[0]))
		return 0

	case "sentinel":
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
		if len(rest) != 2 {
			return helperMisuse
		}
		d, ok := helperDuration(rest[1:])
		if !ok {
			return helperMisuse
		}
		signal.Ignore(syscall.SIGTERM)
		_, _ = fmt.Fprint(os.Stdout, helperDeafMarker)
		time.Sleep(d)
		if err := os.WriteFile(rest[0], []byte("sentinel"), 0o600); err != nil {
			return helperMisuse
		}
		return 0

	case "tree":
		if len(rest) != 3 {
			return helperMisuse
		}
		return runTreeHelper(rest[0], rest[1], rest[2])

	case "hog":
		if len(rest) != 3 {
			return helperMisuse
		}
		return runHogHelper(rest[0], rest[1], rest[2])

	case "burst":
		if len(rest) != 1 {
			return helperMisuse
		}
		return runHogHelper(rest[0], rest[0], "0")

	case "hogtree":
		if len(rest) != 5 {
			return helperMisuse
		}
		if _, code := spawnGrandchild("sentinel", rest[0], rest[1]); code != 0 {
			return code
		}
		return runHogHelper(rest[2], rest[3], rest[4])

	case "deafhog":
		if len(rest) != 3 {
			return helperMisuse
		}
		signal.Ignore(syscall.SIGTERM)
		_, _ = fmt.Fprint(os.Stdout, helperDeafMarker)
		return runHogHelper(rest[0], rest[1], rest[2])

	case "hogchild":
		if len(rest) != 4 {
			return helperMisuse
		}
		return runHogChildHelper(rest[0], rest[1], rest[2], rest[3])

	default:
		fmt.Fprintf(os.Stderr, "helper: unknown verb %q\n", verb)
		return helperMisuse
	}
}

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
		_, _ = fmt.Fprintf(os.Stdout, "hogged %d\n", grown+len(block))
		time.Sleep(pause)
	}
	if pause > 0 {
		time.Sleep(2 * runner.MemorySampleInterval)
	}
	runtime.KeepAlive(held)
	return 0
}

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
