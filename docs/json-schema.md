<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# JSON contracts

**Status: three schemas shipped, plus one vendored.**
`schema/catalog-v1.schema.json`, `schema/run-report-v1.schema.json` and
`schema/doctor-v1.schema.json` exist, are embedded in `internal/schemas`, and
every document the CLI writes is validated against them in the tests. The
Stryker projection is validated too, against the vendored third-party schema in
`schema/stryker/` — which is deliberately kept out of that registry, for the
reasons given below.

go-mutants publishes three native document types and one lossy projection for
the Stryker report ecosystem. Every native document is discriminated by two
fields that a consumer must check before decoding:

```json
{ "document_type": "go-mutants/run-report", "schema_version": 1 }
```

Schemas live in `schema/`, are written in JSON Schema draft 2020-12 with
`additionalProperties: false`, and are validated in tests with
`santhosh-tekuri/jsonschema/v6` against both fixtures and real CLI output. A
schema violation in a document go-mutants wrote is a bug in this repository for
a test to catch before a release, not a run-time condition to recover from — so
nothing on the writing path validates. The validator is in the shipped binary
all the same, because two commands read documents somebody else wrote:
`go-mutants report validate FILE` checks one against the embedded schema, and
`go-mutants report merge` checks every shard report before combining them.

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
| `selection` | `mode`, `changed_ref`, `profile`, `operators`, `include`, `exclude`, `candidates`, `rejected`, `selected` |
| `shard` | Which shard of a split run this is, or `null`; see below |
| `merge` | Present only on a document `report merge` wrote; see below |
| `test` | `command` argv, `baseline`, `timeout_ms`, `timeout_source` |
| `coverage` | `mode`, and in `package` mode `binaries` and `mutants_uncovered` |
| `cache` | `mode`, `hits`, `misses`, `writes`; see below |
| `summary` | The counters, `score_percent`, and `policy` |
| `mutants[]` | One entry per executed or not-run mutant; see below |
| `rejected[]` | Candidates the compiler refused, with diagnostics |
| `skips[]` | Reason-coded static skips, aggregated per file |
| `expectations[]` | Each `[[mutation.expect]]` row, evaluated |
| `warnings[]` | `GOMnnnn` code plus message |

`selection.mode` is `all`, `mutant`, `changed`, or `shard`; `candidates` equals
`rejected` plus the length of `mutants[]`, and `selected` is how many the run
set out to execute.
`test.baseline` carries every unmutated observation — `runs`, `durations_ms`,
`slowest_ms` — not just the summary of them, because the derived timeout is a
function of the slowest run and a reader deserves the numbers it came from.
`test.timeout_source` is `derived` for `max(10s, slowest baseline × 5)` or
`explicit` for a configured `test.timeout` or `--timeout`.

### Narrowed runs: `selection.mode`, `changed_ref`, and `shard`

Narrowing is a decision about *execution* and never about discovery. A
`--changed` or `--shard` run discovers, catalogues and compile-validates the
whole module exactly as a full run does, and executes a subset of it; so the ids
in a narrowed report are the ids a full run would have minted, `rejected[]` and
`skips[]` are identical, and two reports over one workspace can be compared
mutant for mutant. Everything not executed is `outcome: "not-run"` with a
`not_run_reason` saying which narrowing left it out.

`selection.mode` is a label on that decision and not the whole of it, because
the two narrowings compose: a shard of a pull request's diff reports `mode:
"shard"` *and* a `changed_ref`. The mode names the outer narrowing — `shard`
before `changed` before `mutant` — and the two facts underneath it are recorded
independently:

| Field | Contents |
| --- | --- |
| `selection.changed_ref` | The git ref the diff was taken against, or `null`. The merge base of it and `HEAD` is what was compared, so a branch is diffed against the commit it left rather than against the target's current tip |
| `shard.index`, `shard.total` | Which shard of how many, 1-based |
| `shard.assignment` | `id-hash-v1`: the first eight bytes of the SHA-256 of the full mutant id, read big-endian, modulo `total`, plus one |

`shard` is `null` for a run that was not split, and `assignment` is named so
that a consumer can recompute the partition rather than trust it. The function
depends on nothing but the id and the total, which is the property sharding
needs: adding or removing mutants elsewhere in the module never moves an
existing one into another shard.

`merge` is present only on a document `go-mutants report merge` produced, and
absent — not `null` — from every document a run wrote. It carries `shards`, how
many shard reports were combined, and a document that has it always has `shard:
null`: the merged report describes the whole run and no single shard of it. Its
`selection.mode` is `all`, or `changed` when the shards were diff runs, and its
counts, score and expectations are recomputed from the merged rows rather than
added up from the shards.

Merging refuses anything that is not one run split cleanly: the shards must
agree on tool version, workspace digest, module path, catalogue, changed ref,
shard total and assignment; every index from 1 to `total` must be present
exactly once; and every row must belong to the shard that reported it. The first
discrepancy is named and nothing is written, because the whole point of a merged
document is that somebody is going to trust it.

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

### `cache`

| Field | Contents |
| --- | --- |
| `mode` | `off` or `on` |
| `hits` | Mutants answered from the outcome cache and never executed |
| `misses` | Mutants looked up, not found, and executed |
| `writes` | How many of those outcomes were stored for a later run |

`mode` is what the run **did**, not what was configured. `cache.mode = "auto"`
resolves to on or off before any mutant is executed — off for a test command
go-mutants cannot reason about, off again when the cache directory cannot be
opened — and this field records what it resolved to, with a `GOM79xx` warning
saying why whenever it stood down. Recording `auto` would put the one value a
reader cannot act on into a document whose job is to say what happened.

`hits` equals the number of entries in `mutants[]` with `cached: true`, counted
from the rows when the document is built so the two cannot disagree. `writes`
never exceeds `misses`: an outcome is only stored for a mutant that was
measured, and only when it is one a later run may reuse. Every count is zero
when the mode is `off`, and the schema enforces it — which is what makes "the
cache was off" and "the cache was empty" different statements.

The gap between `hits + misses` and the number of mutants the run executed is
the mutants the cache was never asked about: every id named in
`[[mutation.expect]]`, which is measured on every invocation, and every mutant
coverage settled without executing.

A merged shard document sums `misses` and `writes` across the shards — the
shards partition the work, so no mutant is counted twice — and reports `on` if
any shard managed it. The cache changes no outcome, so a matrix where one runner
ran with `--cache off` is still a congruent set and `report merge` does not
refuse it.

### `mutants[]`

Each entry carries the full 64-hex `id` and the 20-hex `display_id`, `path`,
`package`, `family`, `rule`, `rule_version`, `line`, `column`, `start_byte`,
`end_byte`, `original`, `replacement`, `outcome`, `not_run_reason`,
`duration_ms`, `killed_by`, `attempts`, `output_tail`,
`covering_test_packages`, `uncovered`, and `cached`.

`cached` says the outcome was adopted from the outcome cache rather than
measured by this run, so `duration_ms`, `attempts`, `killed_by` and
`output_tail` are the ones the run that measured it recorded — reported as they
stand, because a survivor whose tail explains why it survived is worth exactly
as much second-hand. It is only ever `true` of `killed`, `survived` and
`timed-out`, never of an `uncovered` mutant, and never in a run whose
`cache.mode` is `off`.

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

`not_run_reason` says why a `not-run` mutant was not run, and is `null` for
every mutant that was measured — the pairing holds in both directions, and the
schema enforces it. There is no fourth value, because there are only three ways
a catalogued, compilable mutant goes unmeasured:

| Value | Meaning |
| --- | --- |
| `out-of-selection` | The run narrowed itself and this mutant was outside: `--mutant` named another, or `--changed` found no edited line on it |
| `other-shard` | `--shard` assigned it elsewhere, and that shard is the one that measured it. `report merge` replaces exactly these rows |
| `interrupted` | It was selected and a signal ended the run first |

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
<cache>/go-mutants/workspaces/<key>/go-mutants.marker
<cache>/go-mutants/workspaces/<key>/latest.json
<cache>/go-mutants/workspaces/<key>/runs/<run-id>.json
<cache>/go-mutants/workspaces/<key>/outcomes/<context>/<mutant-id>.json
```

A mutation run must not add files to the tree it is measuring, so nothing lands
in the workspace. `runs/` holds nothing but immutable per-run documents, which
makes listing past runs a directory listing where every entry is a real run,
and `latest.json` is a whole copy of the newest one rather than a pointer that
could dangle. Every write is temp-file plus atomic rename, so a reader sees one
complete document or the previous one, and `ReportPublished` is emitted only
after the rename succeeds.

`outcomes/` is the [outcome cache](configuration.md#cache), one directory per
cache key with one small JSON document per mutant, and `go-mutants.marker` is
what governs the whole workspace directory: a two-line file naming the format
and the full workspace digest. Both stores claim it before writing anything, and
a directory with no marker or one naming another workspace is refused rather
than written to — which turns the one failure a truncated `<key>` has, two
workspaces landing on one name, into a diagnosable error instead of two
projects' records quietly interleaving. It is also what makes `cache gc` and
`cache clean` safe to delete anything at all in a directory the whole machine
shares.

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

Produced only by `doctor --json`, and validated against
`schema/doctor-v1.schema.json` before it is printed.

| Field | Contents |
| --- | --- |
| `document_type`, `schema_version` | `go-mutants/doctor`, `1` |
| `tool_version` | The build that ran the checks |
| `checks[]` | `name`, `status`, `detail` — one row per check, in table order |

`status` is `ok`, `warn`, or `fail`. A `warn` is a check that failed on
something only an opt-in feature needs — git, which only `run --changed` asks
for — and never fails the command; any `fail` exits 2. `detail` is never empty:
the version, the path, or the reason it could not be found, which is what makes
a status something a reader can act on.

The check names are stable within the schema version, so a consumer may branch
on them: `go toolchain`, `module`, `git`, `cache directory`, `platform`,
`configuration`. The list is always complete, even when a check failed — a
machine with two problems should learn about both at once.

This document describes the machine and not any code, so it carries no run ID,
no workspace digest, and no mutants.

## Stryker projection

Written to `reports/mutation/mutation.json`, and embedded in
`reports/mutation/mutation.html`. It is the one document here that go-mutants
does not define: it belongs to the Mutation Testing Report Schema, vendored at
`schema/stryker/mutation-testing-report-schema-3.9.0.json` with its Apache-2.0
licence and a `PROVENANCE.json`.

It is therefore *not* in the `internal/schemas` registry and carries no
`document_type`. That registry maps a document type onto a schema go-mutants
publishes; this is a third-party definition go-mutants writes *against*, so it
is compiled separately — with no default draft, because the file declares
draft-07 itself and reinterpreting somebody else's schema would defeat the
purpose of vendoring it. `report validate` accordingly does not accept one.

One-way, lossy, and deterministic; never read back as state. `schemaVersion` is
`"2"` — the report format's major version, not the 3.9.0 of the npm package the
schema came from. Every projection is validated against the vendored schema
**before** it is written, and a document that fails aborts with `GOM5203`
having touched nothing, rather than emitting something that would look
authoritative.

The full status mapping — including why `NoCoverage` and `Pending` are never
emitted, why `not_run` projects as `Ignored` rather than being omitted, and the
UTF-16 column rule the coordinates obey — is in
[Stryker compatibility](stryker-compatibility.md).

## Compatibility rules

- Consumers must branch on `document_type` and `schema_version`.
- New fields are additive within a schema version; removing or retyping a field
  requires a version bump.
- Unknown fields are rejected by the schemas on purpose: a typo in a generated
  document is a bug, not a forward-compatible extension.
- Every array is `[]` when empty and never `null`. "No warnings" and "warnings
  unknown" are not the same statement, and only one of them is ever true.
