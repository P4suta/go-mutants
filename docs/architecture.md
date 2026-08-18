<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Architecture

**Status: this repository is a scaffold.** The design below is decided and is
what the implementation phases build; almost none of it exists in code yet.
Each section marks what is implemented today. Nothing here should be read as a
description of working software until its status says so.

## Invariants

Four invariants shape every decision:

1. **The target workspace is read-only.** Discovery reads it; every build,
   edit, and test happens inside a disposable snapshot that excludes `.git`,
   caches, and the report directory, rejects symlinks and junctions, and is
   described by one sorted manifest.
2. **Instrumentation happens once.** All compilable mutants for a run are
   spliced into the snapshot in a single pass, and the environment variable
   `GO_MUTANTS_ACTIVE=<64-hex id>` activates exactly one of them per test
   process. The build is effectively performed once, not once per mutant.
3. **Bytes, not pretty-printed syntax.** Mutants are assembled from original
   source byte slices, so comments, whitespace, and CRLF survive untouched, and
   splices preserve line numbers so coverage line data maps one-to-one.
4. **Phases are types.** The pipeline is a typestate: a value that has not
   passed validation cannot be handed to the runner, because the runner's
   signature does not accept it.

## Pipeline

```text
Discovered → Snapshotted → BaselinePassed → Validated → Instrumented → Completed
```

```text
packages.Load + types walk        (candidates, skips with reasons)
            |
            v
sorted snapshot + manifest digest (symlink/junction rejected)
            |
            v
baseline build + test             (timeout derivation, coverage profile)
            |
            v
batch compile + delta debugging   (rejected[] with diagnostics)
            |
            v
instrumented baseline             (no mutant active: meaning preserved)
            |
            v
worker pool over one snapshot     (per-process activation, tree kill)
            |
            v
outcome cache + RunReport v1      (JSON, HTML, history, exit policy)
```

Each arrow is a phase transition with its own type. `runner.Execute(m
Validated)` cannot be called with a raw candidate; that is the compile-time
version of the rule "only validated mutants run".

## Instrumentation: guard-based rewriting

Status: planned. This is the hardest component and the reason for most of the
other decisions.

A generic helper (`__gm.Arith(id, OpAdd, a, b)`) was rejected: it breaks on
untyped constants, shift operands, and named types. Instead both branches are
written so the **compiler** type-checks each one in its original context.
Evaluation order and short-circuiting are preserved because only one side ever
executes.

- **Form S — statement guard.** For assignments, expression statements,
  `return`, `++`/`--`, sends, `defer`, and `go`:

  ```go
  if __gm.M[7] { <mutated copy, flattened> } else { <original bytes> }
  ```

  Several mutants at one site chain with `else if __gm.M[12]`.

- **Form C — boolean selector.** For `if`/`for` conditions and other boolean
  contexts:

  ```go
  (__gm.M[3] && (<mutated condition>) || !__gm.M[3] && (<original condition>))
  ```

  Short-circuiting alone selects the side; there is no allocation.

- **Form D — declaration rewrite.** For `:=` and `var` initializers:

  ```go
  var x T; if __gm.M[9] { x = <mutated RHS> } else { x = <original RHS> }
  ```

  `T` comes from `types.TypeString` with an import-qualifier map. A type that
  cannot be named is a recorded skip with reason `unnameable-decl-type`, never
  a silent omission.

**Flattening.** The mutated copy is re-tokenized with `go/scanner` and explicit
semicolons are inserted where automatic semicolon insertion would have applied,
so the copy fits on one physical line. Line comments are dropped, block
comments kept. The original side stays byte-identical in the `else` branch, so
every line number in the file is unchanged.

**Interval forest.** Spans are grouped under their innermost enclosing rewrite
site. Within a site, mutants are siblings (each copy is the pristine original
plus its own patch); across sites they nest, and splicing is innermost-first
through an offset map. Code growth is proportional to the total size of
rewritten statements, not to the number of mutants times file size.

## Generated runtime package

Status: planned. The runtime lives inside the snapshot at
`<module>/gomutants_rt/`. It cannot live in a `_`- or `.`-prefixed directory,
because the Go tool ignores those. A name collision bumps a suffix.

It exports `var M [N]bool` — a dense array in catalog order — and a map from
full ID to index. Its `init` reads `GO_MUTANTS_ACTIVE`: empty means every entry
stays false, which is exactly the instrumented baseline; an unknown ID calls
`os.Exit(97)` so a stale catalog can never masquerade as a clean baseline, and
the runner classifies 97 as an infrastructure error. Because the package is
first-party, no `go.mod` edit is needed and vendor mode is undisturbed. Writes
to `M` happen only during `init`, which happens-before test code, so the
dispatch is a plain array load and the race detector stays quiet.

## Execution

Status: planned.

- **One build.** Each package with tests is compiled once with
  `go test -c -cover -coverpkg=<module>/...`. `--race`, when requested, applies
  to that build and to the baseline so the derived timeout stays consistent.
- **Direct binary launch.** Test binaries are executed directly, bypassing the
  `go test` result cache entirely, with the working directory set to the
  package directory inside the snapshot so `testdata` paths behave.
- **One shared snapshot.** Activation is per-process, so N workers share it. A
  test that writes into its package directory is caught by re-digesting the
  manifest after the instrumented baseline; drift is exit 2 with the offending
  files listed. `--isolate` is reserved as the per-worker escape hatch.
- **Timeouts.** Explicit, or `max(10s, slowest baseline × 5)`. A first timeout
  is retried serially: two in a row are a confirmed detection, a single one is
  `inconclusive`. Process trees are killed through a Windows Job Object with
  `KILL_ON_JOB_CLOSE` (fail-closed if ownership cannot be established) or a
  POSIX process group `TERM` then `KILL`.
- **Coverage-guided selection.** The baseline runs with `-test.gocoverdir`, and
  `go tool covdata textfmt` blocks are mapped to mutants by line-interval
  overlap only. Columns are not preserved in that format, so they are not used;
  the over-approximation errs toward running a binary rather than missing a
  kill. Test packages that cover nothing relevant are not executed and their
  mutants are reported as `survived (uncovered)`. If coverage cannot be parsed
  the engine fails open and runs everything.
- **`--changed <ref>`** intersects candidates with `git diff -U0` line sets
  from `git merge-base`, while discovery and validation still run over the
  whole module so IDs and `rejected[]` match a full run.
- **`--shard k/n`** assigns by `sha256(ID)[:8] % n`, so adding or removing
  mutants elsewhere does not reshuffle a shard. Each shard emits a complete
  report with other shards marked `not-run (other-shard)`, and `report merge`
  verifies congruence (tool version, workspace digest, catalog ID set, matching
  `n`, disjoint and complete) before merging; a mismatch is exit 2.

## Stable identity

Status: planned. A mutant ID is a SHA-256 over length-prefixed fields: the
normalized relative path, the versioned rule name, the byte span, the source
digest, and the digests of the original and replacement bytes. Absolute paths
and snapshot locations never participate. The CLI shows a collision-checked
20-hex prefix; JSON always carries the full identity.

## Score and exit policy

Status: planned.

```text
score = (killed + confirmed_timeouts) / denominator
```

The denominator excludes expected survivors, inconclusive results, errors, and
not-run mutants — every category that is a signal about the run rather than
about the tests. Exit codes are 0, 1 (opt-in policy failure only), 2
(infrastructure, config, baseline, stale expectation), 130, and 143.

## Reporting and the event stream

Status: planned. The engine never draws. It publishes to a single
`chan engine.Event` (a sealed interface): `RunPlanned`, `PhaseChanged`,
`BaselineProgress`, `MutantStarted`, `MutantFinished`, `CacheHit`, `Warning`,
`SkipRecorded`, `ReportPublished` (only after the atomic rename), and a
terminating `RunCompleted`. A `Renderer` interface has two implementations: the
bubbletea dashboard and deterministic plain lines. The TUI is selected only
when the output is a TTY and CI, `NO_COLOR`, `--quiet`, `--json`, and
`--log-format json` all say otherwise. The final summary is byte-identical
between the two.

`RunReport v1` is the lossless source of truth; the Stryker projection and the
HTML report are one-way, deterministic derivations of it. See
[JSON contracts](json-schema.md) and
[Stryker compatibility](stryker-compatibility.md).

## Package layout

| Package | Responsibility | Status |
| --- | --- | --- |
| `cmd/go-mutants` | Thin main | stub |
| `internal/cli` | cobra tree, flag validation, GOM errors, exit codes | planned |
| `internal/config` | Strict TOML decode and precedence merge | planned |
| `internal/mutation` | Pure: spans, stable IDs, rules, catalog, score | planned |
| `internal/interval` | Pure: interval forest | planned |
| `internal/glob` | Pure: `**` glob semantics, fuzzed | planned |
| `internal/discover` | `packages.Load`, types walk, candidates, skips | planned |
| `internal/instrument` | Forms S/C/D, flattener, runtime codegen, splicer | planned |
| `internal/snapshot` | Manifest, digests, link rejection, cleanup | planned |
| `internal/gocmd` | `go build`, `go test -c`, `go tool covdata` | planned |
| `internal/runner` | Worker pool, timeout retry, process supervisors | planned |
| `internal/coverage` | covdata textfmt parsing, line overlap mapping | planned |
| `internal/cache` | Outcome cache | planned |
| `internal/report` | RunReport, projections, HTML, history, merge | planned |
| `internal/engine` | Orchestration, typestate pipeline, events | planned |
| `internal/tui`, `internal/console` | The two renderers | planned |

Pure packages have no filesystem or process access, which is what makes the
golden ID vectors and property tests meaningful.

## Documented v1 limitations

No `switch`/`select` case mutation, no cgo packages, no package-level `var`
initializers, no cross-`GOOS` matrix (a run describes the host configuration),
and `go.work` support limited to `use` directives inside the snapshot.
