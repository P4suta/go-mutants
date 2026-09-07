// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// preparedCatalogDomain is the domain separator hashed first for every
// [Catalog.PreparedDigest].
//
// It carries the recipe version the way the mutant identity's does: a later
// recipe becomes "go-mutants-prepared-catalog-v2", so a v1 key can never be
// mistaken for a v2 key and a consumer's cache can hold both while it migrates.
const preparedCatalogDomain = "go-mutants-prepared-catalog-v1"

// preparedDigest hashes everything that makes two prepared sessions
// interchangeable, and nothing else.
//
// [Catalog.Digest] answers "the same mutants?" and a consumer keying evidence
// on a prepared session needs "the same session?" — which is the mutant set
// plus the tree it was cut from, the toolchain that compiled it, the packages
// whose binaries exist, and, per mutant, whether it is one an execution can
// reach and one a probe can speak for. Every consumer that wanted this was
// hashing its own approximation of it, which is a second recipe nobody
// versions.
//
// The encoding is the mutant identity's: each field as a four-byte big-endian
// byte length followed by its bytes, so no two different field lists collide.
//
// The order below is the documented recipe's — the one written out on
// [Catalog.PreparedDigest] and in docs/library.md — and it is not free to
// change. It is not the order [Catalog] happens to declare its fields in, and
// nothing should make it follow that order: reordering a struct is a refactor,
// while reordering these writes is a new digest for every session anybody has
// already stored evidence against. This is a wire format. Changing it means
// changing the domain separator with it.
//
// What is left out is left out because it is a function of what is in: the
// display identity is a prefix of the ID, the coordinates and the rejection
// diagnostics are functions of the source the ID already covers, and a branch
// proof is a lemma about the same span. Hashing them would move the key every
// time a line shifted above an untouched mutant, and a key that moves for a
// session that has not changed is a cache that never hits.
func preparedDigest(c Catalog) string {
	h := sha256.New()
	// The error is impossible here: WriteLengthPrefixed refuses only a field
	// whose byte length overflows the 32-bit prefix, and every field below is a
	// digest, an import path, a rule name or a decimal count.
	write := func(field string) { _ = mutation.WriteLengthPrefixed(h, field) }

	write(preparedCatalogDomain)
	write(c.Digest)
	write(c.WorkspaceDigest)
	write(c.ModulePath)
	write(c.GoVersion)
	write(c.Toolchain)
	write(c.Profile)
	write(strconv.Itoa(len(c.TestPackages)))
	for _, pkg := range c.TestPackages {
		write(pkg)
	}
	write(strconv.Itoa(len(c.Mutants)))
	for _, mutant := range c.Mutants {
		write(mutant.ID)
		write(mutant.Package)
		write(mutantFlags(mutant))
	}
	write(strconv.Itoa(len(c.Rejections)))
	for _, rejection := range c.Rejections {
		write(rejection.ID)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// mutantFlags encodes what the prepared digest says about one mutant as three
// bytes: 'a' or '-' for [Mutant.Accepted], 'p' or '-' for [Mutant.Probed], and
// a third that is the constant 's'.
//
// Fixed width rather than a variable list, so that the field is unambiguous
// without a prefix of its own, and readable in a hexdump when somebody is
// asking why two sessions hashed differently.
//
// The third byte stays constant now that [PrepareOptions.Selection] exists, and
// that is a decision rather than an omission. [Mutant.Selected] is advisory: it
// changes nothing the engine does, and [Session.Exec] runs an unselected mutant
// exactly as it runs a selected one. What a consumer keys on this digest is
// *per-mutant evidence* — this mutant survived against this prepared tree —
// which is a fact about the tree, the toolchain and the mutant, none of which a
// selection touches. Hashing the flag would move every key the first time a
// consumer narrowed a run, so the very consumer this feature was built for would
// miss on every row it had stored and re-measure a module to write down answers
// it already had.
//
// What makes that safe is a rule the caller owns, and it is one line: never
// store "not run, out of selection" as evidence. A mutant the selection left out
// was not measured, so there is nothing about it to record; recording an absence
// as a result is the only way two sessions under one key could come to disagree.
//
// So the byte is a constant the v1 recipe reserved and did not need. It stays
// rather than being removed because removing it is a different digest for every
// session anybody has stored evidence against — see [Catalog.PreparedDigest] on
// what changing the recipe costs.
func mutantFlags(m Mutant) string {
	flags := []byte{'-', '-', 's'}
	if m.Accepted {
		flags[0] = 'a'
	}
	if m.Probed {
		flags[1] = 'p'
	}
	return string(flags)
}

// endLine returns the 1-based line the mutant's original text ends on.
//
// It is [coverage.EndLine] and not a second implementation of it, because the
// rule has one consumer-visible meaning: `go-mutants run --changed` narrows a
// catalogue by intersecting a diff's line ranges with `[Line, EndLine]`, and a
// caller narrowing the same catalogue through the library API has to get the
// same mutants. Two spellings of "count the newlines" would agree until one of
// them learned about a line terminator the other did not.
func endLine(line int, original string) int {
	return coverage.EndLine(line, original)
}

// The two halves of the promise [ProbeResult.Infected] makes are proved at two
// different points, over two different sets, and the order is load-bearing.
//
// Everything either of them can refuse is a go-mutants bug: the indices come
// from go-mutants' own probe runtime, written against the catalogue this
// session prepared. So a failure is reported as an error rather than by
// dropping the index, and that choice is the whole point — [ProbeResult.Infected]
// licenses a consumer not to execute a test, and a set quietly repaired would be
// handed over as a measurement. "No facts" would be just as wrong: a caller
// reads it as a pass that could not be vouched for and moves on. The one honest
// answer to an engine that has contradicted itself is to say so.
//
// Nil and the empty set pass both checks untouched. They are the two answers
// that mean something — no facts, and nothing was infected — and neither is a
// claim about any index.

// checkInfectedShape proves the *raw* set, as the probe runtime wrote it, is
// strictly ascending and inside a catalogue of count mutants.
//
// It runs before [filterInfected] because the filter cannot tell the two kinds
// of surprising index apart. Dropping a mutant the mutant tree rejected is its
// job; dropping one past the end of the catalogue is not, and it does so only
// because a filter must not panic. An index outside the catalogue is the
// runtime writing about a catalogue that is not this one, and checked after the
// filter it is indistinguishable from an ordinary rejection — so the pass would
// be reported as a measurement, which is precisely the silence this refuses.
func checkInfectedShape(indices []uint32, count int) error {
	for i, index := range indices {
		ascending := i == 0 || index > indices[i-1]
		if ascending && uint64(index) < uint64(count) {
			continue
		}
		return inconsistentProbe(index)
	}
	return nil
}

// checkInfectedProbed proves every index left after filtering names a mutant
// [Mutant.Probed] reports as probed.
//
// It runs after [filterInfected] because filtering is what makes the claim true
// in the ordinary case: the probe tree is instrumented from the whole
// catalogue, so its log legitimately names sites whose *mutation* did not
// compile, and those are dropped rather than refused. What is left must be
// probed, and an index that is not is the catalogue and the probe tree
// disagreeing about a mutant neither has an excuse for.
//
// Order and range are not re-checked: [filterInfected] preserves order and
// keeps only indices [checkInfectedShape] has already placed inside the
// catalogue, so the lookup below cannot be out of bounds.
func checkInfectedProbed(indices []uint32, mutants []Mutant) error {
	for _, index := range indices {
		if uint64(index) < uint64(len(mutants)) && mutants[index].Probed {
			continue
		}
		return inconsistentProbe(index)
	}
	return nil
}

// inconsistentProbe renders the one sentence both checks report, so that a
// consumer cannot tell which of them fired — the distinction is an engine
// detail, and the index is the whole of the bug report.
func inconsistentProbe(index uint32) error {
	return fmt.Errorf("gomutants: session probe: %w: index %d", ErrProbeInconsistent, index)
}
