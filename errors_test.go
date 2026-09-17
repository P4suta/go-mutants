// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/validate"
)

func TestClosedWorkspaceErrorsAreSentinels(t *testing.T) {
	t.Parallel()

	closed := &Workspace{closed: true}
	_, execErr := closed.Exec(t.Context(), Command{Argv: []string{"go", "version"}})
	if !errors.Is(execErr, ErrWorkspaceClosed) {
		t.Errorf("Exec on a closed workspace = %v, want ErrWorkspaceClosed", execErr)
	}
	if got, want := errorText(execErr), "gomutants: exec: workspace is closed"; got != want {
		t.Errorf("Exec message = %q, want %q", got, want)
	}

	_, prepareErr := closed.Prepare(t.Context(), PrepareOptions{})
	if !errors.Is(prepareErr, ErrWorkspaceClosed) {
		t.Errorf("Prepare on a closed workspace = %v, want ErrWorkspaceClosed", prepareErr)
	}
	if got, want := errorText(prepareErr), "gomutants: prepare: workspace is closed"; got != want {
		t.Errorf("Prepare message = %q, want %q", got, want)
	}

	preparedThenClosed := &Workspace{closed: true, prepareStarted: true, prepareFailed: true}
	_, closedErr := preparedThenClosed.Exec(t.Context(), Command{Argv: []string{"go", "version"}})
	if !errors.Is(closedErr, ErrWorkspaceClosed) {
		t.Errorf("Exec on a closed workspace that was prepared = %v, want ErrWorkspaceClosed", closedErr)
	}
	if errors.Is(closedErr, ErrPrepareFailed) {
		t.Errorf("Exec on a closed workspace that was prepared = %v, which reads as a failed"+
			" preparation: both are true of this workspace and closed has to win", closedErr)
	}
	if got, want := errorText(closedErr), "gomutants: exec: workspace is closed"; got != want {
		t.Errorf("Exec message on a closed prepared workspace = %q, want %q", got, want)
	}
}

func TestSecondPrepareIsErrWorkspacePrepared(t *testing.T) {
	t.Parallel()

	prepared := &Workspace{prepareStarted: true}
	_, err := prepared.Prepare(t.Context(), PrepareOptions{})
	if !errors.Is(err, ErrWorkspacePrepared) {
		t.Errorf("second Prepare = %v, want ErrWorkspacePrepared", err)
	}
	if errors.Is(err, ErrWorkspaceClosed) {
		t.Errorf("second Prepare = %v, which also reads as a closed workspace", err)
	}
	if got, want := errorText(err), "gomutants: prepare: workspace has already been prepared"; got != want {
		t.Errorf("second Prepare message = %q, want %q", got, want)
	}
}

func TestExecAfterAFailedPrepareIsErrPrepareFailed(t *testing.T) {
	t.Parallel()

	failed := &Workspace{prepareStarted: true, prepareFailed: true, scratch: t.TempDir()}
	_, err := failed.Exec(t.Context(), Command{Argv: []string{"go", "version"}})
	if !errors.Is(err, ErrPrepareFailed) {
		t.Errorf("Exec after a failed Prepare = %v, want ErrPrepareFailed", err)
	}
	if errors.Is(err, ErrWorkspaceClosed) || errors.Is(err, ErrWorkspacePrepared) {
		t.Errorf("Exec after a failed Prepare = %v, which also reads as another lifecycle refusal", err)
	}
	want := "gomutants: exec: workspace preparation failed; its tree may hold instrumented sources"
	if got := errorText(err); got != want {
		t.Errorf("Exec after a failed Prepare message = %q, want %q", got, want)
	}
}

func TestExecAfterASuccessfulPrepareIsNotRefused(t *testing.T) {
	t.Parallel()

	prepared := &Workspace{prepareStarted: true, session: &Session{}, scratch: t.TempDir()}
	_, err := prepared.Exec(t.Context(), Command{})
	if errors.Is(err, ErrPrepareFailed) || errors.Is(err, ErrWorkspacePrepared) {
		t.Errorf("Exec after a successful Prepare = %v, want the command itself to be judged", err)
	}
	if got, want := errorText(err), "gomutants: exec: command has no executable"; got != want {
		t.Errorf("Exec after a successful Prepare = %q, want %q", got, want)
	}
}

func TestClosedSessionErrorsAreSentinels(t *testing.T) {
	t.Parallel()

	closed := &Session{closed: true}
	_, execErr := closed.Exec(t.Context(), ExecRequest{Mutant: "deadbeef"})
	_, probeErr := closed.Probe(t.Context(), ProbeRequest{})
	_, controlErr := closed.Control(t.Context(), ControlRequest{})
	_, changesErr := closed.Changes()

	cases := []struct {
		call    string
		err     error
		message string
	}{
		{"Exec", execErr, "gomutants: session exec: session is closed"},
		{"Probe", probeErr, "gomutants: session probe: session is closed"},
		{"Control", controlErr, "gomutants: session control: session is closed"},
		{"Changes", changesErr, "gomutants: changes: session is closed"},
	}
	for _, c := range cases {
		if !errors.Is(c.err, ErrSessionClosed) {
			t.Errorf("%s on a closed session = %v, want ErrSessionClosed", c.call, c.err)
		}
		if got := errorText(c.err); got != c.message {
			t.Errorf("%s message = %q, want %q", c.call, got, c.message)
		}
	}
}

func TestControlRefusalsNameTheControlCall(t *testing.T) {
	t.Parallel()

	session := &Session{scratch: t.TempDir()}

	_, flagErr := session.Control(t.Context(), ControlRequest{Args: []string{"-test.timeout=1s"}})
	var reserved *ReservedError
	if !errors.As(flagErr, &reserved) {
		t.Fatalf("Control with a reserved flag = %v, want a *ReservedError", flagErr)
	}
	if reserved.Call != "control" {
		t.Errorf("Call = %q, want %q", reserved.Call, "control")
	}
	if got, want := errorText(flagErr),
		"gomutants: session control: -test.timeout is reserved by the session's process supervisor"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}

	_, packageErr := session.Control(t.Context(), ControlRequest{Package: "example.com/nowhere"})
	var missing *PackageNotPreparedError
	if !errors.As(packageErr, &missing) {
		t.Fatalf("Control naming an unprepared package = %v, want a *PackageNotPreparedError", packageErr)
	}
	if missing.Call != "control" || missing.Package != "example.com/nowhere" {
		t.Errorf("err = %+v, want the control call and the request's own spelling", missing)
	}
	if got, want := errorText(packageErr),
		`gomutants: session control package "example.com/nowhere" has no prepared test binary`; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestMutantSelectionErrors(t *testing.T) {
	t.Parallel()

	t.Run("invalid prefix", func(t *testing.T) {
		t.Parallel()
		session := &Session{catalog: &mutation.Catalog{}}
		_, err := session.Exec(t.Context(), ExecRequest{Mutant: "zz"})
		var selection *MutantSelectionError
		if !errors.As(err, &selection) {
			t.Fatalf("Exec = %v, want a *MutantSelectionError", err)
		}
		if !errors.Is(err, ErrInvalidMutantID) {
			t.Errorf("Exec = %v, want ErrInvalidMutantID", err)
		}
		if selection.Prefix != "zz" {
			t.Errorf("Prefix = %q, want %q", selection.Prefix, "zz")
		}
		if !errors.Is(err, mutation.ErrInvalidPrefix) {
			t.Errorf("the internal cause is no longer reachable: %v", err)
		}
		if prefix := `gomutants: session exec mutant "zz": `; !strings.HasPrefix(err.Error(), prefix) {
			t.Errorf("message = %q, want the prefix %q", err.Error(), prefix)
		}
	})

	t.Run("no mutant matches", func(t *testing.T) {
		t.Parallel()
		session := &Session{catalog: &mutation.Catalog{}}
		_, err := session.Exec(t.Context(), ExecRequest{Mutant: "0000beef"})
		if !errors.Is(err, ErrMutantNotFound) {
			t.Errorf("Exec = %v, want ErrMutantNotFound", err)
		}
		var selection *MutantSelectionError
		if errors.As(err, &selection) && len(selection.Matches) != 0 {
			t.Errorf("Matches = %v, want none", selection.Matches)
		}
		if prefix := `gomutants: session exec mutant "0000beef": `; !strings.HasPrefix(err.Error(), prefix) {
			t.Errorf("message = %q, want the prefix %q", err.Error(), prefix)
		}
	})

	t.Run("ambiguous prefix", func(t *testing.T) {
		t.Parallel()
		session := &Session{publicCatalog: Catalog{Mutants: []Mutant{
			{ID: "beef2222" + strings.Repeat("0", 56), DisplayID: "beef2222"},
			{ID: "beef1111" + strings.Repeat("0", 56), DisplayID: "beef1111"},
			{ID: "0bad0000" + strings.Repeat("0", 56), DisplayID: "0bad0000"},
		}}}
		cause := fmt.Errorf("%w: %q matches 2 mutants: beef2222, beef1111", mutation.ErrAmbiguousPrefix, "beef")
		err := session.selectionError("beef", cause)
		if !errors.Is(err, ErrAmbiguousMutant) {
			t.Errorf("selectionError = %v, want ErrAmbiguousMutant", err)
		}
		if want := []string{"beef1111", "beef2222"}; !slices.Equal(err.Matches, want) {
			t.Errorf("Matches = %v, want %v", err.Matches, want)
		}
		if got, want := err.Error(), `gomutants: session exec mutant "beef": `+cause.Error(); got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	})

	t.Run("rejected during validation", func(t *testing.T) {
		t.Parallel()
		catalog := catalogOfOne(t)
		mutant := catalog.Mutants()[0]
		rejection := Rejection{
			ID:         mutant.ID,
			DisplayID:  mutant.DisplayID,
			Path:       mutant.Path,
			Rule:       mutant.Rule.Name,
			Diagnostic: "pkg/a.go:3:9: invalid operation",
		}
		session := &Session{
			catalog:    catalog,
			accepted:   map[string]bool{},
			rejections: map[string]Rejection{mutant.ID: rejection},
		}
		_, err := session.Exec(t.Context(), ExecRequest{Mutant: mutant.ID})
		if !errors.Is(err, ErrMutantRejected) {
			t.Fatalf("Exec of a rejected mutant = %v, want ErrMutantRejected", err)
		}
		var selection *MutantSelectionError
		if !errors.As(err, &selection) {
			t.Fatalf("Exec = %v, want a *MutantSelectionError", err)
		}
		if selection.Rejection == nil || selection.Rejection.Diagnostic != rejection.Diagnostic {
			t.Errorf("Rejection = %+v, want the catalogued one", selection.Rejection)
		}
		want := "gomutants: session exec mutant " + mutant.DisplayID +
			" was rejected during validation: pkg/a.go:3:9: invalid operation"
		if got := err.Error(); got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	})
}

func TestDriftErrorRendersEveryKind(t *testing.T) {
	t.Parallel()

	changes := []Change{
		{Kind: ChangeAdded, Path: "command-artifact.txt", AfterSHA256: "aa"},
		{Kind: ChangeRemoved, Path: "gone.go", BeforeSHA256: "bb"},
		{Kind: ChangeModified, Path: "pkg/a.go", BeforeSHA256: "bb", AfterSHA256: "cc"},
	}
	body := "added command-artifact.txt\nremoved gone.go\nchanged pkg/a.go"

	commands := &DriftError{Stage: "commands", Changes: changes}
	if got, want := commands.Error(), "gomutants: prepare commands changed the frozen snapshot:\n"+body; got != want {
		t.Errorf("commands drift = %q, want %q", got, want)
	}
	discovery := &DriftError{Stage: "discovery", Changes: changes}
	if got, want := discovery.Error(),
		"gomutants: prepare commands changed the snapshot during discovery:\n"+body; got != want {
		t.Errorf("discovery drift = %q, want %q", got, want)
	}
	for _, stage := range []string{
		"source restoration", "verification",
		"probe instrumentation", "probe source restoration",
	} {
		staged := &DriftError{Stage: stage, Changes: changes}
		want := "gomutants: prepare " + stage + " changed the snapshot outside instrumentation:\n" + body
		if got := staged.Error(); got != want {
			t.Errorf("%s drift = %q, want %q", stage, got, want)
		}
	}

	for stage, want := range map[string]string{
		"commands":     "gomutants: prepare commands changed the frozen snapshot:",
		"discovery":    "gomutants: prepare commands changed the snapshot during discovery:",
		"verification": "gomutants: prepare verification changed the snapshot outside instrumentation:",
	} {
		if got := (&DriftError{Stage: stage}).Error(); got != want {
			t.Errorf("empty %s drift = %q, want %q", stage, got, want)
		}
	}

	kinds := map[ChangeKind]snapshot.DriftKind{
		ChangeAdded:    snapshot.DriftAdded,
		ChangeRemoved:  snapshot.DriftRemoved,
		ChangeModified: snapshot.DriftChanged,
	}
	for kind, want := range kinds {
		if got := driftWord(kind); got != want.String() {
			t.Errorf("driftWord(%q) = %q, want %q", kind, got, want.String())
		}
	}
}

func TestReservedErrorsRenderTheExistingText(t *testing.T) {
	t.Parallel()

	scratch := t.TempDir()
	flags := []struct {
		argument string
		flag     string
		message  string
	}{
		{
			"-test.fuzzcachedir=" + scratch, "-test.fuzzcachedir",
			"gomutants: session exec: -test.fuzzcachedir is reserved by the session",
		},
		{
			"-test.fuzzworker", "-test.fuzzworker",
			"gomutants: session exec: -test.fuzzworker is reserved by the Go fuzz coordinator",
		},
		{
			"-test.timeout=1s", "-test.timeout",
			"gomutants: session exec: -test.timeout is reserved by the session's process supervisor",
		},
	}
	for _, c := range flags {
		_, err := sessionTargetArgs([]string{c.argument}, scratch, "exec", false)
		var reserved *ReservedError
		if !errors.As(err, &reserved) {
			t.Fatalf("sessionTargetArgs(%q) = %v, want a *ReservedError", c.argument, err)
		}
		if reserved.Flag != c.flag || reserved.Variable != "" || reserved.Call != "exec" {
			t.Errorf("sessionTargetArgs(%q) = %+v, want the flag %q refused for exec", c.argument, reserved, c.flag)
		}
		if got := err.Error(); got != c.message {
			t.Errorf("message = %q, want %q", got, c.message)
		}
	}

	for _, c := range flags {
		_, err := sessionTargetArgs([]string{c.argument}, scratch, "control", false)
		var reserved *ReservedError
		if !errors.As(err, &reserved) {
			t.Fatalf("sessionTargetArgs(%q) for a control = %v, want a *ReservedError", c.argument, err)
		}
		if reserved.Flag != c.flag || reserved.Call != "control" {
			t.Errorf("sessionTargetArgs(%q) = %+v, want the flag %q refused for control",
				c.argument, reserved, c.flag)
		}
		want := strings.Replace(c.message, "session exec:", "session control:", 1)
		if got := err.Error(); got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	}

	const testLogFlagArgument = "-test.testlogfile=/tmp/caller.log"
	for _, call := range []string{"exec", "probe", "control"} {
		_, err := sessionTargetArgs([]string{testLogFlagArgument}, scratch, call, true)
		var reserved *ReservedError
		if !errors.As(err, &reserved) {
			t.Fatalf("sessionTargetArgs(%q) while recording = %v, want a *ReservedError",
				testLogFlagArgument, err)
		}
		if reserved.Flag != "-test.testlogfile" || reserved.Variable != "" || reserved.Call != call {
			t.Errorf("sessionTargetArgs(%q) = %+v, want the flag refused for %s",
				testLogFlagArgument, reserved, call)
		}
		want := "gomutants: session " + call +
			": -test.testlogfile is reserved by the request's test log recording"
		if got := err.Error(); got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	}
	passedThrough, err := sessionTargetArgs([]string{testLogFlagArgument}, scratch, "exec", false)
	if err != nil {
		t.Fatalf("sessionTargetArgs(%q) without recording = %v, want it passed through",
			testLogFlagArgument, err)
	}
	if !slices.Equal(passedThrough, []string{testLogFlagArgument}) {
		t.Errorf("args = %v, want the caller's own flag verbatim: a consumer supplying its own"+
			" log is not composing anything with the session", passedThrough)
	}

	_, err = overlayEnvironment([]string{"PATH=one"}, []string{"GO_MUTANTS_ACTIVE=stolen"})
	var reserved *ReservedError
	if !errors.As(err, &reserved) {
		t.Fatalf("overlayEnvironment = %v, want a *ReservedError", err)
	}
	if reserved.Variable != "GO_MUTANTS_ACTIVE" || reserved.Flag != "" {
		t.Errorf("overlayEnvironment = %+v, want the variable refused", reserved)
	}
	if got, want := err.Error(), "GO_MUTANTS_ACTIVE is reserved by go-mutants"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestDiagnosticCodeReadsInternalCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain", errors.New("gomutants: prepare test binaries: something"), ""},
		{"execute", &execute.Error{Code: execute.CodeTestBuildFailed, Message: "no"}, "GOM7505"},
		{"validate", &validate.Error{Code: validate.CodeNotMutantInduced, Message: "no"}, "GOM7420"},
		{"discover", &discover.Error{Code: discover.CodePackageErrors, Message: "no"}, "GOM4111"},
		{"runner", &runner.Error{Code: runner.CodeProcessStartFailed, Message: "no"}, "GOM7202"},
		{"gocmd", &gocmd.Error{Code: gocmd.CodeToolchainNotFound, Message: "no"}, "GOM7210"},
		{
			"wrapped",
			fmt.Errorf("gomutants: prepare test binaries: %w",
				&execute.Error{Code: execute.CodeTestBuildFailed, Message: "no"}),
			"GOM7505",
		},
	}
	for _, c := range cases {
		if got := DiagnosticCode(c.err); got != c.want {
			t.Errorf("DiagnosticCode(%s) = %q, want %q", c.name, got, c.want)
		}
	}

	inner := &execute.Error{
		Code:       execute.CodeTestBuildFailed,
		Message:    "the test binary for example.com/a could not be built",
		Output:     "a.go:1:1: syntax error",
		Package:    "example.com/a",
		ExitCode:   2,
		Invocation: &runner.Invocation{Argv: []string{"go", "test", "-c"}},
	}
	wrapped := fmt.Errorf("gomutants: prepare test binaries: %w", inner)
	err := buildError(PreparePhaseBinaryBuild, wrapped)
	var build *BuildError
	if !errors.As(err, &build) {
		t.Fatalf("buildError = %v, want a *BuildError", err)
	}
	if got := DiagnosticCode(err); got != "GOM7505" {
		t.Errorf("DiagnosticCode of a BuildError = %q, want GOM7505", got)
	}
	if build.Code != "GOM7505" || build.Package != "example.com/a" || build.ExitCode != 2 ||
		build.Output != inner.Output || !slices.Equal(build.Argv, inner.Invocation.Argv) {
		t.Errorf("BuildError = %+v, want the inner failure's own fields", build)
	}
	if build.Phase != PreparePhaseBinaryBuild {
		t.Errorf("Phase = %q, want %q", build.Phase, PreparePhaseBinaryBuild)
	}
	if got := err.Error(); got != wrapped.Error() {
		t.Errorf("message = %q, want the cause's own %q", got, wrapped.Error())
	}

	plain := errors.New("gomutants: prepare discovery: no")
	if got := buildError(PreparePhaseDiscovery, plain); got != plain {
		t.Errorf("buildError of an untyped cause = %v, want it unchanged", got)
	}
}

func TestBuildErrorFromAValidationCarriesWhatTheSeamKnows(t *testing.T) {
	t.Parallel()

	inner := &validate.Error{
		Code:    validate.CodeStillFailing,
		Message: "the snapshot does not build although every catalogued file was isolated",
		Output:  "pkg/a.go:3:9: undefined: helper",
	}
	wrapped := fmt.Errorf("gomutants: prepare validation: %w", inner)
	err := buildError(PreparePhaseMainValidation, wrapped)
	var build *BuildError
	if !errors.As(err, &build) {
		t.Fatalf("buildError = %v, want a *BuildError", err)
	}
	if build.Code != "GOM7421" || build.Output != inner.Output {
		t.Errorf("BuildError = %+v, want the validation's own code and output", build)
	}
	if build.Phase != PreparePhaseMainValidation {
		t.Errorf("Phase = %q, want %q", build.Phase, PreparePhaseMainValidation)
	}
	if build.ExitCode != 0 || build.TimedOut || build.Argv != nil || build.Package != "" {
		t.Errorf("BuildError = %+v, want no status, no command and no package", build)
	}
	if got := err.Error(); got != wrapped.Error() {
		t.Errorf("message = %q, want the cause's own %q", got, wrapped.Error())
	}
}

func TestBuildErrorTypesAnyCodeItIsGiven(t *testing.T) {
	t.Parallel()

	inner := &runner.Error{
		Code:       runner.CodeProcessStartFailed,
		Message:    "the go command could not be started",
		Output:     "exec: no such file",
		Invocation: &runner.Invocation{Argv: []string{"go", "list", "./..."}},
	}
	wrapped := fmt.Errorf("gomutants: prepare discovery: %w", inner)
	err := buildError(PreparePhaseDiscovery, wrapped)
	var build *BuildError
	if !errors.As(err, &build) {
		t.Fatalf("buildError = %v, want a *BuildError", err)
	}
	if build.Code != "GOM7202" {
		t.Errorf("Code = %q, want GOM7202", build.Code)
	}
	if !slices.Equal(build.Argv, inner.Invocation.Argv) || build.Output != inner.Output {
		t.Errorf("BuildError = %+v, want the command and output the runner named", build)
	}
	if got := err.Error(); got != wrapped.Error() {
		t.Errorf("message = %q, want the cause's own %q", got, wrapped.Error())
	}
}

func TestExecutionErrorNamesTheFailingPackage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		call     string
		selector string
		failure  *execute.Error
		want     string
	}{
		{
			name:     "a request that measured every prepared binary",
			call:     "exec",
			selector: "",
			failure: &execute.Error{
				Code:    execute.CodeMutantStart,
				Message: "the test binary for example.com/a could not be run",
				Output:  "fork/exec: permission denied",
				Package: "example.com/a",
			},
			want: "example.com/a",
		},
		{
			name:     "a request that named a directory the failure disagrees with",
			call:     "exec",
			selector: "./internal/...",
			failure: &execute.Error{
				Code: execute.CodeStaleCatalog,
				Message: "the generated runtime in example.com/m/internal/store does not know the mutant; " +
					"the catalogue and the instrumented snapshot disagree",
				Output:  "unknown mutant",
				Package: "example.com/m/internal/store",
			},
			want: "example.com/m/internal/store",
		},
		{
			name:     "a failure about the pass rather than about one binary",
			call:     "probe",
			selector: "example.com/a/pkg",
			failure: &execute.Error{
				Code:    execute.CodeProbeLog,
				Message: "the infection log cannot be read against the catalogue it was written for",
			},
			want: "example.com/a/pkg",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			wrapped := fmt.Errorf("gomutants: session %s: %w", c.call, c.failure)
			err := executionError(c.call, c.selector, wrapped)
			var execution *ExecutionError
			if !errors.As(err, &execution) {
				t.Fatalf("executionError = %v, want an *ExecutionError", err)
			}
			if execution.Package != c.want {
				t.Errorf("Package = %q, want %q", execution.Package, c.want)
			}
			if execution.Call != c.call {
				t.Errorf("Call = %q, want %q", execution.Call, c.call)
			}
			if execution.Code != c.failure.Code.String() {
				t.Errorf("Code = %q, want the failure's own %q", execution.Code, c.failure.Code)
			}
			if execution.Output != c.failure.Output {
				t.Errorf("Output = %q, want the failure's own %q", execution.Output, c.failure.Output)
			}
			if got := err.Error(); got != wrapped.Error() {
				t.Errorf("message = %q, want the cause's own %q", got, wrapped.Error())
			}
		})
	}
}

func TestHandBuiltErrorsSayWhatTheyAreWithoutACause(t *testing.T) {
	t.Parallel()

	cases := []struct {
		err  error
		want string
	}{
		{&BuildError{Phase: PreparePhaseBinaryBuild}, "gomutants: prepare binary build failed"},
		{&BuildError{Phase: PreparePhaseProbeCoverageBuild}, "gomutants: prepare probe coverage build failed"},
		{&BuildError{}, "gomutants: preparation failed"},
		{&ExecutionError{Call: "exec"}, "gomutants: session exec could not measure"},
		{&ExecutionError{}, "gomutants: the session could not measure"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("message = %q, want %q", got, c.want)
		}
	}
}

func catalogOfOne(t *testing.T) *mutation.Catalog {
	t.Helper()
	builder := mutation.NewBuilder()
	err := builder.Add(mutation.Candidate{
		Path: "pkg/a.go",
		Rule: mutation.Rule{
			Family:  mutation.FamilyComparison,
			Name:    "eq-to-neq",
			Version: 1,
			Tier:    mutation.TierBalanced,
		},
		Span:         mutation.Span{StartByte: 10, EndByte: 12},
		Original:     "==",
		Replacement:  "!=",
		SourceDigest: mutation.DigestString("the source of pkg/a.go"),
	})
	if err != nil {
		t.Fatalf("cataloguing the candidate: %v", err)
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	return catalog
}
