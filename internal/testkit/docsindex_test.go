// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	docsDir          = "docs"
	docsIndex        = "docs/README.md"
	adrIndexLink     = "adr/README.md"
	statusPrefix     = "**Status:"
	adrStatusHeading = "## Status"
)

var relativeLink = regexp.MustCompile(`\]\(([A-Za-z0-9._/-]+\.md)(?:#[A-Za-z0-9-]*)?\)`)

func TestDocsIndexLinksEveryPage(t *testing.T) {
	t.Parallel()

	root := Root(t)
	pages := documentationPages(t, root)
	if len(pages) < 8 {
		t.Fatalf("the scan found %d pages under %s, which is too few to be this repository", len(pages), docsDir)
	}
	linked := linksIn(t, filepath.Join(root, filepath.FromSlash(docsIndex)))

	for _, page := range pages {
		if !slices.Contains(linked, page) {
			t.Errorf("%s does not link to %s;\n"+
				"\tan index that does not name a page is one a reader cannot find it from", docsIndex, page)
		}
	}
	if !slices.Contains(linked, adrIndexLink) {
		t.Errorf("%s does not link to %s, so the records are reachable only by knowing they are there", docsIndex, adrIndexLink)
	}
	for _, target := range linked {
		path := filepath.Join(root, docsDir, filepath.FromSlash(target))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s links to %s, which is not there: %v", docsIndex, target, err)
		}
	}
}

func TestEveryDocumentationPageDeclaresItsStatus(t *testing.T) {
	t.Parallel()

	root := Root(t)
	for _, page := range documentationPages(t, root) {
		body := readPage(t, filepath.Join(root, docsDir, filepath.FromSlash(page)))
		if !declaresStatus(body) {
			t.Errorf("%s/%s has no %s line within three lines of its title;\n"+
				"\ta page that does not say how much of it is built reads as a description of something that may be a plan",
				docsDir, page, statusPrefix)
		}
	}

	records := recordPages(t, root)
	if len(records) < 5 {
		t.Fatalf("the scan found %d records, which is too few to be this repository", len(records))
	}
	for _, record := range records {
		body := readPage(t, filepath.Join(root, docsDir, "adr", record))
		section, ok := sectionOf(body, adrStatusHeading)
		if !ok {
			t.Errorf("%s/adr/%s has no `%s` section", docsDir, record, adrStatusHeading)
			continue
		}
		if strings.TrimSpace(section) == "" {
			t.Errorf("%s/adr/%s has an empty `%s` section", docsDir, record, adrStatusHeading)
		}
	}
}

func TestTheReadmeDocumentationListAndTheDocsIndexAgree(t *testing.T) {
	t.Parallel()

	root := Root(t)
	readme := readPage(t, filepath.Join(root, "README.md"))
	section, ok := sectionOf(readme, "## Documentation")
	if !ok {
		t.Fatalf("README.md has no `## Documentation` section")
	}

	var fromReadme []string
	for _, match := range relativeLink.FindAllStringSubmatch(section, -1) {
		fromReadme = append(fromReadme, filepath.ToSlash(filepath.Clean(match[1])))
	}
	if len(fromReadme) == 0 {
		t.Fatalf("README.md's Documentation section links to no page")
	}

	var fromIndex []string
	for _, target := range linksIn(t, filepath.Join(root, filepath.FromSlash(docsIndex))) {
		fromIndex = append(fromIndex, filepath.ToSlash(filepath.Clean(filepath.Join(docsDir, target))))
	}

	slices.Sort(fromReadme)
	slices.Sort(fromIndex)
	if !slices.Equal(slices.Compact(fromReadme), slices.Compact(fromIndex)) {
		t.Errorf("README.md's Documentation list and %s name different sets:\n\tREADME: %v\n\tindex:  %v",
			docsIndex, fromReadme, fromIndex)
	}
}

func documentationPages(t *testing.T, root string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(root, docsDir))
	if err != nil {
		t.Fatalf("reading %s: %v", docsDir, err)
	}
	var pages []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") || name == "README.md" {
			continue
		}
		pages = append(pages, name)
	}
	slices.Sort(pages)
	return pages
}

func recordPages(t *testing.T, root string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(root, docsDir, "adr"))
	if err != nil {
		t.Fatalf("reading %s/adr: %v", docsDir, err)
	}
	var records []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") || name == "README.md" {
			continue
		}
		records = append(records, name)
	}
	slices.Sort(records)
	return records
}

func linksIn(t *testing.T, path string) []string {
	t.Helper()

	var targets []string
	for _, match := range relativeLink.FindAllStringSubmatch(readPage(t, path), -1) {
		targets = append(targets, match[1])
	}
	slices.Sort(targets)
	return slices.Compact(targets)
}

func readPage(t *testing.T, path string) string {
	t.Helper()

	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(source)
}

func declaresStatus(body string) bool {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		seen := 0
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" {
				continue
			}
			if strings.HasPrefix(next, statusPrefix) {
				return true
			}
			if seen++; seen >= 3 {
				return false
			}
		}
		return false
	}
	return false
}

func sectionOf(body, heading string) (string, bool) {
	lines := strings.Split(body, "\n")
	depth := len(heading) - len(strings.TrimLeft(heading, "#"))
	for i, line := range lines {
		if strings.TrimRight(line, " ") != heading {
			continue
		}
		var section []string
		for _, next := range lines[i+1:] {
			if strings.HasPrefix(next, "#") {
				if d := len(next) - len(strings.TrimLeft(next, "#")); d <= depth {
					break
				}
			}
			section = append(section, next)
		}
		return strings.Join(section, "\n"), true
	}
	return "", false
}
