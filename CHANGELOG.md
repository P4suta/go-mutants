<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Changelog

All notable changes are documented here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project follows
Semantic Versioning for its CLI, its TOML configuration, and its JSON schemas.
Entries say *why* a change was made, not only what changed.

## [Unreleased]

### Added

- The repository scaffold: module `github.com/P4suta/go-mutants` targeting
  Go 1.26, a stub `cmd/go-mutants` that prints its version, and the complete v1
  dependency set recorded in `go.mod` ahead of the packages that will import
  it. Cobra, `pelletier/go-toml/v2`, bubbletea/bubbles/lipgloss,
  `golang.org/x/tools`, and `golang.org/x/sync` are the runtime set;
  `santhosh-tekuri/jsonschema/v6`, `google/go-cmp`, and `pgregory.net/rapid`
  are test-only. Recording them now means the dependency decision is reviewable
  as one commit instead of arriving piecemeal.
- One pinned toolchain in `mise.toml` — the Go compiler included — with tasks
  for `bootstrap`, `build`, `test`, `test-integration`, `fmt`, `lint`, `check`,
  `dogfood`, `package`, and `hooks`. Every gate is defined exactly once there,
  so `just`, the git hooks, and CI cannot drift apart; `justfile` and
  `lefthook.yml` are deliberately thin shims over it.
- CI as three workflows with every action pinned by commit SHA,
  `permissions: contents: read`, per-ref concurrency, and a timeout on every
  job: `ci.yml` (quality, a three-OS test matrix, artifacts, dogfood),
  `nightly.yml` (fuzz and property placeholders), and `release.yml` (verify,
  then a draft-only release job that is the sole holder of `contents: write`).
  Dependabot watches actions and Go modules weekly.
- `.go-mutants.toml`, the configuration this project will dogfood itself with,
  written out in full with comments so the v1 surface is reviewable before the
  strict decoder exists.
- `docs/` covering the instrument-once schemata design and the S/C/D guard
  forms, the typestate pipeline, coverage-guided selection, the 11-family
  operator catalogue with its profile tiers, the full configuration surface,
  the JSON contracts, the Stryker projection boundary, and the release
  checklist. Every page states plainly that it describes planned behaviour, so
  the documentation can be reviewed now without ever claiming working software.
- `.gitattributes` pinning `* -text`. Mutant identities hash exact source
  bytes, so a CRLF checkout would silently change every ID; byte-exact
  checkouts are a correctness requirement here, not a preference.
- The strict configuration decoder, `internal/config`: `.go-mutants.toml`
  decoded with `pelletier/go-toml/v2` and `DisallowUnknownFields()`, every
  problem carrying a `GOM30xx` code, the file name, and a one-based
  line/column. A misspelled key is the single most common way a mutation run
  quietly does the wrong thing, so it is a positioned error rather than a
  default; BurntSushi/toml cannot report that position, which is why it is not
  the dependency. Problems are aggregated instead of returned one per
  invocation, because fixing a configuration file one error per run is
  unpleasant enough that people stop reading the errors.
- The baseline execution layer behind `go-mutants run`: snapshot the workspace
  into a disposable copy, build it, then run the project's test command three
  times (`test.baseline_runs`) before anything is mutated. A suite that is
  already red, flaky, or unbuildable makes every later "survived" meaningless,
  so the run proves the baseline first and stops there rather than reporting a
  score it has not measured. Every observation is retained, not just the
  slowest, and the per-mutant timeout derives as
  `max(10s, slowest baseline x 5)`; an explicit `--timeout` at or below the
  slowest baseline is refused outright, because a timeout a passing suite
  cannot meet turns the whole run into confirmed timeouts.
- Process-tree supervision in `internal/runner`. A test command that spawns
  children is the normal case in Go, and killing only the direct child leaves
  them holding ports and temporary directories for the rest of the run.
  Windows uses a Job Object with `KILL_ON_JOB_CLOSE` and fails closed when
  ownership of the tree cannot be established; POSIX uses a process group with
  `TERM` before `KILL`, so a well-behaved suite gets to clean up after itself.
- Discovery and the `list` command: `internal/discover` loads the module
  through `packages.Load`, walks it with `go/types` evidence, and mints stable
  ids for the `comparison` and `boolean-literal` families, recording every
  suppressed candidate as a skip with a reason instead of dropping it.
  `go-mutants list` prints that catalogue, and `--json` writes the
  `go-mutants/catalog` v1 document validated by
  `schema/catalog-v1.schema.json`. Enumerating mutants before any of them can
  be executed is deliberate: it makes ids, coordinates, and skip reasons
  reviewable — and diffable across changes — while the instrumentation phase is
  still being built.
- Compile validation, `internal/validate`: the instrumented snapshot is built
  once with every catalogued mutant spliced in, and a green build accepts the
  whole catalogue — which is the entire point of the schemata design. A red one
  starts a bisection that first restores every catalogued file to its pristine
  bytes and rebuilds, so a tree that was already broken stops the run with
  `GOM7420` instead of being blamed on whichever candidate was tested first,
  then searches the files the compiler named one at a time: halving while
  halving is cheaper than scanning, verifying every join, and falling back to a
  scan when a join fails, so a pair of candidates that only fail together is an
  ordinary case rather than a wrong answer. Instrumentation is a byte rewrite
  that leaves typing to the compiler, so some guarded sites genuinely cannot
  compile; the alternative to asking the compiler is type-checking every file
  to answer a question it answers for free, or answering it conservatively and
  dropping candidates that were fine. Every refusal is published as a
  `rejected[]` entry carrying the identity, the coordinates, and the compiler's
  own words, captured at the moment of rejection because by the time the phase
  finishes the tree compiles and that message exists nowhere else. Silently
  dropping them is what makes a catalogue shrink between runs with nobody able
  to say what left it, and a mutant that cannot exist must never reach a score's
  denominator.
- The execution engine, `internal/execute`: `go test -c` builds each package
  that has tests once, and every mutant afterwards starts those binaries
  directly with `GO_MUTANTS_ACTIVE` set. Going through `go test` per mutant
  would pay for a build-graph load and a staleness check in the inner loop, and
  would consult a result cache that keys on inputs the mutant is invisible to.
  A non-zero exit is a kill and the remaining binaries are skipped, because they
  cannot change the answer; the generated runtime's exit 97 is not a kill but a
  stale catalogue, reported as errored, since treating it as one would inflate
  a score.
- Timeouts are retried before they are believed. A first timeout is not
  evidence: N test binaries on a loaded machine produce timeouts that say
  nothing about the mutant, and counting one as a detection would flatter a
  suite exactly when the run is least able to notice. Every timed-out mutant is
  held back and retried serially after the queue drains — one at a time,
  nothing else running, the same timeout. A second timeout is a confirmed
  detection; a retry that finishes, pass or fail, is `inconclusive` and counts
  in neither direction. Both attempts stay in the report, so the document shows
  what happened rather than only the verdict.
- `RunReport v1` and its history store, `internal/report`, with
  `schema/run-report-v1.schema.json` and the `GOM51xx` codes. It is lossless by
  construction — every catalogued mutant appears exactly once, in `mutants[]`
  with an outcome or in `rejected[]` with a diagnostic — because the console
  summary, the exit code, and every projection still to come are views of it,
  and that is only safe if it holds everything they need. The exit decision is
  made from the written document rather than beside it, so the number a user
  reads and the gate that failed cannot disagree. `summary.score_percent` is
  `null` rather than a number when the denominator is zero: 0 reads as "your
  tests caught nothing" and 100 as "your tests caught everything", when the
  truth is that nothing was measured. History is kept under the OS cache
  directory at `<cache>/go-mutants/workspaces/<key>/`, not in the project, so a
  mutation run adds no files to the tree it is measuring; every write is
  temp-file plus atomic rename, and `ReportPublished` is emitted only after the
  rename succeeds.
- Licensing and policy files: dual `MIT OR Apache-2.0` with `LICENSES/` and
  `REUSE.toml` annotations for the files that cannot carry an inline SPDX
  header, plus `SECURITY.md`, `CONTRIBUTING.md`, `THIRD_PARTY_NOTICES.md`, and
  `RELEASE_NOTES.md`.

### Notes

- `dogfood` and `package` are honest placeholders that echo and exit 0. They
  exist as named tasks and as CI jobs so that self-mutation and packaging can
  never be bolted on without a gate; they become real in the later phases.
- The operator catalogue enumerates 42 rules while the design plan's headline
  says 43. The registry has now resolved this in favour of the enumeration:
  `mutation.CanonicalRuleCount` is 42, asserted by the canonical registry
  tests, so the headline was the loose count and no phantom 43rd rule was
  invented to match it.
- `run` now performs real mutation testing end to end, but only for the
  `comparison` and `boolean-literal` families — two of the eleven — so a score
  it reports is a score against those rules and not against the full
  catalogue. The outcome cache, coverage-guided selection, `--changed`,
  `--shard`, the HTML report, the Stryker projection, the TUI dashboard, and
  the `init`, `doctor`, `report`, and `cache` commands do not exist yet, and no
  page in `docs/` claims otherwise.

[Unreleased]: https://github.com/P4suta/go-mutants/commits/main
