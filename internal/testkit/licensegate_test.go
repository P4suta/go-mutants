// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const spdxNeedle = "SPDX-License" + "-Identifier:"

const headerWindow = 4 << 10

var plannedPaths = map[string]string{
	".release-please/CHANGELOG.generated.md": "release-please creates it on the first release; REUSE.toml annotates it ahead of time so the first release is not also the first licensing failure",
}

func TestEveryFileIsLicensed(t *testing.T) {
	root := Root(t)
	tracked := trackedFiles(t, root)
	if len(tracked) < 100 {
		t.Fatalf("git reports %d tracked files, which is too few to be this repository;"+
			" the scan is not looking where it thinks", len(tracked))
	}

	annotations := ReusePaths(t, root)
	if len(annotations) == 0 {
		t.Fatalf("%s declares no annotation paths; the parser has stopped seeing them", ReuseFile)
	}

	covered := map[string]bool{}
	for _, rel := range tracked {
		if hasInlineHeader(t, filepath.Join(root, filepath.FromSlash(rel))) {
			continue
		}
		covering, err := ReuseCovering(annotations, rel)
		if err != nil {
			t.Fatalf("matching %s: %v", rel, err)
		}
		for _, pattern := range covering {
			covered[pattern] = true
		}
		if len(covering) == 0 {
			t.Errorf("%s carries no %s header and no %s annotation covers it;\n"+
				"\tadd the header, or add the path to %s with a comment saying why it cannot carry one",
				rel, spdxNeedle, ReuseFile, ReuseFile)
		}
	}

	for _, pattern := range annotations {
		live, err := ReuseAnnotationCoversATrackedFile(pattern, tracked)
		if err != nil {
			t.Fatalf("matching %q: %v", pattern, err)
		}
		matchesSomething := covered[pattern] || live
		if _, planned := plannedPaths[pattern]; planned {
			if matchesSomething {
				t.Errorf("%s annotates %q, which this tree now holds;\n"+
					"\tdelete its row from plannedPaths -- the annotation is doing its job\n"+
					"\tand no longer needs an excuse", ReuseFile, pattern)
			}
			continue
		}
		if covered[pattern] {
			continue
		}
		if matchesSomething {
			t.Errorf("%s annotates %q, which is committed and carries its own %s;\n"+
				"\tdelete the row -- this manifest is for the files that cannot carry a header,\n"+
				"\tand this is not a file without a licence", ReuseFile, pattern, spdxNeedle)
			continue
		}
		t.Errorf("%s annotates %q, which no committed file matches;\n"+
			"\tdelete the row -- a manifest that describes files this tree does not hold\n"+
			"\tis one a reader cannot trust about the files it does;\n"+
			"\tthis is not a file without a licence either", ReuseFile, pattern)
	}

	for pattern := range plannedPaths {
		if !slices.Contains(annotations, pattern) {
			t.Errorf("plannedPaths excuses %q, which %s does not annotate any more;\n"+
				"\tdelete the row -- a stale excuse is as wrong as a missing one", pattern, ReuseFile)
		}
	}
}

func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	_ = GitBinary(t)
	out := Git(t, root, "ls-files", "--cached", "--others", "--exclude-standard")
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files
}

func hasInlineHeader(t *testing.T, path string) bool {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("closing %s: %v", path, closeErr)
		}
	}()
	buffer := make([]byte, headerWindow)
	n, err := file.Read(buffer)
	if n == 0 && err != nil {
		return false
	}
	return strings.Contains(string(buffer[:n]), spdxNeedle)
}
