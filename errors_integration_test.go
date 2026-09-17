// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package gomutants_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestVerificationFailureIsTyped(t *testing.T) {
	t.Parallel()

	root := testkit.Copy(t, "failing-baseline")
	workspace, err := gomutants.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{})
	if err == nil {
		_ = session.Close()
		t.Fatal("Prepare over a failing suite succeeded, want a verification failure")
	}
	var verification *gomutants.VerificationError
	if !errors.As(err, &verification) {
		t.Fatalf("Prepare = %v, want a *VerificationError", err)
	}
	if verification.TimedOut {
		t.Errorf("the verification is reported as timed out: %+v", verification)
	}
	if verification.ExitCode == 0 {
		t.Errorf("ExitCode = 0, want the status the suite exited with")
	}
	if len(verification.Output) == 0 {
		t.Error("Output is empty, so the failure cannot be shown to the user who caused it")
	}
	if verification.Truncated {
		t.Errorf("Truncated = true although the fixture cannot fill the default budget: %+v", verification)
	}
	if verification.TotalBytes != int64(len(verification.Output)) {
		t.Errorf("TotalBytes = %d, want len(Output) = %d when nothing was dropped",
			verification.TotalBytes, len(verification.Output))
	}
	if verification.Duration <= 0 {
		t.Errorf("Duration = %s, want the time the command took", verification.Duration)
	}
	if len(verification.Command.Argv) == 0 {
		t.Error("Command.Argv is empty, so the failure does not say what was run")
	}
	if prefix := "gomutants: prepare instrumented verification exited with status "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("message = %q, want the prefix %q", err.Error(), prefix)
	}
	if !strings.Contains(string(verification.Output), "this fixture fails on purpose") {
		t.Errorf("Output does not carry the suite's own words:\n%s", verification.Output)
	}
}

func TestBuildFailureIsTyped(t *testing.T) {
	t.Parallel()

	root := testkit.Copy(t, "simple")
	testkit.WriteSource(t, root, "broken/broken_test.go", "package broken\n\nfunc {\n")

	workspace, err := gomutants.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		SkipVerify:        true,
		DiscoveryPackages: []string{"."},
	})
	if err == nil {
		_ = session.Close()
		t.Fatal("Prepare over a package that does not compile succeeded, want a build failure")
	}
	var build *gomutants.BuildError
	if !errors.As(err, &build) {
		t.Fatalf("Prepare = %v, want a *BuildError", err)
	}
	if build.Phase != gomutants.PreparePhaseBinaryBuild {
		t.Errorf("Phase = %q, want %q", build.Phase, gomutants.PreparePhaseBinaryBuild)
	}
	if want := "fixture.example/simple/broken"; build.Package != want {
		t.Errorf("Package = %q, want %q", build.Package, want)
	}
	if build.ExitCode == 0 || build.TimedOut {
		t.Errorf("ExitCode = %d and TimedOut = %v, want the status the compile exited with",
			build.ExitCode, build.TimedOut)
	}
	if build.Code != "GOM7505" {
		t.Errorf("Code = %q, want GOM7505, the code for a test binary that would not compile", build.Code)
	}
	if got := gomutants.DiagnosticCode(err); got != build.Code {
		t.Errorf("DiagnosticCode = %q, want the BuildError's own %q", got, build.Code)
	}
	if len(build.Argv) == 0 {
		t.Error("Argv is empty, so the failure cannot be reproduced")
	}
	if build.Output == "" {
		t.Error("Output is empty, so the compiler's own words are lost")
	}
}

func TestPackageNotPreparedIsTyped(t *testing.T) {
	prepared := probeable(t)
	width := mutantkit.APIByRule(t, prepared.catalog, widthRule)

	_, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  width.ID,
		Package: probeableModule + "/absent",
	})
	var missing *gomutants.PackageNotPreparedError
	if !errors.As(err, &missing) {
		t.Fatalf("Exec = %v, want a *PackageNotPreparedError", err)
	}
	if missing.Call != "exec" || missing.Package != probeableModule+"/absent" {
		t.Errorf("PackageNotPreparedError = %+v, want the exec call and the requested package", missing)
	}
	want := `gomutants: session exec package "` + probeableModule + `/absent" has no prepared test binary`
	if got := err.Error(); got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestReservedFlagsAndVariablesAreTyped(t *testing.T) {
	prepared := probeable(t)
	width := mutantkit.APIByRule(t, prepared.catalog, widthRule)

	cases := []struct {
		name     string
		request  gomutants.ExecRequest
		flag     string
		variable string
		message  string
	}{
		{
			name:     "an activation variable",
			request:  gomutants.ExecRequest{Env: []string{"GO_MUTANTS_ACTIVE=x"}},
			variable: "GO_MUTANTS_ACTIVE",
			message:  "gomutants: session exec environment: GO_MUTANTS_ACTIVE is reserved by go-mutants",
		},
		{
			name:    "the fuzz cache directory",
			request: gomutants.ExecRequest{Args: []string{"-test.fuzzcachedir=x"}},
			flag:    "-test.fuzzcachedir",
			message: "gomutants: session exec: -test.fuzzcachedir is reserved by the session",
		},
		{
			name:    "the in-process timeout",
			request: gomutants.ExecRequest{Args: []string{"-test.timeout=1s"}, Timeout: time.Second},
			flag:    "-test.timeout",
			message: "gomutants: session exec: -test.timeout is reserved by the session's process supervisor",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			request := c.request
			request.Mutant = width.ID
			request.Package = probeableModule
			result, err := prepared.session.Exec(t.Context(), request)
			var reserved *gomutants.ReservedError
			if !errors.As(err, &reserved) {
				t.Fatalf("Exec accepted %+v and answered (%+v, %v)", request, result, err)
			}
			if reserved.Flag != c.flag || reserved.Variable != c.variable {
				t.Errorf("ReservedError = %+v, want flag %q and variable %q", reserved, c.flag, c.variable)
			}
			if got := err.Error(); got != c.message {
				t.Errorf("message = %q, want %q", got, c.message)
			}
		})
	}
}
