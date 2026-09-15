<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Diagnostic codes

**Status: implemented.** Every code on this page is one a build of go-mutants
can print, and `internal/testkit/errordocs_test.go` keeps this page and the
constants in every `internal/*/errors.go` equal in both directions.

A code is the searchable name of a failure. It appears in console output, in
the `errors[]` and `warnings[]` of a run report, in a recording, and in a
diagnostics bundle -- so grepping this page, the issue tracker and a trace for
the same string asks all three the same question.

**A code is allocated once and never reused**, even after the condition that
earned it has gone. The two that have are listed under
[Retired codes](#retired-codes) with what they used to report, because a user
searching for what an old report said must find it rather than something else.

## How the numbers are allocated

The leading digit names the area, and the two after it name the block a package
owns. A block is written here as the pattern it is -- `GOM41xx`, `GOM720x` --
because ownership is not at a fixed width: `internal/runner` and
`internal/gocmd` share the seventy-twos, and `internal/tui` and
`internal/gitdiff` share the seventy-sevens.

A package does not re-code the failures of the packages it uses. A toolchain
that cannot be located is `GOM7210` wherever it is met, because two identifiers
for one condition means a user searching for the wrong one.

| Digit | What the failure is about |
| ---: | --- |
| `1` | The invocation -- what the command line itself could not accept |
| `3` | The configuration file |
| `4` | What a run decided to measure |
| `5` | The documents a run reads and writes |
| `7` | The machinery of a run |
| `8` | The commands that measure nothing |
| 0, 2, 6, 9 | Unallocated |

| Block | Package | What it is about |
| --- | --- | --- |
| `GOM10xx` | `internal/cli` | The invocation |
| `GOM30xx` | `internal/config` | The configuration file |
| `GOM40xx` | `internal/engine` | What a run decided to measure |
| `GOM41xx` | `internal/discover` | What is worth mutating |
| `GOM50xx` | `internal/schemas` | Validating a document |
| `GOM51xx` | `internal/report` | The run report and its store |
| `GOM52xx` | `internal/report` | The artefacts a run publishes |
| `GOM70xx` | `internal/snapshot` | The frozen copy |
| `GOM71xx` | `internal/probe` | What a run may skip |
| `GOM720x` | `internal/runner` | One supervised process |
| `GOM721x` | `internal/gocmd` | The toolchain |
| `GOM73xx` | `internal/instrument` | The rewrite |
| `GOM74xx` | `internal/validate` | Which mutants compile |
| `GOM75xx` | `internal/execute` | Measuring a mutant |
| `GOM76xx` | `internal/coverage` | Which tests reach a mutant |
| `GOM770x` | `internal/tui` | The dashboard |
| `GOM771x` | `internal/gitdiff` | What changed |
| `GOM78xx` | `internal/report` | Sharding and merging |
| `GOM79xx` | `internal/cache` | Outcomes a run has proven |
| `GOM80xx` | `internal/cli` | `doctor` |
| `GOM81xx` | `internal/cli` | `init` |
| `GOM82xx` | `internal/cli` | The run history |
| `GOM83xx` | `internal/cli` | Recordings |

## `GOM10xx` -- the invocation

What the command line itself could not accept, before a run began.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM1001` | An invocation the command line could not accept: an unknown command, an unknown flag, a flag with an unusable value, a positional argument where none belongs. | `go-mutants <command> --help` |
| `GOM1002` | A `--` passthrough that cannot be run: nothing after the separator, or a blank program name. | write the program after the separator: `go-mutants run -- go test ./...` |
| `GOM1003` | A working directory that cannot be read. | run from a directory that still exists and can be read |
| `GOM1004` | Two flags that contradict each other, such as `--json` with `--quiet`. | drop one of the two flags the message names |
| `GOM1005` | A `--mutant` value that is not a mutant id prefix at all — the wrong alphabet, or too short to mean anything. | `go-mutants list` prints the ids; pass a prefix of one |
| `GOM1006` | A selection that names only operators this pre-release build cannot discover yet. | drop the operator names, or select a tier with `--profile` instead |
| `GOM1007` | A catalogued mutant that discovery did not report as a candidate. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM1008` | A `--profile` that decided nothing because the configuration file names operators, which take precedence over any tier. | remove `mutation.operators` from `.go-mutants.toml`, or drop `--profile` |
| `GOM1009` | A `run --mutant` prefix, or an `explain` target, that is well formed and did not name exactly one mutant: nothing matched, or several did. | `go-mutants list` prints the ids; pass a longer prefix |
| `GOM1010` | A file named on the command line that could not be read at all: a path that does not exist, a directory, a file without permission. | check the path; `go-mutants report list` names the runs this module has stored |
| `GOM1011` | A file that was read and is not a run report this build can use: not JSON, the wrong document type, a schema version from another release, or a document the published schema refuses. | `go-mutants report validate FILE` says which rule the document breaks |
| `GOM1012` | That the GitHub Actions step summary could not be appended to. | — |
| `GOM1013` | A run that asked for a recording and could not have the directory it named: one inside the workspace and outside `report.directory`, where the stream would be read as part of the tree and reported as drift, or one that could not be created at all. | name a directory outside the workspace, or one under `report.directory` |
| `GOM1014` | A diagnostics bundle a failed run could not write: a directory that would land where the snapshot reads, or one that could not be created or filled at all. | make `report.directory` writable, or pass `--no-diagnostics` to stop asking |

## `GOM30xx` -- the configuration file

Every key is decoded strictly: an unknown key is an error carrying the line
and column where it was found, never a silently ignored typo.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM3001` | A configuration file that exists but could not be read. | — |
| `GOM3002` | A document that is not well-formed TOML, or a value whose type does not fit the key it was written under. | the message carries the line and column; `taplo check .go-mutants.toml` finds the rest |
| `GOM3003` | A key that no version of this schema defines, which is almost always a typo in a key that does exist. | the message names the nearest key it does know |
| `GOM3004` | A file with no `version` key. | add `version = 1`, or start from `go-mutants init` |
| `GOM3005` | A `version` this build cannot read. | upgrade go-mutants, or write the version this build understands |
| `GOM3010` | An include or exclude pattern that does not compile. | see `docs/configuration.md` for the `**` semantics this build accepts |
| `GOM3011` | An operator that is neither a family nor a rule in the catalogue. | `go-mutants list --explain` prints every family and rule |
| `GOM3012` | A profile that is not a tier. | one of `balanced`, `strong`, `all` |
| `GOM3013` | An expectation whose id is not a full mutant id. | `go-mutants list` prints the full ids; an expectation takes a whole one |
| `GOM3014` | One mutant id declared twice. | delete the second row |
| `GOM3015` | An expectation with no reason. | write the argument for the expectation, or delete the row |
| `GOM3016` | One operator selected twice. | delete the second entry |
| `GOM3020` | An empty test argv vector. | write the command, or delete the key to take the default `go test ./...` |
| `GOM3021` | A timeout that is not a Go duration. | a Go duration, such as `90s` or `2m` |
| `GOM3022` | A timeout of zero or less. | a positive duration, or delete the key to take the derived bound |
| `GOM3023` | A baseline_runs outside its range. | the message names the range |
| `GOM3024` | A test command whose first element, the program to run, is empty. | name the program to run as the first element |
| `GOM3025` | A memory bound that is not a byte size: a spelling the units do not cover, a value that is not a number, or one larger than any machine has. | a byte size such as `512MiB` or `2GiB` |
| `GOM3026` | A memory bound of zero or less. | a positive size, or delete the key to take the derived bound |
| `GOM3027` | A narrowing that is not test or package. | one of `test`, `package` |
| `GOM3028` | A probing mode that is not off or on. | one of `off`, `on` |
| `GOM3030` | A worker count outside its range. | the message names the range; delete the key to take `min(CPUs, 8)` |
| `GOM3040` | A cache mode that is not auto, on, or off. | one of `auto`, `on`, `off` |
| `GOM3041` | A cache directory that is absolute or escapes the workspace. | a relative path inside the workspace |
| `GOM3050` | A score floor outside [0,100]. | a number between 0 and 100 |
| `GOM3060` | A report directory that is absolute or escapes the workspace. | a relative path inside the workspace |
| `GOM3061` | A format that is not json or html. | one of `json`, `html` |
| `GOM3062` | One format listed twice. | delete the second entry |
| `GOM3063` | An HTML threshold outside [0,100]. | a number between 0 and 100 |
| `GOM3064` | Report.low above report.high. | raise `report.high` above `report.low`, or lower `report.low` |

## `GOM40xx` -- what a run decided to measure

The orchestration codes: the workspace, the baseline, the scope, and what a
run left behind. The seven-thousands below are the machinery underneath them,
and a failure there is reported by the package that owns it rather than
re-coded here.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM4001` | A workspace root that is empty or cannot be resolved against the current working directory. | run from the module root, or pass a root that resolves |
| `GOM4003` | An empty test command, or one whose program name is blank. | name the program to run as the first element of `test.command` |
| `GOM4004` | A per-run scratch directory that could not be created. | check that the temporary directory exists and is writable |
| `GOM4005` | A caller-supplied [Options.RunID] that is not one: anything other than [RunIDPattern], which is the form [NewRunID] mints. | leave `Options.RunID` empty and let `NewRunID` mint one |
| `GOM4010` | A snapshot that does not compile. | `go build ./...` in your own tree and fix what it reports |
| `GOM4011` | An unmutated test run that failed. | `go test ./...` in your own tree: a suite that is red before mutation cannot measure one |
| `GOM4012` | A baseline command that did not finish inside [BaselineCap]. | raise `test.timeout`, or narrow `test.command` |
| `GOM4013` | The semantic preservation gate: the instrumented snapshot, with no mutant activated, no longer passes the tests the pristine one passed a moment earlier. | file a bug and attach the recording from `go-mutants run --trace`: the instrumented tree is generated code |
| `GOM4014` | A snapshot that stopped matching its manifest in a way instrumentation did not cause. | the message lists the files; a suite that writes into its own package directory needs one of them moved to a temporary directory |
| `GOM4015` | A `go tool covdata textfmt` that would not run, or whose output could not be read back. | — |
| `GOM4020` | An explicit `test.timeout` that is not above the slowest baseline run. | raise `test.timeout` above the slowest baseline the message names, or delete the key |
| `GOM4021` | A `mutation.operators` entry that names neither an operator family nor a rule in the v1 catalogue. | `go-mutants list --explain` prints every family and rule |
| `GOM4022` | A `test.command` that go-mutants recognised as `go test` over package patterns, and whose patterns do not describe a scope any mutant can be measured in: one that names no package at all, a `go list` that would not resolve them, or a whole scope in which no package has a test file. | name package patterns that resolve and that have test files |
| `GOM4030` | A run stopped by a cancelled context, which in practice means Ctrl-C or SIGTERM. | — |
| `GOM4040` | A snapshot directory that survived cleanup. | delete the directory the message names |
| `GOM4041` | The same for the per-run scratch directory. | delete the directory the message names |
| `GOM4042` | A run whose results could not be written to the history store. | check that the OS cache directory is writable |
| `GOM4043` | A `--mutant` prefix that resolved against the catalogue but named a mutant compile validation had refused. | `go-mutants list --explain` says why compile validation refused it |
| `GOM4044` | Temporary directories left by earlier runs that this run could not collect. | delete the directories the message names |
| `GOM4045` | A directory [Options.KeepTemp] asked the run to preserve, whose owner marker could not be written — so it was removed instead of being left behind. | check that the temporary area is writable |
| `GOM4046` | A run whose context ran out of time. | raise the deadline, or narrow the scope with `--include` |
| `GOM4047` | A run in which no mutant is bounded in memory, and says which of the two reasons applies: nothing measured what the baseline runs cost, or this platform cannot watch a process tree while it runs and therefore cannot enforce a bound at all. | — |
| `GOM4048` | A run whose every timed baseline run was answered out of the toolchain's test result cache, which means nothing measured what the suite costs and the per-mutant budgets are sized on cache lookups. | add `-count=1` to `test.command` |

## `GOM41xx` -- what is worth mutating

Loading the packages, walking the types, and building the catalogue.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM4101` | A snapshot root that is empty, cannot be resolved, or is not a directory. | pass a snapshot root that exists and is a directory |
| `GOM4102` | A `go.work` this run cannot proceed on: one that does not parse, uses no module, names a directory that is not a module, joins two modules spelling one module path, or reaches outside the snapshot — or any workspace at all, when a single-module entry point was pointed at it. | the message names the line, or the module to point at |
| `GOM4103` | An include or exclude pattern that does not compile. | see `docs/configuration.md` for the `**` semantics this build accepts |
| `GOM4110` | That the package loader itself could not run: no `go` command reachable, a driver that failed, a cancelled context. | `go-mutants doctor` says whether this machine has a toolchain go-mutants can reach |
| `GOM4111` | Packages that failed to load or type-check. | `go build ./...` in your own tree and fix what it reports |
| `GOM4112` | A snapshot root that no loaded package calls its module root: an empty directory, a directory inside somebody else's module, or a tree with no Go packages at all. | run from the directory holding `go.mod` |
| `GOM4120` | A requested rule that the canonical registry does not know, or knows with different metadata. | upgrade go-mutants, or drop the rule from the selection |
| `GOM4130` | That a candidate's byte span does not cover the text the rule says it replaces. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM4131` | A candidate that internal/mutation refused. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM4140` | A source file that could not be read, or that is too large to address with the 32-bit span offsets identities use. | — |

## `GOM50xx` -- validating a document

The published JSON Schemas, and what happens to a document that does not
answer to one. Nothing on the writing path validates -- a schema violation in
a document go-mutants wrote is a bug for a test to catch -- so these are
reported by the two commands that read documents somebody else wrote.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM5001` | A document type this build has no schema for. | the document's `document_type` names something this build has no schema for; upgrade go-mutants |
| `GOM5002` | Bytes that are not a JSON document at all, so there is nothing to validate. | — |
| `GOM5003` | A well formed JSON document that the schema rejects. | the message carries the JSON pointer of the first violation |
| `GOM5004` | An embedded schema that could not be read or compiled. | — |

## `GOM51xx` -- the run report and its store

The RunReport v1 document every other output is derived from, and the history
directory under the operating system's cache. Most of these are invariants of
a document go-mutants itself builds, so most of their remedies are the same
one: the document is wrong, and that is a bug here rather than a mistake
there.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM5101` | A run id that is not a run id. | file a bug and attach the document |
| `GOM5102` | A status that is not one of the three a run can end in. | file a bug and attach the document |
| `GOM5103` | A missing or backwards run clock. | file a bug and attach the document |
| `GOM5104` | A workspace digest that is not 64 lowercase hex characters. | file a bug and attach the document |
| `GOM5105` | A selection that does not describe the catalogue: an unknown mode, or more mutants selected than there are. | file a bug and attach the document |
| `GOM5106` | A report with no test command in it. | file a bug and attach the document |
| `GOM5107` | A history write with no report to write. | file a bug and attach the document |
| `GOM5108` | A coverage block that does not describe the mutants underneath it: an unknown mode, a negative binary count, or a mutant marked uncovered that the run nonetheless has a measurement for. | file a bug and attach the document |
| `GOM5109` | A cache block that does not describe the mutants underneath it: an unknown mode, a negative or impossible counter, or a mutant marked cached that the block says could not have been. | file a bug and attach the document |
| `GOM5110` | A build with no catalogue at all. | file a bug and attach the document |
| `GOM5111` | A result or a rejection naming an id that is not in the catalogue. | file a bug and attach the document |
| `GOM5112` | One mutant claimed twice: two results, two rejections, or a result and a rejection for the same id. | file a bug and attach the document |
| `GOM5113` | A catalogued mutant that was neither rejected nor given a result. | file a bug and attach the document |
| `GOM5114` | A catalogued mutant that discovery never reported coordinates for. | file a bug and attach the document |
| `GOM5115` | An outcome that is not one of the six, in either spelling. | file a bug and attach the document |
| `GOM5116` | A result whose not-run reason and outcome contradict each other: a measured mutant that says why it was not measured, or a not-run mutant that does not. | file a bug and attach the document |
| `GOM5117` | A file that cannot be read back as a run report: not JSON, the wrong document type, a schema version this build does not know, or a field nothing here declares. | `go-mutants report validate FILE` says which rule the document breaks |
| `GOM5118` | A stored document that could not be read off the disk at all. | — |
| `GOM5119` | History that `report clean` could not delete, or a path it would not delete because it is not inside the store. | delete the path the message names, if it is yours |
| `GOM5120` | A report that could not be encoded as JSON. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM5121` | A mutant whose per-attempt rows contradict the rest of its row: executions on a mutant this run never executed — cached, uncovered or not-run — or a count that disagrees with the attempt count beside it. | file a bug and attach the document |
| `GOM5122` | A run whose memory bound and its source contradict each other: a number with no source, a source with no number, or `unavailable` beside a bound. | file a bug and attach the document |
| `GOM5130` | An operating system that will not say where its cache directory is, so there is nowhere to keep run history. | set the OS cache directory (`XDG_CACHE_HOME` on Linux) to somewhere writable |
| `GOM5131` | A history directory that could not be created or read. | check that the OS cache directory is writable |
| `GOM5132` | A history file that could not be written or moved into place. | check that the OS cache directory is writable and has room |
| `GOM5133` | A history directory that belongs to something else: it holds no go-mutants marker, or one naming a different workspace. | `go-mutants report clean` empties this module's history, or delete the directory the message names |

## `GOM52xx` -- the artefacts a run publishes

`reports/mutation/mutation.json` and `mutation.html`, the Stryker projection
and the self-contained page built from it. They are published atomically
together or not at all.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM5201` | A pristine source file the projection needs and cannot read: deleted, renamed, or outside the workspace. | run `go-mutants` against the tree the report was measured in |
| `GOM5202` | A source file that no longer holds the text a mutant was built from. | re-run `go-mutants run`: the source has changed since the report was written |
| `GOM5203` | A projection that does not satisfy the vendored mutation-testing-report schema. | file a bug and attach `reports/mutation/mutation.json` |
| `GOM5204` | A vendored schema that cannot be compiled at all, which is a broken build rather than a bad document. | — |
| `GOM5210` | A vendored browser asset that is not the one this build recorded: the embedded bytes, the SHA-256 in the source, and the digest in `PROVENANCE.json` do not all agree. | reinstall go-mutants: the embedded asset does not match the digest recorded beside it |
| `GOM5220` | A `report.directory` that could not be created or is not a directory. | make `report.directory` writable, or point it somewhere that is |
| `GOM5221` | A project artefact that could not be staged or moved into place. | make `report.directory` writable, or point it somewhere that is |
| `GOM5222` | The worse half of a failed HTML write: the `mutation.json` written beside it could not be put back as it was. | `reports/mutation/mutation.json` may be half-written; delete it and re-run |

## `GOM70xx` -- the frozen copy

Copying the module into a disposable snapshot, and refusing what cannot be
copied faithfully.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7001` | [Options] that cannot be honoured, such as a report directory that is absolute or climbs out of the source root. | make `report.directory` relative and inside the source root |
| `GOM7002` | A source root that cannot be read or is not a directory. | run from a directory that exists and can be read |
| `GOM7003` | An operating system failure while reading the tree: a directory that cannot be listed, an entry that cannot be stat'ed. | — |
| `GOM7004` | A symbolic link inside the source tree. | add an exclude pattern for the link, or replace it with the file it points at |
| `GOM7005` | A Windows reparse point — a junction, a mount point, or any other name surrogate — inside the source tree. | add an exclude pattern for it, or replace it with a real directory |
| `GOM7006` | A file that is neither a directory nor a regular file: a device, a socket, a named pipe. | add an exclude pattern for it |
| `GOM7007` | A file name that cannot survive the round trip through a '/'-normalized relative path, such as a name containing a backslash on a POSIX filesystem. | rename the file |
| `GOM7008` | A failure to create the snapshot directory itself. | check that the temporary directory exists and is writable |
| `GOM7009` | A failure while copying the tree into the snapshot. | check that the temporary directory has room |
| `GOM7010` | A [Snapshot.Cleanup] call that was refused because the recorded root does not look like a directory this package created. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7011` | A snapshot directory that survived every removal attempt, usually a file still locked by a test binary on Windows. | delete the directory the message names |
| `GOM7012` | A file a worker copy could not be put back: the tree the copy was made of no longer holds it, the destination cannot be removed, or the restored bytes do not digest to what the manifest recorded. | the last of those means the instrumented tree changed under the run; re-run without `--isolate` to see whether the shared tree drifts too |

## `GOM71xx` -- what a run may skip

Deciding, from an infection log, which executions a run has already proven
unnecessary. Every code here is a reason the run learned nothing and measured
everything, which is what it would have done without probing at all.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7101` | A probe pass could not be made: the tree would not build, a pass failed or timed out, or the runtime could not write its log. Always a warning, never a failure. | none needed; the run measured every mutant against every covering binary, as a run without probing does |
| `GOM7102` | A probe was asked for over no mutants or no test binaries, so there was nothing for it to say. Not a failure. | none needed |
| `GOM7103` | An infection log names a mutant index the catalogue cannot explain, so the catalogue and the probe tree were built from different discovery passes. Every fact of the run is discarded. | file a bug and attach the recording from `go-mutants run --trace` |

## `GOM720x` -- one supervised process

Every subprocess go-mutants starts goes through one choke point, and these are
what that choke point can report.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7201` | That the process tree could not be placed under supervision. | — |
| `GOM7202` | That the child process could not be started at all — the executable is missing, is not executable, or the working directory does not exist. | check that the program in `test.command` exists and is executable |
| `GOM7203` | A [Spec] that cannot describe a process, which in practice means an empty or blank argument vector. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7204` | That the child was started and supervised but the operating system then refused to say how it ended. | — |

## `GOM721x` -- the toolchain

Locating `go` and reading its version.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7210` | That no `go` executable could be located — nothing on PATH, nothing at the explicitly configured path, or something found only relative to a working directory this process can no longer read, which leaves it just as unreachable. | put `go` on PATH, or name it in `toolchain.go`; `go-mutants doctor` says what this machine has |
| `GOM7211` | That the executable was found but `go version` could not be run, timed out, or exited non-zero. | `go version` in your own shell and fix what it reports |
| `GOM7212` | That `go version` ran and printed something this package cannot read as a version line. | `go-mutants doctor` prints what the probe read |

## `GOM73xx` -- the rewrite

Splicing every compilable mutant into the snapshot once, and generating the
runtime that activates one of them. This is generated code, so almost every
failure here is a bug in go-mutants rather than a fact about the tree.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7301` | Source that go/scanner could not tokenize: an illegal character, an unterminated literal, invalid UTF-8. | `gofmt -l` the file the message names |
| `GOM7302` | A string or rune literal carrying a line break whose value could not be recovered, and so could not be re-spelled without the line break. | file a bug and attach the file the message names |
| `GOM7303` | That the flattened output does not re-tokenize to the token stream it was built from. | file a bug and attach the file the message names |
| `GOM7304` | Flattened output that still contains a line break. | file a bug and attach the file the message names |
| `GOM7310` | A splice whose span is malformed or reaches past the end of the source it is being applied to. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7311` | A splice whose span does not cover the bytes the splice says it replaces. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7312` | Two splices that would edit the same bytes, or two splices with the same span. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7313` | A span handed to [OffsetMap.MapSpan] that starts or ends inside replaced bytes, where no exact translation into output coordinates exists. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7320` | [Options] that cannot be instrumented against: no snapshot root, no module path, no catalogue, or a catalogue naming a path that leaves the snapshot. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7321` | A snapshot file the catalogue names that could not be read back. | — |
| `GOM7322` | Go source that go/parser rejected. | `go build ./...` in your own tree and fix what it reports |
| `GOM7323` | A guard hint naming a rewrite this phase cannot express: a form it does not emit, a statement Form S may not bury in a block, or a declaration Form D cannot turn into an assignment. | file a bug and attach the file the message names |
| `GOM7324` | A guard hint whose site span names no node of the kind the form needs — no expression for Form C, no statement for the other two — or a declaration missing the very token that declares. | file a bug and attach the file the message names |
| `GOM7325` | Rewrite sites the interval forest refused to place. | file a bug and attach the file the message names |
| `GOM7326` | Instrumentation that would move a line: a splice set that is not [LinePreserving], or output whose line count differs from the file it was built from. | file a bug and attach the file the message names |
| `GOM7327` | A file whose runtime import could not be placed, because neither a package clause nor an import declaration was found where the parsed file says one is. | file a bug and attach the file the message names |
| `GOM7328` | A snapshot the instrumenter could not write to, either an instrumented file or the generated runtime package. | check that the temporary directory has room |
| `GOM7329` | A catalogued mutant with no guard hint. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7330` | An infection log [ReadInfectionLog] will not read: an empty file, a missing header, a header naming another catalogue or another array width, a repeated header that differs from the first, an index that is not a decimal uint32, an index at or past the catalogue's size — which is every index there is when the catalogue is empty — or a last line the process that wrote it never finished. | file a bug and attach the log the message names |

## `GOM74xx` -- which mutants compile

One build, then bisection: the mutants that are not Go once written are
rejected with the compiler's own words rather than measured.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7401` | [Options] that cannot be validated against: no snapshot, no catalogue, no module path, or no located toolchain. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7402` | A catalogued file whose pristine bytes could not be read out of the snapshot before instrumentation rewrote it. | — |
| `GOM7410` | A `go build` that could not be run at all — no toolchain, no permission, a process that could not be supervised. | `go-mutants doctor` says whether this machine has a toolchain go-mutants can reach |
| `GOM7411` | A build that did not answer within [Options.BuildTimeout]. | raise `test.timeout`, or narrow the scope with `--include` |
| `GOM7412` | Validation cancelled through its context. | — |
| `GOM7420` | A snapshot that does not build with every guard removed. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7421` | A snapshot that does not build although every catalogued file has been isolated and each accepted subset compiled on its own. | file a bug and attach the recording from `go-mutants run --trace` |

## `GOM75xx` -- measuring a mutant

Building the test binaries once and running them against one mutant per
process.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7501` | [Options] that cannot be executed against: no toolchain, no snapshot root, or no binary directory. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7502` | A binary directory that could not be created. | check that the temporary directory exists and is writable |
| `GOM7503` | A `go list` over the snapshot that failed. | `go list ./...` in your own tree and fix what it reports |
| `GOM7504` | `go list -json` output this package could not decode. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7505` | A package whose test binary would not compile. | `go test -c` the package the message names and fix what it reports |
| `GOM7510` | An attempt to measure mutants against an empty set of test binaries. | name package patterns in `test.command` that have test files |
| `GOM7511` | A [MutantRun] that cannot be executed: no activation identity, or no timeout. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7512` | A per-worker temporary directory that could not be created, or a [Options.ScratchDir] that could not be resolved against the working directory. | check that the temporary directory exists and is writable |
| `GOM7513` | A test binary that could not be started or supervised for one mutant. | — |
| `GOM7514` | A test binary that exited with [instrument.UnknownMutantExit]: the generated runtime was handed an activation identity it does not know. | re-run without a cached catalogue: `go-mutants cache clean` |
| `GOM7515` | A [ProbeRun] that cannot be measured: no timeout, no log to record into, no test binary, a target overriding the harness timeout, or a binary subset this run does not have. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7516` | A probe tree's test binary that could not be started or supervised. | — |
| `GOM7517` | An infection log that exists and could not be read against the catalogue it was written for — unopenable, empty, truncated, or carrying another catalogue's header. | file a bug and attach the log the message names |
| `GOM7518` | A [ControlRun] that cannot be made: no timeout, no test binary, a target overriding the harness timeout, or a binary subset this run does not have. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7519` | A control run's test binary that could not be started or supervised. | — |
| `GOM7520` | A schedule stopped by a cancelled context, which in practice means Ctrl-C or SIGTERM. | — |
| `GOM7521` | A test binary that refused [testLogFlagName]: the standard flag package printed "flag provided but not defined" and exited 2. | — |
| `GOM7530` | A directory for one test binary's raw coverage data that could not be resolved or created, or that sits inside the snapshot. | check that the temporary directory exists and is writable |
| `GOM7531` | A test binary that would not run, or did not pass, during the coverage pass. | `go test ./...` in your own tree: the coverage pass runs the suite unmutated |

## `GOM76xx` -- which tests reach a mutant

Coverage guidance is an optimisation and can never fail a run: every code here
is a warning, and a run that publishes one measures every mutant against every
binary instead.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7600` | A `go tool covdata textfmt` document this package could not read: a missing mode line, a block record that is not in the documented shape, or a coordinate that is not a number. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7601` | Coverage-guided selection switched off because `test.command` is not `go test` over package patterns. | use the default `test.command`, or one that is `go test` over package patterns, to get coverage guidance |
| `GOM7602` | A coverage pass that did not produce anything usable — the profiling run failed, `go tool covdata` was not there or exited non-zero, the output could not be parsed, or every profile came back empty — and the run continuing with coverage off. | — |
| `GOM7603` | Tests that fail when run on their own and are therefore left out of test-level narrowing: a test that needs what the rest of its suite does first cannot be the sole witness of a mutant. | make the tests the message names pass on their own: `go test -run '^TestName$' ./pkg` |
| `GOM7604` | A set of tests that fails when run together without any mutant active, so a failure under a mutant could not be read as the mutant's doing. | make the tests the message names pass together without a mutant: `go test -run 'A\|B' ./pkg` |

## `GOM770x` -- the dashboard

The live terminal dashboard, and the one thing that can go wrong with it.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7701` | A terminal the dashboard could not drive: raw mode refused, the output handle rejected a mode change, the input reader failed. | `--no-tui` prints the same summary as deterministic plain lines |

## `GOM771x` -- what changed

`--changed` needs git and a repository, and fails rather than guessing when it
cannot read a diff: a narrowing that silently fell back to everything, or to
nothing, would be worse than not running at all.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7710` | A git that could not be run at all: not on PATH, not executable, or killed before it answered. | install git, or drop `--changed` |
| `GOM7711` | A workspace that is not inside a git working tree. | run inside a git working tree, or name what to measure with `--include` |
| `GOM7712` | A bare `--changed` in a repository whose HEAD has no upstream branch: a detached HEAD, or a branch that has never been pushed or tracked. | `--changed=REF` names a ref explicitly, such as `--changed=origin/main` |
| `GOM7713` | A ref that could not be resolved, or that shares no history with HEAD, or a repository with no commits at all. | name a ref this repository holds and that shares history with HEAD |
| `GOM7714` | A `git diff` that would not run or exited non-zero. | run the `git diff` the message names in your own shell |
| `GOM7715` | Diff output this package could not read. | file a bug and attach the diff the message names |
| `GOM7716` | That the untracked files could not be established: the `git ls-files` that names them would not run, or one of the files it named could not be read. | check that every untracked file under the workspace can be read |

## `GOM78xx` -- sharding and merging

`--shard K/N` splits a run, and `report merge` puts it back together --
refusing anything that is not every part of exactly one run.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7801` | A `--shard` value that is not a shard specification: not `K/N`, not two numbers, or a K outside 1..N. | `--shard K/N` with `1 <= K <= N` |
| `GOM7802` | A shard block a document cannot state — an index above its own total, or an assignment function this build does not implement. | file a bug and attach the document |
| `GOM7810` | A merge that was given nothing to merge. | pass the shard reports to `go-mutants report merge` |
| `GOM7811` | A document handed to `report merge` that no shard wrote. | pass documents a `--shard` run wrote |
| `GOM7812` | Two documents that do not describe one run: different tool versions, workspace digests, shard totals, assignment functions, changed refs, or catalogues. | merge the parts of one run: the same tree, the same catalogue, one report per shard |
| `GOM7813` | A set of documents that is not every shard exactly once: an index missing, or one supplied twice. | pass every shard exactly once |
| `GOM7814` | A shard whose rows disagree with the assignment function: it measured a mutant belonging to another shard, or disclaimed one of its own. | file a bug and attach the documents |

## `GOM79xx` -- outcomes a run has proven

Nothing the cache does can change a verdict or fail a run, so every code here
is a warning and a run that publishes one measures what it could have reused.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM7901` | A cache that could not be opened: no operating system cache directory, a directory that could not be created, or one the ownership marker refuses. | — |
| `GOM7902` | That the running executable could not be located or read, so its digest cannot enter the key. | — |
| `GOM7903` | A key context missing a field the recipe requires — no workspace digest, no test command. | file a bug and attach the recording from `go-mutants run --trace` |
| `GOM7904` | An entry that is on disk and is not an entry: truncated JSON, an unknown version, an outcome this build does not know, or a document filed under somebody else's id. | `go-mutants cache clean` removes every stored outcome |
| `GOM7905` | An outcome that could not be stored. | `go-mutants cache status` says where they are; check that it is writable |
| `GOM7910` | A cache directory that could not be listed, which is what `cache status` and `cache gc` walk. | `go-mutants cache status` says where they are; check that it can be listed |
| `GOM7911` | An entry or a directory `cache gc` or `cache clean` could not delete. | delete the path the message names |

## `GOM80xx` -- `doctor`

Whether this machine can run any of it.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM8001` | That at least one `doctor` check failed. | `go-mutants doctor` prints every check and what it wants |

## `GOM81xx` -- `init`

Writing, and checking, a `.go-mutants.toml`.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM8101` | A `.go-mutants.toml` that is already there. | `go-mutants init --dry-run` prints what it would write without writing it |
| `GOM8102` | A `.go-mutants.toml` that `init --check` could not read: a directory of that name, a permission failure. | — |
| `GOM8103` | A configuration file that could not be created: a directory that is not writable, a disk that is full, a name taken by something that is not a file. | make the directory writable, or run from one that is |
| `GOM8104` | `init --check` finding a file that is not what this build of `init` would write. | `go-mutants init --dry-run` prints what this build would write |

## `GOM82xx` -- the run history

`report list`, `report latest`, `report clean`, and the run `explain` reads.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM8201` | A history command run somewhere that is not a module root. | run from the module root, the directory holding `go.mod` |
| `GOM8202` | `report latest` — or `explain`, which reads the same run — finding no run for this module, or none under the id `explain --run` named. | `go-mutants run` first; `go-mutants report list` shows what is stored |

## `GOM83xx` -- recordings

`trace list`, `trace summary`, `trace diff`, `trace validate`, `trace clean`.

| Code | Meaning | Remedy |
| --- | --- | --- |
| `GOM8301` | A recording that is not there: a workspace that has never traced a run, or a run id nothing was filed under. | `go-mutants run --trace` records one |
| `GOM8302` | A file named on the command line that is not a recording this build can read: a path that does not exist, a directory with no stream in it, a stream whose events are outside the contract. | `go-mutants trace validate FILE` says which rule the stream breaks |
| `GOM8303` | A recording `trace clean` could not delete. | delete the path the message names |

## Retired codes

A retired code is never reused for a different meaning. A user searching for
one they saw in an old report must land on what it meant, not on something
else that now wears the number.

| Code | What it used to report | Why it went |
| --- | --- | --- |
| `GOM0001` | A run that ended after the baseline because the mutation phases were not implemented. | Its own documentation said it would disappear, code and all, when they landed. They have. |
| `GOM4002` | An exclude pattern that did not compile, from when `internal/engine` compiled `mutation.exclude` for the snapshot walk. | That was itself the bug: an exclude says what is worth mutating and has no business shrinking the tree that gets built and tested. Discovery owns the question and allocates `GOM4103` for it. |

## Where a code is declared

Every code is a typed constant in its package's `errors.go`, with the sentence
above it that this page's Meaning column is built from.
`TestEveryDiagnosticCodeIsDeclaredInAnErrorsFile` refuses one declared
anywhere else, because the ledgers over this page read those sixteen files and
nothing else.

`DiagnosticCode(err)` pulls the code out of any error chain the engine API
returns; see [the engine API](library.md#errors).
