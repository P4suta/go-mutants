<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Working in this repository

go-mutants instruments every compilable mutant **once** into a disposable
snapshot of a module, then activates one mutant per test process through an
environment variable. Nothing it does modifies the tree under test. Read
[docs/architecture.md](docs/architecture.md) first, then
[ADR 0001](docs/adr/0001-trace-is-not-evidence.md) and
[ADR 0008](docs/adr/0008-the-unit-tier-scripts-the-toolchain.md); between them
they explain most of what looks unusual here.

## The protocol

1. **Red.** Write the test against the behaviour, not the implementation, and
   watch it fail *for the stated reason*. Paste that output in the pull request.
   A test that passes before the change is not evidence.
2. **Green.** The smallest change that is honest about the contract.
   Fail-closed is part of the contract, not an error path to add later.
3. **Refactor.** Remove the duplication with the suite green throughout.
4. **Infrastructure is a deliverable.** Every change carries its tests, its
   trace, its gate, its diagnostics and its documentation as completion
   criteria. Speed never wins over them.

## Gates you must keep green

```console
mise run check
mise run test-integration
mise run dogfood
```

`check` is `fmt`, `build`, `test` and `lint` in the order CI runs them.
`test-integration` is the tier that starts real toolchains. `dogfood` is
go-mutants measuring go-mutants, and it is the one that says whether the tests
*catch* anything rather than which lines they ran.

Two more when a change touches what they pin:

```console
mise run golden-update
mise run test-cost
```

The first rewrites the committed goldens; **the diff is the review**, so read it
before committing. The second prints a per-package cost and skip table.

[docs/ci.md](docs/ci.md) says what each gate is and how to reproduce a red job.
[CONTRIBUTING.md](CONTRIBUTING.md) is the shorter road in;
[docs/development.md](docs/development.md) is the reference underneath it.

## Conventions

- **Two tiers, enforced by a scan.** A test that starts a real `go` or `git`
  carries `//go:build integration`. The exceptions are a ledger at
  `internal/testkit/testdata/unit-toolchain-allowlist.txt`, which may shrink and
  never grow, and in which a stale entry fails as loudly as an offender.
- **One harness.** `internal/testkit` is it. Production code may not import it,
  and it may import nothing from this module — both are tests. Its
  `mutantkit` half is what knows what go-mutants *is*.
- **Goldens fail closed.** A missing golden is a failure, never a silent first
  recording. Regenerating one is a decision somebody makes on purpose.
- **Every error carries a `GOM` code**, declared in its package's `errors.go`
  and explained in [docs/errors.md](docs/errors.md). A code is allocated once
  and never reused, even after the condition it named is gone.
- **SPDX on every file.** The ones that cannot carry a header are annotated in
  `REUSE.toml`, and a test refuses a file that is in neither.
- **Conventional Commits.** This repository squash-merges, so the pull request
  title becomes the subject on `main` and is release-please's only input. Write
  it as a declarative sentence about behaviour, not about implementation.
- **No skip list wearing a ledger's clothes.** A `[[mutation.expect]]` row is
  for a survivor no honest test can reach, and it carries the argument for
  which. "No test covers this" is a missing test.

## The documentation ledger

Any page that enumerates a set the code also enumerates is pinned to the code by
a test, in both directions. That discipline is most of what keeps this
repository's prose worth reading, so here is where each ledger lives.

| What is pinned | To what | Where |
| --- | --- | --- |
| `docs/errors.md` | every `GOM` constant, its block, its digit, and the retired ones | `internal/testkit/errordocs_test.go` |
| `docs/operators.md` catalogue | the canonical rule registry, its counts and its tiers | `internal/mutation/docs_test.go` |
| `docs/operators.md` and `docs/limitations.md` skips | `discover.AllSkipReasons` | `internal/discover/docs_test.go` |
| `docs/roadmap.md` reserved reasons | the run-report schema's superset | `internal/testkit/roadmap_test.go` |
| `docs/architecture.md` package table | every directory holding Go source | `internal/testkit/archdoc_test.go` |
| `docs/command-line.md` and 22 `--help` goldens | the cobra tree | `internal/cli/docs_test.go` |
| the exit-code tables | `exitCodeHelp` and `mutation.ExitCode` | `internal/cli/docs_test.go` |
| `docs/ci.md` | the workflows, their jobs, and the tasks they run | `internal/testkit/cidoc_test.go` |
| `docs/README.md` and every Status line | the directory, and the README's own list | `internal/testkit/docsindex_test.go` |
| `docs/development.md`, `docs/ci.md`, this page | the mise tasks and harness variables they name | `internal/testkit/devdocs_test.go` |
| `docs/adr/README.md` | the records beside it | `internal/testkit/devdocs_test.go` |
| `docs/trace-v1.md` | every `ExecKind` | `trace/docs_test.go` |
| `docs/json-schema.md` | every published schema, every document type, and its own counts | `internal/testkit/schemadocs_test.go` |
| `fixtures/README.md` | the corpus modules and their conformance | `internal/testkit/corpus_test.go` |
| how many children a run starts, by kind | the recording every run keeps | `internal/engine/workceiling_integration_test.go` |
| that a run report and its recording agree | each other, re-derived independently | `internal/devtools/traceaudit/audit_test.go` |
| which mutants provably cannot return | the loop shapes, decided before anything runs | `internal/discover/termination_test.go` |
| what the dashboard looks like | the model, on the ASCII theme | `internal/tui/frames_test.go` |
| that every narrowing reaches one verdict | ADR 0010 | `internal/engine/narrowing_integration_test.go` |
| `REUSE.toml` | every file git holds or would hold | `internal/testkit/licensegate_test.go` |

Adding a page that enumerates something means adding its ledger in the same
change. A page nothing checks is a page that will be wrong, and the only
question is when.

## Where things are

| | |
| --- | --- |
| What it does, and will not do | [docs/architecture.md](docs/architecture.md), [docs/limitations.md](docs/limitations.md) |
| Contracts | [docs/json-schema.md](docs/json-schema.md), [docs/trace-v1.md](docs/trace-v1.md), [docs/library.md](docs/library.md) |
| Surfaces | [docs/command-line.md](docs/command-line.md), [docs/configuration.md](docs/configuration.md), [docs/operators.md](docs/operators.md) |
| Failures | [docs/errors.md](docs/errors.md) |
| Decisions | [docs/adr/](docs/adr/README.md) |
| Working on it | [CONTRIBUTING.md](CONTRIBUTING.md), [docs/development.md](docs/development.md), [docs/ci.md](docs/ci.md) |
| What nobody has done yet | [docs/roadmap.md](docs/roadmap.md) |
| Releasing | [docs/release-checklist.md](docs/release-checklist.md) |
