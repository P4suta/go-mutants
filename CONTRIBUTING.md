<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Contributing

Thank you for improving `go-mutants`. The two invariants worth protecting above
everything else: **a target workspace is never modified**, and **mutant
identities are deterministic**.

## Development setup

```console
mise trust
mise install
mise run bootstrap
mise run check
mise run hooks
```

`mise.toml` pins every tool, including the Go toolchain, so a green
`mise run check` locally and a green CI run mean the same thing. There is no
supported setup that installs these tools some other way.

[`docs/development.md`](docs/development.md) is the reference for everything
below: what a test gets from the harness, where it writes, what a failure leaves
behind, and how to read the account a run keeps of itself.

## The two tiers

`mise run test` is the unit tier — everything that needs a compiler and nothing
else — and it is what to run on every save. `mise run test-integration` adds the
suites that drive a real toolchain: a `go build`, a `go test -c`, a mutation run.

A test that starts a `go` or a `git` command belongs in the second tier and
carries `//go:build integration`. `internal/testkit/tiers_test.go` enforces that
by scanning every `_test.go` in the tree, and the exceptions are written down one
path at a time, each with a paragraph saying why, in
`internal/testkit/testdata/unit-toolchain-allowlist.txt`. **That file is a ledger
rather than a configuration**: a stale entry is reported as loudly as an
offender, so it shrinks when a suite is tagged and grows only when somebody adds
a path and argues for it. Adding a line to silence the gate is not a fix.

Often neither is the answer. `mutantkit.FakeGo` hands a test a `go` command it
writes the answers for, so "what happens when the toolchain misbehaves" is a
unit test on a machine with no Go on it — and a file that constructs one is
exempt by construction, because it supplies the toolchain rather than reaching
for the machine's. The exemption is per file and covers only the calls a fake
can be handed, so a file that also calls `exec.LookPath("go")` or
`testkit.GoBinary` is reported like any other.

## Diagnosing a failing test

Turn the keep policy on for the one test and read what it left:

```console
GO_MUTANTS_TEST_KEEP=1 \
    go test -tags integration ./internal/engine -run TestSomething -v
```

The directory it prints holds `KEPT.txt` — which test, which fixture, which
toolchain, which build cache, and every child it ran — plus the tree the test
worked in, any `dump/` the failure took, and the recording if the test made one.
`GO_MUTANTS_TEST_FORCE_FAIL=TestSomething` fails a chosen test on purpose, which
is how to see all of that on a test that works. In CI it is already on, and a
failed job's `kept-scratch-*` artifact is the same thing.

[`docs/development.md`](docs/development.md#4-diagnosing-a-failing-test) walks
through it, and names what collects it afterwards: `mise run test-clean`, and
nothing else.

## Diagnosing a failing run

```console
go-mutants run --trace -vv --keep-temp=on-failure
```

A failed run writes a bundle by default: the rendered
failure in `error.txt`, the environment's variable names, the `doctor` table,
the recording and the report. **Where it lands depends on the trace.** A traced
run — which the command above is — files it in its own recording's directory,
`reports/mutation/trace/<run-id>/`, so the failure and the account of the run
are one directory rather than two halves of one story; an untraced run files it
in `reports/mutation/diagnostics/<run-id>/`. `--no-diagnostics`, or
`GO_MUTANTS_DIAGNOSTICS=0` for an invocation nobody can add a flag to, writes
none at all.

`--keep-temp` leaves the tree the run worked in. `go-mutants trace summary` says
where the time went, and `go-mutants explain <mutant-id>` gives one mutant's
whole story with a command to paste that runs it again.

[`docs/development.md`](docs/development.md#9-diagnosing-a-failing-run) is the
walkthrough, and [`docs/trace-v1.md`](docs/trace-v1.md) is the event contract.

## Expectations

- Run `mise run check` before submitting; run `mise run test-integration` when
  you touch snapshotting, the runner, or anything that shells out to `go`.
- The suites' child `go` commands compile into a build cache of the harness's
  own rather than into yours, because they key every entry on a path that exists
  for a single run. `mise run test-cache-status` says where it is and how much
  it holds; `mise run test-clean` empties it. `test-integration` and `dogfood`
  run through a wrapper that empties it after a run that left it over 4 GiB.
- Keep `internal/mutation`, `internal/interval`, and `internal/glob` pure: no
  filesystem, no processes, no clock. That purity is what makes golden ID
  vectors and property tests meaningful.
- Never mutate a target workspace, including dirty and untracked files. New
  filesystem code belongs behind `internal/snapshot`.
- A new operator needs a stable name, a rule version, type evidence from
  `go/types`, byte-preservation tests, and a row in `docs/operators.md`.
  Changing what an existing rule emits requires a version bump, because the
  version is part of every affected mutant ID.
- Treat new configuration keys and JSON fields as compatibility work: update
  `docs/configuration.md`, `docs/json-schema.md`, and the schemas together.
- Add an SPDX header to every new file (`// SPDX-FileCopyrightText: 2026
  go-mutants contributors` and `// SPDX-License-Identifier: MIT OR
  Apache-2.0`). Files that cannot carry one are annotated in `REUSE.toml`.
- Write LF line endings. `.gitattributes` pins `* -text`, and CRLF would change
  every mutant ID.

## Commits and pull requests

Use imperative subjects of at most 72 characters (80 is the hard
`committed.toml` ceiling) with wrapped bodies; the `commit-msg` hook and the CI
lint job both run `committed`. Keep commits focused, and update `CHANGELOG.md`
with the reason for the change, not only its shape.

**Pull request titles must be conventional commits.** This repository
squash-merges, so the pull request title — not the titles of the commits inside
it — becomes the subject on `main`, and that subject is the only thing
release-please reads. Use `type(scope): imperative subject`:

- `feat:` a user-visible capability — minor bump
- `fix:` a user-visible defect repaired — patch bump
- `feat!:`, or any type with a `BREAKING CHANGE:` footer — major bump
- `docs:`, `test:`, `chore:`, `build:`, `ci:`, `refactor:`, `perf:` — no bump,
  and excluded from the generated release notes

A title release-please cannot parse produces no version bump and no Release PR
at all, which is a release that silently does not happen rather than a loud
failure. The same 72-character guidance applies: the title is a commit subject.

The commits *inside* the pull request are still linted by `committed` and still
want imperative subjects, but they do not need a conventional-commit type —
they are squashed away. `docs/release-checklist.md` describes what the subject
on `main` then drives.

By contributing you agree that your work may be distributed under
`MIT OR Apache-2.0`.
