<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# go-mutants 0.1.0 pre-release notes (draft)

`go-mutants` is a mutation testing tool for Go modules built around three
decisions: the target workspace stays read-only, all mutants are instrumented
into a disposable snapshot exactly once and selected per test process by
`GO_MUTANTS_ACTIVE`, and mutant identities are stable SHA-256 values that a
cache and a CI shard can both rely on.

**This draft describes a scaffold.** At this commit the repository contains the
module, the pinned toolchain, the quality gates, CI, the licensing and policy
files, and the design documentation. There is no mutation engine: the CLI
prints its version and exits. Nothing is published or tagged, and the tool must
not be described as production-ready.

What the scaffold establishes, and why it is worth a release note:

- A single pinned toolchain in `mise.toml`, the Go compiler included, so that
  the local gates and the CI gates are the same gates.
- Byte-exact checkouts via `* -text`, because mutant identities hash source
  bytes and a CRLF checkout would change every one of them.
- The complete v1 dependency set recorded up front, reviewable as one decision.
- Documentation that states what is planned rather than implying it works,
  including the instrument-once schemata design, the S/C/D guard forms, the
  operator catalogue with its profile tiers, and the JSON contracts.

The intended public compatibility surfaces for 0.1 are the CLI, the
`.go-mutants.toml` version 1 schema, the native `run-report-v1`, `catalog-v1`,
and `doctor-v1` JSON documents, and the one-way Stryker report projection. Go
packages under `internal/` are not a library API.

Before anything is published, every item in
[`docs/release-checklist.md`](docs/release-checklist.md) must be complete on a
single immutable commit.
