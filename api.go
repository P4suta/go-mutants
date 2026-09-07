// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"time"

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testlog"
	"github.com/P4suta/go-mutants/trace"
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
	// KeepTemp preserves every temporary directory the workspace made instead
	// of removing them when it closes: the snapshot, the probe tree, the
	// workspace scratch, and the per-call scratch of every [Workspace.Exec],
	// [Session.Exec] and [Session.Probe].
	//
	// It is the escape hatch for the one question a removed directory cannot
	// answer — what did the tree this mutant ran in actually look like — and the
	// per-call scratch is half of that answer: it is where the target's TMPDIR
	// pointed, where a fuzz cache lived, and where anything the test wrote went.
	//
	// It is deliberate in a way the next run can read. The three durable
	// directories are the ones a sweep could reach — they are the ones directly
	// under TempDirectory wearing a go-mutants name prefix — so those are
	// marked kept and [Open]'s sweep leaves them alone rather than collecting
	// them as orphans. A per-call scratch is nested inside one of them, so no
	// sweep is ever a candidate to remove it and nothing of go-mutants' is
	// written into it: a lock and a marker in the child's own TMPDIR would be
	// two files in the very tree the keep exists to let somebody read.
	//
	// [Workspace.Preserved] names all of them after [Workspace.Close], and the
	// recording carries one artifact event per directory saying which kind it
	// was — a per-call one beside the execution it belonged to, a durable one
	// at Close.
	//
	// Nothing is kept by a process that dies: the decision is made at Close, so
	// a workspace killed before it closes leaves directories the next run's
	// sweep collects once their locks are free.
	//
	// A kept snapshot is a full copy of the module and nothing will ever remove
	// it. That is the price of the answer, and it is charged only when asked.
	KeepTemp bool
	// Env is the complete environment to freeze for child processes. Nil
	// captures the current process environment. GO_MUTANTS_ and temporary
	// directory variables are removed and replaced by the engine as needed.
	Env []string
	// Trace is where this workspace records what it does: every subprocess it
	// starts, every tree it freezes, every preparation stage, every mutant
	// attempt and probe pass, and every directory it kept. The events are
	// [trace.Event] values in `gomutants-trace-v1`, and the `TraceSeq` on every
	// result is the join a consumer keeping a recording of its own writes
	// against them.
	//
	// Nil does not switch recording off. The workspace records into a bounded
	// ring instead — [trace.DefaultRingCapacity] events, output digested away —
	// which [Workspace.Recording] hands back at any point and after
	// [Workspace.Close]. That is the same default an untraced `go-mutants run`
	// takes, and for the same reason: the failure nobody expected is exactly
	// the failure nobody thought to ask for a recording of. A caller that
	// supplies a sink is served by it alone, and Recording then returns nil
	// rather than a second, shorter copy of what the sink already has.
	//
	// A supplied sink is *not* wrapped in [trace.Digested]: it receives the
	// captured output an exec event carries and the tail a mutant attempt
	// carries, because a sink writing to disk is meant to preserve them. A sink
	// that keeps events in memory should wrap itself — `trace.Digested(mine)` —
	// or it grows with the run rather than with its own capacity.
	//
	// A failed [Open] records only into a sink a caller supplied. It ends the
	// recording with a run-end whose verdict is "failed", so a sink sees a
	// complete stream; but no Workspace is returned, so a default ring dies
	// with the workspace that never existed and there is nothing to read it
	// from. A caller that wants the account of a failed Open supplies a sink.
	//
	// A trace is never evidence. Nothing here enters a catalogue digest, a
	// mutant identity, a result or an error, and a sink that fails or panics
	// costs the event rather than the run — the recorder counts the loss and
	// reports it in the recording's last event. The sink belongs to the caller
	// and is never closed by the workspace.
	//
	// See docs/trace-v1.md for the event contract and
	// docs/adr/0001-trace-is-not-evidence.md for why it is fail-open.
	Trace trace.Sink
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
	// MemoryLimit bounds the resident memory of the whole process tree, in
	// bytes. Zero is unbounded, and a negative value is invalid.
	//
	// Unlike a session's runs, a workspace command has no derived bound and
	// gets none by default. There is nothing to derive it from: a command is
	// whatever the consumer chose to run — a build, a vet, a baseline — and
	// go-mutants has measured none of them. A consumer that wants one names it.
	MemoryLimit int64

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
	// PeakRSS is the highest resident memory any binary this call started was
	// observed to hold, in bytes, and MemoryExceeded reports that one of them
	// passed the effective MemoryLimit and had its tree killed for it.
	//
	// PeakRSS is the maximum over the binaries rather than the deciding
	// binary's alone, because what a call cost the machine is the worst moment
	// it put the machine through. It is zero where the platform could not
	// measure, which is a different statement from a peak of zero and is why a
	// consumer comparing it against a budget checks it is positive first.
	//
	// MemoryExceeded and TimedOut are never both set: they are different kills
	// and the supervisor reports exactly one.
	PeakRSS        int64
	MemoryExceeded bool
	// TraceSeq is the `seq` of the `exec` event this command was recorded at,
	// and is how a consumer joins its own recording to the workspace's: the
	// event carries the argument vector, the directory, the environment names,
	// the exit status and the digest of everything the child printed.
	//
	// It is zero only when nothing was recorded, which with the ring default
	// means the command failed before it was started — a missing executable, a
	// directory outside the module, a refused environment overlay. A workspace
	// always has a recorder, so a command that ran always has a sequence.
	//
	// A non-zero sequence names an event that was recorded and not necessarily
	// one that can still be read: a sink that refused it kept nothing, and a
	// bounded ring that overflowed has since dropped it. The recording's
	// run-end reports both.
	TraceSeq int64
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
	// Selection narrows what a caller intends to *execute*, by line range.
	// Nil selects everything.
	//
	// It is applied after discovery and after validation, and it changes
	// nothing about either: [Catalog.Digest], [Catalog.PreparedDigest], every
	// [Mutant.ID], [Mutant.Accepted], [Mutant.Probed] and [Catalog.Rejections]
	// are identical to the same preparation without it. All it does is set
	// [Mutant.Selected], by intersecting each mutant's `[Line, EndLine]` span
	// with the ranges given for its [Mutant.Path] — the rule `go-mutants run
	// --changed` applies to a diff, shared with it rather than reimplemented.
	//
	// PreparedDigest staying put is deliberate and comes with a rule; see it
	// and [Mutant.Selected] for the argument, and the rule itself is: never
	// store "not run, out of selection" as evidence.
	//
	// Narrowing discovery instead would be faster and wrong. Mutant identities
	// are minted from a file's own bytes and the catalogue is deduplicated
	// across the module, so a discovery pass that skipped unselected files
	// would produce a different catalogue — and a consumer could no longer
	// compare a narrowed run against the run before it, which is the whole
	// reason it narrowed.
	//
	// The narrowing is **advisory**. [Session.Exec] runs an unselected mutant
	// exactly as it runs a selected one: this field says what the caller set out
	// to measure, and the session refusing to measure anything else would turn a
	// plan into a cage — a consumer that finds a survivor and wants its
	// neighbours executed would have to prepare the module again.
	//
	// It is refused with [ErrInvalidSelection] when a path or a range is not one
	// the engine can read; see [Selection] for the normalisation it goes
	// through, and [Catalog.Selection] for the value that comes back.
	Selection *Selection
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
	//      [Mutant.Accepted], 'p' or '-' for [Mutant.Probed], and a third that
	//      is the constant 's',
	//  10. the decimal len(Rejections), then every [Rejection.ID] in order.
	//
	// The third flag byte is a constant the v1 recipe reserved and did not need.
	// It stays rather than being dropped, because dropping it is a different
	// digest for every session anybody has already stored evidence against.
	//
	// Not hashed, and this list is exhaustive: Selection, [Mutant.Selected],
	// [Mutant.Index], [Mutant.DisplayID], [Mutant.Path], [Mutant.Line],
	// [Mutant.Column], [Mutant.EndLine], [Mutant.StartByte], [Mutant.EndByte],
	// [Mutant.Family], [Mutant.Rule], [Mutant.RuleVersion],
	// [Mutant.SourceDigest], [Mutant.Original], [Mutant.Replacement],
	// [Mutant.Branch], and every field of a [Rejection] but its ID —
	// [Rejection.DisplayID], [Rejection.Path], [Rejection.Line],
	// [Rejection.Column], [Rejection.Rule] and [Rejection.Diagnostic].
	//
	// Most of them are left out because they are a function of something that
	// *is* hashed. An index is a position, a display identity is a prefix of an
	// ID, and the rule, the span, the text on both sides and the source digest
	// are the very inputs [Mutant.ID] is computed from — so a change to any of
	// them is a change to the ID, and the ID is in the recipe. The coordinates
	// and the compiler's words follow from the source that digest names, and a
	// branch proof is a lemma about the same span. Hashing them again would add
	// nothing and would move the key every time a line shifted above an
	// untouched mutant, and a key that moves for a session that has not changed
	// is a cache that never hits.
	//
	// Selection and [Mutant.Selected] are left out for a different reason, and
	// it is the one worth reading before keying anything on this value. A
	// selection is advisory — it changes nothing the engine does, and
	// [Session.Exec] runs an unselected mutant exactly as it runs a selected one
	// — so it describes the caller's plan rather than the session. What is keyed
	// on this digest is *per-mutant evidence*: this mutant survived against this
	// prepared tree, which is a fact about the tree, the toolchain and the
	// mutant and about none of the caller's intentions. Move the key with the
	// selection and the first narrowed run misses on every row a consumer has
	// ever stored, then re-measures a module's worth of mutants to write down
	// answers it already had.
	//
	// The rule that makes that safe belongs to the caller and is one line:
	// **never store "not run, out of selection" as evidence.** A mutant the
	// selection left out was not measured, so there is nothing about it to
	// record; recording an absence as a result is the only way two sessions
	// under one key could come to disagree.
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
	// Selection is the normalised copy of [PrepareOptions.Selection] this
	// preparation actually applied — paths cleaned, ranges sorted and merged —
	// or nil when none was given and every mutant is selected.
	//
	// It is the engine's answer rather than an echo of the request, which is
	// what makes it worth reading: a caller that handed over "5-7, 1-3, 4" of
	// "./pkg/../pkg/x.go" gets back "1-7" of "pkg/x.go", so two runs can be
	// asked whether they selected the same lines without normalising them
	// again, and a report can say which lines a score covers.
	//
	// It is a deep copy, like the rest of a [Catalog]: editing it cannot change
	// what the session says it selected.
	Selection *Selection
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
	// `[Line, EndLine]`, [PrepareOptions.Selection] intersects a caller's ranges
	// with the same span through the same code, and a caller applying the rule
	// itself has to reach the same mutants — including the multi-line ones,
	// which are exactly the mutants a Line-only comparison silently drops when
	// the range touches their last line and not their first.
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
	// Selected reports whether this mutant's `[Line, EndLine]` span met
	// [PrepareOptions.Selection]. It is true for every mutant when no selection
	// was given, so a consumer that never narrows reads it as "yes" and never
	// has to ask whether it was narrowing.
	//
	// It is **advisory**, and it is the only thing a selection changes. The
	// mutant is catalogued, validated, instrumented and — if a caller asks —
	// executed exactly as any other: [Session.Exec] runs an unselected mutant
	// without complaint, because a plan for a run is not a rule about what may
	// be measured, and a consumer that finds a survivor and wants its
	// neighbours executed must not have to prepare the module a second time.
	//
	// What it is *not* is a statement about the mutant. An unselected mutant is
	// one the caller did not set out to measure this time; it is not
	// uninteresting, not equivalent, and not out of scope. A consumer scoring a
	// narrowed run says which lines the score covers — [Catalog.Selection] is
	// that answer — rather than reporting it as the module's.
	//
	// It is **not** hashed into [Catalog.PreparedDigest], and that is a decision
	// with a rule attached. Evidence keyed on that digest is a fact about the
	// tree, the toolchain and the mutant, none of which a selection touches, so
	// moving the key when a caller narrows a run would cost it every row it had
	// stored. What a caller owes in return is one line: never store "not run,
	// out of selection" as evidence. An unselected mutant was not measured, so
	// there is nothing about it to record.
	Selected bool
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
	// MemoryLimit bounds the resident memory of each test binary's whole
	// process tree, in bytes. Zero uses the session's own bound, derived from
	// what the verification run of the unmutated tests cost; a negative value
	// is invalid.
	//
	// It is [Timeout]'s twin and exists for the same reason: a target that
	// hangs is stopped by the deadline, and a target that allocates without
	// bound takes the machine before any deadline expires. The bound is
	// enforced by the same supervisor that enforces the deadline, over the same
	// process tree, and a tree stopped by it reports MemoryExceeded rather than
	// TimedOut.
	//
	// Not every platform can enforce one — macOS reports what a process cost
	// once it is gone and cannot watch one while it runs — and there the field
	// is accepted and has no effect. PeakRSS is still reported everywhere.
	MemoryLimit int64
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
	// RecordTestLog asks each binary this execution starts to write down which
	// environment variables and files it consulted, and returns the answers in
	// [MutantResult.TestLogs]. See [TestLog].
	//
	// It is off by default, because it is a file per binary per execution and
	// a run executes thousands. While it is on, a caller-supplied
	// `-test.testlogfile` in Args is refused with a [*ReservedError]: two of
	// them are not two logs, since the standard flag package keeps the last
	// value it sees. With it off the flag passes through verbatim, which is the
	// method this field replaces.
	RecordTestLog bool
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
	// PeakRSS is the highest resident memory any binary this call started was
	// observed to hold, in bytes, and MemoryExceeded reports that one of them
	// passed the effective MemoryLimit and had its tree killed for it.
	//
	// PeakRSS is the maximum over the binaries rather than the deciding
	// binary's alone, because what a call cost the machine is the worst moment
	// it put the machine through. It is zero where the platform could not
	// measure, which is a different statement from a peak of zero and is why a
	// consumer comparing it against a budget checks it is positive first.
	//
	// MemoryExceeded and TimedOut are never both set: they are different kills
	// and the supervisor reports exactly one.
	PeakRSS        int64
	MemoryExceeded bool
	Artifacts      []Artifact
	// Binaries are the test binaries this execution started, in launch order,
	// by the import path of the package each was built from — never the file
	// that was executed, which is a name in a directory the session deletes.
	// It stops where the execution stopped: a mutant killed by the second of
	// three binaries was measured against two, and naming all three would
	// describe a measurement that was never made. KilledBy is one of them.
	Binaries []string
	// TraceSeq is the `seq` of the `mutant-exec` event this execution was
	// recorded at. That event names the binaries, the arguments, the timeout,
	// the outcome and the `exec` events of the children underneath it, so a
	// consumer holding this number can reach the whole account of the attempt.
	//
	// It is zero only when nothing was recorded, which with the ring default
	// means the call failed before it reached the execution — an unresolvable
	// mutant, a package with no prepared binary, a refused flag.
	TraceSeq int64
	// TestLogs are what each binary this execution started recorded about the
	// environment variables and files it consulted: one per element of
	// Binaries, in the same order, and nil unless
	// [ExecRequest.RecordTestLog] asked for it. See [TestLog].
	//
	// Nil rather than an empty slice for a request that did not ask, because an
	// empty slice would be a measurement — "these binaries touched nothing" —
	// and that is the sentence a consumer would act on.
	TestLogs []TestLog
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
	// MemoryLimit bounds each test binary's process tree, exactly as
	// [ExecRequest.MemoryLimit] does and with the same meaning for zero. A pass
	// is only evidence about an execution if the same tests ran the same way,
	// so a pass measured with more of the machine than the executions it
	// licenses skipping would not be one.
	MemoryLimit int64
	// OutputLimit caps the retained combined output of each test binary this
	// pass starts, exactly as [ExecRequest.OutputLimit] does and with the same
	// defaults. A probe pass runs the same tests the same way, so a caller that
	// bounded an execution and not a pass would be holding output it had
	// already said it did not want.
	OutputLimit int
	// RecordTestLog asks each binary of this pass to write down what it
	// consulted, exactly as [ExecRequest.RecordTestLog] does, and returns the
	// answers in [ProbeResult.TestLogs]. The binaries are the probe tree's, so
	// [TestLog.Dir] names a directory in that tree rather than in the mutant
	// one.
	RecordTestLog bool
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
	// PeakRSS is the highest resident memory any binary this call started was
	// observed to hold, in bytes, and MemoryExceeded reports that one of them
	// passed the effective MemoryLimit and had its tree killed for it.
	//
	// PeakRSS is the maximum over the binaries rather than the deciding
	// binary's alone, because what a call cost the machine is the worst moment
	// it put the machine through. It is zero where the platform could not
	// measure, which is a different statement from a peak of zero and is why a
	// consumer comparing it against a budget checks it is positive first.
	//
	// MemoryExceeded and TimedOut are never both set: they are different kills
	// and the supervisor reports exactly one.
	PeakRSS        int64
	MemoryExceeded bool
	// Binaries are the probe tree's test binaries this pass started, in launch
	// order and by import path, exactly as [MutantResult.Binaries] names them.
	Binaries []string
	// TraceSeq is the `seq` of the `probe-exec` event this pass was recorded
	// at. That event names the binaries, the arguments, the outcome, the
	// infected mutants by identity, and the `exec` events of the children
	// underneath it.
	//
	// It is zero only when nothing was recorded, which with the ring default
	// means the call failed before it reached the probe tree — a session
	// prepared without one, a package with no prepared binary, a refused flag.
	//
	// A pass that *did* reach the probe tree and then failed — a binary that
	// could not be started, a cancelled context, an infection log this session
	// cannot account for — carries this sequence and Binaries beside the error,
	// and nothing else. It reached an execution, so it is in the recording, and
	// a failure that handed back no sequence would be the one case a consumer
	// most wants to read about and the one it could not find. Outcome, Infected
	// and the captured output stay at their zero values, because the pass
	// established none of them.
	TraceSeq int64
	// TestLogs are what each binary of this pass recorded about what it
	// consulted, one per element of Binaries and in the same order, and nil
	// unless [ProbeRequest.RecordTestLog] asked for it. Like Binaries and
	// TraceSeq, it is kept beside the error of a pass that reached an execution
	// and then failed: what the binaries touched is an account of what ran and
	// never a measurement, so it survives where the infection set cannot.
	TestLogs []TestLog
}

// ControlRequest selects one test or fuzz target to run against a prepared
// session's binaries with no mutant activated. Its fields mean exactly what
// [ExecRequest]'s do, minus the mutant: a control activates none.
type ControlRequest struct {
	// Package is an import path or one module-relative package directory.
	// Empty runs the selected target in every compiled test package, in order,
	// exactly as an [ExecRequest] with no package does — which is the control a
	// caller wants beside an execution it did not narrow either.
	Package string
	// Args are passed verbatim to each selected test binary. -test.timeout is
	// reserved, as it is for [ExecRequest], and for the same reason.
	//
	// They are meant to be the *same* arguments the execution they are a
	// control for was given. A control of a different target is a control of
	// nothing.
	Args []string
	// Env overlays the environment frozen by Open for this run. GO_MUTANTS_ and
	// the temporary-directory variables stay reserved; there is nothing for a
	// caller to supply in their place, because a control is defined by their
	// absence.
	Env []string
	// Timeout overrides PrepareOptions.MutantTimeout when positive. A negative
	// duration is invalid.
	Timeout time.Duration
	// MemoryLimit bounds each test binary's process tree, exactly as
	// [ExecRequest.MemoryLimit] does and with the same meaning for zero. A
	// control is what an execution is compared against, so it is measured under
	// the execution's budget: a control given more of the machine than the
	// mutant beside it is a control of a different program.
	MemoryLimit int64
	// OutputLimit caps the retained combined output of each test binary this
	// run starts, exactly as [ExecRequest.OutputLimit] does and with the same
	// defaults.
	OutputLimit int
	// RecordTestLog asks each binary this run starts to write down what it
	// consulted, exactly as [ExecRequest.RecordTestLog] does, and returns the
	// answers in [ControlResult.TestLogs].
	//
	// A caller comparing an execution against the control beside it asks both
	// or neither: what the *original* program read is what says whether the
	// pair is still evidence about the repository in hand.
	RecordTestLog bool
}

// ControlResult is one run of the original program through the session's
// prepared test binaries.
//
// It carries no [Outcome], deliberately. A control is not a mutant and has
// nothing to survive or be killed by: what it reports is what the program the
// user wrote did — a status, or a timeout — and what that means beside a mutant
// execution is the caller's judgement to make.
type ControlResult struct {
	// Package is the import path of the test binary that decided the run: the
	// one whose tests failed, or the one that hung. It is empty when every
	// binary passed, which is the rule [MutantResult.KilledBy] follows —
	// a name here is a name a consumer reports, and one invented for a run
	// nothing decided would name nothing.
	Package string
	// ExitCode is the status of the binary this run stopped at — the deciding
	// binary's; when nothing decided, the last binary that ran — and it is
	// **negative** for a tree the supervisor killed.
	//
	// The timeout case is the one worth reading twice. internal/runner reports
	// no exit status at all for a tree it killed rather than inventing one, and
	// that is carried up unchanged, exactly as [CommandResult.ExitCode] carries
	// it for a workspace command with the same field set. A zero here would be
	// a status the child never returned, and a caller that forgot to look at
	// TimedOut would read it as the original program passing.
	ExitCode int
	// TimedOut reports a binary the supervisor had to kill at the effective
	// timeout.
	TimedOut bool
	// Duration is the wall-clock time the child processes took, summed over
	// every binary this run started — which is not the binary Output describes.
	// It is zero when the call returns an error, along with everything else the
	// run did not establish.
	Duration time.Duration
	// Output is the bounded combined output of one test binary, capped at the
	// effective [ControlRequest.OutputLimit]: the deciding binary's; when
	// nothing decided, the last binary that ran. A consumer that wants one
	// package's output asks for that package.
	//
	// Unlike [MutantResult.Output] it is there even when everything passed, and
	// the asymmetry is deliberate. A survivor's output is thousands of lines of
	// nothing having gone wrong multiplied by every mutant in a run, which is
	// the memory that cap exists to bound; a control is one run per execution at
	// most, and its output is the very thing a consumer shows beside a mutant's
	// failure to say what the program does when nothing is switched on.
	Output []byte
	// Truncated reports that Output lost bytes to the limit, in which case it
	// begins with [OutputTruncatedPrefix]; TotalBytes is everything *that*
	// binary wrote, kept or not.
	//
	// TotalBytes is one binary's total and never the run's, which is the one
	// place these fields and Duration disagree on purpose: Duration sums over
	// the binaries the run started, and this describes the single binary Output
	// came from.
	Truncated  bool
	TotalBytes int64
	// PeakRSS is the highest resident memory any binary this call started was
	// observed to hold, in bytes, and MemoryExceeded reports that one of them
	// passed the effective MemoryLimit and had its tree killed for it.
	//
	// PeakRSS is the maximum over the binaries rather than the deciding
	// binary's alone, because what a call cost the machine is the worst moment
	// it put the machine through. It is zero where the platform could not
	// measure, which is a different statement from a peak of zero and is why a
	// consumer comparing it against a budget checks it is positive first.
	//
	// MemoryExceeded and TimedOut are never both set: they are different kills
	// and the supervisor reports exactly one.
	PeakRSS        int64
	MemoryExceeded bool
	// Binaries are the test binaries this run started, in launch order and by
	// import path, exactly as [MutantResult.Binaries] names them. They stop
	// where the run stopped: a control that failed in the second of three
	// binaries names two.
	Binaries []string
	// ExecSeqs are the `exec` events those starts were recorded at, in the same
	// order and one per element of Binaries.
	//
	// It is a field rather than something to be read out of the summarising
	// note's prose. `mutant-exec` and `probe-exec` carry an `exec_seqs` of their
	// own and a `note` has no such field, so without this the only way down from
	// a control to the commands underneath it would be to parse a sentence
	// written for a person — which is exactly the coupling every other join in
	// this API exists to remove.
	//
	// It is empty when nothing was recorded, never when a run started
	// something, and it is kept beside Binaries when a run that had already
	// started something then failed.
	ExecSeqs []int64
	// TraceSeq is the `seq` of the `note` event this run was summarised at.
	//
	// A control has no event type of its own — `gomutants-trace-v1` closes the
	// `type` enum, and there is no payload for one — so it is recorded as its
	// per-binary `exec` events, of kind `control-run`, plus one note of kind
	// `control` that names what they came to. The note is what this points at,
	// so that one call has one event a consumer can join its own recording to,
	// and ExecSeqs is the way down from it to those executions.
	//
	// It is zero only when nothing was recorded, which with the ring default
	// means the call failed before it reached an execution — a package with no
	// prepared binary, a refused flag, a closed session. A run that *did* reach
	// one and then failed carries this sequence, Binaries and ExecSeqs beside
	// the error and nothing else, exactly as [ProbeResult.TraceSeq] does.
	TraceSeq int64
	// TestLogs are what each binary this run started recorded about what it
	// consulted, one per element of Binaries and in the same order, and nil
	// unless [ControlRequest.RecordTestLog] asked for it. It is kept beside the
	// error of a run that reached an execution and then failed, exactly as
	// Binaries and ExecSeqs are.
	TestLogs []TestLog
}

// A TestLogOp is one kind of access a target reported: an environment variable
// it read, a file it opened or stat-ed, or a directory it moved into.
//
// The vocabulary is package os's rather than go-mutants', and it is open the
// way the engine treats it: an operation a later Go release reports and this
// build has never heard of is carried through verbatim rather than dropped. A
// dropped operation reads as an input nothing consulted, which is the one
// answer a consumer must never be given by accident.
type TestLogOp string

// The operations the testing package writes today.
const (
	// TestLogGetenv is an environment variable the target read.
	TestLogGetenv TestLogOp = "getenv"
	// TestLogOpen is a file the target opened.
	TestLogOpen TestLogOp = "open"
	// TestLogStat is a file the target asked about without opening.
	TestLogStat TestLogOp = "stat"
	// TestLogChdir is a directory the target moved into. Every relative Name
	// after it is relative to that directory rather than to [TestLog.Dir].
	TestLogChdir TestLogOp = "chdir"
)

// A TestLogEntry is one thing a target consulted.
//
// Name is what the testing package wrote, byte for byte. The engine resolves
// nothing: a relative path stays relative, a name that no longer exists on
// disk is reported as it was written, and what any of it means is the
// consumer's question. Resolving here would be the engine guessing at a
// working directory the log itself may have changed.
type TestLogEntry struct {
	Op   TestLogOp
	Name string
}

// A TestLog is what one test binary recorded about the environment variables
// and files it consulted, when a request asked for it.
//
// It is the standard `-test.testlogfile` the go command uses to decide whether
// a cached test result is still valid, handed over rather than interpreted. A
// consumer keeping evidence about a (mutant, target) pair reads it for the same
// reason: the inputs a target consulted are what say whether yesterday's
// verdict is still about today's repository.
type TestLog struct {
	// Package is the import path of the test binary this log is about — one of
	// [MutantResult.Binaries], [ProbeResult.Binaries] or
	// [ControlResult.Binaries], at the same position.
	Package string
	// Dir is the directory that binary ran in: the package's own directory
	// inside the session's snapshot, or inside the private copy a fuzz target
	// runs in. Every relative Name in Entries is relative to it until a
	// [TestLogChdir] entry says otherwise.
	//
	// It is a path in a tree the session removes when it closes, unless
	// [OpenOptions.KeepTemp] asked for it. It is here because a relative name
	// means nothing without it, not because the directory is somewhere to go
	// and look afterwards.
	Dir string
	// Entries are the accesses the binary reported, in the order it reported
	// them. It is empty for a target that consulted nothing, which is a
	// measurement and not a failure — Err is where a failure to measure is.
	Entries []TestLogEntry
	// Complete reports that the log ends at a line boundary *and* that the
	// binary exited on its own. It is the field to read before acting on
	// Entries.
	//
	// Both halves are needed, and the bytes alone are the trap. The testing
	// package writes the log through a 4096-byte buffer and flushes it whenever
	// it fills, as well as from the deferred call at the end of `M.Run` — so a
	// chatty target the supervisor killed leaves a log that ends in a newline
	// and is nonetheless a fraction of what it touched. A binary the engine
	// timed out or cancelled therefore reports false whatever the last byte is.
	// A quiet one leaves the empty file it created and carries Err instead.
	// Reading a partial log as the whole truth is how a consumer computes a
	// cache key from half a target's inputs and then believes it.
	//
	// The go command additionally trusts a log only from a test that exited 0.
	// That rule is deliberately not applied here — an execution's whole subject
	// is often a binary that did not — so a caller that wants it applies it to
	// the result's own exit status.
	//
	// Two more shapes leave a log short and neither shows in the bytes. A
	// `TestMain` calling `m.Run` more than once flushes only the *first* run's
	// entries, the testing package guarding its own teardown with a sync.Once;
	// and a test calling os.Exit skips that teardown altogether. go-mutants
	// does not pass the go command's companion `-test.paniconexit0`, which
	// would turn the second into a panic, because it would change what the
	// binary does and this API measures the program the user wrote.
	Complete bool
	// Err is why there is no log to read, in one line, and empty when there is
	// one: a binary that wrote none, a file that could not be read, a target
	// this engine records nothing for. It is a string rather than an error
	// because it is one fact about one binary carried inside a result — a run
	// that could not record what a target touched is not a run that failed, and
	// the measurement it did make stands beside it.
	//
	// A fuzz target is the case a consumer will meet. The Go fuzz coordinator
	// starts its workers with the coordinator's own arguments, so every worker
	// inherits the flag and recreates the file the coordinator is writing;
	// go-mutants therefore passes no flag for a `-test.fuzz` target and says so
	// here. The go command does not combine the two either.
	Err string
}

// ErrTestLogUnsupported reports a binary that refused `-test.testlogfile`: the
// standard flag package printed "flag provided but not defined" and exited 2.
//
// **No standard Go test binary does this.** The flag is part of the testing
// package and the session runs binaries it compiled itself, so this is not a
// condition a caller is expected to meet. The branch exists because exit 2 is
// a *non-zero status*, and a non-zero status is how the engine recognises a
// detection: without it, a repository whose binaries somehow refused the flag
// would report every mutant as killed by a binary that never started a test,
// and the score would be a fiction with nothing in the output saying so.
//
// It arrives wrapped in an [*ExecutionError] whose `Call` names which of the
// session's three runs asked, and it is never reported as a kill, a failing
// probe or a red control. What would have to change is the request — drop
// `RecordTestLog` — and that is why it is a sentinel rather than only a
// diagnostic code.
var ErrTestLogUnsupported = testlog.ErrUnsupported

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
