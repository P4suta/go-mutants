// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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
	if tc.Version.GOOS != runtime.GOOS || tc.Version.GOARCH != runtime.GOARCH {
		t.Errorf("target = %s/%s, want %s/%s", tc.Version.GOOS, tc.Version.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
	if !strings.Contains(tc.String(), tc.GoBin) || !strings.Contains(tc.String(), tc.Version.Raw) {
		t.Errorf("String() = %q, want it to name both the path and the version", tc.String())
	}
}

func TestLocateHonoursAnExplicitPath(t *testing.T) {
	found, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("looking up the go that is running this test: %v", err)
	}
	found, err = filepath.Abs(found)
	if err != nil {
		t.Fatalf("resolving %q: %v", found, err)
	}

	t.Setenv("PATH", t.TempDir())

	tc, err := gocmd.Locate(gocmd.Options{Explicit: found})
	if err != nil {
		t.Fatalf("Locate with an explicit path and nothing on PATH = %v, want the configured "+
			"toolchain: configuration is the only way in here", err)
	}
	if tc.GoBin != found {
		t.Errorf("GoBin = %q, want the explicitly configured %q", tc.GoBin, found)
	}
}

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
