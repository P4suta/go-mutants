// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package report is the RunReport v1 document: the lossless record of one
// mutation run, and the store it is kept in.
//
// One function assembles it and one stores it:
//
//	r, err := report.Build(report.Options{ /* what the run learned */ })
//	runPath, latestPath, err := report.WriteHistory(r)
//
// # Why this document is the source of truth
//
// Everything else go-mutants says about a run is derived from this document.
// The console summary, the HTML report, the Stryker projection, `report merge`,
// and the exit code are all views of it, which is only safe if the document
// contains everything they need — so it is lossless by construction: every
// catalogued mutant appears exactly once, either in `mutants[]` with what
// happened to it or in `rejected[]` with the compiler's reason it could not
// exist, and nothing that was measured is summarised away.
//
// The one place losslessness is not obvious is the score. `summary.survived`
// counts every survivor, while the score's denominator counts only the
// unexpected ones, and the split is recovered by joining `expectations[]`: a
// survivor with a `fulfilled` expectation is an expected survivor, and
//
//	score_percent = (killed + timed_out) / (killed + timed_out + survived - fulfilled)
//
// with `null` in place of a percentage when that denominator is zero.
// [Report.Tally] performs exactly that join, which is how the exit decision is
// made from the document rather than beside it.
//
// # What this package decides, and what it only records
//
// It decides two things. The expectations ledger is evaluated here — see
// [StateOf] for the state machine and why "the tests caught it" and "we never
// looked" share one document value while never sharing one decision — and the
// policy verdict is computed here, from this report's own tally, so that the
// number a user reads and the gate that failed cannot disagree.
//
// Everything else is recorded rather than judged. The counting is
// [mutation.Tally]'s, the score is [mutation.Score]'s, the exit rules are
// [mutation.Decide]'s, and the catalogue order is [mutation.Catalog]'s. A
// second implementation of any of them is how a report and a console end up
// disagreeing about a number that is printed twice on one screen.
//
// # Determinism
//
// Two runs over one workspace that reach the same outcomes produce
// byte-identical documents, apart from the run id, the clock, and the
// durations. Field order is struct order, `mutants[]` and `rejected[]` are a
// partition of the catalogue in catalogue order, `skips[]` is sorted by (path,
// reason), `expectations[]` keeps ledger order and `warnings[]` publication
// order — and no map is ever iterated into an array. That is what makes two
// reports diffable, and what lets `report merge` prove that n shards saw one
// catalogue.
//
// # The schema is checked in the tests, not at run time
//
// schema/run-report-v1.schema.json is the published contract, and the package
// tests validate every document this package produces against it through
// internal/schemas. The validator is deliberately not linked into the shipped
// binary: it would be dead weight in every run, and a schema violation is a bug
// in this repository that a test must catch before a release, not a run-time
// condition to recover from. [DocumentType] and [SchemaVersion] are spelled out
// here for the same reason, with a test asserting they equal the constants
// internal/schemas registers.
//
// # The engine's side of the contract
//
// [Options.Results] must hold one result per catalogued mutant that was not
// rejected, including an explicit not-run result for everything the run did not
// execute. That is [mutation.Tally]'s documented contract, and [Build] enforces
// it: a mutant nobody accounted for would leave the score's denominator
// silently, which is the one way a mutation score can flatter a test suite.
// internal/execute's own MutantResult maps onto [MutantResult] field for field,
// with `Final` as the outcome and `len(Attempts)` as the attempt count.
package report
