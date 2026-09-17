// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const signingOffSetting = "commit." + "gpgsign" + "=false"

func TestNoSuiteAsksGitToTurnSigningOff(t *testing.T) {
	t.Parallel()

	root := Root(t)
	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && slices.Contains([]string{".git", "fixtures", "testdata", "node_modules"}, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(source), signingOffSetting) {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	if len(offenders) != 0 {
		t.Errorf("%d test file(s) ask git to turn signing off, which a signing-policy wrapper refuses and "+
			"which a repository a test creates never needed:\n\t%s\nuse testkit.GitInit, testkit.GitCommit "+
			"and testkit.Git, or drop the setting", len(offenders), strings.Join(offenders, "\n\t"))
	}
}
