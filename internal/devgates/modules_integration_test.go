// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package devgates

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
)

// secondModule is the directory the runner's module root sits in.
const secondModule = "goatest"

// TestEveryWholeTreePatternNamesBothModules refuses a command that says
// `./...` and means one module.
//
// `go build ./...` does not descend into a nested module. cmd/go's package
// walker returns SkipDir the moment it finds a go.mod below the one it started
// from, and it does it without a word -- so a task that says it builds every
// package builds thirty-two fewer than it claims, and `mise run check` can be
// green over a runner that does not compile.
//
// Measured rather than assumed: `go list ./...` at the root of this workspace
// names no package under goatest/, and `go list ./goatest/...` names
// thirty-two. The go.work file changes neither answer; a workspace decides
// which *module* a command resolves against, not which directories a pattern
// walks.
//
// So every whole-tree pattern in a task has to name the second module on the
// same line. Naming it in a later step of the same task would leave a task
// whose first step passes over a tree that does not build, and the failure
// would name the step that did look.
func TestEveryWholeTreePatternNamesBothModules(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	text := readFile(t, root+"/mise.toml")
	var offenders []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, "./...") {
			continue
		}
		if strings.Contains(trimmed, "./"+secondModule+"/...") {
			continue
		}
		offenders = append(offenders, trimmed)
	}
	if len(offenders) != 0 {
		t.Errorf("%d command(s) say ./... and mean one module;\n\t%s\n"+
			"\tadd ./%s/... on the same line -- a nested module is skipped in silence",
			len(offenders), strings.Join(offenders, "\n\t"), secondModule)
	}
}

// TestTheWholeTreePatternReallyStopsAtTheNestedModule is the measurement the
// test above rests on, taken rather than quoted.
//
// A rule about `./...` is only worth enforcing while `./...` behaves this way.
// If a future toolchain descended into nested modules, the gate above would be
// ceremony and every line it forces would be redundant -- and nothing else here
// would say so.
func TestTheWholeTreePatternReallyStopsAtTheNestedModule(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	whole := goList(t, root, "./...")
	if strings.Contains(whole, "/"+secondModule+"/") {
		t.Errorf("`go list ./...` now descends into %s/, so the gate beside this test is obsolete", secondModule)
	}
	named := goList(t, root, "./"+secondModule+"/...")
	if !strings.Contains(named, "/"+secondModule+"/") {
		t.Fatalf("`go list ./%s/...` named nothing in the second module", secondModule)
	}
}

// goList runs the go command over one pattern and returns what it named.
func goList(t testing.TB, root, pattern string) string {
	t.Helper()
	cmd := exec.Command(testkit.GoBinary(t), "list", pattern)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %s: %v", pattern, err)
	}
	return string(out)
}
