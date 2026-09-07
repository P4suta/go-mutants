<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0007. Workspace commands overlap preparation outside the instrumentation window

## Status

Accepted.

## Context

`Workspace.Prepare` held the workspace's lock exclusively for its whole
duration, so no `Workspace.Exec` could start while a preparation was running. A
preparation of a real module is minutes: a discovery pass, a compile validation
of every mutant, a verification run of the module's own suite, and the test
binaries. All of it was time during which a consumer could run nothing of its
own.

The consumers that needed to run something anyway did the only thing left. Their
`go vet`, their `go build` and their baseline went into a **second workspace**
over the same root — a second snapshot of the module, a second toolchain probe,
a second discovery pass and a second compile of everything — in order to run,
concurrently, work whose whole purpose was to be about the tree the first
workspace had already frozen.

The lock was that wide for one reason, and the reason is real for only part of
the call. `main_validation` instruments the sources of the frozen tree **in
place** and `main_restoration` puts them back; between the two, the files on
disk are a program nobody wrote, and a command compiled from them would be
measuring something that does not exist. Everywhere else — discovery, the probe
copy, verification, the binary build, the probe build — a preparation reads
exactly the bytes a command reads.

## Decision

A preparation holds the tree against commands only for the stretch that rewrites
it, and holds the workspace's lifetime shared for all of it.

Three locks. No call takes all three but the two that must, and whenever a call
holds more than one, the outer is the earlier in this list — `mu` before `tree`
before `stateMu`. `Prepare`'s claim and `Close` hold `mu` and then `stateMu`,
with no `tree` between; `Exec`'s re-check under the tree, and a window that
publishes its failure before it unlocks, hold `mu`, then `tree`, then `stateMu`:

- `mu` guards the workspace's lifetime — its snapshot, scratch directory and
  toolchain. `Exec` and `Prepare` hold it **shared** for the whole of their
  calls; `Close` holds it exclusively and therefore waits for both.
- `tree` guards the snapshot's bytes. `Exec` holds it shared while its child
  runs, and re-checks the lifecycle once it holds it; `Prepare` holds it
  **exclusively** from the integrity gate through `main_validation`,
  `main_restoration` and the drift check that follows them, and publishes a
  failure of any of those before it unlocks. That stretch is the
  *instrumentation window*.
- `stateMu` guards the four fields that say what has become of the workspace:
  closed, a preparation claimed, a preparation failed, and the session. They
  need a lock of their own because the calls that read and write them now hold
  no more than the shared half of `mu`.

Four consequences fall out and are part of the decision rather than beside it.

**A second `Prepare` is refused immediately.** The claim is taken under `stateMu`
before the call reads anything, so a second caller is told the workspace is
spent instead of waiting out a preparation it was never going to be allowed to
make. It was the exclusive lock that used to refuse it, minutes later. The cost
is that a `Prepare` refused for an *option* — which hands the claim back, so the
caller may retry — refuses a genuinely concurrent second `Prepare` for the
instant it holds the claim, where the old exclusive lock would have made that
one wait and then succeed.

**A failed window is published before it is unlocked.** A window can fail:
validation breaks, the context is cancelled, restoration cannot write. The
unlock that gives up on it is the same unlock that wakes every command queued
behind it, and the tree those commands wake into still holds the instrumented
sources. So the failure is recorded under `stateMu` from *inside* the window,
and `Workspace.Exec` re-asks whether it is still allowed to run once it holds
the tree. That is the one place `stateMu` is taken while `tree` is held, and it
is why the lock order is `mu`, then `tree`, then `stateMu`.

**A preparation that published no session failed.** The state is recorded rather
than inferred, and a recorded state has to be recorded on *every* path out —
including a `panic`, which unwinds with the returned error still nil. A
consumer's `PrepareOptions.Trace` callback is ordinary Go code and ordinary Go
code panics. So what the workspace asks on the way out is "did this call publish
a session", which only the successful path answers yes to; the inferred rule it
replaced was panic-safe by construction and this one has to be made so on
purpose.

**What discovery read is checked against the manifest.** Discovery now runs while
a command may write, so a command that changed a source file *and put it back*
before the gate leaves the gate nothing to find while the catalogue it produced
identifies mutants by the digest of bytes that are nowhere on disk. Once the
tree is held exclusively, every file discovery *read* —
`discover.Result.SourceDigests`, which covers the files that yielded no mutant
as well as the ones that did — and the bytes captured for restoration must equal
the manifest entry the snapshot froze. A mismatch is a `DriftError` with
`Stage: "discovery"`, naming the files.

## Consequences

A consumer runs `go vet`, `go build` and its own baseline against the workspace
it is already preparing. goatest's second workspace, and the snapshot and
discovery pass under it, go away.

A command issued inside the window waits for it rather than being refused, and a
command already running when a preparation reaches the gate makes the
preparation wait: a long baseline delays instrumentation and cannot corrupt it.
A `PrepareOptions.Trace` callback may not start a command — it runs on the
preparation's own goroutine, which is holding what the command would wait for.

The integrity gate moved from before discovery to the top of the window, so the
same refusal for a tree a command had already changed now arrives after
discovery has run. Two things follow. A drift that stops discovery itself — a
command that left a source file that does not parse — is reported as a discovery
failure rather than as drift, which names a worse suspect for the same tree. And
a preparation asked for a probe tree copies it before the gate, so drift there
is refused by the probe snapshot's own digest comparison instead. Both refuse;
the message differs.

The window is not the end of the preparation, and the binaries are compiled
after it. They are compiled from the **manifest**, not from the tree. At the top
of the window — under the exclusive lock, from a tree the integrity gate has
just proved is byte-identical to the manifest — every file the snapshot froze is
copied into a directory the preparation owns, and the overlay names all of them;
the instrumented sources keep their own mapping and win where the two meet. So
the compiler reads no *frozen file* off the disk, and a command rewriting one
while the binaries compile changes the tree and nothing the session is made of,
whether it leaves the write or undoes it between two of the compiler's reads.
What the go command still reads from the tree is the package directories
themselves, which is the residual three paragraphs down.

That is why there is no drift check between the last build and the published
session. There used to be one — `Stage: "test binaries"` — and it was the net
under a build that read the tree: it caught every write a command *left* and
could not catch a write made and undone while the compiler was between one file
and the next. With nothing of the tree reaching the compiler there is nothing
for it to protect, so the stage is gone, and `Session.Changes`'s baseline is the
manifest rather than a scan taken after the build. A write a command leaves
during the build is therefore reported by `Session.Changes` and refused by
nothing, exactly as a write after a successful `Prepare` is.

The overlay pins the bytes of every path the manifest names, and one thing it
cannot pin is a path the manifest does not name. `-overlay` replaces named files
and the `go` command still lists the real directory, so a **new** file a command
creates in a package directory while the binaries compile is seen by the
compiler. `Session.Changes` reports it as an addition, and the rule a consumer
is held to is the one stated at the top: a command must not write the frozen
tree. The checks are the net under that rule, and after the window the net now
has a gap exactly one *added* file wide rather than one transient write wide —
narrower, and made of the case a consumer has no reason to produce.

The check on what discovery read is total over what discovery read, which is a
wider set than the catalogue and deliberately so: a file a transient edit
emptied of everything mutable yields no candidate, so a check built on the
catalogue would never look at it. What no digest covers is a file no byte of
which was read — a test file, a generated one, one an include or exclude pattern
dropped — and none of those can move a mutant's identity, because no mutant was
minted from one.

The window is still exclusive, and that is the residual. It exists because
validation compiles a mutated program by writing it into the tree. A validation
that built through the instrumentation overlay — which is how the *session*
already runs the same sources — would need no exclusive window at all, and a
preparation would then never take the tree from a command. That is engine work
in `internal/validate`, not API work, and it is the next thing this design is
waiting for.

The second residual is closed. The build's inputs are exactly the manifest —
every Go source of every package, the test files, `go.mod`, `go.sum` and every
other file the snapshot froze, copied once per preparation and mapped through
the overlay `Prepare` already writes — so what a command does to the tree while
the binaries compile no longer reaches them, and the drift check after the build
is gone with the hazard it was under. The file set is the manifest whole rather
than a list of the extensions a build reads: a `//go:embed` can name any path in
the module, so a list would have to parse every source to be sure, and every
miss in it would be a file read off the disk with nothing saying so.

The price is one whole-tree copy per preparation, of exactly the snapshot's
bytes and kept for as long as the session, so a prepared workspace holds the
module twice over — three times with a probe tree. The time is proportional to
the tree and is paid inside the window, where a command waits for it: 679 files
and 7.7 MiB of this repository in 50-90 ms, and under a millisecond for a
fixture. It is recorded as the `freeze-build-inputs` trace stage rather than as
a new `PreparePhase`, because that vocabulary is goatest's and closed. A
preparation that gives up removes the copy, on the rule the probe tree already
follows, unless `KeepTemp` asked for it.
