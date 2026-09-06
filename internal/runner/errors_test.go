// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// TestStartFailureCarriesTheInvocation is why [runner.Invocation] exists.
//
// [runner.Result] deliberately says nothing about what was run, so a failure
// that travelled up three layers arrived as "could not start
// /tmp/x/go-build123/b001/pkg.test" with no working directory, no argument
// vector, and nothing to reproduce it with. The command is attached where it is
// still known — here — rather than reconstructed by a renderer that would have
// to guess.
func TestStartFailureCarriesTheInvocation(t *testing.T) {
	t.Parallel()

	recorder, sink := newRecording(t)
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-executable")
	argv := []string{missing, "-test.run", "TestNothing"}

	spec := runner.Spec{
		Argv:  argv,
		Dir:   dir,
		Trace: recorder,
		Kind:  trace.ExecKindGoTestC,
	}
	result := runner.Run(t.Context(), spec)

	var failure *runner.Error
	if !errors.As(result.Err, &failure) {
		t.Fatalf("Err = %v, want a *runner.Error", result.Err)
	}
	invocation := failure.Command()
	if invocation == nil {
		t.Fatal("Command() = nil, want the command that could not be started")
	}
	if !slices.Equal(invocation.Argv, argv) {
		t.Errorf("Argv = %q, want %q", invocation.Argv, argv)
	}
	if invocation.Dir != dir {
		t.Errorf("Dir = %q, want %q", invocation.Dir, dir)
	}
	if invocation.Kind != trace.ExecKindGoTestC {
		t.Errorf("Kind = %q, want %q", invocation.Kind, trace.ExecKindGoTestC)
	}
	event := onlyExec(t, sink)
	if invocation.TraceSeq != event.Seq || invocation.TraceSeq != result.TraceSeq {
		t.Errorf("TraceSeq = %d, want the recorded sequence %d (Result.TraceSeq = %d)",
			invocation.TraceSeq, event.Seq, result.TraceSeq)
	}

	// The message is the user-facing contract and does not change because the
	// error grew a field.
	if want := "GOM7202: could not start " + missing + ": "; !strings.HasPrefix(failure.Error(), want) {
		t.Errorf("Error() = %q, want it to begin %q", failure.Error(), want)
	}

	// A caller reusing its argument buffer for the next command must not be
	// able to rewrite the invocation an error already carries.
	argv[1] = "-test.run=Rewritten"
	if invocation.Argv[1] != "-test.run" {
		t.Errorf("Argv[1] = %q after the caller reused its buffer, want %q", invocation.Argv[1], "-test.run")
	}
}

// TestInvocationClonesArgv pins the copy in both directions, and the empty
// answer a failure that never named a command gives.
func TestInvocationClonesArgv(t *testing.T) {
	t.Parallel()

	argv := []string{"go", "test", "-c"}
	spec := runner.Spec{Argv: argv, Dir: "snapshot", Kind: trace.ExecKindGoTestC}
	invocation := runner.InvocationOf(spec, runner.Result{TraceSeq: 7})

	if invocation.Dir != "snapshot" || invocation.Kind != trace.ExecKindGoTestC || invocation.TraceSeq != 7 {
		t.Errorf("InvocationOf = %+v, want the spec's dir and kind and the result's sequence", invocation)
	}

	argv[1] = "build"
	if invocation.Argv[1] != "test" {
		t.Errorf("Argv[1] = %q, want %q: InvocationOf aliased the caller's slice", invocation.Argv[1], "test")
	}
	invocation.Argv[0] = "other"
	if argv[0] != "go" {
		t.Errorf("the caller's argv[0] = %q, want %q: the invocation shares its backing array", argv[0], "go")
	}

	// An error built by hand — every runner.Error in a test, and every one a
	// future caller constructs — has no command, and says so rather than
	// panicking.
	if got := (&runner.Error{Code: runner.CodeSpecInvalid, Message: "no argument vector"}).Command(); got != nil {
		t.Errorf("Command() = %+v on an error that named no command, want nil", got)
	}
}
