<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Run trace v1

**Status: `run --trace` records, `OpenOptions.Trace` records, and
`go-mutants trace` reads.** `trace/` is the public package,
`schema/trace-v1.schema.json` is the contract, and this page describes both. A
command line opens a recording with [`--trace`](#how-to-enable-it); an embedder
opens one by [handing `OpenOptions` a sink](#recording-a-library-workspace), or
by handing it nothing and reading the ring back afterwards.

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

`run` accepts `--trace[=DIR]`. It is the only command that measures anything and
therefore the only one that opens a recording; `list`, `doctor`, `init`,
`report` and `cache` reject the flag rather than accepting one that would do
nothing.

| Form | Effect |
| --- | --- |
| `--trace` | record into the default directory |
| `--trace=default` | the same; `default` is what `--help` shows the bare flag carrying |
| `--trace=DIR` | record into `DIR`, resolved against the workspace when relative |
| `--trace=` | refused: an empty directory is a shell variable that expanded to nothing |
| `GO_MUTANTS_TRACE=1` or `true` | the same as a bare `--trace` |
| `GO_MUTANTS_TRACE=DIR` | the same as `--trace=DIR` |
| `GO_MUTANTS_TRACE` unset, empty, `0`, or `false` | no directory; the run records in memory |

The value needs an equals sign, because it is optional: `--trace=recordings`,
not `--trace recordings`. pflag can only express an optional value that way, and
written with a space the directory becomes a positional argument — which `run`
refuses, saying how to spell it, rather than quietly recording somewhere else.
`--trace=` with nothing after it is refused for the same reason it would be a
mistake to accept: in a script it is almost always `--trace=$TRACE_DIR` with the
variable unset, and both other readings would record somewhere nobody named. An
empty `GO_MUTANTS_TRACE` is not that — it is how a job switches an inherited
request off, and it produces no flag at all. A directory genuinely named
`default` is asked for as `--trace=./default`.

An explicit flag always wins over the environment, so a job may ask for a trace
it cannot add a flag to and a nested job may switch an inherited one off or send
it elsewhere. The variable is read in `cli.Execute` alone, where it becomes the
flag the command layer parses: no layer below the command line reads the
environment, and a `--trace` after the `--` separator is an argument for the
test binary and stays one.

### Where a recording goes, and why there

The default trace root is `<workspace>/<report.directory>/trace/`, which is
`reports/mutation/trace/` unless the configuration says otherwise. Inside it,
each run gets a directory named by its own run id — the same id the run report
carries, so a recording and the document it explains can always be paired.

```console
$ go-mutants run --trace --no-tui
…
report json: reports/mutation/mutation.json
trace: reports/mutation/trace/20260906T182240Z-735f
```

`report.directory` is not an arbitrary corner. internal/snapshot digests the
workspace and excludes that directory and nothing else inside it, so it is the
one place in your tree where a file may appear while a run is measuring. A
recording grows as the run records into it: a stream written anywhere else in
the workspace would make the tree change under the run, and the run would report
drift it caused itself. `--report none` turns the two published documents off
and does not move the trace, because the directory is where a recording may live
for a reason about the snapshot rather than about formats.

So two directories are refused, each with one `trace-unavailable` note in the
recording, a `warning GOM1013` on standard error, and a run that goes on
recording in memory: one inside the workspace but outside `report.directory`,
and one that cannot be created or opened — which includes a run directory
another recording already owns. Refusing the trace is what keeps the trace from
failing the run.

A directory is judged by where it lands rather than by how it was spelled, so a
symbolic link is not a way past that refusal: `--trace=/tmp/alias/run` is
refused when `/tmp/alias` resolves into the workspace. Only the part of the path
that already exists can be resolved, which is the part that decides where the
directory the sink is about to create will land. A directory outside the
workspace is nobody's source and is always accepted.

Nothing is written under `TMPDIR`. The trace root is the workspace's, which
means it is somewhere a user can find, attach to a bug report, and delete — and
`report.directory` is already in most projects' `.gitignore`, which is the whole
of the ignore guidance this feature needs. If yours ignores `reports/mutation/`
you are done; if it ignores only `reports/mutation/*.json`, add the directory.

### Retention

A traced run collects its trace root as it opens its own recording, keeping the
newest `trace.RetainRuns` — ten — and removing the rest. What it removed is a
`trace-gc` note in the recording that removed it. Collection is best effort: a
directory that could not be tidied is a note and never the exit status, because
the run is about to measure what it was asked to measure either way.

Collecting *before* the run's own directory exists is what makes the rule simple:
the recording being written cannot be a candidate for its own collector, so
nothing has to be excepted from the rule to protect it.

Two things are never collected. A file or a directory somebody else keeps in the
trace root is not a recording — only a directory named by a run id and holding a
`trace.jsonl` is. And a recording whose stream does not end with its `run-end` is
left alone: that is a run still in progress, or one that died, and the second is
the recording you most want to keep. A collector that removed the account of the
crash while keeping ten accounts of runs that went fine would be collecting
exactly backwards. `go-mutants trace clean --all` is how somebody who has read
them says so.

### Reading a recording

```console
go-mutants trace list                # every recording here, newest first
go-mutants trace summary             # the newest one: where the run went
go-mutants trace summary RUN-ID      # or one of the others, or a path
go-mutants trace diff BEFORE AFTER   # what moved between two runs
go-mutants trace validate FILE       # every line against the published schema
go-mutants trace clean --keep 3      # the collector, run by hand
go-mutants trace clean --all         # the unfinished recordings too
```

`trace clean` removes the trace directory itself once its last recording has
gone, so a workspace somebody has cleaned looks like one that was never traced.

`trace list` says of each recording whether it is `complete` — it ends with its
`run-end` and lost nothing — `incomplete`, meaning it was interrupted, or
`lossy`, meaning events are missing from the middle of it. That is the first
thing to check before reading a count out of one. `trace summary` prints the
phases and stages with their durations, the commands tallied by kind, and the
mutant outcomes; `trace diff` prints the deltas between two, which is how a run
that got slower is investigated without reading either stream by eye.

### Recording a library workspace

`OpenOptions.Trace` is a `trace.Sink`, and a `Workspace` records into it
everything it does: the toolchain probe, the sweep it runs before copying
anything, both frozen trees, every preparation stage, every validation build and
test-binary compile, every mutant execution and probe pass, and every directory
a `KeepTemp` close left behind. The recording opens with a `run-start` of kind
`workspace` and closes with the `run-end` that `Workspace.Close` writes.

```go
ws, err := gomutants.Open(ctx, root, gomutants.OpenOptions{Trace: mySink})
```

A nil `Trace` does not switch recording off. The workspace records into a
bounded ring instead — `trace.DefaultRingCapacity` events, wrapped in
`trace.Digested` — and `Workspace.Recording()` hands the events back, before or
after `Close`. That is the same bargain an untraced `run` makes, for the same
reason: the failure nobody expected is exactly the failure nobody thought to ask
for a recording of. A caller that *did* supply a sink gets `nil` from
`Recording()`, because a second, shorter copy of what the sink already holds
would only be a second document to reconcile.

The sink belongs to the caller and is never closed by the workspace. A sink that
returns an error or panics costs the event and never the run: nothing here
enters a catalogue digest, a mutant identity, a result or an error, and the
`run-end` says how many events were lost.

A supplied sink is not wrapped in `trace.Digested` — a sink writing to disk is
meant to preserve the captured output — so a sink that keeps events in memory
should wrap itself, or it grows with the run rather than with its own capacity.
A failed `Open` records only into a supplied sink: it ends the recording with a
`run-end` of verdict `failed`, but no workspace is returned, so a default ring
dies with the workspace that never existed.

A `KeepTemp` workspace records each per-call scratch directory where it is
kept — an `artifact` of kind `kept-exec-scratch` immediately after the
`workspace-exec`, `mutant-exec` or `probe-exec` it belonged to — and the durable
directories at `Close`. A reader therefore finds a kept directory beside the
execution it explains, and a session that kept ten thousand of them cannot push
its own `run-start` and preparation timeline out of a bounded ring by reporting
them all at the end.

Three fields join a consumer's own recording to this one. `CommandResult.TraceSeq`
is the `exec` event of a `Workspace.Exec`, `MutantResult.TraceSeq` the
`mutant-exec` of a `Session.Exec`, and `ProbeResult.TraceSeq` the `probe-exec` of
a `Session.Probe`. `MutantResult.Binaries` and `ProbeResult.Binaries` are what
each ran, by import path, exactly as the events name them. See
[docs/library.md](library.md#tracing-a-session) for the API side.

The two commands a library workspace labels are its own: `workspace-exec` for
one an embedder asked it to run, and `verify` for the verification run inside
`Prepare`. Everything else is the label the layer underneath already gave it.

### The same events, printed

`run -vv` renders every recorded event onto the console as it is recorded, one
line each, indented two spaces — the same stream this document describes, drawn
by the renderer the run is already using rather than read back afterwards. It is
the fastest way to see where a run is spending itself, and it needs no
directory: `--trace` decides where the recording is *written*, `-vv` decides
whether it is *shown*, and the two are independent. What a line carries is the
payload's own fields, with durations in place of the timestamps (a console that
printed the stamps would make every line of two runs differ) and every line
break in the finished line spent on a space, so that counting lines counts
events. The lines carry no `seq`; the numbered stream is the `trace.jsonl` that
`--trace` writes, and a console line matches it by content, not number. They
keep their recorded order among themselves, but where they fall among the run's
own output is up to scheduling — they reach the console through a forwarder that
never makes a run wait for a terminal — so the indentation is what separates the
two streams, and `grep '^  '` or `grep -v '^  '` is how. A line is as wide as the
command it quotes rather than wrapped, because an argument vector is worth
pasting back into a shell. `run -v` draws one of these events in prose instead —
what a sweep reclaimed — and prints the whole reason a coverage-guided run had no
coverage under the warning that states it, which it carries on the event rather
than reading here.

### From a program

An embedder that wants a recorder without a workspace — its own commands, its
own phases — can hand `github.com/P4suta/go-mutants/trace` a sink and read the
recording back:

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

Nothing but the recorder ever writes into a stream. The two notes the command
line contributes — a `trace-unavailable` for a directory it refused, and the
`trace-gc` of the collection it ran — are handed to the run rather than spliced
into its recording, and the recorder emits them immediately after the
`run-start`, which is the moment they are about. Sequence numbers, timestamps
and the position of the last line all belong to the recorder; a caller inserting
an event into a stream it does not number is a caller that can break every one
of them.

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

The ring is what the diagnostics bundle of a failed untraced run is written
from, which is the reason it exists.

## The diagnostics bundle

A run that fails writes one directory holding everything needed to diagnose it
without running it again:

```text
<report.directory>/diagnostics/20260906T120000Z-1a2b/
  error.txt              the rendered failure, then %+v, then the typed chain
  environment.txt        tool, toolchain, platform, paths, and env NAMES
  doctor.txt             the `doctor` table, run against this workspace
  trace.jsonl            this run's recording, from the ring
  report.json            the run report, when the run had published one
  preserved-paths.txt    what --keep-temp left on disk, or a line saying nothing
```

A run traced to a directory gets its bundle *in that directory*, beside the
stream it explains, and no second `trace.jsonl` is written: a traced run's
recording is already the whole account, and the ring is a bounded suffix of it.
No file is ever written empty, so a bundle whose run published no report has no
`report.json` rather than an empty one.

`error.txt` is written first and `preserved-paths.txt` last, and that pair is
the collector's contract: the first makes the directory one of go-mutants', and
the last says it is complete. A bundle with no `preserved-paths.txt` is a run
that died while writing one, and the retention leaves it alone exactly as it
leaves a recording with no `run-end`. The diagnostics root keeps the newest
`trace.RetainRuns` — ten — collected as a bundle is written, and `go-mutants
trace clean` sweeps it with the rule it sweeps the trace root with.

Because a traced run's bundle lands *in* its recording's directory, the trace
root's rule asks both questions: a recording is finished when its stream ends
with `run-end` **and** the directory holds either no bundle or a finished one. A
half-written bundle therefore holds its recording back exactly as it would hold
itself back in the diagnostics root, instead of the answer depending on whether
the run happened to be traced.

Three runs write no bundle: one that succeeded, one that was interrupted, and
one told not to with `run --no-diagnostics` or `GO_MUTANTS_DIAGNOSTICS=0`. A
bundle that cannot be written costs a `GOM1014` warning and never the exit
status, which is [ADR 0001](adr/0001-trace-is-not-evidence.md)'s rule applied to
the other diagnostic: one that can change what a run reports inverts the point
of having it.

Values are never recorded, only variable names — see
[Environment names, never values](#environment-names-never-values), which is the
same rule for the same reason.

## Keeping the temporary directories

`run --keep-temp` leaves the run's snapshot and its scratch directory on disk
instead of removing them, which is the only way to answer "what did the tree
this mutant ran in actually look like". A bare `--keep-temp` — `always` — keeps
them whatever became of the run, a cancellation included: it is the word you
typed, and stopping a run *because* you have seen enough is the moment you most
want the tree. `--keep-temp=on-failure` keeps them only when the run failed,
never when it succeeded and never when it was cancelled, which is the mode a CI
job can leave switched on; a deadline that expired is a failure rather than a
cancellation, so it keeps for one. `--keep-temp=never` is the default, said out
loud. `GO_MUTANTS_KEEP_TEMP=1|true|always|on-failure|never` asks for the same.

Each kept directory is recorded as an `artifact` — `kept-snapshot` and
`kept-scratch` — and marked `kept` in its own owner marker, so the next run's
sweep obeys the decision rather than collecting the directory as an orphan
minutes later. The console prints `kept <kind>: <path>` for each, and
`preserved-paths.txt` names them in the bundle.

It is off by default because a kept snapshot is a whole copy of the module and
nothing will ever remove it. And it takes no part in a mutant identity, a
verdict, or a cache key: a keep is a decision about a directory taken on the way
out, after every mutant has been measured.

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

A `probe-run` is the one kind whose subject is a fact about the *pass* rather
than about the child: a probe pass is one measurement over the binaries it
selected, all appending to one infection log that is read once at the end. So it
names the import path when the pass was narrowed to a single binary — the pass
is then a measurement of that package — and nothing when it ran several, where
no single package names it and picking one of the set would be worse than
naming none. Which binaries a pass ran is in its `probe-exec` event, in
`binaries`.

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
| `infected` | the mutants whose site the pass made differ, by full identity, exactly as the pass recorded them; present on every measured pass, `[]` included |
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

It is the **raw** set: what the probe runtime recorded, before anything was
filtered. The library API's `ProbeResult.Infected` is that set minus the mutants
validation rejected — an infection fact about a mutant nothing will execute
licenses no skipping, and leaving one in would contradict the `Probed` field a
consumer reads it by. So a result is always a subset of the event it came from,
and the difference is always rejected mutants. The event is the account of what
the pass recorded; the result is what a caller may act on.

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

A `run --keep-temp` records one `kept-snapshot` and one `kept-scratch` as it
unwinds; see [Keeping the temporary
directories](#keeping-the-temporary-directories). `diagnostics` is reserved for
a producer that writes its bundle while it is still recording, and `run` is not
one: it writes [the bundle](#the-diagnostics-bundle) after the recording is
closed, because the recording is one of the things that goes into it.

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

`diagnostics` and `diagnostics-unavailable` are reserved and nothing emits them
today, for the same reason nothing emits the `diagnostics` artifact kind: `run`
writes [its bundle](#the-diagnostics-bundle) after the recording is closed,
because the recording is one of the things that goes into it. They are held for
a producer that writes one while it is still recording, and a reader should
expect them to be absent rather than treat them as missing.

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
for field, so a preparation timeline reads the same wherever it is read.

A consumer driving the library has a stronger join than that and does not have
to guess at all: `OpenOptions.Trace` puts both recordings under its own control,
and the `TraceSeq` on every `CommandResult`, `MutantResult` and `ProbeResult`
names the event exactly. The `(argv, dir, output_sha256)` match is what remains
for a consumer that ran `go-mutants` as a command rather than as a library.

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
