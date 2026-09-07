<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0009 — A mutant is bounded in memory as it is in time

## Status

Accepted, 2026-09-07. Implemented by `internal/runner`'s `Spec.MemoryLimit`,
`Result.PeakRSS` and `Result.MemoryExceeded`, and by `internal/engine`'s
`MinDerivedMemory`, `MemoryFactor` and `MemoryDerived` (#59).
[ADR 0002](0002-every-subprocess-is-recorded-at-the-runner.md) records why the
process layer is the one choke point every command passes through, which is what
makes a bound enforceable at all.

## Context

go-mutants has always bounded a mutant's **time**. The bound is derived —
`max(10s, slowest baseline run × 5)`, measured twice before it is believed — and
the supervisor kills the whole process tree when it expires. That is the right
shape and it has worked.

It bounds nothing else, and on 2026-09-06 that stopped being an omission and
became an incident. PR #58 widened this repository's own dogfood gate to
`internal/config`, whose 89 new mutants include two that never return **and
allocate while not returning**: `negate-loop-condition` at
`internal/config/position.go:59` turns `for parser.NextExpression()` into a loop
that appends until the process dies, and the same rule on the `i < 0` condition
in `lineStarts` does the same thing. Measured locally, one of them reached
roughly eleven gigabytes in twelve seconds and was still climbing.

On a laptop that is a swap storm. On GitHub's ubuntu runner the `dogfood` job did
not fail — it *vanished*, with "The runner has received a shutdown signal", right
after the line

```text
KILLED  dbb00722  internal/config/position.go:59:6  negate-loop-condition  (23.123s)
```

The derived timeout for that suite is ten seconds. The mutant took twenty-three,
which is to say the timeout did eventually fire and the runner was already gone
by the time it did: a seven-gigabyte machine cannot survive twelve seconds of a
program allocating as fast as it can, and no wall-clock budget short enough to
prevent that would be long enough to run the tests.

The obvious answer is not available to this project. Excluding a mutant, or
budgeting a target out of a run, is a **regression** by explicit policy: a
mutation score computed over the mutants that happened to be convenient is not a
mutation score. The tool has to be able to run the mutant that eats the machine
and come back with a verdict about it.

## Decision

**A mutant's memory is bounded the way its time is bounded: derived from the
baseline, enforced at the process layer, over the whole tree.**

1. **The runner measures every process and bounds the ones it is asked to.**
   `runner.Result.PeakRSS` is reported for every child go-mutants starts, from
   `wait4`'s `ru_maxrss` on POSIX and from the job object's `PeakJobMemoryUsed`
   on Windows. The two are not the same quantity — resident pages against
   committed charge — and neither is converted into the other, because a
   conversion between two things the kernels measure differently would be a
   number go-mutants invented. `runner.Spec.MemoryLimit` bounds one; while the
   child runs, the tree's memory is sampled every `MemorySampleInterval`
   (100 ms) and the first sample **strictly above** the limit kills the tree —
   without the polite phase a timeout gets, because the evidence a memory kill
   rests on is the peak and that has already been measured, while the grace
   would be spent allocating.

2. **A watchdog, not an rlimit.** RLIMIT_AS bounds address space and the Go
   runtime reserves hundreds of gigabytes of it before allocating anything, so
   any RLIMIT_AS small enough to be a budget kills every Go binary at start-up.
   RLIMIT_DATA is Linux-only and covers a segment a Go heap does not live in.
   Neither reaches the child's own children, which is where a `go test` binary
   keeps the memory this tool is responsible for. Windows additionally carries
   `JOB_OBJECT_LIMIT_JOB_MEMORY` on the job, so the kernel holds the line there
   even if nobody is sampling — set a quarter **above** the sampler's line, and
   that inequality is load-bearing. The flag does not kill a job that reaches
   its limit: it makes the offending commit fail, and caps the job's own
   accounting at the limit while doing so. Set to the same number, the sampler
   could never read more than the limit, would never trip, and Windows would
   lose every user-visible half of this feature while the child died of a failed
   allocation with nothing anywhere saying why.

3. **Derived from the baseline, floored at a gibibyte.**
   `max(1 GiB, largest baseline peak × 4)`, from the same runs the timeout is
   derived from, and replaced by `test.memory` / `--memory` exactly as
   `test.timeout` / `--timeout` replaces a derived timeout. The factor is four
   rather than the timeout's five because memory is not shared the way the clock
   is: a suite run beside fifteen others is genuinely slower and is not
   genuinely larger. The floor is what a suite that never measured above the
   noise gets, and a gibibyte is chosen from both sides — anything tighter is
   tripped by a `-cover` build, the race detector's shadow memory or a property
   test's corpus, and anything looser is not a bound on the incident above,
   which crosses a gibibyte inside its first second.

4. **The outcome vocabulary does not grow.** A mutant stopped by the bound is
   `killed`. It is not a new kind of verdict: the original program was measured
   under the budget the bound was derived from, so a tree that needs four times
   what the whole unmutated suite needed has been changed observably, which is
   what a kill means. What tells this kill apart from an assertion's travels
   *beside* the outcome — `memory_exceeded` and `peak_rss_bytes` on the
   execution row, on the `mutant-exec` record, and in the console's `-v` line —
   rather than inside it.

5. **The baseline itself runs unbounded**, because it is the measurement the
   bound is derived from and a budget derived from a measurement taken under
   that budget is not a measurement. Everything else that starts a prepared test
   binary — mutant runs, probe passes, control runs, the coverage profiling pass
   — is bounded, because a measurement and the thing it is compared against have
   to have had the same machine.

6. **A bound that cannot be enforced is reported as absent.** Sampling a live
   process tree needs `/proc` on Linux or the job object on Windows; macOS
   exposes it only through libproc, which is cgo, and go-mutants is installed
   with `go install` on machines that may have no C toolchain. There the peak is
   still measured and the bound is not applied, and the run says so once, as
   `GOM4047`. A number nothing enforces is worse than an honest absence.

## Consequences

- A runaway mutant is a `killed` mutant with a fact beside it, on Linux and
  Windows. On macOS it is what it was before this ADR: a mutant the timeout
  eventually stops, on a machine that may not survive the wait. That is stated
  in a warning rather than papered over, and closing it means taking a cgo
  dependency, which is a different decision from this one.
- The bound takes no part in a mutant's identity or in the outcome cache key: a
  bound in the key would give every machine whose baseline measured slightly
  differently a cache of its own, which is why `test.timeout` is not in the key
  either. It is instead recorded **on the entry** and judged on every lookup,
  exactly as the timeout is, and the rule has to be there because decision 4
  makes a memory kill indistinguishable from any other kill in the stored
  outcome. An entry killed by the bound is evidence about that bound and any
  tighter one, and about no larger one; an entry that reached a verdict inside a
  bound is not evidence about a smaller one, which might have killed it first.
  Without that, a run at 256 MiB would cache `killed` and a run at 8 GiB would
  adopt it, having never asked whether the mutant would have survived with
  thirty times the memory. See `cache.Entry.UsableWithin`.
- Every process go-mutants starts now costs one `getrusage` read it did not
  before, which is part of the `wait4` the runner already makes. Every *bounded*
  one costs, ten times a second, one `/proc` ReadDir plus one `/proc/<pid>/stat`
  ReadFile per process on the machine — so the per-tick cost scales with the
  machine's process count rather than with the tree's, and a bounded mutant on a
  busy host reads a few hundred small files a second. Windows costs one
  `QueryInformationJobObject` per tick instead, which is O(1). An unbounded run
  starts no sampler and pays none of it.
- `peak_rss_bytes` is a machine fact and is normalised out of every golden
  report, alongside the durations and the worker numbers. A golden that kept it
  would fail on the next machine, which is the same rule ADR 0004's rendering
  tests already live under.
- Two additive optional fields appear in run-report v1 and two in trace v1. A
  document written before them still validates and still parses, which is the
  compatibility rule both schemas already state; a consumer that wants to render
  "killed for its memory" reads `memory_exceeded` rather than matching a
  message.
