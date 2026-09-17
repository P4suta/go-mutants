<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Continuous integration

**Status: implemented, and checked.** Every workflow, every job, every task and
every input and output of the composite action named below exists;
`internal/testkit/cidoc_test.go` fails when one stops existing, when a new one
is not named here, or when the aggregate job stops waiting for one of the
others.

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
| `dogfood` | `mise run dogfood` — go-mutants against go-mutants | The gate on whether the tests *catch* anything. `--strict`, so one undeclared survivor fails it. The audit that reads this run's report against a recording of it is `mise run dogfood-audit`, and it is nightly rather than here — measured after it was wired up, it costs about a third again on the longest job in this workflow |
| `dogfood-runner` | `mise run dogfood-runner` — goatest against goatest | The runner's own gate, and the one that was running nowhere after the import: GitHub reads only the root `.github`, so the workflow that used to run it stopped being a workflow the moment it moved under `goatest/`. It starts inside the module with `GOWORK=off`, because the runner refuses a workspace on purpose and `DetectWorkspace` reads `go.work` as a file rather than from the environment |
| `action-smoke` | the composite action, over `fixtures/killable` | Builds this checkout onto `PATH` and passes `version: skip`, so what is measured is this source and not the last release. Asserts that every output arrived and that they agree with the report |
| `ci-success` | nothing | Needs every job above, so branch protection names one check instead of seven |

## The nightly searches

| Job | Runs | Why it is nightly |
| --- | --- | --- |
| `fuzz` | `go test -fuzz` over each of the fourteen targets, one job each | A fuzz run has no natural end. On a pull request it would either be too short to find anything or too long to wait for |
| `property` | the property suites at `RAPID_CHECKS=2000`, `-count=5` | Each rerun draws a fresh seed, which is the opposite of what a gate wants: the gate pins `RAPID_SEED=1` so a score cannot be a coin flip, and the exploration happens here |
| `race-integration` | `mise run test-integration-race` | Both tiers under the detector is an hour and a half |
| `dogfood-audit` | `mise run dogfood-audit` — dogfood with a recording, then the report re-derived from it | Not on a pull request, because it was measured: the recording costs about a third again on the full scope. It answers whether the document agrees with the account of what ran, which is not a question every push asks |
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
mise run dogfood-audit
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

## The composite action

`.github/actions/go-mutants/action.yml` is the six steps above written once. On
GitHub Actions:

```yaml
- id: mutants
  uses: P4suta/go-mutants/.github/actions/go-mutants@main
  with:
    version: v0.1.0
- run: echo "score ${{ steps.mutants.outputs.score }}"
```

Everything it does is something you could write yourself. What it adds is that
the numbers come back as step outputs, **read from the run report rather than
scraped off the console**, so a later step can comment them on a pull request or
compare them with a previous run without parsing prose.

It does not add a threshold of its own. The run's own policy decides the exit
code — `policy.strict` and `policy.minimum_score` in your `.go-mutants.toml`, or
the flags you pass through `args` — because a second place to configure "how
good is good enough" is a second answer to a question the configuration file
already answers, and the two disagree the first time somebody changes one.

| Input | Default | What it is |
| --- | --- | --- |
| `version` | `latest` | The version to `go install`, as a module version. The literal `skip` installs nothing and expects `go-mutants` on `PATH` already, which is what `action-smoke` uses to measure the checkout it is testing |
| `working-directory` | `.` | The module root to run in |
| `args` | empty | Extra arguments for `go-mutants run`, as one shell word list. `--json` is supplied by the action and must not be repeated: it is how the outputs are read |
| `report` | `go-mutants-report.json` | Where to write the run report, relative to the working directory unless absolute |
| `fail` | `true` | Whether a non-zero run fails the step. `true` is what a gate wants; `false` reports the numbers and leaves the decision to a later step, which is what a pull-request comment wants |

| Output | What it is |
| --- | --- |
| `score` | The mutation score as a percentage, or empty for a run that scored nothing |
| `total` | How many mutants the run catalogued |
| `killed` | How many mutants a test caught |
| `survived` | How many mutants no test caught |
| `timed-out` | How many a timeout settled, which the score counts as detected |
| `status` | The run's own status — `completed`, `interrupted`, or `failed` |
| `exit-code` | What `go-mutants run` exited with, whatever `fail` was set to |
| `report` | The path the run report was written to |

A run that fails before it can publish leaves no document, and the action says
so — a warning annotation and the exit code — rather than reporting zeroes. "0
mutants, 0 killed" is a number a job would compare against last week's.
