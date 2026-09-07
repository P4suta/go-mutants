<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0010 — Narrowing to tests reaches the same verdicts as running the binary

## Status

Accepted, 2026-09-08. Implemented by `internal/execute`'s per-test profiling
(`CollectTestCoverage`) and test selection (`MutantRun.Tests`), by
`internal/coverage`'s `MapTests`, and by `internal/engine`'s `testCoveragePhase`
and its set verifier. Selected by `test.narrowing = "test"`, the default.
[ADR 0001](0001-trace-is-not-evidence.md) draws the line this record depends on:
coverage is an optimisation, so a pass that cannot run it fails open rather than
failing the run.

## Context

Coverage-guided selection has always answered *which test binaries reach this
line* and let a run skip the binaries that reach none. On a package whose one
binary is its whole suite — which is every package — that answer never narrows
anything: every mutant of `internal/discover` runs all of `internal/discover`'s
tests, and its unit tier is about five seconds, so its roughly fifteen hundred
mutants are half a day of wall-clock and have never fitted in the dogfood gate.

The finer question is *which tests reach this line*. A test binary can be asked
for one test with `-test.run`, and profiled one test at a time, so a mutant can
be measured against only the tests whose own coverage reaches it. The gain is
large — ten to fifty times on a package with many tests — but the change is not
free of meaning: running fewer tests against a mutant could, done carelessly,
report a mutant killed that the suite does not actually catch, or survived that
it does. A verdict is the product, and a faster wrong verdict is worse than a
slow right one.

This record is the argument that the faster verdict is the same verdict.

## Decision

`test.narrowing = "test"` runs each mutant against the tests whose coverage
reaches its lines, and it reaches the same verdicts a run against the whole
binaries would. Five things make that true, and each is enforced rather than
assumed.

1. **A survivor under the tests is a survivor under the binary.** Coverage is
   over-approximate in the safe direction: a test that does not execute a
   mutant's line cannot observe the edit, so it can only pass. If every test
   whose coverage reaches a mutant passes, every *other* test would pass too,
   and the mutant survives the whole binary exactly as it survives the subset.
   The line-only over-approximation `internal/coverage` already makes — a block
   counts as reaching a mutant when it covers the line, whether or not the
   mutated expression was evaluated — only ever adds tests to a mutant's set, so
   it too errs towards running more, never fewer.

2. **A kill under the tests is confirmed against a control.** A set of tests
   that each pass on their own can still fail when run *together* without any
   mutant — one leaves state another depends on — and a mutant narrowed to such
   a set would be reported killed by a failure that is not its. So before any
   mutant runs, every distinct set of tests some mutant is narrowed to is run
   once with no mutant active: the *control*. A set whose control passes is
   trusted; a set whose control fails, times out or cannot be started is not,
   and every mutant it would have narrowed is measured against its whole
   covering binaries instead. The control is the difference between narrowing a
   run and misreporting one.

3. **A test that fails on its own is not used to narrow.** The per-test profile
   is taken by running the test alone, and a test that does not pass alone
   profiles nothing trustworthy: it is order-dependent, or was never green. Such
   a test is left out of the narrowing and named in a warning
   (`GOM7603`), and its binary is treated as a whole — every mutant that binary
   reaches runs against all of it — so a mutant reached only by an
   order-dependent test is killed against the binary rather than lost. This is
   the *dirty binary* rule, and it is why the run still collects the whole-binary
   profile of any binary with such a test.

4. **The result does not depend on the mode, so the cache does not key on it.**
   A mutant's outcome is the same whether it was reached through its tests or
   through its binaries, by the four points above, so a `test` run and a
   `package` run of the same workspace produce the same outcome for the same
   mutant. The outcome cache therefore does not include the narrowing in its
   key: a warm run may answer a mutant from an outcome the other mode proved.

5. **The bound is the suite's, which is generous for a subset.** A mutant's
   timeout is derived from the *whole* baseline suite, and a subset of that
   suite runs in no more time than the whole, so a narrowed run is measured
   under a bound that is if anything too loose — never one too tight to
   distinguish a slow test from a hang.

The report says which mode ran (`coverage.mode` is `off`, `package` or `test`),
how many tests were profiled (`coverage.tests`), which tests reach each mutant
(`mutants[].covering_tests`), and which tests each pass was narrowed to
(`mutants[].executions[].tests`). `covering_test_packages` stays what it was in
every mode, so a consumer that folds tests back to binaries and one that never
learned about tests read the same list.

## Consequences

The dogfood gate is the standing proof of point (1) through (4): go-mutants
mutating itself under `test` narrowing reaches the same score it reaches under
`package`, because the outcomes are the same, and the run is faster because
most of the binary is not run for most mutants. A change that made narrowing
lose a kill would move that score, and the gate's floor would catch it.

The cost is real and paid up front: one `-test.list` per binary, one profiling
run per test, and one control per distinct set of tests. On a suite where every
test passes alone and no set fails together — the ordinary case — that is about
one extra run of the suite, paid once, against a saving of most of the suite per
mutant. On a suite full of order-dependent tests the dirty-binary rule collapses
`test` back towards `package`, which is the honest outcome: a suite that cannot
run its tests in isolation cannot be narrowed to them, and says so.

`package` remains available for a project that wants the coarser mapping, and a
custom `test.command` still turns coverage off entirely, as
[the configuration reference](../configuration.md) describes. Neither is a way
to exclude a target from measurement: every mode measures every mutant, and the
only difference is how much of the suite each mutant is measured against.
