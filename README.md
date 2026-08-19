<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# go-mutants

[![CI](https://github.com/P4suta/go-mutants/actions/workflows/ci.yml/badge.svg)](https://github.com/P4suta/go-mutants/actions/workflows/ci.yml)

Mutation testing for Go modules that is fast enough to leave switched on.
go-mutants instruments every compilable mutant **once** into a disposable
snapshot of your module, then activates one mutant per test process through an
environment variable. Your working tree is never modified, the toolchain builds
essentially once instead of once per mutant, and coverage data decides which
test binaries need to run at all.

The design targets the things that make mutation testing painful in practice:
a live TUI dashboard instead of a silent wait, coverage-guided selection,
`--changed` for pull requests, `--shard k/n` for CI fan-out, deterministic
stable mutant IDs, and a lossless JSON report with a Stryker-ecosystem HTML
projection.

## Status: pre-release, no mutant runs yet

Two commands exist. `go-mutants run` snapshots the workspace, builds it, and
runs your test command three times to prove the baseline — then stops and warns
that it did, rather than reporting a mutation score it has not measured.
`go-mutants list` enumerates the mutants a run would execute, for the
`comparison` and `boolean-literal` families, as text or as a schema-validated
JSON catalogue.

Nothing is instrumented or mutated yet: instrumentation, mutant execution,
coverage-guided selection, the outcome cache, policy enforcement, and reports
are still to come, and the `init`, `doctor`, `report`, and `cache` commands do
not exist. The design is settled and written down under [`docs/`](docs/), every
page marks what is implemented versus planned, and the toolchain, gates, and CI
are real and green.

Do not describe go-mutants as production-ready. Nothing is published, tagged,
or released.

## Requirements

- Go 1.26 or newer (the module targets `go 1.26`)
- Windows, Linux, or macOS on x64 or arm64
- Git, for `--changed` only
- [mise](https://mise.jdx.dev) for development; it pins every tool this
  repository uses, including the Go toolchain itself

## Quick start

What works today:

```console
go install github.com/P4suta/go-mutants/cmd/go-mutants@latest
go-mutants list
go-mutants list --operator comparison --json
go-mutants run
```

With no arguments, help is printed. The intended v1 command tree is `run`,
`list`, `doctor`, `init`, `report list|latest|validate|clean|merge`, and
`cache status|gc|clean`; only `run` and `list` are built.

Once the engine lands, the intended flow adds:

```console
go-mutants run --profile strong --jobs 8
go-mutants run --changed origin/main
go-mutants run --shard 1/4 --report json
go-mutants run --include './internal/**' --strict -- go test -run TestFast ./...
```

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
- **The only project writes are reports**, under `reports/mutation/`, written
  temp-file-then-atomic-rename, and excluded from snapshot and cache identity.
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
