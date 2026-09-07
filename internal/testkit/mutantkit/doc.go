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
//   - [FakeGo], [Main]: a `go` command the test writes the answers for.
//
// # The scripted `go`
//
// [Toolchain] locates the machine's real toolchain, which is what a test that
// wants to know whether a mutant is really killed needs. [FakeGo] is the other
// half, for the questions that are about what go-mutants does when the toolchain
// misbehaves:
//
//	func TestMain(m *testing.M) { os.Exit(mutantkit.Main(m)) }
//
//	f := mutantkit.FakeGo(t)
//	f.Version("1.99.0")                       // `go version`
//	f.On("test", "-c").Stderr(diags).Exit(2)  // a compile that fails
//	tc, err := gocmd.Locate(gocmd.Options{Explicit: f.Bin(), Env: f.Env(base)})
//
// It exists because those questions had nowhere to live. A version probe that
// hangs, one that answers garbage, a `go list` that refuses a pattern, a
// baseline suite that is red — every one of them is a failure go-mutants exists
// to report clearly, and every one of them was either tested in the integration
// tier at a cost of minutes and a toolchain, or not tested at all, because a
// `go` that hangs is not something anybody can install. Scripted, they cost one
// process each and run on a machine with no Go on it.
//
// The second thing it buys is an assertion the unit tier could not make at all:
// [Fake.Calls] is what a call site really composed — the argv a phase issued,
// the directory it issued it from, the GOFLAGS it merged, whether an activation
// was stripped — read off a log a real child wrote rather than off an injected
// function. That is how "the compile carries `-vet=off` and the listing does
// not" stopped being a claim about a struct and became a claim about a process.
//
// Three rules make it trustworthy, and [FakeGo] states them in full: every call
// is recorded before it is answered, so a refused one is in the log too; a call
// no rule matches is refused with [FakeGoNoRule] and a message naming the argv,
// never answered with a silent success; and the values the log keeps are only
// the ones production is supposed to set — flag lists and filesystem paths — so
// a developer's own environment never travels into a CI artifact.
//
// A scripted `go test -c -o X` produces the fake again rather than an inert
// file, which is what carries it past the build: internal/execute's scheduler
// starts that binary directly, so it answers the same table and a mutant can be
// scripted killed or survived by exit status. What is still out of reach is a
// whole run past the baseline, because discovery type-checks the module with
// go/packages and that needs real source and a real toolchain.
//
// The binary is installed once per test binary rather than once per fake — it
// is a link to the test binary, and a unit-tier run builds twenty-eight of them
// — and [Main] removes the shared directory when the suite ends. [Fake.Export],
// the PATH form, forces [testkit.ResolveToolchainDirectories] before it changes
// anything, because after it every `go` this process starts is the fake, the
// harness's own environment probe included.
//
// The package's TestMain has to dispatch, because the fake *is* the test binary
// re-executed. [Main] is that hook, [IsFakeGo] composes it with a TestMain that
// has something of its own to do, and a binary started as `go` with the switch
// unset is refused rather than left to run its own suite in place of the go
// command.
//
// Every constructor logs the line the harness above it logs — `mutantkit:
// toolchain=… fixture=… scratch=…` — because a test that fails in CI on a
// machine nobody can reach is diagnosed from its log, and the questions asked
// first are always which fixture, which toolchain, and which directory.
package mutantkit
