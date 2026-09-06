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
                                                    Session.Changes*
                                                    Session.Close
                             ──────────────────────────────────────▶ Workspace.Close
```

- **`Open(ctx, root, options...)`** locates the Go toolchain, sweeps the
  leftovers of runs that were killed before they could tidy up, and copies
  `root` into a disposable snapshot. Nothing after this reads or writes the
  user's tree.
- **`Workspace.Exec`** runs one shell-free command against the frozen snapshot:
  a build, a vet, a baseline `go test`, a `go list`. Calls may run concurrently
  with one another.
- **`Workspace.Prepare`** discovers candidates, builds the catalogue,
  compile-validates every mutant, instruments the sources, verifies the
  instrumented program once, and compiles the selected packages' test binaries —
  optionally building a second, probe tree alongside. A workspace may be
  prepared exactly once, *including when preparation fails after it has begun*.
- **`Session.Exec` / `Session.Probe`** reuse those binaries for any number of
  (mutant, target) combinations without rebuilding or rewriting anything.
- **`Session.Changes`** reports, in path order, anything a target wrote into
  the prepared snapshot.
- **`Session.Close`** releases the binaries, the session scratch, and the probe
  tree. **`Workspace.Close`** closes the session first and then releases the
  scratch directory and the snapshot. Both are idempotent, and closing the
  workspace closes the session; closing the session does not close the
  workspace.

After a `Prepare` — successful or not — `Workspace.Exec` is refused with
`gomutants: exec: workspace is already prepared; execute test targets through
its session`. The rule is a policy line rather than a lock consequence: the
instrumented tree is not the program a baseline command was written for.

## Locking and concurrency

`Workspace.mu` is an `sync.RWMutex` and it is held for the whole of each call
that takes it:

| Call | Hold | Consequence |
|---|---|---|
| `Workspace.Exec` | shared, for the whole command | Exec calls run concurrently with each other, and wait while a `Prepare` runs |
| `Workspace.Prepare` | **exclusive**, for the whole preparation | a `Prepare` in flight blocks every `Exec` and every `Close` until it returns |
| `Workspace.Close` | exclusive to claim the close, released while the directories are removed, retaken to publish the result | a second `Close` waits on the first through a done channel and returns the same error |

So a `Prepare` that takes two minutes is two minutes during which no
`Workspace.Exec` can start. A consumer that wants to overlap control work with
preparation runs it against a *second* workspace over the same root today.

`Session.mu` is a second `sync.RWMutex` with the same shape: `Catalog`, `Exec`
and `Probe` hold it shared, `Changes` and `Close` hold it exclusively. `Exec`
and `Probe` are therefore safe to call concurrently with each other and with
themselves — they share the session and nothing else, each getting its own
scratch directory, its own environment and, for a probe, its own infection log
— while `Changes` and `Close` wait for every call in flight, so neither can
observe a target halfway through a write.

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
| `Jobs int` | `0` → `min(NumCPU, 8)`, at most 32 | concurrent validation and test-binary builds |
| `BuildTimeout time.Duration` | `0` → 10 minutes | bounds each validation and test-binary build. Negative is invalid |
| `MutantTimeout time.Duration` | `0` → 10 seconds | the default outer timeout `Session.Exec` and `Session.Probe` use. Negative is invalid |
| `Verify Command` | zero → `go test ./...`, timed at `BuildTimeout` | run once against pristine files with instrumented Go builds |
| `SkipVerify bool` | `false` | omit `Verify`. Combining it with a non-zero `Verify` is an error rather than a silent preference |
| `Probe bool` | `false` | also prepare the probe tree |
| `Trace func(PrepareEvent)` | `nil` | receives serialized phase start and finish events synchronously |

### `ExecRequest` and `ProbeRequest`

| Field | Default | Meaning |
|---|---|---|
| `Mutant string` (Exec only) | required | a full 64-character ID or an unambiguous catalogue prefix |
| `Package string` | `""` | an import path or one module-relative package directory. Empty selects **every** compiled test package |
| `Args []string` | `nil` | passed verbatim to each selected test binary, after the engine's own `-test.timeout` |
| `Env []string` | `nil` | `KEY=VALUE` overlay for this call |
| `Timeout time.Duration` | `0` → `PrepareOptions.MutantTimeout` | overrides the session default when positive. Negative is invalid |
| `OutputLimit int` | zero or negative → 1 MiB | cap on the retained combined output of each test binary the call starts, exactly as `Command.OutputLimit`. A positive value below 256 is raised to 256 |

Targets are named with standard test-binary flags — `-test.run=^TestX$`,
`-test.fuzz=^FuzzX$`. There is no second test DSL. `-test.timeout` is reserved
(see *Paired timeouts*), and so are `-test.fuzzcachedir` and
`-test.fuzzworker`: the session owns the first and the Go fuzz coordinator owns
the second.

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

The consumer's rule has two clauses and dropping either is unsound:

> Skip executing test `t` against mutant `m` only when `m.Probed` is true **and**
> a `measured` probe of `t` does not name `m.Index`. An unprobed mutant is
> absent from every measurement there will ever be, so treat it as infected by
> every test.

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

`Session.Changes` returns `Change{Kind, Path, BeforeSHA256, AfterSHA256}` in
strict path order, `Kind` one of `added`, `removed`, `modified`.

`Workspace.Swept()` returns what `Open` collected before it copied anything:
`Removed` paths, `RemovedBytes`, `Live` (directories a running go-mutants still
holds a lock on), `Kept` (directories a `KeepTemp` run preserved on purpose),
and `Err` — carried in the value rather than returned, because failing to
collect somebody else's leftovers is not a reason to refuse to run.

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
| `ErrSessionClosed` | `Session.Exec`, `Session.Probe`, `Session.Changes` | the session, or the workspace that owned it, is closed |
| `ErrInvalidMutantID` | `Session.Exec` | `ExecRequest.Mutant` is not an identity: too short, too long, or not lowercase hex |
| `ErrMutantNotFound` | `Session.Exec` | a well-formed prefix no catalogued mutant carries |
| `ErrAmbiguousMutant` | `Session.Exec` | a prefix more than one mutant carries; `Matches` names them |
| `ErrMutantRejected` | `Session.Exec` | validation proved the mutant does not compile; there is no binary to run it in |
| `ErrProbeNotPrepared` | `Session.Probe` | the session was prepared without `PrepareOptions.Probe` |
| `ErrProbeInconsistent` | `Session.Probe` | the probe log named a mutant the catalogue cannot account for — an **engine bug**, never a caller's doing |

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
  manifest. `Stage` is `commands` (the integrity gate before discovery) or one
  of `source restoration`, `verification`, `probe instrumentation`,
  `probe source restoration`. `Changes` carries the paths and both digests, so
  a consumer can say *which* file moved. The remedy belongs to the caller:
  something wrote into the workspace.
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
  failed inside `Session.Exec` or `Session.Probe`: a test binary that would not
  start or could not be supervised, a generated runtime that refused the
  activation it was handed, an infection log that is there and cannot be read.
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
`Session.Exec`, one per `Session.Probe`. Two concurrent calls cannot observe
each other through a temporary file. The scratch lives *beside* the snapshot,
never inside it: every byte under the snapshot root has to be a byte that came
from the user's tree, or "a test wrote into the workspace" stops being
detectable.

Every one of those directories is kept when `OpenOptions.KeepTemp` asked for it
and removed otherwise — `Workspace.Exec`'s, `Session.Exec`'s and
`Session.Probe`'s alike. The per-execution scratch is half of the answer
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
owns it) and `-test.timeout` (see below).

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
the supplied budget. The refusal is a `*ReservedError` from `Exec` and `Probe`
themselves — before a scratch directory is made or a binary is started — and
reads `gomutants: session exec: -test.timeout is reserved by the session's
process supervisor`.

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
truncated JSON stream is not JSON. See the `go list` recipe below.

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

`MutantResult.Binaries` and `ProbeResult.Binaries` are the test binaries the
call started, in launch order, by the **import path** of the package each was
built from. They stop where the call stopped, so a mutant killed by the second
of three binaries names two: naming all three would describe a measurement that
was never made. `MutantResult.KilledBy` is one of them, and the `mutant-exec`
and `probe-exec` events name exactly the same set.

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
   `p` or `-` for `Mutant.Probed`, `s` or `-` for selection, which is `s` for
   every mutant until a selection can narrow a session,
10. the decimal `len(Rejections)`, then every `Rejection.ID` in order.

**Not** hashed, and the list is exhaustive: `Mutant.Index`, `Mutant.DisplayID`,
`Mutant.Path`, `Mutant.Line`, `Mutant.Column`, `Mutant.EndLine`,
`Mutant.StartByte`, `Mutant.EndByte`, `Mutant.Family`, `Mutant.Rule`,
`Mutant.RuleVersion`, `Mutant.SourceDigest`, `Mutant.Original`,
`Mutant.Replacement`, `Mutant.Branch`, and every field of a `Rejection` but its
`ID` — `DisplayID`, `Path`, `Line`, `Column`, `Rule` and `Diagnostic`.

Every one of them is a function of something that *is* hashed. An index is a
position; a display identity is a prefix of an ID; the rule, the span, the text
on both sides and the source digest are the very inputs `Mutant.ID` is computed
from, so a change to any of them is a change to the ID, and the ID is in the
recipe. The coordinates and the compiler's words follow from the source that
digest names, and a branch proof is a lemma about the same span. Hashing them
again would add nothing and would move the key every time a line shifted above
an untouched mutant — and a key that moves for a session that has not changed is
a cache that never hits.

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

`Mutant.EndLine` is the 1-based line the edit ends on: `Line` plus the number of
newlines in `Original`. A mutant is inside a range `[first, last]` when
`Line <= last && EndLine >= first`, and that is exactly the rule
`go-mutants run --changed` applies to a diff, so a caller narrowing the same
catalogue through this API reaches the same mutants:

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

## Recipe: listing the module

`Workspace.Exec` is the passthrough for any command that has to see the frozen
tree, and `go list -json` is the common one. Four details matter:

```go
listed, err := workspace.Exec(ctx, gomutants.Command{
	// "go" is replaced by the toolchain Open located.
	Argv: []string{"go", "list", "-json", "-tags", "integration", "./..."},
	// Empty Dir is the module root; go list resolves ./... from there.
	Dir: "",
	// The go command searches every parent directory for a go.work and obeys
	// $GOWORK, so a snapshot placed one level below somebody's workspace would
	// otherwise resolve against a file the snapshot does not contain.
	Env: []string{"GOWORK=off"},
	// Sized for a document, not for a test failure: truncation keeps the tail,
	// and the tail of a JSON stream does not parse.
	OutputLimit: 32 << 20,
})
if err != nil {
	return err
}
if listed.TimedOut || listed.ExitCode != 0 {
	return fmt.Errorf("go list exited %d: %s", listed.ExitCode, listed.Output)
}
// Before decoding, not after: what survives truncation is the notice line and
// the tail, which is not a shorter document but an unparsable one.
if listed.Truncated {
	return fmt.Errorf("go list produced %d bytes, more than OutputLimit "+
		"kept; raise it", listed.TotalBytes)
}
decoder := json.NewDecoder(bytes.NewReader(listed.Output))
for decoder.More() {
	var pkg struct{ ImportPath, Dir string }
	if decodeErr := decoder.Decode(&pkg); decodeErr != nil {
		return decodeErr
	}
	// …
}
```

- **`-tags` is the consumer's own.** go-mutants does not invent build tags, and
  a package set listed under different tags is a different package set from the
  one whose tests will be built.
- **Run it before `Prepare`.** `Workspace.Exec` is refused once the workspace
  has been prepared.
- **Size `OutputLimit` for the whole document.** The default is 1 MiB, which a
  large module's `go list -json` exceeds; a truncated result is not a shorter
  document but an unparsable one, because the notice line and the tail are
  what survive. Check `listed.Truncated` before decoding rather than letting
  the decoder discover it: the flag says outright what a JSON syntax error at
  byte zero only implies.
