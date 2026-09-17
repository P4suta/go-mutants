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

func buildCatalogue(rt *rapid.T, candidates []mutation.Candidate) *mutation.Catalog {
	builder := mutation.NewBuilder()
	if err := builder.AddAll(candidates); err != nil {
		rt.Fatalf("the builder refused a drawn candidate set: %v", err)
	}
	catalogue, err := builder.Build()
	if err != nil {
		rt.Fatalf("Build: %v", err)
	}
	return catalogue
}
