// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package testkit is the one test harness every suite in this repository
// shares: it finds the module, copies a fixture, hands a test a hermetic
// environment, locates the toolchain, runs a child process, and asserts on what
// came back.
//
// # Why one package rather than a helper per suite
//
// Every helper here existed three or four times before it lived here, and the
// copies disagreed. Three packages redirected the user cache directory and all
// three were wrong on macOS, because os.UserCacheDir ignores XDG there — the
// tests read the developer's real `~/Library/Caches/go-mutants` back and
// interfered with each other through it. Two packages pointed the temporary
// directory somewhere private and the third did not, so "the snapshot was
// removed" was an assertion in two suites and a guess in the third. The
// environment a child received was composed in one suite and inherited in
// another, so a developer with GO_MUTANTS_ACTIVE exported in their shell ran a
// mutant as the baseline in half the repository.
//
// None of that is a fact about go-mutants. It is a fact about the operating
// system, the go command, and git, and a copy of such a helper per package is a
// copy of the rule per package — and the rule is what drifts. So there is one
// copy, it is tested, and each rule carries the failure that taught it.
//
// # Production code may never import this package
//
// This is a test-only package: it takes a [testing.TB], it fails tests, and it
// writes into temporary directories. Nothing that ships may import it, and
// nothing here may import anything from github.com/P4suta/go-mutants.
//
// Both halves of that rule are load-bearing. A production package that imported
// testkit would link `testing` into `go-mutants` — flag registrations, the
// testing package's own init work, and a public API that can fail a test that
// does not exist. And testkit importing an engine package would make the
// harness part of the graph it is supposed to observe: a change to
// internal/mutation would rebuild the harness, and a test of that change would
// be written with helpers compiled from the code under test.
// TestProductionCodeDoesNotImportTestkit enforces the first half; the second is
// enforced by the import list of this package holding nothing from this module,
// and outside the standard library only github.com/google/go-cmp — which is
// here because [Golden] prints a diff, and a hand-written diff of two
// multi-line documents is either wrong or is go-cmp again. The list is a test:
// TestTheHarnessImportsNothingFromThisModule names the next third-party import
// rather than letting it in.
//
// Helpers that need go-mutants' own types — a gocmd.Toolchain, a
// snapshot.Snapshot, a mutation.Catalog, a normalised run report — belong in
// internal/testkit/mutantkit, which imports the engine packages freely and is
// imported only from external test packages. That split is what keeps this
// package's own import weight to one small, test-only dependency, so a pure
// package's unit tests can use it without pulling the engine in behind them.
//
// # What the parts are
//
//   - [Root], [Fixture], [FixtureNames] locate the module and its corpus.
//   - [Copy], [CopyTree], [AgeTree], [WriteFile], [WriteSource], [ReadFile],
//     [Entries], [SamePath] build and read trees a test owns.
//   - [NewModule] builds a synthetic module from nothing, a fixture, or both.
//   - [Env], [Compose], [BuildCache] hand a test and its children a hermetic
//     environment.
//   - [GoBinary], [GitBinary], [RequireTools], [GitInit], [GitCommit], [Git]
//     reach the toolchain under a stated skip policy.
//   - [Exec], [Result], [RequireExit], [RequireOutput], [RequireNoOutput] run a
//     child and say what it did.
//   - [Scratch], [KeptDir], [KeepPath], [KeepSection], [PackageScratch],
//     [DumpFiles] decide what a failing test leaves behind.
//
// Every constructor that resolves a fixture, a toolchain, or a scratch directory
// logs one `testkit:` line naming what it resolved and the keep policy in
// force, so a failure in CI says which fixture, which toolchain and which cache
// the run was against without anybody having to reproduce it.
//
// # What a failing test leaves behind
//
// A test that fails takes its evidence with it: the fixture copy, the
// instrumented tree, the composed environment's scratch and the commands it ran
// are all under a t.TempDir the testing package removes the moment the
// assertion has been printed. Reproducing it means running it again, which is
// not available on a runner nobody can log into and is expensive everywhere
// else.
//
// So [Scratch] replaces t.TempDir in every constructor here that hands a test a
// tree to work in, and what happens to that tree at the end is a policy:
//
//	GO_MUTANTS_TEST_KEEP       unset, 0, false,   remove it, exactly as before
//	                           no, n, off,
//	                           disabled
//	                           1, true, yes, y,   keep it when the test failed
//	                           on, enabled,
//	                           failed, on-failure
//	                           always             keep it whatever happened
//	                           anything else      refused, with the spellings
//	                                              named — a typo in a CI job
//	                                              must not read as "off"
//	GO_MUTANTS_TEST_KEEP_DIR   the root a kept directory is filed under;
//	                           <os.UserCacheDir()>/go-mutants-test/kept by
//	                           default, reported by `mise run test-cache-status`
//	                           and emptied by `mise run test-clean`
//	GO_MUTANTS_TEST_VERBOSE=1  print a [DumpFiles] dump on a test that passed
//	GO_MUTANTS_TEST_FORCE_FAIL fail the test of this exact name on purpose,
//	                           which is how to see what a failure leaves
//
// and the two the rest of the harness reads:
//
//	GO_MUTANTS_TEST_GOCACHE        the shared build cache the suites' children use
//	GO_MUTANTS_TEST_REQUIRE_TOOLS  a missing `go` or `git` fails rather than skips
//
// Locally the policy is off, because keeping unconditionally filled a disk
// twice. CI sets it to `1` for every test job and uploads the root as an
// artifact, so a red build arrives with the evidence attached.
//
// A kept directory holds [KeptFileName], which names the test, the fixture, the
// toolchain, the build cache, the test's other kept directories and every child
// the test ran through [Exec]; a `dump/` of whatever [DumpFiles] was pointed at,
// whole, one numbered directory per call; and — for a test that called
// internal/testkit/mutantkit's Trace or TraceSink — the recording, in the
// encoding `trace validate` reads. To see all of it on a test that works:
//
//	GO_MUTANTS_TEST_KEEP=1 GO_MUTANTS_TEST_FORCE_FAIL=TestSomething \
//	    go test -tags integration ./internal/engine -run TestSomething
//
// # What is not collected
//
// Nothing reclaims a kept directory but `mise run test-clean`. Re-running a test
// files a new directory beside the old one rather than replacing it — the
// suffix is random, and two runs of one test are two pieces of evidence — so
// `always` grows for as long as it is left on, and `testcache trim`, which
// enforces a budget on the build cache, never looks at this root. A developer
// who has been keeping things is expected to empty it; nothing here decides
// that somebody else's evidence is stale.
//
// GO_MUTANTS_TEST_KEEP_DIR is also how to put the root somewhere fast. The
// default is under the user cache directory, which is on the machine's disk;
// pointing it at a tmpfs makes every kept directory a memory write and every
// removal free, at the price of losing the evidence with the reboot — which is
// the right trade for a keep-everything run and the wrong one for a nightly job
// whose failures somebody reads in the morning.
package testkit
