// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/engine"
	executepkg "github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/validate"
)

func TestRenderErrorPrintsExecuteOutputUnderneathAGOM7505(t *testing.T) {
	t.Parallel()

	var b bytes.Buffer
	RenderError(&b, &executepkg.Error{
		Code:    executepkg.CodeTestBuildFailed,
		Message: "the test binary for example.com/m/pkg could not be built: exited with status 2",
		Output:  "./a_test.go:9:2: undefined: Missing\n./a_test.go:12:2: undefined: AlsoMissing",
	})
	want := "error GOM7505: the test binary for example.com/m/pkg could not be built: exited with status 2\n" +
		"    ./a_test.go:9:2: undefined: Missing\n" +
		"    ./a_test.go:12:2: undefined: AlsoMissing\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

func TestRenderErrorPrintsValidateOutputExactlyOnce(t *testing.T) {
	t.Parallel()

	var b bytes.Buffer
	RenderError(&b, &validate.Error{
		Code:    validate.CodeNotMutantInduced,
		Message: "the snapshot does not build with every mutant removed",
		Output:  "./a.go:1:1: undefined: x\n./a.go:2:1: undefined: y",
	})
	want := "error GOM7420: the snapshot does not build with every mutant removed\n" +
		"    ./a.go:1:1: undefined: x\n" +
		"    ./a.go:2:1: undefined: y\n"
	got := b.String()
	if got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
	if n := strings.Count(got, "undefined: x"); n != 1 {
		t.Errorf("the compiler's first diagnostic was printed %d times, want once:\n%s", n, got)
	}
}

func TestRenderErrorPrintsTheFailingCommandAndDirectory(t *testing.T) {
	t.Parallel()

	var b bytes.Buffer
	RenderError(&b, &engine.Error{
		Code:    engine.CodeBaselineTestFailed,
		Message: "baseline run 1 of 3 failed: exited with status 1",
		Output:  "--- FAIL: TestX\nFAIL",
		Invocation: &runner.Invocation{
			Argv: []string{"/usr/bin/go", "test", "-run", "Test A", "", "./..."},
			Dir:  "/tmp/go-mutants-snapshot",
		},
	})
	want := "error GOM4011: baseline run 1 of 3 failed: exited with status 1\n" +
		"    command: /usr/bin/go test -run \"Test A\" \"\" ./...\n" +
		"    dir: /tmp/go-mutants-snapshot\n" +
		"    --- FAIL: TestX\n" +
		"    FAIL\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

func TestRenderErrorQuotesAWindowsPathWithoutDoublingItsSeparators(t *testing.T) {
	t.Parallel()

	var b bytes.Buffer
	RenderError(&b, &runner.Error{
		Code:    runner.CodeProcessStartFailed,
		Message: "could not start it",
		Invocation: &runner.Invocation{
			Argv: []string{`C:\Program Files\Go\bin\go.exe`, "test", `-run=Says"Hello"`},
			Dir:  `C:\snapshot`,
		},
	})
	want := "error GOM7202: could not start it\n" +
		"    command: \"C:\\Program Files\\Go\\bin\\go.exe\" test \"-run=Says\\\"Hello\\\"\"\n" +
		"    dir: C:\\snapshot\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

func TestRenderErrorPrintsNoCommandLinesForACommandlessInvocation(t *testing.T) {
	t.Parallel()

	var b bytes.Buffer
	RenderError(&b, &runner.Error{
		Code:       runner.CodeSpecInvalid,
		Message:    "the spec has no argument vector",
		Invocation: &runner.Invocation{Dir: "/tmp/go-mutants-snapshot"},
	})
	want := "error GOM7203: the spec has no argument vector\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

func TestRenderErrorWalksPastAnErrorThatKeptNothing(t *testing.T) {
	t.Parallel()

	inner := &runner.Error{
		Code:       runner.CodeProcessWaitFailed,
		Message:    "the child could not be waited for",
		Output:     "inner",
		Invocation: &runner.Invocation{Argv: []string{"/snapshot/pkg.test"}, Dir: "/snapshot/pkg"},
	}
	var b bytes.Buffer
	RenderError(&b, &engine.Error{
		Code:    engine.CodeBaselineTestFailed,
		Message: "baseline run 1 of 3 failed: the command could not be run",
		Err:     inner,
	})
	want := "error GOM4011: baseline run 1 of 3 failed: the command could not be run: " +
		"GOM7204: the child could not be waited for\n" +
		"    command: /snapshot/pkg.test\n" +
		"    dir: /snapshot/pkg\n" +
		"    inner\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

func TestRenderErrorPrefersTheOutermostErrorThatDecided(t *testing.T) {
	t.Parallel()

	inner := &runner.Error{
		Code:       runner.CodeProcessWaitFailed,
		Message:    "the child could not be waited for",
		Output:     "inner",
		Invocation: &runner.Invocation{Argv: []string{"/inner/pkg.test"}, Dir: "/inner"},
	}
	var b bytes.Buffer
	RenderError(&b, &engine.Error{
		Code:       engine.CodeBaselineTestFailed,
		Message:    "baseline run 1 of 3 failed: the command could not be run",
		Output:     "outer",
		Err:        inner,
		Invocation: &runner.Invocation{Argv: []string{"/outer/pkg.test"}, Dir: "/outer"},
	})
	got := b.String()
	want := "error GOM4011: baseline run 1 of 3 failed: the command could not be run: " +
		"GOM7204: the child could not be waited for\n" +
		"    command: /outer/pkg.test\n" +
		"    dir: /outer\n" +
		"    outer\n"
	if got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
	if n := strings.Count(got, "outer"); n != 3 {
		t.Errorf("the outer answer appears %d times, want three:\n%s", n, got)
	}
	if strings.Contains(got, "inner") {
		t.Errorf("the inner error's answer was printed as well as the outer one:\n%s", got)
	}
}

func TestRenderErrorPrintsAJoinedErrorsOutputAndCommand(t *testing.T) {
	t.Parallel()

	failure := &executepkg.Error{
		Code:    executepkg.CodeTestBuildFailed,
		Message: "the test binary for example.com/m/pkg could not be built: exited with status 2",
		Output:  "./a_test.go:9:2: undefined: Missing",
		Invocation: &runner.Invocation{
			Argv: []string{"/usr/bin/go", "test", "-c", "-o", "/bin/pkg.test", "example.com/m/pkg"},
			Dir:  "/snapshot",
		},
	}
	var b bytes.Buffer
	RenderError(&b, errors.Join(failure, errors.New("the per-run temporary directory could not be removed")))
	want := "error GOM7505: the test binary for example.com/m/pkg could not be built: exited with status 2\n" +
		"error GOM7505: the per-run temporary directory could not be removed\n" +
		"    command: /usr/bin/go test -c -o /bin/pkg.test example.com/m/pkg\n" +
		"    dir: /snapshot\n" +
		"    ./a_test.go:9:2: undefined: Missing\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

func TestRenderErrorConsultsEveryBranchOfAJoinedError(t *testing.T) {
	t.Parallel()

	silent := &engine.Error{
		Code:    engine.CodeScratchNotRemoved,
		Message: "the per-run temporary directory could not be removed",
	}
	failure := &executepkg.Error{
		Code:    executepkg.CodeTestBuildFailed,
		Message: "the test binary for example.com/m/pkg could not be built: exited with status 2",
		Output:  "./a_test.go:9:2: undefined: Missing",
		Invocation: &runner.Invocation{
			Argv: []string{"/usr/bin/go", "test", "-c", "example.com/m/pkg"},
			Dir:  "/snapshot",
		},
	}
	var b bytes.Buffer
	RenderError(&b, errors.Join(silent, failure))
	want := "error GOM4041: the per-run temporary directory could not be removed\n" +
		"error GOM7505: the test binary for example.com/m/pkg could not be built: exited with status 2\n" +
		"    command: /usr/bin/go test -c example.com/m/pkg\n" +
		"    dir: /snapshot\n" +
		"    ./a_test.go:9:2: undefined: Missing\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

func TestRenderErrorPrintsRunnerStartFailureCommand(t *testing.T) {
	t.Parallel()

	var b bytes.Buffer
	RenderError(&b, &runner.Error{
		Code:    runner.CodeProcessStartFailed,
		Message: "could not start /snapshot/pkg.test",
		Invocation: &runner.Invocation{
			Argv: []string{"/snapshot/pkg.test", "-test.timeout=1m0s"},
			Dir:  "/snapshot/pkg",
		},
	})
	want := "error GOM7202: could not start /snapshot/pkg.test\n" +
		"    command: /snapshot/pkg.test -test.timeout=1m0s\n" +
		"    dir: /snapshot/pkg\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

func TestRenderErrorPrintsGocmdProbeOutput(t *testing.T) {
	t.Parallel()

	var b bytes.Buffer
	RenderError(&b, &gocmd.Error{
		Code:    gocmd.CodeVersionProbeFailed,
		Message: "`/opt/fake/go version` exited with status 1",
		Output:  "this is not the go toolchain",
		Invocation: &runner.Invocation{
			Argv: []string{"/opt/fake/go", "version"},
		},
	})
	want := "error GOM7211: `/opt/fake/go version` exited with status 1\n" +
		"    command: /opt/fake/go version\n" +
		"    this is not the go toolchain\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}

func TestRenderErrorStillPrintsNothingForASilentPolicyFailure(t *testing.T) {
	t.Parallel()

	verdict := mutation.Decide(
		mutation.Tally{Killed: 1, UnexpectedSurvivors: 1},
		mutation.Policy{Strict: true, RequireMutants: true},
		mutation.Signals{},
	)
	var silent bytes.Buffer
	RenderError(&silent, policyFailure(verdict))
	if silent.Len() != 0 {
		t.Errorf("a policy failure printed %q, want nothing", silent.String())
	}

	var b bytes.Buffer
	RenderError(&b, usagef("the flag --nope is not a flag"))
	want := "error GOM1001: the flag --nope is not a flag\n" +
		"hint: run `go-mutants --help` to see the commands and flags\n"
	if got := b.String(); got != want {
		t.Errorf("RenderError:\n got %q\nwant %q", got, want)
	}
}
