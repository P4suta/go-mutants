// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation_test

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// TestACatalogueIsAFunctionOfItsCandidates is the property every stored thing
// rests on.
//
// `Catalog.Digest` keys the outcome cache and joins a shard's report to the run
// it is part of, and `report merge` refuses documents whose catalogues differ.
// All of that assumes one thing: **the same candidates always produce the same
// catalogue.** A builder that folded a map into its digest, or that let an
// allocation address reach an id, would break every one of those and would do
// it intermittently -- which is the shape of bug a table of hand-written cases
// is worst at finding.
func TestACatalogueIsAFunctionOfItsCandidates(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		candidates := drawCandidates(rt)

		first := buildCatalogue(rt, candidates)
		second := buildCatalogue(rt, candidates)

		if first.Digest() != second.Digest() {
			rt.Fatalf("two builds of one candidate set digest differently:\n\t%s\n\t%s",
				first.Digest(), second.Digest())
		}
		if first.Len() != second.Len() {
			rt.Fatalf("two builds of one candidate set hold %d and %d mutants", first.Len(), second.Len())
		}
		for i := range first.Len() {
			a, _ := first.At(i)
			b, _ := second.At(i)
			if a.ID != b.ID || a.Index != b.Index {
				rt.Fatalf("mutant %d differs between builds: %s@%d and %s@%d", i, a.ID, a.Index, b.ID, b.Index)
			}
		}
	})
}

// TestTheOrderCandidatesArriveInDoesNotChangeTheCatalogue is the same property
// from the other side.
//
// Discovery walks files in whatever order the loader returns them, and a
// catalogue that depended on that order would change its digest -- and so
// invalidate every cached outcome -- whenever the toolchain changed how it
// enumerates a package. The catalogue sorts, and this is the statement that it
// sorts *completely*: two shuffles of one set are one catalogue.
func TestTheOrderCandidatesArriveInDoesNotChangeTheCatalogue(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		candidates := drawCandidates(rt)
		shuffled := slices.Clone(candidates)
		rapid.Permutation(shuffled).Draw(rt, "order")

		if got, want := buildCatalogue(rt, shuffled).Digest(), buildCatalogue(rt, candidates).Digest(); got != want {
			rt.Fatalf("a shuffled candidate set digests %s and the original %s", got, want)
		}
	})
}

// TestEveryMutantIsInExactlyOneShard is the partition property `--shard` is
// only sound because of.
//
// A sharded run is judged by `report merge`, which refuses a set of documents
// that is not every shard exactly once and then recomputes the score from the
// merged rows. That is only a run's score if the shards *partition* the
// catalogue: a mutant in two shards would be measured twice and counted twice,
// and one in none would be missing from a document that claims to describe the
// whole run.
//
// The existing tests say an index is in range and stable. This says the indices
// cover the catalogue, once each.
func TestEveryMutantIsInExactlyOneShard(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		catalogue := buildCatalogue(rt, drawCandidates(rt))
		total := rapid.IntRange(1, 16).Draw(rt, "shards")

		counted := map[string]int{}
		for shard := 1; shard <= total; shard++ {
			for _, mutant := range catalogue.Mutants() {
				if mutation.ShardIndex(mutant.ID, total) == shard {
					counted[mutant.ID]++
				}
			}
		}
		if len(counted) != catalogue.Len() {
			rt.Fatalf("%d shards between them measured %d of %d mutants",
				total, len(counted), catalogue.Len())
		}
		for id, times := range counted {
			if times != 1 {
				rt.Fatalf("%s is in %d of the %d shards, want exactly one", id[:8], times, total)
			}
		}
	})
}

// drawCandidates draws a set of candidates that differ in the fields an
// identity is built from.
//
// The paths and spans are drawn narrowly on purpose: a wide draw produces
// candidates that never collide, and the interesting inputs are the ones that
// nearly do -- two rules at one span, one rule at two spans, the same span in
// two files.
func drawCandidates(rt *rapid.T) []mutation.Candidate {
	rules := mutation.CanonicalRegistry().Rules()
	count := rapid.IntRange(1, 12).Draw(rt, "candidates")

	seen := map[string]bool{}
	candidates := make([]mutation.Candidate, 0, count)
	for i := range count {
		path := rapid.SampledFrom([]string{"a.go", "b.go", "sub/c.go"}).Draw(rt, "path")
		rule := rules[rapid.IntRange(0, len(rules)-1).Draw(rt, "rule")]
		start := uint32(rapid.IntRange(0, 8).Draw(rt, "start"))
		end := start + uint32(rapid.IntRange(1, 4).Draw(rt, "width"))

		candidate := mutation.Candidate{
			Path:         path,
			Rule:         rule,
			Span:         mutation.Span{StartByte: start, EndByte: end},
			Original:     strings.Repeat("o", int(end-start)),
			Replacement:  rapid.SampledFrom([]string{"x", "yy", ""}).Draw(rt, "replacement"),
			SourceDigest: mutation.DigestString(path),
		}
		// A builder refuses a duplicate, which is its own tested behaviour and
		// not this property's subject.
		key := candidate.Path + "\x00" + candidate.Rule.Name + "\x00" +
			string(rune(candidate.Span.StartByte)) + string(rune(candidate.Span.EndByte))
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, candidate)
		_ = i
	}
	return candidates
}

// buildCatalogue builds one, failing the property when the builder refuses.
func buildCatalogue(rt *rapid.T, candidates []mutation.Candidate) *mutation.Catalog {
	builder := mutation.NewBuilder()
	if err := builder.AddAll(candidates); err != nil {
		// Fatal rather than skipped, and checked: the draw above is built to
		// produce sets a builder accepts, and a skip here would let a future
		// change that started refusing them turn every property in this file
		// into one that passes without running.
		rt.Fatalf("the builder refused a drawn candidate set: %v", err)
	}
	catalogue, err := builder.Build()
	if err != nil {
		rt.Fatalf("Build: %v", err)
	}
	return catalogue
}
