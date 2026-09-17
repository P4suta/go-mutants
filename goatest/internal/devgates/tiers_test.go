// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const integrationTag = "integration"

var toolchainNeedles = []string{
	"GoBinary" + "(",
	"GitBinary" + "(",
	".Git" + "()",
	"LookPath" + `("go")`,
	"LookPath" + `("git")`,
}

func TestEveryTestThatStartsAToolchainIsInTheIntegrationTier(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	files, err := scanTestFiles(root)
	if err != nil {
		t.Fatalf("scan the test files: %v", err)
	}
	var untagged, stale []string
	for _, found := range files {
		switch {
		case found.startsToolchain && !found.integration:
			untagged = append(untagged, found.path+": "+strings.Join(found.needles, ", "))
		case !found.startsToolchain && found.integration:
			stale = append(stale, found.path)
		}
	}
	if len(untagged) > 0 {
		slices.Sort(untagged)
		t.Errorf("%d test file(s) start a real toolchain without //go:build %s:\n%s\n\n"+
			"A unit tier that starts processes is a unit tier that fails on a machine\n"+
			"with no toolchain, which is the one situation it exists to survive. Add\n"+
			"the tag, or move the test that needs the tool into a file that carries it.",
			len(untagged), integrationTag, strings.Join(untagged, "\n"))
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		t.Errorf("%d test file(s) carry //go:build %s and start nothing:\n%s\n\n"+
			"The tag hides a file from `go test ./...` completely. A stale one is a\n"+
			"suite that stopped running without being skipped, failed or counted - it\n"+
			"is not compiled, so nothing else in this tree can notice. Remove the tag.",
			len(stale), integrationTag, strings.Join(stale, "\n"))
	}
}

func TestTheTierScanReadsTheSpellingsThisRepositoryUses(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"path := testkit." + toolchainNeedles[0] + "t)",
		"path := " + toolchainNeedles[1] + "t)",
		"repository := testkit.NewRepo(t).BoundaryFixture()" + toolchainNeedles[2],
		"path, err := exec." + toolchainNeedles[4],
	} {
		if found := matchedNeedles(source); len(found) == 0 {
			t.Errorf("matchedNeedles(%q) found nothing", source)
		}
	}
	for _, source := range []string{
		"result.Repository.Git.Commit = commit",
		"if metadata.Git.Available {",
		"binary := filepath.Join(root, \"go\")",
	} {
		if found := matchedNeedles(source); len(found) != 0 {
			t.Errorf("matchedNeedles(%q) = %q, want none", source, found)
		}
	}
}

type testFile struct {
	path string

	integration bool

	startsToolchain bool

	needles []string
}

func scanTestFiles(root string) ([]testFile, error) {
	var found []testFile
	walk := func(path string, entry fs.DirEntry, err error) error {
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
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(data)
		code, err := withoutComments(path, source)
		if err != nil {
			return err
		}
		needles := matchedNeedles(code)
		found = append(found, testFile{
			path:            relative,
			integration:     hasIntegrationConstraint(source),
			startsToolchain: len(needles) != 0,
			needles:         needles,
		})
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		return nil, err
	}
	return found, nil
}

func hasIntegrationConstraint(source string) bool {
	for line := range strings.SplitSeq(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "//") {
			if strings.HasPrefix(trimmed, "package ") {
				return false
			}
			continue
		}
		expression, err := constraint.Parse(trimmed)
		if err != nil {
			continue
		}
		if namesTag(expression, integrationTag) {
			return true
		}
	}
	return false
}

func namesTag(expression constraint.Expr, tag string) bool {
	switch typed := expression.(type) {
	case *constraint.TagExpr:
		return typed.Tag == tag
	case *constraint.NotExpr:
		return namesTag(typed.X, tag)
	case *constraint.AndExpr:
		return namesTag(typed.X, tag) || namesTag(typed.Y, tag)
	case *constraint.OrExpr:
		return namesTag(typed.X, tag) || namesTag(typed.Y, tag)
	}
	return false
}

func withoutComments(path, source string) (string, error) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, source, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return "", err
	}
	blanked := []byte(source)
	base := fileSet.File(parsed.Pos()).Base()
	for _, group := range parsed.Comments {
		for _, comment := range group.List {
			start := int(comment.Pos()) - base
			end := int(comment.End()) - base
			for index := start; index < end && index < len(blanked); index++ {
				if blanked[index] != '\n' {
					blanked[index] = ' '
				}
			}
		}
	}
	return string(blanked), nil
}

func matchedNeedles(source string) []string {
	var found []string
	for _, needle := range toolchainNeedles {
		for index := 0; ; {
			at := strings.Index(source[index:], needle)
			if at < 0 {
				break
			}
			at += index
			index = at + len(needle)
			if isIdentifierByte(needle[0]) && at > 0 && isIdentifierByte(source[at-1]) {
				continue
			}
			found = append(found, needle)
			break
		}
	}
	return found
}

func isIdentifierByte(character byte) bool {
	switch {
	case character >= 'a' && character <= 'z',
		character >= 'A' && character <= 'Z',
		character >= '0' && character <= '9',
		character == '_':
		return true
	}
	return false
}
