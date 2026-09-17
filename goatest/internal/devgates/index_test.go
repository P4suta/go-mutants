// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	documentationDirectory = "docs"
	recordDirectory        = "docs/adr"

	indexName = "README.md"
)

var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

func TestTheDocumentationIndexNamesEveryPage(t *testing.T) {
	t.Parallel()
	pinIndex(t, documentationDirectory, []string{indexName})
}

func TestTheRecordIndexNamesEveryRecord(t *testing.T) {
	t.Parallel()
	pinIndex(t, recordDirectory, []string{indexName})
}

func TestTheIndexLedgerSeesAPageThatIsNotLinked(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	linked := indexTargets(t, filepath.Join(root, filepath.FromSlash(recordDirectory), indexName))
	if len(linked) == 0 {
		t.Fatal("the index links nothing, so the ledger would agree with anything")
	}
	if slices.Contains(linked, "0000-a-record-that-does-not-exist.md") {
		t.Fatal("the index links the fixture record, so this test proves nothing")
	}
}

func pinIndex(t *testing.T, directory string, exempt []string) {
	t.Helper()
	root := repositoryRoot(t)
	full := filepath.Join(root, filepath.FromSlash(directory))
	entries, err := os.ReadDir(full)
	if err != nil {
		t.Fatalf("read %s: %v", directory, err)
	}
	var pages []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") || slices.Contains(exempt, entry.Name()) {
			continue
		}
		pages = append(pages, entry.Name())
	}
	linked := indexTargets(t, filepath.Join(full, indexName))
	for _, page := range pages {
		if !slices.Contains(linked, page) {
			t.Errorf("%s/%s does not link %s.\n\n"+
				"A page the index does not name is a page nobody will find.", directory, indexName, page)
		}
	}
	for _, target := range linked {
		if strings.HasSuffix(target, "/"+indexName) || !strings.HasSuffix(target, ".md") {
			continue
		}
		if !slices.Contains(pages, target) {
			t.Errorf("%s/%s links %s, which %s no longer holds.\n\n"+
				"A line naming a page that is gone sends a reader nowhere and says the\n"+
				"project does not know its own shape.", directory, indexName, target, directory)
		}
	}
}

func indexTargets(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var targets []string
	for _, match := range markdownLink.FindAllStringSubmatch(string(data), -1) {
		target := match[1]
		if strings.Contains(target, "://") || strings.HasPrefix(target, "#") {
			continue
		}
		if !slices.Contains(targets, target) {
			targets = append(targets, target)
		}
	}
	return targets
}

func TestEveryDocumentedPathExists(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	var broken []string
	for _, page := range markdownPages(t, root) {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page)))
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		for _, reference := range repositoryPaths(string(data)) {
			if written(t, root, reference) {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(reference))); err != nil {
				broken = append(broken, page+": "+reference)
			}
		}
	}
	if len(broken) > 0 {
		slices.Sort(broken)
		broken = slices.Compact(broken)
		t.Errorf("%d documented path(s) do not exist:\n%s\n\n"+
			"A page that names a file that is gone tells a reader the project moved,\n"+
			"when what moved was the page.", len(broken), strings.Join(broken, "\n"))
	}
}

func TestEveryDocumentedLinkResolves(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	var broken []string
	for _, page := range markdownPages(t, root) {
		directory := filepath.Dir(filepath.Join(root, filepath.FromSlash(page)))
		for _, target := range indexTargets(t, filepath.Join(root, filepath.FromSlash(page))) {
			path, _, _ := strings.Cut(target, "#")
			if path == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(directory, filepath.FromSlash(path))); err != nil {
				broken = append(broken, page+": "+target)
			}
		}
	}
	if len(broken) > 0 {
		slices.Sort(broken)
		broken = slices.Compact(broken)
		t.Errorf("%d documented link(s) resolve to nothing:\n%s",
			len(broken), strings.Join(broken, "\n"))
	}
}

func written(t *testing.T, root, reference string) bool {
	t.Helper()
	first, _, _ := strings.Cut(reference, "/")
	for _, pattern := range ignoredRoots(t, root) {
		if pattern == first {
			return true
		}
	}
	return false
}

func ignoredRoots(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	var roots []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name := strings.Trim(trimmed, "/")
		if name != "" && !strings.ContainsAny(name, "*?[") {
			roots = append(roots, name)
		}
	}
	return roots
}

func markdownPages(t *testing.T, root string) []string {
	t.Helper()
	var pages []string
	walk := func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative != "." && skipDirectory(relative, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".md") {
			pages = append(pages, relative)
		}
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	return pages
}
