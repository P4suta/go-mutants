<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Architecture decision records

A decision gets a record here when it constrains code that has not been written
yet — when the interesting part is not what was built but what may not be built
on top of it. Everything else belongs in a package doc, beside the code it
explains.

An ADR says what the situation was, what was decided, and what that costs. It is
not revised when the code changes: a record whose consequences no longer hold is
superseded by a new one, so that reading them in order is reading the history of
the design rather than a snapshot of it.

| ADR | Decision |
| --- | --- |
| [0001](0001-trace-is-not-evidence.md) | A trace is the diagnostic account of a run and is never evidence: it takes no part in a verdict, an identity or a cache key, and a recording that cannot be written costs a note rather than the run |
| [0002](0002-every-subprocess-is-recorded-at-the-runner.md) | Every subprocess is recorded where it is started, at the one choke point: every `runner.Spec` names a `Kind`, the recorder is a nil-safe field rather than a global, and an unlabelled command is refused by the schema rather than by the runner |
| [0003](0003-diagnostics-live-in-the-report-directory.md) | A failed run's diagnostics are written into the report directory — the one in-tree place the snapshot never reads — under the run's own id, never into a temporary one, and a bundle that cannot be written costs a warning rather than the verdict |
| [0004](0004-verbosity-renders-the-event-stream.md) | Verbosity is a rendering of the events the run already records rather than a second set of print statements: nothing prints what it did not record, and a run that asked for no verbosity pays nothing |
| [0005](0005-the-test-harness-owns-its-temporaries.md) | The test harness owns a build cache and a kept-scratch root outside any temporary directory, says so with a marker file, and one tool collects them; keeping is opt-in locally and on in CI, and nothing sweeps a package directory |
| [0006](0006-selection-is-advisory.md) | A selection narrows what a caller intends to execute and nothing else: it is applied after discovery, `Session.Exec` still runs an unselected mutant, and it takes no part in `Catalog.PreparedDigest` |
| [0007](0007-commands-overlap-preparation.md) | A workspace command runs beside a preparation and waits only for its instrumentation window, so no consumer needs a second workspace for control work; the price is that every file discovery read is checked against the frozen manifest, and that a window which fails publishes the failure before it unlocks |
| [0008](0008-the-unit-tier-scripts-the-toolchain.md) | The unit tier scripts the `go` command rather than needing one installed: the test binary re-executes as `go` and answers from a rule table, an unscripted call is refused, only nine environment values are logged, and a file that scripts a toolchain is not driving one |
| [0009](0009-a-mutant-is-bounded-in-memory-as-in-time.md) | A mutant is bounded in memory the way it is bounded in time: `max(1 GiB, largest baseline peak × 4)`, enforced by sampling the process tree at the one place go-mutants starts processes, with the baseline itself unbounded because it is what the bound is derived from; a mutant the bound stops is `killed` with `memory_exceeded` beside it rather than a new outcome, and a platform that cannot enforce one says so instead of pretending |
| [0010](0010-narrowing-to-tests-is-sound.md) | Narrowing a mutant to the tests whose coverage reaches it reaches the same verdicts as running its binaries: a survivor under the tests is a survivor under the binary, a kill is confirmed against a control of the same tests with no mutant, a test that fails alone is not used to narrow and its binary runs whole, and because the outcome does not depend on the mode the cache does not key on it |
| [0011](0011-an-unobservable-mutant-need-not-be-executed.md) | A mutant no covering test binary could observe need not be executed: a probe tree runs the original program and records, per mutant, whether each binary could rule it out, and a mutant every covering binary ruled out is a survivor the run reports without starting a process. The licence is "this pass could not rule it out" rather than "the value differed", which is what admits a form for a site with no value; a binary that established nothing is never read as one that saw nothing; and because a probe pass is the whole binary it proves ADR 0010's confirmation unnecessary rather than skipping it |
| [0012](0012-a-workspace-is-one-run-of-many-modules.md) | A `go.work` at the root is measured as one run over one catalogue that spans its modules, not as N runs that share a directory: a mutant carries its module path as a tenth identity field under a second domain, every module's generated runtime holds the whole catalogue so a sibling module's tests can kill it, the one workspace file the tree being built carries is obeyed and no other, and the split happens in the reporting -- one run report per module plus a workspace report -- rather than in the measurement |
| [0013](0013-a-mutant-that-does-not-return-is-decided-by-work.md) | A mutant that does not return is decided by the work it does rather than by the clock: every loop of an instrumented file carries a local counter, the ceiling is what the original program did under the same suite as the instrumented baseline measured it, and exceeding it is a detection that needs no second measurement because it is the same on every machine. The stopwatch stays as the backstop for what counting cannot see -- a mutant that blocks, a loop in code the run did not instrument, a subprocess that hangs -- and stops being what decides the ordinary case |

The index is checked: `internal/testkit`'s `TestADRIndexListsEveryADRFile` fails
when a record here links to nothing or a record exists that this table does not
link to.

For the contracts these decisions produced, see
[the JSON contracts](../json-schema.md) and
[the trace format](../trace-v1.md); for how the pieces fit together, see
[the architecture](../architecture.md). For how to work on any of it, see
[the development guide](../development.md).

## The runner's records

Renumbered by +13 when the two products came into one repository, so that
one sequence covers both: the engine's 167 references include public godoc
and could not move, the runner's 38 were all prose and could. Each record
keeps the number it was accepted under in its own Status line.

| ADR | Decision |
| --- | --- |
| [0014](../../goatest/docs/adr/0014-seam-policy.md) | 0001 — Test seams are arguments, not package-level variables |
| [0015](../../goatest/docs/adr/0015-trace-is-not-evidence.md) | 0002 — A trace is not evidence |
| [0016](../../goatest/docs/adr/0016-no-replay-engine.md) | 0003 — No general record/replay engine |
| [0017](../../goatest/docs/adr/0017-proof-layers-not-budgets.md) | 0004 — Proof layers, not budgets |
| [0018](../../goatest/docs/adr/0018-build-cache-goatest-owns.md) | 0005 — A build cache goatest owns, and what may write to it |
| [0019](../../goatest/docs/adr/0019-every-temporary-directory-has-an-owner.md) | 0006 — Every temporary directory has an owner |
| [0020](../../goatest/docs/adr/0020-survived-evidence-is-universal.md) | 0007 — Survived evidence is a universal proposition over the reaching set |
| [0021](../../goatest/docs/adr/0021-controls-before-timeouts.md) | 0008 — Controls before timeouts |
| [0022](../../goatest/docs/adr/0022-parallel-measurement-serial-commit.md) | 0009 — Parallel measurement, serial commit |
| [0023](../../goatest/docs/adr/0023-whole-suite-reach-before-fallback.md) | 0010 — Whole-suite reach before fallback execution |
| [0024](../../goatest/docs/adr/0024-append-only-checkpoint-journal.md) | 0011 — Append-only checkpoint journal |
| [0025](../../goatest/docs/adr/0025-aggregate-proof-before-timeout.md) | 0012 — Exact compatible-group mutation proofs |
| [0026](../../goatest/docs/adr/0026-preserve-block-routing-across-resume.md) | 0013 — Preserve block routing across resume |
| [0027](../../goatest/docs/adr/0027-resume-complete-probe-phase.md) | 0014 — Resume a complete probe phase |
| [0028](../../goatest/docs/adr/0028-execute-framed-baselines-directly.md) | Execute framed baseline targets directly |
| [0029](../../goatest/docs/adr/0029-publish-every-baseline-control.md) | 0016 — Publish every completed baseline control |
| [0030](../../goatest/docs/adr/0030-project-controls-use-a-native-cache-projection.md) | 0017 — Project controls use a native cache projection |
| [0031](../../goatest/docs/adr/0031-confirm-comparative-watchdogs.md) | 0018 — Fail closed at comparative watchdogs |
| [0032](../../goatest/docs/adr/0032-instrument-the-test-binary-import-closure.md) | 0019 — Instrument the test binary import closure |
| [0033](../../goatest/docs/adr/0033-bootstrap-cold-preparation-with-verified-local-work.md) | 0020 — Bootstrap cold preparation with verified local work |
| [0034](../../goatest/docs/adr/0034-what-a-ledger-cannot-check.md) | 0021 — What a ledger cannot check |
| [0035](0035-one-repository-two-modules.md) | The engine and the runner live in one repository as two modules joined by `go.work`, so that one proof is one pull request rather than a sequence of two with a version pin between them |
| [0036](0036-the-runner-reaches-the-engine-through-its-public-api.md) | The runner's production code imports the engine's published API and never its `internal/`, which Go's path-prefix rule permits and a gate refuses |
| [0037](0037-a-value-carries-its-meaning-in-its-type.md) | A value's meaning belongs in its type; where Go cannot express it a check enforces it, and where a check cannot decide it a reason is required beside the code |
| [0038](../../goatest/docs/adr/0038-a-request-with-no-prior-observation-measures-its-own.md) | A mutant request with no prior clean observation measures its own exact original control rather than going unevaluated, because that control runs under the containment ceiling either way |
