<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0004 — Verbosity is a renderer, not a second source of truth

## Status

Accepted, 2026-09-07. Implemented by `engine.Options.PublishTrace`, the
`engine.Traced` event and the verbose renderer in `internal/cli` (#43), on the
recording ADR [0002](0002-every-subprocess-is-recorded-at-the-runner.md) laid
down.

## Context

`run -v` and `-vv` answer the two questions a summary line cannot: where did the
time go, and why did *this* mutant get that outcome. There are two ways to build
that, and the difference is not visible in the output.

The obvious one is to print more. A `if verbose { fmt.Fprintf(...) }` beside each
interesting moment is a day's work and reads fine on the first run. What it
actually creates is a second description of the run — one for the screen, one for
the recording — maintained in different files by different changes. They drift.
Within a release, a fact appears under `-v` and not in `--trace`, or a duration
is measured from a different point in each, and the two accounts of one run
disagree. Nothing fails; a reader simply cannot rely on either.

There is a sharper version of the same problem. Print statements are executed by
the run, so the run under `-vv` and the run without it are different programs:
different allocations, different lock hold times, different interleavings in a
scheduler that runs mutants concurrently. That is the property
[ADR 0001](0001-trace-is-not-evidence.md) spent its whole decision protecting,
undone by a flag.

By this point the run already had an account of itself, recorded unconditionally
and travelling through a channel a renderer was already draining.

## Decision

Verbosity is a rendering of the events the run already records. There is one
account, and `-v`, `-vv`, `--trace` and the diagnostics bundle are four views of
it.

1. **Nothing prints what it did not record.** A fact that shows up under `-v`
   is an event first. The renderer selects, formats and orders; it does not
   observe.

2. **`engine.Traced` carries a recorded event onto the event stream**, published
   only when `Options.PublishTrace` is set — which is exactly what a verbose run
   asks for. Publishing each event as it is recorded makes the two orders one
   order: a renderer printing them as they arrive is printing the recording.

3. **A run nobody asked to publish pays nothing.** The fan-out is a tee built
   only under `PublishTrace`: no wrapper, no clone per event, no send. The
   published branch is wrapped in `trace.Digested`, because a renderer prints an
   execution's status, duration and argv and never its bytes — so publishing the
   output would deep-copy up to a megabyte per mutant for nobody, while the size
   and the digest, which are what a reader joins on, stay.

4. **`-v` and `-vv` are two depths of one renderer.** `-v` adds phase durations,
   what killed each mutant, how many attempts it took, and which suites cover a
   survivor. `-vv` adds one line per recorded event, indented, so `grep '^  '`
   separates the two. Both imply `--no-tui`, and both are refused with `--quiet`
   and `--json`: a document on standard output and a commentary on it are
   different requests.

5. **The default output does not move.** A run with no verbosity flag prints
   byte-identically to what it printed before the flags existed, pinned by an
   `internal/console` golden.

## Consequences

- Adding a fact to `-v` means adding an event, which means deciding what it is
  called and where it belongs in the schema. That is more work than a print
  statement and it is the point: the fact then exists in the recording, in the
  diagnostics bundle and on the screen, at once and by construction.
- A rendered line is *not* the recording's own line. It carries no sequence
  number, and the run id in it is new every run, so two `-vv` logs of one
  workspace differ there whatever else they agree on. A reader diffing them
  should expect that; a reader who wants byte-comparable output wants
  `go-mutants trace diff`.
- `-vv` orders events as they were recorded, and mutant executions are recorded
  when they return. Two runs of one workspace may therefore interleave them
  differently. That is a property of the recording rather than of the renderer,
  and it is stated in `trace`'s package doc for the same reason.
- The renderer can only be as good as the vocabulary. A note or an artifact kind
  that nothing explains produces a `-vv` line a reader cannot act on, which is
  why the kinds are pinned to `docs/trace-v1.md` by test.
