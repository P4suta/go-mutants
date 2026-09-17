<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Command line

**Status: implemented.** Every command below exists, and
`internal/cli/docs_test.go` holds this page to the tree: a command the page
does not name fails the build, and so does a command the page names that the
tree does not hold. The `--help` of each one is recorded as a golden beside it,
so a change to what a user reads is a diff somebody reviewed.

The workspace root is the current directory. `.go-mutants.toml` is read from
there and nowhere else; see [Configuration](configuration.md) for what it holds.
Nothing here modifies your tree: every command that measures anything does so
against a disposable copy.

A directory holding a `go.work` is measured as **one run over every module it
joins**, so that a mutant is executed against every test that covers it —
whichever module compiled that test. `run` publishes a
`go-mutants/workspace-report` there rather than a run report, with each module's
own run report inside it, and `list` names the modules rather than a module
path. `report merge` puts a sharded workspace run back together module by
module, and the `report` and `explain` commands treat the workspace as a project
of its own — a run of one module measured alone is not one of its runs, because
the two mint different mutant identities. See
[ADR 0012](adr/0012-a-workspace-is-one-run-of-many-modules.md) for what that
costs.

## The commands

| Command | What it does |
| --- | --- |
| `go-mutants run` | Snapshot the workspace, prove the baseline, and run the mutants |
| `go-mutants list` | List the mutants a run would execute, without executing them |
| `go-mutants doctor` | Check that this machine can run go-mutants, and say what it found |
| `go-mutants init` | Write a commented `.go-mutants.toml` with the built-in defaults in it |
| `go-mutants explain` | Say why one mutant got its verdict, and how to reproduce it |
| `go-mutants report` | Work with the documents a run wrote |
| `go-mutants report list` | List the runs this module has recorded, newest first |
| `go-mutants report latest` | Print the newest run this module recorded, and where it is filed |
| `go-mutants report clean` | Delete this module's run history |
| `go-mutants report merge` | Combine the reports of a sharded run into the whole run's report |
| `go-mutants report validate` | Check a run report against the schema go-mutants publishes |
| `go-mutants cache` | Work with the outcomes go-mutants has proven before |
| `go-mutants cache status` | Print where the outcome cache lives and what is in it |
| `go-mutants cache gc` | Delete stored outcomes written more than N days ago |
| `go-mutants cache clean` | Delete every stored outcome |
| `go-mutants trace` | Read the recordings `run --trace` wrote |
| `go-mutants trace list` | List the recordings in this workspace, newest first |
| `go-mutants trace summary` | Summarise one recording: where the run went, and what it ran |
| `go-mutants trace diff` | Compare two recordings and print what moved between them |
| `go-mutants trace validate` | Check a recording against the schema go-mutants publishes |
| `go-mutants trace clean` | Delete the recordings in this workspace |

`completion` and `help` come from cobra rather than from this repository. They
are checked too — the root's command list is compared with the tree plus those
two, in both directions — so a cobra release that adds a third is a failure here
rather than something a user finds in their `--help`.

## `go-mutants run`

The one command that measures anything. It copies the workspace into a
disposable snapshot, proves the unmutated tests pass there, discovers the
candidates, validates that they compile, instruments the snapshot once, and
measures one mutant per test process — then writes a `run-report-v1` document
and publishes `reports/mutation/mutation.{json,html}`. At a `go.work` root the
document is a `workspace-report-v1` instead, holding one run report per module.

The flags that change a verdict rather than a rendering:

| Flag | What it decides |
| --- | --- |
| `--include`, `--exclude` | Which files are worth mutating; each **replaces** the configured list rather than adding to it |
| `--operator`, `--profile` | Which rules are catalogued. `--operator` names families or rules and takes precedence over any tier |
| `--mutant` | One mutant, by id prefix, and nothing else |
| `--changed` | Only the mutants on lines that changed since a ref; needs git and a repository |
| `--shard K/N` | One share of the catalogue, for CI fan-out; `report merge` puts the parts back together |
| `--timeout`, `--memory` | Override the per-mutant bounds the run would derive from its own baseline |
| `--strict` | Fail on an undeclared survivor. It defaults to off: go-mutants does not fail your build unless you ask it to |
| `--isolate` | A copy of the instrumented tree per worker, put back between mutants. The escape hatch from the drift gate, and the only way to measure a suite that writes into the package directory it runs in |

Everything after `--` replaces `test.command` verbatim, and is never
shell-split.

`--trace[=DIR]` records what the run did; see [Run trace v1](trace-v1.md).
`--keep-temp` leaves the snapshot behind for `explain` to point at.

## `go-mutants list`

The same discovery `run` does, reported instead of executed. `--json` writes a
`go-mutants/catalog` document; `--explain` adds every site that was refused and
the reason it was.

It is the command to reach for when a score surprises you: the catalogue is what
a score is a fraction of.

## `go-mutants doctor`

One line per check — the toolchain and its version, the module this directory
is in (or the modules its `go.work` joins), git, the cache directory, the
platform, whether a per-mutant memory limit is enforced here, the configuration
file — with
`ok`, `warn` or `fail` beside each. A `warn` is a check that failed on something
only an opt-in feature needs and never fails the command; any `fail` exits 2.

`--json` writes a `go-mutants/doctor` document. The check names are stable
within the schema version, so a script may branch on them.

## `go-mutants init`

Writes a `.go-mutants.toml` in which every value is the built-in default and
every key carries the comment explaining it, so the file changes nothing until
somebody edits it. `--dry-run` prints it instead; `--check` exits 1 when the
file on disk is not what this build would write.

## `go-mutants explain`

Joins the run report and the recording beside it for one mutant: what it is,
what happened to it, which suites cover it, every pass the run made over the
test binaries with the commands underneath, and a command to paste that runs it
again. `go-mutants explain <path>:<line>` asks the same question from the other
end.

`--report`, `--run` and `--trace` choose which run it reads. `--json` writes the
same account as a [`go-mutants/explain`](json-schema.md#go-mutantsexplain-v1)
document instead of prose — one gatherer, two renderings — which is how a
program gets at the four things the join composes and neither source document
holds: the command to paste, the line that rebuilds the binary, this mutant's
share of each stage, and the tail of what its last pass printed.

## `go-mutants report`

Reads the documents a run wrote. `list` and `latest` show the history this
module has recorded, `clean` deletes it, `merge` combines the reports of a
sharded run, and `validate` checks any report against the schema this build
embeds.

The history is filed per workspace, and a workspace is identified by a digest of
its contents — so runs with an edit between them are stored apart, and `list`,
`latest` and `clean` gather one module's runs back together by the module path
in each document. Run them from a module root.

## `go-mutants cache`

Works with the outcomes go-mutants has proven before. `status` says where they
are and what is stored, `gc --days N` removes what was written more than N days
ago, and `clean` removes them all.

Nothing the cache does can change a verdict or fail a run. See
[Configuration](configuration.md) for `cache.mode` and what a run will and will
not reuse.

## `go-mutants trace`

Reads the recordings `run --trace` wrote. `summary` says where a run went and
what it ran; `diff` compares two summaries, which is how a run that got slower
is investigated without reading either stream by eye; `validate` checks one
against the published schema; `list` is newest-first and `clean` is the
collector a traced run applies to the newest ten.

## Exit codes

Every command's help ends with this table, and a test keeps the two equal — and
both equal to the codes `internal/mutation` declares.

| Code | Meaning |
| ---: | --- |
| `0` | the run completed and no policy gate failed |
| `1` | an opt-in gate failed (`--strict`, `policy.minimum_score`, `init --check`) |
| `2` | an infrastructure, configuration, baseline, or expectation failure |
| `130` | interrupted (Ctrl-C) |
| `143` | terminated (SIGTERM) |

**130 and 143 publish a partial report first.** A run that is interrupted or
terminated writes what it had measured before it exits, so the id of every
mutant already settled is on disk rather than lost.

## When something fails

Every failure carries a `GOM` code, and
[Diagnostic codes](errors.md) says what each one means and what to do about it.
A failed run also writes a diagnostics bundle — the rendered failure, the
environment's variable names, a `doctor` table, the run's own account, and an
index a program can read — under `report.directory`, or beside the recording
when the run was traced.
