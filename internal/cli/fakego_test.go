// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// `doctor` against a toolchain that is not a toolchain.
//
// Everything `doctor` says about a working machine is asserted by
// doctor_test.go over fabricated findings and by doctor_integration_test.go
// against the real `go`. Neither can say what the command does when the
// toolchain is the *problem*, which is the machine `doctor` exists for: the
// findings would have to be invented, and the real toolchain works.
//
// The scripted `go` from internal/testkit/mutantkit is a real process that
// answers from a table, so it can be made to fail, hang, or answer with
// something no Go release has ever printed — and the check that reads it is the
// real one.

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

// TestMain turns this binary into the scripted `go` when it is started as one.
// The tests below put it on PATH, so without the dispatch every probe would
// start the whole package again.
func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

// hangCutoff is how long the hanging probe below is given before the caller
// walks away. `doctor` gives its own probe [gocmd.DefaultProbeTimeout], which is
// thirty seconds and far too long to wait to learn what a failed row looks like.
//
// It is paid in full every run, so it is as short as the claim allows: two
// hundred milliseconds is far longer than the fake takes to start, and a
// machine so loaded that it has not started is a machine where the probe did
// not answer either, which is the same row.
const hangCutoff = 200 * time.Millisecond

// TestDoctorReportsAToolchainThatDoesNotAnswer is the first row of the table on
// the machine the table exists for: one where go-mutants cannot run.
//
// Three claims, and none of them could be made before the toolchain could be
// scripted. The row is a FAIL and carries the toolchain's own words, so the
// reader is not sent to run the probe by hand. The command exits 2, which is
// what makes `go-mutants doctor` usable as the first step of a CI job. And the
// probe is issued exactly once however many rows depend on it — the platform row
// shares the toolchain the first row located — which is a fact about processes
// and had no way of being observed at all.
func TestDoctorReportsAToolchainThatDoesNotAnswer(t *testing.T) {
	// No t.Parallel in either subtest: the scripted toolchain is published
	// into this process with t.Setenv, which is what `doctor` reads.
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
		// The code belongs on the error path, where every line carries one so
		// that they stay greppable. It does not belong in a cell of a table
		// whose first column is already the verdict.
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

		// `doctor` gives its own probe thirty seconds, which is the right
		// budget for a user and the wrong one for a test, so the caller's own
		// deadline is what ends this one. Every check is asked under it, so
		// only the toolchain row is read here.
		ctx, cancel := context.WithTimeout(t.Context(), hangCutoff)
		defer cancel()
		started := time.Now()
		checks := diagnose(ctx, t.TempDir())
		if elapsed := time.Since(started); elapsed > 30*time.Second {
			t.Fatalf("diagnose waited %s, want the caller's deadline rather than the probe's", elapsed)
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

// checkRow returns the line of a rendered table that belongs to one check.
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
