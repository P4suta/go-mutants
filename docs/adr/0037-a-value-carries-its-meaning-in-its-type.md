<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0037. A value carries its meaning in its type

And where a type cannot carry it, a check does.

## Status

Accepted, 2026-09-17.

## Context

Five defects were fixed in this repository on one day. They looked unrelated —
a memory sampler, an instrumentation pass, a runner's error reporting, a worker
count, a CI summary — and they were one defect:

| What broke | What carried the meaning |
| --- | --- |
| The memory sampler gave up on its first unanswered look, and Linux then reported that a process had used no memory at all | a `bool` that meant both "measured" and "keep going" |
| A loop inside a rewrite site made a whole file fail to instrument | a `[]Splice` with no notion of which coordinate space it was in, and nothing saying two producers had been handed the same bytes |
| A run ended `ERROR` saying "workspace preparation failed" and nothing about what had failed | a sentinel that is by construction a consequence, said only in prose |
| A mutation run took four cores on an eighteen-core machine | one decision, written twice, as two bare literals |
| The job that exists to name the gates that failed printed `0: null` | a jq fragment written for one input shape and reused on another |

In every one of them the meaning was carried by a convention that only a careful
reader enforced. None of them was caught by a test, because the tests agreed
with the code: two agreeing programs is also what it looks like when both are
wrong, which [the runner's ADR 0034](../../goatest/docs/adr/0034-what-a-ledger-cannot-check.md) already says about
ledgers.

The repository had also already written down the answer, in the one place it was
practised. `internal/devtools/traceaudit` returns three values — agree, disagree
and **unaudited** — and says why: *turning "I cannot check this" into "this is
fine" and turning it into "this is broken" are both wrong*. Nothing enforced it
anywhere else, and the sampler is exactly the place it was not practised.

## Decision

**A value's meaning belongs in its type. Where Go cannot express it, a check
enforces it, and where a check cannot decide it, a sentence is required.**

Three tiers, in that order of preference.

### 1. The type, where it can

A domain with three outcomes gets three values, not two and a convention.
`internal/runner`'s sampler now answers `sampleTaken`, `sampleUnanswered` or
`sampleExceeded`, and the bug it had is no longer expressible.

### 2. A check, where the type cannot

Go has no exhaustive `switch`, so a set that grows silently breaks every
consumer at run time rather than at compile time. `cmd/gomutants-vet` carries
`internal/analysis/exhaustive`, which refuses a switch over one of this
repository's own closed vocabularies that does not name every word, over both
modules, in `mise run lint`.

A `default` does not excuse a missing word. That is the decision rather than a
setting: a default is precisely what converts "this set grew" from a compile
error into a failure on a user's machine, and the engine's `Outcome` reaching
the runner's `default` is the exact shape this repository is most exposed to.

It is a `go/analysis` pass rather than another scan under `internal/devgates`
because a pass is a value. The same one loads into `go vet -vettool`, into
`golangci-lint`, and into a driver of our own, over either module, and it reads
types — which no text scan can. The scans stay: they are cheap, they run in the
unit tier with no toolchain, and a check that needs no toolchain is the first
line of defence.

### 3. A sentence, where the check cannot decide

A switch whose `default` really is the right answer for every word says so
immediately above itself:

```go
switch outcome {
```

Two properties of that marker are load-bearing:

- **It carries a reason.** Switching a check off is a sentence somebody wrote,
  not a token somebody copied.
- **It is the switch's own comment.** A marker five statements away would exempt
  whichever switch came next, which is how a reader comes to trust a sentence
  written about other code.

It is a comment rather than a line in a ledger elsewhere because it cannot then
go stale: delete the switch and the exemption goes with it. Every path-keyed
allowlist in this repository has had to be taught separately to fail on an entry
that no longer names anything.

### And a diagnostic names its producers

Where a conflict is only detectable at run time, it says who conflicted.
`GOM7312` reported `splice 71 at [4942,4942) overlaps splice 6 at [4232,5675)` —
every word true, none of it actionable, and in a file it could not name. It now
reports the file, and each side's producer. That is not a type, but it is the
same discipline: the information a reader needs was already in the program and
was being thrown away before it reached them.

## Consequences

- A vocabulary this repository declares cannot grow without every switch over it
  being visited. That is the cost, and it is the point.
- Thirty-five switches carry `//exhaustive:total` today. Every reason is a fact
  read off the code rather than a guess about intent, and one of them was worth
  the exercise alone: goatest's mutant accounting names five statuses and not
  `MutantOutOfScope`, because out-of-scope is computed by subtraction before the
  loop and a case there would count it twice. Nothing said so until the check
  made somebody find out.
- Vocabularies the standard library owns are out of scope. `token.Token` and
  `reflect.Kind` grow on somebody else's schedule, and demanding every case of
  them is how a check ends up switched off.
- This ADR is not finished work. The classes named in the table above are closed
  one at a time; the coordinate spaces in `internal/instrument` are still one
  type used for two things, and `PrepareEvent` still records that a phase failed
  without recording why — that one waits on `trace-v2`, because the recording's
  schema is `additionalProperties: false` and adding a field to it is a format
  change rather than a fix.
