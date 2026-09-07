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

The index is checked: `internal/testkit`'s `TestADRIndexListsEveryADRFile` fails
when a record here links to nothing or a record exists that this table does not
link to.

For the contracts these decisions produced, see
[the JSON contracts](../json-schema.md) and
[the trace format](../trace-v1.md); for how the pieces fit together, see
[the architecture](../architecture.md). For how to work on any of it, see
[the development guide](../development.md).
