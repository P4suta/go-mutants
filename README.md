<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# go-mutants

[![CI](https://github.com/P4suta/go-mutants/actions/workflows/ci.yml/badge.svg)](https://github.com/P4suta/go-mutants/actions/workflows/ci.yml)

Mutation testing for Go modules that is fast enough to leave switched on.
go-mutants instruments every compilable mutant **once** into a disposable
snapshot of your module, then activates one mutant per test process through an
environment variable. Your working tree is never modified, and the toolchain
builds essentially once instead of once per mutant. Coverage data then decides
which test binaries a mutant needs to run against at all, and a mutant no test
reaches is reported without being executed.

The design targets the things that make mutation testing painful in practice.
A live TUI dashboard instead of a silent wait, coverage-guided selection,
deterministic stable mutant IDs, a lossless JSON report, `--changed` for pull
requests, `--shard K/N` for CI fan-out, an outcome cache that keeps a second run
from re-measuring what has not moved, and a self-contained HTML report that
opens from `file://` with the network unplugged are all built today.

## Status: pre-release, feature-complete for v1

The v1 command tree is complete. `go-mutants run` performs real mutation
testing: it snapshots the workspace, proves the baseline, discovers candidates,
validates that they compile, instruments the snapshot once, and measures one
mutant per test process — then writes a `run-report-v1` document, publishes
`reports/mutation/mutation.{json,html}`, and reports a mutation score it
actually measured. `go-mutants list` enumerates the same mutants without
executing them, as text or as a schema-validated JSON catalogue.
`go-mutants doctor` checks that this machine can run any of it, as a table or
as a JSON document, and `go-mutants init` writes a fully commented
`.go-mutants.toml` in which every value is the built-in default.
`go-mutants report` reads those documents back: `list` and `latest` show the
run history, `clean` deletes it, `merge` combines the reports of a sharded run
into the whole run's report, and `validate` checks any report against the
schema this build embeds. `go-mutants cache` works with the outcomes a run has
proven: `status` says where they are and what is stored, `gc --days N` removes
what was written more than N days ago, and `clean` removes them all.

Coverage guidance is automatic and needs no flag: a run with the default test
command profiles each test binary once, then measures a mutant only against the
binaries that reach its lines, and reports one no binary reaches without
executing it at all.

The outcome cache needs no flag either. Outcomes go-mutants has proven are kept
under your OS cache directory, keyed by everything that could change one — the
build, the code, the catalogue, the command, the environment — so a second run
over unchanged code measures only what has moved, and one over changed code
finds nothing to reuse rather than reusing something stale.

On a terminal that can do better than ASCII, `run` draws a live dashboard — the
phase, a score gauge, one row per worker, and a scrolling survivor feed — and
prints the closing summary into the scrollback once the screen is restored.
Everywhere else it prints deterministic plain lines instead: into a pipe or a
file, on a dumb terminal, under `--no-tui`, `--json`, `--quiet`, or
`--no-color`, and whenever `NO_COLOR` or `CI` is set. The summary block is
byte-identical either way, because a dashboard run replays it through the plain
renderer rather than formatting its own.

All eleven operator families and all forty-two rules are discovered,
instrumented, compile-validated, executed, and scored. A score from go-mutants
is a score against the whole v1 catalogue, narrowed only by the profile you
chose: `balanced` is the default and leaves out `bitwise`,
`arithmetic-assignment`, and `statement-deletion`, which `strong` and `all` add
back. [`docs/operators.md`](docs/operators.md) is the table.

The honest limits:

- **No `switch`/`select` case mutation, and no `if`-branch replacement.** They
  are v2: each needs a guard form or a neutral-value model the instrumenter
  does not build. Package-level `var` initialisers, `const` declarations, array
  lengths, and generic type parameter lists are excluded for reasons that are
  not going to change, and cgo packages and generated files are excluded
  wholesale. Every one of those is a recorded skip with a reason rather than a
  silent omission.
- **A rewrite site none of the three guard forms can express is skipped**, with
  the reason `unnameable-decl-type`. The commonest are a `:=` that redeclares
  rather than declares, a declared type the file cannot spell with the imports
  it has, and a statement in a `for` post or an `if` initialiser, where a block
  is not legal Go. `go-mutants list` prints the count.
- **Coverage guidance is on only for the default test command.** A
  `test.command` other than `go test ./...` cannot be attributed to
  go-mutants' own per-package test binaries, so such a run measures every
  mutant against every binary and says so with a `GOM7601` warning. Any failure
  of the coverage pass itself does the same with `GOM7602`: the optimisation
  can never fail a run.
- **The Stryker projection is one-way and lossy**, by design. The HTML report
  and `reports/mutation/mutation.json` are built from the run report after it
  has been filed, and are never read back. Six outcomes become five statuses:
  an uncovered survivor projects as `Survived` rather than `NoCoverage`, so
  that the two documents agree about how many survivors there were, and the
  expectations ledger, the cache accounting, and coverage do not survive the
  trip. The `run-report-v1` document is the one to diagnose, resume, or audit
  from; [`docs/stryker-compatibility.md`](docs/stryker-compatibility.md) states
  the whole mapping.
- **One host platform per report.** Build constraints decide which files a
  package even has, so a report is a statement about the platform it was
  measured on; there is no cross-`GOOS` matrix. `doctor` warns when the `go` on
  `PATH` targets a platform other than the host. A `go.work` workspace is
  supported only for the modules the snapshot itself holds.
- **The outcome cache is on only for the default test command**, on the same
  terms and for the same reason as coverage guidance: `cache.mode = "auto"`
  reuses nothing for a command go-mutants cannot reason about and says so with
  `GOM7901`. `cache.mode = "on"` is how you promise your own command is
  reproducible. Inconclusive outcomes, harness errors, interruptions, uncovered
  mutants, and every mutant named in `[[mutation.expect]]` are measured on
  every invocation, and nothing the cache does can change a verdict or fail a
  run.
- **`--changed` needs git and a repository**, and fails rather than guessing
  when it cannot read a diff: a narrowing that silently fell back to
  "everything" or to "nothing" would be worse than not running at all. Rename
  detection is off, so a renamed file selects every mutant in it.
- **The run history is filed per workspace**, and a workspace is identified by a
  digest of its contents, so runs with an edit between them are stored apart.
  `report list|latest|clean` gather one module's runs back together by the
  module path in each document, and are run from a module root.

The design is settled and written down under [`docs/`](docs/), every page
carries a status line saying how much of it is built, and the toolchain, gates,
and CI are real and green.

Do not describe go-mutants as production-ready. Nothing is published, tagged,
or released.

## Requirements

- Go 1.26 or newer (the module targets `go 1.26`)
- Windows, Linux, or macOS on x64 or arm64
- Git, for `--changed` only
- [mise](https://mise.jdx.dev) for development; it pins every tool this
  repository uses, including the Go toolchain itself

## Quick start

```console
go install github.com/P4suta/go-mutants/cmd/go-mutants@latest
cd your-module
go-mutants doctor            # is this machine ready?
go-mutants init              # write a commented .go-mutants.toml (optional)
go-mutants run
```

Then open `reports/mutation/mutation.html`. It is one file: double-click it,
attach it to a CI job, drop it on a shared drive. It fetches nothing, so it
works from `file://` on a laptop with no network — see
[Safety model](#safety-model).

`init` is optional and changes nothing by itself: every value it writes is the
built-in default, so the file is a place to start editing rather than a
prerequisite. It never overwrites an existing one.

On a terminal the run draws the dashboard. Into a pipe, in CI, or under
`--no-tui`, it prints its phases as it goes, then one line per mutant as it
settles — survivors carrying their diff — then the summary. Abridged:

```text
baseline ok: avg 1.011s, slowest 1.969s, timeout 10s (derived)
phase mutate: discovering candidates, validating them, then executing the mutants
discovered 14 candidates, 0 skips
validated 13 mutants, 0 rejections
coverage: 1 test binary, 10 of 13 mutants covered, 3 uncovered
SURVIVED (uncovered)  bf513c0d  untested.go:14:11  neq-to-eq  != -> ==  (0s)
    - !=
    + ==
mutants 13  killed 10  survived 3  timeout 0  inconclusive 0  errored 0
    not-run 0  rejected 0  uncovered 3
score 76.92%
run 20260820T221649Z-67af  exit 0
```

The counters are one line on a real terminal; they are wrapped above to fit.

`SURVIVED (uncovered)` and the `uncovered 3` column are coverage's own finding:
no test binary reaches that line, so the mutant was never executed and the run
knows why it survived. The column appears only in a coverage-guided run, and
`uncovered` is a subset of `survived` rather than a seventh bucket — the
columns still add up to `mutants`.

`score N/A` is printed instead of a percentage when nothing scoreable was
measured; there is no sentinel number for it. The full document goes to the
history store under your OS cache directory, and `run --json` writes it to
standard output instead.

Every run also publishes into your own tree, at `reports/mutation/`:
`mutation.json`, the Stryker-ecosystem projection, and `mutation.html`, the
self-contained viewer. The run prints where each went, one labelled path per
line, so a CI step can grep for the one it wants to attach. They are the only
files go-mutants writes into a workspace; `--report none` turns them off, and
`--report json` or `--report html` asks for one of the two. The pair is
published together or not at all — a `mutation.json` from this run beside a
`mutation.html` from last week is worse than either alone — and both are
written only after the run's own record is safely filed.

Other flags that work today:

```console
go-mutants run --jobs 8 --strict
go-mutants run --include './internal/**' -- go test -run TestFast ./...
go-mutants run --mutant bf513c0d
go-mutants run --no-tui
go-mutants run --explain
go-mutants run --changed=origin/main
go-mutants run --shard 1/4
go-mutants run --report none
go-mutants list --operator comparison --json
go-mutants doctor --json
go-mutants init --check
go-mutants report latest
go-mutants report merge shard-*.json --output mutation.json
go-mutants report validate mutation.json
```

`--changed` executes only the mutants sitting on lines you have changed since a
ref — the merge base of it and `HEAD`, so a branch is measured against the
commit it left rather than against whatever has landed on the target since.
Bare `--changed` uses the upstream of `HEAD` and reports it by name. Its value
needs an equals sign, because the ref is optional: `--changed=origin/main`, not
`--changed origin/main`. What counts as changed is your working tree: edits you
have not committed, and files you have not added either — every line of a file
git has never seen is a new line.

`--shard K/N` executes only its own share, assigned from the mutant id alone so
that editing one file never reshuffles the rest. Every shard discovers,
validates and reports the entire catalogue, so the N documents are directly
comparable — and `report merge` proves they describe one run before combining
them into the document an unsharded run would have written.

`--explain` prints, underneath the usual output, every rejected mutant with the
compiler's own words and every suppressed site by reason. It is the answer to
"why is this smaller than I expected", and it cannot be combined with `--json`:
everything it prints is already in the document.

`--no-tui` is the escape hatch for a terminal you would rather read as lines —
a `script` session, a recorded demo. It changes nothing about what the run
measures. An editor's output pane needs no flag: it is a pipe rather than a
terminal, so it already gets the plain lines.

With no arguments, help is printed. The v1 command tree is `run`, `list`,
`doctor`, `init`, `report list|latest|validate|clean|merge`, and
`cache status|gc|clean`, and all of it is built.

Everything after `--` is captured verbatim as the test command's argv; it is
never handed to a shell. It replaces `test.command`, so anything other than the
built-in `go test ./...` turns coverage-guided selection off with a `GOM7601`
warning. The comparison is against the resulting command and not against where
it was written, so a passthrough that spells the default out is the default.

Working on this repository instead:

```console
mise trust
mise install
mise run bootstrap
mise run check
```

## Safety model

- **Your tree is read-only.** Discovery reads it; every build, edit, and test
  happens in a disposable snapshot built from a sorted manifest that excludes
  `.git`, caches, and the report directory, and that rejects symlinks,
  junctions, and special files.
- **Run reports and cached outcomes are written outside your tree**, into the
  OS cache directory, temp-file-then-atomic-rename. The single exception is
  `reports/mutation/`, where the JSON projection and the HTML page are
  published; it is excluded from snapshot and cache identity, so writing one
  cannot change a digest. Add it to your `.gitignore` — it is build output.
- **go-mutants proves a directory is its own before it deletes anything.**
  Every workspace directory in the OS cache carries an ownership marker naming
  the workspace it belongs to, and `cache gc` and `cache clean` refuse any
  directory without one — so a truncated key collision, or somebody else's tool
  keeping files under the same root, is a diagnosable skip rather than a
  deletion.
- **Test commands are trusted project code.** They run inside the snapshot with
  a per-worker `TMPDIR`, but a snapshot is not an operating-system sandbox.
- **Process trees are cleaned up.** Timeouts and interrupts kill the whole tree
  via a Windows Job Object (fail-closed) or a POSIX process group.
- **No network, no telemetry**, at any point, including the HTML report.

## Exit codes

| Code | Meaning |
| ---: | --- |
| 0 | Run completed; no policy failure |
| 1 | Opt-in gate failure only (`--strict`, `policy.minimum_score`, `init --check`) |
| 2 | Infrastructure, configuration, baseline, or stale-expectation failure |
| 130 | Interrupted (Ctrl-C); a partial report is published first |
| 143 | Terminated (SIGTERM); a partial report is published first |

`strict` defaults to **false**: go-mutants does not fail your build unless you
ask it to, in a terminal, a pipe, and CI alike. A confirmed timeout counts as
detected in the score but is always displayed separately; errors, inconclusive
results, and not-run mutants are excluded from the score denominator.

## Documentation

- [Architecture](docs/architecture.md)
- [Operators](docs/operators.md)
- [Configuration](docs/configuration.md)
- [JSON contracts](docs/json-schema.md)
- [Stryker report ecosystem compatibility](docs/stryker-compatibility.md)
- [Release checklist](docs/release-checklist.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)

## Sibling projects

go-mutants is the third in a family of mutation testing tools that share this
architecture — read-only workspaces, disposable snapshots, stable IDs, strict
configuration, and honest reports:

- [gleam-mutants](https://github.com/P4suta/gleam-mutants) — Gleam, across
  Erlang, Node.js, Deno, and Bun
- [ocaml-mutants](https://github.com/P4suta/ocaml-mutants) — OCaml and Dune,
  driven by compiler Typedtrees

## License

Licensed under either the MIT License or the Apache License 2.0, at your
option. See [LICENSE-MIT](LICENSE-MIT), [LICENSE-APACHE](LICENSE-APACHE), and
[third-party notices](THIRD_PARTY_NOTICES.md).
