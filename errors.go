// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/validate"
)

var (
	ErrWorkspaceClosed   = errors.New("workspace is closed")
	ErrWorkspacePrepared = errors.New("workspace has already been prepared")
	ErrPrepareFailed     = errors.New("workspace preparation failed; its tree may hold instrumented sources")
	ErrSessionClosed     = errors.New("session is closed")
	ErrInvalidMutantID   = errors.New("mutant id prefix is invalid")
	ErrMutantNotFound    = errors.New("no mutant matches that id prefix")
	ErrAmbiguousMutant   = errors.New("mutant id prefix is ambiguous")
	ErrMutantRejected    = errors.New("mutant was rejected during validation")
	ErrProbeInconsistent = errors.New("the probe log names a mutant the catalogue cannot account for")
	ErrInvalidSelection  = errors.New("selection is invalid")
)

type MutantSelectionError struct {
	Prefix    string
	Reason    error
	Matches   []string
	Rejection *Rejection

	cause error
}

func (e *MutantSelectionError) Error() string {
	if e.Rejection != nil {
		return fmt.Sprintf("gomutants: session exec mutant %s was rejected during validation: %s",
			e.Rejection.DisplayID, e.Rejection.Diagnostic)
	}
	return fmt.Sprintf("gomutants: session exec mutant %q: %s", e.Prefix, errorText(e.cause))
}

func (e *MutantSelectionError) Is(target error) bool { return target != nil && target == e.Reason }

func (e *MutantSelectionError) Unwrap() error { return e.cause }

type DriftError struct {
	Stage   string
	Changes []Change
}

func (e *DriftError) Error() string {
	header := "gomutants: prepare " + e.Stage + " changed the snapshot outside instrumentation:"
	switch e.Stage {
	case driftStageCommands:
		header = "gomutants: prepare commands changed the frozen snapshot:"
	case driftStageDiscovery:
		header = "gomutants: prepare commands changed the snapshot during discovery:"
	}
	if len(e.Changes) == 0 {
		return header
	}
	lines := make([]string, 0, len(e.Changes))
	for _, change := range e.Changes {
		lines = append(lines, driftWord(change.Kind)+" "+change.Path)
	}
	return header + "\n" + strings.Join(lines, "\n")
}

const (
	driftStageCommands  = "commands"
	driftStageDiscovery = "discovery"
)

func driftWord(kind ChangeKind) string {
	switch kind {
	case ChangeAdded:
		return snapshot.DriftAdded.String()
	case ChangeRemoved:
		return snapshot.DriftRemoved.String()
	case ChangeModified:
		return snapshot.DriftChanged.String()
	default:
		return snapshot.DriftKind(0).String()
	}
}

func driftChanges(drifts []snapshot.Drift) []Change {
	changes := make([]Change, 0, len(drifts))
	for _, drift := range drifts {
		changes = append(changes, Change{
			Kind:         changeKindOf(drift.Kind),
			Path:         drift.RelPath,
			BeforeSHA256: drift.WantSHA256,
			AfterSHA256:  drift.GotSHA256,
		})
	}
	return changes
}

func changeKindOf(kind snapshot.DriftKind) ChangeKind {
	switch kind {
	case snapshot.DriftAdded:
		return ChangeAdded
	case snapshot.DriftRemoved:
		return ChangeRemoved
	case snapshot.DriftChanged:
		return ChangeModified
	default:
		return ""
	}
}

type VerificationError struct {
	Command    Command
	ExitCode   int
	TimedOut   bool
	Duration   time.Duration
	Output     []byte
	Truncated  bool
	TotalBytes int64
}

func (e *VerificationError) Error() string {
	if e.TimedOut {
		return fmt.Sprintf("gomutants: prepare instrumented verification timed out after %s", e.Command.Timeout)
	}
	return fmt.Sprintf("gomutants: prepare instrumented verification exited with status %d: %s",
		e.ExitCode, outputSummary(e.Output))
}

type BuildError struct {
	Phase    PreparePhase
	Package  string
	Argv     []string
	ExitCode int
	TimedOut bool
	Output   string
	Code     string

	cause error
}

func (e *BuildError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	if e.Phase == "" {
		return "gomutants: preparation failed"
	}
	return "gomutants: prepare " + phaseWords(e.Phase) + " failed"
}

func phaseWords(phase PreparePhase) string {
	return strings.ReplaceAll(string(phase), "_", " ")
}

func (e *BuildError) Unwrap() error { return e.cause }

func buildError(phase PreparePhase, err error) error {
	typed := &BuildError{Phase: phase, cause: err}
	var executeErr *execute.Error
	var validateErr *validate.Error
	var discoverErr *discover.Error
	switch {
	case errors.As(err, &executeErr):
		typed.Code = executeErr.Code.String()
		typed.Package = executeErr.Package
		typed.Output = executeErr.Output
		typed.ExitCode = executeErr.ExitCode
		typed.TimedOut = executeErr.TimedOut
		typed.Argv = invocationArgv(executeErr.Invocation)
	case errors.As(err, &validateErr):
		typed.Code = validateErr.Code.String()
		typed.Output = validateErr.Output
		typed.TimedOut = validateErr.TimedOut
		typed.Argv = invocationArgv(validateErr.Invocation)
	case errors.As(err, &discoverErr):
		typed.Code = discoverErr.Code.String()
	default:
		code := DiagnosticCode(err)
		if code == "" {
			return err
		}
		typed.Code = code
		var described interface {
			Command() *runner.Invocation
			RetainedOutput() string
		}
		if errors.As(err, &described) {
			typed.Argv = invocationArgv(described.Command())
			typed.Output = described.RetainedOutput()
		}
	}
	return typed
}

func invocationArgv(invocation *runner.Invocation) []string {
	if invocation == nil {
		return nil
	}
	return slices.Clone(invocation.Argv)
}

type ExecutionError struct {
	Call    string
	Package string
	Code    string
	Output  string

	cause error
}

func (e *ExecutionError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	if e.Call == "" {
		return "gomutants: the session could not measure"
	}
	return "gomutants: session " + e.Call + " could not measure"
}

func (e *ExecutionError) Unwrap() error { return e.cause }

func executionError(call, selector string, err error) error {
	code := execute.CodeOf(err)
	if code == "" {
		return err
	}
	pkg := selector
	var failure *execute.Error
	if errors.As(err, &failure) && failure.Package != "" {
		pkg = failure.Package
	}
	return &ExecutionError{
		Call:    call,
		Package: pkg,
		Code:    code.String(),
		Output:  execute.OutputOf(err),
		cause:   err,
	}
}

type PackageNotPreparedError struct {
	Call    string
	Package string
}

func (e *PackageNotPreparedError) Error() string {
	return fmt.Sprintf("gomutants: session %s package %q has no prepared test binary", e.Call, e.Package)
}

type ReservedError struct {
	Call     string
	Flag     string
	Variable string
	Owner    string
}

func (e *ReservedError) Error() string {
	if e.Variable != "" {
		return e.Variable + " is reserved by " + e.Owner
	}
	return "gomutants: session " + e.Call + ": " + e.Flag + " is reserved by " + e.Owner
}

func DiagnosticCode(err error) string {
	if err == nil {
		return ""
	}
	if code := execute.CodeOf(err); code != "" {
		return code.String()
	}
	if code := validate.CodeOf(err); code != "" {
		return code.String()
	}
	if code := discover.CodeOf(err); code != "" {
		return code.String()
	}
	if code := runner.CodeOf(err); code != "" {
		return code
	}
	return gocmd.CodeOf(err)
}

func (s *Session) selectionError(prefix string, cause error) *MutantSelectionError {
	selection := &MutantSelectionError{Prefix: prefix, cause: cause}
	switch {
	case errors.Is(cause, mutation.ErrInvalidPrefix):
		selection.Reason = ErrInvalidMutantID
	case errors.Is(cause, mutation.ErrMutantNotFound):
		selection.Reason = ErrMutantNotFound
	case errors.Is(cause, mutation.ErrAmbiguousPrefix):
		selection.Reason = ErrAmbiguousMutant
		selection.Matches = s.matchingDisplayIDs(prefix)
	}
	return selection
}

func rejectionError(prefix, displayID string, rejection Rejection) *MutantSelectionError {
	if rejection.DisplayID == "" {
		rejection.DisplayID = displayID
	}
	return &MutantSelectionError{Prefix: prefix, Reason: ErrMutantRejected, Rejection: &rejection}
}

func (s *Session) matchingDisplayIDs(prefix string) []string {
	matches := make([]string, 0, 2)
	for _, mutant := range s.publicCatalog.Mutants {
		if strings.HasPrefix(mutant.ID, prefix) {
			matches = append(matches, mutant.DisplayID)
		}
	}
	slices.Sort(matches)
	return matches
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
