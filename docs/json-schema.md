<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# JSON contracts

**Status: two schemas shipped.** `schema/catalog-v1.schema.json` and
`schema/run-report-v1.schema.json` exist, are embedded in `internal/schemas`,
and every document the CLI writes is validated against them in the tests. The
`go-mutants/doctor` document and the Stryker projection are still planned, and
say so below.

go-mutants publishes three native document types and one lossy projection for
the Stryker report ecosystem. Every native document is discriminated by two
fields that a consumer must check before decoding:

```json
{ "document_type": "go-mutants/run-report", "schema_version": 1 }
```

Schemas live in `schema/`, are written in JSON Schema draft 2020-12 with
`additionalProperties: false`, and are validated in tests with
`santhosh-tekuri/jsonschema/v6` against both fixtures and real CLI output. The
validator is deliberately not linked into the shipped binary: a schema
violation is a bug in this repository for a test to catch before a release, not
a run-time condition to recover from.

## `go-mutants/run-report` v1

The authoritative, lossless record of a run. Produced by `run --json` on
standard output, and written to the history store described below. Every
catalogued mutant appears exactly once — in `mutants[]` with what happened to
it, or in `rejected[]` with the compiler's reason it could not exist — so the
console summary, the exit code, and every later projection are views of this
document rather than second opinions.

| Field | Contents |
| --- | --- |
| `document_type`, `schema_version` | `go-mutants/run-report`, `1` |
| `tool_version` | The build that wrote the document |
| `run_id` | `YYYYMMDDThhmmssZ-xxxx`, also the history file name |
| `status` | `completed`, `interrupted`, or `failed` |
| `started_at`, `finished_at`, `duration_ms` | RFC 3339 UTC to the second, and the elapsed milliseconds |
| `workspace` | `module_path`, `go_version`, `workspace_digest`, `platform.{os,arch}` |
| `selection` | `mode`, `profile`, `operators`, `include`, `exclude`, `candidates`, `rejected`, `selected` |
| `test` | `command` argv, `baseline`, `timeout_ms`, `timeout_source` |
| `coverage` | `mode`, and in `package` mode `binaries` and `mutants_uncovered` |
| `summary` | The counters, `score_percent`, and `policy` |
| `mutants[]` | One entry per executed or not-run mutant; see below |
| `rejected[]` | Candidates the compiler refused, with diagnostics |
| `skips[]` | Reason-coded static skips, aggregated per file |
| `expectations[]` | Each `[[mutation.expect]]` row, evaluated |
| `warnings[]` | `GOMnnnn` code plus message |

`selection.mode` is `all` or `mutant`; `candidates` equals `rejected` plus the
length of `mutants[]`, and `selected` is how many the run set out to execute.
`test.baseline` carries every unmutated observation — `runs`, `durations_ms`,
`slowest_ms` — not just the summary of them, because the derived timeout is a
function of the slowest run and a reader deserves the numbers it came from.
`test.timeout_source` is `derived` for `max(10s, slowest baseline × 5)` or
`explicit` for a configured `test.timeout` or `--timeout`.

### `coverage`

`coverage` is an object rather than a bare string, which is what let
coverage-guided selection arrive as `mode: "package"` without any consumer
having to learn a new top-level shape.

| Field | Contents |
| --- | --- |
| `mode` | `off` or `package` |
| `binaries` | How many test binaries were profiled. Present only in `package` mode |
| `mutants_uncovered` | How many entries in `mutants[]` carry `uncovered: true`. Present only in `package` mode |

`off` means every selected mutant was measured against every test binary.
`package` means each test binary was profiled once and every mutant was
measured only against the binaries whose profile reaches its lines.

The two numbers are absent — not zero — outside `package` mode, and the schema
refuses them there. An `off` run carrying `binaries: 0` would be stating a
measurement it never made, and a reader cannot tell a real zero from a default
one. `mutants_uncovered` is derived from `mutants[]` when the document is built,
so the summary and the rows underneath it cannot disagree.

`mode` is `package` exactly when the effective `test.command` is the built-in
`go test ./...` **and** the coverage pass succeeded. A custom command turns it
off with a `GOM7601` warning, because the mapping is from a test binary to the
lines it reached and there is no honest way to attribute an opaque command's
coverage to go-mutants' own per-package binaries. Any failure of the pass itself
turns it off with a `GOM7602` warning and runs every mutant against every
binary; see [Architecture](architecture.md).

### `mutants[]`

Each entry carries the full 64-hex `id` and the 20-hex `display_id`, `path`,
`package`, `family`, `rule`, `rule_version`, `line`, `column`, `start_byte`,
`end_byte`, `original`, `replacement`, `outcome`, `duration_ms`, `killed_by`,
`attempts`, `output_tail`, `covering_test_packages`, and `uncovered`.

`killed_by` names the test binary that detected the mutant — the one that
failed, or the one it hung — and is `null` for an outcome that detected
nothing, and for a detection whose output named no binary. `attempts` is 0 for
a mutant the run never reached, 1 for an outcome settled first time, and 2 for
a confirmed timeout.

`covering_test_packages` is the sorted import paths of the test binaries whose
coverage profile reaches the mutant's lines, and `uncovered` says the run
established that none does. Both are always present, and an empty
`covering_test_packages` means two different things depending on
`coverage.mode`: with `off` nobody asked, and with `package` nothing covers it —
in which case `uncovered` is `true`. An uncovered mutant is always `survived`
with `attempts: 0` and `duration_ms: 0`: no test runs the line, so no test could
have caught the edit, and the run does not spend a process finding that out. It
still counts as a survivor in the score, because it is one.

The `outcome` enum:

| Value | Meaning |
| --- | --- |
| `killed` | At least one test failed with the mutant active |
| `survived` | The whole suite passed with the mutant active |
| `timed-out` | A *confirmed* timeout: timed out, retried serially, timed out again. Counts as a detection |
| `inconclusive` | Undecidable — including a single timeout whose serial retry finished. Counts in neither direction |
| `errored` | The harness itself failed for this mutant |
| `not-run` | Never executed |

The values are hyphenated while the summary keys are snake_case (`timed_out`,
`not_run`). That is deliberate and must not be unified by anybody tidying up:
the keys are field names, the values are a published enum, and renaming one
breaks somebody's `jq` expression.

### The score, and when it is null

```text
score_percent = 100 × (killed + timed_out) / (killed + timed_out + survivors)
```

where *survivors* counts only the unexpected ones. `summary.survived` counts
every survivor; the split is recovered by joining `expectations[]`, since a
survivor with a `fulfilled` expectation is an expected survivor. Inconclusive
results, errors, and not-run mutants are in neither the numerator nor the
denominator.

`score_percent` is `null` — never a number — exactly when that denominator is
zero. Both plausible sentinels are lies: 0 reads as "your tests caught nothing"
and 100 as "your tests caught everything", when the truth is that nothing was
measured. The console prints `score N/A` for it.

`summary.policy` records the gates as configured (`strict`, `minimum_score`,
`require_mutants`) and `failure`, the first gate that failed, or `null`.
Naming one loses nothing: every gate is a function of the counts in the same
object, so a consumer that cares about the second can recompute it.

### `rejected[]` and `skips[]`

A `rejected` entry is a catalogued mutant that compile validation refused, with
`id`, `display_id`, `path`, `line`, `column`, `rule`, and the compiler's own
`diagnostic`. It has no outcome, duration, or attempt count, because it was
never executed, and it is not counted in the summary — reporting it as errored
or not-run would put a mutant that cannot exist into a denominator.

A `skip` entry is one recorded reason a site was never turned into a candidate,
aggregated per file: `path`, `reason`, and `count`. The `reason` enum is
`const-decl`, `array-length`, `type-param`, `case-label`, `package-var-init`,
`cgo`, `generated`, `excluded`, `struct-tag`, `label-or-goto`, and
`unnameable-decl-type` — the identifiers documented in
[Operators](operators.md). `struct-tag` and `label-or-goto` are still reserved
for instrumentation; everything else is emitted by discovery.

### `expectations[]`

One row of the `[[mutation.expect]]` ledger checked against this run: `id`,
`reason`, and a three-valued `state`.

| State | Meaning |
| --- | --- |
| `fulfilled` | The mutant survived, as the ledger predicted |
| `unfulfilled` | The predicted survival was not observed |
| `stale` | The id is not in this catalog any more |

`unfulfilled` covers two different situations on purpose, and the document
carries what tells them apart: join `mutants[]` and `rejected[]` by `id` to see
whether the tests caught the mutant — the ledger is lying, which is exit 2 —
or whether it was simply never measured, which is not a failure of anything.

### History

History is kept outside the workspace, under the OS cache directory:

```text
<cache>/go-mutants/workspaces/<key>/latest.json
<cache>/go-mutants/workspaces/<key>/runs/<run-id>.json
```

A mutation run must not add files to the tree it is measuring, so nothing lands
in the workspace. `runs/` holds nothing but immutable per-run documents, which
makes listing past runs a directory listing where every entry is a real run,
and `latest.json` is a whole copy of the newest one rather than a pointer that
could dangle. Every write is temp-file plus atomic rename, so a reader sees one
complete document or the previous one, and `ReportPublished` is emitted only
after the rename succeeds.

## `go-mutants/catalog` v1

Produced only by `list --json`, and validated against
`schema/catalog-v1.schema.json`.

| Field | Contents |
| --- | --- |
| `document_type`, `schema_version` | `go-mutants/catalog`, `1` |
| `tool_version` | The build that wrote the document |
| `workspace` | Same shape as the run report's |
| `selection` | `profile`, `operators`, `include`, `exclude` |
| `mutants[]` | Identity and coordinates only — no outcome |
| `skips[]` | The same `path`/`reason`/`count` shape |

A catalog is not a run report: it has no outcomes, no summary, no test output,
and no run ID, because nothing was executed to produce one. Its `mutants[]`
entries stop at `replacement`, and it records the profile separately from the
selection patterns so that two catalogs from the same tree can be compared
byte-for-byte as a determinism gate.

## `go-mutants/doctor` v1

**Planned; no schema file exists yet.** Will be produced by `doctor --json`,
reporting the discovered toolchain, module layout, git availability, cache
directory, terminal capabilities, and every check's pass/fail with a
remediation hint.

## Stryker projection

**Planned.** One-way, lossy, and deterministic; never read back as state.
`inconclusive` maps to `Ignored` with a `statusReason`, and a confirmed timeout
maps to `Timeout`. If the projection cannot be validated against the vendored
schema, the run aborts rather than emitting a document that would look
authoritative. See [Stryker compatibility](stryker-compatibility.md).

## Compatibility rules

- Consumers must branch on `document_type` and `schema_version`.
- New fields are additive within a schema version; removing or retyping a field
  requires a version bump.
- Unknown fields are rejected by the schemas on purpose: a typo in a generated
  document is a bug, not a forward-compatible extension.
- Every array is `[]` when empty and never `null`. "No warnings" and "warnings
  unknown" are not the same statement, and only one of them is ever true.
