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

## Documents

| # | What | Done when |
| ---: | --- | --- |
| 1 | A `Remedy()` on every diagnostic code, so that [`docs/errors.md`](errors.md)'s third column is pinned verbatim rather than by shape. 213 constants across sixteen packages | `TestEveryDiagnosticCodeRowSaysWhatItMeansAndWhatToDo` compares the column with the method rather than checking that the cell is non-empty |
| 2 | A `run-report` v2 that carries a workspace in one flat document, instead of the workspace report that holds one run report per module | `internal/schemas`' registry is keyed on (type, version) and `Validate` reads `schema_version` out of the instance before choosing a schema — which is the cost the current design avoids, and the reason it was chosen. See [ADR 0012](adr/0012-a-workspace-is-one-run-of-many-modules.md) |

## The engine

| # | What | Done when |
| ---: | --- | --- |
| 3 | Probing at test granularity rather than binary granularity. A probe pass records, per *binary*, whether it could rule a mutant out; a pass that recorded it per test would narrow a mutant to the tests that could observe it rather than to the binaries | A second soundness argument is written as an ADR and survives review. The current design rejects this on purpose, and the argument to beat is stated in [ADR 0011](adr/0011-an-unobservable-mutant-need-not-be-executed.md): a test profiled on its own is a different execution from the same test inside its set, so "this test could not observe it" is a weaker licence than "this binary could not" |

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
