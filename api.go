// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import "time"

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
