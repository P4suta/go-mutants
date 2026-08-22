<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# go-mutants 0.1.0 pre-release notes

**Nothing is published, tagged, or released.** This is the note the `v0.1.0`
tag will carry once every box in
[`docs/release-checklist.md`](docs/release-checklist.md) is ticked on one
immutable commit. It describes the tree as it stands today rather than the tree
as it is planned, which is the only useful kind of release note. Do not
describe go-mutants as production-ready.

The v1 feature set is complete. What changes between here and the tag is
evidence, not capability.

## What go-mutants does

`go-mutants run` takes a Go module and answers one question with a measurement
rather than a proxy: when your program is changed in a way that matters, do
your tests notice?

The whole pipeline runs on every invocation.

- **Snapshot.** Your workspace is copied into a disposable directory built from
  a sorted manifest that excludes `.git`, caches, and the report directory, and
  that refuses symlinks, junctions, and special files. Everything after this
  point happens in the copy.
- **Baseline.** The unmutated tests are run in the snapshot three times. A
  baseline that does not pass ends the run, because a mutation score measured
  against a failing suite is a number about nothing. The per-mutant timeout is
  derived from what those runs measured — `max(10s, slowest × 5)` — so a slow
  suite is not punished by a constant somebody guessed.
- **Discovery.** Candidates are found across all eleven operator families and
  all forty-two rules, type-aware, each with a SHA-256 identity derived from the
  source bytes and the rule rather than from a position in a list. The identity
  is what makes a cache entry, a shard assignment, and a `[[mutation.expect]]`
  entry survive an edit somewhere else in the file.
- **Validation.** Every candidate is compiled before anything is measured, and
  the ones that cannot compile are isolated by bisection and reported as
  rejections with the compiler's own words. An uncompilable mutant is noise, and
  noise that reaches the score is a score nobody can act on.
- **Instrumentation.** Every surviving mutant is rewritten into the snapshot
  **once**, as a guarded form the compiler can fold away, and the test process
  chooses which one is live through `GO_MUTANTS_ACTIVE`. The toolchain therefore
  builds essentially once for the whole run instead of once per mutant, which is
  the difference between mutation testing you leave switched on and mutation
  testing you run on Fridays.
- **Coverage.** Each test binary is profiled once, and a mutant is then measured
  only against the binaries that reach its lines. One no binary reaches is
  reported as a survivor that was never executed, with `(uncovered)` next to it,
  because "no test detected it" and "no test could have" are different findings.
- **Execution.** Mutants run in parallel workers, each with its own `TMPDIR`,
  each supervised as a whole process tree so a timeout or a Ctrl-C leaves no
  child and no grandchild behind.
- **Reporting.** The run's own `run-report-v1` document is filed into a
  per-workspace history under your OS cache directory, and
  `reports/mutation/mutation.json` and `reports/mutation/mutation.html` are
  published into your tree. The HTML is one self-contained file: it opens from
  `file://` with the network unplugged.

On a terminal, `run` draws a live dashboard — phase, score gauge, one row per
worker, a scrolling survivor feed — and prints the closing summary into the
scrollback once the screen is restored. Into a pipe, in CI, and under
`--no-tui`, `--json`, `--quiet`, or `--no-color`, it prints deterministic plain
lines instead. The summary block is byte-identical either way, because a
dashboard run replays it through the plain renderer rather than formatting its
own.

Around that pipeline sit the narrowings a real project needs. `--changed`
measures only the mutants on lines that moved since a ref; `--shard K/N` splits
a catalog across a CI matrix and `report merge` puts the shards back together
into the document an unsharded run would have written; an outcome cache keeps a
second run from re-measuring what has not moved; `--explain` says why the
catalog is smaller than you expected. `list`, `doctor`, `init`, `report`, and
`cache` round out the command tree, and all of it is built.

## The guarantees

Four properties are worth stating as promises rather than as features, because
they are what make the tool safe to point at a repository you care about.

**Your workspace is read-only.** Discovery reads it. Nothing else touches it.
Every build, rewrite, and test happens in the snapshot, and the run reports and
cached outcomes are written outside your tree entirely, into the OS cache
directory, temp-file-then-atomic-rename. The single exception is
`reports/mutation/`, which is build output and is excluded from both snapshot
and cache identity, so publishing a report can never change a digest.
Deleting is held to a stricter standard still: every directory go-mutants owns
in the cache carries an ownership marker, and `cache gc`, `cache clean`, and
`report clean` refuse any directory without one and say how many they skipped.

**The run report is lossless.** `run-report-v1` records every mutant, its
outcome, its coordinates, the diff, the attempts behind the verdict, the
rejections with their compiler messages, the suppressed sites with their
reasons, the coverage and cache accounting, and the expectations ledger. It is
the document to diagnose, resume, audit, or merge from. The Stryker projection
beside it is deliberately one-way and lossy, and
[`docs/stryker-compatibility.md`](docs/stryker-compatibility.md) states exactly
what does not survive the trip.

**Nothing is written that would not validate.** The report builder refuses a
document whose parts disagree — a survivor with no attempts, a cache hit in a
run that reports the cache off, a mutant claiming an identity the run does not
have — rather than filing it and letting a reader discover the contradiction.
The Stryker projection is built, encoded, and validated against the vendored
schema *before* the destination directory is touched, and the HTML is rendered
from those same validated bytes; a failure at any point leaves
`reports/mutation/` exactly as it was found, because a `mutation.json` from this
run beside a `mutation.html` from last week is worse than either alone.

**The optimizations fail open; the narrowings fail closed.** Coverage guidance
and the outcome cache exist to do less work, so when either cannot do its job it
stands down with a `GOM76xx` or `GOM79xx` warning and the run measures more than
it had to. Neither can change a verdict and neither can fail a run. `--changed`
is the opposite and deliberately so: it is a request to measure *less*, and a
narrowing that quietly fell back to "everything" would take twenty minutes where
one was expected while a narrowing that quietly fell back to "nothing" would exit
0 having proved nothing. It fails instead.

## What is deliberately not in v1

The honest limits, in the order they are most likely to matter to you. Every one
of them is a recorded skip with a reason rather than a silent omission, and
`go-mutants list` prints the counts.

- **No `switch`/`select` case mutation and no `if`-branch replacement.** Both
  are v2 work: each needs a guard form or a neutral-value model the instrumenter
  does not build yet.
- **Package-level `var` initializers, `const` declarations, array lengths, and
  generic type parameter lists are excluded** for reasons that are not going to
  change, and cgo packages and generated files are excluded wholesale.
- **A rewrite site none of the three guard forms can express is skipped**, as
  `unnameable-decl-type`. The common cases are a `:=` that redeclares rather
  than declares, a declared type the file cannot spell with the imports it has,
  and a statement in a `for` post or an `if` initializer, where a block is not
  legal Go.
- **Coverage guidance and the outcome cache are on only for the default test
  command.** A `test.command` other than `go test ./...` is one go-mutants can
  reason nothing about — it may consult a clock, a database, or a network — so
  both stand down and say so. `cache.mode = "on"` is how a project promises its
  own command is reproducible.
- **`--changed` needs git and a repository**, and rename detection is off, so a
  renamed file selects every mutant in it.
- **One host platform per report.** Build constraints decide which files a
  package even has, so a report is a statement about the platform it was
  measured on. There is no cross-`GOOS` matrix, and a `go.work` workspace is
  supported only for the modules the snapshot itself holds.
- **The Stryker projection is one-way.** Six outcomes become five statuses, and
  the expectations ledger, the cache accounting, and coverage do not survive.

## The compatibility surface

Semantic versioning applies to the CLI, the `.go-mutants.toml` version 1 schema,
the native `run-report-v1`, `catalog-v1`, and `doctor-v1` documents, and the
one-way Stryker projection. Go packages under `internal/` are not a library API
and never will be; `cmd/go-mutants` is a two-line `main` precisely so that the
command tree, and not a package, is the thing being promised.

Exit codes are part of that contract, printed by every `--help` as well as
documented in the README: 0 completed with no policy failure, 1 an opt-in gate
failed, 2 an infrastructure or configuration or baseline or expectation failure,
130 interrupted, 143 terminated. `strict` defaults to **false** everywhere,
terminal and CI alike. go-mutants does not fail your build unless you ask it to.

## Before this is published

Every box in [`docs/release-checklist.md`](docs/release-checklist.md) must be
ticked on a single immutable commit, with the evidence produced by
GitHub-hosted runners on Linux, Windows, and macOS rather than by a laptop.
Tagging, publishing, and turning the drafted GitHub release into a real one are
separate human decisions taken after that.
