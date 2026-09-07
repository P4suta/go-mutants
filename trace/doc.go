// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package trace records what a go-mutants run did, as a machine-readable
// stream of events.
//
// A recording answers the questions a report cannot. A report says a mutant
// survived; a recording says which test binaries were run against it, with
// which arguments, for how long, and where their output was preserved. A
// report says a run took nine minutes; a recording says that eight of them
// went into compiling test binaries. That is the difference between knowing a
// result and being able to act on it, and it is the whole reason this package
// exists.
//
// The wire contract is `gomutants-trace-v1`: [JSONSchema] returns it,
// `docs/trace-v1.md` explains it, and a golden test pins every byte of it.
//
// # A trace is not evidence
//
// go-mutants is fail-closed about its results. A recording has the opposite
// failure profile, deliberately: a full disk, a read-only directory or a sink
// that could not encode one event costs the event and never the run. Nothing
// in a recording is a claim about the software under test, no trace option
// enters a mutant identity or a cache key, and a traced and an untraced run of
// one workspace reach the same verdict. See
// `docs/adr/0001-trace-is-not-evidence.md`.
//
// A [Sink] that *panics* costs the event too, and is counted exactly as one
// that returned an error: a sink is an interface, an embedder's implementation
// of it is ordinary Go code, and ordinary Go code panics — and without that the
// panic would unwind through the recorder on whichever goroutine was recording,
// which during a mutation run is one of the execution workers, and take the
// whole process with it.
//
// Best effort is only honest if the loss is reported, so every [Sink] counts
// what it could not keep and every recording ends with `events_emitted` and
// `events_dropped`. A reader has exactly two things to check: that the last
// line is a `run-end`, and that its `events_dropped` is zero.
//
// # The disabled recorder is nil
//
// [New] returns a nil *[Recorder] for a nil sink, and every method of a nil
// recorder does nothing. Call sites therefore record unconditionally:
//
//	seq := opts.Trace.Exec(trace.ExecRecord{Kind: trace.ExecKindMutantRun, ...})
//
// with no branch on whether a trace was asked for. That is not a convenience.
// A branch is a place for the traced and the untraced path to diverge, and the
// one property this package must not break is that recording a run cannot
// change it.
//
// # Two reductions the recorder makes, and no caller can undo
//
// [Recorder.Exec] reduces the environment it is handed to variable *names*,
// sorted and deduplicated, and digests the captured output into a size and a
// SHA-256 rather than serialising it. Both happen inside the recorder rather
// than at its call sites, which is what makes them properties of the format:
// no future caller can leak a credential by passing `NAME=VALUE`, and no
// command's output can grow an event.
//
// The bytes are not discarded — they are the developer's own test output, and
// a digest would be useless for reading a failure. A [DirSink] preserves them
// beside the stream as `output/<seq>.txt`, capped at [OutputFileLimit] with a
// [TruncationMarker], so a reader can quote a line of a failure and a
// publisher can attach a recording with the `output/` directory removed.
//
// # Sinks
//
// [NewDirSink] collects one recording in a directory of its own and appends
// JSON Lines to it, so a run that is killed leaves a readable prefix.
// [NewMemorySink] is the bounded ring an untraced run records into — the
// failure nobody expected is exactly the failure nobody thought to pass a flag
// for. [NewTeeSink] records into both at once, and [Digested] strips the bytes
// a ring must not grow with.
//
// # Reading a recording back
//
// [Read] returns every event of a stream, strictly: unknown fields, a foreign
// payload, two payloads, an unknown type, a sequence that does not increase
// and a second `run-end` are all refused. [ReadSummary] returns what a
// recording says about itself without the events, and [Diff] compares two
// summaries — which is how a run that got slower is investigated without
// reading either stream by eye.
//
// # Joining a go-mutants recording to its consumer's
//
// go-mutants is a library as well as a command, and its embedders record their
// own runs. The `exec`, `prepare`, `mutant`, `artifact`, `note` and `run`
// payloads therefore carry the same field names as goatest's own trace, and a
// command recorded in both streams is the same `(argv, dir, output_sha256)` in
// both. One payload is named differently on each side: goatest spells what this
// package calls `note` a `progress` record, with the same `kind` and `detail`
// fields, so a consumer joining the streams reads the two as one kind of line.
//
// The hook for handing go-mutants a sink of your own is `OpenOptions.Trace` in
// the root package. A workspace records into that [Sink], and the `TraceSeq` on
// every result is what the join is written against. A nil sink does not switch
// recording off — the workspace keeps a bounded ring, which
// `Workspace.Recording` hands back at any point and after `Close` — so the
// choice is where the account goes rather than whether there is one.
//
// A supplied sink is deliberately *not* wrapped in [Digested]: it receives the
// captured output an exec event carries, because a sink writing to disk is
// meant to preserve it. A sink that keeps its events in memory should wrap
// itself, or it grows with the run rather than with its own capacity.
//
// # Concurrency
//
// Every method of a [Recorder] is safe to call from many goroutines at once.
// Sequence numbers are assigned under the same lock that hands the event to the
// sink, so the order of the file and the order of `seq` are one order however
// many workers record at once. What a reader may not assume is *which* order
// concurrent work arrives in: mutant executions and probe passes are recorded
// when they return, so two recordings of one workspace may interleave them
// differently.
package trace
