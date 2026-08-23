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
stamp, the checksums, and a release that attaches to one release-please
created, and `mise run package` drives it rather than echoing a sentence and
exiting 0. `mise run dogfood`, which was a placeholder beside it, is real too.
Neither fact ticks a box. A `dist/` built on a laptop and a dogfood run from
one are not the evidence the paragraph above asks for; what ticks these boxes
is the `artifacts` and `dogfood` jobs green on GitHub-hosted runners at the
release commit.

## How a release happens

Two workflows, and a human between them.

1. **`release-please.yml`** runs on every push to `main` and maintains one open
   **Release PR**. It reads the conventional-commit subjects merged since
   `bootstrap-sha`, works out the next version, and rewrites `VERSION`,
   `internal/cli/root.go`'s marked line, and
   `.release-please/CHANGELOG.generated.md`. It never tags and never creates a
   release — `skip-github-release: true` is what reserves that.
2. **`release-publish.yml`** is `workflow_dispatch` only, behind the `release`
   environment's required reviewer. Approving it is the release decision. It
   asks release-please to tag and create the release, checks the tag against
   the source, replays `check`, `test-integration`, and `dogfood` on the tagged
   tree, then builds, signs, attests, and uploads.

The order matters, and it is not the order the tooling suggests:

- **Roll the narrative `CHANGELOG.md` first, in an ordinary pull request**, and
  merge it before the Release PR. `[Unreleased]` becomes `## X.Y.Z - <date>` by
  hand, with a comparison link. The Release PR is then rebuilt on top of it and
  the tag carries a changelog a human wrote.
- **Then merge the Release PR.** Nothing has been published at this point; the
  merge only lands the version bumps on `main`.
- **Then run `Publish release`** from the Actions tab with `tag` left empty,
  and approve the environment when GitHub asks.
- **A retry** — the gates went red, a runner died, an upload 422'd — re-runs
  the same workflow with `tag` set to the existing `vX.Y.Z`. That skips
  release-please entirely (the tag and the release already exist), rebuilds
  from the immutable tag, and `--clobber`s the evidence back over the top.

There is a window, and it is inherent rather than an oversight: the release is
created in the workflow's first step, because nothing can be checked out and
verified against a tag that does not exist yet, and the gates take about ninety
minutes after that. **A publish run that fails leaves a real, publicly visible
prerelease with notes and no binaries on the releases page.** That is what the
`tag` retry is for, and filling it is the only correct response — deleting the
release would strand the tag. Do not announce a release before its run is
green.

### Why the changelog is in two places

`release-please-config.json` sets
`"changelog-path": ".release-please/CHANGELOG.generated.md"`, which is a
deliberate divergence from release-please's default of `CHANGELOG.md`, and the
reason it is written here is that JSON carries no comments and the config file
cannot say it itself.

`CHANGELOG.md` is the narrative one. Its entries say *why* a change was made,
they are longer than a commit subject, and several of them describe a decision
that spans a dozen commits. A machine that concatenates subjects cannot write
that, and letting it try would mean either losing the narrative or hand-editing
a file a bot rewrites — a merge conflict on every release. So the generated log
lives out of the way, under `.release-please/`, where it is release-please's
own bookkeeping and the input to the GitHub release notes. `CHANGELOG.md` stays
authoritative and stays hand-rolled. `.rumdl.toml` excludes the generated file
from the Markdown linter for the same reason: nothing here controls its style.

### Why `v0.1.0`'s notes are hand-written

`bootstrap-sha` in `release-please-config.json` is the `main` head as of the
commit that introduced this automation. Everything before it — `Scoped test
binaries, …` and its neighbours — was squashed with subjects that are not
conventional commits, so release-please cannot parse them and must not try.
That means the generated notes for `v0.1.0` describe only what landed after the
bootstrap, which is a small fraction of the release.

`RELEASE_NOTES.md` is the answer, and it is why that file exists: it is the
hand-written `v0.1.0` note, reviewed against `CHANGELOG.md`, and it is pasted
over the generated body on the GitHub release before the release is announced.
From `v0.2.0` on, every subject in range is conventional and the generated
notes stand on their own.

### Conventional commits are load-bearing

This repository squash-merges, so **the pull request title becomes the commit
subject on `main`, and that subject is release-please's only input.** A
`feat:` title produces a minor bump, `fix:` a patch, `feat!:` or a
`BREAKING CHANGE:` footer a major. A title release-please cannot parse produces
nothing at all — no version bump, and no Release PR. See `CONTRIBUTING.md`.

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
      date and a comparison link **in a pull request of its own, merged before
      the Release PR**, and `RELEASE_NOTES.md` reviewed against it
- [ ] The released binary's `--version` is the tag, stamped by the
      `-X …/internal/cli.Version=<tag>` link flag **(packaging)**
- [ ] A source build reports something honest. This used to be the
      `0.1.0-dev` suffix on the checked-in default, and that guarantee is gone:
      release-please owns `internal/cli`'s `defaultVersion` now and rewrites it
      to the bare released version, so between `v0.1.0` and the next Release PR
      a `go build` from `main` also says `0.1.0`. What replaces it is
      `resolveVersion`: an unstamped build asks `runtime/debug.ReadBuildInfo`
      first, so `go install …@main` reports the pseudo-version of the commit it
      actually compiled rather than the last release's number. A `go build`
      from a working tree still has nothing better to say than the default, and
      that is the residual honesty gap — it is recorded here rather than
      papered over
- [ ] REUSE lint clean; `LICENSES/` complete; `REUSE.toml` covers every file
      that cannot carry an inline SPDX header

## Packaging and signing

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
- [ ] `VERSION` and `internal/cli/root.go`'s `defaultVersion` both equal the
      tag on the tagged tree. `release-publish.yml` checks this before it
      installs a toolchain, so a mistyped `tag` input costs seconds rather than
      an hour of gates
- [ ] The three gates — `mise run check`, `mise run test-integration`,
      `mise run dogfood` — green on the tagged tree inside `release-publish.yml`
      itself, not merely green on `main` beforehand
- [ ] `git status --porcelain` empty after those gates. They share one checkout
      with goreleaser now, which `release.yml`'s two jobs did not, and
      `goreleaser release` without `--snapshot` refuses a dirty tree —
      `mise run package`'s `--snapshot` skips that check, so nothing local
      would have caught it. The workflow asserts it between the last gate and
      the build
- [ ] Every `dist/` archive and `checksums.txt` signed with keyless cosign and
      **verified** in the same step against the workflow's own OIDC identity; a
      signature nothing checked is not evidence
- [ ] `actions/attest` attestation over `dist/checksums.txt` uploaded beside
      the signature bundles
- [ ] The GitHub release is the one release-please created, carrying its
      generated notes: `.goreleaser.yaml` sets `release.mode: keep-existing`,
      so goreleaser attaches archives without rewriting the body

## Publication

- [ ] **The first Release PR proposes `0.1.0`, not `0.0.1`.** This is the one
      box to read before anything is merged, and it is checked by looking at
      the pull request title. `release-please-config.json` says
      `"initial-version": "0.1.0"` while `.release-please-manifest.json` says
      `{".": "0.0.0"}`, which are two different claims about what was last
      released. `bootstrap-sha` means no tag exists, so `initial-version`
      should win — but if release-please reads the manifest's `0.0.0` as a
      prior release instead, a `feat:` subject produces `0.0.1`. Catching that
      in the pull request title costs a config edit; catching it after the
      merge costs a bad tag
- [ ] `CHANGELOG.md` rolled and merged, then the Release PR merged — in that
      order (see [How a release happens](#how-a-release-happens))
- [ ] `Publish release` dispatched with an empty `tag`, and the `release`
      environment approved by a human who has read this list. That approval is
      the release decision; there is no second one
- [ ] Tag `v0.1.0` exists and points at the merged Release PR commit
- [ ] `RELEASE_NOTES.md` pasted over the generated release body before the
      release is announced, because `bootstrap-sha` postdates the
      pre-conventional squash subjects — first release only
