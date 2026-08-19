// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"slices"
	"time"
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
	// PhaseDiscover locates the toolchain, copies the workspace, and — once
	// discovery lands — builds the mutant catalogue.
	PhaseDiscover Phase = "discover"
	// PhaseBaseline builds the snapshot and measures the unmutated tests.
	PhaseBaseline Phase = "baseline"
	// PhaseMutate instruments the snapshot and executes the mutants. It is
	// declared but not yet entered; see the package documentation.
	PhaseMutate Phase = "mutate"
	// PhaseReport writes the run report. It is declared but not yet entered.
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
	// decided from the outcomes by internal/mutation, not here.
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

// An Event is one thing the engine has to say while a run is in flight.
//
// The interface is sealed: the marker method is unexported, so every event is
// one of the types in this file and a renderer's type switch over them is
// exhaustive by construction. Adding a case is a change to this file, which is
// where the reviewers who care about the contract are looking.
//
// Events are values and are safe to keep: nothing the engine sends is mutated
// afterwards, and the one slice-valued field is cloned before it is published.
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
// phase, on entry, so the [Detail] describes what is about to happen rather
// than what just did.
type PhaseChanged struct {
	// Phase is the phase being entered.
	Phase Phase
	// Detail is a one-line human description of the work, composed by the
	// engine. Renderers print it verbatim: only the engine knows how many
	// baseline runs were configured or which test command will be run, and a
	// renderer that guessed would be wrong the first time a default changed.
	Detail string
}

// BaselineProgress reports one completed baseline run.
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

// Warning reports something the user should know that did not stop the run.
//
// Every warning carries a stable GOM#### code for the same reason every error
// does: a message can be reworded, and a code is what somebody searches for.
type Warning struct {
	// Code is the stable GOM#### identifier.
	Code string
	// Message is a one-line explanation that does not repeat the code.
	Message string
}

// ReportPublished reports that a report artefact is on disk and complete.
//
// It is emitted only after the atomic rename, never before: a path that has
// been announced has to be a path that can be opened. Nothing emits it yet —
// the reporting phase arrives later — and it is declared now so that renderers
// written against this contract do not have to change shape when it does.
type ReportPublished struct {
	// Format is the artefact's format, matching a config.ReportFormat value.
	Format string
	// Path is the absolute path of the published file.
	Path string
	// Bytes is the file's size.
	Bytes int64
}

// RunCompleted is the last event of every run, on every path.
type RunCompleted struct {
	// Status is how the run ended.
	Status Status
	// Summary is a one-line closing statement: what was accomplished when the
	// run succeeded, and what stopped it when it did not.
	Summary string
}

func (RunPlanned) event()        {}
func (PhaseChanged) event()      {}
func (BaselineProgress) event()  {}
func (BaselineCompleted) event() {}
func (Warning) event()           {}
func (ReportPublished) event()   {}
func (RunCompleted) event()      {}

// clone returns a copy that shares no slice with the receiver, so that a
// renderer holding the event cannot observe the engine reusing its buffer.
func (e BaselineCompleted) clone() BaselineCompleted {
	e.Runs = slices.Clone(e.Runs)
	return e
}
