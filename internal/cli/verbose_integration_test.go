// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/trace"
)

func TestRunVVPrintsEverySubprocessOfTheKillableFixture(t *testing.T) {
	inKillableFixture(t)

	code, stdout, stderr := execute(t, "run", "-vv", "--trace", "--no-tui", "--no-color")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants run -vv --trace` exited %d\nstderr:\n%s", code, stderr)
	}

	var directory string
	for _, line := range strings.Split(stdout, "\n") {
		if rest, ok := strings.CutPrefix(line, "trace: "); ok {
			directory = rest
		}
	}
	if directory == "" {
		t.Fatalf("the run did not say where it recorded:\n%s", stdout)
	}
	events, err := trace.Read(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatalf("the recording does not read back: %v", err)
	}

	recorded := map[string]int{}
	for _, event := range events {
		recorded[event.Type]++
	}
	printed := map[string]int{}
	for _, line := range strings.Split(stdout, "\n") {
		rest, ok := strings.CutPrefix(line, "  ")
		if !ok {
			continue
		}
		if word, _, found := strings.Cut(rest, " "); found {
			printed[word]++
		}
	}

	if recorded[trace.TypeExec] == 0 {
		t.Fatal("the run recorded no subprocess at all, so the comparison proves nothing")
	}
	if got, want := printed["exec"], recorded[trace.TypeExec]; got != want {
		t.Errorf("the console shows %d exec lines and the recording holds %d exec events", got, want)
	}
	if recorded[trace.TypeMutantExec] == 0 {
		t.Fatal("the run executed no mutant, so the fixture is not what this test is about")
	}
	if got, want := printed["attempt"], recorded[trace.TypeMutantExec]; got != want {
		t.Errorf("the console shows %d attempt lines and the recording holds %d mutant-exec events", got, want)
	}

	code, quiet, stderr := execute(t, "run", "--trace", "--no-tui", "--no-color")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants run --trace` exited %d\nstderr:\n%s", code, stderr)
	}
	for _, line := range strings.Split(quiet, "\n") {
		if strings.HasPrefix(line, "  exec ") || strings.HasPrefix(line, "  attempt ") {
			t.Errorf("a run without -vv printed a recorded line: %q", line)
		}
	}
}
