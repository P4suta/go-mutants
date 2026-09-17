// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package evidence_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/evidence"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func TestScanRefusesWhatItCannotHashAsAFile(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		place   func(t *testing.T, root string)
		message string
	}{
		{
			name: "a symbolic link",
			place: func(t *testing.T, root string) {
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "elsewhere.go")); err != nil {
					t.Fatal(err)
				}
			},
			message: "refuses symbolic link elsewhere.go",
		},
		{
			name:    "a socket",
			place:   func(t *testing.T, root string) { placeSocket(t, filepath.Join(root, "socket.go")) },
			message: "refuses irregular file socket.go",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package fixture\n"), filemode.ReadableFile); err != nil {
				t.Fatal(err)
			}
			test.place(t, root)
			files, corpus, err := evidence.Scan(root)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Scan = (%v, %v, %v), want %q", files, corpus, err, test.message)
			}
		})
	}
}

func placeSocket(t *testing.T, path string) {
	t.Helper()
	bound := filepath.Join("/tmp", fmt.Sprintf("goatest-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	listener, err := net.Listen("unix", bound)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(bound)
	})
	if err := os.Rename(bound, path); err != nil {
		t.Fatal(err)
	}
}
