// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/validate"
)

// TestErrorStringNoLongerEmbedsTheCompilerOutput pins a deliberate removal.
//
// This package used to append the compiler's output to its own error text,
// because at the time nothing downstream would have printed it. The cost was
// paid on every line: internal/cli lifts a code off the first line and prefixes
// every following one with the code above it, so a compiler blob arrived as a
// run of continuation lines and came back out as "error GOM7420: ./a.go:1:1:
// undefined: x" — go-mutants claiming the compiler's words as a diagnostic of
// its own. The renderer now asks for the output through [validate.Error.Output]
// and prints it once, indented and uncoded, so folding it into the message
// would print it twice.
func TestErrorStringNoLongerEmbedsTheCompilerOutput(t *testing.T) {
	t.Parallel()

	const output = "./a.go:1:1: undefined: x\n./a.go:2:1: undefined: y"
	err := &validate.Error{
		Code:    validate.CodeNotMutantInduced,
		Message: "the snapshot does not build with every mutant removed",
		Output:  output,
	}

	text := err.Error()
	if want := "GOM7420: the snapshot does not build with every mutant removed"; text != want {
		t.Errorf("Error() = %q, want %q", text, want)
	}
	if strings.Contains(text, "undefined: x") {
		t.Errorf("the compiler's output is still folded into the message:\n%s", text)
	}
	if strings.Contains(text, "\n") {
		t.Errorf("Error() spans several lines, which is what makes it ungreppable:\n%s", text)
	}

	// The output has not been lost, only moved: it is still reachable, and now
	// through the one method every go-mutants error answers.
	if got := err.RetainedOutput(); got != output {
		t.Errorf("RetainedOutput() = %q, want %q", got, output)
	}
	var carrier interface{ RetainedOutput() string }
	if !errors.As(error(err), &carrier) {
		t.Fatal("a validation failure is not an output carrier")
	}
	if got := carrier.RetainedOutput(); got != output {
		t.Errorf("RetainedOutput() through the interface = %q, want %q", got, output)
	}

	// And an error that named no command says so rather than naming an invented
	// one.
	if got := err.Command(); got != nil {
		t.Errorf("Command() = %+v on an error built by hand, want nil", got)
	}
	err.Invocation = &runner.Invocation{Argv: []string{"go", "build", "./..."}}
	if got := err.Command(); got == nil || got.Argv[1] != "build" {
		t.Errorf("Command() = %+v, want the build it was about", got)
	}
}
