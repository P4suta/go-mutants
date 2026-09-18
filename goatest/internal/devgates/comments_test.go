// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	commentLineLimit    = 1
	diagnosticTextLimit = 72
	decimalBase         = 10
)

var allowedDirectives = []string{"//go:", "//line ", "//nolint", "//exhaustive:", "//gitleaks:", "//goatest:"}

func TestNoGoFileCarriesACommentThatIsNotAllowed(t *testing.T) {
	t.Parallel()

	roots := []string{moduleRoot(t)}
	if engine, found := engineRoot(t); found {
		roots = append(roots, engine)
	}
	var offenders []string
	for _, root := range roots {
		offenders = append(offenders, commentOffenders(t, root)...)
	}
	if len(offenders) != 0 {
		t.Errorf("%d comment(s) are neither a licence header, a directive, nor a doc comment of %d line:\n\t%s",
			len(offenders), commentLineLimit, strings.Join(offenders, "\n\t"))
	}
}

func TestNoOtherFileCarriesACommentThatIsNotAllowed(t *testing.T) {
	t.Parallel()

	binary, err := exec.LookPath("ocomment")
	if err != nil {
		t.Fatalf("ocomment is not on PATH: %v", err)
	}
	if _, err := os.ReadFile(binary); err != nil {
		t.Fatalf("read the comment gate's own tool: %v", err)
	}
	root := moduleRoot(t)
	if engine, found := engineRoot(t); found {
		root = engine
	}
	listed, err := trackedPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, name := range listed {
		if !strings.HasSuffix(name, ".go") {
			paths = append(paths, name)
		}
	}
	if len(paths) == 0 {
		t.Fatal("no file was offered to the comment gate")
	}
	command := exec.Command(binary, append([]string{"check", "--policy", "legal"}, paths...)...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		return
	}
	t.Errorf("a file that is not Go carries a comment that is neither a licence nor a directive:\n%s", output)
}

func commentOffenders(t *testing.T, root string) []string {
	t.Helper()
	var offenders []string
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if name == ".git" || name == "testdata" || name == "fixtures" || name == "vendor-assets" ||
				name == "dist" || name == "reports" || name == ".goatest" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		offenders = append(offenders, offendingComments(t, root, path)...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking %s: %v", root, walkErr)
	}
	return offenders
}

func offendingComments(t *testing.T, root, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	docs := map[*ast.CommentGroup]bool{}
	if file.Doc != nil {
		docs[file.Doc] = true
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch declared := node.(type) {
		case *ast.GenDecl:
			docs[declared.Doc] = true
		case *ast.FuncDecl:
			docs[declared.Doc] = true
		case *ast.TypeSpec:
			docs[declared.Doc] = true
		case *ast.ValueSpec:
			docs[declared.Doc] = true
		case *ast.Field:
			docs[declared.Doc] = true
		}
		return true
	})
	relative, relErr := filepath.Rel(root, path)
	if relErr != nil {
		relative = path
	}
	var offenders []string
	for _, group := range file.Comments {
		if allowedGroup(group, docs[group]) {
			continue
		}
		position := fset.Position(group.Pos())
		offenders = append(offenders, relative+":"+itoa(position.Line)+": "+firstLine(group))
	}
	return offenders
}

func allowedGroup(group *ast.CommentGroup, isDoc bool) bool {
	prose := 0
	for _, comment := range group.List {
		if allowedLine(comment.Text) {
			continue
		}
		prose++
	}
	if prose == 0 {
		return true
	}
	return isDoc && prose <= commentLineLimit
}

func allowedLine(text string) bool {
	for _, directive := range allowedDirectives {
		if strings.HasPrefix(text, directive) {
			return true
		}
	}
	body := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "//"), "/*"))
	return strings.HasPrefix(body, "SPDX-")
}

func firstLine(group *ast.CommentGroup) string {
	text := group.List[0].Text
	if len(text) > diagnosticTextLimit {
		return text[:diagnosticTextLimit] + "…"
	}
	return text
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%decimalBase)}, digits...)
		n /= decimalBase
	}
	return string(digits)
}

func trackedPaths(root string) ([]string, error) {
	command := exec.Command("git", "ls-files", "-z")
	command.Dir = root
	listed, err := command.Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, name := range strings.Split(strings.TrimRight(string(listed), "\x00"), "\x00") {
		if name == "" || strings.Contains(name, "/testdata/") || strings.HasPrefix(name, "testdata/") ||
			strings.HasPrefix(name, "fixtures/") || strings.HasPrefix(name, "vendor-assets/") ||
			strings.HasPrefix(name, "LICENSES/") {
			continue
		}
		if _, statErr := filepath.Abs(filepath.Join(root, name)); statErr != nil {
			continue
		}
		paths = append(paths, name)
	}
	return paths, nil
}
