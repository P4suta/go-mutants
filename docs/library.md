<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# The engine API

**Status: implemented.** `github.com/P4suta/go-mutants` — the module root, not
a subpackage — is the reusable mutation engine behind the `go-mutants` command.
A tool that wants mutation as a measurement rather than as a score-producing
run imports it directly, and everything on this page is pinned by
`external_contract_test.go` (which compiles a synthetic consumer module against
the surface) and `api_contract_test.go` (which checks the invariants below
against real prepared sessions).

Public values use standard-library types only. Discovery, instrumentation,
runner, cache, and report internals do not cross the package boundary, so a
consumer's build never links them and cannot depend on them by accident.

## Lifecycle

One `Workspace` is one frozen copy of one module, and it is prepared exactly
once:

```text
Open ──▶ Workspace.Exec*  ──▶ Workspace.Prepare ──▶ Session.Catalog
                                                    Session.Exec*
                                                    Session.Probe*
                                                    Session.Control*
                                                    Session.Changes*
                                                    Session.Close
                                                ──▶ Workspace.Exec*
                             ──────────────────────────────────────▶ Workspace.Close
```

- **`Open(ctx, root, options...)`** locates the Go toolchain, sweeps the
  leftovers of runs that were killed before they could tidy up, and copies
  `root` into a disposable snapshot. Nothing after this reads or writes the
  user's tree.
- **`Workspace.Exec`** runs one shell-free command against the frozen snapshot:
  a build, a vet, a baseline `go test`, a `go list`. Calls may run concurrently
  with one another, and with a prepared session.
- **`Workspace.Module`** reports what the frozen module holds — its path, its
  `go` directive, the located toolchain, and the packages a query matched — so
  that a consumer does not have to run `go list -json` itself and parse it. See
  [Listing the module](#listing-the-module).
- **`Workspace.Prepare`** discovers candidates, builds the catalogue,
  compile-validates every mutant, instruments the sources, verifies the
  instrumented program once, and compiles the selected packages' test binaries —
  optionally building a second, probe tree alongside. A workspace may be
  prepared exactly once, *including when preparation fails after it has begun*.
- **`Session.Exec` / `Session.Probe`** reuse those binaries for any number of
  (mutant, target) combinations without rebuilding or rewriting anything.
- **`Session.Control`** reuses the same binaries to run the *original* program:
  an execution minus the activation. See
  [`Session.Control`](#sessioncontrol).
- **`Session.Changes`** reports, in path order, anything written into the
  prepared snapshot since it was prepared — by a `Session.Exec` or
  `Session.Control` target, or by a `Workspace.Exec` command run beside the
  session. A `Session.Probe` target's writes are **not** reported: a probe pass
  runs in the probe tree, a second snapshot beside this one, which no call
  scans.
- **`Session.Close`** releases the binaries, the session scratch, and the probe
  tree. **`Workspace.Close`** closes the session first and then releases the
  scratch directory and the snapshot. Both are idempotent, and closing the
  workspace closes the session; closing the session does not close the
  workspace.

`Workspace.Exec` and `Workspace.Prepare` share one tree, and the lifecycle rule
is one line per state of the preparation:

| State | `Workspace.Exec` and `Workspace.Module` |
|---|---|
| no `Prepare` yet | runs; a command that *changes* the tree is refused by `Prepare`'s integrity gate |
| `Prepare` in flight, outside the instrumentation window | runs, beside the preparation |
| `Prepare` in flight, inside the instrumentation window | **waits**, until the window closes |
| `Prepare` succeeded | runs, beside the session |
| `Prepare` failed | refused, carrying `ErrPrepareFailed` |
| session closed, workspace open | runs — `Session.Close` releases the binaries and the probe tree, not the snapshot |
| workspace closed | refused, carrying `ErrWorkspaceClosed`, whatever the preparation did |

`Workspace.Module` is in the same column because it is the same call underneath:
it holds the workspace shared and the tree shared around a `go list` child, so
it overlaps a preparation everywhere a command does and waits for the window
where a command waits. The two refusals carry the same sentinels and name
themselves in the message — `gomutants: module: workspace is closed` beside
`gomutants: exec: workspace is closed` — because a consumer branches on the
sentinel and reads the sentence to find the line of its own program.

The last row wins over every other: a workspace can be closed *and* hold a
preparation that failed, and a consumer whose workspace is gone has to be told
that rather than sent to open another one, so `Exec` asks whether the workspace
is closed first.

The three middle rows are the ones worth stating outright.

The **instrumentation window** is the stretch of a preparation from its
integrity gate to the end of `main_restoration`: `main_validation` rewrites the
sources of the frozen tree in place and `main_restoration` puts them back, and
in between the files on disk are not the program anybody wrote. That is the only
part of a preparation a command has to be kept out of. Everything else it does —
discovery, the probe copy, verification, the two builds — reads the same frozen
bytes a command reads, so commands and a preparation **overlap**, which for a
real module is minutes of preparation a consumer can run its own `go vet`,
`go build` or baseline in.

The window is a lock rather than a policy check, and it is symmetric. A command
issued while the window is open waits for it and then runs against the restored
tree; a command already running when a preparation reaches its gate makes the
*preparation* wait, so a long baseline delays the window and can never corrupt
it.

A `PrepareOptions.Trace` callback is the one place a command may not be started
from, and the reason is worth stating in full because half of it is invisible.
The callback runs on the preparation's own goroutine. Inside the window that
goroutine holds the tree exclusively, so the command waits for a lock its own
caller is holding. Outside the window the command *runs* — and that is the
dangerous half, because it works until a `Workspace.Close` queues for the
workspace: Go's `RWMutex` hands out no more read locks once a writer is waiting,
so the command waits for the `Close`, the `Close` waits for the preparation, and
the preparation waits for the callback.

What the overlap costs is stated where it is paid: discovery now reads the tree
while a command may write it, so the catalogue is compared against the frozen
manifest at the top of the window (see
[Drift, and which stage names it](#drift-and-which-stage-names-it)).

A command **runs after a successful `Prepare`** because the tree it runs against
is the snapshot `Open` froze, byte for byte — or, if a command wrote there while
the binaries were compiling, the snapshot plus that write, which
`Session.Changes` names. `main_restoration` puts the pristine sources back and
re-digests the tree before a single test binary is built, so a `Prepare` that
returned a session has already proved the instrumentation was undone; the
instrumented sources exist only in the overlay manifest the session owns, and
nothing but `Session.Exec` and `Session.Probe` puts that manifest in a child's
environment. So `go vet`, `go build` and a control of the consumer's own belong
beside a live session rather than in a second workspace opened for them.

Nothing stops such a command from **writing** into the tree, and nothing
invalidates the session when one does: the overlay still names the frozen
sources, the binaries are already built, and executions go on answering. What a
write changes is the tree every later target runs in, and `Session.Changes`
reports it against the manifest preparation captured — the same call, and the
same answer, as for a write a target made.

What a write does **not** change is `Catalog.WorkspaceDigest` or
`Catalog.PreparedDigest`. Both were frozen by `Prepare` and neither moves for
anything a caller does afterwards, so the identity a consumer keys stored
evidence on ([What `PreparedDigest` covers](#what-prepareddigest-covers)) still
names a tree the measurements were not taken in. Evidence must therefore not be
carried across a write the consumer itself made, and `Session.Changes` is the
only thing that can say there was one. A consumer that writes there owns that.

Fuzzing is the write most easily made by accident. `Session.Exec` and
`Session.Control` run a fuzz target in a copy of the tree and reserve
`-test.fuzzcachedir`, so neither the snapshot nor another call's corpus can be
written; a `go test -fuzz=…` run through `Workspace.Exec` has neither, so the go
command writes any crasher it finds into `testdata/fuzz/` **in the frozen tree**,
where it becomes a seed for every later target and a change `Session.Changes`
reports.

A command is **refused after a failed `Prepare`** with `gomutants: exec:
workspace preparation failed; its tree may hold instrumented sources`, carrying
`ErrPrepareFailed`. A preparation that stopped part-way promises nothing about
the tree. Every failed preparation is refused, including one that stopped before
anything was instrumented: which failures left the tree alone is not a question
a caller could answer, and the engine does not answer it either. The workspace
is spent — `Prepare` refuses a second attempt with `ErrWorkspacePrepared` — so
the answer is to open another one.

## Locking and concurrency

A workspace keeps three locks, and they are three because they answer three
different questions. Whenever a call holds more than one, the outer is the
earlier in this table — `mu` before `tree` before `stateMu` — and only two paths
hold all three: `Exec`'s re-check once it has the tree, and a window that
publishes its failure before it unlocks. `Prepare`'s claim and `Close` hold `mu`
and then `stateMu`, never `tree`:

| Lock | Guards | Held by |
|---|---|---|
| `mu` (`RWMutex`) | the workspace's *lifetime*: its snapshot, scratch directory and toolchain | `Exec` and `Prepare` **shared**, for the whole of their calls; `Close` **exclusive** |
| `tree` (`RWMutex`) | the snapshot's *bytes* | `Exec` shared, while its command runs; `Prepare` **exclusive**, for the instrumentation window alone |
| `stateMu` (`Mutex`) | the four fields that say what has become of the workspace: closed, a preparation claimed, a preparation failed, the session | every call, briefly |

`mu` is what makes `Close` the one call that may take the tree away: it waits
for every command and for a preparation that is still discovering, instrumenting
or compiling, because a workspace that removed its snapshot underneath one of
those would be a use-after-free with a friendlier name. `Close` then releases it
while the directories are removed and retakes it to publish the result, so a
second `Close` waits on the first through a done channel and returns the same
error.

`tree` is what makes commands and a preparation overlap. A preparation holds it
exclusively only from its integrity gate to the end of `main_restoration`, so a
`Prepare` that takes two minutes blocks commands for the fraction of it that
actually rewrites the tree, and `Workspace.Exec` holds the shared half only
while its child runs.

`stateMu` exists because the fields it guards are now read and written by calls
holding no more than the shared half of `mu` — several of those running at once
is the point — so `mu` is not what keeps them consistent. It is also what
refuses a second `Prepare` **immediately**: the claim is taken before anything
is read, rather than by an exclusive lock a second caller would have had to wait
minutes to be refused by.

A preparation that fails *inside* the window publishes that failure under
`stateMu` before it unlocks `tree`, and a command re-asks whether it is still
allowed to run once it holds `tree`. Both are needed and neither is decoration:
the unlock that ends a failed window is the same unlock that wakes every command
queued behind it, and the tree those commands would wake into still holds the
instrumented sources. That ordering — `stateMu` taken while `tree` is held — is
why the order is `mu`, then `tree`, then `stateMu`, and never the other way
round.

### What a command sees at each moment

| Moment | A command |
|---|---|
| before `Prepare` | runs, against the frozen tree |
| while discovery, the probe copy or verification runs | runs, against the frozen tree — those read the same bytes it does |
| while the test binaries are compiled | runs, and may even write: the build reads the frozen copies through the overlay, never the tree |
| while the instrumentation window is open | waits for the window, then runs against the restored tree |
| already running when the window is about to open | finishes; the window waits for it |
| queued behind a window that then **failed** | refused, carrying `ErrPrepareFailed` — it never runs, because the tree it would have woken into still holds instrumented sources |
| after `Prepare` returned a session | runs, against the frozen tree |

A consumer therefore needs **no second workspace** for control work beside a
preparation. goatest opened one — a second snapshot, a second discovery pass and
a second compile of everything — to run `go vet`, `go build` and a baseline
while the first workspace was preparing; that is what this replaces.

The ideal is smaller still: instrumentation writes into the tree because that is
how validation compiles a mutated program today, and a validation that built
through the overlay instead would need no exclusive window at all. That is
engine work rather than API work, and it is named in
[ADR 0007](adr/0007-commands-overlap-preparation.md) as the thing this design
leaves on the table.

### Drift, and which stage names it

Nothing stops a command from writing into the frozen tree. A write made before a
preparation, or during the stretches of one that read the tree, fails it, and
the question a `DriftError`'s `Stage` answers is *which* check found it; a write
made while the test binaries compile, or after a preparation succeeded, is
nobody's failure and is reported by `Session.Changes` instead.

| Stage | Finds |
|---|---|
| `commands` | a change that is still in the tree when the integrity gate re-digests it at the top of the window |
| `discovery` | a change a command made *and undid* while discovery was reading, so the gate sees nothing and the catalogue is built from bytes nobody has |
| `source restoration`, `verification`, `probe instrumentation`, `probe source restoration` | a change around instrumentation, whoever made it |

The `discovery` stage is the one the overlap introduced. Discovery reads the
tree while a command may write it, so once the tree is held exclusively every
file discovery *read* — not only the ones that produced a mutant — is compared
against the frozen manifest, along with the bytes captured for restoration. A
mismatch fails the preparation and names the file, because a mutant identified
by the digest of a file that is not there would poison every answer keyed on it,
a cached one most of all.

The check is total over what discovery read, and that is deliberately a wider
set than the catalogue. A file a transient edit emptied of everything mutable
yields no candidate at all, so a check built on the catalogue alone would never
look at it; discovery records the digest of every file it opens
(`discover.Result.SourceDigests`) precisely so this one can. What no digest
covers is a file no byte of which was read — a test file, a generated one, one
an include or exclude pattern dropped — and none of those can move a mutant's
identity, because no mutant was minted from one.

The other end has no stage at all any more, and that is the point rather than an
omission. The window ends at `main_restoration` and the test binaries are
compiled after it, so there used to be a `test binaries` re-digest between the
last build and the published session: the binaries were compiled from the tree —
the overlay replaced the instrumented sources and nothing else — and
`Session.Changes`'s own baseline was scanned off the tree afterwards, so a write
during the build was compiled in *and* invisible. The digest caught every write
a command **left**, and could not catch one made and undone while the compiler
was between one file and the next.

The build no longer reads a frozen file off the disk. At the top of the window,
from a tree the integrity gate has just proved is the manifest, every file the
snapshot froze is copied into a directory the preparation owns, and the overlay
names all of them; the instrumented sources keep their own mapping and win where
the two meet. The `go` command reads Go sources, `go.mod`, `go.sum`, assembly,
the cgo inputs and `//go:embed` targets through `-overlay`, so what the compiler
sees is the frozen program whatever the tree holds by then.
`Session.Changes`'s baseline is the manifest for the same reason — it is what the
binaries were built from — so a write a command leaves during the build is
reported by `Session.Changes` and fails nothing, exactly as a write after a
successful `Prepare` is.

One thing the overlay cannot pin is a path the manifest does not name. `-overlay`
replaces the files it names and the `go` command still lists the real directory,
so a **new** file a command creates in a package directory while the binaries
compile is seen by the compiler; `Session.Changes` reports it as an addition,
and the rule at the top of this section — a command must not write the frozen
tree — is what stands between a consumer and it.

**What the copy costs.** One whole-tree copy per preparation, of exactly the
snapshot's bytes, kept for as long as the session: a prepared workspace holds
the module twice over, three times with a probe tree. The time is proportional
to the tree and paid inside the instrumentation window, so it is time a command
waits for — 679 files and 7.7 MiB of go-mutants' own repository in 50–90 ms, and
under a millisecond for each of its fixture modules. It is recorded in a trace
as the `freeze-build-inputs` stage with the file count and byte total, so a slow
preparation says how much of itself went there. It is deliberately *not* a
`PreparePhase`: that vocabulary is shared with goatest and closed, and a stage
is what the trace format has for a step inside a phase.

None of this is about **run** time. The prepared binaries still start in the
directory of the package they were built from, so a test that opens `testdata/`
reads the tree, and one that writes — a golden file it updates, a fuzz crasher
the runtime files under `testdata/fuzz/` — writes into the tree. That is the
same write this section is about, made by a target rather than by a command, and
`Session.Changes` is where it shows up.

### The session's own lock

`Session.mu` is a fourth `sync.RWMutex`, and the shape is the one `mu` used to
have: `Catalog`, `Exec`, `Probe` and `Control` hold it shared, `Changes` and
`Close` hold it exclusively. The three measuring calls are therefore safe to
call concurrently with each other and with themselves — they share the session
and nothing else, each getting its own scratch directory, its own environment
and, for a probe, its own infection log — while `Changes` and `Close` wait for
every call in flight, so neither can observe a target halfway through a write.

The locks are separate, and `Session.Changes` takes only the session's. A
`Workspace.Exec` command writing into the tree concurrently with a `Changes` is
therefore **not** waited for and can be observed part-way through its write. A
consumer that wants a settled answer sequences its own commands against the
call; the engine cannot do it, because a workspace command is not the session's
to wait for.

## Options and defaults

### `OpenOptions`

| Field | Default | Meaning |
|---|---|---|
| `GoBinary string` | `""` | the `go` executable. Empty resolves `go` through `PATH` |
| `SnapshotExclude []string` | `nil` | module-relative `/`-separated glob patterns for generated trees that must not enter the snapshot |
| `ReportDirectory string` | `""` | one further module-relative directory excluded from the snapshot, beside go-mutants' conventional report directory |
| `TempDirectory string` | `""` | parent of the snapshot and every scratch directory. Empty uses `os.TempDir()` |
| `KeepTemp bool` | `false` | preserve the snapshot, the probe tree and the scratch instead of removing them at `Close` |
| `Env []string` | `nil` | the complete environment to freeze for children. Nil captures `os.Environ()` |
| `Trace trace.Sink` | `nil` | where the workspace records what it does. Nil records into a bounded ring `Workspace.Recording()` hands back |

`Open` takes at most one `OpenOptions` value; two are an error rather than a
silent merge.

### `Command`

| Field | Default | Meaning |
|---|---|---|
| `Argv []string` | required | executable followed by arguments. No element is split, expanded, or interpreted by a shell. `go` as `Argv[0]` is replaced by the located toolchain |
| `Dir string` | `""` | working directory relative to the module root. Absolute and escaping paths are refused |
| `Env []string` | `nil` | `KEY=VALUE` overlay on the environment frozen by `Open` |
| `Timeout time.Duration` | `0` → 10 minutes | bounds the whole process tree. Negative is invalid |
| `OutputLimit int` | zero or negative → 1 MiB | cap on retained combined stdout and stderr. A positive value below 256 is raised to 256 |

### `PrepareOptions`

| Field | Default | Meaning |
|---|---|---|
| `Profile string` | `""` → `balanced` | `balanced`, `strong`, or `all` |
| `Operators []string` | `nil` | canonical family or rule names, selected *instead of* `Profile`. The result is always in canonical order |
| `Include`, `Exclude []string` | `nil` | module-relative mutation globs. Excludes win. They select candidates and never remove files from the snapshot |
| `DiscoveryPackages []string` | `nil` → `./...` | module-relative package patterns whose source is mutated |
| `Packages []string` | `nil` → `./...` | module-relative package patterns whose test binaries are built |
| `ProbeCoverPackages []string` | `nil` | package patterns included in probe coverage |
| `Selection *Selection` | `nil` | module-relative paths onto 1-based inclusive line ranges. Narrows what the caller means to *execute*; `nil` selects everything, while a non-nil `Selection` that retains no range (an empty map, or paths with no ranges) selects nothing. See [Selecting by line range](#selecting-by-line-range) |
| `Jobs int` | `0` → `min(NumCPU, 8)`, at most 32 | concurrent validation and test-binary builds |
| `BuildTimeout time.Duration` | `0` → 10 minutes | bounds each validation and test-binary build. Negative is invalid |
| `MutantTimeout time.Duration` | `0` → 10 seconds | the default outer timeout `Session.Exec` and `Session.Probe` use. Negative is invalid |
| `Verify Command` | zero → `go test ./...`, timed at `BuildTimeout` | run once against pristine files with instrumented Go builds |
| `SkipVerify bool` | `false` | omit `Verify`. Combining it with a non-zero `Verify` is an error rather than a silent preference |
| `Probe bool` | `false` | also prepare the probe tree |
| `Trace func(PrepareEvent)` | `nil` | receives serialized phase start and finish events synchronously |

### `ExecRequest`, `ProbeRequest` and `ControlRequest`

| Field | Default | Meaning |
|---|---|---|
| `Mutant string` (Exec only) | required | a full 64-character ID or an unambiguous catalogue prefix |
| `Package string` | `""` | an import path or one module-relative package directory. Empty selects **every** compiled test package |
| `Args []string` | `nil` | passed verbatim to each selected test binary, after the engine's own `-test.timeout` |
| `Env []string` | `nil` | `KEY=VALUE` overlay for this call |
| `Timeout time.Duration` | `0` → `PrepareOptions.MutantTimeout` | overrides the session default when positive. Negative is invalid |
| `OutputLimit int` | zero or negative → 1 MiB | cap on the retained combined output of each test binary the call starts, exactly as `Command.OutputLimit`. A positive value below 256 is raised to 256 |
| `RecordTestLog bool` | `false` | record which environment variables and files each binary consulted; see [Recording what a target touched](#recording-what-a-target-touched) |

Targets are named with standard test-binary flags — `-test.run=^TestX$`,
`-test.fuzz=^FuzzX$`. There is no second test DSL. `-test.timeout` is reserved
(see *Paired timeouts*), and so are `-test.fuzzcachedir` and
`-test.fuzzworker`: the session owns the first and the Go fuzz coordinator owns
the second. `-test.testlogfile` is reserved *while* `RecordTestLog` asks for a
log and passes through verbatim otherwise.

## Results and their invariants

`TestCatalogInvariants` checks the `Catalog` and `Mutant` claims below over
three prepared sessions — with a probe tree, without one, and over
`fixtures/rejectable`, whose validation rejects, so that the clauses about a
rejection are exercised by a catalogue that has some rather than held vacuously
by one that has none. `TestProbeResultInvariants` and
`TestMutantResultInvariants` check the two result types over the probe session.

The rest of the section — the `Command`, `Change` and `SweepResult` claims, and
`OutputTail`'s length — is established by the integration tests beside them or
is the code's stated intent, not by those three.

### `Catalog`

`Session.Catalog()` returns a deep copy, so a caller may keep and edit it.

- `Digest`, `WorkspaceDigest`, `ModulePath` and `Toolchain` are non-empty, and
  `Toolchain` is exactly `Workspace.ToolchainVersion()`.
- `PreparedDigest` is 64 lowercase hex characters.
- `TestPackages` is non-empty, every element is non-empty, and no element
  repeats.
- `Rejections` IDs are distinct and each names a mutant in `Mutants`.
- A mutant is `Accepted` **iff** it carries no rejection: rejected implies not
  accepted, and not accepted implies rejected. A mutant never disappears into a
  rejection nothing can look up.
- `Selection` is nil, and every mutant is `Selected`, for a preparation that
  asked for no narrowing. When one was asked for, it is the **normalised** copy
  the engine applied — cleaned paths, sorted and merged ranges — and a deep one,
  so editing it cannot change what the session says it selected.

### `Mutant`

- `ID` is 64 lowercase hex characters and unique in the catalogue.
- `DisplayID` is a non-empty prefix of `ID` and is itself unique — it is what a
  user types and what `Session.Exec` resolves.
- `Mutants[i].Index == uint32(i)`. The probe log's indices address this slice
  directly, with no translation.
- `Path` is module-relative, `/`-separated, and never escapes the module.
  `Package` is non-empty.
- `Line >= 1` and `Column >= 1` — 1-based line and 1-based byte column, the
  coordinates `go test -coverprofile` reports blocks in.
- `EndLine >= Line`, and `EndLine == Line + strings.Count(Original, "\n")` — the
  1-based line the edit ends on. Select by line range with `[Line, EndLine]`;
  see [Selecting by line range](#selecting-by-line-range).
- `StartByte <= EndByte`. `Original != Replacement`: replacing bytes with
  themselves is not a mutation.
- `Rule` is non-empty and `RuleVersion >= 1`. `SourceDigest` is 64 lowercase hex
  characters.
- `Probed` implies `Accepted`. A mutant validation rejected is never executed,
  so a probe status on it would describe a run that cannot happen.
- `Selected` is true for every mutant when `PrepareOptions.Selection` is nil,
  and otherwise exactly when `[Line, EndLine]` meets a range given for `Path`.
  It is advisory: `Session.Exec` runs an unselected mutant like any other, and
  `PreparedDigest` does not hash it.
- `Branch` is nil when go-mutants proved nothing, which is not the same
  statement as "no branch".

### `MutantResult`

- `Outcome` is one of `not_run`, `killed`, `survived`, `timed_out`,
  `inconclusive`, `errored`.
- `KilledBy` is the import path of the package that decided the result, and it
  is non-empty **exactly** for `killed` and `timed_out`.
- `ID` is the full ID and `DisplayID` a prefix of it, whether the request named
  the mutant by prefix or in full.
- `Duration` is non-negative wall-clock time across the binaries that ran.
- `OutputTail` is the last 50 lines of the deciding binary's combined output,
  with carriage returns stripped. It is unchanged: a consumer that renders it
  needs no change. Being a *tail*, it usually loses the truncation notice,
  which sits at the top of a capped capture.
- `Output` is the whole of what the budget kept of that same binary's combined
  output — `OutputTail` summarises exactly these bytes — bounded by the
  effective `ExecRequest.OutputLimit`.
- `Truncated` reports that `Output` lost bytes to the limit, and `TotalBytes`
  is everything the deciding binary wrote whether kept or not.
- **`Output` is empty for a survivor**, as `OutputTail` has always been, and
  `Truncated` and `TotalBytes` are then false and zero. A survivor's output is
  thousands of lines of nothing having gone wrong, multiplied by every mutant
  in a run, and holding it is how a mutation run runs a machine out of memory.
  The same goes for an attempt a cancellation cut off: it decided nothing, and
  what it had printed travels on the error instead.
- `Artifacts` are bounded copies of the standard `go test fuzz v1` inputs a
  fuzz target wrote, captured before its private cache is removed.
- `Binaries` are the test binaries the execution started, in launch order and by
  import path, stopping where the execution stopped; `KilledBy` is one of them.
  `TraceSeq` is the `mutant-exec` event that explains the result — see
  [Tracing a session](#tracing-a-session).
- `TestLogs` is one record per element of `Binaries`, in the same order, and
  `nil` unless `ExecRequest.RecordTestLog` asked for one — see
  [Recording what a target touched](#recording-what-a-target-touched).

### `ProbeResult`

- `Infected` is non-nil **exactly** when `Outcome` is `measured`, the empty set
  included. Every other outcome, and every error, carries `nil`, so a caller
  that forgets to read the outcome ranges over nothing rather than over a set
  that means the opposite of what it looks like.
- When present it is strictly ascending, distinct, in range of
  `Catalog.Mutants`, and names only mutants that are both `Probed` and
  `Accepted`. The probe tree is instrumented from the whole catalogue and its
  runtime knows nothing of the mutant tree's verdict, so its log can name a site
  whose *mutation* does not compile; `Session.Probe` drops those indices, since
  an infection fact about a mutant nothing will execute licenses nothing and
  would contradict its own `Probed`.
- The engine proves all of that before it returns the set, in **two validation
  stages with the filtering between them**:
  1. **The raw log**, as the probe runtime wrote it, must be strictly ascending
     and inside the catalogue. This runs *first*, before anything is dropped,
     because the filter has to tolerate an out-of-range index in order not to
     panic on one — and an index past the end of the catalogue means the runtime
     wrote about a catalogue that is not this one, which must not be mistaken
     for an ordinary rejection.
  2. **The indices of mutants the mutant tree rejected are dropped**, not
     refused. A well-formed, in-range index naming a rejected mutant therefore
     never produces an error; it is simply absent from the result.
  3. **Every index that survives the filter** must name a mutant that is
     `Probed`. An *accepted* mutant that is not probed is the catalogue and the
     probe tree disagreeing, and there is no excuse for it.

  A failure at stage 1 or 3 is `ErrProbeInconsistent` naming the index, and no
  set is returned at all. A caller never has to defend against a malformed one.
- `ExitCode` is 0 for a measured pass and `Duration` is non-negative.
- `Output` is the bounded combined output of the binary that decided the pass —
  the failing one for `test-failed`, the last one for `measured` — capped at
  the effective `ProbeRequest.OutputLimit`. It is there for every outcome,
  including the ones that carry no `Infected`, because a pass that proves
  nothing is exactly the one whose output has to be readable. `Truncated` and
  `TotalBytes` say what the cap dropped, exactly as they do on `MutantResult`.
- `Binaries` are the probe tree's test binaries the pass started, named exactly
  as `MutantResult.Binaries` are, and `TraceSeq` is the `probe-exec` event that
  explains the pass — see [Tracing a session](#tracing-a-session).
- `TestLogs` is `MutantResult.TestLogs` exactly, over the probe tree's binaries,
  and it is kept beside the error of a pass that reached an execution and then
  failed — what the binaries touched is an account of what ran and never a
  measurement, so it survives where `Infected` cannot.

The consumer's rule has two clauses and dropping either is unsound:

> Skip executing test `t` against mutant `m` only when `m.Probed` is true **and**
> a `measured` probe of `t` does not name `m.Index`. An unprobed mutant is
> absent from every measurement there will ever be, so treat it as infected by
> every test.

### `ControlResult`

- `ExitCode` is the status of the binary this run stopped at — the deciding
  binary's; when nothing decided, the last binary that ran — and `TimedOut`
  reports one the supervisor had to kill. A killed tree has **no** status, so
  `ExitCode` is then **negative**, exactly as `CommandResult.ExitCode` is for a
  workspace command with the same field set: a zero would be a status the child
  never returned, and a caller that forgot `TimedOut` would read it as green.
- `Package` is the import path of that deciding binary, and it is empty when
  every binary passed: the rule `MutantResult.KilledBy` follows.
- `Output` is **one** binary's bounded combined output — the deciding binary's;
  when nothing decided, the last binary that ran — with `Truncated` and
  `TotalBytes` beside it as everywhere else. A consumer that wants one
  package's output asks for that package. Unlike `MutantResult.Output` it is
  present **even when everything passed**: a survivor's output is held once per
  mutant in a run and is what the cap exists to bound, while a control runs at
  most once per execution and its output is the very thing a consumer shows
  beside a mutant's failure.
- `Duration` **sums** over every binary the run started; `TotalBytes` does
  **not** — it is the total of the single binary `Output` came from. That is
  deliberate and it is the one place the two disagree. `Duration` is zero when
  the call returns an error.
- `Binaries` are the binaries the run started, in launch order and by import
  path, stopping where it stopped, and `ExecSeqs` are the `exec` events those
  starts were recorded at, one per binary and in the same order. `TraceSeq` is
  the `note` event that summarises the run — see
  [Tracing a session](#tracing-a-session).
- `TestLogs` is `MutantResult.TestLogs` exactly, and it is kept beside the error
  of a run that reached an execution and then failed, as `Binaries` and
  `ExecSeqs` are.
- There is no `Outcome`. A control is not a mutant and has nothing to survive or
  be killed by; what a status means beside an execution is the caller's
  judgement.

### `CommandResult`, `Change`, `SweepResult`

`CommandResult` describes a command that *started*: a non-zero `ExitCode` and a
`TimedOut` are results, not infrastructure errors. An error alongside it is
either a process failure — the command could not be started or supervised — or
the caller's own cancellation, and in both cases the result still carries what
the command produced before it stopped. `Output` is bounded by the effective
`Command.OutputLimit`, `Truncated` says whether the cap dropped anything, and
`TotalBytes` is what the command wrote in total — the same number either way,
so a caller reporting a size never has to ask which case it is in.
`*VerificationError` carries `Truncated` and `TotalBytes` beside its own
`Output` for the same reason. `TraceSeq` is the `exec` event the command was
recorded at, which carries the argument vector the child actually received —
see [Tracing a session](#tracing-a-session).

A **cancellation and a deadline are not the same thing**, and go-mutants tells
them apart by the context's own cause rather than by which command happened to
notice the expiry. `context.Canceled` in an error's chain is somebody stopping
the run — a Ctrl-C, the dashboard's quit key, a caller calling `cancel` — and
nothing went wrong: such a run reports itself as interrupted, keeps no temporary
directory that `--keep-temp=on-failure` would otherwise have kept, and writes no
diagnostics bundle. (`KeepTempAlways` still keeps: it is unconditional by name,
and a caller who cancelled a run half way through is usually a caller who wants
to look at the tree.) `context.DeadlineExceeded` is the run failing to finish
inside the time it was given, which is a failure worth diagnosing: it reports as
failed, it keeps what it was asked to keep, and the CLI writes the bundle every
other failure gets.

The division is worth stating, because only two of those three are the engine's.
`engine.Interrupted` classifies the failure and the engine settles the
directories; the bundle is `go-mutants run`'s, written from the outcome and the
error after the run returns, and an embedder that wants one writes its own from
the same two values. All three answer to one predicate, so the status, the keep
and the bundle cannot come to disagree about one run.

`Session.Changes` returns `Change{Kind, Path, BeforeSHA256, AfterSHA256}` in
strict path order, `Kind` one of `added`, `removed`, `modified`.

`Workspace.Swept()` returns what `Open` collected before it copied anything:
`Removed` paths, `RemovedBytes`, `Live` (directories a running go-mutants still
holds a lock on), `Kept` (directories a `KeepTemp` run preserved on purpose),
and `Err` — carried in the value rather than returned, because failing to
collect somebody else's leftovers is not a reason to refuse to run.

## `Session.Control`

```go
func (s *Session) Control(
	ctx context.Context, request ControlRequest,
) (ControlResult, error)
```

**What it is.** The same prepared test binaries, run with no mutant activated —
which runs the program the user wrote. Instrumentation leaves every original
branch in place and selects between them on one environment variable, `Exec` is
the only thing that ever sets it, and every `GO_MUTANTS_*` variable is stripped
out of the frozen environment before a child's is composed. So a control is an
execution minus one entry, and nothing else.

**Why it exists.** A mutant's suite going red is evidence about the mutant only
if the same suite is green without it, and a prepared session could not say so.
goatest reaches that answer two ways today, and this replaces both:

- it opens a **second workspace** over the same root and runs `Workspace.Exec`
  there — a second snapshot, a second discovery pass, a second compile of every
  test binary — to run tests the session it already holds had already compiled;
- and where a probe tree exists it reads `Session.Probe`'s `test-failed` outcome
  as "the original program is red", which spends a probe pass, requires
  `PrepareOptions.Probe`, measures the *probe* tree's binaries rather than the
  mutant tree's, and answers in a vocabulary built for infection facts instead
  of with the suite's own exit status and output.

**The guarantee: the same binaries, the same launch shape, no activation.** A
control and an execution of the same package with the same arguments and the
same budget are settled by one function inside the session, so they cannot come
to disagree about the argument vector, the working directory, the paired
timeouts, the instrumentation overlay, the reserved flags, the private scratch
directory or the fuzz isolation. In a recording the two `exec` events differ in
their `kind`, their `subject`, and one name in `env_names`:
`GO_MUTANTS_ACTIVE`. That is asserted by a test rather than promised by this
paragraph.

**Stopping.** The run stops at the first binary that does not exit zero, and
that is the answer rather than a saving: a control asks whether the original
program passes these tests, and the first binary that says no has answered.

**A failing control is a finding about the repository.** It comes back as a
*result* — exit status, output, deciding package — and not as an error, because
what it means is that the user's suite is red, flaky, or depends on something
the frozen snapshot does not carry, and a consumer has to be able to report that
as the user's own failure rather than as a broken engine.

**Timeouts** are `Exec`'s, exactly: the supervisor kills the whole process tree
at `Timeout` — the request's when positive, otherwise
`PrepareOptions.MutantTimeout` — and the binary is additionally given
`-test.timeout` at twice that. See [Paired timeouts](#paired-timeouts).
`-test.timeout` in `Args` is refused for the same reason it is refused there.

**`Package: ""`** runs the target in every compiled test package, in order,
exactly as an `ExecRequest` with no package does. It is the control a caller
wants beside an execution it did not narrow either.

**Fuzz targets** run in a private copy of the snapshot, as they do under `Exec`,
so a corpus entry cannot drift the tree every later mutant is measured against.
The corpus is **not** captured: `MutantResult.Artifacts` exists to preserve the
input that killed a mutant, and a control kills nothing. The copy goes with the
rest of the call's scratch unless `KeepTemp` asked for it, in which case it is
preserved and recorded as `kept-exec-scratch` — the same kind an execution's is,
because it is the same kind of directory.

**Errors** are `Exec`'s with `Call` reading `control`: `*PackageNotPreparedError`,
`*ReservedError`, `ErrSessionClosed`, and `*ExecutionError` when the measurement
itself could not be made. A **cancellation is never an exit status**: a child
go-mutants killed comes back with none, and reading that as an ordinary non-zero
one would report the original program as *failing* whenever somebody stopped the
run — so a control whose child was cut off returns an `*ExecutionError` carrying
`Binaries`, `ExecSeqs` and `TraceSeq` and no verdict at all. A context cancelled
*after* the run finished is the other case and is reported as `Exec` reports it:
the populated result comes back beside a plain wrapped context error, because
the run did establish what it says it did.

**In a recording** the call is its per-binary `exec` events, of kind
`control-run`, plus one `note` of kind `control` summarising them.
`gomutants-trace-v1` closes its event `type` enum and holds no payload for a
control, so the note is the one line a single call can be named by, and
`ControlResult.TraceSeq` points at it. Because a `note` has no `exec_seqs` of
its own, the way down to those executions is `ControlResult.ExecSeqs` — a field,
so that nobody has to parse the note's `detail`, which is a sentence written for
a person.

## Recording what a target touched

```go
request.RecordTestLog = true          // Exec, Probe and Control alike
result.TestLogs                       // one per binary started, in launch order
```

A Go test binary can be told to write down what it consults, and the go command
uses exactly that to decide whether a cached test result is still valid.
`RecordTestLog` hands the same file over: which environment variables a target
read, which files it opened or stat-ed, and where it changed directory to.

**The engine resolves nothing and interprets nothing.** A `Name` is the bytes
the testing package wrote — relative paths stay relative, a name that no longer
exists on disk is reported as it was written, and what any of it means is the
consumer's question. `TestLog.Dir` is the directory that binary ran in, which is
what a relative name is relative to until a `chdir` entry says otherwise.

### The format

The flag is `-test.testlogfile=<path>`, documented in the testing package as
"for use only by cmd/go", and the file it writes is:

```text
# test log
getenv EXPECT_CLEAN
open /tmp/x
stat /tmp/y
chdir /tmp
```

The header is written when the log is opened, before any test code runs, and
one line follows per action: an operation, a space, and the name. `TestLogOp` is
`getenv`, `open`, `stat` or `chdir` — the four package `os` reports — and the
vocabulary is **open**: an operation a later Go release writes and this build
has never heard of is carried through verbatim, because a dropped operation
reads as an input nothing consulted.

| Field | Meaning |
|---|---|
| `Package string` | the import path of the binary, one of the result's `Binaries` at the same position |
| `Dir string` | the directory that binary ran in, inside the session's snapshot or the private copy a fuzz target gets |
| `Entries []TestLogEntry` | the actions, in order, verbatim. Empty is a target that consulted nothing, which is a measurement |
| `Complete bool` | the log ends at a line boundary **and** the binary exited on its own |
| `Err string` | why there is no log, in one line, and empty when there is one |

**Read `Complete` before acting on `Entries`,** and note that it is two claims
and not one. The testing package writes the log through a 4096-byte buffer and
flushes it whenever that fills, as well as from the deferred call at the end of
`M.Run` — so a *chatty* target the supervisor killed leaves a log that ends in a
newline and is nonetheless a fraction of what it touched, with nothing in the
bytes saying so. A binary the engine timed out or cancelled therefore reports
`false` whatever the last byte is; a quiet one leaves the empty file it created
and carries `Err` instead. Reading a partial log as the whole truth is how a
consumer keys a cache on half a target's inputs and then believes it.

The go command additionally trusts a log only from a test that **exited 0**.
That rule is deliberately not applied here — an execution's whole subject is
often a binary that did not — so a caller that wants it applies it to the
result's own exit status.

Two more shapes leave a log short and neither shows in the bytes. A `TestMain`
calling `m.Run` more than once flushes only the **first** run's entries, the
testing package guarding its own teardown with a `sync.Once`; and a test calling
`os.Exit` skips that teardown altogether. go-mutants does not pass the go
command's companion `-test.paniconexit0`, which would turn the second into a
panic, because it would change what the binary does and this API measures the
program the user wrote.

`Err` is a string beside the measurement rather than an error, because a run
that could not record what a target touched is not a run that failed: the
outcome, the output and the timings are all still there.

### The one failure, and why it is not a kill

A binary that does not define the flag it was handed is refused by the standard
flag package: it prints `flag provided but not defined: -test.testlogfile` and
exits **2**. **No standard Go test binary does this** — the flag is the testing
package's own, and the session runs binaries it compiled itself — so it is not a
condition to plan for. The branch exists because exit 2 is a *non-zero status*,
and a non-zero status is how the engine recognises a detection: without it a
repository whose binaries somehow refused the flag would report every mutant as
killed by a binary that never started a test.

It is therefore reported as `ErrTestLogUnsupported`, wrapped in an
`*ExecutionError` whose `Call` says which of the three runs asked, with
`DiagnosticCode` `GOM7521`. The outcome is `errored` and never `killed`,
`test-failed` or a red control. It fires only for a binary the engine really did
hand the flag to: a target that exits 2 having printed that same line for its own
reasons — a fuzz target, say, which is given no flag at all — is a kill.

### Fuzz targets record nothing

A `-test.fuzz` target is given no flag at all, and the record says so in `Err`.

The reason is in the Go source rather than in a policy. `internal/fuzz` starts
every worker with the coordinator's own arguments —
`append([]string{"-test.fuzzworker"}, os.Args[1:]...)` — so a worker inherits
`-test.testlogfile`; each worker's first call to `M.Run` reaches the
`os.Create` in testing's `m.before()`, truncating the file the coordinator is
writing, and several processes then append to one path at offsets of their own.
The go command does not combine the two either: `-test.fuzz` is not a cacheable
test argument, so it disables the test cache and the flag is never passed.

### Where the log lives, and the join with a trace

Each binary writes its own file in a `testlogs/` directory inside the call's
private scratch, one file per binary, and the lot goes when that scratch does —
kept only under `OpenOptions.KeepTemp`, like everything else in there. A shared
path would be several processes appending to one log, and a log two binaries
wrote cannot be attributed to either. The subdirectory is not tidiness: the
scratch *is* the target's `TMPDIR`, so a test that lists its own temporary
directory finds one entry go-mutants put there rather than one per binary, and
what that entry is is written on it.

In a recording the flag is simply part of the `exec` event's `argv`, ahead of
the caller's own arguments where the go command puts it. **Nothing else
changes**: no new event, no new field, no second flag — `-test.paniconexit0`,
which the go command passes beside it, is deliberately not — and `env_names` is
identical to the same target's run with recording off, because the log is named
on the command line and never in the environment.

## Errors

Every failure this API returns is one of four things, and a consumer does
something different about each: **the user's suite is red**, **the repository
moved under the engine**, **the caller composed a request the session cannot
serve**, or **go-mutants itself broke**. The sentinels and types below are how
that question is answered without matching text — no message changed when they
were introduced, and none of them is a message you may parse.

### Sentinels

| Sentinel | Returned by | Means |
|---|---|---|
| `ErrWorkspaceClosed` | `Workspace.Exec`, `Workspace.Prepare` | the workspace is closed |
| `ErrWorkspacePrepared` | a second `Workspace.Prepare` | a workspace may be prepared once, a failed preparation included |
| `ErrPrepareFailed` | `Workspace.Exec` | a preparation began and failed, so the tree may hold instrumented sources; open another workspace |
| `ErrSessionClosed` | `Session.Exec`, `Session.Probe`, `Session.Control`, `Session.Changes` | the session, or the workspace that owned it, is closed |
| `ErrInvalidMutantID` | `Session.Exec` | `ExecRequest.Mutant` is not an identity: too short, too long, or not lowercase hex |
| `ErrMutantNotFound` | `Session.Exec` | a well-formed prefix no catalogued mutant carries |
| `ErrAmbiguousMutant` | `Session.Exec` | a prefix more than one mutant carries; `Matches` names them |
| `ErrMutantRejected` | `Session.Exec` | validation proved the mutant does not compile; there is no binary to run it in |
| `ErrProbeNotPrepared` | `Session.Probe` | the session was prepared without `PrepareOptions.Probe` |
| `ErrProbeInconsistent` | `Session.Probe` | the probe log named a mutant the catalogue cannot account for — an **engine bug**, never a caller's doing |
| `ErrTestLogUnsupported` | `Session.Exec`, `Session.Probe`, `Session.Control` | a target refused `-test.testlogfile`, so `RecordTestLog` cannot be served. Never a kill |
| `ErrInvalidSelection` | `Workspace.Prepare` | `PrepareOptions.Selection` holds a path or a range the engine will not narrow by; the sentence names the entry |

Match them with `errors.Is`. They survive wrapping, and the sentences they
appear in are the ones the engine has always printed. `ErrProbeNotPrepared`
predates the rest and spells its own text with the prefix — `gomutants: the
session was prepared without a probe tree` — while the newer sentinels carry
only the condition and are wrapped into the engine's sentence where the failure
happens. Both spellings are frozen, and neither is a string to compare against.

### Typed errors

Reach all of these with `errors.As`.

- **`*VerificationError{Command, ExitCode, TimedOut, Duration, Output,
  Truncated, TotalBytes}`** —
  `PrepareOptions.Verify` failed. This is a **finding about the repository**,
  not a broken engine: instrumentation preserves behaviour, so a suite that is
  red here is red on the user's own program, flaky, or depending on something
  the frozen snapshot does not carry. `Output` is what to show the user.
- **`*DriftError{Stage, Changes}`** — the frozen tree stopped matching its
  manifest. `Stage` names the check that found it: `commands` (the integrity
  gate at the top of the instrumentation window), `discovery` (what discovery
  read, compared against the manifest once the tree is held exclusively),
  `source restoration`, `verification`, `probe instrumentation` or
  `probe source restoration` — see
  [Drift, and which stage names it](#drift-and-which-stage-names-it).
  `Changes` carries the paths and both digests, so a consumer can say *which*
  file moved. The remedy belongs to the caller: something wrote into the
  workspace.
- **`*BuildError{Phase, Package, Argv, ExitCode, TimedOut, Output, Code}`** — a
  preparation phase could not do its work: a package that would not load, a
  snapshot that would not compile, a test binary that would not build. This is
  **infrastructure**. `Code` is the stable diagnostic code and `Argv` the
  command to reproduce it. `Error()` is the cause's own message, so
  `gomutants: prepare test binaries: GOM7505: …` reads exactly as before.

  How much of it is filled in depends on which phase failed. A build error from
  `main_validation` or `probe_validation` carries `Code` and `Output` and
  nothing else — `ExitCode` is zero, `TimedOut` is false and `Argv` is nil —
  because those failures come back through the seam the bisection search is
  faked behind, which answers whether a subset compiled and not with what
  status. `binary_build` and `probe_coverage_build` carry all of it, `Package`
  included. `discovery` carries the code alone: a package that will not load is
  decided in this process, with no child command to name.
- **`*ExecutionError{Call, Package, Code, Output}`** — the measurement itself
  failed inside `Session.Exec`, `Session.Probe` or `Session.Control`: a test
  binary that would not start or could not be supervised, a generated runtime
  that refused the activation it was handed, an infection log that is there and
  cannot be read. `Call` is `exec`, `probe` or `control`.
  Never a statement about the tests — a killed, survived or timed-out mutant is
  a result and comes back as one.

  `Package` is the import path of the binary the failure was about, whenever
  the failure named one. It falls back to the request's `Package` for the
  failures that are about the pass rather than about one binary — an unreadable
  infection log, say. The request's field is a *selector*: it may be a
  module-relative directory, and it is empty for the ordinary request that
  measures every prepared binary, so a caller grouping failures by package
  wants the failing binary's own path and gets it whenever the execution phase
  knew it.

  It covers what the execution phase reports, not everything the two calls can
  fail at. A session scratch directory that could not be created, an
  environment or instrumentation overlay that could not be composed, a fuzz
  workspace that could not be copied, and the artifacts captured after a fuzz
  target are plain errors today: they happen before or after the measurement
  and carry no diagnostic code.
- **`*MutantSelectionError{Prefix, Reason, Matches, Rejection}`** — the request
  named a mutant the session will not run. `Reason` is one of the four
  selection sentinels and `errors.Is` matches it.
- **`*PackageNotPreparedError{Call, Package}`** — the request named a package
  this session built no test binary for. In a long-lived consumer this is the
  ordinary one: a package list that has moved on, or a typo.
- **`*ReservedError{Call, Flag, Variable, Owner}`** — the request supplied
  something the engine owns. Exactly one of `Flag` and `Variable` is set.

### Which failures are the user's

A consumer scoring somebody's repository should split them like this:

| Failure | Report as |
|---|---|
| `*VerificationError` | the user's test suite: quote `Output` |
| `*DriftError` | the repository changed under the run; name `Changes` |
| `*MutantSelectionError`, `*PackageNotPreparedError`, `*ReservedError` | the caller's request; fix and retry |
| `*BuildError`, `*ExecutionError` | infrastructure; quote `Code` and file a bug |
| `ErrProbeInconsistent` | the engine broke its own contract; file a bug quoting the index, and treat the pass as having no facts |
| a lifecycle sentinel | a programming error in the consumer |

`ErrProbeInconsistent` is the only row nothing outside go-mutants can cause. The
indices are the engine's own, written against the catalogue the engine prepared,
so a set it cannot account for is a bug in go-mutants and never a fact about the
repository, the request or the machine.

### `DiagnosticCode`

```go
func DiagnosticCode(err error) string   // "GOM7505", or "" when it carries none
```

The codes are the one part of a failure promised to stay put, and they live in
packages a consumer cannot import. `DiagnosticCode` reaches through every
wrapper to the innermost error that carries one, so a report can quote a code
instead of four characters lifted out of a sentence.

## Guarantees

### A private temporary directory per call

`TMP`, `TEMP` and `TMPDIR` are set on every platform, for every child, to a
directory created for that call: one per `Workspace.Exec`, one per
`Session.Exec`, one per `Session.Probe`, one per `Session.Control`. Two
concurrent calls cannot observe each other through a temporary file. The
scratch lives *beside* the snapshot, never inside it: every byte under the
snapshot root has to be a byte that came from the user's tree, or "a test wrote
into the workspace" stops being detectable.

Every one of those directories is kept when `OpenOptions.KeepTemp` asked for it
and removed otherwise — `Workspace.Exec`'s, `Session.Exec`'s, `Session.Probe`'s
and `Session.Control`'s alike. The per-execution scratch is half of the answer
`KeepTemp` exists to give: it is where the target's `TMPDIR` pointed, where a
fuzz cache lived, and where anything the test wrote went, so a keep that left
the snapshot and removed that was answering half the question.

Keeping a probe pass's directory is safe for the reason removing it used to be
necessary: every pass gets a directory of its own, so the infection log a kept
one leaves behind is nobody else's to append to. What must never happen is two
passes sharing one log, and two passes never share a directory.

Only the three **durable** directories — the snapshot, the probe tree and the
workspace scratch — carry a lock and a marker, and only they need one. A sweep
looks at the direct children of `TempDirectory` and collects one only if it
wears a go-mutants name prefix; the per-call scratch is *nested* inside the
workspace scratch, so it is never a candidate and survives for exactly as long
as the marked parent above it does. Nothing of go-mutants' is written into it,
deliberately: a lock and a marker in the child's own `TMPDIR` would be two files
in the very tree the keep exists to let somebody read. A durable keep the marker
could not record is not a keep — the directory is removed instead, it is not
among the preserved ones, and `Close` reports why.

`Workspace.Preserved()` names them all after `Close`, in path order, and the
recording carries one `artifact` event per directory: `kept-exec-scratch` beside
the execution it belonged to, and `kept-snapshot`, `kept-scratch` and
`kept-probe-tree` at `Close`.

**Nothing is kept by a process that dies.** `KeepTemp` is decided at `Close`, so
a workspace killed before it closes leaves directories that are still locked and
unmarked, and the next run's sweep collects them once the locks are free. That
is intended: the escape hatch is for reading what a run produced, and a run that
never finished has an owner who is still there to ask.

### Reserved variables and flags

`GO_MUTANTS_*` — activation, the probe log path, everything the engine sets for
itself — is stripped from the frozen environment and refused in every `Env`
overlay with `%s is reserved by go-mutants`, as a `*ReservedError` naming the
`Variable`. So is each of `TMP`, `TEMP` and `TMPDIR`. An entry that is not
`KEY=VALUE` is refused with `%q is not KEY=VALUE`. A `GO_MUTANTS_ACTIVE`
exported in a developer's shell therefore cannot turn a mutant on inside a
baseline, a build, or another mutant's run — which is the failure that would
look exactly like a detection.

A session target's `Args` are refused the same way for `-test.fuzzcachedir`
(the session owns the fuzz cache), `-test.fuzzworker` (the Go fuzz coordinator
owns it) and `-test.timeout` (see below) — by `Exec`, `Probe` and `Control`
alike, since a caller composing one request for a mutant run and its control has
to be able to hand the same arguments to both.

A fourth is refused *conditionally*, and it is the only one that is.
`-test.testlogfile` belongs to the request exactly while `RecordTestLog` asks
for a log, because two of them are not two logs: the standard flag package keeps
the last value it sees, so one of the two would silently win and the other would
report on a file nobody wrote. A request that did not ask is composing nothing,
so the flag passes through verbatim.

The engine adds its own `GOFLAGS` entries for the instrumented builds:
`-overlay=<manifest>`, `-vet=off`, and `-count=1`. Instrumented sources live
only in `<scratch>/session-*/main-overlay`; the snapshot itself is byte-identical
to the frozen manifest after a successful `Prepare`.

### Paired timeouts

Two layers, deliberately unequal:

- The **supervisor** kills the whole process tree at `Timeout` — the request's
  when positive, otherwise `PrepareOptions.MutantTimeout`. It is the authority:
  it is the only mechanism that can end a target that hangs outside the testing
  framework's reach.
- The test binary is additionally given `-test.timeout=2×Timeout`
  (`execute.InProcessTimeoutFactor`). It is insurance underneath the supervisor
  and is set deliberately *later* so the two never race: a binary that panicked
  on its own deadline would exit non-zero, and a non-zero exit is a kill.

That is why `-test.timeout` in `Args` is refused rather than merged. Passing it
would let a target switch off the in-process half while the API still claimed
the supplied budget. The refusal is a `*ReservedError` from `Exec`, `Probe` and
`Control` themselves — before a scratch directory is made or a binary is
started — and reads `gomutants: session exec: -test.timeout is reserved by the
session's process supervisor`, with the call naming itself.

### The outcome vocabulary is not the report's

`Outcome` is **snake_case**: `not_run`, `killed`, `survived`, `timed_out`,
`inconclusive`, `errored`. The published `run-report-v1` schema spells the same
two multi-word outcomes in **kebab-case**: `not-run` and `timed-out`. The
difference is deliberate and both spellings are frozen; a consumer moving a
value between the live API and a published report translates rather than
assumes. `ProbeOutcome` is a third vocabulary again — `measured`,
`test-failed`, `timed-out`, `unavailable` — and is not an `Outcome`.

### Output is capped, and truncation keeps the tail

A capture never grows without bound. When a child produced more than
`OutputLimit` bytes the result holds a notice line beginning
`OutputTruncatedPrefix` — `[go-mutants] output truncated` — followed by as much
of the *tail* as the remaining budget allows, and `len(Output) <= OutputLimit`
still holds, notice included.

**Read `Truncated`, not the notice.** Every result carrying output carries the
flag beside it: `CommandResult`, `MutantResult`, `ProbeResult` and
`*VerificationError`. It is true exactly when `TotalBytes` exceeds the effective
limit, and it is the contract. The prefix stays exported for renderers, which
style the notice differently from the process's own output, and for the
consumers that were matching the text before the flag existed — but matching a
sentence written for a person makes a diagnostic into a wire format nobody can
reword, and the flag says the same thing without knowing how the notice is
spelled.

Every call chooses its own budget: `Command.OutputLimit`,
`ExecRequest.OutputLimit`, `ProbeRequest.OutputLimit`. All three default to
1 MiB, and a positive value below 256 is raised to 256 so the notice still fits
inside the budget.

Keeping the tail is right for a test failure and wrong for a document: a
truncated JSON stream is not JSON. `Workspace.Module` sizes its own budget for
the document and refuses a truncated one rather than decoding it; a consumer
running its own `go list -json` through `Workspace.Exec` owns that choice — see
[Listing the module](#listing-the-module).

### Temporary directories: ownership, sweep and keep

Every temporary directory go-mutants creates carries a lock and a marker file
inside it. `Open` sweeps its `TempDirectory` before copying anything, and
removes only a directory that is under that parent, wears one of go-mutants'
own name prefixes (`go-mutants-snap-`, `go-mutants-api-`), has a free lock, and
is not marked kept. A directory belonging to a live run, a directory somebody
kept on purpose, and every unrelated name are left exactly as they were found;
an unowned directory from an older release is collected only after 24 hours
untouched.

`OpenOptions.KeepTemp` is the escape hatch for the one question a removed
directory cannot answer — what did the tree this mutant ran in actually look
like. A kept directory is marked kept, so the next run's sweep leaves it alone
rather than collecting it as an orphan, and `Workspace.Preserved()` names them
after `Close`. A kept snapshot is a full copy of the module and nothing will
ever remove it. That is the price of the answer, and it is charged only when
asked.

## Tracing a session

A `Workspace` records everything it does, and `OpenOptions.Trace` decides where.
The events are `trace.Event` values in the `gomutants-trace-v1` contract that
`docs/trace-v1.md` describes and `schema/trace-v1.schema.json` states.

**A sink, or the ring.** Handing `Trace` a `trace.Sink` sends every event there
and nowhere else; `Workspace.Recording()` then returns `nil`, because a second,
shorter copy of what the sink already holds would only be a second document to
reconcile. Handing it nothing records into a bounded ring —
`trace.DefaultRingCapacity` events, wrapped in `trace.Digested` so that captured
output cannot grow it — which `Recording()` hands back, before or after `Close`.
That default is deliberate: the failure nobody expected is exactly the failure
nobody thought to ask for a recording of. `Recording()` is safe to call at any
point in a workspace's life, executions in flight included; before `Close` it is
the account so far, with no `run-end` on the end of it.

A supplied sink is **not** wrapped in `trace.Digested`, because a sink writing to
disk is meant to preserve the captured output an `exec` carries and the tail a
`mutant-exec` carries. A sink that keeps events in memory should wrap itself —
`trace.Digested(mine)` — or it grows with the run rather than with its own
capacity.

A **failed `Open`** records only into a sink you supplied. It ends the recording
with a `run-end` whose verdict is `failed`, so a sink sees a complete stream; but
no `Workspace` is returned, so the default ring dies with the workspace that
never existed and there is nothing to read it from.

```go
ws, err := gomutants.Open(ctx, root, gomutants.OpenOptions{Trace: mySink})
// …or, with no sink at all:
defer func() { publish(ws.Recording()) }()
```

The recording opens with a `run-start` of kind `workspace` and closes with the
`run-end` that `Close` writes — `closed`, or `failed` with the error `Close`
reported. The sink belongs to the caller and is never closed by the workspace.

**A trace is never evidence.** No option here changes a catalogue digest, a
mutant identity, a result, or an error. A sink that returns an error, or panics,
costs the event and never the run: the recorder counts the loss and the
`run-end` reports it in `events_dropped`.

**The join.** Every result names the event that explains it:

| Field | Points at |
|---|---|
| `CommandResult.TraceSeq` | the `exec` event of that `Workspace.Exec`, kind `workspace-exec` |
| `MutantResult.TraceSeq` | the `mutant-exec` event of that `Session.Exec` |
| `ProbeResult.TraceSeq` | the `probe-exec` event of that `Session.Probe` |
| `ControlResult.TraceSeq` | the `note` event, of kind `control`, summarising that `Session.Control` |
| `ControlResult.ExecSeqs` | the `exec` events, of kind `control-run`, of the binaries that control started |

`probe-exec.infected` is the pass's **raw** infection set: what the probe runtime
recorded, by mutant identity, before anything was filtered. `ProbeResult.Infected`
is that set minus the mutants validation rejected — an infection fact about a
mutant nothing will execute licenses no skipping — so the result is always a
subset of the event, and the difference is always rejected mutants. The event is
the account of what the pass recorded; the result is what a caller may act on.

A sequence of `0` means nothing was recorded, which — since a workspace always
has a recorder — means the call failed before it reached an execution: an
unresolvable mutant, a package with no prepared binary, a refused flag. A call
that *did* reach one and then failed carries its sequence and its `Binaries`
beside the error, and nothing else: the outcome, the infection set and the
captured output stay at their zero values, because the call established none of
them and the recording is all the account there is.

A *non-zero* sequence names an event that was recorded, not necessarily one that
can still be read: a sink that refused it kept nothing, and a bounded ring that
overflowed has since dropped it. The `run-end` reports both.

`ControlResult` has a `note` rather than an event of its own because the `type`
enum is closed and holds no payload for a control. Its children are ordinary
`exec` events of kind `control-run`, and they are the account of what ran: same
argv, same `dir`, same `timeout_ms` as the `mutant-run` beside them, and an
`env_names` that is the mutant run's minus `GO_MUTANTS_ACTIVE`. A `note` carries
no `exec_seqs` of its own the way `mutant-exec` and `probe-exec` do, which is
why `ControlResult.ExecSeqs` is a field: the note's `detail` names the same
sequences, but it is a sentence for a reader and never a field to branch on.

`MutantResult.Binaries`, `ProbeResult.Binaries` and `ControlResult.Binaries` are
the test binaries the call started, in launch order, by the **import path** of
the package each was built from. They stop where the call stopped, so a mutant
killed by the second of three binaries names two: naming all three would
describe a measurement that was never made. `MutantResult.KilledBy` is one of
them, `ControlResult.Package` likewise, and the `mutant-exec` and `probe-exec`
events name exactly the same set.

**Reproducing a run by hand.** `Session.OverlayManifest()` and
`Session.ProbeOverlayManifest()` are the two files `go` is pointed at to compile
the instrumented trees; the second is empty for a session prepared without a
probe tree. Neither can be derived — the manifest lives in a scratch directory
named when the session is prepared — so rebuilding one of the session's test
binaries is:

```console
cd <snapshot> && GOFLAGS=-overlay=<manifest> go test -c -o mutant.test ./<package>
```

Running it is the `exec` event's own `argv`, in the `exec` event's own `dir`,
with the mutant switched on:

```console
cd <exec.dir> && GO_MUTANTS_ACTIVE=<mutant id> <exec.argv...>
```

`exec.dir` is the **package's** directory inside the snapshot, not the snapshot
root: a Go test resolves `testdata` relative to where it runs, so the engine
starts every test binary in the directory of the package it was built from, and
a reproduction started anywhere else is running a different program.

A probe pass activates no mutant. What it needs instead is a private log to
record into, named by `GO_MUTANTS_PROBE` — a *file path*, not an index:

```console
cd <exec.dir> && GO_MUTANTS_PROBE=/tmp/infection.log <exec.argv...>
```

Every binary of one pass appends to one log, which is read once at the end, so a
file two passes share is two measurements nothing can tell apart.

Both manifests are also in the recording, as `artifact` events of kind
`overlay-manifest` and `probe-overlay-manifest`. They stay valid for as long as
the session does, which is why `OpenOptions.KeepTemp` is usually asked for
beside them.

**Preparation is recorded twice, on purpose.** `PrepareOptions.Trace` still
receives every `PrepareEvent` exactly as it did — one start and one finish per
phase, in phase order, synchronously — and the same timeline also reaches the
recorder as `prepare` events. The callback is for watching a preparation happen;
the recording is for reading it afterwards, and neither can tell a story the
other cannot.

## The preparation phase vocabulary is open

`PrepareOptions.Trace` receives a `PrepareEvent` when each phase starts and
again when it finishes. `KnownPreparePhases()` returns every phase *this build*
emits, in the order a preparation reaches them:

```text
discovery, probe_snapshot, main_validation, main_restoration, verification,
binary_build, probe_validation, probe_coverage_build, probe_restoration
```

The order is the order of first starts, not of finishes: `binary_build` starts
before the probe tree's three phases and finishes after them, because the two
builds run concurrently. A phase the options turned off is still emitted, as a
start immediately followed by a finish carrying `skipped`, so this is what a
consumer sees for every preparation and not only for a fully configured one.

**`PreparePhase` is an open vocabulary.** A later engine may emit a phase that
is not in this list — splitting a stage in two, or timing a step that is not
timed today, adds a phase without changing the meaning of any phase already
here. A consumer must therefore accept an unknown phase rather than refuse it:
record the string verbatim, or map it to a catch-all of its own. A consumer
that keeps a closed schema, an enumeration, or a fixed set of timers must pin
`KnownPreparePhases()` in a test of its own, so that the day a phase is added
is the day that test says so rather than the day somebody's run fails on a
string nobody had heard of.

Each call returns a fresh slice. `PrepareEvent.State` is `started` or
`finished`; a finish carries `Result` — `succeeded`, `failed` or `skipped` —
and a `Duration`. Callbacks are serialized, and every phase starts before it
finishes.

## What `Catalog.Digest` covers

`Digest` identifies the **set of mutants** and nothing else. It is the SHA-256
over three length-prefixed things:

1. the domain separator `go-mutants-catalog-v1`,
2. the decimal mutant count,
3. every `Mutant.ID`, in catalogue order.

Everything else about the session is **outside** it: `ModulePath`, `GoVersion`,
`Toolchain`, `Profile`, `TestPackages`, `WorkspaceDigest`, `Mutant.Package`,
`Mutant.Accepted`, `Mutant.Probed`, and `Rejections`.

So two sessions share this digest whenever they catalogued the same mutants,
however differently they were prepared — a different module path, a different
toolchain, a different profile that happened to select the same rules, a probe
tree in one and none in the other, or a validation that rejected mutants the
other accepted. It answers exactly one question: *are these two runs looking at
the same mutants?* The second question — *are these two prepared sessions
interchangeable?* — is `PreparedDigest`.

## What `PreparedDigest` covers

`PreparedDigest` identifies the **prepared session**: everything that has to
match before evidence gathered against one session may be reused against
another. It is the SHA-256 over these fields, each written as a four-byte
big-endian byte length followed by its bytes — the encoding the mutant ID uses
— in this order:

1. the domain separator `go-mutants-prepared-catalog-v1`,
2. `Digest`,
3. `WorkspaceDigest`,
4. `ModulePath`,
5. `GoVersion`,
6. `Toolchain`,
7. `Profile`,
8. the decimal `len(TestPackages)`, then every `TestPackages` element in order,
9. the decimal `len(Mutants)`, then per mutant in catalogue order: `Mutant.ID`,
   `Mutant.Package`, and three flag bytes — `a` or `-` for `Mutant.Accepted`,
   `p` or `-` for `Mutant.Probed`, and a third that is the constant `s`,
10. the decimal `len(Rejections)`, then every `Rejection.ID` in order.

The third flag byte is a constant the v1 recipe reserved and did not need. It
stays rather than being dropped, because dropping it is a different digest for
every session anybody has already stored evidence against.

**Not** hashed, and the list is exhaustive: `Catalog.Selection`,
`Mutant.Selected`, `Mutant.Index`, `Mutant.DisplayID`, `Mutant.Path`,
`Mutant.Line`, `Mutant.Column`, `Mutant.EndLine`, `Mutant.StartByte`,
`Mutant.EndByte`, `Mutant.Family`, `Mutant.Rule`, `Mutant.RuleVersion`,
`Mutant.SourceDigest`, `Mutant.Original`, `Mutant.Replacement`,
`Mutant.Branch`, and every field of a `Rejection` but its `ID` — `DisplayID`,
`Path`, `Line`, `Column`, `Rule` and `Diagnostic`.

Most of them are a function of something that *is* hashed. An index is a
position; a display identity is a prefix of an ID; the rule, the span, the text
on both sides and the source digest are the very inputs `Mutant.ID` is computed
from, so a change to any of them is a change to the ID, and the ID is in the
recipe. The coordinates and the compiler's words follow from the source that
digest names, and a branch proof is a lemma about the same span. Hashing them
again would add nothing and would move the key every time a line shifted above
an untouched mutant — and a key that moves for a session that has not changed is
a cache that never hits.

`Catalog.Selection` and `Mutant.Selected` are out for a different reason, and it
is the one to read before keying anything on this value. A selection is
**advisory** — it changes nothing the engine does, and `Session.Exec` runs an
unselected mutant exactly as it runs a selected one — so it describes the
caller's plan and not the session. What is keyed on this digest is *per-mutant
evidence*: this mutant survived against this prepared tree, which is a fact
about the tree, the toolchain and the mutant and about none of the caller's
intentions. Move the key with the selection and the first narrowed run misses on
every row a consumer has ever stored, then re-measures a module's worth of
mutants to write down answers it already had.

The rule that makes that safe belongs to the caller, and it is one line:
**never store "not run, out of selection" as evidence.** A mutant the selection
left out was not measured, so there is nothing about it to record; recording an
absence as a result is the only way two sessions under one key could come to
disagree.

The order of the fields is part of the recipe and is not free to change: a
different order is a different digest for every session anybody has already
stored evidence against, and changing it means changing the domain separator
with it.

`Catalog.PreparedDigest` does not follow an edited copy. `Session.Catalog()`
returns a deep copy a caller may rewrite, and the field keeps naming the session
the engine prepared rather than the struct in hand — usually what a caller wants,
since evidence stays keyed to the preparation that produced it, but a caller that
has rewritten a catalogue and needs a key for what it now holds hashes it itself
from the recipe above.

**Key evidence on this value, not on a fingerprint of your own.** Every
consumer that stored results per mutant needed this question answered and
computed some approximation of it: the mutant set plus `Package` and
`Accepted`, or plus `Probed`, usually as two fingerprints because no single one
covered both. Those are second recipes nobody versions, and the day the engine
starts reporting something new about a prepared mutant they go on hashing the
old thing and go on hitting. `PreparedDigest` moves when the engine's own
answer moves, and its domain separator carries the version, so a v1 key can
never be mistaken for a later one and both can sit in one store during a
migration.

Two preparations produce the same value when **every hashed input agrees**, and
that is the whole of the guarantee. What it rules out is incidental variation:
nothing in the recipe is a path — `WorkspaceDigest` names contents, not
locations, and `TestPackages` are import paths — and nothing in it is a
wall-clock time or a process identifier. So the same tree prepared twice in two
different temporary directories, by two different runs, hashes the same.

Across machines the qualification bites, because `GoVersion` and `Toolchain` are
in the recipe by design. The same source prepared under a different Go toolchain
is a *different* prepared session and gets a different digest — deliberately,
since a mutant's compilation and execution are the toolchain's behaviour, and
evidence gathered under one is not evidence about the other. A consumer sharing
a store between machines should therefore expect a miss when the toolchains
differ, and must not read that miss as "the source changed". The same goes for
`Profile` and the package set: a narrower preparation of one tree is not
interchangeable with a wider one, and the digest says so.

## Identity: which engine build produced the evidence

`PreparedDigest` names the prepared *session*. It does not name the *engine*,
and it cannot: two builds of go-mutants with different mutation rules can
prepare the same tree with the same toolchain and agree on every field it
hashes. A consumer storing mutation evidence needs both halves, and the second
one is here:

```go
// gomutants.ModulePath is the module path it looks for.
info, ok := gomutants.ReadBuildInfo()
```

`ok` is false in exactly one case: the program carries no build information at
all — a binary the go command did not build, or one built with it stripped. It
is **not** about go-mutants being named. Build information that reads fine and
simply does not mention this module returns the zero `BuildInfo` with `ok`
true, because the reading succeeded and what it says is "not here". Build
information that names the module **twice** returns the same thing: it
contradicts itself, no single version can be read out of it, and answering with
one of the two would be picking a version rather than reading one. In both
cases `Version` is `""`, `Version()` says `"unknown"`, and `Auditable` is
false.

**A test binary names no dependency at all**, and a consumer will meet this
before anything else on this page. The go command fills build information in
before a test binary's imports are known, so `go version -m` on one shows its
main module and no `dep` lines — which means that from inside a consumer's own
`go test`, go-mutants is *absent*: `ok` is true, the `BuildInfo` is the zero
value, and `Version()` says `"unknown"`, however firmly that consumer's go.mod
requires the engine. Only a built program names what it linked. A consumer
recording which engine produced its evidence therefore reads the identity in
the program that does the measuring, and a test asserting anything about the
engine's identity from inside a test binary is asserting about nothing.

| Field | What it says |
|---|---|
| `Version` | the version build information names: a tag, a pseudo-version, `"(devel)"`, or `""` when the module is not named. Since go1.24 a main module built out of a VCS checkout is itself stamped with a pseudo-version — with `+dirty` appended when the tree had edits — so `"(devel)"` now means the go command could not stamp one at all (see below). Under a replacement this is the version that was *required*, which is not the code that ran |
| `Sum` | the module checksum, when there is one. `""` under a replacement (the checksum recorded there covers the replacement), and `""` whenever the go command had none — a working-tree build, a vendored one. A main module is not automatically without one: `go install example.com/tool@v1.0.0` stamps what the proxy served |
| `Replaced`, `ReplacePath`, `ReplaceVersion` | a `replace` was in effect and where it pointed. A directory has no version of its own, and the go command writes its placeholder rather than an empty string, so `ReplaceVersion` is `"(devel)"` there |
| `Main` | go-mutants is the running program's main module — the command itself, or this module's own tests — rather than a dependency |
| `VCSRevision`, `VCSModified` | the `vcs.revision` and `vcs.modified` build settings, read **only** when `Main`, because build settings describe the main module and no other. Both are zero under `-buildvcs=false`, and a `vcs.modified` value that will not parse counts as modified |
| `Auditable` | `Version` names one immutable set of sources: a tag or pseudo-version that is not replaced, or a main module with a clean revision. A version carrying build metadata — anything after a `+` other than `+incompatible` — is never one, whatever the settings beside it say |

**Which builds still say `"(devel)"`**, now that a VCS checkout is stamped: a
test binary, which the go command never stamps; a build with `-buildvcs=false`;
a tree under no version control; and a build from a git **worktree**, because
the go command wants `.git` to be a directory and in a worktree it is a file.
The dirty case is the one to watch: `go build` in an edited checkout stamps
something like `v0.0.0-20260906202401-5950bb89fe10+dirty`, which looks exactly
like a pseudo-version, is served by no proxy, and is why `Auditable` refuses
build metadata outright rather than trusting `vcs.modified` beside it.

`Version()` is the same string with `"unknown"` for the empty case. It is the
label to print in a header or a log line, never the value to decide on:
`"(devel)"`, a `+dirty` stamp and a replaced module's required version all come
back looking like versions, and only `Auditable` says whether any of them names
any sources.

**What to key stored evidence on**, in order:

1. `Catalog.PreparedDigest` for everything about one prepared session — the
   mutants, the tree, the toolchain, the packages.
2. `BuildInfo.Version` **when `Auditable`** — a released or pseudo-versioned
   engine, or one whose main module has a clean revision, is a name that will
   mean the same thing tomorrow.
3. Otherwise the SHA-256 of the running executable, which is the consumer's to
   take.

**There is deliberately no `Identity()` and no embedded source digest.** The
case that would need one is the replaced one — a `replace` pointing at a
working tree, which is how every consumer develops against an unreleased
engine — and it is exactly the case such a digest gets wrong. Anything this
package could hash about itself would describe the sources somebody *committed*
(or shipped in the module zip), not the edited tree that actually compiled, so
it would read as a proof precisely when it is a lie, and a consumer's cache
would hit across two different engines. The bytes actually running are the
executable's, and taking that digest is the consumer's job because only the
consumer knows which file it launched, whether that file is still where it was,
and whether reading it is worth the cost. What this package owes it is an
honest `Auditable` false, which is what it returns.

## Selecting by line range

A consumer that narrows a run to the code somebody touched can do it by *file*
without help — drop every mutant whose `Path` the diff does not name — and that
over-selects badly: a file with one edited line and two hundred mutants
contributes all two hundred. The line-level rule has been the engine's since
`--changed` existed, and `PrepareOptions.Selection` is it, exposed.

```go
session, err := workspace.Prepare(ctx, gomutants.PrepareOptions{
	Selection: &gomutants.Selection{Lines: map[string][]gomutants.LineRange{
		// Module-relative, '/'-separated. Ranges are 1-based and inclusive.
		"internal/clamp/clamp.go": {{First: 41, Last: 48}, {First: 90, Last: 90}},
	}},
})
```

Every mutant then carries `Selected`, and `Catalog.Selection` is the
**normalised** copy the engine applied.

### What it changes, and what it does not

It sets `Mutant.Selected` and nothing else. The selection is applied *after*
discovery and *after* validation, so `Catalog.Digest`,
`Catalog.PreparedDigest`, every `Mutant.ID`, `Accepted`, `Probed` and
`Rejections` are identical to the same preparation without it. That is what
lets a consumer compare a narrowed run against the full run before it, hand an
id out of either straight back to `Session.Exec`, and merge two narrowings of
one tree.

Narrowing *discovery* instead would be faster and wrong, for the reason
`--changed` does not do it either: a mutant id is minted from its file's own
bytes and the catalogue is deduplicated across the module, so a discovery pass
that skipped unselected files would produce a different catalogue — and a mutant
in an untouched file would change identity because somebody edited a file
elsewhere.

`Catalog.PreparedDigest` does **not** move. Neither `Selected` nor `Selection`
is hashed, so a narrowed session and the full one carry the same key — which is
the point: what a consumer keys on that digest is per-mutant evidence, a fact
about the tree and the mutant that a plan does not change, and moving the key
the first time somebody narrowed a run would cost them every stored row. The one
thing a caller owes in exchange is to **never store "not run, out of selection"
as evidence**: an unselected mutant was not measured, so there is nothing about
it to record. See [What `PreparedDigest`
covers](#what-prepareddigest-covers).

**The narrowing is advisory.** `Session.Exec` runs an unselected mutant exactly
as it runs a selected one. A selection is the plan for a run, not a rule about
what may be measured: a consumer that finds an interesting survivor and wants
the mutants beside it executed must not have to prepare the module a second time
to do it.

The probe tree is unaffected. It is instrumented from the whole catalogue and
`Session.Probe` answers about all of it, which is what a consumer wants — an
infection fact is about a mutant and a test, not about this run's plan.

### The rule

`Mutant.EndLine` is the 1-based line the edit ends on: `Line` plus the number of
newlines in `Original`. A mutant is inside a range `[first, last]` when
`Line <= last && EndLine >= first`. It is exactly the rule
`go-mutants run --changed` applies to a diff — the same function, not a second
implementation — so the library and the CLI select the same mutants, and a
caller applying it by hand to a catalogue it already holds reaches them too:

```go
func touches(m gomutants.Mutant, first, last int) bool {
	return m.Line <= last && m.EndLine >= first
}
```

Comparing `Line` alone silently drops the multi-line edits — a condition
spanning three lines, a composite literal — whenever the range touches their
last line and not their first, and those are the mutants a `--changed` run
would have executed.

Two details of the count:

- **An `Original` ending in a newline opens the next line.** That newline is
  counted like any other, so an edit covering `"return 0\n"` on line 10 reports
  `EndLine` 11 and a range naming only line 11 selects it. The over-approximation
  by one line is deliberate: it is what `--changed` already does, so the library
  and the CLI select the same mutants, and erring towards selecting a mutant
  costs an execution while erring the other way drops one in silence and reports
  a score higher than the truth.
- **A carriage return is not a line break.** A CRLF file's break is one `\n`
  preceded by a byte that is not one, so `"a\r\nb"` spans two lines, not three.

### Normalisation, and what is refused

`Prepare` canonicalises the selection before it applies it, and hands the result
back as `Catalog.Selection`:

- paths are `path.Clean`ed, so `./pkg/../pkg/x.go` and `pkg/x.go` become one
  entry rather than two that select different sets;
- ranges are sorted and overlapping or adjacent ones are merged, so `5-7, 1-3,
  4` and `1-7` are the same value — which is what lets a consumer diff two runs'
  selections and be comparing the lines rather than the order somebody appended
  their hunks in;
- a path named with no ranges at all is dropped, since it selects the same
  mutants as a path nobody named: none.

Four entries are **refused**, with `ErrInvalidSelection` and a sentence naming
the entry: a path that is empty, absolute, backslash-separated, or escapes the
module, and a `LineRange` whose `First` is below 1 or whose `Last` is below its
`First`. Each is a request that looks like a narrowing and would select nothing,
and the judgement is `--changed`'s: a selection nobody can satisfy measures no
mutants, and a run that measured none and reported a perfect score is the one
failure this feature must not produce. The refusal happens before discovery
starts, so a mistake in the request does not cost a preparation to find.

A path the engine *can* read and the module does not hold is **not** refused: it
selects nothing, and is documented to. `Prepare` has read no source when the
options are resolved, and a selection is usually built from a diff — which names
deleted files, documents and testdata beside source — so refusing them would
make every consumer filter the engine's own input on its behalf.

## Listing the module

`Workspace.Module` answers what the frozen module holds. It is the call that
replaced the `go list -json` recipe this page used to carry: the workspace had
already frozen the tree, probed the toolchain and knew the exclusions, so every
consumer was re-deciding the same four things — which patterns are legal, how
`-tags` is spelled, how much output to keep, and where the module's Go version
comes from — and each answer was one more thing to get wrong on its own.

```go
module, err := workspace.Module(ctx, gomutants.ModuleQuery{
	// Module-relative patterns as go list reads them. Empty means "./...".
	Packages: []string{"./..."},
	// Build tags are the consumer's own; go-mutants invents none.
	Tags: []string{"integration"},
})
if err != nil {
	return err
}
for _, pkg := range module.Packages {
	if !pkg.HasTests {
		continue
	}
	// pkg.Dir is absolute and inside the frozen snapshot, so it may be read.
	fmt.Println(pkg.ImportPath, pkg.Dir, pkg.GoFiles)
}
```

`Module` is `{Path, GoVersion, Toolchain, Packages, TraceSeq}`. `Path` and
`GoVersion` are go.mod's own — read in this process with
`golang.org/x/mod/modfile`, with no child — `Toolchain` is what
`Workspace.ToolchainVersion` reports, and `TraceSeq` names the `exec` event the
listing was recorded at. `GoVersion` is the directive without its keyword
(`"1.26"`) and is **empty** for a module whose go.mod declares none, which is
legal and which a consumer comparing versions has to handle. Each `Package` is
`{ImportPath, Dir, Name, HasTests, GoFiles, TestGoFiles, XTestGoFiles, Imports,
Deps, EmbedFiles}`. Every file list is relative to `Dir` and spelled as
`go list` prints it: the Go source lists hold bare file names, and `EmbedFiles`
holds slash-separated relative paths, because a `//go:embed` may name a file at
any depth (`assets/deep/x.txt`).

- **The answer is sorted, and memoised.** `Packages` is in import-path order
  whatever order the patterns were given in, so two listings can be diffed. One
  query is listed once and a second identical query is answered from the memo,
  carrying the same `TraceSeq`. The key is the normalised query — patterns in
  the order given, tags sorted and deduplicated — so `ModuleQuery{}` and
  `ModuleQuery{Packages: []string{"./..."}}` are one question and a *different*
  tag set is a different one. Two listings are not remembered: one that
  **failed**, and one every caller **abandoned** — every caller gone before it
  finished — because what a listing being torn down came back with was produced
  for nobody. A listing that had already finished when its last caller left is a
  complete listing of a frozen tree and stays.
- **A listing is reused until a command has run.** Every memoised listing is
  dropped when a `Workspace.Exec` call returns, because that is the one call
  that can change the frozen tree: nothing refuses a command that writes into
  the snapshot — a `go generate`, a test that rewrites a golden file, a fuzz
  target the go command files a crasher for — and a package set that outlived
  one would describe a tree nobody has. It is dropped whatever the command did,
  since "did this one write" cannot be answered without re-freezing the tree.
  `Prepare` needs no such rule: its integrity gate refuses a tree a command has
  changed, and a successful preparation leaves the tree byte for byte the one
  `Open` froze.
- **`-tags` is the consumer's own.** go-mutants does not invent build tags, and
  a package set listed under different tags is a different package set from the
  one whose tests will be built.
- **Run it before `Prepare`, or beside the session.** Both are allowed and the
  answer is the same, because a successful preparation leaves the tree byte for
  byte the one `Open` froze. Only a preparation *in flight* makes the call wait,
  and only a *failed* one refuses it. It may not be called from a
  `PrepareOptions.Trace` callback, for the reason `Workspace.Exec` may not.
- **One listing is bounded like any other command**: by the caller's context and
  by the same ten-minute safety default a `Command` with no `Timeout` gets.
- **Two callers asking the same question share one `go list`.** The listing runs
  under a context of its own rather than under whichever caller reached the memo
  first, so a caller that goes away does not take the answer from the one still
  waiting; when the *last* interested caller leaves before the listing has
  finished, the child is killed and nothing is remembered — the next caller
  lists again rather than being handed what a torn-down listing came back with.
  A caller that leaves on its own cancelled context is told about its own
  context and never about somebody else's, and the message names the call
  exactly once.
- **A bad query is refused before anything runs.** An absolute pattern — POSIX
  or Windows-shaped, refused the same way on every operating system — one that
  escapes the module, one that is not module-relative, one beginning with a dash
  the go command would read as a flag, and a `Tags` element holding a comma or
  whitespace are all `ErrInvalidQuery`, with the offending element quoted. It is
  a refusal rather than a listing of nothing, on the rule `ErrInvalidSelection`
  already states.
- **Every other failure is an `*ExecutionError` with `Call: "module"`**, whether
  it was `go list` exiting non-zero, a child that would not start, a cancelled
  context or a go.mod that could not be read. The message begins
  `gomutants: module: `, the cause is wrapped so `errors.Is` still reaches it,
  and `Output` carries the toolchain's own words. The only refusals that are not
  this type are the two a consumer *branches* on rather than reports:
  `ErrInvalidQuery`, and the lifecycle sentinels `ErrPrepareFailed` and
  `ErrWorkspaceClosed`.
- **Toolchain noise on stderr is not an error.** `go list` writes its document
  to stdout and writes `go: warning: "./x/..." matched no packages`,
  `go: downloading …` and toolchain switches to stderr, all on a command that
  exits zero — so the two streams are captured separately and only the document
  is decoded. A pattern that matches nothing is an empty `Packages` and no
  error. The combined capture is what a failure quotes and what the recording
  digests.
- **The recording says it happened.** One `exec` event of kind `go-list` with
  the subject `module`, carrying the argv, the directory, the environment names
  and the exit status, joined to the answer by `Module.TraceSeq`.

`Workspace.Exec` is still the passthrough for any *other* command that has to
see the frozen tree — a `go build`, a `go vet`, a baseline of the caller's own.
A consumer that runs its own `go list -json` through it owns the three details
`Module` settles: pass `Env: []string{"GOWORK=off"}` so a `go.work` above the
snapshot cannot change the package set, raise `OutputLimit` from its 1 MiB
default to something sized for the document, and check `CommandResult.Truncated`
*before* decoding — what survives truncation is the notice line and the tail,
which is not a shorter document but an unparsable one.
