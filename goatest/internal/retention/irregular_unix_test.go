// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package retention

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMetadataRefusesAnArtifactHoldingAFileThatIsNotRegular(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := retainedAt(t, root, "entry", retainedBytes, time.Time{})
	bound := filepath.Join("/tmp", fmt.Sprintf("goatest-retention-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	listener, err := net.Listen("unix", bound)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(bound)
	})
	if err := os.Rename(bound, filepath.Join(directory, "socket")); err != nil {
		t.Fatal(err)
	}
	_, _, err = metadata(directory)
	if err == nil || !strings.Contains(err.Error(), "contains irregular file") {
		t.Fatalf("metadata over a socket = %v", err)
	}
}
