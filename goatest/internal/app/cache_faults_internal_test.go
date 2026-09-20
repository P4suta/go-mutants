// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/cli"
	"github.com/P4suta/go-mutants/goatest/internal/evidence"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func cacheServiceRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = 1\ncontract = \"standard-v1\"\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	return root
}

func blockWith(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("this is not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
}

func TestACacheOperationStopsAtTheFirstThingItCannotRead(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		blocked func(root string) string
		actions []string
	}{
		{
			name:    "a trace store that is a file",
			blocked: func(root string) string { return filepath.Join(root, ".goatest", "trace") },
			actions: []string{"status", "gc"},
		},
		{
			name:    "a diagnostics store that is a file",
			blocked: func(root string) string { return filepath.Join(root, ".goatest", "diagnostics") },
			actions: []string{"status", "gc"},
		},
		{
			name:    "a repair candidate store that is a file",
			blocked: func(root string) string { return filepath.Join(root, ".goatest", "candidates") },
			actions: []string{"status", "gc"},
		},
		{
			name:    "a repair patch store that is a file",
			blocked: func(root string) string { return filepath.Join(root, ".goatest", "patches") },
			actions: []string{"status", "gc"},
		},
		{
			name:    "a cache store that is a file",
			blocked: func(root string) string { return filepath.Join(root, ".goatest", "cache") },
			actions: []string{"status", "gc", "flush"},
		},
		{
			name:    "a report directory that is a file",
			blocked: func(root string) string { return filepath.Join(root, "reports", "runs") },
			actions: []string{"status", "gc"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, action := range test.actions {
				root := cacheServiceRoot(t)
				blockWith(t, test.blocked(root))
				service := Service{Root: root, TempDirectory: t.TempDir()}
				if _, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, action); err == nil {
					t.Errorf("cache %s over %s reported nothing", action, test.name)
				}
			}
		})
	}
}

func TestFlushingRefusesMutationEvidenceItCannotRemove(t *testing.T) {
	t.Parallel()
	root := cacheServiceRoot(t)
	stored := filepath.Join(root, ".goatest", "cache", evidence.MutationFileName)
	if err := os.MkdirAll(stored, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	service := Service{Root: root, TempDirectory: t.TempDir()}
	_, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "flush")
	if err == nil {
		t.Fatal("a flush over mutation evidence it cannot remove reported nothing")
	}
	if _, statErr := os.Stat(stored); statErr != nil {
		t.Errorf("the flush it refused removed the path anyway: %v", statErr)
	}
}

func TestACacheOperationStopsAtABuildLayerItCannotRead(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"status", "gc"} {
		root := t.TempDir()
		layer := filepath.Join(root, "build-layer")
		if err := os.WriteFile(layer, []byte("this is not a directory"), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
			[]byte("version = 1\ncontract = \"standard-v1\"\n[cache]\nbuild_dir = "+
				strconv.Quote(layer)+"\n"), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
		service := Service{Root: root, TempDirectory: t.TempDir()}
		if _, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, action); err == nil {
			t.Errorf("cache %s over a build layer that is a file reported nothing", action)
		}
	}
}

func TestACacheOperationRefusesAnActionItDoesNotKnow(t *testing.T) {
	t.Parallel()
	root := cacheServiceRoot(t)
	service := Service{Root: root, TempDirectory: t.TempDir()}
	_, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, "reticulate")
	if err == nil {
		t.Fatal("an action nothing implements was accepted")
	}
}

func TestACacheOperationReadsTheConfigurationBeforeAnythingElse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = \"not a number\"\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	service := Service{Root: root, TempDirectory: t.TempDir()}
	for _, action := range []string{"status", "gc", "flush"} {
		if _, err := service.Execute(t.Context(), cli.CommandCache, cli.Request{}, action); err == nil {
			t.Errorf("cache %s over a configuration nothing can read reported nothing", action)
		}
	}
}

func TestASweepThatCouldNotRunSaysSoRatherThanStopping(t *testing.T) {
	t.Parallel()
	unreadable := "version = \"not a number\"\n"
	sound := "version = 1\ncontract = \"standard-v1\"\n"
	for _, test := range []struct {
		name    string
		config  string
		blocked string
		sweep   func(Service, string)
		want    string
	}{
		{
			name: "a diagnostic sweep over a configuration nothing can read", config: unreadable,
			sweep: Service.collectDiagnosticRetention, want: "diagnostic-gc-unavailable",
		},
		{
			name: "a diagnostic sweep over a store that is a file", config: sound,
			blocked: filepath.Join(".goatest", "trace"),
			sweep:   Service.collectDiagnosticRetention, want: "diagnostic-gc-unavailable",
		},
		{
			name: "a verdict sweep over a configuration nothing can read", config: unreadable,
			sweep: Service.collectVerdictCache, want: "cache-gc-unavailable",
		},
		{
			name:   "a verdict sweep over a policy below zero",
			config: "version = 1\ncontract = \"standard-v1\"\n[cache]\nmax_bytes = -1\n",
			sweep:  Service.collectVerdictCache, want: "cache-gc-unavailable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
				[]byte(test.config), filemode.PrivateFile); err != nil {
				t.Fatal(err)
			}
			if test.blocked != "" {
				blockWith(t, filepath.Join(root, test.blocked))
			}
			var progress strings.Builder
			test.sweep(Service{Root: root, TempDirectory: t.TempDir(), Progress: &progress}, root)
			if !strings.Contains(progress.String(), test.want) {
				t.Fatalf("%s said %q, want it to say %q", test.name, progress.String(), test.want)
			}
		})
	}
}

func TestASweepThatRanSaysNothing(t *testing.T) {
	t.Parallel()
	root := cacheServiceRoot(t)
	var progress strings.Builder
	service := Service{Root: root, TempDirectory: t.TempDir(), Progress: &progress}
	service.collectDiagnosticRetention(root)
	service.collectVerdictCache(root)
	if progress.Len() != 0 {
		t.Fatalf("a sweep over a repository it can read said %q, want nothing", progress.String())
	}
}
