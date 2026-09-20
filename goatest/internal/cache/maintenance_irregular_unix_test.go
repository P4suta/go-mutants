// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package cache

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func TestInspectRefusesAnEntryHoldingAFileThatIsNotRegular(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := cacheEntryAt(t, root, "entry", 4, time.Time{})
	if err := syscall.Mkfifo(filepath.Join(directory, "pipe"), uint32(filemode.PrivateFile)); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(root)
	if err == nil || !strings.Contains(err.Error(), "contains irregular file") {
		t.Fatalf("Inspect = (%+v, %v), want the named pipe refused", status, err)
	}
}
