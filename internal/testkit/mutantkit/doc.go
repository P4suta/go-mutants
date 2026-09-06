// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package mutantkit is the half of the test harness that knows what go-mutants
// is: snapshots, catalogues, mutants, run reports and the toolchain that builds
// them.
//
// # Why it is a package of its own
//
// [github.com/P4suta/go-mutants/internal/testkit] imports nothing from this
// module, and a test enforces it. That rule is what lets the tests of the pure
// packages — internal/mutation, internal/interval, internal/glob — use the
// harness without linking internal/snapshot, go/packages and the engine behind
// it, and it is what keeps the harness from being rebuilt by a change to the
// code it is used to test. Helpers that do need those types cannot live there,
// so they live here.
//
// The split is the same one goatest made for the same reason, and the cost of
// getting it wrong is not abstract: a harness compiled from the code under test
// is a harness whose bug and the bug it is hiding are the same bug.
//
// # Only external test packages may import it
//
// A file here imports the engine, and an engine package that imported it back
// would be a cycle — and, worse, a production package linking `testing`. So the
// import gate treats this tree as the harness: nothing outside
// `internal/testkit/` may import it from a non-test file, and inside a package
// it is always `package foo_test`.
//
// # What is here
//
// One helper per thing an integration suite in this repository does by hand
// today, each with the assertion the hand-written copies disagreed about:
//
//   - [Toolchain], [RunGo], [RunSuite], [RequireExit], [RequireOutput],
//     [Activate]: locating `go`, running it in a snapshot with a composed
//     environment, and quoting the child's output on every mismatch.
//   - [Snapshot], [SnapshotOf]: a corpus module copied into a directory of the
//     test's own, aged past cmd/go's index cutoff, with its removal registered
//     before anything else can fail.
//   - [Discover], [Catalog], [Hints], [Instrument]: the four-step sequence from
//     a snapshot to an instrumented tree, which four suites each wrote out.
//   - [MutantAt], [ByRule], [APIMutantAt], [APIByRule]: naming one mutant by the
//     rule that produced it, asserting there is exactly one.
//   - [MustMarshal], [DecodeJSON], [EncodeJSON], [NormalizeRunReport]: a report
//     as bytes that validate, as a tree a test can edit, and as a document whose
//     varying fields have been replaced so that two runs can be compared.
//
// Every constructor logs the line the harness above it logs — `mutantkit:
// toolchain=… fixture=… scratch=…` — because a test that fails in CI on a
// machine nobody can reach is diagnosed from its log, and the questions asked
// first are always which fixture, which toolchain, and which directory.
package mutantkit
