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
deterministic stable mutant IDs, and a lossless JSON report are built today;
`--changed` for pull requests, `--shard k/n` for CI fan-out, and the
Stryker-ecosystem HTML projection are designed and not yet.

## Status: pre-release, the whole operator catalogue

Two commands exist. `go-mutants run` performs real mutation testing: it
snapshots the workspace, proves the baseline, discovers candidates, validates
that they compile, instruments the snapshot once, and measures one mutant per
test process — then writes a `run-report-v1` document and reports a mutation
score it actually measured. `go-mutants list` enumerates the same mutants
without executing them, as text or as a schema-validated JSON catalogue.

Coverage guidance is automatic and needs no flag: a run with the default test
command profiles each test binary once, then measures a mutant only against the
binaries that reach its lines, and reports one no binary reaches without
executing it at all.

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
- **No HTML report** and no Stryker projection yet; the JSON document and the
  console summary are the output.
- **No outcome cache, no `--changed`, and no `--shard`.**
- The `init`, `doctor`, `report`, and `cache` commands do not exist.

The design is settled and written down under [`docs/`](docs/), every page marks
what is implemented versus planned, and the toolchain, gates, and CI are real
and green.

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
go-mutants list
go-mutants run
```

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

Other flags that work today:

```console
go-mutants run --jobs 8 --strict
go-mutants run --include './internal/**' -- go test -run TestFast ./...
go-mutants run --mutant bf513c0d
go-mutants run --no-tui
go-mutants list --operator comparison --json
```

`--no-tui` is the escape hatch for a terminal you would rather read as lines —
a `script` session, a recorded demo. It changes nothing about what the run
measures. An editor's output pane needs no flag: it is a pipe rather than a
terminal, so it already gets the plain lines.

With no arguments, help is printed. The intended v1 command tree is `run`,
`list`, `doctor`, `init`, `report list|latest|validate|clean|merge`, and
`cache status|gc|clean`; only `run` and `list` are built. `--changed <ref>` and
`--shard k/n` are designed and not yet accepted.

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
- **Run reports are written outside your tree**, into the OS cache directory,
  temp-file-then-atomic-rename. `reports/mutation/` is reserved for the
  in-project outputs the HTML report will add, and is already excluded from
  snapshot and cache identity so that writing one cannot change a digest.
- **Test commands are trusted project code.** They run inside the snapshot with
  a per-worker `TMPDIR`, but a snapshot is not an operating-system sandbox.
- **Process trees are cleaned up.** Timeouts and interrupts kill the whole tree
  via a Windows Job Object (fail-closed) or a POSIX process group.
- **No network, no telemetry**, at any point, including the HTML report.

## Exit codes

| Code | Meaning |
| ---: | --- |
| 0 | Run completed; no policy failure |
| 1 | Opt-in policy failure only (`--strict` or `policy.minimum_score`) |
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
