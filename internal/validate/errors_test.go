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

	if got := err.Command(); got != nil {
		t.Errorf("Command() = %+v on an error built by hand, want nil", got)
	}
	err.Invocation = &runner.Invocation{Argv: []string{"go", "build", "./..."}}
	if got := err.Command(); got == nil || got.Argv[1] != "build" {
		t.Errorf("Command() = %+v, want the build it was about", got)
	}
}

func TestEveryCodeIsSpelledTheWayItIsPrinted(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]string{
		{validate.CodeOptions.String(), "GOM7401"},
		{validate.CodeSourceUnreadable.String(), "GOM7402"},
		{validate.CodeBuildFailed.String(), "GOM7410"},
		{validate.CodeBuildTimedOut.String(), "GOM7411"},
		{validate.CodeInterrupted.String(), "GOM7412"},
		{validate.CodeNotMutantInduced.String(), "GOM7420"},
		{validate.CodeStillFailing.String(), "GOM7421"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("a code renders as %q, want %q", pair[0], pair[1])
		}
	}
}
