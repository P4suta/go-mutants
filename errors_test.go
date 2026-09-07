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

// TestClosedWorkspaceErrorsAreSentinels is the first half of the claim a
// consumer needs: a refusal that is a fact about the lifecycle is recognisable
// with errors.Is, and its message is exactly the one it always was.
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

	// A closed workspace that *was* prepared is the state Close leaves behind,
	// and it is byte for byte the shape Exec's other guard reads as a failed
	// preparation: Close sets closed and drops the session while prepared stays
	// true. Only the order of the two checks decides which sentinel comes out,
	// so the order is a contract and this is what pins it. A consumer whose
	// workspace is gone must be told that it is gone; "the preparation failed"
	// would send it to open another workspace for a preparation that succeeded.
	preparedThenClosed := &Workspace{closed: true, prepared: true}
	_, closedErr := preparedThenClosed.Exec(t.Context(), Command{Argv: []string{"go", "version"}})
	if !errors.Is(closedErr, ErrWorkspaceClosed) {
		t.Errorf("Exec on a closed workspace that was prepared = %v, want ErrWorkspaceClosed", closedErr)
	}
	if errors.Is(closedErr, ErrPrepareFailed) {
		t.Errorf("Exec on a closed workspace that was prepared = %v, which reads as a failed"+
			" preparation: Close leaves this shape behind and closed has to win", closedErr)
	}
	if got, want := errorText(closedErr), "gomutants: exec: workspace is closed"; got != want {
		t.Errorf("Exec message on a closed prepared workspace = %q, want %q", got, want)
	}
}

// TestSecondPrepareIsErrWorkspacePrepared pins the other lifecycle refusal. It
// is a different condition from a closed workspace and so a different sentinel:
// a caller that retried a preparation has to be able to tell "this workspace is
// spent" from "this workspace is gone".
func TestSecondPrepareIsErrWorkspacePrepared(t *testing.T) {
	t.Parallel()

	prepared := &Workspace{prepared: true}
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

// TestExecAfterAFailedPrepareIsErrPrepareFailed pins the third lifecycle
// refusal, and it is a third sentinel for the same reason the second is a
// second: the three conditions have three different answers. A closed workspace
// is gone, a prepared one may still be executed against, and one whose
// preparation failed is spent — the caller has to open another.
//
// A preparation that began and failed is what a nil session under a prepared
// workspace means. It is the state Prepare leaves behind when it stops
// part-way, which may be with the instrumented sources still in the tree, so
// the command that would run there is refused rather than allowed to compile a
// program nobody wrote.
func TestExecAfterAFailedPrepareIsErrPrepareFailed(t *testing.T) {
	t.Parallel()

	failed := &Workspace{prepared: true, scratch: t.TempDir()}
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

// TestExecAfterASuccessfulPrepareIsNotRefused is the other side of the same
// state, and the one the lifecycle changed: a workspace holding a session was
// prepared successfully, its tree is the snapshot Open froze, and a command
// against it is ordinary work rather than a refusal.
//
// The command is deliberately malformed, so that the assertion is about the
// lifecycle gate alone: reaching the argument check is proof that nothing above
// it refused, without this unit test having to start a process.
func TestExecAfterASuccessfulPrepareIsNotRefused(t *testing.T) {
	t.Parallel()

	prepared := &Workspace{prepared: true, session: &Session{}, scratch: t.TempDir()}
	_, err := prepared.Exec(t.Context(), Command{})
	if errors.Is(err, ErrPrepareFailed) || errors.Is(err, ErrWorkspacePrepared) {
		t.Errorf("Exec after a successful Prepare = %v, want the command itself to be judged", err)
	}
	if got, want := errorText(err), "gomutants: exec: command has no executable"; got != want {
		t.Errorf("Exec after a successful Prepare = %q, want %q", got, want)
	}
}

// TestClosedSessionErrorsAreSentinels covers the four calls a session refuses
// once it is closed. All four carry one sentinel, because a consumer that has
// lost its session has one thing to do about it whichever call noticed.
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

// TestControlRefusalsNameTheControlCall drives [Session.Control] itself for the
// one thing a table over helper functions cannot establish: that the call
// passes its own name down.
//
// [sessionTargetArgs] and [selectTestPackages] both take the call as an
// argument, so a test that calls them with "control" proves only that they were
// told. What a consumer acts on is `Call` on the error *Session.Control
// returned*, and a Control that had been wired to say "exec" would pass every
// other test in this repository — the refusal is the same type, the same
// sentinel and, but for one word, the same sentence.
//
// It is in the unit tier because it needs no toolchain: a hand-built session
// with a scratch directory reaches both refusals before anything is compiled or
// started.
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

// TestMutantSelectionErrors is the four ways a request can name a mutant the
// session will not run, and the one type that says which.
//
// The messages are the ones the API always produced; what is new is that the
// caller can tell a typo from a rejection without reading them.
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
		// The catalogue's own resolution is what reports the ambiguity; what
		// this session adds is the list a caller can act on, in an order that
		// does not depend on catalogue order.
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

// TestDriftErrorRendersEveryKind pins both message shapes and all three kind
// words. The words are the snapshot layer's, and they are asserted against it
// rather than copied, because the two vocabularies are deliberately different:
// a snapshot's file "changed" while a session's file is "modified", and the
// message a user has been reading for a release is the snapshot's.
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
	for _, stage := range []string{"source restoration", "verification", "probe instrumentation", "probe source restoration"} {
		staged := &DriftError{Stage: stage, Changes: changes}
		want := "gomutants: prepare " + stage + " changed the snapshot outside instrumentation:\n" + body
		if got := staged.Error(); got != want {
			t.Errorf("%s drift = %q, want %q", stage, got, want)
		}
	}

	// A drift with nothing in it is not a failure the engine reports, and the
	// header alone is what a hand-built value has to print: a message ending in
	// a newline reaches a log with a hole in it.
	for stage, want := range map[string]string{
		"commands":     "gomutants: prepare commands changed the frozen snapshot:",
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

// TestReservedErrorsRenderTheExistingText covers the flags and the variables a
// session owns. Three of the four messages are the ones the engine has always
// printed; `-test.timeout` is the deliberate change, refused here rather than
// four layers down as GOM7511, so that the two halves of the timeout pairing
// are refused in one place with one sentence.
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

	// The same three flags refused for the session's third call, which is why
	// the call names itself: a consumer composing arguments for a mutant run and
	// handing them to the control beside it has to be told which one said no.
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

	// The fourth flag, which is the only one reserved *conditionally*: it
	// belongs to the request exactly while the request asked for a test log,
	// and passes through untouched when it did not. The message is new and it
	// is the same sentence the other three are written in, so a consumer that
	// renders one renders all four.
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

// TestDiagnosticCodeReadsInternalCodes is the one thing a consumer cannot do
// for itself: the codes live in packages it cannot import, and a code parsed
// out of a message is a code that breaks the day the message is reworded.
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

	// An error carrying no code at all is returned as it was, so that a caller
	// reading the message of a failure that never named a build sees the failure
	// and not a wrapper around it.
	plain := errors.New("gomutants: prepare discovery: no")
	if got := buildError(PreparePhaseDiscovery, plain); got != plain {
		t.Errorf("buildError of an untyped cause = %v, want it unchanged", got)
	}
}

// TestBuildErrorFromAValidationCarriesWhatTheSeamKnows is the shape of the
// other build failure, and the one whose absences are deliberate: a validation
// phase reaches this layer through the seam the bisection search is faked
// behind, which answers whether a subset compiled and not with what status, so
// the code and the compiler's output are there and the exit status and the argv
// are not.
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

// TestBuildErrorTypesAnyCodeItIsGiven covers the failure that reaches a phase
// without one of the three shapes around it — a toolchain probe, or a process
// that could not be supervised, surfacing straight through discovery. The code
// is the part a consumer reports, so it is kept rather than dropped for want of
// a wrapper this function recognises.
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

// TestExecutionErrorNamesTheFailingPackage is the difference between a request
// and a failure.
//
// [ExecRequest.Package] is a *selector*: it may be a module-relative directory,
// and it is empty for the request that measures every prepared binary — which
// is the ordinary one. The failure knows better than that, because the
// execution phase names the binary it could not start, so the concrete import
// path is what a caller is told when there is one. The selector is the fallback
// for the failures that are about the pass rather than about one binary.
//
// The middle case is the one that pins the rule rather than a symptom of it: a
// selector that is present *and disagrees* has to lose. An implementation that
// merely filled in a blank would satisfy the first case and still hand a caller
// a directory pattern where it asked which package broke.
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

// TestHandBuiltErrorsSayWhatTheyAreWithoutACause pins what a value a consumer
// constructed — in a double, in a table of its own — prints. Neither type may
// panic on a nil cause, and neither may print the wire spelling of a phase at
// somebody who is reading a sentence.
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

// catalogOfOne catalogues a single candidate, which is how a resolution is
// tested without a toolchain: the catalogue is a pure value and discovery is
// not what these assertions are about.
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
