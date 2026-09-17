// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/P4suta/go-mutants/internal/mutation"
)

func TestShardIndexGoldenVectors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		id    string
		wants map[int]int
	}{
		{strings.Repeat("ab", 32), map[int]int{1: 1, 2: 1, 3: 2, 4: 1, 7: 3}},
		{strings.Repeat("0", 64), map[int]int{1: 1, 2: 2, 3: 2, 4: 4, 7: 7}},
		{strings.Repeat("f", 64), map[int]int{1: 1, 2: 2, 3: 1, 4: 2, 7: 5}},
		{
			"3f9c1d2e4b5a60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9",
			map[int]int{1: 1, 2: 1, 3: 2, 4: 3, 7: 4},
		},
	}
	for _, c := range cases {
		for total, want := range c.wants {
			if got := mutation.ShardIndex(c.id, total); got != want {
				t.Errorf("ShardIndex(%s.., %d) = %d, want %d", c.id[:8], total, got, want)
			}
		}
	}
}

func TestShardIndexIsTheDocumentedFunction(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		id := rapid.StringMatching(`[0-9a-f]{64}`).Draw(rt, "id")
		total := rapid.IntRange(1, 64).Draw(rt, "total")

		sum := sha256.Sum256([]byte(id))
		head, err := strconv.ParseUint(hex.EncodeToString(sum[:8]), 16, 64)
		if err != nil {
			rt.Fatalf("reading the leading eight bytes: %v", err)
		}
		want := int(head%uint64(total)) + 1
		if got := mutation.ShardIndex(id, total); got != want {
			rt.Fatalf("ShardIndex(%s.., %d) = %d, want %d", id[:8], total, got, want)
		}
	})
}

func TestShardIndexIsInRangeAndStable(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		id := rapid.StringMatching(`[0-9a-f]{64}`).Draw(rt, "id")
		total := rapid.IntRange(1, 128).Draw(rt, "total")

		first := mutation.ShardIndex(id, total)
		if first < 1 || first > total {
			rt.Fatalf("ShardIndex(.., %d) = %d, which is not a shard of %d", total, first, total)
		}
		if second := mutation.ShardIndex(id, total); second != first {
			rt.Fatalf("ShardIndex is not a function: %d then %d", first, second)
		}
	})
}

func TestAddingMutantsNeverReshufflesTheOthers(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		total := rapid.IntRange(1, 16).Draw(rt, "total")
		before := rapid.SliceOfNDistinct(
			rapid.StringMatching(`[0-9a-f]{64}`), 1, 40,
			func(s string) string { return s },
		).Draw(rt, "catalogue")

		assigned := make(map[string]int, len(before))
		for _, id := range before {
			assigned[id] = mutation.ShardIndex(id, total)
		}

		added := rapid.SliceOfNDistinct(
			rapid.StringMatching(`[0-9a-f]{64}`), 0, 40,
			func(s string) string { return s },
		).Draw(rt, "added")
		keep := rapid.IntRange(0, len(before)).Draw(rt, "kept")
		after := append(append([]string{}, before[:keep]...), added...)

		for _, id := range after {
			was, known := assigned[id]
			if !known {
				continue
			}
			if now := mutation.ShardIndex(id, total); now != was {
				rt.Fatalf("mutant %s.. moved from shard %d to %d when the catalogue changed", id[:8], was, now)
			}
		}
	})
}

func TestEveryShardGetsWork(t *testing.T) {
	t.Parallel()

	const total = 4
	counts := make(map[int]int, total)
	for i := range 1000 {
		sum := sha256.Sum256([]byte("mutant-" + strconv.Itoa(i)))
		counts[mutation.ShardIndex(hex.EncodeToString(sum[:]), total)]++
	}
	for index := 1; index <= total; index++ {
		if counts[index] == 0 {
			t.Errorf("shard %d of %d was assigned nothing at all: %v", index, total, counts)
		}
	}
}

func TestShardIndexRefusesAnImpossibleTotal(t *testing.T) {
	t.Parallel()

	for _, total := range []int{0, -1, -8} {
		if got := mutation.ShardIndex(strings.Repeat("ab", 32), total); got != 0 {
			t.Errorf("ShardIndex(.., %d) = %d, want 0", total, got)
		}
	}
}

func TestShardAssignmentIsVersioned(t *testing.T) {
	t.Parallel()

	if mutation.ShardAssignment != "id-hash-v1" {
		t.Errorf("ShardAssignment = %q, want id-hash-v1", mutation.ShardAssignment)
	}
}
