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

## Operators

| # | What | Done when |
| ---: | --- | --- |
| 1 | **Tagged** `switch` case mutation. The tagless form landed: its labels are exactly `bool`, so Form C expressed them and what was missing was asking the guard chooser rather than suppressing them before it. A tagged label is compared against the tag, and needs the case expression selected at the point it is evaluated | `case-label` no longer appears in `go-mutants list` over `fixtures/discovery` except for the type switch, and `fixtures/families` holds a live candidate in a tagged `switch` |
| 2 | `if`-branch replacement: replacing a whole condition with a constant. Negating one and replacing a literal are already rules; replacing an arbitrary condition is not | The registry holds the new family, `fixtures/families` kills a candidate of every rule in it, and the dogfood gate's balanced catalogue is unchanged because the family is not balanced |
| 3 | Map and slice neutral values: `[]T{}` and `map[K]V{}` beside the `nil` `return-replacement` already offers. `len(x) == 0` is true of both and `x == nil` is true of one, which is the difference a suite routinely fails to assert | `return-empty-slice` and `return-empty-map` are in the registry, `fixtures/families` kills one of each, and a function whose result type cannot be spelled with the file's own imports records `unnameable-decl-type` rather than being dropped |
| 4 | `label-or-goto`: dropping the label from a `break L` or a `continue L`. The reserved skip reason belongs to `goto`, which has no expressible mutation | The reason is emitted for a `goto`, the two rules are in the registry, and `fixtures/families` holds a labelled loop whose every mutant still terminates |

## Guard forms

| # | What | Done when |
| ---: | --- | --- |
| 5 | Shrink `unnameable-decl-type`. Six shapes are refused today, and the [operators page](operators.md#guard-site-hints) lists them. A named boolean condition, a `for` post statement, an `if` initialiser and a `:=` that redeclares are each a form away from being expressible | `go-mutants list` over this repository reports fewer `unnameable-decl-type` skips than it did before the change, and the number is written into the commit that changes it |

## The engine

| # | What | Done when |
| ---: | --- | --- |
| 6 | Probe forms beyond the return-value one. `internal/instrument`'s dispatch returns nil for a boolean site, an arithmetic operand and a deleted statement, and its own comment calls that "the dispatch point for every form still to come" | Each form has a golden and a compile test, and a deleted statement's log records reachability with the honest note that infection cannot be observed where there is no value |
| 7 | Probing in the `run` pipeline. `Session.Probe` exists and `internal/engine` has no reference to it, so a run pays for executions a probe could have proven unnecessary | A run with probing on reaches the same verdict for every mutant as one with it off, and the work ceiling records fewer `mutant-run` children |
| 8 | Multi-module `go.work`. Refused today with `GOM4102` | `fixtures/workspace` measures both modules from its root, and the mutant identities of a module measured alone and measured in the workspace are told apart deliberately rather than by accident |
| 9 | `--isolate`, the per-worker snapshot copy. Reserved in [the architecture](architecture.md) and absent from the command line. It is the escape hatch for a suite that legitimately writes into its own package directory, which today cannot run at all | `fixtures/selfwriting` completes with a real tally under `--isolate`, a worker's copy is restored between mutants, and the drift gate still stops the same run without it |

## Documents

| # | What | Done when |
| ---: | --- | --- |
| 10 | `explain --json`. Refused today on the argument that everything it prints is already in the report and the recording — which is true of the facts and not of the joins: the reproduction command, the rebuild line, this mutant's share of each stage, and the preserved output tail exist in neither document | A `go-mutants/explain` document answers a published schema, the prose and the JSON come from one gatherer, and the `reproduce.command` in it is pasted and run by a test that asserts it reproduces the verdict |
| 11 | A `Remedy()` on every diagnostic code, so that [`docs/errors.md`](errors.md)'s third column is pinned verbatim rather than by shape. 213 constants across sixteen packages | `TestEveryDiagnosticCodeRowSaysWhatItMeansAndWhatToDo` compares the column with the method rather than checking that the cell is non-empty |

## Reserved and unemitted

The run-report schema's `reason` enumeration is deliberately a superset of the
reasons the code declares, so that landing one is a code change and not a schema
change. Two names are reserved and unemitted today, and this table is the index
of them — `internal/testkit/roadmap_test.go` compares it with the schema, so a
reason reserved and unlisted fails the build and a reason that lands fails it
too, until its row is deleted.

| Reserved | Where it stands |
| --- | --- |
| `label-or-goto` | **Work**, and it is row 4 above. The reason itself belongs to `goto`, which has no expressible mutation; the label of a `break L` or a `continue L` does, so the row lands a family and this reason together |
| `struct-tag` | **Not work.** A tag is part of a type, and no guard form can select between two types at run time. It is a [limitation](limitations.md#boundaries-that-are-facts-about-go-rather-than-about-go-mutants), and it is in this table only to say that it will never leave it |
