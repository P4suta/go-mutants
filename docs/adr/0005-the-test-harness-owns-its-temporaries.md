<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0005 — The test harness owns its temporaries

## Status

Accepted, 2026-09-07. Implemented by `internal/testkit` and
`internal/devtools/testcache` (#22, #24, #26, #32, #35, #44), with point 6
learned the hard way in #50.

## Context

Three failures, all of them about a directory nobody owned.

**The build cache.** The suites drive thousands of child `go build`, `go test -c`
and `go list` commands against fixtures, synthesized modules and instrumented
snapshots, each living at an absolute path that exists for one run. Every entry
is keyed on a path nothing will ever look up again, so the cache is rubble by the
time the suite ends — and it was the developer's own `~/.cache/go-build`. It
reached 14 GB and filled a disk twice.

The two obvious alternatives are both worse. A `GOCACHE` under `t.TempDir()` is
hermetic and recompiles the standard library once per test binary, which is
minutes per package. Leaving it in the developer's cache is what caused the
problem.

**The evidence.** A test that fails takes its evidence with it: the fixture copy,
the instrumented tree, the composed environment's scratch and the commands it ran
are under a `t.TempDir` the testing package removes the moment the assertion has
been printed. Reproducing it means running it again, which is not available on a
runner nobody can log into.

**Ownership.** Both problems invite a collector, and a collector is where this
gets dangerous. `GO_MUTANTS_TEST_GOCACHE=$HOME` is an absolute path like any
other. A tool that trusted the variable would run `go clean -cache` and
`os.RemoveAll` against a home directory.

## Decision

The harness owns the directories it writes outside a temporary one, says so in
the filesystem, and one tool collects them.

1. **One test-owned build cache, shared and persistent.**
   `GO_MUTANTS_TEST_GOCACHE` when it names an absolute path, and
   `<os.UserCacheDir()>/go-mutants-test/go-build` otherwise. `testkit.Env` and
   `testkit.Compose` point `GOCACHE` there in every environment they compose, so
   a suite's children compile into it whether or not the task wrapped them.

2. **Ownership is a marker file.** Whatever resolves the cache creates it and
   leaves `.go-mutants-testcache` in it; the kept root carries
   `.go-mutants-kept`. `internal/devtools/testcache` removes nothing that does
   not carry one — not even partly, and it does not run `go clean -cache` against
   it either. A directory that already holds files that are not the harness's is
   left exactly as it was.

3. **The harness is the owner; `testcache` is the collector.** Nothing else
   removes either directory. The path rule is duplicated in both, because a
   test-only package may not be imported from a production `main`, and
   `TestPathAgreesWithTestkit` and `TestMarkerNamesAgreeWithTestcache` run the
   tool and compare — two spellings would fail in the worst possible direction,
   with the harness writing one file, the collector looking for another, and the
   only symptom a cache that was never emptied.

4. **Keeping is opt-in locally and on in CI.** `testkit.Scratch` replaces
   `t.TempDir` in every constructor that hands a test a tree.
   `GO_MUTANTS_TEST_KEEP` is unset by default, because keeping unconditionally
   filled a disk twice; CI sets it to `1` — keep on failure — for every test job
   and uploads the kept root as an artifact, so a red build arrives with the
   evidence attached. An unrecognised spelling is *refused* rather than read as
   "off": the only symptom of a typo in a workflow file would be a failed job
   with nothing to upload.

5. **Nothing reclaims a kept directory but `mise run test-clean`.** Re-running a
   test files a new directory beside the old one, because two runs of one test
   are two pieces of evidence. `testcache trim`, which enforces the budget on the
   build cache, never looks at the kept root at all.

6. **Nothing removes a package directory.** `<kept root>/<package>/` is made by
   the first test of a binary that files something and is never removed — not
   even by a green run that removed everything in it, and not by any sweep.

   This one was learned rather than designed. The directory used to be pruned
   the moment it went empty, which raced a passing test's cleanup against
   another test's `MkdirAll`/`Mkdir` pair and killed a test that had nothing to
   do with keeping, with an `ENOENT` on its own scratch directory. A mutex
   cannot fix it: `go test ./...` runs several package binaries at once, two of
   them can have the same short name, and one process's removal means nothing
   to another's lock. So the removal went instead of the race. What is left is
   one retry on `ENOENT`, as belt and braces for a deleter this package does not
   control — an `rm -rf`, a CI step tidying the runner's temporary directory.

   The cost is an empty directory per package under the kept root, and it is not
   a cost anybody pays: an empty directory is not evidence,
   `actions/upload-artifact` puts *files* in an artifact and skips empty
   directories, so a green job still uploads nothing, and `test-clean` empties
   the whole root regardless.

7. **A collector's failure is not a suite's failure.** `test-clean` exits
   non-zero for exactly one reason — it was pointed at something that is not the
   harness's — and never because a file was busy. An unlinkable file is retried
   once, reported, and the task still succeeds: a collector that failed a run
   because a directory it wanted to delete is still there has done more damage
   than the directory ever would.

## Consequences

- Under `GO_MUTANTS_TEST_KEEP=always` the kept root grows for as long as it is
  left on, and that is the developer's to empty. Nothing here decides that
  somebody else's evidence is stale.
- Both directories live under one parent, so `<cache root>/go-mutants-test` is
  the single thing to delete to undo everything this harness has ever written.
  `mise run test-cache-status` names both and their sizes.
- The persistent cache is only safe because it is emptied *after* a run and only
  when it is over budget — so the run that paid to fill it is the run that
  benefits from it, and no machine grows an unbounded one.
- `mise run test` is deliberately *not* wrapped in `testcache exec`: it compiles
  go-mutants itself, and that belongs in the developer's own cache like every
  other `go test` they run. `mise run dogfood` accepts the opposite trade and the
  reasoning is written out on the task, because splitting one run across two
  caches would leave `test-clean` removing half of it.
- A test that wants the evidence somewhere fast points `GO_MUTANTS_TEST_KEEP_DIR`
  at a tmpfs, which makes every kept directory a memory write and every removal
  free — at the price of losing the evidence with the reboot.
