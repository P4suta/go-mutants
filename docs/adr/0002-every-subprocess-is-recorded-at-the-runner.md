<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0002 — Every subprocess is recorded at the runner

## Status

Accepted, 2026-09-07. Implemented by `internal/runner` and wired through every
layer above it: the runner's own recording (#23), the execution and validation
phases (#29), the engine's phases and stages (#33), the CLI's directory and
flag (#36), and the library's session recorder (#41).

## Context

[ADR 0001](0001-trace-is-not-evidence.md) settled what a recording is *for*. It
did not settle where a recording comes from, and that question has a wrong answer
that looks reasonable.

go-mutants starts processes from a dozen places: the `go version` probe that
locates a toolchain, the scope listing, the pristine baseline, the instrumented
baseline, a `go test -c` per package, a coverage run per binary, a compile per
validation step and per bisection step, one test binary run per mutant, one per
probe, one control, a verification pass, and whatever an embedder asks a
workspace to run for it. Each of them could record itself.

That is a rule with a dozen chances to be broken, and every one of them breaks
silently. A call site that forgets produces a recording missing exactly one
command — and a reader hunting for the absence of a command that did in fact run
is worse off than a reader with no recording at all. Worse, the omission surfaces
in the run somebody is trying to diagnose, which is the run where nobody can
check.

The second half of the problem is that the label cannot come from the runner. A
`go test -c` and a mutant's test binary are the same syscall from inside
`internal/runner`: same shape, same supervision, same output handling. What
distinguishes them is why they were started, and only the caller knows that.

## Decision

Every subprocess is started through `internal/runner`, and that is where it is
recorded. Concretely:

1. **One choke point.** `runner.Run` emits the `exec` event. No layer above it
   records a command of its own, and no layer below it starts one. A command
   that reaches the operating system without passing through the runner is a
   defect, and the tier gate in `internal/testkit/tiers_test.go` — which greps
   for `exec.LookPath("go")` outside the integration tag — is a side effect of
   the same rule being enforced for a different reason.

2. **`Kind` is on every `Spec`.** `runner.Spec.Kind` is one of
   `trace.ExecKinds()`, and it is the one field a call site *must* supply,
   because it is the one thing the runner cannot know. `Spec.Subject` carries
   what the command was about — a mutant id, an import path, a scope pattern —
   and is empty when the kind says everything there is to say.

3. **An unlabelled command is refused by the schema, not by the runner.** The
   runner stores what it is given; `schema/trace-v1.schema.json` closes the
   enumeration. That division is deliberate and follows directly from ADR 0001:
   a runner that rejected an unlabelled spec would turn a missing *diagnostic*
   into a failed *run*, which is the one thing a diagnostic must never do. The
   cost of forgetting is a recording that does not validate, which is loud, late
   and harmless.

4. **The recorder is a field, not a global.** `runner.Options.Trace` is the
   run's own `*trace.Recorder`, handed down through the options of every layer.
   Two runs in one process therefore record into two recordings, which is what
   makes the library's in-process API traceable at all.

5. **Call sites record unconditionally.** A nil `*trace.Recorder` is the disabled
   recorder and every method on it is nil-receiver safe, so there is no `if
   tracing` anywhere. A branch on whether a trace was asked for is a place for
   the traced and the untraced path to diverge, and ADR 0001's first point is
   exactly the property such a branch would break.

6. **A failure names the command it was about.** `runner.Result` carries the
   `Kind` back, and the typed errors carry an `Invocation` — what was run, where,
   under which label, and the sequence number the recording filed it under. So
   the rendered error and the recording are joinable without reading either by
   eye.

## Consequences

- Adding a new kind of subprocess is three things rather than one: the call,
  a new `ExecKind` constant, and a paragraph in `docs/trace-v1.md`. The last is
  enforced — `trace/docs_test.go` fails when a kind exists in the code and
  nowhere on the page — because a label a reader cannot look up is not a label.
- The runner's options struct grows fields that most callers do not care about.
  That is accepted: the alternative is a package-level recorder, which would make
  two concurrent runs in one process record into one stream and would make the
  trace a global that a test could not isolate.
- A command started by a *child* is invisible here. `go test` compiling a
  package, or a fixture's own test suite starting something, is one `exec` event
  and one captured output, not a tree. The recording is an account of what
  go-mutants did, not of everything that ran because of it.
- Because the label is the caller's, a mislabelled command is possible and the
  schema cannot catch it: `control-run` and `mutant-run` are the same executable
  started the same way, and only the label tells them apart. That pairing is
  written out in `trace/kinds.go` precisely because it is the one place a reader
  has to trust the caller.
