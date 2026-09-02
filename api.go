// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"time"
)

// OpenOptions controls how [Open] freezes a workspace. Its zero value is the
// ordinary local invocation.
type OpenOptions struct {
	// GoBinary selects the go executable. Empty resolves "go" through PATH.
	GoBinary string
	// ReportDirectory is a module-relative report directory to exclude from
	// the snapshot in addition to go-mutants' conventional report directory.
	ReportDirectory string
	// TempDirectory is the parent for the snapshot and all session scratch
	// directories. Empty uses the operating system's temporary directory.
	TempDirectory string
	// Env is the complete environment to freeze for child processes. Nil
	// captures the current process environment. GO_MUTANTS_ and temporary
	// directory variables are removed and replaced by the engine as needed.
	Env []string
}

// Command is one shell-free process invocation in a frozen workspace.
type Command struct {
	// Argv is the executable followed by its arguments. No element is split,
	// expanded, substituted, or interpreted by a shell.
	Argv []string
	// Dir is the working directory relative to the module root. Empty means
	// the module root. Absolute and escaping paths are rejected.
	Dir string
	// Env overlays the environment frozen by Open. Each element has KEY=VALUE
	// form. Activation and temporary-directory variables are reserved.
	Env []string
	// Timeout bounds the whole process tree. Zero uses a ten-minute safety
	// default. A negative duration is invalid.
	Timeout time.Duration
	// OutputLimit caps retained combined stdout and stderr. The runner's safe
	// default is used when this is not positive.
	OutputLimit int
}

// CommandResult describes a command that started. A non-zero exit and a
// timeout are results rather than infrastructure errors.
type CommandResult struct {
	ExitCode int
	TimedOut bool
	Duration time.Duration
	Output   []byte
}

// PrepareOptions selects and prepares a reusable mutation session.
type PrepareOptions struct {
	// Profile is balanced, strong, or all. Empty selects balanced.
	Profile string
	// Operators, when non-empty, selects canonical operator family or rule
	// names instead of Profile. The result is always in canonical order.
	Operators []string
	// Include and Exclude are module-relative mutation glob patterns. Excludes
	// win. They select candidates and never remove files from the snapshot.
	Include []string
	Exclude []string
	// Packages are relative Go package patterns whose test binaries are built.
	// Empty selects ./....
	Packages []string
	// Jobs bounds concurrent test-binary builds. Zero uses min(NumCPU, 8).
	Jobs int
	// BuildTimeout bounds each validation and test-binary build. Zero uses ten
	// minutes. A negative duration is invalid.
	BuildTimeout time.Duration
	// MutantTimeout is the default outer timeout used by Session.Exec. Zero
	// uses ten seconds. An ExecRequest may override it with a positive value.
	MutantTimeout time.Duration
	// Verify is run once after instrumentation with no mutant active. Its zero
	// value means `go test ./...`. Vet is disabled only for this generated tree.
	Verify Command
	// Probe also builds the probe tree: a second instrumented snapshot of the
	// same source in which no mutant is ever active and each site it has a form
	// for reports, without side effects, whether the mutated value would have
	// differed. [Session.Probe] measures against it, and [Mutant.Probed] says
	// which mutants it speaks for.
	//
	// It is off by default because it is not free — a second instrumentation, a
	// second compile validation and a second set of test binaries — and because
	// a caller that never asks the infection question should not pay for the
	// answer. Nothing about the mutant tree, the catalogue or [Session.Exec]
	// changes either way.
	Probe bool
}

// Catalog is the immutable public description of one prepared session.
// Session.Catalog returns a deep copy.
type Catalog struct {
	WorkspaceDigest string
	Digest          string
	ModulePath      string
	GoVersion       string
	Toolchain       string
	Profile         string
	Mutants         []Mutant
	Rejections      []Rejection
	TestPackages    []string
}

// Mutant is one canonical, deduplicated source edit.
type Mutant struct {
	Index        uint32
	ID           string
	DisplayID    string
	Path         string
	Package      string
	Line         int
	Column       int
	StartByte    uint32
	EndByte      uint32
	Family       string
	Rule         string
	RuleVersion  int
	SourceDigest string
	Original     string
	Replacement  string
	Accepted     bool
	// Branch is the body this mutant's condition gates, when go-mutants could
	// prove the edit only narrows it. Nil means no proof, never "no branch".
	Branch *BranchProof
	// Probed reports whether a probe of this mutant was compiled into the
	// session's probe tree, and so whether [Session.Probe] can ever name it.
	//
	// It is false without [PrepareOptions.Probe], false for a mutant whose
	// family has no probe form yet, false where discovery could not prove the
	// rewrite exact, and false where the probe site turned out not to compile.
	// A false here is never a statement about the mutant itself: it is
	// catalogued, mutated and executed exactly as any other.
	//
	// What it changes is how the *absence* of this mutant from a
	// [ProbeResult.Infected] set may be read. For a probed mutant that absence
	// is the fact that the target never produced a value the mutant would have
	// changed, so the target cannot kill it. For an unprobed one nothing could
	// have recorded it, so its absence says nothing at all and a caller must
	// treat it as infected by every test. Reading the two the same way is the
	// one mistake this field exists to prevent, and it is the mistake that
	// silently drops the executions that find kills.
	//
	// It is session-local, like the rest of this API's live values: it appears
	// in no report, in no schema, and in no `go-mutants list --json` document,
	// because it describes a tree that exists for as long as the session does.
	Probed bool
}

// BranchDecreasing is the one Direction go-mutants emits today: the mutated
// condition implies the original one on every evaluation.
const BranchDecreasing = "decreasing"

// BranchProof is present on a mutant whose edit can only narrow the condition
// of an if or a for statement. BodyStart is the body's opening brace and
// BodyEnd its closing brace, as 1-based lines and 1-based byte columns of the
// pristine file — the coordinates `go test -coverprofile` reports statement
// blocks in.
//
// The contract is about the span alone: a test during which no statement of
// that body executed cannot distinguish the mutant from the original program,
// so it need not be executed against it. Direction names the lemma the span
// came from and is diagnostic. A consumer must not need to read it, so that a
// later lemma can attach a proof of its own without any consumer changing.
type BranchProof struct {
	Direction       string
	BodyStartLine   int
	BodyStartColumn int
	BodyEndLine     int
	BodyEndColumn   int
}

// Rejection is a catalogued mutant that validation proved does not compile.
type Rejection struct {
	ID         string
	DisplayID  string
	Path       string
	Line       int
	Column     int
	Rule       string
	Diagnostic string
}

// ExecRequest selects one mutant and one test or fuzz target from a prepared
// session. Args are standard Go test-binary arguments, for example
// `-test.run=^TestRoundTrip$` or `-test.fuzz=^FuzzRoundTrip$`.
type ExecRequest struct {
	// Mutant is a full ID or an unambiguous catalog prefix.
	Mutant string
	// Package is an import path or one module-relative package directory.
	// Empty executes the selected target in every compiled test package.
	Package string
	// Args are passed verbatim to each selected test binary. -test.timeout is
	// reserved because the session owns both timeout layers.
	Args []string
	// Env overlays the environment frozen by Open for this execution.
	Env []string
	// Timeout overrides PrepareOptions.MutantTimeout when positive. A negative
	// duration is invalid.
	Timeout time.Duration
}

// Outcome is the stable result vocabulary returned by [Session.Exec].
type Outcome string

// Mutation execution outcomes.
const (
	OutcomeNotRun       Outcome = "not_run"
	OutcomeKilled       Outcome = "killed"
	OutcomeSurvived     Outcome = "survived"
	OutcomeTimedOut     Outcome = "timed_out"
	OutcomeInconclusive Outcome = "inconclusive"
	OutcomeErrored      Outcome = "errored"
)

// MutantResult is one execution of one mutant against the selected binaries.
type MutantResult struct {
	ID         string
	DisplayID  string
	Outcome    Outcome
	KilledBy   string
	Duration   time.Duration
	OutputTail string
	Artifacts  []Artifact
}

// ErrProbeNotPrepared is returned by [Session.Probe] on a session prepared
// without [PrepareOptions.Probe].
//
// It is a sentinel because it is the one probe failure a caller can act on
// rather than only report: the answer is to prepare the session again asking
// for a probe tree. Everything else that stops a probe is an ordinary error.
var ErrProbeNotPrepared = errors.New("gomutants: the session was prepared without a probe tree")

// ProbeRequest selects one test or fuzz target to run against a prepared
// session's probe tree. Its fields mean exactly what [ExecRequest]'s do, minus
// the mutant: a probe tree activates none.
type ProbeRequest struct {
	// Package is an import path or one module-relative package directory.
	// Empty probes the selected target in every compiled test package, and the
	// one log they all append to is what makes the answer a statement about the
	// target rather than about one binary of it.
	Package string
	// Args are passed verbatim to each selected test binary. -test.timeout is
	// reserved, as it is for [ExecRequest].
	Args []string
	// Env overlays the environment frozen by Open for this pass. GO_MUTANTS_
	// and the temporary-directory variables stay reserved; the probe runtime's
	// own variable is set by the session and is not a caller's to supply.
	Env []string
	// Timeout overrides PrepareOptions.MutantTimeout when positive. A negative
	// duration is invalid.
	Timeout time.Duration
}

// ProbeOutcome is how one [Session.Probe] pass ended.
//
// Exactly one of the four is a measurement, and the asymmetry is deliberate: an
// infection fact licenses a caller not to execute a test, so a pass that cannot
// be vouched for reports that it has no facts rather than reporting that
// nothing was infected — which is the same sentence spelled in a way somebody
// would act on.
type ProbeOutcome string

// The probe outcomes.
const (
	// ProbeMeasured is a pass whose every binary exited zero and whose log was
	// readable. It is the only outcome carrying [ProbeResult.Infected].
	ProbeMeasured ProbeOutcome = "measured"
	// ProbeTestFailed is a pass in which a test binary exited non-zero. The
	// probe tree runs the program the user wrote, so a red suite there is a
	// flaky test or a bug in go-mutants, and neither is evidence about which
	// sites the target would have reached.
	ProbeTestFailed ProbeOutcome = "test-failed"
	// ProbeTimedOut is a pass the session's supervisor had to kill. What it had
	// not reached yet is indistinguishable from what it would never reach.
	ProbeTimedOut ProbeOutcome = "timed-out"
	// ProbeUnavailable is a pass whose probe runtime could not open or write
	// its log and refused to run the tests at all. It is the failure mode the
	// runtime exists to make loud: a silent probe reads exactly like one that
	// saw nothing.
	ProbeUnavailable ProbeOutcome = "unavailable"
)

// ProbeResult is one pass of one target over the session's probe tree.
type ProbeResult struct {
	// Outcome is how the pass ended.
	Outcome ProbeOutcome
	// Infected are the catalogue indices — [Mutant.Index] — of the mutants
	// whose site produced, at least once during this target, a value the mutant
	// would not have produced. They are sorted ascending and distinct, and they
	// index the same catalogue [Session.Catalog] returns, so they need no
	// translation.
	//
	// It is non-nil exactly when Outcome is [ProbeMeasured], the empty set
	// included: a target that ran and infected nothing is a fact, and the most
	// useful one there is. Every other outcome carries nil, so a caller that
	// forgets to check the outcome ranges over nothing rather than over a set
	// that means something else.
	//
	// Only a mutant whose [Mutant.Probed] is true can appear here, and the
	// converse is what a caller has to remember: an unprobed mutant is absent
	// from every measurement and must be treated as infected by every test.
	Infected []uint32
	// ExitCode is the status of the test binary that decided the pass.
	ExitCode int
	// Duration is the wall-clock time the child processes took.
	Duration time.Duration
	// OutputTail is the last lines the deciding binary printed, and is empty
	// for a measured pass.
	OutputTail string
}

// Artifact is one bounded standard fuzz-corpus file captured before a target's
// private execution scratch is removed.
type Artifact struct {
	Path   string
	SHA256 string
	Data   []byte
}

// ChangeKind describes how a prepared snapshot moved while targets ran.
type ChangeKind string

// Snapshot change kinds.
const (
	ChangeAdded    ChangeKind = "added"
	ChangeRemoved  ChangeKind = "removed"
	ChangeModified ChangeKind = "modified"
)

// Change is one module-relative difference from the state captured when
// Prepare completed. Changes are returned in path order.
type Change struct {
	Kind         ChangeKind
	Path         string
	BeforeSHA256 string
	AfterSHA256  string
}
