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

const secondModule = "goatest"

func TestEveryWholeTreePatternNamesBothModules(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	var offenders []string
	for name, task := range tasksOf(t, root) {
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

type miseTask struct {
	Dir        string            `toml:"dir"`
	Run        any               `toml:"run"`
	RunWindows any               `toml:"run_windows"`
	Env        map[string]string `toml:"env"`
}

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
