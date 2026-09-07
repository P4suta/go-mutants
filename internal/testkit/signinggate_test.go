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

// signingOffSetting is the git configuration a test must never pass. It is
// spelled in two halves so that this file, which has to name it in order to
// look for it, is not itself a hit.
const signingOffSetting = "commit." + "gpgsign" + "=false"

// TestNoSuiteAsksGitToTurnSigningOff is the rule [GitInit] states,
// made a gate: no test in this module hands git a setting that switches
// commit signing off.
//
// The setting buys nothing — a repository a test creates has no configuration
// file to carry signing, so it is off already — and it costs the whole suite
// on a machine whose git is wrapped by a signing policy that refuses exactly
// that argument, which is a machine this repository is developed on. Two
// suites carried their own git helper with the flag in it for months and could
// not run there at all; this test is what keeps the third from appearing.
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
