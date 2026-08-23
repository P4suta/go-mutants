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

## Expectations

- Run `mise run check` before submitting; run `mise run test-integration` when
  you touch snapshotting, the runner, or anything that shells out to `go`.
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
