<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Run trace v1

**Status: the format, the recorder and the readers exist; nothing asks for a
recording yet.** `trace/` is the public package, `schema/trace-v1.schema.json`
is the contract, and this page describes both. The flag and the environment
variable that open a recording, and the wiring that fills one, arrive in later
changes; see [How to enable it](#how-to-enable-it).

The first trace contract is `gomutants-trace-v1`. A trace is the diagnostic
account of one run: the phases it passed through, the steps inside them, every
subprocess it started, how coverage placed each mutant, what became of every
mutant execution, and what it wrote or could not write.

A trace is never evidence. It takes no part in a verdict, in a mutant identity,
or in the key a cached result is stored under, and a trace that cannot be
written costs a note rather than the run. See
[ADR 0001](adr/0001-trace-is-not-evidence.md) for the reasoning and
[the run report](json-schema.md#go-mutantsrun-report-v1) for the artifact that
*is* the durable claim.

## What a trace answers that a report does not

A report says a mutant survived. A trace says which test binaries were run
against it, with which arguments, under which timeout, for how long, and where
their output was preserved. A report says a run took nine minutes; a trace says
that eight of them went into compiling test binaries. A warm report says
`cached: true`; a trace says which key the entry was found under.

That is the division of labour. The report is the claim; the trace is the
account of how the claim was arrived at.

## How to enable it

Not yet. There is no `--trace` flag, no `GO_MUTANTS_TRACE` variable and no
`trace` subcommand in this release: the format and its recorder landed first so
that everything recorded into them is recorded against a contract rather than
into a shape that is still moving.

What is already usable is the library surface. An embedder can hand
`github.com/P4suta/go-mutants/trace` a sink of its own and read the recording
back:

```go
ring := trace.NewMemorySink(trace.DefaultRingCapacity)
recorder := trace.New(trace.Digested(ring), time.Now, trace.StartRecord{
    Kind:        trace.StartKindWorkspace,
    ToolVersion: version,
    PID:         os.Getpid(),
    Root:        root,
})
defer recorder.RunEnd("closed", 0, nil)
```

`Digested` is not optional around a ring. A ring exists to cost a bounded
amount of memory, and captured output grows with the run instead of with the
ring; the wrapper strips those bytes — and the `output_truncated` and
`output_path` that describe the file they were preserved in — leaving the size
and the digest a reader joins on.

When the flag arrives it will write into the same format, through the same
`DirSink`, so anything written against this page keeps working.

## Directory layout

A recording lives in a directory of its own, named for the run it records, so
that the same trace root can serve every run traced into it:

```text
<trace root>/
  20260906T120000Z-1a2b/
    trace.jsonl
    output/7.txt
    output/12.txt
```

The run directory is created exclusively. Recordings would otherwise collide,
because everything in a recording is numbered from its first event: a second
run into one directory would append to the first run's stream and write its
`output/<seq>.txt` over the files the first run's events digested. Creating the
directory rather than opening it is also what keeps two go-mutants processes
tracing one workspace out of each other's recording.

`trace.jsonl` is JSON Lines: one event object per line, in sequence order. Each
completed event is written once and is immediately readable, so a run that
hangs, is interrupted, or is killed leaves a readable prefix and only its
`run-end` is missing. Normal shutdown syncs the stream once. An operating-system
or storage failure may lose a recording and can never change a verdict.

`output/<seq>.txt` holds the captured output of the command recorded at that
sequence number. Output is preserved beside the stream rather than serialised
into it, and a preserved file is capped at 1 MiB and ends with a `...` marker
when it did not fit. The digest and the byte count in the event always cover the
whole capture, whether or not the file was truncated — so two runs are compared
on what their commands produced rather than on what fitted.

## Recording without a directory

A run that writes no file still records. It keeps its last
`trace.DefaultRingCapacity` — 4096 — events in a ring in memory: no directory,
and a bounded price a run of any length pays once. The reason is that a failure
nobody expected is exactly the failure nobody thought to ask for a recording
of, so the account that explains one cannot be an account a flag had to open in
advance.

That recording is the same event stream this page describes, read back in
process rather than from a file, with two differences a reader should expect. It
holds the last events rather than all of them, and says in `events_dropped` how
many fell out. And `trace.Digested` strips the captured output bytes and the
`mutant.output_tail` before the ring, because those grow with the run instead of
with the ring; the size and the digest stay, which is what a reader joins on.

## Event envelope

Every line has the same five envelope fields, followed by exactly one payload
named after the concept it carries. The schema rejects unknown fields, so a
reader may decode strictly.

| Field | Present | Meaning |
| --- | --- | --- |
| `seq` | always | position in the recording, counting from one |
| `type` | always | which event this is, and therefore which payload it carries |
| `schema` | `run-start` only | `gomutants-trace-v1` |
| `timestamp` | always | when the event was recorded, RFC 3339 in UTC, nanosecond precision |
| `elapsed_ms` | always | milliseconds since the recording started |

Sequence numbers are assigned under the same lock that hands the event to the
sink, so the order of the file and the order of `seq` are one order however many
workers record at once.

## Event types

| `type` | Payload | Recorded when |
| --- | --- | --- |
| `run-start` | `start` | the recording opens; always the first line |
| `phase-start` | `phase` | the run enters a phase |
| `phase-end` | `phase` | the phase ends, with its duration |
| `stage` | `stage` | one step inside a phase starts or finishes |
| `prepare` | `prepare` | one library preparation stage starts or finishes |
| `exec` | `exec` | a subprocess returned |
| `mutant-exec` | `mutant` | one attempt at one mutant returned |
| `probe-exec` | `probe` | one pass through the probe tree returned |
| `validate` | `validate` | a validation or bisection step happened |
| `coverage-map` | `coverage` | coverage placed one mutant |
| `cache` | `cache` | the cache was opened, consulted, or written |
| `snapshot` | `snapshot` | a tree was frozen |
| `sweep` | `sweep` | collection reclaimed a temporary directory |
| `artifact` | `artifact` | the run wrote or kept a file or directory |
| `note` | `note` | the run could not do something |
| `run-end` | `run` | the recording closes; always the last line |

The type and the payload are one contract, in both directions: a line carries
the payload its type names and never another one, and a payload appears only on
the event type it belongs to. `schema` is the same kind of pairing, which is why
`run-start` is the only line that may carry it. The schema enforces all of it
with `if`/`then` pairs in both directions, so a reader may switch on `type` and
reach for that payload alone.

### `start`

| Field | Meaning |
| --- | --- |
| `kind` | `run` for a command, `workspace` for a library workspace |
| `run_id` | the run identity the report is also named by, `YYYYMMDDThhmmssZ-xxxx` |
| `tool_version` | the build that recorded |
| `pid` | the process that recorded |
| `root` | the workspace the recording is about |
| `args` | the command line, verbatim |

`run_id` is what pairs a recording with a report: the same string names the
history file the run wrote. A `workspace` recording carries neither `run_id` nor
`args`, because a workspace is opened by a program rather than by a command
line; the schema refuses either field on one.

### `phase`

| Field | Meaning |
| --- | --- |
| `name` | the phase |
| `duration_ms` | how long it lasted; present on `phase-end` including when it is zero, and absent from `phase-start` |

The phases are `discover`, `baseline`, `mutate` and `report`, and a run passes
through them in that order. They are a sequence rather than a nesting: what a
phase says is where the run was, never what it was obliged to do. A run that
ends early — a red baseline, a signal — stops partway through the list, and the
last open phase ends when the run does, so every `phase-start` has a
`phase-end`.

### `stage`

One step inside a phase, recorded as a started/finished pair.

| Field | Meaning |
| --- | --- |
| `phase` | the phase that was open when the stage started |
| `name` | the step |
| `state` | `started` or `finished` |
| `result` | what became of it; present only on `finished` |
| `duration_ms` | how long it took; present only on `finished`, including when it is zero |
| `detail` | what it was working on; recorded on `started` where it is known |

Stages are where a phase becomes readable. "The mutate phase took ninety
seconds" says much less than "it spent eighty of them building test binaries",
and the second is a sentence only a stage can produce.

`phase` is stamped by the recorder from the phase that was open when the stage
started, rather than passed in, so no call site can mislabel one — and a stage
that outlives its phase is still attributed to it. A stage started with no phase
open carries none.

A started stage carries neither a result nor a duration. A finished stage always
carries its duration, so a reader can tell a stage that took under a millisecond
from an event that was lost. The same stage name appears in more than one phase,
which is why a summary keys a stage by `<phase>/<name>`.

### `prepare`

One timed stage of a library mutation session's preparation. Its fields, its
stage names and its vocabulary are goatest's exactly, so one preparation
timeline reads the same in both recordings.

| Field | Meaning |
| --- | --- |
| `phase` | `discovery`, `probe_snapshot`, `main_validation`, `main_restoration`, `verification`, `binary_build`, `probe_validation`, `probe_coverage_build`, or `probe_restoration` |
| `state` | `started` or `finished` |
| `result` | `succeeded`, `failed`, or `skipped`; present only on `finished` |
| `duration_ms` | stage duration; present only on `finished`, including when it is zero |

A skipped stage is still a started/finished pair with a zero duration, so a
reader can distinguish an intentional omission from an event that was lost. A
failed stage is the last completed preparation stage, because the preparation
call returns that failure.

### `exec`

One executed command, recorded after the child was reaped so that the execution
and its result are one line.

| Field | Meaning |
| --- | --- |
| `kind` | what the command was; see below |
| `subject` | what it was about: a mutant id, an import path, a pattern |
| `argv` | the complete argument vector, executable first, exactly as the child received it; `[]` and never `null`, even for a command that could not be run |
| `dir` | the working directory |
| `env_names` | the environment variable *names*, sorted and deduplicated |
| `timeout_ms` | the timeout the command was given |
| `exit_code` | the exit status |
| `timed_out` | whether the timeout ended it |
| `duration_ms` | how long it ran |
| `output_bytes` | size of the whole captured output |
| `output_sha256` | SHA-256 of the whole captured output |
| `output_truncated` | whether the preserved file was cut at the 1 MiB cap |
| `output_path` | the preserved output, relative to the run directory |
| `error` | the error the execution failed with, if it failed |

`kind` is an enumeration, and that is what makes "every subprocess is recorded"
checkable rather than aspirational: a call site can only forget to *label* a
command, and an unlabelled command is a recording that does not validate.

| `kind` | The command |
| --- | --- |
| `go-version` | resolving the toolchain |
| `scope-list` | expanding the configured scope patterns into packages |
| `baseline-build` | compiling the unmutated tree |
| `baseline-test` | one unmutated observation of the test command |
| `instrumented-baseline` | the tests against the instrumented but inactive tree, which is what proves instrumentation changed nothing |
| `covdata-textfmt` | converting a coverage directory into a profile |
| `go-list` | listing the packages a test binary set covers |
| `go-test-c` | compiling one test binary |
| `coverage-run` | one profiling run of a test binary |
| `mutant-run` | one test binary run with one mutant active |
| `probe-run` | one test binary run against the probe tree |
| `validate-build` | one compile of the instrumented tree during validation or its bisection |
| `workspace-exec` | a command an embedder asked the workspace to run |
| `verify` | re-checking the frozen tree before a session claims to measure it |

`subject` is the mutant id for `mutant-run`, the import path for `go-test-c`,
`coverage-run` and `covdata-textfmt`, the pattern for `scope-list`, and absent
where the kind says everything there is to say.

`output_truncated` and `output_path` describe a file, so they are set by the
sink that wrote it and never by the code that recorded the command. A sink that
keeps no file leaves both alone, and `trace.Digested` clears them along with the
bytes — which is what stops a recording that went through a ring from claiming a
truncation of a file nothing wrote.

`output_path` is absent when the command produced no output, and also when the
output could not be written — preserving output is best effort, and a failure
costs the path, not the event.

### `mutant`

One attempt at one mutant. It shares `id`, `display_id`, `package`, `args`,
`timeout_ms`, `outcome`, `killed_by`, `duration_ms` and `error` with goatest's
own mutant record.

| Field | Meaning |
| --- | --- |
| `id` | mutant identity, the one the report and the cache key on |
| `display_id` | the short identity go-mutants names the mutant by |
| `attempt` | which attempt this is, from one |
| `worker` | which execution slot ran it, from zero |
| `package` | the package the mutant belongs to |
| `binaries` | the test binaries this attempt ran, in order, by import path |
| `args` | the arguments the execution ran with |
| `timeout_ms` | the timeout the execution was given |
| `outcome` | `killed`, `survived`, `timed_out`, `inconclusive`, `errored`, or `not_run` |
| `killed_by` | the test binary that detected it, by import path; one of `binaries` |
| `duration_ms` | how long the attempt took |
| `exec_seqs` | the `exec` events of the binaries it ran, in order |
| `output_tail` | the tail of the killing binary's output |
| `error` | the error the attempt failed with, if it failed |

`attempt` is recorded rather than collapsed into a count, because "survived" and
"survived twice" are different facts about a flaky test: attempt 1 is the
concurrent pass and attempt 2 the serial retry a survivor is given.

A test binary has two names, and the contract uses each in one place. `argv[0]`
on an `exec` is the file the run executed — `argv` itself is the whole vector,
that executable followed by every argument it was given, exactly as the child
received it. `binaries`, `killed_by` and a `coverage-map`'s `covering` are the
*import path* of the package the binary was built from — the name the run
report's `killed_by` and `covering_test_packages` use, and the one that outlives
the temporary directory the file lived in. `killed_by` is therefore always one
of `binaries`, and a reader may join the two directly.

`exec_seqs` is the join into the commands underneath the attempt, and therefore
into their preserved output: a reader with an attempt in hand has the argv, the
exit status and the file for every binary it ran.

The `outcome` vocabulary is the library's — `github.com/P4suta/go-mutants`'s
`Outcome`, with underscores — and not the run report's, which spells the same
two outcomes `timed-out` and `not-run`. A trace records what the code that ran
called it; the report is where the document spelling is fixed. The field is
deliberately not enumerated in the schema, so that a recording made by an older
build stays readable when the vocabulary grows.

Attempts run concurrently and the recorder serialises them, so the stream holds
one complete line per attempt, in completion order.

### `probe`

One pass through the prepared probe tree, where no mutant is active and what is
measured is which mutants' sites ever differed.

| Field | Meaning |
| --- | --- |
| `package` | the package the pass ran |
| `binaries` | the test binaries it ran, by import path |
| `args` | the arguments it ran with |
| `timeout_ms` | the timeout it was given |
| `outcome` | `measured`, `test-failed`, `timed-out`, or `unavailable` |
| `exit_code` | the exit status |
| `duration_ms` | how long it ran |
| `infected` | the mutants whose site the pass made differ, by full identity; present on every measured pass, `[]` included |
| `exec_seqs` | the `exec` events of the binaries it ran |
| `error` | the error that stopped it, if one did |

Facts come from a `measured` pass alone. The other three outcomes, and a pass
carrying an `error` instead of an outcome, say nothing about any mutant: a
reader treats every mutant as possibly infected by such a pass rather than as
one the pass proved anything about. `infected` therefore appears beside
`measured` and nowhere else, and on *every* measured pass. The empty list is the
point of that second half: a measured pass that infected nothing is the
strongest thing the probe phase says about a binary — nothing it runs can
observe any of them — and omitting the list would make that claim
indistinguishable from a pass nothing measured. Both the schema and the reader
refuse a list beside another outcome, and a measured pass with no list at all.

`infected` names each mutant once, by the same full identity `mutant.id`
carries.

### `validate`

One step of establishing which catalogued mutants can exist: the
instrumentation, each compile, and each step of the bisection a red compile
starts.

| Field | Meaning |
| --- | --- |
| `tree` | `mutant` or `probe` |
| `op` | `instrument`, `build`, `gate`, `isolate`, `reject`, or `done` |
| `build` | which compile of this validation it was, from one |
| `failed` | whether that compile was red |
| `blamed` | the files the compiler named, in the order the search will take them |
| `pending` | how many files were still undecided after it |
| `path` | the file an isolation or a rejection is about |
| `candidates` | how many mutants that file offered |
| `accepted` | how many of them were kept |
| `mutant_id` | the candidate a rejection removed |
| `diagnostic` | the compiler's first line about it |
| `builds` | how many compiles the validation spent |
| `rejected` | how many candidates it removed |
| `exec_seq` | the `exec` event of the compile this step ran |

`gate` is the step that says the tree was broken with no mutant in it. Nothing
validation could reject would fix such a tree, so it refuses to bisect rather
than blame a candidate — and the recording says which of the two happened.

`done` closes the validation with what it spent. One build means the whole
catalogue compiled on the first try, which is the ordinary case; a `builds` in
the double digits is the signal that a schema or an operator is producing
candidates that cannot exist.

### `coverage`

How coverage placed one mutant.

| Field | Meaning |
| --- | --- |
| `mutant_id` | the mutant this placement is about |
| `path` | the mutated file |
| `start_line` | the first line of the coverage block the mutation was mapped into |
| `end_line` | the last line of it |
| `covering` | the test binaries whose profile reaches that block, by import path |
| `uncovered` | `true` when none does |

The lines are the *block's*, not the mutation's own span: a reader asking "why
was this binary run" is asking about the block coverage measured, and the
mutation's coordinates are in the report. A mutant whose position no block
contained carries zeroes.

`uncovered` says the same thing as an empty `covering`, recorded explicitly so
that a reader is not asked to infer a decision from an omitted array.

### `cache`

| Field | Meaning |
| --- | --- |
| `op` | `open`, `lookup`, or `store` |
| `mutant_id` | the mutant a lookup or a store is about |
| `result` | `hit`, `miss`, `corrupt`, `expected`, `written`, `failed`, `not-cacheable`, `unavailable`, or `opened` |
| `outcome` | the cached verdict a hit produced |
| `directory` | the store the run opened |
| `context_key` | the identity the entry is filed under: the context key truncated to `cache.ContextKeyLength` (16) hex characters, which is the directory name rather than the whole digest |
| `error` | why the step failed, if it did |

A hit is the one event that explains an absence of work, which is why a warm
run's recording is as long as it is: nothing ran, and the recording says so
mutant by mutant. `context_key` is the key the entry lives under; no trace
option enters it, which is why a traced and an untraced run share a cache.

### `snapshot`

| Field | Meaning |
| --- | --- |
| `kind` | `workspace` or `probe` |
| `source` | the tree that was copied |
| `dir` | where the copy is |
| `stable` | whether the copy carried the source tree's modification times with it |
| `files` | how many files it holds |
| `digest` | the frozen workspace digest |
| `duration_ms` | how long freezing took |
| `error` | why it failed, if it did |

`stable` is what decides whether the Go build cache can be reused across
snapshots, so a run that is unexpectedly slow is one `stable: false` away from
an explanation.

### `sweep`

| Field | Meaning |
| --- | --- |
| `parent` | the temporary directory collection ran in |
| `removed` | the directories it reclaimed |
| `removed_bytes` | how much that was |
| `live` | directories left alone because a running process holds their lock |
| `kept` | directories left alone because a `--keep-temp` asked for them |
| `error` | why collection could not finish, if it could not |

Housekeeping reports itself here and nowhere else, and it can never change a
verdict.

### `artifact`

| Field | Meaning |
| --- | --- |
| `kind` | what kind of file or directory it is |
| `path` | where it is, as the run recorded it |

The kinds are `report-run`, `report-latest`, `report-json`, `report-html`,
`overlay-manifest`, `probe-overlay-manifest`, `kept-snapshot`, `kept-scratch`,
`kept-probe-tree`, `kept-exec-scratch`, `coverage-profile`, `diagnostics` and
`trace`. The `kept-` kinds are the temporary directories a run was asked to
leave behind; their paths are absolute and outside the workspace, because that
is where a temporary directory is made, so a `path` is read as it was recorded
rather than resolved against anything.

### `note`

| Field | Meaning |
| --- | --- |
| `kind` | `warning`, `trace-unavailable`, `diagnostics`, `diagnostics-unavailable`, `trace-gc`, `coverage-unavailable`, or `prepare-failed` |
| `code` | the `GOMnnnn` code the run also reported to its console, where there is one |
| `detail` | the detail line that accompanies it |

A note is what the run could not do, said once. None of them can change a
verdict or an exit code. goatest spells this payload `progress` rather than
`note`, with the same `kind` and `detail` fields, so a consumer joining the two
streams reads go-mutants' `note` and goatest's `progress` as one kind of line.

`coverage-unavailable` carries the whole reason rather than its first line,
which is the difference between a note and the console warning beside it.

### `run`

| Field | Meaning |
| --- | --- |
| `verdict` | how the run ended |
| `exit_code` | the status the process is about to exit with |
| `error` | the error that ended it, if one did |
| `events_emitted` | events the sink kept *before this one* |
| `events_dropped` | events the sink could not keep |

The accounting is never optional, because it is what tells a complete recording
from a lossy one.

The accounting is taken before this event is written, so `events_emitted`
excludes the `run-end` itself. That is goatest's rule as well, so one number
means one thing in both recordings. For an intact recording,
`events_emitted + events_dropped + 1` is the number of lines in the stream, with
`events_dropped` zero.

## Honest about loss

Recording is best effort, and best effort is only honest if the loss is
reported:

- every sink counts the events it could not keep, and the `run-end` event always
  carries `events_emitted` and `events_dropped`;
- a sink that reports its own drops is authoritative, so a bounded ring that
  discards an old event counts it even though nothing failed;
- a bounded ring keeps its last slot for the `run-end`, because the accounting
  is taken before that event is written and a ring with no room left for it
  would drop the one event that could have reported the loss;
- each completed line is immediately readable, so a killed run leaves a readable
  prefix, while normal shutdown syncs the stream once.

A reader therefore has exactly two things to check: that the last line is a
`run-end`, and that its `events_dropped` is zero. A recording without the first
was interrupted; one whose count is not zero is lossy, and says so. A sequence
gap says the same thing about a file that lost a line another way.

## Environment names, never values

`env_names` carries variable names alone, sorted and deduplicated, and the
recorder reduces any `NAME=VALUE` entry handed to it to its name. A trace
records which part of the environment a command could see and never what it
held. That is what makes a recording safe to attach to a bug report from a
machine holding real credentials, and it is a property of the recorder rather
than of its callers, so no future call site can leak a value by passing one. The
schema refuses an `env_names` item containing `=` as a backstop.

Captured output is not filtered — it is the output of the developer's own test
suite, and reducing it to a digest would defeat the purpose. It is preserved
beside the stream instead of inside it, so a reader can quote a line of it and a
publisher can drop the `output/` directory and keep the rest.

## What is deterministic

Every field of an event is deterministic except its `timestamp`, its
`elapsed_ms`, the `duration_ms` of a phase, stage, preparation stage, command,
attempt or probe pass, and the paths of temporary directories. JSON field order
is the declaration order of the event rather than a map iteration, so two
recordings of the same events differ only where time differs — which is what
lets a golden test pin the format.

The *stream* is a weaker promise than the fields. Test binaries, mutant attempts
and probe passes run concurrently and are recorded when they return, so a second
run of one workspace may interleave `exec`, `mutant-exec` and `probe-exec`
events differently and hand them different sequence numbers. What holds is the
relationship a reader needs: a mutant's `coverage-map` is recorded before the
attempts it explains, and `seq` order is the order of the file.

A recording also depends on what the run actually did. Trace options take no
part in cache identity, so a warm run answers from the cache — and its recording
says so rather than describing the work the cached result stands for.

## Joining a go-mutants recording to its consumer's

go-mutants is a library as well as a command, and its embedders record their own
runs. The `exec`, `prepare`, `mutant`, `artifact`, `note` and `run` payloads
therefore carry the same field names as goatest's own trace, which is the
consumer this alignment was designed against.

A command that appears in both streams is the same `(argv, dir, output_sha256)`
in both — the argument vector, the directory it ran in, and the digest of what
it printed — so two recordings of one execution can be matched without either
tool knowing about the other's sequence numbers. `prepare` is identical field
for field, so a preparation timeline reads the same wherever it is read. There
is no hook yet for handing go-mutants a sink of your own: `OpenOptions` gains
one in a later change, alongside the `--trace` flag. What the alignment already
fixes is the part that would be expensive to change afterwards — the field
names — so a consumer can write the join now and get one timeline across two
tools rather than two timelines to reconcile.

## Validating and reading

The schema is `schema/trace-v1.schema.json`, embedded in the binary, returned by
`trace.JSONSchema()`, and registered in `internal/schemas` as
`go-mutants/trace-event`. Each *line* of `trace.jsonl` is one instance of it —
this is the one published contract that describes a line rather than a file.

```go
events, err := trace.Read(dir)              // every event, strictly decoded
summary, err := trace.ReadSummary(dir)      // what the recording says about itself
delta := trace.Diff(before, summary)        // what changed between two runs
```

`Read` is strict, because a recording is a contract and a reader that silently
accepted a document outside it would report a run that never happened. It
refuses:

- an unknown field, in the envelope or in a payload;
- a trailing value after an event;
- an event carrying no payload, two payloads, or a payload its type does not
  name;
- an unknown event type;
- a `schema` on anything but a `run-start`, or a `run-start` without one;
- a `run-start` anywhere but the first line;
- any event after a `run-end`, a second `run-end` included;
- a sequence number that does not increase;
- a started `stage` or `prepare` carrying a result or a duration, or a finished
  one carrying no duration;
- a `phase-start` carrying a duration, or a `phase-end` carrying none;
- an `infected` list on a probe pass that was not measured, or a measured pass
  with none.

The last four are rules the schema cannot state: it validates one line at a
time, so where a line sits in a stream is the reader's to enforce.

A `run-start` that is missing altogether is *not* refused. A bounded ring that
overflowed begins partway through the run it recorded, which is a lossy
recording rather than an invalid one; `MissingSequences` is where it says so.

`ReadSummary` returns per-type counts, per-phase and per-stage durations,
`ExecByKind` — how many commands of each kind ran and how long they took between
them — and `MutantOutcomes`. `ExecByKind` is the number a performance question
is asked of: "where did the run go" is answered by the kinds, not by the event
types. `Diff` reports the deltas, which is how a run that got slower is
investigated without reading either stream by eye.

Everything on this page is also readable with `jq`:

```console
$ jq -r 'select(.type=="exec")
         | [.exec.duration_ms, .exec.kind, (.exec.argv|join(" "))] | @tsv' \
    trace.jsonl | sort -rn | head
$ jq -r 'select(.type=="stage" and .stage.state=="finished")
         | [.stage.duration_ms, .stage.phase, .stage.name, .stage.result]
         | @tsv' \
    trace.jsonl | sort -rn
$ jq -r 'select(.type=="mutant-exec")
         | [.mutant.display_id, .mutant.attempt, .mutant.outcome] | @tsv' \
    trace.jsonl
$ tail -n1 trace.jsonl | jq .run
```

`trace/schema_test.go` validates every recorded event against the embedded
schema, and two goldens pin every byte of the format:
`trace/testdata/events.golden.jsonl` holds one event of every type from a run
that went well, and `trace/testdata/events-failure.golden.jsonl` holds the other
half — an execution that timed out, a probe pass stopped before it reached an
outcome, an uncovered mutant, the whole validation vocabulary, a snapshot and a
sweep that failed, and an accounting that admits to a loss. A field that drifts
from this page fails the suite.
