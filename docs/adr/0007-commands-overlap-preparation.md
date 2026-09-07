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
after it, from the tree: the overlay replaces only the instrumented sources, and
the compiler reads every other file where it lies. A command writing there would
have its bytes compiled into the test binaries *and* recorded as the state the
session was prepared in, so `Session.Changes` would compare the tree against the
drift and report nothing. One re-digest between the last build and the published
session — `Stage: "test binaries"` — catches a write that is still there when
the build ends, which is every write a command *leaves*. It does not catch a
write that is made and undone while the compiler is between one file and the
next: a transient edit is compiled in and gone before the digest looks. The tree
is held shared during the build, and shared is what a command holds too, so
nothing in this design excludes it; holding the tree exclusively for the build
would exclude commands from the longest phase of a preparation, which is the
one thing this decision exists to stop. The rule a consumer is held to is
therefore the one stated at the top: a command must not write the frozen tree.
The checks are the net under that rule, and after the window the net has a gap
exactly one transient write wide. Closing it soundly means compiling from the
frozen manifest rather than from the tree — every source of the module mapped
through the overlay, so the compiler never reads the tree at all — and that is
the second residual below.

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

The build reads the tree, and that is the second residual. `Stage: "test
binaries"` catches a write a command leaves behind and not one it undoes before
the build ends. Compiling the test binaries from the frozen manifest — every
Go source, `go.mod` and `go.sum` mapped through the overlay to bytes the
preparation owns — would make the build's inputs exactly the manifest, whatever
a command does to the tree meanwhile, and would let the drift check after the
build go, since there would be nothing left for it to catch. That is
`internal/execute` work on the overlay `Prepare` already writes, and it is the
follow-up to this decision.
