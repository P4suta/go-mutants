<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0013 — A mutant that does not return is decided by work, not by the clock

## Status

Accepted, 2026-09-15. Implemented by `internal/instrument`'s loop counters and
the `Limit`/`Over` half of the generated runtime, by `internal/engine`'s census
of the instrumented baseline and its `DivergenceFactor`/`DivergenceFloor`, and
by `internal/execute`'s classification of the divergence exit as a non-return
that needs no second measurement.
[ADR 0009](0009-a-mutant-is-bounded-in-memory-as-in-time.md) is the record this
one is the twin of: that one bounds a mutant in memory as it is bounded in time,
and this one replaces the bound in time with a bound in work wherever the work
can be counted.

## Context

A mutant that turns a terminating loop into a spinning one has to be told apart
from a mutant that is merely slow, and go-mutants has had exactly one instrument
for that: a stopwatch. The budget is `max(10s, slowest baseline run × 5)`, a
timeout is measured a second time before it is believed, and the second
measurement is made serially so that nothing else is competing for the machine.

Every part of that is a fact about the machine rather than about the mutant.

- **The verdict moves with the load.** A suite measured on a quiet machine and a
  mutant measured on a busy one are two different experiments, and the
  difference between them is reported as a property of the mutant. This
  repository's own gate has recorded kills as `inconclusive` for exactly that
  reason, and the note under `[policy]` in `.go-mutants.toml` says so.
- **It is the most expensive thing a mutant can do.** A mutant that never
  returns pays the whole budget, twice, in a pass that is serial on purpose. On
  this repository's gate that is three mutants and about two minutes of a twelve
  minute run — more than the rest of the executions put together, per mutant.
- **It cannot say why.** "This mutant did not return in 33 seconds" is not a
  finding a user can act on, check, or reproduce on another machine.

The deeper problem is that the question being asked is the wrong one. What makes
a spinning mutant a spinning mutant is not that it took a long time. It is that
**it does an amount of work the original program never did**. That is a counted
quantity, it is a property of the program and the suite rather than of the
machine, and go-mutants is in the one position from which it can be counted:
it generates the tree the mutant runs in.

## Decision

A mutant is bounded in **work** as well as in time, and the bound in work is the
one that decides the common case.

1. **Every loop of an instrumented file carries a counter.** The rewrite adds
   two locals in front of the `for` and one test at the top of its body:

   ```go
   __gm_n7, __gm_k7 := uint64(0), __gm.Limit[7]
   for i := 0; i < n; i++ {
       if __gm_n7++; __gm_n7 > __gm_k7 { __gm_k7 = __gm.Over(7, __gm_n7) }
       …
   }
   ```

   `Over` is one call rather than two because the two things that can happen
   there are the same shape. A process enforcing a table never returns from it;
   a process taking the census records the count and hands back a higher
   ceiling, so the local ladder doubles and the census gets about one line per
   doubling rather than one per iteration. What a census holds is therefore
   within a factor of two below the true maximum, which the factor absorbs.

   The counter is a local, so there is nothing shared between goroutines, no
   atomic, and no race: two registers, an increment and a compare per iteration.
   It counts one *entry* to the loop, which is the granularity the question is
   asked at — "how many times round did this go" and not "how many times round
   did every instance of it go, ever". The rewrite preserves every line number,
   like every other rewrite this tool makes.

2. **The ceiling is what the original program did, measured by a run that
   already happens.** The instrumented baseline runs the whole test command
   against the instrumented tree with nothing activated; it is the semantic
   preservation gate, and it is paid for on every run. Under
   `GO_MUTANTS_LOOP_CENSUS` that run also records, per loop, the largest number
   of iterations any single entry reached. The engine turns the census into
   `max(DivergenceFloor, observed × DivergenceFactor)` per site and hands the
   table to every mutant run.

   A loop the census never saw gets the floor. That is the honest reading: the
   original never entered it under this suite, so there is no measurement to
   scale, and the floor is the number that says "this is more work than any
   plausible answer needs".

3. **Exceeding the ceiling is a detection, and it is a fact rather than an
   observation.** The runtime prints which site diverged and how far — "the
   loop at internal/config/position.go:170 ran 1048577 times, past the 1048576
   this run derived for it from what the original program did under the same
   tests; this mutant does not return" — and exits with a status of its own,
   which `internal/execute` classifies as a confirmed non-return. There is no
   second measurement, because there is nothing about the machine to control
   for: the same tree, the same test, the same census produce the same answer
   on every machine, every time.

4. **The clock stays, as the backstop it should always have been.** Counting
   sees a loop in an instrumented file and nothing else, so three things stay
   the stopwatch's:

   - a mutant that blocks rather than spins — a channel, a mutex, a socket —
     which executes no iterations at all;
   - a loop in code this run did not instrument, which is every dependency and
     every package outside `mutation.include`;
   - a mutant that makes something *else* hang: a subprocess, a syscall.

   For those, the budget and the second measurement are exactly what they were.
   What changes is that they stop being the mechanism that decides the ordinary
   case.

5. **A generous ceiling is nearly free, and that is the point.** The cost of
   waiting for a clock is seconds per mutant; the cost of waiting for a counter
   is the original loop's work multiplied by the factor, which for the loops
   that actually spin is microseconds to milliseconds. So the margin against a
   false positive can be a thousand times more generous than the stopwatch's and
   still settle a thousand times sooner.

## Consequences

**A mutant that legitimately does far more loop work than the original can be
reported as diverged.** A mutant that turns `i *= 2` into `i += 2` makes a
logarithmic loop linear; if the suite never gave it enough input for the floor
to cover, it is a detection the tests did not earn. Two things make that
acceptable where a false timeout is not: it is deterministic, so it is the same
on the user's machine as in CI and it does not appear and disappear with load;
and it is *explained* — the report names the loop, the count it reached and the
count the original reached — so a user who disagrees can see the arithmetic
rather than the stopwatch.

**The outcome vocabulary does not grow.** A mutant the counter stops is
`timed_out`: it did not return, which is what that outcome means. What changes
is its definition — "a confirmed timeout, or a proven divergence" — and the
diagnosis beside it. This is ADR 0009's judgement applied again: a memory kill
is a `killed` with `memory_exceeded` beside it rather than a new outcome, and a
consumer that has to learn a new enum value to understand an old fact has been
made to pay for the implementation's history.

**The instrumented tree is bigger and its loops are slower.** Two registers and
a compare per iteration, in the files the run mutates and nowhere else. It is
paid by every mutant run and by the instrumented baseline; it is not paid by the
plain baseline, by the coverage profiling pass, or by anything outside the
snapshot.

**A census is a new artifact, and it fails open.** A census that cannot be
written, cannot be read, or names a site the tree does not hold leaves every
ceiling at the floor and the run measures as it always did, with a warning. That
is `internal/coverage`'s judgement and `internal/cache`'s: an optimisation that
cannot be verified is one the run does without.

**`goto` is refused rather than worked around.** Go forbids a jump over a
variable declaration into its scope, so a function holding a `goto` has its
loops left uncounted rather than rewritten into something that will not compile.
It is a skip with a reason, like every other one this tool records.

## Alternatives considered

**Keep the clock and make the budget finer** — per mutant, from the profiled
durations of the tests it is narrowed to. It is the same instrument with a
better ruler: the verdict still moves with the load, and a tighter budget moves
it *more*. Rejected because it trades accuracy for speed in a project whose
first claim is accuracy.

**Compare a mutant against a control run beside it**, so that a machine that
slowed down slows both. Robust, and not deterministic: the answer depends on
what else the scheduler happened to be running, which is the property this
record exists to remove.

**Count instructions or cycles rather than iterations.** Closer to the metal and
further from the program: perf counters are unportable, unavailable on macOS
without entitlements, and vary with the microarchitecture. Loop iterations are a
property of the program.

**Prove non-termination statically for every case.** Discovery already proves
what it can — see `internal/discover`'s termination proof — and the three
mutants this repository's own gate still waits for are the ones it cannot:
`for scanner.Scan()`, `for strings.Contains(s, "--")`, and a stride that stops
advancing. Each needs the semantics of a library function. The static proof and
this record are complements: what can be decided before anything runs is, and
what cannot is decided by counting rather than by waiting.
