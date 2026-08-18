<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Stryker report ecosystem compatibility

**Status: planned.** Neither the projection nor the HTML report exists yet.
This page states the boundary now so that later work cannot quietly widen it.

## Scope

go-mutants is a Go-native mutation engine that participates in the Stryker
**reporting** ecosystem. The compatibility point is the Mutation Testing Report
Schema and the [Mutation Testing Elements][mte] viewer — not a shared mutation
kernel.

This project is independent of the Stryker project. It is not affiliated with,
endorsed by, or maintained by the Stryker team. The name is used only to
identify the report schema and the viewer this project targets.

[mte]: https://github.com/stryker-mutator/mutation-testing-elements

## What is compatible

- A deterministic projection of a completed run into the report schema,
  emitted as JSON.
- A single self-contained HTML file that embeds that JSON and a vendored copy
  of Mutation Testing Elements.

## What is not compatible, by design

Command-line options, configuration files, the plugin API, the mutation kernel,
operator implementations, the execution protocol, and the cache representation
are all Go-native and are not intended to match Stryker's.

## Two documents, two responsibilities

`go-mutants/run-report` v1 is the authoritative ledger: rejected candidates
with compiler diagnostics, confirmed versus inconclusive timeouts, expectation
states, per-shard `not_run` entries, coverage mapping decisions, cache
provenance, and bounded process output. The shared schema has a smaller model
and cannot hold any of that.

The projection is therefore **one-way and lossy**. It is derived from the
native report after that report has been stored, and it is never read back as
cache or resume state. Anything that diagnoses, resumes, or audits a run reads
the native document.

Status mapping is fixed:

| go-mutants outcome | Projected status |
| --- | --- |
| `killed` | `Killed` |
| `survived` | `Survived` |
| `survived (uncovered)` | `NoCoverage` |
| confirmed `timeout` | `Timeout` |
| `inconclusive` | `Ignored`, with a `statusReason` |
| `error` | `CompileError` or `RuntimeError` as recorded |
| `not_run` | Omitted; the native report keeps the reason |

## Validation gate

The vendored schema is pinned by version and SHA-256 alongside its license and
a `PROVENANCE.json`. Both a checked-in fixture and real CLI output must
validate against it. A projection that cannot be validated aborts the run
rather than emitting a document that appears authoritative but is not.

## HTML report

The viewer bundle is vendored under `vendor-assets/<pkg>/<ver>/` with its
license, provenance, and digest; it is embedded with `go:embed` and its SHA-256
is re-checked at render time. The output is one self-contained file: the JSON
rides in a non-executable script tag with every `<` written as the JSON
escape `\u003c`, the Content Security Policy allows only the hashed
script, and the page makes zero network requests so it works from `file://`.
