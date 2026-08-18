// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package mutation holds the pure core of go-mutants: byte spans, the stable
// mutant identity, the operator registry, the mutant catalogue, run outcomes,
// the mutation score, and the exit-code policy.
//
// Everything here is deterministic data plus algorithms. The package performs
// no I/O, spawns no processes, reads no clock, and deliberately does not
// import go/ast or go/types: source discovery lives in internal/discover and
// rewriting lives in internal/instrument. Keeping the identity recipe and the
// scoring rules in a dependency-free package is what makes them cheap to test
// exhaustively and impossible to accidentally couple to a file system layout.
//
// Two of this package's contracts face outward and are frozen, because
// artefacts outside this process depend on them:
//
//   - The mutant ID recipe in id.go. Identities appear in reports, in the
//     outcome cache key, in `--mutant` selectors, and in the
//     `[[mutation.expect]]` ledger of user configuration files. Changing the
//     recipe renames every mutant in the world; changing a rule's version is
//     the supported way to say "this rule now emits something else".
//   - The rule table in rule.go. Rule names and versions are part of the ID,
//     so the table is append-only in spirit: a rule that changes behaviour
//     bumps its version rather than editing what an existing version means.
//
// One contract runs the other way, from the caller into this package: a Tally
// is a statement about the whole catalogue, so callers record one result per
// catalogued mutant and give every mutant the run did not execute an
// OutcomeNotRun result. `policy.require_mutants` gates on that total being
// zero, and a tally built only from executed mutants would report an empty
// `--shard k/n` or an untouched `--changed` run as a run that discovered
// nothing. See Tally and Decide.
package mutation
