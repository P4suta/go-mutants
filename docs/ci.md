<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Continuous integration

**Status: implemented.** `internal/testkit/cidoc_test.go` checks the workflow, job, task, and composite-action names documented here, including the dependencies of the required aggregate.

## The workflows

| Workflow | Trigger | Purpose |
| --- | --- | --- |
| `ci.yml` | Pull requests, merge groups, and manual dispatch | Always run text checks, then select verification by changed contracts; manual dispatch selects every gate |
| `nightly.yml` | Manual dispatch with one required `check` choice | Run only the requested deeper campaign |
| `release-please.yml` | Pushes to `main` | Maintain a release proposal through `release-pr` |
| `release-publish.yml` | Manual dispatch behind the release environment | Publish through `publish` after the separate release decision |

CI does not repeat on a push to `main`, and no workflow runs a scheduled verification campaign.
The release workflows retain their existing approval and publication boundaries.
See [the release checklist](release-checklist.md).

## The gates

The selection rules live in `.github/filters/ci.yml`.
Go sources and module dependencies select source checks.
Native behavior selects all three supported hosts and the race detector; known pure packages and documentation-contract tests need only source checks.
Release files select packaging, and the composite action or its implementation selects its smoke test.
Changes to the shared tool configuration, selection rules, or CI workflow select every gate.

| Job | Local command or boundary | Selection |
| --- | --- | --- |
| `quality` | `mise run lint-text` and `committed` | Every change; validates all selection outputs before exposing them |
| `checks` | `mise run check-source` | Go sources, dependencies, schemas, and source-check configuration |
| `platform-tests` | `mise run build` and `mise run test-cost-integration` | Native behavior on Linux, Windows, and macOS; the integration invocation already includes unit tests |
| `race` | `mise run test-race` | Native behavior on Linux |
| `artifacts` | `mise run release-assets-check` and `mise run package` | Release tooling, package metadata, and dependencies; snapshot artifacts remain unpublished |
| `action-smoke` | Build this checkout and run the composite action against a copied `fixtures/killable` | Composite-action behavior |
| `ci-success` | Validate every primitive job's result against the selected contracts | Always; rejects failed, cancelled, missing, or unexpectedly skipped selected jobs |
| `required` | Require `ci-success` to succeed | Always; the single terminal protection check |

`mise run check-source` checks formatting, both workspace and standalone-module builds, unit tests, and typed Go analysis.
`mise run check` remains the complete deterministic entry point, adding release-helper and text checks.
The pre-push hook uses the same source, native, and release scopes, always runs text and corpus checks, and leaves full mutation and packaging campaigns explicit.

## Manual verification

Select one `check` when dispatching `nightly.yml`.
The `selection` job rejects an unknown or missing choice before any campaign starts.
Manual verification keeps its configured scope and budgets; a safety timeout leaves its result incomplete.

| Job | Command or scope | Purpose |
| --- | --- | --- |
| `selection` | Validate the requested campaign | Reject invalid API inputs |
| `fuzz` | The fourteen `go test -fuzz` targets | Explore parser and rewrite boundaries |
| `property` | Property suites with `RAPID_CHECKS=2000` and `-count=5` | Broader exploration beyond the normal deterministic suite |
| `race-integration` | `mise run test-integration-race` | Exercise toolchain-backed integration under the race detector |
| `dogfood-audit` | `mise run dogfood-audit` | Run full mutation with a recording and independently reconstruct the report |
| `dogfood-runner` | `mise run dogfood-runner`, then `mise run report-diff` | Run the complete goatest scope and compare its report with the previous available report |
| `bench` | `mise run bench` | Collect timing observations |

Full mutation is also available locally as `mise run dogfood`.
Coverage remains an optional local signal through `mise run cover-integration`.
Neither full mutation nor coverage runs automatically for every pull request.
For changed-scope goatest verification, see [`goatest/docs/ci.md`](../goatest/docs/ci.md) and `mise run dogfood-changed`.

## Test isolation and diagnostics

Suite-running native jobs put the test-owned build cache and kept scratch under the runner's temporary directory.
The child builds use paths that exist for one run, so that cache is not restored across jobs.
`mise run test-cache-status` reports its size as a diagnostic.
`GO_MUTANTS_TEST_REQUIRE_TOOLS=1` turns a missing required tool into a failure on hosted runners.

`mise run check-corpus` rejects tracked, untracked, and ignored changes under `fixtures/` after verification.
The corpus remains a collection of independent inputs rather than a place to run mutation in place.
Failed native tests retain their diagnostic scratch for seven days.
See [the development guide](development.md) for local isolation, `mise run test-integration`, and cleanup.

## Using go-mutants in your own CI

Install the CLI with a compatible Go toolchain and run:

```console
go-mutants run --changed --strict
```

Keep the outcome cache between runs so unchanged mutants can reuse their verdicts.
For parallel execution, use `--shard K/N` and combine complete matching shards with `report merge`.
Retain `reports/mutation/` when you need the HTML report or failure diagnostics.

## The composite action

`.github/actions/go-mutants/action.yml` installs or uses the CLI and reads its outputs from the run report.
The run's policy controls its verdict; the action adds no separate score threshold.

| Input | Default | Meaning |
| --- | --- | --- |
| `version` | `latest` | Module version to install; `skip` uses a CLI already on `PATH` |
| `working-directory` | `.` | Module root |
| `args` | Empty | Additional `go-mutants run` arguments; do not repeat the action's `--json` |
| `report` | `go-mutants-report.json` | Report path, relative to the working directory unless absolute |
| `fail` | `true` | Fail the step when the run returns a nonzero exit status |

| Output | Meaning |
| --- | --- |
| `score` | Mutation score, or empty when nothing was scored |
| `total` | Catalogued mutants |
| `killed` | Mutants caught by tests |
| `survived` | Mutants not caught by tests |
| `timed-out` | Mutants decided by the timeout policy |
| `status` | `completed`, `interrupted`, or `failed` |
| `exit-code` | The CLI's exit status, independent of `fail` |
| `report` | Written report path |

A run that fails before writing its report emits a warning and its exit status rather than inventing zero-valued results.
