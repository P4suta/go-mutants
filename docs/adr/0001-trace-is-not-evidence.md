<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0001 — A trace is not evidence

## Status

Accepted, 2026-09-06. Implemented by the `trace` package and
`schema/trace-v1.schema.json`; enforced by the wiring of every later phase that
records into them.

## Context

go-mutants produces a claim. A run report says which mutants existed, which of
them a test suite detected, and what the run was measured against; the score, the
exit code and every projection are views of that one document. That is why the
report is fail-closed — a document that would not validate is never written, and
a workspace that changed underneath a run ends the run rather than producing a
report about a program that no longer exists.

Execution tracing introduces a second kind of output with the opposite failure
profile. A recording is diagnostic: a developer reads it to find out why a run
behaved the way it did, usually on a machine where it misbehaves and often on a
CI runner they cannot attach a debugger to. Nothing in it is a claim about the
software under test.

Applying the report's discipline to that output means a full disk, a read-only
directory, or a sink that could not encode one event ends a run that was
otherwise sound. That inverts the point of the feature: a diagnostic that makes
the tool less reliable than it was without it. And it has a sharper form here
than in most tools, because a go-mutants run *digests its own workspace*. A
recording written where the snapshot reads grows while the run reads it, so the
frozen digest stops matching and the run dies of drift it caused itself.

But "best effort" is where diagnostics usually start lying. A recording that
silently dropped a third of its events, or stopped early without saying so,
sends a reader hunting for the absence of a command that did in fact run.

## Decision

A trace is the diagnostic account of a run and is never evidence. Concretely:

1. **It takes no part in any claim.** No trace option enters `cache.Context`,
   `config.Config`, the workspace digest, the catalogue, or a mutant identity, so
   a traced and an untraced run of one workspace share a cache key and reach the
   same verdict. Nothing a run decides may depend on whether it is being
   recorded — which is also what makes a recording worth reading, since a
   recording that changed the run would be describing a different run.
2. **It never costs the run.** Sink failures are counted, never returned to the
   run. A directory that cannot be created or opened, and a close that fails,
   cost one `note` and a run that continues with a recording in memory.
3. **The disabled recorder is nil.** `trace.New` returns a nil `*Recorder` for a
   nil sink and every method is nil-receiver safe, so call sites record
   unconditionally. That is not a convenience: a branch on whether a trace was
   asked for is a place for the traced and the untraced path to diverge, and this
   ADR's first point is exactly the property such a branch would break.
4. **Honesty replaces fail-closed as the discipline.** Every sink counts its
   drops, every recording ends with `events_emitted` and `events_dropped`, and
   every line is written as it is produced rather than buffered until
   shutdown. A reader can always tell a complete recording from a lossy one,
   and a killed run from a finished one.
5. **A trace is secret safe.** An `exec` event records environment variable
   *names* alone, and the recorder — not its callers — is what reduces an entry
   to its name, so no future call site can leak a value by passing one. The
   schema refuses an `env_names` item containing `=` as a backstop.
6. **Output is preserved beside the stream, never inside it.** Captured output is
   the developer's own test output, so it is kept rather than filtered; but it is
   digested into the event and written to `output/<seq>.txt`, so an event's size
   does not depend on what a test printed, a bounded ring stays bounded, and a
   reader may attach a recording with the `output/` directory removed.

## Consequences

- A trace can never be cited as proof of anything. "Did this run really execute
  that binary" is answered by the report; the trace only says what the run
  appeared to do while it did it.
- A reader has two things to check before trusting a recording: that its last
  line is a `run-end`, and that `events_dropped` is zero. That obligation is the
  price of never failing the run, and it is why the accounting is a required
  field rather than an optional one.
- Because trace options are outside cache identity, tracing a warm run records
  the cache hit rather than the work the cached result stands for. Diagnosing the
  work means invalidating the entry, not adding a flag.
- A user who asks for a recording they do not get is told once, in a note, and
  gets their report and their exit code. A workflow that must have a recording
  has to check for the directory itself; the exit code will not tell it.
- Recording unconditionally costs something on every run: a bounded ring, and a
  digest of every captured output. That is the price of being able to explain the
  failure nobody passed a flag for, and it is bounded by construction rather than
  by how long the run was.
- A recording is written where the snapshot does not read. The trace root is
  therefore not a free choice, and the phase that adds the flag has to refuse the
  directories that would fail the run — a refusal that is a feature of this
  decision rather than fastidiousness about paths.
