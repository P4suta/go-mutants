<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0003 — Diagnostics live in the report directory

## Status

Accepted, 2026-09-07. Implemented by `internal/cli/diagnostics.go` and the
trace root it shares with `internal/cli/trace.go` (#36, #40).

## Context

A run that fails in CI could only be investigated by reproducing it, and by the
time anybody looked, the snapshot every question was about had been deleted. The
fix is obvious — write the failure down — and the only interesting part is
*where*.

Three candidates, and two of them are wrong for reasons specific to this tool.

**A temporary directory** is where a run already works, and it is removed at the
end of the run. That is precisely when the evidence becomes interesting: the
directory dies at the moment somebody would want to read it, and on a CI runner
it dies with the machine. A bundle nobody can find is not a bundle.

**Anywhere else inside the workspace** collides with the thing that makes this
tool what it is: *a go-mutants run digests its own workspace*. The snapshot is
frozen and the digest is checked again before the run claims to have measured
anything, so a file written where the snapshot reads makes the tree drift under
the run — and the run then dies of drift it caused itself, reporting a workspace
that changed when nothing changed it. This is not hypothetical; it is the same
constraint [ADR 0001](0001-trace-is-not-evidence.md) put on the trace root.

**The report directory** is the one in-tree place the snapshot never reads. It is
already excluded from the copy, already git-ignored in the corpus, already the
place a user looks for what a run produced, and already named by configuration so
a workspace that wants it elsewhere can say so.

## Decision

A failed run's diagnostics are written into the report directory, beside the
recordings, under the run's own id.

1. **`<report.directory>/diagnostics/<run-id>/`**, or the run's trace directory
   when the run was traced — so a traced failure leaves one directory rather than
   two halves of one story.

2. **Never a temporary directory**, and never anywhere else in the workspace. A
   `--trace=DIR` naming a directory inside the workspace but outside the report
   directory is refused (`GOM1013`) with a `trace-unavailable` note, for the same
   reason.

3. **The bundle is written in a fixed order, and the last file is the receipt.**
   `error.txt` first — the rendered failure and its typed chain — then
   `environment.txt` (variable *names* only, by ADR 0001's rule),
   `doctor.txt`, the recording, `report.json` if there was one, and
   `preserved-paths.txt` last. The presence of that last file is what says the
   bundle is finished, which is how the collector tells a complete bundle from a
   run still writing one.

4. **Retention is the trace root's rule, because it is the same rule.** The
   newest ten are kept; only a directory named by a run id and holding a bundle
   of ours is collectable at all; an unfinished one is left alone. `go-mutants
   trace clean` is the same collector run by hand.

5. **It never changes the verdict.** A bundle that cannot be written is a
   `GOM1014` warning and the run's own exit code. An interrupted run writes none,
   because nothing went wrong. `--no-diagnostics`, or
   `GO_MUTANTS_DIAGNOSTICS=0` for an invocation nobody can add a flag to, turns
   it off.

## Consequences

- `--report none` does not switch the bundle off, and that surprises people once.
  A bundle is not a report: it is the account of a run that failed to produce
  one, and a user who asked for no documents did not ask to be unable to diagnose
  a failure. The flag's own help says so.
- A failing run writes files into the user's tree that a passing run does not.
  That is a real cost and it is bounded twice: only on failure, and only ten
  deep.
- The evidence is only as good as the report directory's configuration. A
  workspace that points `report.directory` at a path outside the tree gets its
  bundles there, which is correct and is worth knowing before hunting for them.
- Because the run id names the directory, a bundle and the report it is about are
  always pairable, and a CI step can attach `reports/mutation/diagnostics/` as an
  artifact without knowing anything about which run failed.
- `--keep-temp` remains a separate decision. The bundle answers "what happened";
  only a kept snapshot answers "what did the tree this ran in look like", and
  that one is off by default because a kept snapshot is a whole copy of the
  module and nothing will ever remove it.
