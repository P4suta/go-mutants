// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package app

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/cli"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func irregularCacheEntry(t *testing.T, root string) {
	t.Helper()
	directory := filepath.Join(root, ".goatest", "cache", "v1", "entry")
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "report.json"), nil, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(directory, "pipe"), uint32(filemode.PrivateFile)); err != nil {
		t.Skipf("this platform does not make named pipes: %v", err)
	}
}

func TestACacheOperationStopsAtAnEntryItRefusesToRead(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"status", "gc", "flush"} {
		root := cacheServiceRoot(t)
		irregularCacheEntry(t, root)
		service := Service{Root: root, TempDirectory: t.TempDir()}
		_, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, action)
		if err == nil || !strings.Contains(err.Error(), "irregular file") {
			t.Errorf("cache %s over an entry holding a named pipe reported %v", action, err)
		}
	}
}

func TestACacheCollectionRefusesAPolicyBelowZero(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = 1\ncontract = \"standard-v1\"\n[cache]\nmax_bytes = -1\n"),
		filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	service := Service{Root: root, TempDirectory: t.TempDir()}
	_, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "gc")
	if err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("a collection under a policy below zero reported %v", err)
	}
}
