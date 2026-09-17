// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package devgates

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

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
//
// Two exemptions, and both are the same fact rather than two conveniences: the
// rule is about `./...` read as "the whole repository", and there are places
// where it provably cannot be read that way. A task with `dir` is already
// inside one module. A task with GOWORK=off has no workspace to aggregate, so
// `./...` is the module it starts in whatever anybody meant -- which is the
// point of build-published, where naming both modules on one line would be
// asking for exactly the aggregate build the task exists to avoid.
func TestEveryWholeTreePatternNamesBothModules(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	var offenders []string
	for name, task := range tasksOf(t, root) {
		// A task with `dir` is already inside one module, and `./...` there
		// means that module -- which is what the runner's own audit tasks mean
		// and what they have to mean, since the runner refuses a workspace. The
		// rule is about a command run at the root, where `./...` reads as the
		// whole repository and is not.
		if task.Dir != "" || task.Env["GOWORK"] == "off" {
			continue
		}
		for _, step := range taskSteps(task.Run) {
			if !strings.Contains(step, "./...") || strings.Contains(step, "./"+secondModule+"/...") {
				continue
			}
			offenders = append(offenders, name+": "+step)
		}
	}
	if len(offenders) != 0 {
		t.Errorf("%d command(s) say ./... and mean one module;\n\t%s\n"+
			"\tadd ./%s/... on the same line -- a nested module is skipped in silence",
			len(offenders), strings.Join(offenders, "\n\t"), secondModule)
	}
}

// miseTask is one task's shape, as much of it as the gates here read.
type miseTask struct {
	// Dir is the directory the task runs in, empty for the repository root.
	Dir string `toml:"dir"`
	// Run is a string for a one-step task and a list for the rest.
	Run any `toml:"run"`
	// RunWindows is the second list a task may carry.
	RunWindows any `toml:"run_windows"`
	// Env is the task's own environment. Only GOWORK is read here, and only to
	// tell a command that means one module on purpose from one that means the
	// repository and gets one module by accident.
	Env map[string]string `toml:"env"`
}

// tasksOf decodes every task mise.toml defines.
func tasksOf(t testing.TB, root string) map[string]miseTask {
	t.Helper()
	var file struct {
		Tasks map[string]miseTask `toml:"tasks"`
	}
	if err := toml.Unmarshal([]byte(readFile(t, root+"/mise.toml")), &file); err != nil {
		t.Fatalf("decoding mise.toml: %v", err)
	}
	if len(file.Tasks) == 0 {
		t.Fatal("mise.toml defines no task, and the reader of it sees many")
	}
	return file.Tasks
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
