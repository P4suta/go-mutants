<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Architecture

**Status: partially implemented.** The pure packages, the strict
configuration decoder, the snapshot, the baseline execution layer, discovery
for the whole eleven-family catalogue, guard-based instrumentation with its
generated runtime in all three forms, compile validation, mutant execution,
coverage-guided selection,
`RunReport v1` with its history store, the live TUI dashboard, `--changed`,
`--shard` with `report merge`, `--explain`, the outcome cache, and the `list`,
`run`, `report` and `cache` commands exist. The HTML report and the Stryker
projection do not. Each section below marks which of
the two it is; nothing here should be read as a description of working
software until its status says so.

## Invariants

Four invariants shape every decision. They describe what the design holds
itself to, not what every phase already does; all four are load-bearing in
every run today.

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
baseline build + test             (timeout derivation)
            |
            v
batch compile + delta debugging   (rejected[] with diagnostics)
            |
            v
instrumented baseline             (no mutant active: meaning preserved)
            |
            v
profile each test binary once     (coverage-guided selection)
            |
            v
worker pool over one snapshot     (per-process activation, tree kill)
            |
            v
outcome cache + RunReport v1      (JSON, HTML, history, exit policy)
```

Every transition on this line is what `run` performs today, and discovery on
its own is what `list` prints. One annotation above is still a promise: the HTML
report does not exist.

Each arrow is a phase transition with its own type. `runner.Execute(m
Validated)` cannot be called with a raw candidate; that is the compile-time
version of the rule "only validated mutants run".

## Instrumentation: guard-based rewriting

Status: implemented in `internal/instrument`. This is the hardest component and
the reason for most of the other decisions. Which of the spliced mutants
actually compile is not decided here; see
[Compile validation](#compile-validation).

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

  `T` comes from `types.TypeString` with an import-qualifier map, and it is
  discovery that computes it: the type information the qualifier needs is
  there and nowhere else, so every candidate carries the form, the site span,
  and any declared types down to instrumentation as a hint. A type that cannot
  be named is a recorded skip with reason `unnameable-decl-type`, never a
  silent omission — and so is every other site none of the three forms can
  express; see [Operators](operators.md).

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

Status: implemented. The runtime lives inside the snapshot at
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

## Compile validation

Status: implemented in `internal/validate`. Instrumentation is a byte rewrite
that leaves typing to the compiler, so a few guarded sites cannot compile — a
mutated copy can be a program the compiler refuses, as `x * 0` swapped into
`x / 0` is a constant division by zero. The compiler is this phase's oracle,
and the phase's job is to ask it precisely enough that one bad candidate costs
one candidate rather than a file or a run.

- **The fast path is one build.** Every catalogued mutant is spliced in at
  once and `go build ./...` accepts the lot. That is the ordinary case, and
  making it ordinary is the whole point of the schemata design.
- **A red build starts a bisection.** Every catalogued file is restored to its
  pristine bytes and rebuilt first: a failure there is not mutant-induced and
  stops the run rather than blaming whichever candidate was tested first. The
  files the compiler named are then searched one at a time — halving while
  halving is cheaper than scanning, verifying every join, and falling back to
  a scan when a join fails, so a pair of candidates that only fail together is
  an ordinary case rather than a wrong answer.
- **The generated runtime is never regenerated.** Its activation array is
  sized by the full catalog and every guard spells its own dense index, so a
  runtime rebuilt from a subset would renumber flags that other files read.
- **Rejections are data, not failures.** A candidate that will not compile
  comes back as a `rejected[]` entry carrying its identity, its coordinates,
  and the compiler's own words, captured at the moment of rejection — by the
  time the phase finishes the tree compiles and that message exists nowhere
  else. Dropping such a candidate silently would quietly shrink the catalog
  between runs with no record of what left it.

## Execution

Status: implemented. The whole pipeline runs — snapshot, baseline, discovery,
compile validation, the instrumented baseline, the drift gate, per-mutant
activation, the report, and the exit code — so `go-mutants run` measures a real
mutation score today, narrowed by coverage, by the selection stage below, and by
the outcome cache.

- **One build.** Each package with tests is compiled once with `go test -c`;
  packages with no test files are skipped. `-cover -coverpkg=<module>/...` is
  added whenever coverage-guided selection is on, and the same binaries serve
  both the profiling pass and every mutant — there is no second, non-cover
  build. That is not free: a `-cover` test binary runs its coverage teardown on
  every exit whatever `-test.gocoverdir` says, which measured at roughly 6 ms
  per run on a three-file fixture and 8-16 ms on this repository's own
  `internal/mutation` binary. Two builds of one tree would cost more.
  `--race`, when requested, applies to that build and to the baseline so the
  derived timeout stays consistent.
- **Direct binary launch.** Test binaries are executed directly, bypassing the
  `go test` result cache entirely, with the working directory set to the
  package directory inside the snapshot so `testdata` paths behave.
- **One shared snapshot.** Activation is per-process, so N workers share it. A
  test that writes into its package directory is caught by re-digesting the
  manifest after the instrumented baseline; drift is exit 2 with the offending
  files listed. `--isolate` is reserved as the per-worker escape hatch.
- **Timeouts.** Explicit, or `max(10s, slowest baseline × 5)`. A first timeout
  is not evidence: N test binaries on a loaded machine produce timeouts that
  say nothing about the mutant, and counting one as a detection would inflate
  the score exactly when the run is least able to notice. So every timed-out
  mutant is held back and retried **serially** after the queue drains, with
  nothing else running. Two in a row are a confirmed detection; a retry that
  finishes — pass or fail — is `inconclusive`, which counts in neither
  direction. Both attempts are kept in the report. Process trees are killed
  through a Windows Job Object with
  `KILL_ON_JOB_CLOSE` (fail-closed if ownership cannot be established) or a
  POSIX process group `TERM` then `KILL`.
- **Coverage-guided selection.** *Implemented.* The test binaries are built
  with `-cover -coverpkg=<module>/...` and each is then run once with nothing
  activated and `-test.gocoverdir` pointed at a directory of its own — the
  flag, never the `GOCOVERDIR` environment variable, which a *test* binary does
  not read. `go tool covdata textfmt` blocks are mapped to mutants by
  line-interval overlap only: columns describe the instrumented text while a
  mutant's span was measured against the user's own bytes, and only the lines
  agree. The over-approximation errs toward running a binary rather than
  missing a kill. A mutant no binary reaches is not executed at all and is
  reported as `survived (uncovered)`.

  Two rules bound it. Narrowing is auto-on only for the built-in
  `go test ./...` and off with a `GOM7601` warning for any other
  `test.command`, because an opaque command's coverage cannot be attributed to
  go-mutants' own per-package binaries. And every failure of the pass —
  including a `-cover` build that will not compile — publishes a `GOM7602`
  warning and runs everything, so the optimisation can never fail a run.
- **`--changed [=<ref>]`.** *Implemented.* It intersects candidates with the
  `git diff -U0` line set taken against `git merge-base <ref> HEAD`, unioned
  with every untracked, unignored file
  (`git ls-files --others --exclude-standard`) as the whole of itself — a file
  with no index entry has nothing to be diffed against, so the diff alone would
  see an edited line and miss a file written from scratch. It
  is read from the *original* workspace — a snapshot excludes `.git`, so there
  is no repository in one. Bare `--changed`, and `--changed=@{upstream}` written
  out longhand, both follow the upstream of `HEAD` and record it by name; a
  branch with no upstream is `GOM7712` rather than a merge base that cannot
  resolve. Discovery and validation still run over the whole module, so the IDs
  and `rejected[]` match a full run's and the two documents can be compared
  mutant for mutant. Unlike coverage guidance it fails closed: a diff that
  cannot be read stops the run, because a narrowing that silently measured
  everything or nothing is worse than not running at all.
- **`--shard K/N`.** *Implemented.* It assigns by `sha256(ID)[:8] % N + 1`,
  published as `shard.assignment: "id-hash-v1"`, so adding or removing mutants
  elsewhere does not reshuffle a shard. Each shard emits a complete report with
  the other shards' mutants marked `not-run` with
  `not_run_reason: "other-shard"`, and
  `report merge` verifies congruence (tool version, workspace digest, module
  path, catalog ID sequence, changed ref, matching `N`, every index exactly
  once, and every row owned by the shard that reported it) before merging; a
  mismatch is exit 2 naming the first discrepancy. The two compose: a shard of a
  `--changed` run narrows by both, reports `mode: "shard"` with a `changed_ref`,
  and merges into a `changed` document.
- **Everything not executed says why.** *Implemented.*
  `mutants[].not_run_reason` is `out-of-selection`, `other-shard`, or
  `interrupted`, and is `null` for every mutant that was measured — which is
  what keeps a narrowed run's report a complete statement about the catalogue
  rather than a fragment of one.

## Stable identity

Status: implemented. A mutant ID is a SHA-256 over length-prefixed fields: the
normalized relative path, the versioned rule name, the byte span, the source
digest, and the digests of the original and replacement bytes. Absolute paths
and snapshot locations never participate. The CLI shows a collision-checked
20-hex prefix; JSON always carries the full identity.

## Score and exit policy

Status: implemented. The score function and the exit-code mapping live in
`internal/mutation`, and the run feeds them from the report it just wrote, so
the number a user reads and the gate that failed cannot disagree.

```text
score = (killed + confirmed_timeouts) / denominator
```

The denominator excludes expected survivors, inconclusive results, errors, and
not-run mutants — every category that is a signal about the run rather than
about the tests. It is `null`, printed as `score N/A`, when that denominator is
zero: both plausible sentinels are lies, since 0 reads as "your tests caught
nothing" and 100 as "your tests caught everything" when the truth is that
nothing was measured. Exit codes are 0, 1 (opt-in policy failure only), 2
(infrastructure, config, baseline, stale expectation), 130, and 143.

## Reporting and the event stream

Status: partially implemented. The event stream, both console renderers,
`RunReport v1`, its history store, and `report merge` exist and carry a whole
run today; the HTML report and the Stryker projection are planned. The
engine never draws. It publishes to a single `chan engine.Event` (a sealed
interface): `RunPlanned`, `PhaseChanged`, `BaselineProgress`,
`BaselineCompleted`, `Discovered`, `Validated`, `CoverageMapped`,
`MutantStarted`, `MutantFinished`, `CacheHit`, `Warning`, `ReportPublished`
(only after the atomic rename), and a terminating `RunCompleted`.
`CoverageMapped` is published only by a run that narrowed itself; one with
coverage off publishes the `GOM76xx` `Warning` saying why instead. A `CacheHit`
is the accounting for one mutant answered from the cache, and the
`MutantFinished` carrying the outcome follows it immediately — with no
`MutantStarted` before either, because nothing started. Publishing both is what
keeps a renderer's counts and the report's in step, exactly as an uncovered
mutant's lone `MutantFinished` does. A `Renderer` interface has two implementations:
the bubbletea dashboard and deterministic plain lines. The TUI is selected only
when standard output is a terminal that can do better than ASCII and
`--no-tui`, `--json`, `--quiet`, `--no-color`, `NO_COLOR`, and `CI` all say
otherwise; anything else gets the plain lines. The final summary is
byte-identical between the two: a dashboard run replays its warnings and its
closing block through the plain renderer itself, once the alternate screen has
been restored.

`RunReport v1` is the lossless source of truth; the Stryker projection and the
HTML report are one-way, deterministic derivations of it. History is kept
outside the workspace, under the OS cache directory at
`<cache>/go-mutants/workspaces/<key>/runs/<run-id>.json`, with `latest.json`
holding a whole copy of the newest rather than a name that could dangle — a
mutation run must not add files to the tree it is measuring, and
`runs/` then holds nothing but immutable per-run documents. Every write is
temp-file plus atomic rename, and `ReportPublished` is emitted only after the
rename succeeds. See
[JSON contracts](json-schema.md) and
[Stryker compatibility](stryker-compatibility.md).

## Outcome cache

Status: implemented. `internal/cache` files one small JSON document per mutant
under `<cache>/go-mutants/workspaces/<key>/outcomes/<context>/`, beside the run
history and under the same ownership marker, claimed through
`report.History.Claim` rather than through a second copy of the same dance.

The `<context>` is a SHA-256 over length-prefixed fields — the tool version, the
running executable's digest, the Go toolchain's own release, the workspace
digest, the catalogue digest, the test command, the configured timeout, and
`CGO_ENABLED`/`GOARCH`/`GODEBUG`/`GOEXPERIMENT`/`GOFLAGS`/`GOOS` — truncated to
16 hex characters. Entries are *filed* under it rather than validated against
it, which is why nothing is ever invalidated: an edit moves the key, so the old
entries become unreachable rather than wrong, and every entry carries the id and
the *full* key it was written under — not the truncation, which two colliding
contexts would agree about — so a truncation collision is a refusal instead of
an adoption.

The toolchain's release is in the key because nothing else in that list carries
it: the test command is hashed as the user wrote it, so the default command
hashes the word `go` rather than the toolchain substituted for it at exec time,
and `go.mod` pins a language version and not a patch release.

Two decisions are worth stating. The effective timeout is judged rather than
keyed on: a derived bound is `max(10s, slowest baseline × 5)`, a wall-clock
measurement, so hashing it would have given every run of a non-trivial project
its own empty directory; each entry records the bound it was measured under and
a lookup refuses one that bound could not have produced. And the partition runs
*after* coverage narrowing, so a mutant no test covers is settled before the
cache is consulted — the coverage pass fails open, and a cached
`survived (uncovered)` adopted by a run that would have executed the mutant
would be a detection nobody performed.

Only killed, survived, and confirmed timed-out are stored; inconclusive results,
harness errors, interruptions, and every mutant named in `[[mutation.expect]]`
are measured on every invocation. Every failure in the stage is a `GOM79xx`
warning and a run that measures more than it had to, exactly as coverage fails
open — the exception being `cache status|gc|clean`, where operating on the cache
is the whole of what was asked for and a failure is an error.

## Package layout

| Package | Responsibility | Status |
| --- | --- | --- |
| `cmd/go-mutants` | Thin main | implemented |
| `internal/cli` | cobra tree, flag validation, GOM errors, exit codes | `run`, `list` |
| `internal/config` | Strict TOML decode and precedence merge | implemented |
| `internal/mutation` | Pure: spans, stable IDs, rules, catalog, score | implemented |
| `internal/interval` | Pure: interval forest | implemented |
| `internal/glob` | Pure: `**` glob semantics, fuzzed | implemented |
| `internal/discover` | `packages.Load`, types walk, candidates, skips | 2 families |
| `internal/instrument` | Forms S/C/D, flattener, runtime codegen, splicer | implemented |
| `internal/snapshot` | Manifest, digests, link rejection, cleanup | implemented |
| `internal/gocmd` | `go build`, `go test -c`, `go tool covdata` | build, test |
| `internal/runner` | One process, timed and supervised; tree kill | implemented |
| `internal/coverage` | covdata textfmt parsing, line overlap mapping | implemented |
| `internal/cache` | Outcome cache: key, store, mode, `gc` | implemented |
| `internal/validate` | One build, then bisection; rejections with diagnostics | implemented |
| `internal/execute` | Test-binary build, scheduling, timeout retry | implemented |
| `internal/report` | RunReport, projections, HTML, history, merge | v1, history |
| `internal/engine` | Orchestration, typestate pipeline, events | implemented |
| `internal/console` | Deterministic plain-line renderer | implemented |
| `internal/tui` | The bubbletea dashboard | implemented |
| `internal/schemas` | Embedded JSON Schemas, test-time validation | catalog, run report |

Pure packages have no filesystem or process access, which is what makes the
golden ID vectors and property tests meaningful.

## Documented v1 limitations

No `switch`/`select` case mutation, no cgo packages, no package-level `var`
initializers, no cross-`GOOS` matrix (a run describes the host configuration),
and `go.work` support limited to `use` directives inside the snapshot.
