<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Release checklist

**Status: the v1 feature set is complete; what is missing is evidence.** The
pipeline runs end to end, the catalog is whole, the schemas are shipped, and
every gate below names something that exists in this tree today. Nothing is
tagged, published, or described as production-ready until every box is ticked
on one immutable commit, with the evidence produced by GitHub-hosted runners
rather than by a laptop.

Items marked **(packaging)** depend on the goreleaser chain, and that chain is
now built: `.goreleaser.yaml` describes the whole matrix, the `-X …cli.Version`
stamp, the checksums, and a draft-only release, and `mise run package` drives it
rather than echoing a sentence and exiting 0. `mise run dogfood`, which was a
placeholder beside it, is real too. Neither fact ticks a box. A `dist/` built on
a laptop and a dogfood run from one are not the evidence the paragraph above
asks for; what ticks these boxes is the `artifacts` and `dogfood` jobs green on
GitHub-hosted runners at the release commit.

## Toolchain and gates

- [ ] `mise install` and `mise run bootstrap` on a clean checkout
- [ ] `mise run check` green — `fmt`, `build`, `test`, `lint` in CI order
- [ ] `mise run test-integration` green on Windows, Linux, and macOS
- [ ] CI green on the exact release commit: the `quality` job, all three legs
      of the `platform-tests` matrix, `artifacts`, and `dogfood`
- [ ] All three legs of the nightly `fuzz` matrix green — `FuzzParse`
      (`internal/config`), `FuzzMatch` (`internal/glob`), and `FuzzFlatten`
      (`internal/instrument`), each searching for its whole `-fuzztime` — with
      no `fuzz-crasher-*` artifact uploaded
- [ ] The nightly `property` job green at its deepened budget
      (`RAPID_CHECKS=2000`, `-count=5`, so each rerun draws a fresh seed), with
      no `rapid-failures` artifact uploaded
- [ ] `go.mod` requires exactly the agreed dependency set, `go.sum` is
      reproducible from it, and every module in it is accounted for in
      `THIRD_PARTY_NOTICES.md`

## Correctness evidence

- [ ] Golden mutant-ID vectors unchanged
      (`internal/mutation.TestGoldenIDVectors`), or a documented rule version
      bump explaining every change
- [ ] Catalog determinism: `internal/discover.TestDiscoverIsDeterministic`
      green, and two `list --json` runs over this repository produce
      byte-identical documents
- [ ] Golden instrumentation output byte-exact against `internal/instrument`'s
      `testdata/*.golden` (`TestInstrumentGolden`), the CRLF form included
      (`TestInstrumentPreservesCRLFOutsideTheGuards`), and no golden updated
      with `-update` in the release commit without a changelog entry saying why
- [ ] `TestInstrumentIsDeterministic` green: instrumenting the same input twice
      produces the same bytes, which the goldens alone do not prove
- [ ] Flattener property suite green — `TestFlattenPreservesMeaning`
      (`parse(flattened) ≡ parse(patched original)`) together with
      `TestGeneratorProducesHardInput` and `TestCensusDiscriminates`, which are
      what keep it from passing on inputs too easy to mean anything — and the
      `FuzzFlatten` corpus with it
- [ ] Shard congruence: `internal/engine.TestShardedRunsMergeIntoTheUnshardedOne`
      green, and `report merge` refuses incongruent shards with
      `CodeIncongruentShards`
- [ ] The instrumented baseline passes on every corpus module — `coverage`,
      `discovery`, `families`, `killable`, `rejectable`, `simple` — and
      `failing-baseline` still fails, since it is the fixture that proves the
      baseline gate works; `fixtures/rejectable`'s traps are still rejected by
      compile validation
- [ ] Timeout, Ctrl-C, and SIGTERM leave no child or grandchild process on
      either supervisor (`internal/runner`), and each publishes a partial
      report atomically before exiting 130 or 143
- [ ] Windows specifics: `\\?\` long paths
      (`internal/snapshot/longpath_windows_test.go`), a locked test binary, and
      Job Object fail-closed adoption
      (`internal/runner.TestChildDoesNotRunBeforeItIsAdopted`)

## Reports and schemas

- [ ] Real CLI output validates against `schema/run-report-v1.schema.json`,
      `schema/catalog-v1.schema.json`, and `schema/doctor-v1.schema.json` —
      `run --json`, `list --json`, and `doctor --json` each checked, not only
      the fixtures
- [ ] `report validate` accepts a freshly written report, and `report merge`
      output validates too
- [ ] The Stryker projection validates against the vendored pinned schema, and
      `schema/stryker`'s unaltered-copy, version, and license tests are green
- [ ] `reports/mutation/mutation.html` opens from `file://` with no network
      request (`internal/report.TestHTMLFetchesNothing`), and `vendor-assets`
      still agrees with its recorded digest and provenance

## Dogfood and docs

- [ ] `mise run dogfood` green with `--strict`, and the source workspace is
      byte-identical afterwards
- [ ] That dogfood run ends `inconclusive 0`, because green is not by itself
      evidence that the gate gated. `Tally.Denominator` is detections plus
      unexpected survivors, so an inconclusive mutant leaves the denominator
      rather than counting against it, and `mutation.Decide` never reads
      `Tally.Inconclusive` — none of `--strict`, `policy.minimum_score`, or
      `policy.require_mutants` can fail a run over one. A run that ends
      `inconclusive N` therefore prints a percentage of a subset of its own
      catalog and exits 0, which is the one shape of green this list must not
      accept. `not-run 0` too, for this run rather than as a general rule:
      `not-run` is how a mutant that a `--shard` or `--changed` narrowing left
      out is deliberately recorded, and the dogfood gate applies neither. The
      third thing outside the denominator, an expected survivor, is deliberate
      everywhere — the `[[mutation.expect]]` ledger accounts for it, and an
      unfulfilled or stale expectation is exit 2 already
- [ ] README, `docs/`, and `--help` agree on flags, exit codes, and defaults;
      the README exit-code table matches `internal/cli`'s `exitCodeHelp`
      verbatim
- [ ] Every **Status** line in `docs/` is accurate for this commit
- [ ] `CHANGELOG.md`'s `[Unreleased]` section promoted to `[0.1.0]` with a
      date and a comparison link, and `RELEASE_NOTES.md` reviewed against it
- [ ] The released binary's `--version` is the tag, stamped by the
      `-X …/internal/cli.Version=<tag>` link flag; the checked-in default stays
      `0.1.0-dev`, so a build from source is never mistaken for a release
      **(packaging)**
- [ ] REUSE lint clean; `LICENSES/` complete; `REUSE.toml` covers every file
      that cannot carry an inline SPDX header

## Packaging and publication

- [ ] `goreleaser check` green against `.goreleaser.yaml`
- [ ] `mise run package` runs `goreleaser release --snapshot --clean` and
      produces the six archives — linux, darwin, and windows on amd64 and
      arm64 — plus `checksums.txt` **(packaging)**
- [ ] The snapshot build is smoke-tested rather than trusted: an archive
      unpacks and its `go-mutants --version` prints the snapshot version, which
      is what proves the `-X …/internal/cli.Version` stamp reached its target.
      A build whose `-X` silently missed would still exit 0 **(packaging)**
- [ ] `LICENSE-MIT`, `LICENSE-APACHE`, and `README.md` are inside every archive,
      because the offer is dual and shipping one license would misstate it
- [ ] The `Release` workflow's `verify` job is green on the tagged tree before
      `release` runs; `release` is the only job with write access, and the
      GitHub release it creates is a draft
- [ ] Tag `v0.1.0` created and signed only after approval
- [ ] The GitHub release stays a draft until the artifacts and the notes are
      reviewed by a human
