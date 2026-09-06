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
- `OutputTail` is the last 50 lines of the deciding binary's combined output.
- `Artifacts` are bounded copies of the standard `go test fuzz v1` inputs a
  fuzz target wrote, captured before its private cache is removed.

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
- `ExitCode` is 0 for a measured pass and `Duration` is non-negative.
- `Output` is the bounded combined output of the binary that decided the pass —
  the failing one for `test-failed`, the last one for `measured`. It is there
  for every outcome, including the ones that carry no `Infected`, because a
  pass that proves nothing is exactly the one whose output has to be readable.

The consumer's rule has two clauses and dropping either is unsound:

> Skip executing test `t` against mutant `m` only when `m.Probed` is true **and**
> a `measured` probe of `t` does not name `m.Index`. An unprobed mutant is
> absent from every measurement there will ever be, so treat it as infected by
> every test.

### `CommandResult`, `Change`, `SweepResult`

`CommandResult` describes a command that *started*: a non-zero `ExitCode` and a
`TimedOut` are results, not infrastructure errors. An error alongside it means
go-mutants could not run the command at all.

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

Match them with `errors.Is`. They survive wrapping, and the sentences they
appear in are the ones the engine has always printed. `ErrProbeNotPrepared`
predates the rest and spells its own text with the prefix — `gomutants: the
session was prepared without a probe tree` — while the newer sentinels carry
only the condition and are wrapped into the engine's sentence where the failure
happens. Both spellings are frozen, and neither is a string to compare against.

### Typed errors

Reach all of these with `errors.As`.

- **`*VerificationError{Command, ExitCode, TimedOut, Duration, Output}`** —
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
| a lifecycle sentinel | a programming error in the consumer |

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

`Workspace.Exec` keeps its per-call directory when `OpenOptions.KeepTemp` asked
for it, and removes it otherwise. A session call always removes its own,
`KeepTemp` or not. For `Session.Probe` that is deliberate rather than an
oversight: the pass's infection log lives in that directory, and a log left
behind would be appended to by the next pass over the same session, which would
then read the previous pass's indices as its own. What `KeepTemp` preserves is
the durable state — the snapshot, the probe tree, and the workspace scratch the
session's own scratch lives inside — which is where the question "what did the
tree this mutant ran in look like" is actually answered.

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
`OutputLimit` bytes the result holds a notice line beginning `[go-mutants]
output truncated` followed by as much of the *tail* as the remaining budget
allows, and `len(Output) <= OutputLimit` still holds, notice included.

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
the same mutants?* A consumer that needs "are these two prepared sessions
interchangeable?" hashes the rest itself.

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
  what survive.
