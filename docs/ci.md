<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Continuous integration

**Status: implemented, and checked.** Every workflow, every job and every task
named below exists; `internal/testkit/cidoc_test.go` fails when one stops
existing, when a new one is not named here, or when the aggregate job stops
waiting for one of the others.

Everything CI runs is a `mise` task, and every one of them is a task a
contributor can run. That is the rule this page exists to make usable: **a red
job should be reproducible in one command**, and the command is in the table
beside the job.

## The workflows

| Workflow | When it runs | What it is for |
| --- | --- | --- |
| `ci.yml` | every push to `main`, every pull request, and on demand | The gates. Nothing merges past a red one |
| `nightly.yml` | `17 02` daily | The searches that have no natural end: fuzzing, property exploration at a deepened budget, the race detector over the integration tier, and the benchmarks |
| `release-please.yml` | every push to `main` | Maintains one open Release PR. It never tags and never creates a release |
| `release-publish.yml` | on demand, behind a required reviewer | The release decision. See [the release checklist](release-checklist.md) |

The two release workflows have one job each — `release-pr` and `publish` — and
neither is a gate. `release-pr` rewrites the version files in an open pull
request; `publish` is the only thing in this repository that tags anything, it
is `workflow_dispatch` only, and approving its environment *is* the release
decision.

## The gates

| Job | Runs | Also |
| --- | --- | --- |
| `quality` | `mise run check` — `fmt`, `build`, `test`, `lint` in the order a contributor runs them | The corpus gate, and `committed` over the pull request's own commits |
| `platform-tests` | `mise run build`, then `mise run test-cost` and `mise run test-cost-integration` on ubuntu, windows and macos | `fail-fast: false`, so one platform's failure does not hide another's. The corpus gate, the build-cache report, and the kept scratch uploaded on failure |
| `race` | `mise run test-race` — the unit tier under `-race` | ubuntu only: the detector needs cgo |
| `coverage` | `mise run cover-integration` | **Not a gate.** `continue-on-error`, and skipped on pull requests entirely. See below |
| `artifacts` | `mise run package` | Exercises the packaging path on every run rather than for the first time on a tag, and smoke-tests that the version stamp reached its target |
| `dogfood` | `mise run dogfood` — go-mutants against go-mutants | The gate on whether the tests *catch* anything. `--strict`, so one undeclared survivor fails it |
| `ci-success` | nothing | Needs every job above, so branch protection names one check instead of six |

## The nightly searches

| Job | Runs | Why it is nightly |
| --- | --- | --- |
| `fuzz` | `go test -fuzz` over each target in turn | A fuzz run has no natural end. On a pull request it would either be too short to find anything or too long to wait for |
| `property` | the property suites at `RAPID_CHECKS=2000`, `-count=5` | Each rerun draws a fresh seed, which is the opposite of what a gate wants: the gate pins `RAPID_SEED=1` so a score cannot be a coin flip, and the exploration happens here |
| `race-integration` | `mise run test-integration-race` | Both tiers under the detector is an hour and a half |
| `bench` | `mise run bench` | Numbers, never a gate |

## Coverage is a signal and not a gate

`coverage` is `continue-on-error` and does not run on pull requests at all, and
that is deliberate. Coverage measures which lines *ran*. The question a
mutation-testing tool has to answer about its own suite is whether the tests
*catch* anything, and `dogfood` is the job that answers it: `--strict` fails on
one undeclared survivor, and `policy.minimum_score` is a floor stated as a fixed
number of survivors rather than as a percentage of a growing catalogue.

A coverage percentage as a required check would be a number nobody may lower and
everybody may satisfy without writing an assertion. See
[the development guide](development.md) for the dogfood gate and how its scope
is widened.

## Two conventions every suite-running job follows

**The test-owned build cache goes to the runner's temporary area.** Every job
that runs a suite publishes `GO_MUTANTS_TEST_GOCACHE` into `$GITHUB_ENV`
pointing at `$RUNNER_TEMP`, and never restores it from an actions cache. The
suites drive thousands of child `go` commands against trees at absolute paths
that exist for one run, so every entry is keyed on a path nothing will look up
again — a restored cache would be a cache of garbage. It is published through
`$GITHUB_ENV` rather than a job's `env:` because the `runner` context is not
available there.

**`GO_MUTANTS_TEST_REQUIRE_TOOLS=1` is set at workflow level.** A missing `go`
or `git` makes a test skip locally, so that a contributor without git installed
can still run the suite. On a runner it must fail instead: a job that retired
half a package and reported green is the failure this variable exists to
prevent, and it has happened.

## The corpus gate

Eight jobs repeat one check:

```console
git status --porcelain --ignored -- fixtures
```

`fixtures/` is a corpus of independent modules, and a suite that wrote into one
would make every later run measure a different corpus. `--ignored` is
load-bearing: `**/reports/mutation/`, `*.test` and `*.out` are gitignored
precisely so a stray run cannot be committed, and without the flag the gate
would pass over all of them. It runs with `if: always()`, because a run that
wrote into the corpus and then failed its own gate is exactly the case worth
naming.

## Running a gate yourself

Every job above is one command, and none of them needs a runner:

```console
mise run check
mise run test-integration
mise run test-race
mise run dogfood
mise run package
mise run cover-integration
mise run bench
```

`mise run test` is `check`'s middle third on its own, and `mise run test-cost`
is the same suite with a per-package cost and skip table printed after it, which
is what `platform-tests` runs so the table lands in the job summary.

Three jobs also run `mise run test-cache-status`, which reports the size of the
test-owned build cache and never fails: it is `continue-on-error` everywhere it
appears, because how large a cache grew is a thing to know and not a thing to
gate on.

## Using go-mutants in your own CI

go-mutants needs a Go toolchain and nothing else. The shape that works:

```console
go-mutants run --strict
```

and, for a pull request that should not pay for the whole catalogue:

```console
go-mutants run --changed --strict
```

Three things are worth knowing before you make it a required check.

**Give it a cache directory that survives.** The outcome cache is keyed on
everything that could change a verdict, so a second run over unchanged code
measures only what moved. Without a cache it measures everything, every time.

**`--shard K/N` fans out, and `report merge` puts the parts back together.**
Each shard discovers and validates the whole catalogue and executes its own
share, so the documents are directly comparable, and the merge refuses anything
that is not every part of exactly one run.

**Upload `reports/mutation/`.** The HTML report opens from `file://` with the
network unplugged, and a failed run also writes a diagnostics bundle whose
`manifest.json` a job can read to say which failure it uploaded. See
[Diagnostic codes](errors.md) for what the code in it means.
