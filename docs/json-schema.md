<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# JSON contracts

**Status: four schemas shipped, plus one vendored.**
`schema/catalog-v1.schema.json`, `schema/run-report-v1.schema.json`,
`schema/doctor-v1.schema.json` and `schema/trace-v1.schema.json` exist, are
embedded in `internal/schemas`, and every document the CLI writes is validated
against them in the tests. The Stryker projection is validated too, against the
vendored third-party schema in `schema/stryker/` — which is deliberately kept
out of that registry, for the reasons given below.

go-mutants publishes four native document types and one lossy projection for
the Stryker report ecosystem. The three that describe *results* are
discriminated by two fields that a consumer must check before decoding:

```json
{ "document_type": "go-mutants/run-report", "schema_version": 1 }
```

The fourth, `go-mutants/trace-event`, is not: a trace is a stream of lines
rather than a document, and it states its format once on its first line. See
[below](#go-mutantstrace-event-v1).

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
| `workspace` | `module_path`, `go_version`, `workspace_digest`, `platform.{os,arch}`, and the optional `snapshot` |
| `selection` | `mode`, `changed_ref`, `profile`, `operators`, `include`, `exclude`, `candidates`, `rejected`, `selected` |
| `shard` | Which shard of a split run this is, or `null`; see below |
| `merge` | Present only on a document `report merge` wrote; see below |
| `test` | `command` argv, `baseline`, `timeout_ms`, `timeout_source`, and the optional `toolchain` and `resolved_command` |
| `coverage` | `mode`, in `package` and `test` mode `binaries` and `mutants_uncovered`, in `test` mode `tests`, and the optional `build_fallback` and `unavailable_reason` |
| `cache` | `mode`, `hits`, `misses`, `writes`; see below |
| `summary` | The counters, `score_percent`, and `policy` |
| `mutants[]` | One entry per executed or not-run mutant; see below |
| `rejected[]` | Candidates the compiler refused, with diagnostics |
| `skips[]` | Reason-coded static skips, aggregated per file |
| `expectations[]` | Each `[[mutation.expect]]` row, evaluated |
| `warnings[]` | `GOMnnnn` code plus message |
| `timing` | Optional: where the run's wall clock went, phase by phase and stage by stage |
| `validation` | Optional: `builds`, how many compiles establishing the catalogue cost |

`selection.mode` is `all`, `mutant`, `changed`, or `shard`; `candidates` equals
`rejected` plus the length of `mutants[]`, and `selected` is how many the run
set out to execute.
`test.baseline` carries every unmutated observation — `runs`, `durations_ms`,
`slowest_ms` — not just the summary of them, because the derived timeout is a
function of the slowest run and a reader deserves the numbers it came from.
`test.timeout_source` is `derived` for `max(10s, slowest baseline × 5)` — the
slowest of the runs after the first, which is the one that compiles — or
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

### Run facts: `timing`, `validation`, `workspace.snapshot`, `test.toolchain`

A report should be able to answer *why was this slow* and *what actually ran
this* with nothing beside it. A trace answers both and is opt-in, kept for ten
runs, and thrown away; the report is the permanent record, so the run's own
account of its cost is in it.

Every key in this section is **optional**: a document an older build wrote does
not have it and still validates, which is why none of this needed a schema
version. Each run-level section is written when the run measured it (an
interrupted run has no `validation` to report); `mutants[].executions` is the
exception and is always present, as `[]` for a cached, uncovered or not-run
mutant. Every one of them is **omitted by `report merge`** — see the merge rule
below.

| Field | Contents |
| --- | --- |
| `timing.phases[]` | `name` and `duration_ms` for each engine phase the run finished, in the order they closed |
| `timing.stages[]` | `phase`, `name`, `duration_ms` and `result` for each step inside a phase, in the order they closed |
| `validation.builds` | How many `go build` invocations it took to establish which catalogued mutants compile |
| `workspace.snapshot.stable_dir` | The disposable copy carried the tree's own derived name, so two runs of one workspace share a Go build cache |
| `workspace.snapshot.files` | How many regular files were copied |
| `test.toolchain.go_bin` | The resolved absolute path of the `go` that compiled and ran the tests |
| `test.toolchain.version` | What `go version` printed, verbatim |
| `test.resolved_command` | The argv that was really started: `command` with `go_bin` in place of a bare `go` |
| `coverage.build_fallback` | The coverage-instrumented build failed and the run compiled plain test binaries instead |
| `coverage.unavailable_reason` | The whole failure that made it do so: the coded message and the compiler's diagnostics under it |
| `test.memory_bytes`, `test.memory_source` | The per-mutant memory bound and where it came from: `explicit`, `derived`, or `unavailable` for a run with no bound at all. Written together, and absent from a document an older build wrote |
| `mutants[].executions[]` | One row per pass over the test binaries; see [`mutants[]`](#mutants) |
| `mutants[].memory_exceeded`, `mutants[].peak_memory_bytes` | Whether the memory bound settled the mutant and what it cost; see [`mutants[]`](#mutants) |
| `mutants[].executions[].memory_exceeded` | Whether the run's memory bound stopped that pass; see [`mutants[]`](#mutants) |
| `mutants[].executions[].peak_memory_bytes` | What that pass cost the machine; see [`mutants[]`](#mutants) |
| `coverage.tests` | How many tests the coverage pass profiled on their own. Present exactly in `test` mode; see [`coverage`](#coverage) |
| `mutants[].covering_tests[]` | The tests whose own coverage reaches the mutant, as `{package, name}`; written by a `test` run when there are any |
| `mutants[].executions[].tests[]` | The tests that pass was narrowed to, as `{package, name}`; absent when every binary ran whole |

`timing.stages[].result` is `succeeded`, `failed`, or `skipped` — the trace's
own vocabulary, so a reader holding both documents is not reconciling two
accounts of one run, and the durations are the same numbers the recording's
`phase-end` and `stage` events carry. A `failed` stage is not a failed run: a
coverage-instrumented build that will not compile is exactly that, and the run
carries on. The phases do not add up to `duration_ms`, because a run is more
than its phases, and the report is written *during* the `report` phase — so the
phase it is in and its own stages are in the recording and not in the document.

`test.toolchain` is not `workspace.go_version`: that is the `go` directive of
the module under test, and this is the executable that ran it. Under a
toolchain manager the two disagree, and only one of them says which `go` to
blame. `workspace.snapshot` is about the *copy* rather than about the code,
which is why neither field is in `workspace_digest`: a run whose copy could not
take the stable name pays for a cold build cache, and that is a fact about the
machine's temporary directory.

`coverage.build_fallback` and `coverage.unavailable_reason` are absent — not
`false` and `null` — from the runs that did not fall back, which is nearly all
of them. They are the same event as the `GOM7602` warning beside them, at the
length a person investigating needs rather than the one line a console prints.

**`report merge` omits every field in this section, and every mutant's
`executions`.** A merged document is four shards from four machines: there is
no single timeline, no single validation bisection, no one snapshot, no one
toolchain, and no worker that executed a given mutant. Reporting the first
shard's would present one machine as the run — and unlike the baseline timings,
which are taken from the first shard and documented as such, these are the
fields somebody reads *precisely* to explain a cost. Each shard's own document
still carries all of it.

### `coverage`

`coverage` is an object rather than a bare string, which is what let
coverage-guided selection arrive as `mode: "package"` without any consumer
having to learn a new top-level shape.

| Field | Contents |
| --- | --- |
| `mode` | `off`, `package` or `test` |
| `binaries` | How many test binaries were profiled. Present only in `package` and `test` mode |
| `tests` | How many tests were profiled on their own, summed over the binaries. Present only in `test` mode |
| `mutants_uncovered` | How many entries in `mutants[]` carry `uncovered: true`. Present only in `package` and `test` mode |

`off` means every selected mutant was measured against every test binary.
`package` means each test binary was profiled once and every mutant was
measured only against the binaries whose profile reaches its lines. `test`
means every test of every binary was profiled on its own and every mutant was
measured only against the tests whose profile reaches its lines, each binary
started with those tests selected; it is what `test.narrowing = "test"`, the
default, produces, and `"package"` produces the mode of the same name.

The numbers are absent — not zero — outside the modes that measure them, and
the schema refuses them there. An `off` run carrying `binaries: 0` would be
stating a measurement it never made, and a reader cannot tell a real zero from
a default one. `mutants_uncovered` is derived from `mutants[]` when the
document is built,
so the summary and the rows underneath it cannot disagree.

`mode` is `package` or `test` exactly when the effective `test.command` is one
go-mutants reads as a scope — `go test` followed only by package patterns, the built-in
`go test ./...` included — **and** the coverage pass succeeded. Anything else
turns it off with a `GOM7601` warning, because the mapping is from a test binary
to the lines it reached and there is no honest way to attribute an opaque
command's coverage to go-mutants' own per-package binaries. The `test.command`
the document already records verbatim is the scope: no field is added for it,
because the command *is* the statement of which packages were measured. Any
failure of the pass itself
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
`covering_test_packages`, `uncovered`, and `cached`. Three more keys are
optional: `branch`, which appears only on the mutants go-mutants could prove
something extra about — see [`branch`](#branch) below — `executions`, and
`covering_tests`, which a `test`-mode run writes for every mutant some test
reaches: the tests whose own coverage reaches its lines, as `{package, name}`
sorted by package and then name. `covering_test_packages` stays what it was in
every mode, so a consumer that folds tests to binaries and one that never
learned about tests read the same list.

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

`executions[]` is `attempts` in detail: one row per pass this run made over the
test binaries, in attempt order.

| Field | Contents |
| --- | --- |
| `attempt` | Which pass this was, counting from 1, and the row's own position |
| `worker` | The scheduler slot that made it, counting from 0; the serial timeout retry is worker 0 |
| `outcome` | What this pass observed: `killed`, `survived`, `timed-out` or `errored`. The schema's enum here is narrower than a mutant's on purpose — `inconclusive` is a judgement about two passes and `not-run` is what a mutant nobody measured is, and neither is a thing one pass can see |
| `killed_by` | The binary that detected the mutant on this pass; absent when it detected nothing |
| `duration_ms` | The wall-clock time this pass took, summed over the binaries it ran |
| `binaries[]` | The test binaries it started, in launch order, stopping where the pass stopped |
| `tests[]` | The tests the pass was narrowed to, as `{package, name}`: the binary was started with exactly these selected. Absent when every binary ran whole, which is every pass outside `test` mode |
| `memory_exceeded` | This pass was stopped by the run's per-mutant memory bound rather than by a test failing or by the deadline; absent when it was not. Optional. The bound itself is `test.memory_bytes` |
| `peak_memory_bytes` | The highest the pass was observed to hold, as the **maximum over every binary it started** rather than the deciding binary's: resident memory on Unix, committed charge on Windows, which are close but not the same quantity and are deliberately not converted into one another. Written for every pass and not only the bounded ones — every process is sampled, which on Linux is the only measurement that is the child's own; absent where nothing observed one, which includes a pass that ended before its first sample |

The same two facts are on the **mutant** as well as on its rows, and the
repetition is for one reader: a `cached` mutant has an attempt count and no rows,
because this run started no process for it, so a consumer looking only at the
rows would see a kill it could not explain. For a mutant this run executed
`mutants[].peak_memory_bytes` is the maximum over its rows and
`mutants[].memory_exceeded` is true when any of them tripped the bound; for a
cached one they are what the run that measured it recorded. Both are dropped by
`report merge`, which describes no machine and reports no bound.

`memory_exceeded` is why a row can say `killed` and name a binary whose tests
did not fail. A mutant that turns a terminating loop into one that allocates
forever is not caught by any timeout short enough to be useful — it takes the
machine first — so go-mutants bounds a mutant's memory the way it bounds its
time, at `max(1GiB, largest baseline peak × 4)` unless `test.memory` says
otherwise. The outcome vocabulary does not grow for it: the original program was
measured under the budget the bound was derived from, so a tree needing several
times what the whole suite needed has been changed observably, and that is what
`killed` already means. See
[ADR 0009](adr/0009-a-mutant-is-bounded-in-memory-as-in-time.md).

There are exactly `attempts` rows for a mutant this run executed, and the list
is `[]` — present and empty — for a `cached`, `uncovered` or `not-run` mutant:
none of them had a process started for it by this run. Two of those keep an
attempt count with no rows under it — a cached mutant keeps the count of the run
that *did* measure it, and a mutant that timed out once and was interrupted
before the serial retry reports the one pass it made — and they are the only
ways the two numbers differ. A confirmed timeout is two rows that both timed
out; an `inconclusive` mutant is a row that timed out and a row that did not,
which is the shape no summary could show. The whole key is absent from a
document an older build wrote, and from a merged one.

The schema cannot state any of that: JSON Schema can require the rows to be
well formed, and it cannot say that a `cached` mutant must have none of them or
that a run's row count must equal its own `attempts`. `report.Build` enforces
both and refuses to write a document that breaks either (`GOM5121`), so no
go-mutants run can produce one — but a hand-edited file that claims three
executions for a cached mutant is still a *valid* run-report v1, and a consumer
that cares should check the pairing rather than assume it.

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

### `branch`

`branch` is optional in both documents that carry a mutant — the run report and
the catalog — and it appears only on a mutant whose edit go-mutants proved can
only make the condition of an `if` or a `for` *less* often true. It is absent
rather than `null` when there is no proof, so a decoder sees a missing key.

| Field | Contents |
| --- | --- |
| `direction` | `decreasing`: the mutated condition implies the original one on every evaluation |
| `body_start_line`, `body_start_column` | The body's opening brace |
| `body_end_line`, `body_end_column` | The body's closing brace, inclusive |

The coordinates are 1-based lines and 1-based **byte** columns of the pristine
file, the same as `line` and `column`, and they are the coordinates
`go test -coverprofile` reports its statement blocks in. What they promise is
this: **a test during which no statement of that body executed cannot
distinguish the mutant from the original program**, so it does not have to be
executed against it. A consumer holding per-test coverage can therefore
discharge tests without running them.

`direction` is diagnostic. It names the lemma the span was derived from, and a
consumer must not branch on it: a later release may prove the same span from a
different lemma, and the promise above is about the span alone.

Why the braces rather than the first statement is
[in the operator reference](operators.md#branch-proof), together with the
conditions a proof has to satisfy before go-mutants will state it.

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
| `mutants[]` | Identity and coordinates only — no outcome — plus the optional [`branch`](#branch) |
| `skips[]` | The same `path`/`reason`/`count` shape |

A catalog is not a run report: it has no outcomes, no summary, no test output,
and no run ID, because nothing was executed to produce one. Its `mutants[]`
entries stop at `replacement` — plus the optional [`branch`](#branch), which is
a fact about the source and not about a run — and it records the profile
separately from the selection patterns so that two catalogs from the same tree
can be compared byte-for-byte as a determinism gate.

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

## `go-mutants/trace-event` v1

One line of a run trace, validated against `schema/trace-v1.schema.json` and
returned by `trace.JSONSchema()`.

It is the odd one out in three ways, all deliberate. It describes a *line*
rather than a file, because a recording is JSON Lines and each event is one
instance of the schema. It carries no `document_type` or `schema_version`
field — a recording states its format once, in the `schema` field of its first
line, rather than on every one of thousands of events. And it is never
evidence: a trace takes no part in a verdict, in a mutant identity, or in the
key a cached result is stored under, and a recording that cannot be written
costs a note rather than the run.

| Field | Contents |
| --- | --- |
| `seq` | Position in the recording, counting from one |
| `type` | Which event this is, and therefore which payload it carries |
| `schema` | `gomutants-trace-v1`, on the `run-start` line alone |
| `timestamp`, `elapsed_ms` | When it was recorded, and how far into the recording |
| one payload | Named after its type: `start`, `phase`, `stage`, `prepare`, `exec`, `mutant`, `probe`, `validate`, `coverage`, `cache`, `snapshot`, `sweep`, `artifact`, `note`, or `run` |

The type and the payload are one contract in both directions, so a reader may
switch on `type` and reach for that payload alone. `exec.kind` is an
enumeration of every command go-mutants starts, which is what makes "every
subprocess is recorded" checkable; `env_names` carries variable names without
their values, and the schema refuses an item containing `=`.

The full contract — every type, every field, the retention rule, what is
deterministic, and how a recording joins a consumer's own — is
[the trace format](trace-v1.md). The reasoning is
[ADR 0001](adr/0001-trace-is-not-evidence.md).

## Compatibility rules

- Consumers of the result documents must branch on `document_type` and
  `schema_version`. A trace consumer reads the format identity from the
  `schema` field of the `run-start` line instead.
- New fields are additive within a schema version; removing or retyping a field
  requires a version bump.
- Unknown fields are rejected by the schemas on purpose: a typo in a generated
  document is a bug, not a forward-compatible extension.
- Every array is `[]` when empty and never `null`. "No warnings" and "warnings
  unknown" are not the same statement, and only one of them is ever true.
