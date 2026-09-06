// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/engine"
	// Aliased because this package's own tests already spell `execute` the
	// helper that drives the whole command tree.
	executepkg "github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/validate"
)

// TestRenderErrorPrintsExecuteOutputUnderneathAGOM7505 is the failure this
// whole change is about.
//
// A test binary that will not compile is reported as a go-mutants bug in the
// instrumented rewrite, and the only evidence for it is what the compiler
// said — which the renderer used to drop on the floor, because it asked
// internal/engine for the output and this error is internal/execute's. One line
// saying "the test binary could not be built" is a bug report nobody can act
// on.
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

// TestRenderErrorPrintsValidateOutputExactlyOnce pins the half of the change
// that is a removal.
//
// internal/validate used to append the compiler's output to its own Error()
// text, because nothing else would print it. That made every line of a
// multi-line blob arrive at the renderer as a continuation line and come back
// out with a GOM7420 in front of it, and it would now be printed twice over —
// once folded into the message and once by the renderer. The output belongs
// under the message, once, and it is the renderer's job to put it there.
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

// TestRenderErrorPrintsTheFailingCommandAndDirectory is why the invocation is
// carried up at all: a reader who cannot reproduce a failure cannot fix it.
//
// The quoting rule is the interesting part, and all three of its cases are
// here. An element is printed verbatim unless it contains whitespace or a
// double quote, or is empty: a line that escaped every path would be unreadable
// for the ninety-nine commands in a hundred that need no escaping, one that
// escaped nothing would silently turn `-run` `Test A` into three shell words,
// and an empty argument printed verbatim would vanish along with the argument
// count.
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

// TestRenderErrorQuotesAWindowsPathWithoutDoublingItsSeparators is the reason
// the quoting is hand-rolled rather than strconv.Quote's.
//
// The one platform whose ordinary paths contain a space is the one whose
// separator is a backslash, so Go's own quoting turns the single command a user
// most needs to paste into `"C:\\Program Files\\Go\\bin\\go.exe"` — a path that
// is correct as a Go string literal and wrong in every shell there is. Only the
// quote itself is escaped here; everything else goes through as written.
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

// TestRenderErrorPrintsNoCommandLinesForACommandlessInvocation covers the
// invocation that names nothing: a spec the runner refused for having no
// argument vector at all.
//
// A `dir:` on its own would be the worst of both answers — it says a command
// ran somewhere without saying what it was — so the two lines stand or fall
// together.
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

// TestRenderErrorWalksPastAnErrorThatKeptNothing pins half of the precedence
// rule: an error that could carry an output or a command and does not is walked
// past, not stopped at.
//
// It is the shape a start failure arrives in. internal/engine wraps the
// runner's error and has no tail of its own, because there was no child to
// produce one, while the runner's error knows exactly which command it was —
// and a walk that stopped at the first error merely *capable* of answering
// would print nothing for it.
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

// TestRenderErrorPrefersTheOutermostErrorThatDecided pins the other half.
//
// When both ends of the chain have an answer the outer one wins, because the
// outer error is the one that decided what a terminal should see: internal/engine
// and internal/execute trim a fifty-line tail for a console, while the runner
// retains up to a megabyte for a report. Printing the inner capture as well —
// or instead — would bury the failure in the scrollback it was trimmed out of.
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
	// Three times over: the argv, the directory, and the output tail — each of
	// them the outer error's answer, and none of them the inner one's.
	if n := strings.Count(got, "outer"); n != 3 {
		t.Errorf("the outer answer appears %d times, want three:\n%s", n, got)
	}
	if strings.Contains(got, "inner") {
		t.Errorf("the inner error's answer was printed as well as the outer one:\n%s", got)
	}
}

// TestRenderErrorPrintsAJoinedErrorsOutputAndCommand is the shape a run that
// failed and then failed to clean up arrives in.
//
// internal/engine joins the run's error with whatever the scratch removal said,
// and a joined error unwraps to a *slice*: a walk that only followed a single
// cause reached the join, found no `Unwrap() error` on it, and stopped — losing
// the compiler's diagnostics and the command for every failure that happened to
// be joined to a second one.
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

// TestRenderErrorConsultsEveryBranchOfAJoinedError is the same shape with the
// branches the other way round, and it is the one that catches a walk which
// merely *starts* branch-aware.
//
// The first branch here can answer and has nothing to say — the cleanup failure
// is coded, so it is a carrier, and it never ran a command — while the second
// holds the compiler's diagnostics. Following one cause at a time reaches the
// join, finds no single cause under it, and gives up with the evidence one
// branch away.
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

// TestRenderErrorPrintsRunnerStartFailureCommand covers the error that names a
// command and nothing else.
//
// A process that could not be started produced no output, so the command is the
// whole of the diagnosis: "could not start /tmp/.../pkg.test" is a sentence
// about a path the user has never seen, and the argv and the working directory
// are what turn it into something reproducible.
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

// TestRenderErrorPrintsGocmdProbeOutput is the first failure a new user can
// hit, and the one where the child's own words matter most: whatever the thing
// on PATH answered `go version` with is the evidence that it is not a Go
// toolchain.
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

// TestRenderErrorStillPrintsNothingForASilentPolicyFailure is the regression
// guard on everything the new lines must not touch.
//
// A failed policy gate prints nothing at all, because the run's own summary
// already named the survivors and the score. An error carrying neither a
// command nor an output renders exactly as it did before this change: the coded
// line, the hint, and nothing else.
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
