<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Limitations

**Status: this page is the ledger, and it is checked.**
`internal/discover/docs_test.go` keeps the skip table below equal to the reasons
discovery can emit, in both directions, and
`internal/testkit/errordocs_test.go` refuses a code cited here that
[`docs/errors.md`](errors.md) does not explain. What no test can check is
whether the arguments are still good, which is the half a reader is here for.

Everything go-mutants will not do is on this page, and it is organised by the
one thing that matters when you meet a boundary: **what happens instead.** There
are exactly three answers, and which one applies is a design decision rather
than an accident.

| What go-mutants does | What it means for a score |
| --- | --- |
| **Records a skip with a reason** | The site is not in the catalogue. `list` prints the breakdown, `--explain` says why, and the mutant was never counted |
| **Warns and carries on** | The measurement is the same; only its cost, or its precision about *which* tests could have caught something, is reduced |
| **Refuses, and stops the run** | A run that continued would publish a number about a question nobody asked |

A boundary that is none of the three — a site quietly dropped, an optimisation
that changed a verdict — is a bug, not a limitation.

## Recorded skips

These are sites go-mutants could see and declined to mutate. Each one is
counted, each carries the reason string below verbatim into `list` output and
into the catalogue and report JSON, and `list --explain` names the site as
`path:line:col`.

| Reason | Why go-mutants declines |
| --- | --- |
| `generated` | The file says it is generated, so an edit here would measure the generator's tests and be overwritten by its next run |
| `cgo` | The package imports `"C"`, and v1 does not put its rewrites through the cgo preprocessor |
| `excluded` | `mutation.include` and `mutation.exclude` removed the file |
| `const-decl` | The expression is inside a `const` declaration, where a constant has to stay constant and one edit can renumber a whole `iota` block |
| `array-length` | The expression is an array length, which is part of a type and is evaluated by the compiler rather than at run time |
| `package-var-init` | The expression initialises a package-level variable, where initialisation order is a global property a per-mutant guard cannot express in v1 |
| `type-param` | The expression is inside a type parameter list, a constraint, or a type argument, which hold types rather than values |
| `label-or-goto` | The statement is a `goto`. Its target cannot be moved without risking a jump over a declaration or into a block, which Go forbids, and removing it would leave a function reaching its closing brace without returning — the same argument the deletion family makes about `panic`. Dropping a *label* from the `break` or `continue` that carries it is a different edit and is a rule, not a refusal |
| `unnameable-decl-type` | None of the guard forms can express a rewrite here. Usually a type with no source form outside its own package: a missing *name* is supplied by [import completion](operators.md#import-completion), so what is left is a type that cannot be written anywhere |

Three things are **not** recorded skips, because nothing was declined:
`_test.go` files are built and run and never mutated, which is inherent rather
than a decision; the deletion family does not offer a `panic` call, because
removing a terminating panic manufactures a missing-return error wholesale in
exactly the defensive code the mutant would have been interesting in; and a
return value already spelled as its own replacement produces no candidate,
because the mutation and the source would be the same program.

`struct-tag` is reserved in the run-report schema and emitted by nothing, and
nothing will ever emit it: a tag is part of a *type*, so there is no run-time
value for a guard to select between. The argument is below, under boundaries
that are facts about Go.

## Warnings that never change a verdict

Each of these is an optimisation or a convenience failing open. The run measures
the same mutants and reaches the same verdicts; what it loses is speed, or the
ability to say which tests could have caught something.

| Code | What is reduced |
| --- | --- |
| `GOM7601` | Coverage guidance is off, because `test.command` is not `go test` over package patterns and go-mutants cannot attribute a binary to a package. Every mutant is measured against every binary |
| `GOM7602` | The coverage pass itself failed. Same consequence, and this code exists so that the optimisation can never fail a run |
| `GOM7603` | Tests that fail when run on their own are left out of test-level narrowing, and the binaries that hold them run whole |
| `GOM7604` | A set of tests that fails together with no mutant active cannot witness a kill, so its mutants are widened to the whole binary |
| `GOM7901` | The outcome cache is reusing nothing, because `cache.mode = "auto"` and the test command is one go-mutants cannot reason about |
| `GOM4047` | No mutant is bounded in memory: either nothing measured what the baseline cost, or this platform cannot watch a process tree while it runs |
| `GOM1012`, `GOM1013`, `GOM1014` | A GitHub step summary, a recording, or a diagnostics bundle could not be written. The run is unaffected |
| `GOM4040`, `GOM4041`, `GOM4044`, `GOM4045` | A temporary directory survived cleanup, or one the run was asked to keep could not be marked. The message names the path |

## Refusals that stop the run

A run that continued past one of these would publish a number about a different
question.

| Code | What is refused, and why |
| --- | --- |
| `GOM4011` | The unmutated suite does not pass. A score against a red baseline is a fraction of something nobody has established |
| `GOM4013` | The instrumented snapshot, with no mutant active, no longer passes what the pristine one passed. Instrumentation is supposed to be meaning-preserving, and this is the gate that says so |
| `GOM4014` | The snapshot stopped matching its manifest in a way instrumentation did not cause — a suite that writes into the package directory it runs in. Every mutant would be measured against a different program from the baseline's |
| `GOM4022` | A `test.command` whose package patterns describe a scope no mutant can be measured in. A scope that resolves to nothing is a refusal rather than a silent widening |
| `GOM4102` | A `go.work` at the snapshot root. A workspace has no single module path, no single set of module-relative identities and no single baseline. The file is read before it is refused, so one that is itself malformed says which line is wrong |
| `GOM7711`, `GOM7712` | `--changed` outside a repository, or with no upstream to compare against. A narrowing that silently fell back to everything, or to nothing, would be worse than not running at all |
| `GOM7812`, `GOM7813` | `report merge` given documents that are not every part of exactly one run |

## Boundaries that are facts about Go rather than about go-mutants

- **A struct tag cannot be mutated at all.** A tag is part of a *type*, not a
  runtime-evaluated expression, and two struct types differing only in their
  tags are not identical — the specification ignores tags for conversion and
  not for assignability. Every guard form is a runtime selection between two
  expressions of one type, so there is no rewrite that compiles. This is not a
  v1 limitation; it is not expressible.
- **A type switch's case labels cannot be mutated.** They hold types, for the
  same reason a struct tag does, and no rule in the registry rewrites a type.
  Nothing is ever proposed at one, so nothing is declined and no skip is
  recorded — the same silence a `fallthrough` gets. A *tagged* switch's labels
  are ordinary expressions of the tag's type and are mutated; a *tagless*
  switch's are exactly `bool` and are mutated.
- **One host platform per report.** Build constraints decide which files a
  package even has, so a report is a statement about the platform it was
  measured on. There is no cross-`GOOS` matrix, and `doctor` warns when the
  `go` on `PATH` targets a platform other than the host.
- **`--changed` needs git.** It is the one feature that needs a tool go-mutants
  does not ship. Rename detection is off, so a renamed file selects every
  mutant in it.

## What the projection loses

The Stryker projection is one-way and lossy by design. Six outcomes become five
statuses — an uncovered survivor projects as `Survived` rather than
`NoCoverage`, so that the two documents agree about how many survivors there
were — and the expectations ledger, the cache accounting and coverage do not
survive the trip. `run-report-v1` is the document to diagnose, resume or audit
from. [Stryker report ecosystem compatibility](stryker-compatibility.md) states
the whole mapping.

## What is not here

Things go-mutants does not do *yet*, with what would have to be true for it to,
are on the [Roadmap](roadmap.md). The difference is the point of both pages: a
limitation is a decision, and a roadmap entry is work.
