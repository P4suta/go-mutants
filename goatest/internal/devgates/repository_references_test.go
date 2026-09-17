//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/testkit"
)

const (
	repositoryReferenceLedger = "internal/devgates/repository_references.txt"

	repositoryName = "P4suta/go-mutants"

	importPrefix = "github.com/"

	urlPrefix = "https://github.com/"

	ledgerTestFile = "internal/devgates/repository_references_test.go"
)

func readLedgerPaths(t *testing.T, root, ledger string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ledger)))
	if err != nil {
		t.Fatalf("read %s: %v", ledger, err)
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		paths = append(paths, trimmed)
	}
	if len(paths) == 0 {
		t.Fatalf("%s lists no file, and this repository refers to itself by name in several", ledger)
	}
	return paths
}

func referencesRepositoryByName(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	remaining := string(data)
	for {
		index := strings.Index(remaining, repositoryName)
		if index < 0 {
			return false
		}
		before := remaining[:index]
		switch {
		case strings.HasSuffix(before, urlPrefix):
			return true
		case !strings.HasSuffix(before, importPrefix):
			return true
		}
		remaining = remaining[index+len(repositoryName):]
	}
}

func TestEveryFileNamingTheRepositoryIsInTheLedger(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	listed := readLedgerPaths(t, root, repositoryReferenceLedger)
	for _, relative := range trackedFiles(t, testkit.GitBinary(t), root) {
		if relative == repositoryReferenceLedger || relative == ledgerTestFile {
			continue
		}
		if !referencesRepositoryByName(t, filepath.Join(root, filepath.FromSlash(relative))) {
			continue
		}
		if !slices.Contains(listed, relative) {
			t.Errorf("%s names this repository as a repository and is absent from %s, so a migration that rewrites import paths would rewrite it too",
				relative, repositoryReferenceLedger)
		}
	}
}

func TestEveryLedgeredFileStillNamesTheRepository(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, relative := range readLedgerPaths(t, root, repositoryReferenceLedger) {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s lists %s, which this repository does not hold", repositoryReferenceLedger, relative)
			continue
		}
		if !referencesRepositoryByName(t, path) {
			t.Errorf("%s lists %s, which no longer names this repository as a repository", repositoryReferenceLedger, relative)
		}
	}
}

func TestTheRepositoryReferenceLedgerIsSorted(t *testing.T) {
	t.Parallel()
	listed := readLedgerPaths(t, repositoryRoot(t), repositoryReferenceLedger)
	if !slices.IsSorted(listed) {
		t.Errorf("%s lists %v, which is not sorted", repositoryReferenceLedger, listed)
	}
}

func TestTheClassificationTellsAnImportPathFromARepositoryName(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		content string
		want    bool
	}{
		{name: "import", content: `import "github.com/P4suta/go-mutants/goatest/internal/report"`},
		{name: "module", content: "module github.com/P4suta/go-mutants/goatest"},
		{name: "url", content: "see https://github.com/P4suta/go-mutants/goatest/releases", want: true},
		{name: "bare", content: "gh attestation verify x --repo P4suta/go-mutants", want: true},
		{name: "import-then-url", content: "github.com/P4suta/go-mutants/goatest/internal and https://github.com/P4suta/go-mutants/goatest", want: true},
		{name: "absent", content: "nothing here"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "sample.txt")
			if err := os.WriteFile(path, []byte(testCase.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := referencesRepositoryByName(t, path); got != testCase.want {
				t.Errorf("referencesRepositoryByName(%q) = %t, want %t", testCase.content, got, testCase.want)
			}
		})
	}
}
