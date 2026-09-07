// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The half of the toolchain wrapper that has to meet a real `go`.
//
// Everything else this package does is now driven by a scripted stand-in in the
// unit tier — a probe that hangs, one that answers garbage, one that exits
// non-zero, a listing that fails — and every one of those is a claim about
// go-mutants' own code. These four are the other kind of claim: that the probe
// agrees with reality. A `go version` line the parser has never seen, a
// toolchain manager that puts `go` somewhere unexpected, a released format
// change — none of them can be discovered from a table, because the table is
// written from the same belief the parser is.
//
// So they run against whatever `go` this machine has, and they are the reason
// the ledger in internal/testkit/testdata/unit-toolchain-allowlist.txt no longer
// names this package: the unit tier drives no toolchain at all.
//
// Run them with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/gocmd

package gocmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

// TestLocateFindsTheToolchainOnPath is the happy path against the real
// toolchain, and the one assertion no fake can make: that what this package
// parses is what a released `go` prints.
func TestLocateFindsTheToolchainOnPath(t *testing.T) {
	t.Parallel()

	tc, err := gocmd.Locate(gocmd.Options{})
	if err != nil {
		t.Fatalf("Locate = %v, want the toolchain running this test", err)
	}
	if !filepath.IsAbs(tc.GoBin) {
		t.Errorf("GoBin = %q, want an absolute path so a later PATH change cannot re-resolve it", tc.GoBin)
	}
	if tc.Version.Raw == "" {
		t.Error("Version.Raw is empty")
	}
	if !strings.HasPrefix(tc.Version.Release, "go") && !tc.Version.IsDevel() {
		t.Errorf("Version.Release = %q, want a go release or a devel build", tc.Version.Release)
	}
	// The toolchain that built this test binary is the toolchain that runs it,
	// so the target it reports has to be the one this code is executing on.
	if tc.Version.GOOS != runtime.GOOS || tc.Version.GOARCH != runtime.GOARCH {
		t.Errorf("target = %s/%s, want %s/%s", tc.Version.GOOS, tc.Version.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
	if !strings.Contains(tc.String(), tc.GoBin) || !strings.Contains(tc.String(), tc.Version.Raw) {
		t.Errorf("String() = %q, want it to name both the path and the version", tc.String())
	}
}

// TestLocateHonoursAnExplicitPath checks that configuration wins over PATH,
// using the toolchain PATH would have found anyway so the assertion is about
// which mechanism was used rather than about which binary exists.
func TestLocateHonoursAnExplicitPath(t *testing.T) {
	t.Parallel()

	found, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("looking up the go that is running this test: %v", err)
	}

	tc, err := gocmd.Locate(gocmd.Options{Explicit: found})
	if err != nil {
		t.Fatalf("Locate with an explicit path = %v, want a toolchain", err)
	}
	if tc.GoBin != found {
		t.Errorf("GoBin = %q, want the explicitly configured %q", tc.GoBin, found)
	}
}

// TestCommandProducesARunnableSpec closes the loop between the two packages:
// the fragment Command returns really is something runner.Run accepts, and the
// real toolchain answers it with the very line the probe parsed.
func TestCommandProducesARunnableSpec(t *testing.T) {
	t.Parallel()

	tc, err := gocmd.Locate(gocmd.Options{})
	if err != nil {
		t.Fatalf("Locate = %v", err)
	}

	spec := tc.Command("version")
	spec.Timeout = gocmd.DefaultProbeTimeout
	result := runner.Run(t.Context(), spec)
	if result.Err != nil {
		t.Fatalf("Err = %v, want nil", result.Err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0; output: %s", result.ExitCode, result.Output)
	}
	if got := strings.TrimSpace(string(result.Output)); got != tc.Version.Raw {
		t.Errorf("`go version` printed %q, want the located %q", got, tc.Version.Raw)
	}
}

// TestLocateRecordsTheVersionProbeAsGoVersion is the first labelled command in
// a run: the toolchain probe. It is here rather than in internal/runner because
// the label belongs to the call site, and this is the only call site that has
// one until the engine's own commands are labelled.
//
// It stays on the real toolchain because the fact it pins is a join between two
// things only a real probe has both of: the binary [testkit.GoBinary] resolved
// on PATH, and the argv the recorder wrote down.
func TestLocateRecordsTheVersionProbeAsGoVersion(t *testing.T) {
	t.Parallel()

	found := testkit.GoBinary(t)
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, time.Now, trace.StartRecord{
		Kind:        trace.StartKindWorkspace,
		ToolVersion: "test",
		PID:         os.Getpid(),
	})

	tc, err := gocmd.LocateContext(t.Context(), gocmd.Options{Trace: recorder})
	if err != nil {
		t.Fatalf("LocateContext = %v, want the toolchain running this test", err)
	}
	want, err := filepath.Abs(found)
	if err != nil {
		t.Fatalf("resolving %q: %v", found, err)
	}
	if tc.GoBin != want {
		t.Fatalf("GoBin = %q, want the toolchain on PATH, %q", tc.GoBin, want)
	}

	events := execEvents(sink)
	if len(events) != 1 {
		t.Fatalf("the recording holds %d exec events, want exactly one for the version probe", len(events))
	}
	rec := events[0].Exec
	if rec.Kind != trace.ExecKindGoVersion {
		t.Errorf("kind = %q, want %q", rec.Kind, trace.ExecKindGoVersion)
	}
	if argv := []string{tc.GoBin, "version"}; !slices.Equal(rec.Argv, argv) {
		t.Errorf("argv = %q, want %q", rec.Argv, argv)
	}
	if rec.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", rec.ExitCode)
	}
	if rec.TimeoutMS != gocmd.DefaultProbeTimeout.Milliseconds() {
		t.Errorf("timeout_ms = %d, want the probe timeout %d", rec.TimeoutMS, gocmd.DefaultProbeTimeout.Milliseconds())
	}
	if rec.OutputBytes == 0 || rec.OutputSHA256 == "" {
		t.Errorf("output_bytes = %d and output_sha256 = %q, want the digest of the version line",
			rec.OutputBytes, rec.OutputSHA256)
	}
}
