<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0011 — A mutant no covering binary could observe need not be executed

## Status

Accepted, 2026-09-14. Implemented by `internal/instrument`'s probe trees and
infection log, `internal/execute`'s `RunProbe`, `internal/probe`'s settle rule,
and `internal/engine`'s probe phase. Selected by `test.probing = "on"`, which is
off by default. [ADR 0001](0001-trace-is-not-evidence.md) draws the line this
record depends on: probing is an optimisation, so a pass that cannot be made
fails open rather than failing the run.
[ADR 0010](0010-narrowing-to-tests-is-sound.md) is the record this one extends,
and its point (1) is the one this subsumes.

## Context

Coverage answers *which test binaries reach this line*, and a run skips the
binaries that reach none. That is a question about the program counter, and it
over-approximates twice over: a binary that reaches a line still need not
evaluate the mutated expression, and evaluating it still need not produce a
value different from the one the mutant would have produced. A comparison whose
two readings agree on every input a suite supplies is executed once per mutant
for an answer that was decidable before any of them ran.

The finer question is *which mutants could this binary have observed*. A **probe
tree** answers it: the original program, with each site instrumented to record —
per mutant — whether this pass could rule that mutant out. Nothing is activated;
what runs is the program the user wrote.

The gain is not bounded the way narrowing's is. On a module whose mutants are
thinly covered and whose tests are quick it is most of the run; on one whose
every test touches everything it is nothing, and the tree costs a second
snapshot, a second instrumentation, a second validation, a second build and one
suite run per binary. So unlike narrowing this is not the default, and unlike
narrowing the arithmetic genuinely can come out either way.

## Decision

`test.probing = "on"` proves, before any mutant is executed, which executions
are unnecessary, and it reaches the same verdicts a run without it would. Five
things make that true, and each is enforced rather than assumed.

1. **The licence is "this pass could not rule the mutant out".** `Infect(i)`
   means exactly that, and the absence of `i` from every log of every covering
   binary means the opposite. It is deliberately not "the value differed": a
   deleted statement's mutant differs from the original by the *absence* of an
   effect, which no comparison can see, and what a probe records there is that
   the statement ran. Both readings license the same thing — a pass that could
   not distinguish the mutant cannot kill it — and only the second wording
   admits a form for a site with no value.

2. **The evidence is per binary, and a settled mutant needs every covering
   binary to agree.** A mutant is settled only when every binary that covers it
   produced facts and none of them named it. A binary whose pass failed, timed
   out or could not write its log is a binary *nothing is known about*, and is
   never read as one that saw nothing — the two are the same bytes and the
   opposite meaning.

3. **This subsumes ADR 0010's confirmation rather than skipping it.** That
   record's point (1) pays for a whole-binary re-run of every narrowed survivor,
   because a non-covering test can kill a mutant indirectly through shared
   state. A probe pass *is* the whole binary, with no test selection, so where
   it establishes that a binary could not observe a mutant it has established it
   for every test in that binary — the ones that cover the line and the ones
   that do not. The confirmation is proved unnecessary, not dropped.

4. **Everything fails closed.** A mutant with no probe form is never settled:
   its site is not in the probe tree at all, so its absence from every log is
   the absence of a question rather than the answer to one, and only
   `internal/instrument` can say which forms exist. A mutant with no covering
   binaries is never settled: the empty intersection is vacuously "no binary
   named it", which reads exactly like "every covering binary looked and saw
   nothing". A log naming an index the catalogue cannot explain discards *every*
   fact of the run, because a repaired set is one nobody can vouch for.

5. **The probe tree is the original program, and it is checked.** It goes
   through the same validation the mutant tree does, so a probe site that does
   not compile is bisected out and costs one mutant its probe rather than the
   file its measurement. Its forms evaluate the mutated reading as well as the
   original, so each is refused wherever that second evaluation could differ
   from doing nothing: an effect anywhere in the site, a panic in either
   reading, an edit that introduces a division, or a site the language's
   ordering rules would pull a call into. `docs/architecture.md` states each.

The report says which mutants this settled: `mutants[].unobserved` is a survivor
the run did not execute because nothing could see it, and it is never true
beside `uncovered`. The pair is what tells two remedies apart — an uncovered
mutant's lines are never run, and an unobserved one's are run while nothing
asserts anything about what they produce.

## Consequences

A probing run and a run without one reach the same verdict for every mutant, and
two standing tests say so from different ends.
`TestProbingReachesTheSameVerdictsForLessWork` runs `fixtures/unobserved` both
ways: two mutants chosen to exercise the two answers a probe can give, so the
settling, the narrowing and the saving are each asserted, the last of them as
strictly fewer `mutant-run` children. Cost is compared as counted child
processes and never as a duration, for the reason this repository compares
everything that way.
`TestProbingChangesNoVerdictOverTheWholeOperatorCorpus` runs `fixtures/families`
both ways, which is every rule the registry implements with a live candidate
each, and asserts only that not one verdict moved. It says nothing about the
saving on purpose: whether that fixture holds a mutant a probe can settle is a
fact about the fixture, and a test that required one would fail the day somebody
tightened an assertion in it.

The probe tree is put back between passes. A probe pass runs a whole suite, and
a suite that legitimately writes into the package directory it runs in would
leave the next pass measuring a program nobody instrumented — which would not
make the pass fail, but would make its answer a licence about a different
program. It is done on every probing run rather than only on an isolating one,
because the alternative is a condition nobody can check; on a suite that writes
nothing the walk finds nothing.

What this does not do is measure per test. A test profiled alone is a different
execution from the same test inside its suite — shared state, ordering,
`TestMain` — so a per-test log licenses less than it appears to, and buying that
licence means paying ADR 0010's controls a second time for a second soundness
argument. The whole-binary rule needs neither. Revisiting it would mean a second
record, not a change to this one.

The second tree is the cost, and it is charged whether or not it saves anything:
a module whose every test touches everything pays a full second build and a full
second suite for a decision that settles nothing. That is why the default is
off, and why there is no flag — like `test.narrowing`, it is a fact about how a
project is measured rather than about one invocation.
