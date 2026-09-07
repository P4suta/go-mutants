<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Architecture

**Status: implemented.** The pure packages, the strict configuration decoder,
the snapshot, the baseline execution layer, discovery for the whole
eleven-family catalogue, guard-based instrumentation with its generated runtime
in all three forms, compile validation, mutant execution, coverage-guided
selection, `RunReport v1` with its history store, the Stryker projection and
the self-contained HTML report, the live TUI dashboard, `--changed`, `--shard`
with `report merge`, `--explain`, the outcome cache, and the whole v1 command
tree — `run`, `list`, `doctor`, `init`, `report`, and `cache` — all exist.
Each section below carries its own status line; nothing here should be read as
a description of working software until its status says so.

## Invariants

Four invariants shape every decision. They describe what the design holds
itself to, not what every phase already does; all four are load-bearing in
every run today.

1. **The target workspace is read-only.** Discovery reads it; every build,
   edit, and test happens inside a disposable snapshot that excludes `.git`,
   caches, and the report directory, rejects symlinks and junctions, and is
   described by one sorted manifest.
2. **Instrumentation happens once.** All compilable mutants for a run are
   spliced into the snapshot in a single pass, and the environment variable
   `GO_MUTANTS_ACTIVE=<64-hex id>` activates exactly one of them per test
   process. The build is effectively performed once, not once per mutant.
3. **Bytes, not pretty-printed syntax.** Mutants are assembled from original
   source byte slices, so comments, whitespace, and CRLF survive untouched, and
   splices preserve line numbers so coverage line data maps one-to-one.
4. **Phases are types.** The pipeline is a typestate: a value that has not
   passed validation cannot be handed to the runner, because the runner's
   signature does not accept it.

## Pipeline

```text
Discovered → Snapshotted → BaselinePassed → Validated → Instrumented → Completed
```

```text
packages.Load + types walk        (candidates, skips with reasons+sites)
            |
            v
sorted snapshot + manifest digest (symlink/junction rejected)
            |
            v
baseline build + test             (timeout derivation)
            |
            v
batch compile + delta debugging   (rejected[] with diagnostics)
            |
            v
instrumented baseline             (no mutant active: meaning preserved)
            |
            v
profile each test, then each mutant against the tests that reach it
            |
            v
worker pool over one snapshot     (per-process activation, tree kill)
            |
            v
outcome cache + RunReport v1      (JSON, HTML, history, exit policy)
```

Every transition on this line is what `run` performs today, and discovery on
its own is what `list` prints. Nothing on it is a promise any more.

Each arrow is a phase transition with its own type. `runner.Execute(m
Validated)` cannot be called with a raw candidate; that is the compile-time
version of the rule "only validated mutants run".

## Temporary directories: an owner and a collector

Every byte a run writes has an owner and a collector. The snapshot is a full
copy of somebody's module, and "removed at the end of the run" is a promise no
process can keep: a SIGKILL, an out-of-memory kill or a closed terminal leaves
the copy behind, and nothing will ever delete it.

So each top-level directory go-mutants creates under the temporary parent — the
snapshot, the probe tree, the per-run scratch — carries the pair
`internal/tempowner` writes into it:

- `owner.lock`, an exclusive advisory lock held open for the directory's whole
  lifetime. It is the liveness signal and the only one: it disappears when the
  process does, however the process died. A pid is not used for liveness,
  because pids are reused and the answer would be about some other process.
- `owner.json`, `{"schema":"go-mutants-temp-owner-v1","pid":…,"started":…,
  "kept":…}`, which people read and which the sweep reads for one bit.

Before it copies anything, a run sweeps its own prefixes in that parent. A
directory whose lock is free and whose marker does not say `kept` is an orphan
and goes, and so does an unowned directory nothing has touched for a day — the
shape a run started by a version older than this leaves behind. A locked
directory belongs to a live run, a kept one was preserved on purpose, and
everything else in a directory shared with the whole machine is left exactly as
it was found. A failure to collect is a warning (`GOM4044` for `run`) and never
the reason a run stops: the measurements are unaffected either way.

The copy itself lives one level down, in `go-mutants-snap-…/tree`. That is
forced rather than chosen. `snapshot.Redigest` applies no exclusions, so a
marker beside the sources would be reported as drift by every run that checks;
and the probe tree, which is a snapshot of a snapshot, would copy the marker
and hash a manifest that no longer described the tree it came from.

The `…` above is not random. It is sixteen hex characters of the SHA-256 of the
absolute source root, so the snapshot of a given module lands on the same path
on every run. The go command hashes each package's absolute directory into its
compile action id unless `-trimpath` is passed, and go-mutants will not pass
it: `-trimpath` changes the program under test — a test that reads
`runtime.Caller` paths behaves differently under it — and the build that was
verified has to be the build the user's own `go test` runs. So a snapshot at a
fresh path shared nothing with the previous run's build cache: every run
recompiled the whole module, and the cache filled with one copy of its objects
per run. One name per source root turns the second run into an incremental one.

Only the path is reused, never the bytes. A directory already sitting under
that name goes to the sweep described above, and the tree is copied fresh into
whatever the sweep left, so a run never inherits the half-instrumented tree of
one that died before it. What the sweep will not collect is not waited for
either: a directory locked by a concurrent run of the same root, one a
`KeepTemp` run preserved, or an unowned young one leaves this run with an
`os.MkdirTemp` name instead, which costs it the build-cache hits and nothing
else and is reported in `Snapshot.StableDir`. In either order of that race, two
runs of one root end with one stable directory and one random one, each holding
its own lock, and never with two runs in one tree.

`OpenOptions.KeepTemp` is the escape hatch for the one question a removed
directory cannot answer — what the tree a mutant ran in actually looked like.
It marks each directory `kept` instead of removing it, so the next run's sweep
obeys the decision rather than collecting it minutes later, and
`Workspace.Preserved` names what was left behind.

### What the report directory keeps, and what collects it

The temporary parent is not the only place a run writes. `report.directory` —
`reports/mutation/` by default — is the one directory *inside* a user's own tree
that go-mutants may write into, and the same pair of questions applies to it:
who owns each thing there, and what takes it away again.

Three kinds of thing live there, and they are owned differently:

```text
reports/mutation/
  mutation.json            the newest run, overwritten
  mutation.html            the newest run, overwritten
  trace/<run-id>/          one per traced run, newest 10 kept
  diagnostics/<run-id>/    one per failed run, newest 10 kept
```

- `mutation.json` and `mutation.html`, the published projections of one run.
  There is one of each and the next run overwrites them, so they need no
  collector. `--report none` writes neither.
- `trace/<run-id>/`, the diagnostic account of one run, written only under
  `--trace`. Each run adds a directory, so this one does need a collector: a
  traced run prunes its trace root to the newest `trace.RetainRuns` — ten —
  recordings *as it opens its own*, and `go-mutants trace clean` is that
  collector run by hand. Collecting before the run's own directory exists is
  what keeps the rule free of an exception protecting the recording being
  written.

  Two things are never collected, and both are the interesting half of the rule.
  Only a directory named by `engine.RunIDPattern` and holding a `trace.jsonl` is
  a recording at all, so a file or a directory somebody else keeps beside them
  survives. And a recording whose stream does not end with its `run-end` is left
  alone — a run in progress, or one that died — because the account of the crash
  is the one a reader most wants, and a collector that took it while keeping ten
  accounts of runs that went fine would be collecting exactly backwards. That is
  also what makes a live run safe from a concurrent `trace clean`, rather than
  only from being the newest name in the root. `trace clean --all` is how
  somebody who has read them says so, and the empty directory goes with the last
  recording in it.
- `diagnostics/<run-id>/`, the bundle a *failed* run leaves so that it can be
  diagnosed without being run again: the rendered failure and its typed chain,
  the environment's variable names, the `doctor` table, the run's own recording
  out of the in-memory ring, and the report if it had published one. A run
  already traced to a directory writes the bundle *there* instead, beside the
  stream it explains, and writes no second `trace.jsonl`.

  It is collected by the same implementation and the same rule, with one
  predicate swapped: a bundle is finished when its last file,
  `preserved-paths.txt`, is there, and one without it is a run that died while
  writing it. That is why the bundle's files are written in a fixed order —
  `error.txt` first, because it is what makes the directory go-mutants', and
  `preserved-paths.txt` last, because it is what says the directory is complete.
  A bundle whose *first* write fails is a third case and is removed on the spot:
  an empty directory carries no marker, so neither the retention nor
  `trace clean --all` could ever name it, and it would keep the empty
  diagnostics root from being removed as well. `trace clean` sweeps both roots;
  `trace list` lists only recordings, because a bundle is not one.

  A traced run's bundle lands in the recording's own directory, so the *trace*
  root's predicate has to ask both questions: a recording is finished when its
  stream ends with `run-end` **and** the directory holds either no bundle at all
  or a finished one. Asking only about the stream would call such a directory
  complete while half a bundle sat in it, while the same half-written bundle in
  the diagnostics root is held back — so the answer would have depended on
  whether the run happened to be traced, which is not a fact about how complete
  the account is.

That everything diagnostic lands under `report.directory` rather than beside the
snapshot in the temporary parent is forced rather than chosen, by the same fact
that forced the snapshot marker one level down. `snapshot.Create` excludes
`.git` and `report.directory` and nothing else in the workspace, so a recording
written anywhere else in the tree would grow while the run digests the tree, and
the run would report drift it caused itself — a diagnostic that fails the run it
is a diagnostic of. That is why `--trace=DIR` refuses a directory inside the
workspace and outside `report.directory`, symbolic links resolved on the longest
existing prefix, and why the refusal costs a `trace-unavailable` note and a
`GOM1013` warning rather than the run: the run then records into memory, exactly
as an untraced run does. The diagnostics root is placed by the same function and
refused by the same check, under `GOM1014`, and the refusal costs the bundle
rather than the failure it was going to explain.

The other direction of the same rule is that nothing diagnostic is added under
`TMPDIR`. A recording is something a user attaches to a bug report, and a run's
temporary parent is swept by the next run of the same root: a diagnostic that
disappears on the next invocation is not one anybody can hand over.

## Instrumentation: guard-based rewriting

Status: implemented in `internal/instrument`. This is the hardest component and
the reason for most of the other decisions. Which of the spliced mutants
actually compile is not decided here; see
[Compile validation](#compile-validation).

A generic helper (`__gm.Arith(id, OpAdd, a, b)`) was rejected: it breaks on
untyped constants, shift operands, and named types. Instead both branches are
written so the **compiler** type-checks each one in its original context.
Evaluation order and short-circuiting are preserved because only one side ever
executes.

- **Form S — statement guard.** For assignments, expression statements,
  `return`, `++`/`--`, sends, `defer`, and `go`:

  ```go
  if __gm.M[7] { <mutated copy, flattened> } else { <original bytes> }
  ```

  Several mutants at one site chain with `else if __gm.M[12]`.

- **Form C — boolean selector.** For `if`/`for` conditions and other boolean
  contexts:

  ```go
  (__gm.M[3] && (<mutated condition>) || !__gm.M[3] && (<original condition>))
  ```

  Short-circuiting alone selects the side; there is no allocation.

- **Form D — declaration rewrite.** For `:=` and `var` initializers:

  ```go
  var x T; if __gm.M[9] { x = <mutated RHS> } else { x = <original RHS> }
  ```

  `T` comes from `types.TypeString` with an import-qualifier map, and it is
  discovery that computes it: the type information the qualifier needs is
  there and nowhere else, so every candidate carries the form, the site span,
  and any declared types down to instrumentation as a hint. A type that cannot
  be named is a recorded skip with reason `unnameable-decl-type`, never a
  silent omission — and so is every other site none of the three forms can
  express; see [Operators](operators.md).

**Flattening.** The mutated copy is re-tokenized with `go/scanner` and explicit
semicolons are inserted where automatic semicolon insertion would have applied,
so the copy fits on one physical line. Line comments are dropped, block
comments kept. The original side stays byte-identical in the `else` branch, so
every line number in the file is unchanged.

**Interval forest.** Spans are grouped under their innermost enclosing rewrite
site. Within a site, mutants are siblings (each copy is the pristine original
plus its own patch); across sites they nest, and splicing is innermost-first
through an offset map. Code growth is proportional to the total size of
rewritten statements, not to the number of mutants times file size.

## Generated runtime package

Status: implemented. The runtime lives inside the snapshot at
`<module>/gomutants_rt/`. It cannot live in a `_`- or `.`-prefixed directory,
because the Go tool ignores those. A name collision bumps a suffix.

It exports `var M [N]bool` — a dense array in catalog order — and a map from
full ID to index. Its `init` reads `GO_MUTANTS_ACTIVE`: empty means every entry
stays false, which is exactly the instrumented baseline; an unknown ID calls
`os.Exit(97)` so a stale catalog can never masquerade as a clean baseline, and
the runner classifies 97 as an infrastructure error. Because the package is
first-party, no `go.mod` edit is needed and vendor mode is undisturbed. Writes
to `M` happen only during `init`, which happens-before test code, so the
dispatch is a plain array load and the race detector stays quiet.

## Probe runtime and the infection log

Status: the runtime, its log format and the first probe form — the return-value
one — are implemented in `internal/instrument`, and the pass that drives these
processes is implemented in `internal/execute` and reachable through the engine
API's `Session.Probe`; the other probe forms are not.

The next proof a consumer can act on is **infection**: if the site of mutant
`m` never evaluated to a value different from the original's during test `t`,
then `t` cannot have killed `m`. A mutant is a one-site edit, so equal values
at every evaluation and equal side effects mean the two programs ran the same
state sequence, and executing `t` against `m` can only reproduce a result
already known. Measuring that needs a **probe tree**: a second instrumented
snapshot in which no mutant is ever active, where the original semantics run
and each site checks — without side effects — whether the mutated value would
have differed.

`Instrument` chooses between the two trees with `Options.Mode`. The zero value
is the mutant tree above; `ModeProbe` rewrites every site it has a probe form
for and generates the probe runtime beside them. `InstrumentFile` takes the same
mode and `validate.Options` carries it into both, so a probe tree is built and
bisected by exactly the phase that builds and bisects the mutant tree — one
build if it compiles, and a per-mutant rejection when a site does not.

The probe runtime is generated into a **different snapshot** under the same
`<module>/gomutants_rt/` name and the same collision rule, so the two packages
never meet and everything above them reads one `Result` without asking which
tree produced it. It exports exactly one name, `Infect(i uint32)`, and holds no
ID table at all: a probe tree activates nothing, so it never resolves an ID —
it records the dense index a guard already spells. One atomic
compare-and-swap per mutant keeps a site evaluated a million times to one line,
and the line goes straight to an `O_APPEND` file, so there is no exit hook and
no flush window: whatever a process wrote before it died is exactly what it
proved. Because writes to the mutant runtime's `M` still happen only in `init`,
that invariant is untouched — this is a different artifact in a different tree,
not a change to the one the mutant pass builds.

`init` reads `GO_MUTANTS_PROBE` for the file to append to. Empty or unset is an
ordinary run: the runtime is linked in, records nothing, and costs one nil
check, which matters because the same tree is also built and run by people who
are not probing. A log it cannot open or write makes the process exit `98`
instead of running the tests. That is not defensive: an empty log reads exactly
like a run in which no site was ever infected, and *that* reading is what
licenses skipping executions, so silence is the one answer a probe must never
give. The pass classifies 98 as its own outcome, `unavailable`, which carries no
facts — a red suite and a runtime that could not record are different problems
and neither is evidence about infection.

The log is the append-only `gomutants-infection-v1` format:

```text
gomutants-infection-v1 <catalog digest> <N>
<index>
<index>
```

`<N>` is the runtime's array length — the catalog size, or 1 for an empty
catalog, exactly as `M`'s is. Several test processes of one target append to
one file, so the header appears **once per process** rather than once per file:
a process that held its header back until it had an index to write would say
nothing at all if it died first. Every occurrence must be identical.
`fmt.Fprintln` issues a single `Write` and POSIX `O_APPEND` keeps small writes
whole, so concurrent processes never interleave inside a line.

`instrument.ReadInfectionLog` reads it back and is deliberately fail-closed. It
is handed the **catalog size**, not `<N>`, and derives `<N>` from it through the
same rule the generators size the array with, so no caller has to know that
rule. The distinction is the empty catalog and nothing else: indices are bounded
by the size rather than by `<N>`, so an empty catalog's log is readable — its
header alone says nothing was infected, because nothing could be — while any
index in it names a mutant that does not exist and is refused.

An empty file, a missing header, a header naming another catalog or another
width, a repeated header that differs from the first, an index that is not a
decimal `uint32`, an index at or past the catalog size, or a last line the
writer never finished each yield `GOM7330` and no indices at all. The part of a
damaged log that still parses is precisely what a smaller, wrong answer looks
like, and a smaller answer here is a test that was skipped when it should have
run. The caller's one safe reading of an error is "this target yields no
infection facts".

### The return probe

The first probe form covers the return-value rules — `return-zero-numeric`,
`return-empty-string`, `return-true`, `return-false`, `return-nil` and
`return-err-to-nil` — whose replacement is always a constant `K` drawn from
`0`, `""`, `true`, `false` and `nil`. One temporary is declared per result and
one `if` written per mutant, so a mutant on the second result of
`return E0, E1` makes that statement

```go
{ var r0 T0 = E0; var r1 T1 = E1; if r1 != K { __gm.Infect(i) }; return r0, r1 }
```

and the form is chosen first because its exactness needs the least argument.

The mutant it stands in for is `return E0, K`: it returns the constant
*instead of evaluating* `E1`. So the probe speaks for that mutant only where
evaluating `E1` is nothing but computing a value, which is three conditions
`internal/discover` proves before it hands out a hint at all.

- **Every operand of the statement is effect-free** — no call, no method call,
  no receive, no `append`. The mutated operand has to be, because an effect
  there is an effect the mutant does not have: `return compute(), nil` mutated
  to `return 0, nil` never calls `compute`, and a test watching for what it did
  kills a mutant the probe reported as never differing. The operands beside it
  have to be as well, because the rewrite evaluates in source order and the
  compiler does not — the spec leaves the order of a plain variable read
  relative to a call in another operand unspecified, and gc performs the read
  after the calls, so for `return s.n, set()` the probe would compare a value
  the original binary never returned. With no effects anywhere, every order
  yields the same values and the block's execution is the original's.
- **The probed operand cannot panic.** If it panics the mutant does not, the
  two programs diverge, and the divergence is invisible here: the `if` is never
  reached, so nothing is recorded and the log reads exactly as it reads for a
  site that never differed. The other operands may still panic, because the
  original and the mutant then panic there identically. This one is decided per
  result, so `return p.n, err` keeps the probe on `err` and loses it on `p.n`.
- **The probed result is not floating-point or complex.** `-0.0 != 0` is false,
  so a `return-zero-numeric` mutant at a float result holding negative zero
  reads as *not* infected — the answer that skips a test — while `math.Signbit`
  and `1/x` tell the two values apart. NaN needs no rule and gets none:
  `NaN != 0` is true, which is the safe answer.
- **`Tj` is the declared result type, not the operand's.** `return 0` in a
  function returning `int64` becomes `var r0 int64 = 0`, which is exactly
  the conversion the `return` performs — and the conversion the mutant's
  `return K` would have performed too, so `rj != K` compares the two values the
  two programs would really have returned. `internal/discover` spells those
  types with the machinery Form D's declarations already go through, so there
  is one spelling rule rather than two that can drift apart.
- **The comparison is total.** Numbers, strings and booleans compare with `!=`
  whatever named type they wear, and comparing an interface, pointer, slice,
  map, channel or function value with the `nil` literal compares against the
  nil value of that very type — no dynamic-type comparison happens, so nothing
  panics.
- **The statement stays well-formed.** `return r0, r1` assigns named results
  exactly as the original did, so deferred functions observe the same values,
  and a block whose last statement is a `return` is itself a terminating
  statement, so a function whose body ended in one still does.
- **Several mutants of one statement are several `if` lines in one block.**
  `return-true` and `return-false` on one boolean result, or a rule on each of
  two results, are alternatives of a single rewrite; there is never more than
  one rewrite per statement. A `return` nested inside another's operand — in a
  function literal — is composed children-first, exactly as nested guards are.

Two more shapes are refused, and every refusal happens in `internal/discover`
where a hint is computed rather than in the rewriter that would have to use it.
A result type the file cannot spell — a dot-imported package's,
`unsafe.Pointer` — leaves the site without a hint, and so does a result that is
or contains a type parameter, because a value of a type parameter's type need
not be comparable with a constant. A refusal costs the *probe* and nothing else:
the candidate is still catalogued, still mutated and still guarded in the mutant
tree, which is why it is not a skip reason — a skip would tell a user
go-mutants passed over an edit it in fact makes.

The three soundness conditions cost the layer almost nothing, which is the
measured reason they are drawn where they are. On a consumer's baseline of some
6,300 mutants, the return-value survivors whose operand is a bare call are
overwhelmingly `return fmt.Errorf(…)`, always infected and never dischargeable
by this layer anyway; of the survivors whose operand is pure — an identifier, a
literal, a field, a composite — almost all sit in statements whose other
operands are pure too, because `return 0, err` and `return report{}, err` are
what such a statement looks like.

Everything else is simply unprobed for now. Only this family has a form, so a
file of comparisons comes out of `ModeProbe` byte for byte as its author wrote
it, with no runtime import at all; a run then learns nothing about which tests
could observe those mutants and runs them all, which is the safe direction.
A probe site that turns out not to compile is dropped the same way, by the
bisection in `internal/validate`: the mutant loses its probe and keeps
everything else.

### The probe pass

A tree that records nothing until somebody runs it is not yet a measurement.
The pass is what runs it, and it reaches a consumer as one method on the engine
API's session:

```go
session, err := workspace.Prepare(ctx, gomutants.PrepareOptions{Probe: true})
…
measured, err := session.Probe(ctx, gomutants.ProbeRequest{
    Package: "example.com/project/internal/codec",
    Args:    []string{"-test.run=^TestRoundTrip$"},
})
```

`PrepareOptions.Probe` is what builds the tree, and it is off by default: a
second instrumentation, a second compile validation and a second set of test
binaries are not free, and a caller that never asks the infection question
should not pay for the answer. Nothing about the mutant tree, the catalog or
`Session.Exec` changes either way.

The ordering inside `Prepare` is forced rather than chosen. A workspace holds
one snapshot and validation instruments it *in place*, so the probe tree is
copied from the pristine snapshot **before** the mutant tree's validation
rewrites it — and from the snapshot rather than from the user's tree, which may
have moved since `Open` froze it. The copy's `WorkspaceDigest` has to equal the
mutant snapshot's or `Prepare` fails: a probe tree that is not the mutant tree's
source proves nothing about it, and the digest is the statement of sameness the
snapshot package already has. It is then validated by the same
`internal/validate` phase with `Mode: instrument.ModeProbe` and passes the same
drift gate, and its test binaries are built into the session's own scratch.
There is deliberately **no verification command** on it: a probe pass already
reports a failing target as "no facts" per call, so a suite-wide gate would buy
what the per-call rule already gives and cost a full test run to get it. The
tree lives as long as the session and `Session.Close` removes it.

`Prepare` re-digests the frozen snapshot at the top of its *instrumentation
window* — the stretch from that gate to the end of source restoration, and the
only part of a preparation where the files on disk are not the program anybody
wrote — and holds the tree exclusively for exactly that stretch.
`Workspace.Exec` holds the shared side of the same lock while its child runs, so
independent controls run concurrently with each other **and with a
preparation**, waiting only for the window; a command already running when the
window is about to open makes the preparation wait for it instead. Both hold the
workspace's lifetime lock shared for their whole calls, so `Close` waits for
every one. Each call has its own temporary directory; any change a command
leaves in the shared frozen tree before that gate is a deterministic preparation
failure, never an input silently accepted by discovery, and a change made *and
undone* while discovery was reading is caught by comparing the catalogue's own
source digests against the frozen manifest. After the window the binaries are
compiled from a copy of every frozen file, taken at the top of the window and
mapped through the same overlay, so the build reads no byte of the tree: a
command writing there while it runs — leaving the write or undoing it between
two of the compiler's reads — changes nothing the session is made of, and there
is nothing left for a re-digest after the build to catch. Commands are also
allowed after a preparation has succeeded — the tree they run against is then
the snapshot `Open` froze, plus whatever such a write left — and a change one of
those leaves is nobody's failure: there is no later discovery for it to corrupt,
the frozen digests do not move, and `Session.Changes` is what reports it.

One call is one target against however many binaries `ProbeRequest.Package`
selects, each started exactly as a mutant's is — same working directory, same
paired timeouts, same arguments, since evidence about a mutant run is only
evidence if the same tests ran the same way — but with `GO_MUTANTS_PROBE` in the
environment where a mutant run has `GO_MUTANTS_ACTIVE`. All of them append to
one log, private to the call, in a scratch directory removed when it returns:
several processes appending to one file is what the format is built for, and it
is what makes the answer a statement about the *target* rather than about one of
the binaries that ran it.

The four outcomes are one measurement and three refusals:

| Outcome | When | `Infected` |
| --- | --- | --- |
| `measured` | every selected binary exited 0 and the log was readable | the set |
| `test-failed` | a binary exited non-zero | `nil` |
| `timed-out` | the supervisor killed a binary | `nil` |
| `unavailable` | a runtime exited `98` and never ran the tests | `nil` |

The asymmetry is the design rather than an accident of it. An infection fact is
a licence not to execute a test, so everything the pass cannot vouch for is
reported as *no facts* and never as "nothing was infected" — which is the same
sentence spelled in a way somebody would act on. `Infected` is non-nil exactly
for a `measured` pass, so a caller that forgets to read the outcome ranges over
nothing instead of over a set that means the opposite of what it looks like. An
error — an unprepared session (`ErrProbeNotPrepared`), a malformed request, a
process that would not start, an unreadable log, a log the catalogue cannot
account for (`ErrProbeInconsistent`) — carries no result at all, for the same
reason. The pass also stops at the first binary that does not exit zero: the
indices the rest would append cannot be combined with a pass that has already
failed, and the result would be a subset of the truth wearing the shape of the
whole of it.

`ErrProbeInconsistent` is the one of those that is nobody's request and nothing
about the machine. The engine proves the shape of the raw log — strictly
ascending, inside the catalogue — before it drops the rejected mutants' indices,
and proves the survivors are `Probed` afterwards; the order matters, because the
filter would otherwise swallow an index past the end of the catalogue as though
it were an ordinary rejection. Anything either check refuses is go-mutants
contradicting itself, and it is reported rather than repaired: a repaired set
would be handed to a consumer as a measurement, and a measurement is a licence
to skip executions.

A **missing** log after a clean exit is the one absence that is a fact, and it
is the empty set rather than a failure. The runtime writes its header in `init`,
before any test code runs, so a binary that produced no file is a binary that
never linked a probe — and one that never linked a probe ran no probed site. An
*existing* log that `ReadInfectionLog` refuses is `GOM7517` and no facts.

`Mutant.Probed` says which mutants the tree speaks for, and it is the field a
consumer is likeliest to misread. It is the conjunction of three things: the
mutant has a probe form, its site survived the probe tree's validation, and the
*mutant tree's* validation accepted the mutant itself. None of the three is
enough on its own — a mutant with no form leaves its file untouched, so the
probe tree compiles and the validation **accepts** it exactly as it accepts a
probed one, and a caller reading "accepted" as "probed" would take its permanent
absence from every log as licence to skip the tests that kill it.
`instrument.Hints.Probes` answers the half only that package can, and it is
`probeFor` and nothing beside it, so a form added there is answered for here
with no second list to keep in step.

The third clause is about two independent passes over two trees. The probe
rewrite at a site is a different edit from the mutation there, and often a
smaller one, so a site whose probe compiles while its mutant does not is an
ordinary outcome rather than a contradiction. But a rejected mutant is never
executed, and `Probed` is read as a fact about the executions a consumer may
skip — so on a mutant the mutant tree rejected it is false, whatever the probe
tree made of the site. **`Probed` implies `Accepted`.**

So the consumer's rule has two clauses, and dropping either one is unsound:

> Skip executing test `t` against mutant `m` only when `m.Probed` is true and a
> `measured` probe of `t` does not name `m.Index`. An unprobed mutant is absent
> from every measurement there will ever be, so treat it as infected by every
> test.

`Probed` is session-local, like the rest of the engine API's live values: it
appears in no report, in no schema, and in no `go-mutants list --json` document,
because it describes a tree that exists for as long as the session does.

## Compile validation

Status: implemented in `internal/validate`. Instrumentation is a byte rewrite
that leaves typing to the compiler, so a few guarded sites cannot compile — a
mutated copy can be a program the compiler refuses, as `x * 0` swapped into
`x / 0` is a constant division by zero. The compiler is this phase's oracle,
and the phase's job is to ask it precisely enough that one bad candidate costs
one candidate rather than a file or a run.

- **The fast path is one build.** Every catalogued mutant is spliced in at
  once and `go build ./...` accepts the lot. That is the ordinary case, and
  making it ordinary is the whole point of the schemata design.
- **A red build starts a bisection.** Every catalogued file is restored to its
  pristine bytes and rebuilt first: a failure there is not mutant-induced and
  stops the run rather than blaming whichever candidate was tested first. The
  files the compiler named are then searched one at a time — halving while
  halving is cheaper than scanning, verifying every join, and falling back to
  a scan when a join fails, so a pair of candidates that only fail together is
  an ordinary case rather than a wrong answer.
- **The generated runtime is never regenerated.** Its activation array is
  sized by the full catalog and every guard spells its own dense index, so a
  runtime rebuilt from a subset would renumber flags that other files read.
- **Rejections are data, not failures.** A candidate that will not compile
  comes back as a `rejected[]` entry carrying its identity, its coordinates,
  and the compiler's own words, captured at the moment of rejection — by the
  time the phase finishes the tree compiles and that message exists nowhere
  else. Dropping such a candidate silently would quietly shrink the catalog
  between runs with no record of what left it.

## Execution

Status: implemented. The whole pipeline runs — snapshot, baseline, discovery,
compile validation, the instrumented baseline, the drift gate, per-mutant
activation, the report, and the exit code — so `go-mutants run` measures a real
mutation score today, narrowed by coverage, by the selection stage below, and by
the outcome cache.

- **The test command is a scope.** *Implemented.* A `test.command` of `go test`
  followed only by package patterns is read as the set of packages the project
  measures itself with, and only those packages get a test binary — so a module
  whose tests live in three of forty packages starts three processes per mutant
  instead of forty. The patterns reach `go list` verbatim, because the go
  command's pattern vocabulary is the one the user wrote the command in.
  Recognition is spelling-strict: `go`, `test`, then `.` or anything under `./`
  with no `..` in it, and nothing else. A flag of any kind, a bare import path,
  a `..` in any position — including one that climbs out of the tree and back
  in, which resolves differently from the snapshot than from the workspace — a
  Windows `.\internal\...`, or another program is unrecognised, and an
  unrecognised command behaves exactly as before — every binary, every mutant,
  and a `GOM7601` warning. There is no shortlist of harmless flags, because
  `-run`, `-tags` and `-race` each change what a `go test` means and the failure
  is silent in the direction that costs a kill.

  A scope that resolves to nothing is `GOM4022` and stops the run: a pattern
  that places no package directory (which the go command answers with a warning
  and exit zero, so nothing else would notice), or a whole scope with no test
  file in it. A pattern naming a directory that exists but holds no Go files is
  left to the baseline a moment later, which runs the user's command verbatim
  and gets the go command's own "no Go files in ..." — the same reason a package
  that does not compile is a build failure rather than a scope error.
  It is the one part of this story that does not fail open, because there is no
  open direction — widening back to `./...` runs the suites the command
  excludes, and running nothing reports every mutant as having survived a suite
  that never started. Every pattern is resolved with one `go list -e` before the
  baseline is measured, so a typo costs a second rather than a full pipeline;
  `-e` is what keeps a package that does not compile from being mistaken for a
  pattern that names nothing.
- **One build.** Each package in the scope that has tests is compiled once with
  `go test -c`;
  packages with no test files are skipped. `-cover -coverpkg=<module>/...` is
  added whenever coverage-guided selection is on, and the same binaries serve
  both the profiling pass and every mutant — there is no second, non-cover
  build. That is not free: a `-cover` test binary runs its coverage teardown on
  every exit whatever `-test.gocoverdir` says, which measured at roughly 6 ms
  per run on a three-file fixture and 8-16 ms on this repository's own
  `internal/mutation` binary. Two builds of one tree would cost more.
  `--race`, when requested, applies to that build and to the baseline so the
  derived timeout stays consistent.
- **Vet off, on the instrumented tree only.** A Form C guard splices every
  mutant of an expression in beside the original, so the snapshot legitimately
  holds `s == "." && s == ".."` — which is what vet's `bools` analyzer exists to
  report, and `go test` and `go test -c` run it by default. The two commands
  issued against the instrumented tree, the instrumented baseline and the
  `go test -c` above, therefore get `-vet=off` merged into `GOFLAGS`
  (`gocmd.AppendGoflags`, so an inherited `-mod=readonly` survives). The
  *pristine* baseline keeps vet at its default, which is the whole scope
  argument: a real `bools` finding in the user's own source still stops the run
  before anything is instrumented, and what is suppressed is an analyzer's
  opinion of a rewrite rather than of them. `go build` and `go list` do not
  define the flag and are unaffected.
- **Direct binary launch.** Test binaries are executed directly, bypassing the
  `go test` result cache entirely, with the working directory set to the
  package directory inside the snapshot so `testdata` paths behave.
- **Every subprocess is recorded at the runner.** *The choke point records; the
  labels and the sink are not wired yet.* `runner.Run` records exactly one
  `exec` event per call into whatever recorder its `Spec.Trace` names — argv,
  directory, environment *names*, timeout, exit code, duration, and the size
  and SHA-256 of the retained output — after the child has been reaped, and
  hands back the sequence it was recorded at as `Result.TraceSeq`. Today every
  production call site still passes a nil recorder and only the `go version`
  probe names a kind; the engine's labels, the options that carry a recorder
  down to them, and the sink that writes a recording out arrive with the trace
  flag. Processes are started from
  a dozen places (the `go version` probe, both baselines, a compile per
  package, the coverage pass, each validation build, a run per mutant), and a
  rule that every one of them must remember to record would have a dozen
  chances to be broken silently in exactly the run somebody is trying to
  diagnose. Recorded at the choke point, a call site can only forget to
  *label* its command with a `Spec.Kind`, which the schema's `kind` enum turns
  into a recording that does not validate. A refused spec and a command that
  could not be started are recorded too, with `exit_code: -1` and the refusal
  as the event's `error`: a command that never became a process is precisely
  what a reader needs to be told. Every `runner.Error` — and every
  `gocmd.Error` from a failed version probe, together with the output that
  probe produced — carries the `Invocation` it was about, so a failure that
  travelled up three layers can still say which command it was, where it ran,
  and where the recording kept its output. The layers above carry it on:
  `engine.Error`, `execute.Error` and `validate.Error` answer the same
  `Command()` and `RetainedOutput()` pair, reusing the invocation the runner
  already attached rather than describing the command a second time, and
  `internal/cli` walks the cause chain for whichever error carries them and
  prints `command:`, `dir:` and the output tail under the coded message. A nil
  recorder records nothing and costs a zero `TraceSeq`, so the traced and the
  untraced paths are one path.
- **One shared snapshot.** Activation is per-process, so N workers share it. A
  test that writes into its package directory is caught by re-digesting the
  manifest after the instrumented baseline; drift is exit 2 with the offending
  files listed. `--isolate` is reserved as the per-worker escape hatch.
- **Timeouts.** Explicit, or `max(10s, slowest baseline × 5)` over the baseline
  runs after the first, which is the one that compiles — or over the only run
  when there is one. A first timeout
  is not evidence: N test binaries on a loaded machine produce timeouts that
  say nothing about the mutant, and counting one as a detection would inflate
  the score exactly when the run is least able to notice. So every timed-out
  mutant is held back and retried **serially** after the queue drains, with
  nothing else running. Two in a row are a confirmed detection; a retry that
  finishes — pass or fail — is `inconclusive`, which counts in neither
  direction. Both attempts are kept in the report. Process trees are killed
  through a Windows Job Object with
  `KILL_ON_JOB_CLOSE` (fail-closed if ownership cannot be established) or a
  POSIX process group `TERM` then `KILL`.
- **Memory bounds.** *Implemented.* Explicit `test.memory`, or
  `max(1GiB, largest baseline peak × 4)` from the same runs the timeout is
  derived from. It exists because a deadline does not bound a program that
  allocates: `negate-loop-condition` turning a terminating loop into one that
  appends forever reached eleven gigabytes in twelve seconds against this
  repository's own `internal/config`, and took a CI runner down before its
  ten-second timeout could expire — see
  [ADR 0009](adr/0009-a-mutant-is-bounded-in-memory-as-in-time.md). While a
  bounded child runs, `runner` samples the tree every 100 ms — the *proportional*
  set size on Linux, so a page shared between a fuzz coordinator and its workers
  is counted once rather than once each — and the first sample **strictly above**
  the limit kills it, with `Result.MemoryExceeded` rather than
  `Result.TimedOut` — and without the SIGTERM grace a timeout gets,
  because the evidence a memory kill rests on is the peak and that is already
  recorded, while two seconds of politeness for a tree that is already over
  budget is hundreds of megabytes more of what the bound exists to prevent.
  Windows also carries `JOB_OBJECT_LIMIT_JOB_MEMORY` on the job so the kernel
  holds the line under the sampler, set a quarter *above* the sampler's line:
  the flag caps the job's own accounting at its limit, so a kernel line equal to
  the sampler's would make the sampler unable to ever read a number above it.
  Rlimits are not used: RLIMIT_AS bounds address space, of which the Go runtime
  reserves hundreds of gigabytes before allocating anything, RLIMIT_DATA is
  Linux-only, and neither reaches the child's own children.

  A derived bound is sound only for a run of the baseline's shape, so a **fuzz
  target gets none**: `go test -fuzz` is a coordinator plus a worker process per
  core, each mapping the same 100 MiB region the fuzzing engine communicates
  through, and a bound derived from one process running the suite once would
  kill it for being what it is. A limit the caller names still applies.

  A mutant the bound stops is **`killed`**, with `memory_exceeded` and
  `peak_memory_bytes` beside the outcome on its execution row, in its `mutant-exec`
  record and in the console's `-v` line. It is not retried the way a timeout is:
  a timeout may be the machine being busy, and a bound four times what the whole
  unmutated suite needed is not. The baseline itself runs unbounded, because it
  is the measurement the bound is derived from; every prepared test binary the
  run starts afterwards — mutant, probe, control, coverage pass — is bounded, so
  a measurement and what it is compared against had the same machine. Sampling a
  live tree needs `/proc` or the job object, so macOS reports a peak, records an
  explicit `test.memory` in the report because that is what the user asked for,
  enforces neither it nor a derived one, and says so once as `GOM4047`.

  The bound is not in the outcome cache key — a derived bound follows the
  baseline peak, and keying on it would give every machine a cache of its own —
  so it is recorded on the entry and judged on every lookup, exactly as the
  timeout is. An entry killed *by* the bound is evidence about that bound and
  any tighter one; an entry that reached a verdict inside a bound is not
  evidence about a smaller one, which might have killed it first. Without that
  rule a run at 256 MiB would cache `killed` and a run at 8 GiB would adopt it.
  See `cache.Entry.UsableWithin`.
- **Coverage-guided selection.** *Implemented.* The test binaries are built
  with `-cover -coverpkg=<module>/...` and each is then run once with nothing
  activated and `-test.gocoverdir` pointed at a directory of its own — the
  flag, never the `GOCOVERDIR` environment variable, which a *test* binary does
  not read — and an inherited `GOCOVERDIR` is stripped from every child
  environment go-mutants composes, so a run started underneath somebody else's
  coverage collection cannot append into it. `go tool covdata textfmt` blocks
  are mapped to mutants by
  line-interval overlap only: columns describe the instrumented text while a
  mutant's span was measured against the user's own bytes, and only the lines
  agree. The over-approximation errs toward running a binary rather than
  missing a kill. A mutant no binary reaches is not executed at all and is
  reported as `survived (uncovered)`.

  **Narrowing to tests.** By default (`test.narrowing = "test"`) the pass goes
  one step finer than the binary: it profiles every test on its own —
  `-test.list` names them, and each runs under `-test.run=^(name)$` with a
  coverage directory of its own — and runs each mutant against only the tests
  whose profile reaches its lines, the binary started with those tests
  selected. Two things keep that sound, and [ADR 0010](adr/0010-narrowing-to-tests-is-sound.md)
  is the whole argument. A test that does not pass on its own is order-dependent
  and cannot be isolated: it is named in a `GOM7603` warning and left out, and
  its binary is run whole for every mutant it reaches, so nothing it covers is
  lost. And a set of tests that each pass alone but fail *together* without a
  mutant is checked with a control — the same tests, nothing activated — before
  any kill is trusted; a set whose control fails is named in a `GOM7604`
  warning and its mutants are measured against the whole binary. Because the
  outcome does not depend on which mode ran, the outcome cache does not key on
  it, and `test.narrowing = "package"` selects the coarser binary-level mapping
  for a project that prefers it. Neither narrows what is *measured*: every mode
  measures every mutant, and only how much of the suite each mutant is measured
  against differs.

  Two rules bound the whole optimisation. Narrowing is auto-on exactly when
  `test.command` is one
  go-mutants can read as a scope — `go test` over package patterns, the built-in
  `go test ./...` included — and off with a `GOM7601` warning for anything else,
  because an opaque command's coverage cannot be attributed to go-mutants' own
  per-package binaries. A scoped command narrows what the mapping is *over* and
  changes nothing about what it means: the binaries are the ones the user's own
  command runs, so a mutant none of them reaches is an uncovered survivor, which
  is the same answer the run would reach by executing every scoped binary
  against it and watching them all pass. And every failure of the pass —
  including a `-cover` build that will not compile — publishes a `GOM7602`
  warning and runs everything, so the optimisation can never fail a run.
- **`--changed [=<ref>]`.** *Implemented.* It intersects candidates with the
  `git diff -U0` line set taken against `git merge-base <ref> HEAD`, unioned
  with every untracked, unignored file
  (`git ls-files --others --exclude-standard`) as the whole of itself — a file
  with no index entry has nothing to be diffed against, so the diff alone would
  see an edited line and miss a file written from scratch. It
  is read from the *original* workspace — a snapshot excludes `.git`, so there
  is no repository in one. Bare `--changed`, and `--changed=@{upstream}` written
  out longhand, both follow the upstream of `HEAD` and record it by name; a
  branch with no upstream is `GOM7712` rather than a merge base that cannot
  resolve. Discovery and validation still run over the whole module, so the IDs
  and `rejected[]` match a full run's and the two documents can be compared
  mutant for mutant. Unlike coverage guidance it fails closed: a diff that
  cannot be read stops the run, because a narrowing that silently measured
  everything or nothing is worse than not running at all.
- **`--shard K/N`.** *Implemented.* It assigns by `sha256(ID)[:8] % N + 1`,
  published as `shard.assignment: "id-hash-v1"`, so adding or removing mutants
  elsewhere does not reshuffle a shard. Each shard emits a complete report with
  the other shards' mutants marked `not-run` with
  `not_run_reason: "other-shard"`, and
  `report merge` verifies congruence (tool version, workspace digest, module
  path, catalog ID sequence, changed ref, matching `N`, every index exactly
  once, and every row owned by the shard that reported it) before merging; a
  mismatch is exit 2 naming the first discrepancy. The two compose: a shard of a
  `--changed` run narrows by both, reports `mode: "shard"` with a `changed_ref`,
  and merges into a `changed` document.
- **Everything not executed says why.** *Implemented.*
  `mutants[].not_run_reason` is `out-of-selection`, `other-shard`, or
  `interrupted`, and is `null` for every mutant that was measured — which is
  what keeps a narrowed run's report a complete statement about the catalogue
  rather than a fragment of one.

## Stable identity

Status: implemented. A mutant ID is a SHA-256 over length-prefixed fields: the
normalized relative path, the versioned rule name, the byte span, the source
digest, and the digests of the original and replacement bytes. Absolute paths
and snapshot locations never participate. The CLI shows a collision-checked
20-hex prefix; JSON always carries the full identity.

## Score and exit policy

Status: implemented. The score function and the exit-code mapping live in
`internal/mutation`, and the run feeds them from the report it just wrote, so
the number a user reads and the gate that failed cannot disagree.

```text
score = (killed + confirmed_timeouts) / denominator
```

The denominator excludes expected survivors, inconclusive results, errors, and
not-run mutants — every category that is a signal about the run rather than
about the tests. It is `null`, printed as `score N/A`, when that denominator is
zero: both plausible sentinels are lies, since 0 reads as "your tests caught
nothing" and 100 as "your tests caught everything" when the truth is that
nothing was measured. Exit codes are 0, 1 (opt-in policy failure only), 2
(infrastructure, config, baseline, stale expectation), 130, and 143.

## Reporting and the event stream

Status: implemented. The event stream, both console renderers, `RunReport v1`,
its history store, `report merge`, the Stryker projection, and the
self-contained HTML report all exist and carry a whole run today. The
engine never draws. It publishes to a single `chan engine.Event` (a sealed
interface): `RunPlanned`, `PhaseChanged`, `PhaseCompleted`, `BaselineProgress`,
`BaselineCompleted`, `Discovered`, `Validated`, `CoverageMapped`,
`MutantStarted`, `MutantFinished`, `CacheHit`, `Warning`, `Traced`,
`ReportPublished` (only after the atomic rename), and a terminating
`RunCompleted`. `CoverageMapped` is published only by a run that narrowed
itself; one with coverage off publishes the `GOM76xx` `Warning` saying why
instead. A `CacheHit` is the accounting for one mutant answered from the cache,
and the `MutantFinished` carrying the outcome follows it immediately — with no
`MutantStarted` before either, because nothing started. Publishing both is what
keeps a renderer's counts and the report's in step, exactly as an uncovered
mutant's lone `MutantFinished` does. Every `PhaseChanged` is answered by exactly
one `PhaseCompleted` carrying that phase's duration, before the next phase is
announced and — for the last phase of a run — before `RunCompleted`, on the
failure and interruption paths as well. A `Renderer` interface has two
implementations: the bubbletea dashboard and deterministic plain lines. `Traced`
and `PhaseCompleted` are rendered by neither at the default verbosity — they are
the run's account of itself rather than its findings — and the plain renderer's
output is byte-identical whether or not a run was traced. The TUI is selected only
when standard output is a terminal that can do better than ASCII and
`--no-tui`, `--json`, `--quiet`, `--no-color`, `NO_COLOR`, and `CI` all say
otherwise; anything else gets the plain lines. The final summary is
byte-identical between the two: a dashboard run replays its warnings and its
closing block through the plain renderer itself, once the alternate screen has
been restored.

A run also accounts for itself into `engine.Options.TraceSink`, as the
`gomutants-trace-v1` stream `trace` defines: every phase and stage, every
subprocess under a label, the snapshot, the sweep, one coverage decision per
mapped mutant, every cache lookup and write-back, each file written, and every
warning. A nil sink is the disabled trace, and it is what the command line
passes today — `--trace` is a later change, and the report carries none of this
yet. The engine records unconditionally into a nil `*trace.Recorder`, so a traced
run and an untraced one take the same path through the package and there is no
branch for a verdict to come to depend on. The sink belongs to the caller and the
engine never closes it. `engine.Traced` is that same stream published on the event
channel, only when `Options.PublishTrace` asks for it, through a bounded buffer
and one forwarding goroutine so that the recorder's lock is never held across a
send onto a channel a terminal is draining.

A recording is a diagnostic and never evidence: no trace option enters the cache
key, the workspace digest, the catalogue or a mutant id, and a sink that fails —
by returning an error or by panicking — costs the events and nothing else. See
[ADR 0001](adr/0001-trace-is-not-evidence.md) and
[the trace contract](trace-v1.md).

`RunReport v1` is the lossless source of truth; the Stryker projection and the
HTML report are one-way, deterministic derivations of it. History is kept
outside the workspace, under the OS cache directory at
`<cache>/go-mutants/workspaces/<key>/runs/<run-id>.json`, with `latest.json`
holding a whole copy of the newest rather than a name that could dangle — a
mutation run must not add files to the tree it is measuring, and
`runs/` then holds nothing but immutable per-run documents. Every write is
temp-file plus atomic rename, and `ReportPublished` is emitted only after the
rename succeeds. See
[JSON contracts](json-schema.md) and
[Stryker compatibility](stryker-compatibility.md).

## Outcome cache

Status: implemented. `internal/cache` files one small JSON document per mutant
under `<cache>/go-mutants/workspaces/<key>/outcomes/<context>/`, beside the run
history and under the same ownership marker, claimed through
`report.History.Claim` rather than through a second copy of the same dance.

The `<context>` is a SHA-256 over length-prefixed fields — the tool version, the
running executable's digest, the Go toolchain's own release, the workspace
digest, the catalogue digest, the test command, the configured timeout, and
`CGO_ENABLED`/`GOARCH`/`GODEBUG`/`GOEXPERIMENT`/`GOFLAGS`/`GOOS` — truncated to
16 hex characters. Entries are *filed* under it rather than validated against
it, which is why nothing is ever invalidated: an edit moves the key, so the old
entries become unreachable rather than wrong, and every entry carries the id and
the *full* key it was written under — not the truncation, which two colliding
contexts would agree about — so a truncation collision is a refusal instead of
an adoption.

The toolchain's release is in the key because nothing else in that list carries
it: the test command is hashed as the user wrote it, so the default command
hashes the word `go` rather than the toolchain substituted for it at exec time,
and `go.mod` pins a language version and not a patch release.

Two decisions are worth stating. The effective timeout is judged rather than
keyed on: a derived bound is `max(10s, slowest baseline × 5)` over the runs
after the first (the only run, when there is one), a wall-clock
measurement, so hashing it would have given every run of a non-trivial project
its own empty directory; each entry records the bound it was measured under and
a lookup refuses one that bound could not have produced. And the partition runs
*after* coverage narrowing, so a mutant no test covers is settled before the
cache is consulted — the coverage pass fails open, and a cached
`survived (uncovered)` adopted by a run that would have executed the mutant
would be a detection nobody performed.

Only killed, survived, and confirmed timed-out are stored; inconclusive results,
harness errors, interruptions, and every mutant named in `[[mutation.expect]]`
are measured on every invocation. Every failure in the stage is a `GOM79xx`
warning and a run that measures more than it had to, exactly as coverage fails
open — the exception being `cache status|gc|clean`, where operating on the cache
is the whole of what was asked for and a failure is an error.

## Package layout

| Package | Responsibility | Status |
| --- | --- | --- |
| module root (`gomutants`) | Public frozen-workspace and reusable-session API | implemented |
| `cmd/go-mutants` | Thin main | implemented |
| `internal/cli` | cobra tree, flag validation, GOM errors, exit codes | `run`, `list` |
| `internal/config` | Strict TOML decode and precedence merge | implemented |
| `internal/mutation` | Pure: spans, stable IDs, rules, catalog, score | implemented |
| `internal/interval` | Pure: interval forest | implemented |
| `internal/glob` | Pure: `**` glob semantics, fuzzed | implemented |
| `internal/discover` | `packages.Load`, types walk, candidates, skips with their sites | 2 families |
| `internal/instrument` | Forms S/C/D, flattener, runtime codegen, splicer | implemented |
| `internal/snapshot` | Manifest, digests, link rejection, cleanup | implemented |
| `internal/tempowner` | Temporary-directory lock, marker, and orphan sweep | implemented |
| `internal/gocmd` | `go build`, `go test -c`, `go tool covdata` | build, test |
| `internal/runner` | One process, timed, supervised and recorded; tree kill | implemented |
| `internal/coverage` | covdata textfmt parsing, line overlap mapping per binary and per test | implemented |
| `internal/cache` | Outcome cache: key, store, mode, `gc` | implemented |
| `internal/validate` | One build, then bisection; rejections with diagnostics | implemented |
| `internal/execute` | Test-binary build, profiling per binary and per test, scheduling, timeout retry | implemented |
| `internal/operatorselect` | Shared profile/family/rule selection | implemented |
| `internal/drift` | Shared instrumentation-aware snapshot drift gate | implemented |
| `internal/testflag` | Shared Go test-binary flag recognition | implemented |
| `internal/report` | RunReport, projections, HTML, history, merge | implemented |
| `internal/engine` | Orchestration, typestate pipeline, events | implemented |
| `internal/console` | Deterministic plain-line renderer | implemented |
| `internal/tui` | The bubbletea dashboard | implemented |
| `internal/schemas` | Embedded JSON Schemas, validation before writing | catalog, run report, doctor |
| `internal/testkit` | Module and fixture paths, tree copies, hermetic environment, toolchain lookup, child processes, golden files, helper subprocesses, clocks, the keep-on-failure policy and its dumps | test-only support |
| `internal/testkit/mutantkit` | Snapshots, the discover/catalogue/instrument sequence, mutant lookups, report marshalling and normalisation, a per-test trace recording, a scripted `go` command | test-only support |
| `internal/devtools/testcache` | The test-owned build cache and the kept scratch root: `path`, `status`, `clean`, `trim`, `exec` | developer tool |
| `vendor-assets` | The vendored viewer bundle and its digest check | implemented |

Pure packages have no filesystem or process access, which is what makes the
golden ID vectors and property tests meaningful.

`internal/testkit` is test-only in both directions, and a test enforces it.
Production code may never import it — that would link `testing`, and its flag
registrations, into `go-mutants` — and it may never import anything from this
module, so a pure package's unit tests can use it without pulling the engine in
behind them. Helpers that do need engine types live in `internal/testkit/mutantkit`
and are imported only from external test packages; the import gate treats that
tree as part of the harness, so it may import `internal/testkit` while nothing
outside the harness may import either.

`mutantkit.FakeGo` is the harness's answer to the other half of the toolchain
question. `mutantkit.Toolchain` locates the machine's real `go`, which is what a
test that wants to know whether a mutant is really killed needs; the fake is an
executable named `go` that re-executes the test binary and answers from a rule
table the test writes, which is what a test of what go-mutants does when the
toolchain misbehaves needs. A version probe that hangs, one that answers
garbage, a `go list` that refuses a pattern and a baseline suite that is red
were all either integration-tier tests costing minutes and a toolchain or no
test at all — a `go` that hangs cannot be installed. It also gives the unit tier
an assertion it never had: the call log is the argv, the working directory and
the composed `GOFLAGS`, `GOWORK`, `GOCACHE` and activation variables a child
process really received, so "the compile carries `-vet=off` and the listing does
not" is now a claim about a process rather than about a struct. Only nine
values are kept — `GOFLAGS`, `GOWORK`, `GOCACHE`, `GOTOOLCHAIN`, `GOENV`,
`GOMODCACHE`, `PATH` and the two activation variables, all of them flag lists or
paths; every other variable is logged by name alone, because a call log is
uploaded as a CI artifact. A call no rule matches is refused with exit 97 naming
the argv, so a test can never pass on a command nobody scripted, and the
package's `TestMain` dispatches through `mutantkit.Main` because the fake is the
test binary itself.

A scripted `go test -c -o X` produces the fake rather than an inert file, so the
binary the scheduler then starts answers the same rule table and a mutant can be
scripted killed or survived by exit status — `RunOne`, the per-worker scratch,
the `-test.timeout` the supervisor owns and the single `GO_MUTANTS_ACTIVE` it
sets all run with no toolchain. A whole run past the baseline still needs real
source, because discovery type-checks the module with go/packages. The binary is
installed once per test binary rather than once per fake, because it is a link
to the test binary and a Windows runner cannot link across the volumes its
temporary directory and build cache sit on; `mutantkit.Main` removes the shared
directory after the suite, and what a failing test's kept scratch holds is the
rule table and the call log. `Fake.Export`, the PATH form, forces
`testkit.ResolveToolchainDirectories` first: after it, every `go` this process
starts is the fake, and the harness's own lazy `go env` probe would otherwise
reach it in some orderings and not others.

The tier ledger reads the fake as the opposite of driving a toolchain.
`TestEveryToolchainDrivingTestIsIntegrationTagged` scans every `_test.go` for
the calls that start a `go` command and requires each one to carry
`//go:build integration` or to be named in
`internal/testkit/testdata/unit-toolchain-allowlist.txt`; a file that constructs
a fake is exempt, because it supplies the toolchain rather than reaching for
one. That is what let `internal/gocmd` leave the ledger: its unit tier scripts
every misbehaviour, and the four claims that are about a real `go` moved to
`internal/gocmd/toolchain_integration_test.go`. The exemption is per file, so a
file may not do both — which is why the split is a second file rather than a
build tag on a function.

That rule is why the two directories the harness owns outside a temporary one —
the test-owned build cache and the kept scratch root — and the names of the
ownership markers that licence emptying them are written down twice: once in
`internal/testkit` for the suites, and once in `internal/devtools/testcache` —
a production `main`, which may not import the harness. `TestPathAgreesWithTestkit`,
`TestKeptRootAgreesWithTestkit` and `TestMarkerNamesAgreeWithTestcache` run the
tool and compare what it prints with what the harness resolved, so the copies
cannot drift apart in silence. Drift would not fail anywhere else: the suites
would fill one directory and the collector would empty another.

What a failing test leaves behind is a policy rather than a habit.
`GO_MUTANTS_TEST_KEEP` is unset locally, so `testkit.Scratch` is `t.TempDir` and
nothing changes; set to `1` it keeps the directories of a test that failed, and
to `always` it keeps every test's. A kept directory carries `KEPT.txt` — the
test, the fixture, the toolchain, the build cache, the test's other kept
directories, and every child it ran through `testkit.Exec` — plus whatever
`testkit.DumpFiles` was pointed at (the instrumented source, for a snapshot),
one numbered directory per dump, and the recording as `trace.jsonl` in the
encoding `trace validate` reads. The recording is what accounts for the
commands `KEPT.txt` cannot: the suites drive `go` through `internal/runner`
rather than through the harness, so the engine and validate suites attach
`mutantkit.TraceSink`/`mutantkit.Trace` by default and a failure logs the tail
of it. Every CI test
job sets the policy and uploads the root on a failure, so a red build arrives
with its evidence attached; the dogfood job deliberately does not, because a
mutation run fails this repository's own tests thousands of times on purpose.
`GO_MUTANTS_TEST_FORCE_FAIL=<test name>` fails one named test, which is how to
see any of it without breaking something.

The `-update` flag for golden files is registered once, in `internal/testkit`,
and is therefore the same flag in every test binary that links the harness.
`mise run golden-update` names the packages holding goldens explicitly, and
`TestGoldenPackagesAreNamedByTheUpdateTask` fails when that list goes stale. It
is two commands rather than one because `internal/engine`'s goldens are run
reports of real runs and the test that records them carries
`//go:build integration`; a command without the tag would compile a package with
no golden test in it, pass, and rewrite nothing, so
`TestGoldenUpdateTaskHandlesTaggedPackages` requires every integration-only
golden package to be named in a command that passes the tag.

### The corpus

`fixtures/` is the corpus: the small modules the suites run against, each its own
module so that this repository's own `./...` never compiles one — a fixture that
fails on purpose would otherwise fail this repository's test run. Module paths
live under `fixture.example/`, which RFC 2606 reserves, so no fixture can collide
with something publishable and no `go get` of one can reach the network; nothing
has a `require`, so nothing needs `go.sum` or a module cache inside the snapshot,
and the integration tier never touches the network. `fixtures/README.md` is the
ledger: every fixture, what it is for, and the tests that drive it.

Most fixtures are about the operators or about one phase. Five are about the
edges of a workspace, and are what the instrumentation and the run have to get
right around an ordinary module: `workspace/` is a `go.work` over two modules —
measured one module at a time when pointed inside it, refused by `run` and by
`list` at its root before anything is copied, so that the `go.work` the refusal
names is the user's own rather than a snapshot's; `tagged/` puts one of its two
candidates behind `//go:build special`, so `GOFLAGS` changes both the catalogue
and the outcome cache's context; `untested/` is a package with tests beside one
without; `selfwriting/`'s suite writes into the package directory it runs in,
which is what the drift gate exists for; and `unnameable/` holds the site no
guard form can express. A CRLF workspace is the one member with no directory:
`.gitattributes` pins `* -text`, so a checked-in CRLF file would be CRLF on
every platform and would change every mutant identity that covers it, and the
module is synthesized by `testkit.NewModule(t).From("simple").CRLF()` instead.

Two gates keep the corpus what it is. `internal/testkit`'s
`TestCorpusConformance` reads the ledger and the directories and requires them to
agree — module paths, a `go` directive no newer than the toolchain in use, no
`require`/`replace`/`go.sum`, LF endings and SPDX headers everywhere, a ledger
row for every fixture and a fixture for every row, a test naming each one, and
nothing under `fixtures/` that is a report, a `.go-mutants*` state file or a
compiled binary. Every CI job that runs a suite then ends with
`git status --porcelain --ignored -- fixtures`, because the corpus is an input
and a suite that wrote where it reads cannot be trusted to have noticed.

## Documented v1 limitations

No `switch`/`select` case mutation, no cgo packages, no package-level `var`
initializers, no cross-`GOOS` matrix (a run describes the host configuration),
and `go.work` support limited to `use` directives inside the snapshot.
