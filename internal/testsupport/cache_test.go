// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testsupport

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCacheDirTurnsGoTelemetryOffInTheMovedHome pins the one file CacheDir
// writes into the HOME it moves.
//
// The go command derives its telemetry directory from os.UserConfigDir, which
// on macOS — and on a Linux without XDG_CONFIG_HOME — is derived from HOME, and
// there is no variable of its own to pin it with. A go command that finds no
// mode file there starts in "local" mode: it opens counters and forks a sidecar
// that outlives it. So a test that drove a single `go version` under a moved
// HOME left a detached process writing into its own temporary directory, and
// t.TempDir's cleanup failed with "directory not empty" — which is how
// TestDoctorPublishesItsCheckNames failed on macOS on 2026-09-03. Telemetry is
// turned off in the moved HOME, and only there.
func TestCacheDirTurnsGoTelemetryOffInTheMovedHome(t *testing.T) {
	CacheDir(t)
	home := os.Getenv("HOME")
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("os.UserConfigDir after moving HOME: %v", err)
	}
	if !within(home, config) {
		t.Skipf("%s derives the config directory from something other than HOME (%s), so nothing is written", runtime.GOOS, config)
	}

	data, err := os.ReadFile(filepath.Join(config, "go", "telemetry", "mode"))
	if err != nil {
		t.Fatalf("reading the telemetry mode file below the moved HOME: %v", err)
	}
	if mode, _, _ := strings.Cut(strings.TrimSpace(string(data)), " "); mode != "off" {
		t.Errorf("the telemetry mode below the moved HOME is %q, want %q", mode, "off")
	}
}
