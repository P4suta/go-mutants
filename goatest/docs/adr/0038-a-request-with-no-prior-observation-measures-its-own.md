<!--
SPDX-FileCopyrightText: 2026 goatest contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0038 — A request with no prior observation measures its own

## Status

Accepted, 2026-09-17. Supersedes point 4 of
[ADR 0031](0031-confirm-comparative-watchdogs.md).

## Context

[ADR 0031](0031-confirm-comparative-watchdogs.md) derives a mutant budget from
the positive clean durations a run has already observed of the same request, and
runs the exact original control under the containment ceiling rather than under
that derived budget. Its point 3 gives the reason: the original is a program the
baseline already ran to completion, so a narrow control deadline cannot protect
anything — the only thing it can do is lose the observation the budget is
derived from and make the group inconclusive for a reason that says nothing
about the mutation.

Its point 4 then refuses to start the control at all when no prior observation
exists, and reports `mutation-control-unavailable`. That refusal is the same
loss point 3 rejects, arrived at by a different route. The control was going to
run under the ceiling either way; declining to run it discards the one
measurement that would have made the budget derivable, and the mutant is
counted inconclusive without a single process having been started.

A full-scope run of this repository produced 214 such findings. Every one was a
mutant with no reaching target in a package whose suite the baseline had not
separately measured — a fact about which packages the coverage pass happened to
measure, not about the mutant, the tests, or the machine. Which packages those
are is not a property a reader of a report can check, so 214 unknowns arrived
with no way to act on them.

## Decision

1. A request with no prior observation runs its exact original control exactly
   as a request with one does: once, under the containment ceiling, memoized by
   package, arguments, environment, and deadline. Its completed positive
   duration is the request's first observation, and the budget is the saturating
   sum as before — of one sample rather than two.
2. `mutation-control-unavailable` keeps the two cases in which no observation
   can be had: no control facility is configured for the run, and a control that
   passed without a positive duration.
3. Nothing else in [ADR 0031](0031-confirm-comparative-watchdogs.md) changes. A
   control that fails or expires still makes its group inconclusive without
   starting a mutant, and a derived budget that expires is still re-measured
   once and re-run under the ceiling.

## Soundness

No verdict rests on the budget. A kill still rests on a passing exact original
and a completed failing mutant execution, and a survival on completed passing
executions; the budget decides only how long a run waits before it stops
waiting. Deriving it from one observation instead of two therefore cannot make
a verdict wrong — it can only make a slow-but-terminating mutation expire that
would have fitted inside the wider sum.

That case is already answered. Point 5 of ADR 0031 measures the original once
more after an expiration and, if it completes, runs the mutant again under the
containment ceiling itself. A one-sample budget is the narrowest claim about
how long the work takes, and the machinery that exists to absorb a falsified
claim absorbs this one. The cost is bounded exactly as before: one derived
budget plus one ceiling per group.

## Consequences

- Mutants that were counted inconclusive because their package suite went
  unmeasured are now executed, and reach a kill, a survival, or an honest
  timeout. A run's inconclusive count no longer depends on which packages the
  coverage pass measured.
- Those mutants now cost a package-suite execution each. The control in front
  of them is shared by every request with the same execution identity, so the
  added cost is one control per distinct command plus the mutant executions
  themselves — the cost every other mutant already paid.
- `mutation-control-unavailable` becomes rare, and now means what it says: this
  run has no way to observe the request at all.
