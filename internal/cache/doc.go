// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package cache stores the outcome of one mutant so that a later run of the
// same code, by the same build, against the same command, need not measure it
// again.
//
// # What a cached outcome claims
//
// An entry says: with this exact tree instrumented from this exact catalogue,
// this exact go-mutants binary ran this exact command against this exact Go
// toolchain under this timeout configuration in this environment, and the
// mutant with this id was killed, survived, or confirmed to hang. Every clause
// of that sentence is a field of
// [Context] and therefore of the key, and the entry is filed under the key; a
// run that differs in any of them looks in a different directory and finds
// nothing. See [Context.Key] for the frozen recipe.
//
// The consequence worth stating plainly is that nothing is ever invalidated.
// Editing a source file changes the workspace digest, which changes the key,
// which changes the directory — so the old entries are not wrong, they are
// simply unreachable, and `cache gc` removes them when they get old. There is
// no invalidation pass to get wrong, and no window in which a stale entry is
// still reachable.
//
// One thing is deliberately judged rather than keyed: the per-mutant timeout.
// A derived one is a wall-clock measurement, so a key over it would give every
// run of any non-trivial project its own empty directory. The entry records the
// bound it was measured under instead, and a lookup refuses one that could not
// have produced its outcome under this run's bound. See [Entry.UsableUnder],
// which is more precise than a key would have been as well as more useful.
//
// # What is never cached
//
// [Cacheable] is the whole rule and its documentation is the whole argument:
// killed, survived, and confirmed timed-out are reusable, and inconclusive,
// errored and not-run are not. Two further exclusions live outside this
// package, because they are properties of the run rather than of the outcome:
//
//   - a mutant no test binary covers, which internal/engine settles before the
//     cache is consulted, because the coverage pass fails open and the coverage
//     mode is not in the key;
//   - a mutant named in the `[[mutation.expect]]` ledger, which
//     `docs/configuration.md` promises is measured on every invocation. An
//     expectation is evidence to check, and evidence that is copied from
//     yesterday's answer has not been checked.
//
// # Failing open
//
// Inside a run, nothing this package reports is fatal. A cache that cannot be
// opened, an entry that cannot be read, an outcome that cannot be written: each
// is a warning and a run that measures more than it had to. That is the same
// judgement internal/coverage makes, for the same reason — the cache is an
// optimisation, and a run without it reaches exactly the verdicts it would have
// reached — and it is the opposite of the judgement made for `--changed`, which
// is the question the user asked rather than a way of answering it faster.
//
// `cache status`, `cache gc` and `cache clean` are the exception: there the
// cache is the subject rather than an optimisation, so a failure is an error.
//
// # Sharing a directory
//
// The store lives inside the run history's workspace directory, under one
// ownership marker, and claims it through [report.History.Claim] rather than
// through a second implementation of the same dance. The marker is what makes
// deleting files in the operating system's cache directory defensible: a
// directory with no marker, or with one naming another workspace, is refused.
// See [report.History] for the argument and the race it settles.
package cache
