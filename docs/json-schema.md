<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# JSON contracts

**Status: planned.** No schema file exists yet. This page fixes the shape of
the documents before anything writes one, because these are the compatibility
surfaces users script against.

go-mutants publishes three native document types and one lossy projection for
the Stryker report ecosystem. Every native document is discriminated by two
fields that a consumer must check before decoding:

```json
{ "document_type": "go-mutants/run-report", "schema_version": 1 }
```

Schemas live in `schema/`, are written in JSON Schema draft 2020-12 with
`additionalProperties: false`, and are validated in tests with
`santhosh-tekuri/jsonschema/v6` against both fixtures and real CLI output.

## `go-mutants/run-report` v1

The authoritative, lossless record of a run. Produced by `run --json` and by
`report latest --json`, and written to `reports/mutation/mutation.json`.

| Field | Contents |
| --- | --- |
| `run_id`, `status` | Identity and completed/interrupted/failed status |
| `workspace` | Module path, Go version, workspace digest, platform, VCS |
| `selection` | Include/exclude, operators, profile, changed-ref, mutants |
| `shard` | Present only for `--shard k/n` runs |
| `test` | Command, baseline observations, timeout with its source |
| `coverage` | Mode, per-package mapping outcome, fail-open reason if any |
| `cache` | Mode, key inputs, hits and misses |
| `summary` | Counters, `score_percent`, and the applied policy |
| `mutants[]` | See below |
| `rejected[]` | Candidates the compiler refused, with diagnostics |
| `not_run[]` | Not-run mutants and why (uncovered, other shard, interrupt) |
| `expectations[]` | Fulfilled, unfulfilled, stale, not-evaluated |
| `warnings[]`, `skips[]` | Non-fatal notes and reason-coded static skips |

Each entry of `mutants[]` carries the full `id` and the 20-hex `display_id`,
file/line/column coordinates, the versioned operator, the original and
replacement text, the outcome, `duration_ms`, whether it was a cache hit, the
covering test packages, and the per-stage timings including a timeout retry as
a separate attempt.

`summary.score_percent` is `100 × (killed + confirmed_timeouts) / denominator`,
where the denominator excludes expected survivors, inconclusive results,
errors, and not-run mutants. It is `null` when that denominator is zero.

History is kept outside the workspace, under the OS cache directory at
`<os-cache>/go-mutants/<workspace-key>/runs/<run_id>.json` with a `latest.json`
pointer. Every write is temp-file plus atomic rename, and `ReportPublished` is
emitted only after the rename succeeds.

## `go-mutants/catalog` v1

Produced only by `list --json`. A catalog is not a run report: it has no
outcomes, no summary, no test output, and no run ID. It records the profile
separately from the selection description so that two catalogs from the same
tree can be compared byte-for-byte as a determinism gate.

## `go-mutants/doctor` v1

Produced by `doctor --json`. Reports the discovered toolchain, module layout,
git availability, cache directory, terminal capabilities, and every check's
pass/fail with a remediation hint.

## Stryker projection

One-way, lossy, and deterministic; never read back as state. `inconclusive`
maps to `Ignored` with a `statusReason`, and a confirmed timeout maps to
`Timeout`. If the projection cannot be validated against the vendored schema,
the run aborts rather than emitting a document that would look authoritative.
See [Stryker compatibility](stryker-compatibility.md).

## Compatibility rules

- Consumers must branch on `document_type` and `schema_version`.
- New fields are additive within a schema version; removing or retyping a field
  requires a version bump.
- Unknown fields are rejected by the schemas on purpose: a typo in a generated
  document is a bug, not a forward-compatible extension.
