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
builds essentially once instead of once per mutant. Coverage-guided selection —
letting coverage data decide which test binaries need to run at all — is
designed and not yet built.

The design targets the things that make mutation testing painful in practice:
a live TUI dashboard instead of a silent wait, coverage-guided selection,
`--changed` for pull requests, `--shard k/n` for CI fan-out, deterministic
stable mutant IDs, and a lossless JSON report with a Stryker-ecosystem HTML
projection.

## Status: pre-release, two operator families

Two commands exist. `go-mutants run` performs real mutation testing: it
snapshots the workspace, proves the baseline, discovers candidates, validates
that they compile, instruments the snapshot once, and measures one mutant per
test process — then writes a `run-report-v1` document and reports a mutation
score it actually measured. `go-mutants list` enumerates the same mutants
without executing them, as text or as a schema-validated JSON catalogue.

The honest limits:

- **Two of the eleven operator families**, `comparison` and `boolean-literal`.
  A score from go-mutants today is a score against those rules, not against the
  full catalogue.
- **No coverage guidance.** Every mutant is measured against every test binary
  of every package, which is slower and never wrong.
- **No HTML report** and no Stryker projection yet; the JSON document and the
  console summary are the output.
- **No outcome cache, no `--changed`, no `--shard`,** and no TUI dashboard.
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

A run prints its phases as it goes, then one line per mutant as it settles —
survivors carrying their diff — then the summary. Abridged:

```text
baseline ok: avg 1.091s, slowest 2.166s, timeout 10.829s (derived)
phase mutate: discovering candidates, validating them, then executing the mutants
discovered 4 candidates, 0 skips
validated 4 mutants, 0 rejections
SURVIVED   bf513c0d  untested.go:14:11  neq-to-eq  != -> ==  (616ms)
    - !=
    + ==
mutants 4  killed 3  survived 1  timeout 0  inconclusive 0  errored 0
    not-run 0  rejected 0
score 75.00%
run 20260820T175055Z-57bc  exit 0
```

The counters are one line on a real terminal; they are wrapped above to fit.

`score N/A` is printed instead of a percentage when nothing scoreable was
measured; there is no sentinel number for it. The full document goes to the
history store under your OS cache directory, and `run --json` writes it to
standard output instead.

Other flags that work today:

```console
go-mutants run --jobs 8 --strict
go-mutants run --include './internal/**' -- go test -run TestFast ./...
go-mutants run --mutant bf513c0d
go-mutants list --operator comparison --json
```

With no arguments, help is printed. The intended v1 command tree is `run`,
`list`, `doctor`, `init`, `report list|latest|validate|clean|merge`, and
`cache status|gc|clean`; only `run` and `list` are built. `--changed <ref>` and
`--shard k/n` are designed and not yet accepted.

Everything after `--` is captured verbatim as the test command's argv; it is
never handed to a shell.

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
