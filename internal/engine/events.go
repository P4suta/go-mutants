// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"slices"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/trace"
)

// A Phase names one stage of a run. Phases are ordered, they are entered at
// most once, and a run may stop in any of them.
//
// The names are user-facing: they are what the plain renderer prints after
// "phase " and what the dashboard labels its progress with, so they are short,
// lowercase, and fixed.
type Phase string

// The phases, in the order a complete run enters them.
const (
	// PhaseDiscover locates the toolchain and copies the workspace. Finding the
	// candidates themselves belongs to PhaseMutate, because a catalogue is only
	// worth building once the unmutated tests have been seen to pass.
	PhaseDiscover Phase = "discover"
	// PhaseBaseline builds the snapshot and measures the unmutated tests.
	PhaseBaseline Phase = "baseline"
	// PhaseMutate discovers the candidates, catalogues them, instruments and
	// validates the snapshot, proves the instrumented tree still passes, and
	// executes the mutants.
	PhaseMutate Phase = "mutate"
	// PhaseReport builds the run report and publishes it.
	PhaseReport Phase = "report"
)

// String returns the phase as it is printed.
func (p Phase) String() string { return string(p) }

// Phases returns every phase in run order. A renderer that draws a progress
// bar needs to know the whole sequence before the first event arrives, which is
// why the list is exported rather than inferred from the events.
func Phases() []Phase {
	return []Phase{PhaseDiscover, PhaseBaseline, PhaseMutate, PhaseReport}
}

// A Status is how a run ended. It is carried by [RunCompleted] and by
// [RunOutcome], and it is what the report's `status` field records.
type Status string

// The terminal statuses.
const (
	// StatusOK means the run finished everything it set out to do. It says
	// nothing about the mutation score or about any policy gate — those are
	// decided from the outcomes by internal/mutation, and travel in
	// [RunSummary.ExitCode].
	StatusOK Status = "ok"
	// StatusFailed means the run stopped on an error and its results, if any,
	// cannot be trusted.
	StatusFailed Status = "failed"
	// StatusInterrupted means a signal or a cancelled context ended the run
	// early. It is kept distinct from StatusFailed because nothing was wrong:
	// the user asked for it, and the exit code says 130 or 143 rather than 2.
	StatusInterrupted Status = "interrupted"
)

// String returns the status as it is printed and serialised.
func (s Status) String() string { return string(s) }

// A TimeoutSource says where the per-mutant timeout came from.
//
// It is reported next to the timeout everywhere the timeout is shown, because
// the two numbers a user needs in order to act are the value and whether they
// chose it: a derived timeout that is too tight is fixed by making the tests
// faster or by setting `test.timeout`, and an explicit one is fixed by editing
// the configuration.
type TimeoutSource string

// The timeout sources.
const (
	// TimeoutDerived is max(10s, slowest baseline × 5).
	TimeoutDerived TimeoutSource = "derived"
	// TimeoutExplicit is the configured `test.timeout` or `--timeout`.
	TimeoutExplicit TimeoutSource = "explicit"
)

// String returns the source as it is printed.
func (s TimeoutSource) String() string { return string(s) }

// A MemorySource says where the per-mutant memory bound came from, or that
// there is none.
//
// It is [TimeoutSource]'s twin with one value the clock does not need. A
// deadline can always be enforced; a memory bound cannot, because measuring a
// tree while it runs is not something every supported platform offers, and a
// run that has no bound has to be able to say so rather than report a number
// nothing is enforcing.
type MemorySource string

// The memory sources.
const (
	// MemorySourceDerived is max(1 GiB, largest baseline peak × 4).
	MemorySourceDerived MemorySource = "derived"
	// MemorySourceExplicit is the configured `test.memory` or `--memory`.
	MemorySourceExplicit MemorySource = "explicit"
	// MemorySourceUnavailable is no bound: either nothing measured a peak to
	// derive one from, or this platform cannot enforce one. Both are reported
	// once, as a [Warning], because a user who thinks a runaway mutant will be
	// stopped and is wrong should be told by the run rather than by the machine.
	MemorySourceUnavailable MemorySource = "unavailable"
)

// String returns the source as it is printed.
func (s MemorySource) String() string { return string(s) }

// A CoverageMode says how coverage narrowed the run.
//
// It is this package's own spelling of the same two facts internal/report
// publishes, for the same reason [TimeoutSource] is: a report is a published
// format and an event stream is not, and one enum serving both would make a
// rename of a console label a breaking change to somebody's jq expression. The
// mapping is one function in the engine.
type CoverageMode string

// The coverage modes.
const (
	// CoverageOff means every mutant was measured against every test binary.
	// It is what a custom `test.command` produces — see
	// [coverage.CodeCustomTestCommand] — and what a run whose coverage pass
	// failed falls back to.
	CoverageOff CoverageMode = "off"
	// CoveragePackage means each test binary was profiled once and every mutant
	// was measured only against the binaries whose profile reaches its lines.
	// The mutants no binary reaches were not executed at all; they are
	// survivors with [MutantResult.Uncovered] set.
	CoveragePackage CoverageMode = "package"
)

// String returns the mode as it is printed.
func (m CoverageMode) String() string { return string(m) }

// A CacheMode says whether the run reused outcomes it had proven before.
//
// It is what the run did rather than what was configured: `cache.mode auto`
// resolves to on or off before any mutant is executed, and this is what it
// resolved to. See [report.CacheMode], whose spelling this mirrors.
type CacheMode string

// The cache modes.
const (
	// CacheOff means no outcome was read and none was stored.
	CacheOff CacheMode = "off"
	// CacheOn means every executable mutant was looked up and every reusable
	// outcome was stored.
	CacheOn CacheMode = "on"
)

// String returns the mode as it is reported.
func (m CacheMode) String() string { return string(m) }

// An Event is one thing the engine has to say while a run is in flight.
//
// The interface is sealed: the marker method is unexported, so every event is
// one of the types in this file and a renderer's type switch over them is
// exhaustive by construction. Adding a case is a change to this file, which is
// where the reviewers who care about the contract are looking.
//
// Events are values and are safe to keep: nothing the engine sends is mutated
// afterwards, and every slice-valued field is cloned before it is published.
//
// [MutantStarted] and [MutantFinished] are published from the execution
// workers, so several goroutines send on the channel at once and the order of
// two mutants' events is whatever order the machine settled them in. Everything
// a renderer must be able to reproduce byte for byte is therefore in
// [RunCompleted], which is published from the run's own goroutine after every
// worker has joined.
type Event interface {
	// event seals the interface. It has no behaviour.
	event()
}

// RunPlanned is the first event of every run. It arrives before any work is
// done, so a renderer can draw its frame before there is anything to put in it.
type RunPlanned struct {
	// RunID identifies this run: a UTC timestamp and four hex digits, unique
	// enough to name a report file and short enough to quote in a bug report.
	// The engine generates it, so every renderer and every report agree.
	RunID string
	// Workers is the number of mutants that will be executed concurrently,
	// after `execution.jobs` and `--jobs` have been resolved.
	Workers int
}

// PhaseChanged reports that the run has entered a phase. It is emitted once per
// phase, on entry, so the [PhaseChanged.Detail] describes what is about to
// happen rather than what just did.
type PhaseChanged struct {
	// Phase is the phase being entered.
	Phase Phase
	// Detail is a one-line human description of the work, composed by the
	// engine. Renderers print it verbatim: only the engine knows how many
	// baseline runs were configured or which test command will be run, and a
	// renderer that guessed would be wrong the first time a default changed.
	Detail string
}

// PhaseCompleted reports that the run has left a phase, and how long it was in
// it. It is emitted once per phase, on exit, immediately before the
// [PhaseChanged] of the next one — and for the last phase of a run, on every
// path out of it, including a failure and an interruption.
//
// It is the other half of [PhaseChanged] and exists because a phase's duration
// is the coarsest useful thing a run can say about where its time went. A
// renderer could time the gap between two PhaseChanged events itself, but the
// last phase has no next one to be followed by, and a run that stopped in the
// middle of one would leave that phase untimed exactly when the timing matters
// most.
type PhaseCompleted struct {
	// Phase is the phase being left, and is always the one the most recent
	// [PhaseChanged] announced.
	Phase Phase
	// Duration is the wall-clock time spent in it.
	Duration time.Duration
}

// Traced carries one event of the run's own recording onto the event stream.
//
// It is published only when [Options.PublishTrace] is set, which is what a
// verbose run asks for: the verbose renderer draws the recording — every event
// under `-vv`, two of them in prose under `-v` — and the recording already
// travels through a channel a renderer is draining. Publishing each event as it
// is recorded makes the two orders one order — a renderer printing them as they
// arrive is printing the recording — and it costs a run that did not ask for
// them exactly nothing, because the fan-out is only built when somebody did.
//
// The event is a deep copy taken at the moment it was recorded, so a renderer
// may keep it. Unlike every other event in this file it is published from
// whichever goroutine recorded it, execution workers included; that is already
// the contract for [MutantStarted] and [MutantFinished] and it is why the engine
// publishes its reproducible summary in [RunCompleted] instead.
type Traced struct {
	// Event is the recorded event, exactly as the sink received it.
	Event trace.Event
}

// BaselineProgress reports one completed baseline run.
//
// It is published twice over in a complete run: once per configured
// observation while the pristine snapshot is being measured, and once more for
// the single instrumented baseline that proves the rewrite preserved meaning.
// The two are told apart by the phase they arrive in — the instrumented one is
// the sole `1 of 1` inside [PhaseMutate] — rather than by a discriminator, so
// that a renderer showing "the tests were run and took this long" needs one
// case and not two.
type BaselineProgress struct {
	// Run is the one-based index of the run that just finished.
	Run int
	// Of is how many baseline runs there are in total.
	Of int
	// Duration is how long the run took, measured the same way every mutant
	// will be: the whole supervised wall-clock time, so the timeout derived
	// from it covers the same overhead it will have to pay for.
	Duration time.Duration
}

// BaselineCompleted reports the finished baseline measurement and the timeout
// derived from it.
//
// It exists because the timeout and its provenance are facts only the engine
// holds, and the renderer's "baseline ok" line has to state both. Deriving them
// in the renderer from [BaselineProgress] would mean writing the derivation
// rule twice, in two packages, with no test that they agree.
type BaselineCompleted struct {
	// Runs holds every observation, in the order they were measured. It is a
	// fresh slice the engine does not retain.
	Runs []time.Duration
	// Average is the mean of Runs.
	Average time.Duration
	// Slowest is the maximum of Runs, the number the derivation is built on.
	Slowest time.Duration
	// Timeout is the per-mutant timeout this run will use.
	Timeout time.Duration
	// TimeoutSource says whether Timeout was derived or configured.
	TimeoutSource TimeoutSource
}

// MemoryDerived reports the per-mutant memory bound and where it came from.
//
// It is a separate event rather than three more fields on [BaselineCompleted]
// because a bound is not always there. The baseline's own line says what the
// suite measured and what budget it bought; this says whether the machine will
// hold anybody to it, which on one supported platform and on any platform whose
// baseline nobody could measure is "no" — and a renderer that had to infer that
// from a zero in a field would be inferring it.
//
// It follows [BaselineCompleted] and, like it, is published once.
type MemoryDerived struct {
	// Limit is the bound in bytes, and is zero when Source is
	// [MemorySourceUnavailable] — the one case where there is no bound at all.
	Limit int64
	// Source says whether Limit was derived, configured, or is absent.
	Source MemorySource
	// Peak is the largest resident memory any baseline run was observed to
	// reach, in bytes, and the number the derivation is built on. It is zero
	// when nothing could measure one, which is one of the two reasons a run is
	// unbounded.
	Peak int64
}

// Discovered reports what one discovery pass found.
//
// The two numbers are deliberately not the same kind of thing, and the field
// documentation says which is which: candidates are proposed edits, and skips
// are suppressed sites. A run that reports many skips and no candidates has
// found something worth explaining, which is what `--explain` is for.
type Discovered struct {
	// Candidates is how many proposed edits discovery produced, before
	// deduplication into the catalogue.
	Candidates int
	// Skips is how many candidate sites were suppressed, summed over every
	// file and reason. It is the sum of the recorded counts rather than the
	// number of (file, reason) rows, so it answers "how much was passed over"
	// rather than "how many kinds of thing were passed over".
	Skips int
}

// Validated reports what compiling the instrumented snapshot established.
//
// A rejection is an ordinary outcome rather than a failure — see
// internal/validate — so both numbers are facts about the run and neither of
// them stops it.
type Validated struct {
	// Accepted is how many catalogued mutants compile.
	Accepted int
	// Rejected is how many do not, and are reported in the run report's
	// `rejected[]` with the compiler's own diagnostic.
	Rejected int
}

// SelectionNarrowed reports that the run will execute less than the catalogue
// it just validated, and why.
//
// It is published only by a run that narrowed itself with `--changed` or
// `--shard`, between validation and the first mutant — the moment a user learns
// how much of the run is about to not happen, which is the single most useful
// number a narrowed run has to offer. A `--mutant` run publishes nothing here:
// naming one mutant is its own announcement.
//
// The fields are the facts rather than a sentence, so that the two renderers
// can word it their own way and neither has to parse the other's.
type SelectionNarrowed struct {
	// ChangedRef is the ref the diff was taken against, and is empty for a run
	// that did not narrow by a diff.
	ChangedRef string
	// Shard and Shards are which shard of how many, and are both zero for a run
	// that was not split.
	Shard  int
	Shards int
	// Selected is how many mutants survived the narrowing, and Of how many
	// there were to narrow — the accepted catalogue, not the whole of it, since
	// a rejected mutant was never going to be executed by anybody.
	Selected int
	Of       int
}

// CoverageMapped reports what the coverage pass established, and is published
// only by a run that did one: a run with coverage off publishes a [Warning]
// saying why instead, and never this.
//
// It arrives after the test binaries have been profiled and before the first
// mutant is executed, which is the moment a user learns how much of the run is
// about to be skipped — the single most useful number a coverage-guided run
// has to offer.
type CoverageMapped struct {
	// Binaries is how many test binaries were profiled.
	Binaries int
	// Covered and Uncovered partition the selected mutants: Covered are the
	// ones at least one binary reaches, and Uncovered the ones none does, which
	// are reported as survivors without being executed.
	Covered   int
	Uncovered int
}

// A MutantResult is one mutant's settled outcome, with everything a renderer
// needs in order to describe it without holding the catalogue.
//
// It carries display data rather than references: a renderer that had to look a
// mutant up would need the catalogue, the discovery result, and the join
// between them, which is three things the engine already did once.
type MutantResult struct {
	// ID is the full 64 hex character activation identity.
	ID string
	// DisplayID is the collision-checked short form.
	DisplayID string
	// Path is the '/'-normalized module-relative source path.
	Path string
	// Line and Column are the 1-based coordinates discovery reported, with the
	// column measured in bytes.
	Line   int
	Column int
	// Rule is the operator that proposed the edit.
	Rule string
	// Original and Replacement are the bytes the mutant swapped. They are
	// source text, not printable prose: a renderer quotes them rather than
	// assuming they fit on a line.
	Original    string
	Replacement string
	// Outcome is the settled verdict. It is never
	// [mutation.OutcomeInconclusive] before the serial retry has run, because
	// internal/execute only settles a timeout once it has tried to reproduce it.
	Outcome mutation.Outcome
	// Duration is the wall-clock time the mutant's child processes took, summed
	// over every attempt. It is zero for an uncovered mutant, which had none.
	Duration time.Duration
	// Worker is the worker that settled the mutant. The serial retry pass
	// reports worker 0, which is honest rather than arbitrary: it runs one
	// mutant at a time with nothing else in flight.
	Worker int
	// Uncovered says no test binary reaches this mutant's lines, so the run did
	// not execute it. It is only ever set alongside
	// [mutation.OutcomeSurvived] — nothing ran, so nothing could have caught
	// it — and only in [CoveragePackage] mode.
	//
	// A renderer that ignores it is not wrong, only less informative: the
	// mutant really did survive. A renderer that shows it is telling the user
	// something more actionable than "your test missed this", namely "no test
	// runs this line at all".
	Uncovered bool
	// Cached says the outcome was adopted from the outcome cache rather than
	// measured by this run, so the duration is the one the run that measured it
	// recorded. It is only ever set on an outcome the cache stores — killed,
	// survived, or a confirmed timeout — and never alongside Uncovered.
	Cached bool
	// KilledBy is the import path of the test binary that detected the mutant —
	// the one that failed, or the one it hung — and is empty for every other
	// outcome. It is the same identity the report's `killed_by` carries.
	//
	// A renderer needs it to answer the first question a kill raises, which is
	// which suite caught it: on a module with forty test binaries, "killed"
	// alone leaves a reader with forty places to go and look.
	KilledBy string
	// Attempts is how many passes over the test binaries the mutant took: one
	// for a mutant that settled first time, two for one that timed out and was
	// retried serially, and zero for an uncovered mutant.
	//
	// It is carried rather than collapsed into the outcome because "survived"
	// and "survived twice" are different facts about a flaky test, and because a
	// confirmed timeout is only distinguishable from an unconfirmed one by
	// whether the second pass happened.
	Attempts int
	// CoveringTestPackages are the import paths of the test binaries whose
	// coverage profile reaches this mutant's lines, sorted, exactly as the
	// report's `covering_test_packages` carries them.
	//
	// It is empty for a run with coverage off, where nothing was measured, and
	// empty for an uncovered mutant, where the measurement is what established
	// that nothing reaches it — [MutantResult.Uncovered] is what tells those two
	// apart. For a survivor that *was* covered it is the actionable half of the
	// finding: these are the suites that ran the line and did not notice.
	CoveringTestPackages []string
	// PeakRSS is the highest resident memory any binary this mutant was
	// measured against was observed to hold, MemoryExceeded says the run's
	// memory bound is what stopped it, and MemoryLimit is the bound it was
	// measured under.
	//
	// All three travel on the result rather than being looked up, which is the
	// rule this whole type follows: a renderer that had to remember a number
	// from an earlier event in order to describe this one would be a renderer
	// that draws differently depending on what it happened to have seen.
	//
	// MemoryLimit is zero for an unbounded run and for a mutant nothing
	// executed — a cached outcome, an uncovered one — because a bound is a fact
	// about a measurement and neither of those is one: a cached outcome was
	// measured by another run under whatever budget that run had, and an
	// uncovered mutant was settled by a coverage profile. PeakRSS is zero in
	// those two cases too, and on a platform that could not measure.
	// MemoryExceeded is only ever set alongside [mutation.OutcomeKilled].
	PeakRSS        int64
	MemoryExceeded bool
	MemoryLimit    int64
}

// clone returns a copy that shares no slice with the receiver, so that a
// renderer holding the result cannot observe the engine reusing a list it read
// out of the coverage mapping.
func (m MutantResult) clone() MutantResult {
	m.CoveringTestPackages = slices.Clone(m.CoveringTestPackages)
	return m
}

// MutantStarted reports that an attempt at one mutant has begun.
//
// It fires at the beginning of every attempt, the serial retry of a timed-out
// mutant included, so a live dashboard can show that retry happening rather
// than a worker apparently stuck. A mutant that timed out and was retried
// therefore produces two of these and one [MutantFinished].
type MutantStarted struct {
	// ID is the full activation identity.
	ID string
	// DisplayID is the short form.
	DisplayID string
	// Path and Line locate the mutant for a progress line.
	Path string
	Line int
	// Rule is the operator that proposed the edit.
	Rule string
	// Worker is the worker that claimed it.
	Worker int
}

// MutantFinished reports one mutant's settled outcome. It fires exactly once
// per mutant the run reached, including one whose outcome is
// [mutation.OutcomeNotRun] because a signal arrived before its retry could
// happen: the stream reports what became of every mutant it started.
//
// One kind of mutant produces a MutantFinished with **no preceding
// [MutantStarted]**, and a renderer that pairs the two has to allow for it: an
// uncovered mutant, published with [MutantResult.Uncovered] set. Nothing
// started, so announcing a start would be inventing one — there is no worker
// and no attempt — but the mutant does have a settled outcome, and a stream
// that stayed silent about it would leave every renderer's counts short of the
// report's. These arrive from the run's own goroutine, all together, before the
// first [MutantStarted] of the execution phase.
type MutantFinished struct {
	// Result is the settled outcome and the data to render it with.
	Result MutantResult
}

// CacheHit reports that one mutant's outcome was adopted from the outcome cache
// instead of being measured.
//
// It is the accounting event and not the outcome: a [MutantFinished] carrying
// the same id, with [MutantResult.Cached] set, follows it immediately. Both are
// published for the same reason an uncovered mutant produces a MutantFinished
// with no MutantStarted — a renderer's counts and the report's have to agree —
// and separating them means a renderer that only wants the outcomes can ignore
// this event entirely without its numbers going wrong.
//
// No [MutantStarted] precedes it. Nothing started: that is the point.
type CacheHit struct {
	// ID is the full activation identity.
	ID string
	// DisplayID is the short form.
	DisplayID string
	// Outcome is what the cache says happened, which is always one the cache
	// stores: killed, survived, or a confirmed timeout.
	Outcome mutation.Outcome
}

// DirectoryKept reports one temporary directory the run left behind because it
// was asked to, and where it is.
//
// It is published on the way out of the run, once per preserved directory, in
// [RunOutcome.Preserved]'s order — and only by a run that set
// [Options.KeepTemp], so a run that keeps nothing publishes none of these and
// its output is byte-identical to what it was before the option existed.
//
// It is a path the run produced rather than something that went wrong, which is
// why it is its own event and not a [Warning]: the user asked for the directory
// and got it, and a CI log parser that treats warnings as problems should not
// see one here.
type DirectoryKept struct {
	// Kind is [KeptSnapshot] or [KeptScratch].
	Kind string
	// Path is the absolute path of the directory.
	Path string
}

// Warning reports something the user should know that did not stop the run.
//
// Every warning carries a stable GOM#### code for the same reason every error
// does: a message can be reworded, and a code is what somebody searches for.
type Warning struct {
	// Code is the stable GOM#### identifier.
	Code string
	// Message is a one-line explanation that does not repeat the code.
	Message string
	// Detail is everything there is to say about the same condition, and is
	// empty whenever the message already says all of it.
	//
	// It exists because one line is the right length for a run that is about to
	// carry on and succeed, and the wrong length for the person asking why: the
	// coverage fallback's real reason is a compiler's output, and folding that
	// onto the warning would print a diagnostic blob at every user who did not
	// ask. It travels on the event rather than only in the recording so that the
	// renderer showing it — which is `-v` — can put it under the line it
	// explains, on every run, including one whose recording could not be opened
	// and which therefore publishes no [Traced] at all.
	//
	// It may be several lines. A renderer that ignores it prints exactly what it
	// printed before this field existed, which is what every renderer does below
	// `-v`. It is deliberately absent from the run report: [report.Warning] is a
	// published format, and this is prose for a console.
	Detail string
}

// ReportPublished reports that the run report is on disk and complete.
//
// It is emitted only after the atomic rename, never before: a path that has
// been announced has to be a path that can be opened. Every path it names obeys
// that rule individually, which is why a run whose project artefacts failed
// still publishes this event — with the two history paths filled in and the
// artefact paths empty — before it reports the failure. The alternative would
// be a run that wrote a report and never said where.
type ReportPublished struct {
	// RunPath is the absolute path of this run's own immutable document.
	RunPath string
	// LatestPath is the absolute path of the copy that always names the newest
	// run of this workspace. It is a copy rather than a pointer, so reading the
	// newest run is one file open; see internal/report.
	LatestPath string
	// ProjectionPath is the absolute path of the `mutation.json` written into
	// `report.directory`: the lossy, one-way projection of this run into the
	// mutation-testing-report format the Stryker ecosystem's viewers read.
	//
	// It is empty when `report.formats` did not ask for it — `--report none` and
	// `--report html` both leave it so — and empty on the failure path described
	// above. It is never the path of a file that is not there.
	ProjectionPath string
	// HTMLPath is the absolute path of the `mutation.html` written beside it:
	// one self-contained page that opens from `file://` and fetches nothing.
	// Empty on the same terms as ProjectionPath.
	HTMLPath string
	// TracePath is the directory this run recorded its diagnostic account into,
	// and is empty for a run that kept its account in memory — which is every
	// run that did not ask for a recording.
	//
	// It rides on this event rather than being printed by whoever opened the
	// recording, because it is one more path a run produced and the renderers
	// already know what to do with those: it is laid out like the rest, it is
	// suppressed by --quiet like the rest, and a dashboard run replays it into
	// the scrollback with the rest. See [Options.TraceDirectory].
	TracePath string
}

// Counts is the counted breakdown of a run, as the closing summary states it.
//
// Survived is every survivor, expected and unexpected alike, because that is
// what a reader counting mutants sees. The split the score is computed from
// lives in [RunSummary.Score], whose denominator has already had the expected
// survivors taken out of it.
type Counts struct {
	// Total is every catalogued mutant that was not rejected by validation.
	Total int
	// Killed, Survived, TimedOut, Inconclusive, Errored and NotRun partition
	// Total.
	Killed       int
	Survived     int
	TimedOut     int
	Inconclusive int
	Errored      int
	NotRun       int
	// Rejected is how many catalogued mutants validation refused. It is not
	// part of Total: a mutant that does not compile was never executed and has
	// no outcome to count.
	Rejected int
	// Uncovered is how many of Survived were survivors because no test binary
	// reaches their lines. It is a *subset* of Survived rather than a seventh
	// bucket, and adding it to the partition would double-count them: an
	// uncovered mutant is a survivor, and the reason it survived is the extra
	// fact this number carries. It is zero unless the run was
	// [CoveragePackage].
	Uncovered int
	// Cached is how many of Total the run adopted from the outcome cache rather
	// than measuring. It is a subset of the partition rather than a bucket of
	// its own, exactly as Uncovered is: a cached mutant was killed, survived, or
	// timed out, and the extra fact this number carries is where the answer came
	// from. It is zero unless the run was [CacheOn].
	Cached int
}

// ExpectationCounts is the `[[mutation.expect]]` ledger, counted by state.
type ExpectationCounts struct {
	// Fulfilled is the number of rows whose mutant survived, as predicted.
	Fulfilled int
	// Unfulfilled is the number the run did not confirm: the tests caught the
	// mutant, or it was never measured.
	Unfulfilled int
	// Stale is the number whose id is not in this catalogue any more.
	Stale int
}

// Total is how many rows the ledger holds.
func (e ExpectationCounts) Total() int { return e.Fulfilled + e.Unfulfilled + e.Stale }

// A SkipCount is one suppression reason and how many candidate sites it
// accounted for across the whole workspace.
type SkipCount struct {
	// Reason is discovery's own spelling of why the site was passed over.
	Reason string
	// Count is how many sites it suppressed.
	Count int
}

// A RunSummary is everything the closing summary block states.
//
// It exists so that the summary is a fact the engine computed once rather than
// a derivation each renderer performs: the score, the ordering, and the exit
// code are decisions, and two implementations of a decision eventually disagree
// in front of a user who is looking at both.
type RunSummary struct {
	// RunID is the identifier the report was filed under.
	RunID string
	// ExitCode is what [mutation.Decide] made of the published report. It is
	// meaningful for a completed run; an interrupted one exits on its signal,
	// which the engine does not know, so a renderer says "interrupted" rather
	// than printing a number this field cannot honestly carry.
	ExitCode mutation.ExitCode
	// Failure is the first gate that failed, and the zero value when none did.
	//
	// It is carried rather than left to be inferred because half the gates are
	// invisible in the numbers beside them. A strict run's survivors are on the
	// screen and a low score is on the screen, but "the run produced no
	// mutants", "an expectation is unfulfilled or stale", and "a mutant could
	// not be executed by the harness" each leave a reader with an exit code and
	// nothing that names the gate. [mutation.Failure.Detail] is documented to be
	// deterministic and free of paths and timings, so it is safe in a block
	// meant to be diffed.
	Failure mutation.Failure
	// Notable are the mutants worth looking at, worst first: survivors, then
	// confirmed timeouts, then inconclusive results, then harness errors, each
	// group ordered by path, line, column, rule, and id. Killed and not-run
	// mutants are deliberately absent — they are in [RunSummary.Counts], and a
	// summary that listed every kill would bury the four lines that need
	// acting on.
	Notable []MutantResult
	// Counts is the counted breakdown.
	Counts Counts
	// Coverage is how coverage narrowed the run. A renderer needs it to decide
	// whether "uncovered 0" is a measurement worth printing or a number nobody
	// went looking for; see [Counts.Uncovered].
	Coverage CoverageMode
	// Cache is what the outcome cache did, for the same reason Coverage is
	// carried: a renderer needs it to tell "the cache was on and cold" from "the
	// cache was off", which are the same zero in [Counts.Cached].
	Cache CacheMode
	// Score is the mutation score, as the two integers it is derived from. It
	// is undefined exactly when the denominator is zero; see [mutation.Score].
	Score mutation.Score
	// Expectations is the ledger counted by state.
	Expectations ExpectationCounts
	// Warnings is how many warnings the run published. The warnings themselves
	// arrived as [Warning] events, so the summary states the count rather than
	// repeating the text.
	Warnings int
	// Skips are discovery's suppressions, aggregated per reason and sorted by
	// it.
	Skips []SkipCount
}

// clone returns a copy that shares no slice with the receiver.
func (s RunSummary) clone() RunSummary {
	s.Notable = slices.Clone(s.Notable)
	s.Skips = slices.Clone(s.Skips)
	return s
}

// RunCompleted is the last event of every run, on every path.
type RunCompleted struct {
	// Status is how the run ended.
	Status Status
	// Summary is a one-line closing statement: what stopped the run when
	// something did, and empty when [RunCompleted.Run] carries the real answer.
	Summary string
	// Run is the closing summary block, or nil when the run stopped before it
	// had measured anything worth summarising. It is non-nil exactly when a
	// report was published, which is the same boundary [ReportPublished] marks.
	Run *RunSummary
}

func (RunPlanned) event()        {}
func (PhaseChanged) event()      {}
func (PhaseCompleted) event()    {}
func (Traced) event()            {}
func (BaselineProgress) event()  {}
func (BaselineCompleted) event() {}
func (MemoryDerived) event()     {}
func (Discovered) event()        {}
func (Validated) event()         {}
func (SelectionNarrowed) event() {}
func (CoverageMapped) event()    {}
func (MutantStarted) event()     {}
func (MutantFinished) event()    {}
func (CacheHit) event()          {}
func (DirectoryKept) event()     {}
func (Warning) event()           {}
func (ReportPublished) event()   {}
func (RunCompleted) event()      {}

// clone returns a copy that shares no slice with the receiver, so that a
// renderer holding the event cannot observe the engine reusing its buffer.
func (e BaselineCompleted) clone() BaselineCompleted {
	e.Runs = slices.Clone(e.Runs)
	return e
}
