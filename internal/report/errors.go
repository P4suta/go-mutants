// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"errors"
	"slices"
	"strings"
)

// A Code is a stable, user-facing diagnostic code. Codes are part of the
// command line interface: they are printed, searched for, and quoted in bug
// reports, so a code is allocated once and never reused for a different
// meaning.
//
// This package owns the GOM51xx block. GOM5001 through GOM5009 belong to
// internal/schemas, which checks the documents this package writes; the two
// halves of the GOM50xx schema-and-report range are deliberately kept apart so
// that "the report could not be assembled" and "the report does not match its
// own schema" can never be confused for one another.
//
// It also owns the GOM78xx block, which is sharding: the `--shard K/N`
// specification, the `shard` block of the document, and every way `report
// merge` can refuse a set of shard reports. They are a block of their own
// rather than more GOM51xx numbers because they are one feature with one
// remedy — look at how the run was split — and a user reading a refusal should
// see at a glance that all of it is about sharding. They live in this package
// because the document block and the merge are both this package's business,
// and splitting a feature across two packages to keep one number range tidy
// would be the tail wagging the dog.
type Code string

// The report diagnostic codes.
const (
	// CodeInvalidRunID reports a run id that is not a run id. The id names the
	// history file, so a value with a path separator or a Windows device name
	// in it is a path bug waiting to happen and is refused here rather than
	// handed to the filesystem.
	CodeInvalidRunID Code = "GOM5101"

	// CodeInvalidStatus reports a status that is not one of the three a run can
	// end in. There is no default: guessing "completed" for a run that failed
	// would put a lie at the top of the document.
	CodeInvalidStatus Code = "GOM5102"

	// CodeInvalidTimestamps reports a missing or backwards run clock. A report
	// whose finish precedes its start cannot have its duration believed.
	CodeInvalidTimestamps Code = "GOM5103"

	// CodeInvalidWorkspaceDigest reports a workspace digest that is not 64
	// lowercase hex characters. It is checked early and hard because it is what
	// names the history directory: a digest that is not a digest would put one
	// run's history under a directory named after nothing.
	CodeInvalidWorkspaceDigest Code = "GOM5104"

	// CodeInvalidSelection reports a selection that does not describe the
	// catalogue: an unknown mode, or more mutants selected than there are.
	CodeInvalidSelection Code = "GOM5105"

	// CodeInvalidTestCommand reports a report with no test command in it. The
	// command is what the whole run means — "these mutants survived" is a
	// statement about one argv vector — and a document that omits it says
	// nothing checkable.
	CodeInvalidTestCommand Code = "GOM5106"

	// CodeNoReport reports a history write with no report to write. It is a
	// caller's slip rather than a user's, and it is returned rather than
	// dereferenced so that the slip is diagnosable instead of a nil panic
	// inside the last step of a run.
	CodeNoReport Code = "GOM5107"

	// CodeInvalidCoverage reports a coverage block that does not describe the
	// mutants underneath it: an unknown mode, a negative binary count, or a
	// mutant marked uncovered that the run nonetheless has a measurement for.
	// The last is the one worth having: `uncovered` means "not executed", so a
	// killed or retried mutant carrying it is a document claiming a detection
	// nothing performed.
	CodeInvalidCoverage Code = "GOM5108"

	// CodeInvalidCache reports a cache block that does not describe the mutants
	// underneath it: an unknown mode, a negative or impossible counter, or a
	// mutant marked cached that the block says could not have been. The last is
	// the one worth having: a run with the cache off that reports a reused
	// outcome, or a cached mutant that is also uncovered, would be a document
	// contradicting itself about where a verdict came from.
	CodeInvalidCache Code = "GOM5109"

	// CodeNoCatalog reports a build with no catalogue at all. Every run has one,
	// even an empty one, and a nil catalogue means the caller lost it rather
	// than that the run found nothing.
	CodeNoCatalog Code = "GOM5110"

	// CodeUnknownMutant reports a result or a rejection naming an id that is not
	// in the catalogue. It is the symptom of two phases disagreeing about which
	// catalogue they are working on, which is exactly the disagreement a report
	// must never paper over.
	CodeUnknownMutant Code = "GOM5111"

	// CodeDuplicateEntry reports one mutant claimed twice: two results, two
	// rejections, or a result and a rejection for the same id.
	CodeDuplicateEntry Code = "GOM5112"

	// CodeMissingResult reports a catalogued mutant that was neither rejected
	// nor given a result. See [Options.Results] for why silence is not treated
	// as "not run": the tally counts what it is told, and a forgotten mutant
	// would quietly leave the denominator.
	CodeMissingResult Code = "GOM5113"

	// CodeMissingLocation reports a catalogued mutant that discovery never
	// reported coordinates for. Reported rather than papered over with a zero
	// line number, which would be a coordinate pointing at nothing.
	CodeMissingLocation Code = "GOM5114"

	// CodeInvalidOutcome reports an outcome that is not one of the six, in
	// either spelling.
	CodeInvalidOutcome Code = "GOM5115"

	// CodeEncodeFailed reports a report that could not be encoded as JSON.
	CodeEncodeFailed Code = "GOM5120"

	// CodeCacheUnavailable reports an operating system that will not say where
	// its cache directory is, so there is nowhere to keep run history.
	CodeCacheUnavailable Code = "GOM5130"

	// CodeHistoryDirectory reports a history directory that could not be created
	// or read.
	CodeHistoryDirectory Code = "GOM5131"

	// CodeHistoryWrite reports a history file that could not be written or moved
	// into place.
	CodeHistoryWrite Code = "GOM5132"

	// CodeForeignWorkspace reports a history directory that belongs to
	// something else: it holds no go-mutants marker, or one naming a different
	// workspace. See [History] for why a shared cache root is worth being
	// paranoid about.
	CodeForeignWorkspace Code = "GOM5133"

	// CodeInvalidNotRunReason reports a result whose not-run reason and outcome
	// contradict each other: a measured mutant that says why it was not
	// measured, or a not-run mutant that does not. The pairing is what makes
	// `not_run_reason` worth reading at all, so it is checked rather than
	// trusted; see [NotRunReason].
	CodeInvalidNotRunReason Code = "GOM5116"
	// CodeMalformedDocument reports a file that cannot be read back as a run
	// report: not JSON, the wrong document type, a schema version this build
	// does not know, or a field nothing here declares. It is separate from
	// internal/schemas' GOM5003, which is a document that parsed and then failed
	// the published schema: this one never got that far.
	CodeMalformedDocument Code = "GOM5117"
)

// The sharding diagnostic codes, which are the GOM78xx block this package also
// owns. See [Code].
const (
	// CodeInvalidShardSpec reports a `--shard` value that is not a shard
	// specification: not `K/N`, not two numbers, or a K outside 1..N.
	CodeInvalidShardSpec Code = "GOM7801"
	// CodeInvalidShard reports a shard block a document cannot state — an index
	// above its own total, or an assignment function this build does not
	// implement. The assignment is checked because it is a promise to a
	// consumer that the partition can be recomputed, and a document naming a
	// function nobody has is a promise nobody can keep.
	CodeInvalidShard Code = "GOM7802"

	// CodeNoShardReports reports a merge that was given nothing to merge.
	CodeNoShardReports Code = "GOM7810"
	// CodeNotAShardReport reports a document handed to `report merge` that no
	// shard wrote. Merging an unsharded run into anything is meaningless: it
	// already is the whole run.
	CodeNotAShardReport Code = "GOM7811"
	// CodeIncongruentShards reports two documents that do not describe one run:
	// different tool versions, workspace digests, shard totals, assignment
	// functions, changed refs, or catalogues. Merging them would produce a
	// document whose numbers describe no run that ever happened, so the first
	// discrepancy is named and nothing is written.
	CodeIncongruentShards Code = "GOM7812"
	// CodeIncompleteShardSet reports a set of documents that is not every shard
	// exactly once: an index missing, or one supplied twice. A merge of a
	// subset would silently report the mutants of the missing shards as not-run
	// and flatter the score.
	CodeIncompleteShardSet Code = "GOM7813"
	// CodeShardOwnershipMismatch reports a shard whose rows disagree with the
	// assignment function: it measured a mutant belonging to another shard, or
	// disclaimed one of its own. Either way two shards' rows would collide or a
	// mutant would fall through the gap between them.
	CodeShardOwnershipMismatch Code = "GOM7814"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the GOM51xx block.
var codes = []Code{
	CodeInvalidRunID,
	CodeInvalidStatus,
	CodeInvalidTimestamps,
	CodeInvalidWorkspaceDigest,
	CodeInvalidSelection,
	CodeInvalidTestCommand,
	CodeNoReport,
	CodeInvalidCoverage,
	CodeInvalidCache,
	CodeNoCatalog,
	CodeUnknownMutant,
	CodeDuplicateEntry,
	CodeMissingResult,
	CodeMissingLocation,
	CodeInvalidOutcome,
	CodeInvalidNotRunReason,
	CodeMalformedDocument,
	CodeEncodeFailed,
	CodeCacheUnavailable,
	CodeHistoryDirectory,
	CodeHistoryWrite,
	CodeForeignWorkspace,
	CodeInvalidShardSpec,
	CodeInvalidShard,
	CodeNoShardReports,
	CodeNotAShardReport,
	CodeIncongruentShards,
	CodeIncompleteShardSet,
	CodeShardOwnershipMismatch,
}

// Codes returns every diagnostic code this package can report, in numeric
// order, so that `doctor` can print the table without reading the source.
func Codes() []Code { return slices.Clone(codes) }

// An Error is one reporting failure carrying a stable [Code].
//
// It mirrors the shape internal/discover, internal/engine, internal/gocmd and
// internal/instrument use — code, one-line message, optional cause — so a
// single renderer can lay them all out the same way, without the packages
// sharing an error identity.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// Message states the problem in one line, without the code.
	Message string
	// Err is the underlying cause, or nil. It stays reachable through
	// errors.Is and errors.As.
	Err error
}

// Error renders "GOM5132: <message>", with the cause appended when there is
// one.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the [Code] carried by err, or the empty Code if err did not
// come from this package.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
