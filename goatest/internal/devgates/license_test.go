//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/P4suta/go-mutants/goatest/internal/testkit"
)

const (
	reuseManifest = "REUSE.toml"

	spdxIdentifier = "SPDX-License-Identifier"
)

func TestEveryTrackedFileSaysUnderWhatTermsItIsOffered(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	tracked := trackedFiles(t, testkit.GitBinary(t), root)
	annotated := annotatedPaths(t, filepath.Join(root, reuseManifest))

	var silent []string
	matched := make(map[string]bool, len(annotated))
	for _, relative := range tracked {
		if carriesLicenceHeader(t, filepath.Join(root, filepath.FromSlash(relative))) {
			continue
		}
		covering := matchingAnnotations(annotated, relative)
		if len(covering) == 0 {
			silent = append(silent, relative)
			continue
		}
		for _, pattern := range covering {
			matched[pattern] = true
		}
	}
	if len(silent) > 0 {
		slices.Sort(silent)
		t.Errorf("%d tracked file(s) carry no %s header and no %s entry:\n%s\n\n"+
			"Add the header where the format has a comment, and an annotation where it\n"+
			"does not - a golden file compared byte for byte cannot hold one, and a\n"+
			"JSON Lines fixture has nowhere to put it.",
			len(silent), spdxIdentifier, reuseManifest, strings.Join(silent, "\n"))
	}
	for _, pattern := range annotated {
		if !matched[pattern] {
			t.Errorf("%s annotates %q, which matches no tracked file that needs it.\n\n"+
				"Two things look like this and the wording is deliberate. Either the\n"+
				"files it described are gone - an annotation covers a pattern, so it\n"+
				"outlives them and goes on asserting a licence for nothing - or they are\n"+
				"here and carry their own headers, which this manifest exists to avoid\n"+
				"needing. Both are entries to delete; neither is a file without a licence.",
				reuseManifest, pattern)
		}
	}
}

func TestTheLicenceLedgerSeesAFileThatSaysNothing(t *testing.T) {
	t.Parallel()
	annotated := annotatedPaths(t, filepath.Join(repositoryRoot(t), reuseManifest))
	if len(annotated) == 0 {
		t.Fatal("the manifest annotates nothing, so the ledger would agree with anything")
	}
	if len(matchingAnnotations(annotated, "a/file/nothing/covers.bin")) != 0 {
		t.Fatal("an uncovered path matched an annotation, so the ledger proves nothing")
	}
}

func trackedFiles(t *testing.T, git, root string) []string {
	t.Helper()
	command := exec.CommandContext(t.Context(), git, "-C", root, "ls-files")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list the tracked files: %v", err)
	}
	var tracked []string
	for _, line := range strings.Split(string(output), "\n") {
		if path := strings.TrimSpace(line); path != "" {
			tracked = append(tracked, path)
		}
	}
	return tracked
}

func carriesLicenceHeader(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), spdxIdentifier)
}

func annotatedPaths(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", reuseManifest, err)
	}
	var manifest struct {
		Annotations []struct {
			Path []string `toml:"path"`
		} `toml:"annotations"`
	}
	if err := toml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode %s: %v", reuseManifest, err)
	}
	var patterns []string
	for _, annotation := range manifest.Annotations {
		patterns = append(patterns, annotation.Path...)
	}
	return patterns
}

func matchingAnnotations(patterns []string, relative string) []string {
	var covering []string
	for _, pattern := range patterns {
		if prefix, found := strings.CutSuffix(pattern, "/**"); found {
			if strings.HasPrefix(relative, prefix+"/") {
				covering = append(covering, pattern)
			}
			continue
		}
		if pattern == relative {
			covering = append(covering, pattern)
		}
	}
	return covering
}

func TestEveryAnnotationPatternIsOneThisGateImplements(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	patterns := annotatedPaths(t, filepath.Join(root, reuseManifest))
	if len(patterns) == 0 {
		t.Fatalf("%s yielded no annotated path, and this repository has several", reuseManifest)
	}
	for _, pattern := range patterns {
		bare, _ := strings.CutSuffix(pattern, "/**")
		if strings.ContainsAny(bare, "*?[") {
			t.Errorf("%s annotates %q, and this gate implements only an exact path or a trailing /**; it would report the entry as covering nothing, which is the wrong reason",
				reuseManifest, pattern)
		}
	}
}
