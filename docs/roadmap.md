<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Roadmap

**Status: every row below is work, and every row states what finishing it looks
like.** `internal/testkit/roadmap_test.go` keeps the reserved skip reasons on
this page equal to the ones the run-report schema reserves and no build emits —
so a row that lands is a test failure telling you to delete it, rather than a
page that quietly describes something already done.

This page is the counterpart to [Limitations](limitations.md), and the
difference between them is the whole point. **A limitation is a decision.** A
roadmap entry is work nobody has done. A boundary that turns out to be a fact
about Go rather than about go-mutants moves from here to there and never comes
back — `struct-tag` did.

Every **Done when** is an observation somebody can make, not a feeling. A row
whose Done when reads "it works well" is a row that cannot be finished.

## The engine

| # | What | Done when |
| ---: | --- | --- |
| 1 | Probe forms beyond the return-value one. `internal/instrument`'s dispatch returns nil for a boolean site, an arithmetic operand and a deleted statement, and its own comment calls that "the dispatch point for every form still to come" | Each form has a golden and a compile test, and a deleted statement's log records reachability with the honest note that infection cannot be observed where there is no value |
| 2 | Probing in the `run` pipeline. `Session.Probe` exists and `internal/engine` has no reference to it, so a run pays for executions a probe could have proven unnecessary | A run with probing on reaches the same verdict for every mutant as one with it off, and the work ceiling records fewer `mutant-run` children |
| 3 | Multi-module `go.work`. Refused today with `GOM4102` | `fixtures/workspace` measures both modules from its root, and the mutant identities of a module measured alone and measured in the workspace are told apart deliberately rather than by accident |
| 4 | `--isolate`, the per-worker snapshot copy. Reserved in [the architecture](architecture.md) and absent from the command line. It is the escape hatch for a suite that legitimately writes into its own package directory, which today cannot run at all | `fixtures/selfwriting` completes with a real tally under `--isolate`, a worker's copy is restored between mutants, and the drift gate still stops the same run without it |

## Documents

| # | What | Done when |
| ---: | --- | --- |
| 5 | `explain --json`. Refused today on the argument that everything it prints is already in the report and the recording — which is true of the facts and not of the joins: the reproduction command, the rebuild line, this mutant's share of each stage, and the preserved output tail exist in neither document | A `go-mutants/explain` document answers a published schema, the prose and the JSON come from one gatherer, and the `reproduce.command` in it is pasted and run by a test that asserts it reproduces the verdict |
| 6 | A `Remedy()` on every diagnostic code, so that [`docs/errors.md`](errors.md)'s third column is pinned verbatim rather than by shape. 213 constants across sixteen packages | `TestEveryDiagnosticCodeRowSaysWhatItMeansAndWhatToDo` compares the column with the method rather than checking that the cell is non-empty |

## Reserved and unemitted

The run-report schema's `reason` enumeration is deliberately a superset of the
reasons the code declares, so that landing one is a code change and not a schema
change. One name is reserved and unemitted today, and this table is the index of
it — `internal/testkit/roadmap_test.go` compares it with the schema, so a reason
reserved and unlisted fails the build and a reason that lands fails it too,
until its row is deleted.

| Reserved | Where it stands |
| --- | --- |
| `case-label` | **Not work.** What is left under this name is the label of a *type* switch case, which holds a type rather than a value. No rule in the registry rewrites a type, so nothing is ever proposed at one and there is nothing to decline — the same silence a `fallthrough` gets. A *tagged* switch's labels are ordinary expressions and are mutated through Form E; a *tagless* switch's are exactly `bool` and are mutated through Form C |
| `struct-tag` | **Not work.** A tag is part of a type, and no guard form can select between two types at run time. It is a [limitation](limitations.md#boundaries-that-are-facts-about-go-rather-than-about-go-mutants), and it is in this table only to say that it will never leave it |

`label-or-goto` was the third, and it left this table the way the paragraph
above describes: a family landed, the reason became a real one that `goto`
emits, and the row failed until it was deleted. `case-label` arrived in it from
the other direction — it used to be emitted and is not any more, because the
positions it covered became sites.
