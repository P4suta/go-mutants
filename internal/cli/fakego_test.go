// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

const hangCutoff = 200 * time.Millisecond

func TestDoctorReportsAToolchainThatDoesNotAnswer(t *testing.T) {
	t.Run("the probe answers with something else", func(t *testing.T) {
		const complaint = "dyld: Library not loaded: libgo.so.1"
		f := mutantkit.FakeGo(t)
		f.On("version").Stderr(complaint + "\n").Exit(2)
		f.Export()

		code, stdout, stderr := execute(t, "doctor")
		if code != 2 {
			t.Errorf("exit code = %d, want 2 so a CI job can stop here\n%s\n%s", code, stdout, stderr)
		}

		row := checkRow(t, stdout, checkToolchain)
		if !strings.HasPrefix(row, "FAIL") {
			t.Errorf("the toolchain row is %q, want a FAIL", row)
		}
		if !strings.Contains(row, "exited with status 2") {
			t.Errorf("the toolchain row is %q, want it to say what the probe did", row)
		}
		if strings.Contains(row, "GOM72") {
			t.Errorf("the toolchain row is %q, want the diagnostic code left off the table", row)
		}

		calls := f.Calls()
		if len(calls) != 1 {
			t.Fatalf("`doctor` probed the toolchain %d times, want once shared with the platform "+
				"row: %v", len(calls), calls)
		}
		if want := []string{"version"}; !slices.Equal(calls[0].Argv, want) {
			t.Errorf("the probe was %q, want %q: nothing is measured and nothing is executed beyond "+
				"the two version probes", calls[0].Argv, want)
		}
	})

	t.Run("the probe never answers", func(t *testing.T) {
		f := mutantkit.FakeGo(t)
		f.On("version").Sleep(2 * time.Minute)
		f.Export()

		ctx, cancel := context.WithTimeout(t.Context(), hangCutoff)
		defer cancel()
		started := time.Now()
		checks := diagnose(ctx, t.TempDir())
		if elapsed, bound := time.Since(started), 5*hangCutoff; elapsed > bound {
			t.Fatalf("diagnose waited %s, want the caller's %s deadline to have ended it (allowing "+
				"%s for a loaded machine) rather than the probe's own budget", elapsed, hangCutoff, bound)
		}

		index := slices.IndexFunc(checks, func(c check) bool { return c.Name == checkToolchain })
		if index < 0 {
			t.Fatalf("no %q row in %+v", checkToolchain, checks)
		}
		toolchain := checks[index]
		if toolchain.Status != statusFail {
			t.Errorf("the toolchain row is %+v, want a %s: a `go` that never answers is not a "+
				"toolchain this machine can run with", toolchain, statusFail)
		}
		if toolchain.Detail == "" {
			t.Error("the toolchain row carries no detail, so the reader is told only that it failed")
		}
		if strings.Contains(toolchain.Detail, "GOM72") {
			t.Errorf("the toolchain row is %q, want the diagnostic code left off the table",
				toolchain.Detail)
		}
	})
}

func checkRow(t *testing.T, table, name string) string {
	t.Helper()
	for line := range strings.Lines(table) {
		if strings.Contains(line, name) {
			return strings.TrimRight(line, "\r\n")
		}
	}
	t.Fatalf("no %q row in the table:\n%s", name, table)
	return ""
}
