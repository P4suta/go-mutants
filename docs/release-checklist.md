<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Release checklist

**Status: the scaffold satisfies none of this yet.** Nothing is tagged,
published, or described as production-ready until every box below is ticked on
one immutable commit. Items marked (later) depend on phases that have not
landed; they are listed now so the release gate cannot be quietly shortened.

## Toolchain and gates

- [ ] `mise install` and `mise run bootstrap` on a clean checkout
- [ ] `mise run check` green (fmt, build, test, lint)
- [ ] `mise run test-integration` green on Windows, Linux, and macOS
- [ ] CI green on the exact release commit, including the `platform-tests`
      matrix and the nightly fuzz and property jobs
- [ ] `go.mod` requires exactly the agreed dependency set, and every module in
      `go.sum` is accounted for in `THIRD_PARTY_NOTICES.md`

## Correctness evidence (later)

- [ ] Golden mutant-ID vectors unchanged, or a documented rule version bump
      explaining every change
- [ ] Catalog determinism: two discovery runs over this repository produce
      byte-identical `list --json`
- [ ] Golden instrumentation output byte-exact, including the CRLF fixture
- [ ] Flattener property suite (`parse(flattened) ≡ parse(patched original)`)
      and its fuzz corpus green
- [ ] Shard congruence: `n` shards merged equal one full run
- [ ] Instrumented baseline passes on every fixture in the corpus
- [ ] Timeout retry, Ctrl-C, and SIGTERM leave no child or grandchild process
      on either supervisor, and each publishes a partial report atomically
- [ ] Windows specifics: `\\?\` long paths, a locked test binary, and Job
      Object fail-closed behaviour

## Reports and schemas (later)

- [ ] Real CLI output validates against `schema/run-report-v1.schema.json`,
      `catalog-v1`, and `doctor-v1`
- [ ] Stryker projection validates against the vendored pinned schema, and the
      vendored digest and provenance still match
- [ ] `mutation.html` opens from `file://` with no network request and its
      embedded bundle digest matches

## Dogfood and docs

- [ ] `mise run dogfood` green with `--strict`, and the source workspace is
      byte-identical afterwards
- [ ] README, `docs/`, and `--help` agree on flags, exit codes, and defaults
- [ ] Every "planned" marker in `docs/` is accurate for this commit
- [ ] `CHANGELOG.md` and `RELEASE_NOTES.md` reviewed and dated
- [ ] REUSE lint clean; `LICENSES/` complete

## Publication

- [ ] `mise run package` produces the intended artifacts and checksums
- [ ] Tag `v0.1.0` created and signed only after approval
- [ ] The GitHub release stays a draft until the artifacts and notes are
      reviewed by a human
