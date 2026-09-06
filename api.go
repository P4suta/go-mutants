// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"time"

	"github.com/P4suta/go-mutants/internal/runner"
)

// OutputTruncatedPrefix begins the first line of a capture that lost bytes to
// its output limit — [CommandResult.Output], [MutantResult.Output] and
// [ProbeResult.Output] alike.
//
// It is exported for renderers, which style the notice differently from the
// process's own output, and for the consumers that were matching this text
// before there was anything else to match. It is not the way to *ask* whether
// output was lost: the `Truncated` beside each of those fields is the fact, and
// a consumer that branches on it keeps working the day this sentence is
// reworded.
//
// It is defined from internal/runner's own constant rather than repeated, so
// that the string a renderer matches and the string the engine writes cannot
// come to differ.
const OutputTruncatedPrefix = runner.OutputTruncatedPrefix

// OpenOptions controls how [Open] freezes a workspace. Its zero value is the
// ordinary local invocation.
type OpenOptions struct {
	// GoBinary selects the go executable. Empty resolves "go" through PATH.
	GoBinary string
	// SnapshotExclude holds module-relative '/'-separated glob patterns for
	// generated trees that must not enter the frozen workspace.
	SnapshotExclude []string
	// ReportDirectory is a module-relative report directory to exclude from
	// the snapshot in addition to go-mutants' conventional report directory.
	ReportDirectory string
	// TempDirectory is the parent for the snapshot and all session scratch
	// directories. Empty uses the operating system's temporary directory.
	TempDirectory string
	// KeepTemp preserves the snapshot, the probe tree and the scratch directory
	// instead of removing them when the workspace closes.
	//
	// It is the escape hatch for the one question a removed directory cannot
	// answer — what did the tree this mutant ran in actually look like — and it
	// is deliberate in a way the next run can read: each preserved directory is
	// marked kept, so [Open]'s sweep leaves it alone rather than collecting it
	// as an orphan. [Workspace.Preserved] names them after [Workspace.Close].
	//
	// A kept snapshot is a full copy of the module and nothing will ever remove
	// it. That is the price of the answer, and it is charged only when asked.
	KeepTemp bool
	// Env is the complete environment to freeze for child processes. Nil
	// captures the current process environment. GO_MUTANTS_ and temporary
	// directory variables are removed and replaced by the engine as needed.
	Env []string
}

// SweepResult is what [Open] collected before it copied anything: the
// temporary directories of go-mutants runs that were killed before they could
// remove their own.
//
// It is returned by [Workspace.Swept] rather than being part of any report,
// because it is a fact about the machine and not about the workspace. There is
// no OpenReport to hang it on — [Open] returns a workspace or an error — and
// inventing one to carry housekeeping would put a second thing in the way of
// the call every consumer makes.
type SweepResult struct {
	// Removed holds the absolute path of every directory that was collected.
	Removed []string
	// RemovedBytes is what they held, as far as the sweep could measure.
	RemovedBytes int64
	// Live is how many directories a running go-mutants process still holds a
	// lock on. They are somebody's workspace and are never touched.
	Live int
	// Kept is how many were preserved on purpose by a [OpenOptions.KeepTemp]
	// run. They are never touched either, however old they are.
	Kept int
	// Err is why the sweep could not finish, or nil.
	//
	// It is carried here rather than returned by Open, because failing to
	// collect somebody else's leftovers is not a reason to refuse to run: the
	// workspace is fine, the disk is merely fuller than it should be, and a
	// caller that wants to say so has the whole diagnosis in one place.
	Err error
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
	// OutputLimit caps retained combined stdout and stderr, in bytes. Zero or
	// negative selects the engine's default of 1 MiB; a positive value below
	// 256 is raised to 256, so that the truncation notice still fits inside the
	// budget and len(Output) <= OutputLimit stays satisfiable.
	//
	// This is the field [ExecRequest.OutputLimit] and [ProbeRequest.OutputLimit]
	// refer to; all three mean the same thing and default the same way.
	OutputLimit int
}

// CommandResult describes a command that started. A non-zero exit and a
// timeout are results rather than infrastructure errors.
type CommandResult struct {
	ExitCode int
	TimedOut bool
	Duration time.Duration
	// Output is the retained combined stdout and stderr, capped at the
	// effective [Command.OutputLimit] by keeping the tail. len(Output) never
	// exceeds that limit, the truncation notice included.
	Output []byte
	// Truncated reports that Output lost bytes to the limit, in which case it
	// begins with [OutputTruncatedPrefix]. It is true exactly when TotalBytes
	// exceeds the effective limit, and it is the field to branch on: the notice
	// is a line written for a person to read, and a consumer matching its text
	// makes a diagnostic into a wire format nobody can reword.
	Truncated bool
	// TotalBytes is everything the command wrote to both streams, kept or not.
	// It is the same number whether or not anything was dropped, so a caller
	// reporting how much a command produced never has to ask which case it is
	// in.
	TotalBytes int64
}

// PreparePhase identifies one timed stage of session preparation.
//
// The vocabulary is *open*. [KnownPreparePhases] lists every phase this build
// emits, and a later engine may emit one that is not in that list: splitting a
// stage in two, or timing a step that is not timed today, adds a phase without
// changing the meaning of any phase already here. So a consumer must accept an
// unknown phase rather than refuse it — record the string verbatim, or map it
// to a catch-all of its own — and a consumer that keeps a closed schema, an
// enumeration or a fixed set of timers must pin [KnownPreparePhases] in a test
// of its own. That way the day a phase is added is the day that test says so,
// rather than the day somebody's run fails on a string nobody had heard of.
type PreparePhase string

const (
	PreparePhaseDiscovery          PreparePhase = "discovery"
	PreparePhaseProbeSnapshot      PreparePhase = "probe_snapshot"
	PreparePhaseMainValidation     PreparePhase = "main_validation"
	PreparePhaseMainRestoration    PreparePhase = "main_restoration"
	PreparePhaseVerification       PreparePhase = "verification"
	PreparePhaseBinaryBuild        PreparePhase = "binary_build"
	PreparePhaseProbeValidation    PreparePhase = "probe_validation"
	PreparePhaseProbeCoverageBuild PreparePhase = "probe_coverage_build"
	PreparePhaseProbeRestoration   PreparePhase = "probe_restoration"
)

// KnownPreparePhases returns every phase this build emits, in the order a
// preparation reaches them.
//
// The order is the order of the first [PrepareEventStarted] for each phase and
// not of the finishes: the binary build starts before the probe tree's own
// three phases and finishes after them, because the two builds run
// concurrently. A phase [PrepareOptions] turned off is still emitted, as a
// start immediately followed by a finish carrying [PreparePhaseSkipped], so
// this list is what a consumer sees for every preparation rather than only for
// a fully configured one.
//
// [PreparePhase] is an open vocabulary, and this list does not close it: it is
// what today's engine emits, not what every engine will ever emit. It exists so
// that a consumer keeping a closed schema of its own can pin the list
// deliberately, in a test, instead of discovering the vocabulary from whichever
// run happened to exercise every phase.
//
// Each call returns a fresh slice: the list is a fact about this build and not
// a value a caller may edit out from under the next one.
func KnownPreparePhases() []PreparePhase {
	return []PreparePhase{
		PreparePhaseDiscovery,
		PreparePhaseProbeSnapshot,
		PreparePhaseMainValidation,
		PreparePhaseMainRestoration,
		PreparePhaseVerification,
		PreparePhaseBinaryBuild,
		PreparePhaseProbeValidation,
		PreparePhaseProbeCoverageBuild,
		PreparePhaseProbeRestoration,
	}
}

// PrepareEventState distinguishes phase entry from phase completion.
type PrepareEventState string

const (
	PrepareEventStarted  PrepareEventState = "started"
	PrepareEventFinished PrepareEventState = "finished"
)

// PreparePhaseResult records how a finished phase ended.
type PreparePhaseResult string

const (
	PreparePhaseSucceeded PreparePhaseResult = "succeeded"
	PreparePhaseFailed    PreparePhaseResult = "failed"
	PreparePhaseSkipped   PreparePhaseResult = "skipped"
)

// PrepareEvent is emitted synchronously in deterministic dependency order.
// Independent phases may overlap, but callbacks are serialized and every phase
// starts before it finishes.
type PrepareEvent struct {
	Phase    PreparePhase
	State    PrepareEventState
	Result   PreparePhaseResult
	Duration time.Duration
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
	// DiscoveryPackages are module-relative package patterns whose source is mutated.
	DiscoveryPackages []string
	// Packages are relative Go package patterns whose test binaries are built.
	// Empty selects ./....
	Packages []string
	// ProbeCoverPackages are package patterns included in probe coverage.
	ProbeCoverPackages []string
	// Jobs bounds concurrent test-binary builds. Zero uses the configured worker default.
	Jobs int
	// BuildTimeout bounds each validation and test-binary build. Zero uses ten
	// minutes. A negative duration is invalid.
	BuildTimeout time.Duration
	// MutantTimeout is the default outer timeout used by Session.Exec. Zero
	// uses ten seconds. An ExecRequest may override it with a positive value.
	MutantTimeout time.Duration
	// Verify runs once against pristine files with instrumented Go builds. Its zero value means `go test ./...`.
	Verify Command
	// SkipVerify omits Verify when the caller runs prepared controls separately.
	SkipVerify bool
	// Probe prepares binaries that record whether probed mutant values differ.
	Probe bool
	// Trace receives serialized phase start and finish events synchronously.
	Trace func(PrepareEvent)
}

// Catalog is the immutable public description of one prepared session.
// Session.Catalog returns a deep copy.
//
// A caller may keep and edit that copy, and PreparedDigest does not follow it:
// it is the value the engine computed for the session it prepared, not a
// checksum of the struct in hand. Edit a copy and the field goes stale, still
// naming the session the catalogue came from. That is usually what a caller
// wants — evidence stays keyed to the preparation that produced it — but a
// caller that has rewritten a catalogue and needs a key for what it now holds
// has to hash it itself, from the recipe below.
type Catalog struct {
	WorkspaceDigest string
	// Digest identifies the *set of mutants* and nothing else. It is the
	// SHA-256 over three length-prefixed things: the domain separator
	// "go-mutants-catalog-v1", the decimal mutant count, and every [Mutant.ID]
	// in catalogue order.
	//
	// What it does not cover is the half worth writing down, because a digest
	// invites being used as an identity for more than it is. ModulePath,
	// GoVersion, Toolchain, Profile, TestPackages, WorkspaceDigest,
	// [Mutant.Package], [Mutant.Accepted], [Mutant.Probed] and Rejections are
	// all outside it. Two sessions therefore share this digest whenever they
	// catalogued the same mutants, however differently they were prepared: a
	// different module path, a different toolchain, a different profile that
	// happened to select the same rules, a probe tree in one and none in the
	// other, or a validation that rejected mutants the other accepted.
	//
	// So it answers exactly one question — are these two runs looking at the
	// same mutants? — and a consumer that needs "are these two prepared
	// sessions interchangeable?" wants [Catalog.PreparedDigest], which covers
	// the rest of the preparation and carries a recipe version of its own.
	// Hashing these fields into a key by hand is the thing PreparedDigest
	// exists to stop: a recipe nobody versions goes on hitting the day the
	// engine starts reporting something new about a prepared mutant.
	Digest string
	// PreparedDigest identifies the *prepared session*: everything that has to
	// match before evidence gathered against one session may be reused against
	// another. It is what Digest is not, and it exists because every consumer
	// that needed the second question was hashing an approximation of it.
	//
	// It is the SHA-256 over these fields, each written as a four-byte
	// big-endian byte length followed by its bytes — the encoding the mutant ID
	// uses — in this order:
	//
	//   1. the domain separator "go-mutants-prepared-catalog-v1",
	//   2. Digest,
	//   3. WorkspaceDigest,
	//   4. ModulePath,
	//   5. GoVersion,
	//   6. Toolchain,
	//   7. Profile,
	//   8. the decimal len(TestPackages), then every TestPackages element in
	//      order,
	//   9. the decimal len(Mutants), then per mutant in catalogue order:
	//      [Mutant.ID], [Mutant.Package], and three flag bytes — 'a' or '-' for
	//      [Mutant.Accepted], 'p' or '-' for [Mutant.Probed], 's' or '-' for
	//      selection, which is 's' for every mutant until a selection can
	//      narrow a session,
	//  10. the decimal len(Rejections), then every [Rejection.ID] in order.
	//
	// Not hashed, and this list is exhaustive: [Mutant.Index],
	// [Mutant.DisplayID], [Mutant.Path], [Mutant.Line], [Mutant.Column],
	// [Mutant.EndLine], [Mutant.StartByte], [Mutant.EndByte], [Mutant.Family],
	// [Mutant.Rule], [Mutant.RuleVersion], [Mutant.SourceDigest],
	// [Mutant.Original], [Mutant.Replacement], [Mutant.Branch], and every field
	// of a [Rejection] but its ID — [Rejection.DisplayID], [Rejection.Path],
	// [Rejection.Line], [Rejection.Column], [Rejection.Rule] and
	// [Rejection.Diagnostic].
	//
	// Every one of them is a function of something that *is* hashed. An index is
	// a position, a display identity is a prefix of an ID, and the rule, the
	// span, the text on both sides and the source digest are the very inputs
	// [Mutant.ID] is computed from — so a change to any of them is a change to
	// the ID, and the ID is in the recipe. The coordinates and the compiler's
	// words follow from the source that digest names, and a branch proof is a
	// lemma about the same span. Hashing them again would add nothing and would
	// move the key every time a line shifted above an untouched mutant, and a key
	// that moves for a session that has not changed is a cache that never hits.
	//
	// The recipe is written out because this is a wire format: a consumer keying
	// a store on it has to be able to recompute it, recognise a value from an
	// older engine, and say why two sessions differ. The order is the recipe's
	// and is not free to change; a different order is a different digest, and it
	// would come with a new domain separator.
	PreparedDigest string
	ModulePath     string
	GoVersion      string
	Toolchain      string
	Profile        string
	Mutants        []Mutant
	Rejections     []Rejection
	TestPackages   []string
}

// Mutant is one canonical, deduplicated source edit.
type Mutant struct {
	Index     uint32
	ID        string
	DisplayID string
	Path      string
	Package   string
	Line      int
	Column    int
	// EndLine is the 1-based line Original ends on: Line plus the number of
	// newlines in Original. A single-line edit has EndLine equal to Line.
	//
	// It is here so that selecting mutants by line range is one rule rather than
	// two. `go-mutants run --changed` intersects a diff's ranges with
	// `[Line, EndLine]`, and a caller narrowing the same catalogue through this
	// API has to reach the same mutants — including the multi-line ones, which
	// are exactly the mutants a Line-only comparison silently drops when the
	// diff touches their last line and not their first.
	//
	// The count is exact rather than an estimate: Original is precisely the
	// bytes the mutant's span covers, so its newlines are exactly the line
	// breaks inside the span and no re-read of the source can disagree.
	//
	// One case is worth stating outright. An Original that *ends* in a newline
	// counts that newline too, so EndLine is the line after the last one holding
	// any of its bytes: an edit covering "return 0\n" on line 10 reports EndLine
	// 11, and a range naming only line 11 selects it. That over-approximates by
	// one line, deliberately. It is the rule `go-mutants run --changed` already
	// applies, so the library and the CLI select the same mutants; and erring
	// towards selecting a mutant costs an execution, while erring the other way
	// drops one silently and reports a score higher than the truth.
	//
	// A carriage return is not a line break here. A CRLF file's break is one
	// "\n" preceded by a byte that is not one, so "a\r\nb" spans two lines and
	// not three.
	EndLine      int
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
	// Probed implies Accepted. A mutant validation rejected is never executed,
	// so a probe status on it would describe a tree nothing will ever run
	// against; it reads as false whatever the probe tree made of the site.
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
	// OutputLimit caps the retained combined output of each test binary this
	// execution starts, as [Command.OutputLimit] does: the engine's 1 MiB
	// default when it is not positive, and a floor of 256 bytes so that the
	// truncation notice still fits inside the budget.
	//
	// It is per request because one prepared session serves callers that want
	// different amounts out of the same binaries: a console wants a screenful,
	// and a consumer archiving the evidence of a kill wants all of it. Before
	// this field there was no way to say either, and every execution silently
	// took the default.
	OutputLimit int
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
	ID        string
	DisplayID string
	Outcome   Outcome
	KilledBy  string
	Duration  time.Duration
	// OutputTail is the last 50 lines of the deciding binary's combined output,
	// with carriage returns stripped. It is what a console prints, and it is
	// kept as it was: a consumer that renders it needs no change.
	//
	// Being a tail, it usually loses the truncation notice, which sits at the
	// *top* of a capped capture. Truncated is where that fact lives now.
	OutputTail string
	// Output is the bounded combined output of the deciding binary — the
	// failing one for a kill, the timed-out one for a timeout, the one that
	// would not run for an error — capped at the effective
	// [ExecRequest.OutputLimit]. OutputTail summarises exactly these bytes.
	//
	// It is empty for a survivor, as OutputTail is. A survivor's output is
	// thousands of lines of nothing having gone wrong, multiplied by every
	// mutant in a run, and holding it is how a mutation run runs a machine out
	// of memory.
	Output []byte
	// Truncated reports that Output lost bytes to the limit, in which case it
	// begins with [OutputTruncatedPrefix]; TotalBytes is everything the deciding
	// binary wrote, kept or not. Both are false and zero wherever Output is
	// empty.
	Truncated  bool
	TotalBytes int64
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
	// OutputLimit caps the retained combined output of each test binary this
	// pass starts, exactly as [ExecRequest.OutputLimit] does and with the same
	// defaults. A probe pass runs the same tests the same way, so a caller that
	// bounded an execution and not a pass would be holding output it had
	// already said it did not want.
	OutputLimit int
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
	//
	// That is a promise the probe tree cannot keep on its own. It is
	// instrumented from the whole catalogue and its runtime never learns the
	// mutant tree's verdict, so its log can name a site whose *mutation* does
	// not compile — a mutant [Session.Exec] refuses and [Mutant.Probed] reports
	// as false. [Session.Probe] drops those indices before returning: an
	// infection fact about a mutant nothing will execute licenses no skipping,
	// and leaving it in would contradict the very field a caller reads it by.
	//
	// Every claim above is checked before this set is returned, in two stages
	// with the filtering between them: the raw log must be ascending and in
	// range, and every index that survives the filtering must be probed. An
	// index that fails either is go-mutants contradicting itself, and
	// [Session.Probe] reports it as [ErrProbeInconsistent] rather than returning
	// the set. The dropped indices are the exception and not a failure: they
	// name mutants the mutant tree rejected, which the probe tree was entitled
	// to instrument. A caller therefore never has to defend against a malformed
	// set, which is the point — a bounds check nobody writes is a bounds check
	// nobody gets wrong, and a set the engine repaired in silence would arrive
	// here as a measurement licensing skips it cannot justify.
	Infected []uint32
	// ExitCode is the status of the test binary that decided the pass.
	ExitCode int
	// Duration is the wall-clock time the child processes took.
	Duration time.Duration
	// Output is the bounded combined output of the deciding test binary, capped
	// at the effective [ProbeRequest.OutputLimit]. It is there for every
	// outcome, including the ones that carry no Infected, because a pass that
	// proves nothing is exactly the one whose output has to be readable.
	Output []byte
	// Truncated reports that Output lost bytes to the limit, in which case it
	// begins with [OutputTruncatedPrefix]; TotalBytes is everything the deciding
	// binary wrote, kept or not.
	Truncated  bool
	TotalBytes int64
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
