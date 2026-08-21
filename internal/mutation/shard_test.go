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

// TestShardIndexGoldenVectors pins the assignment function itself.
//
// The property tests below prove that the partition is stable and balanced,
// which any deterministic hash would satisfy — including a different one. This
// is what stops the function from changing: `shard.assignment` is "id-hash-v1"
// in every document go-mutants has ever written, and a build whose id-hash-v1
// puts a mutant in a different shard from another build's would merge two shard
// reports that had each executed the same mutant and neither executed another.
//
// So the vectors are the specification, spelled out. Changing any number here
// means minting id-hash-v2, not editing the table.
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

// TestShardIndexIsTheDocumentedFunction states the definition a second time, as
// arithmetic rather than as a table, so that the doc comment and the schema's
// description of `id-hash-v1` are checkable rather than merely asserted.
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

// TestShardIndexIsInRangeAndStable proves the two things every caller relies
// on: an index that names a real shard, and the same answer every time.
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

// TestAddingMutantsNeverReshufflesTheOthers is the property sharding exists
// for.
//
// A partition by position — every nth mutant in catalogue order — would move
// most of the catalogue every time somebody added a line to a file, so shard 3
// would measure a different set on every commit and nothing about a previous
// run's timings would predict the next one's. Assigning from the id alone means
// a mutant's shard is a fact about that mutant, and this is the statement of it:
// grow the catalogue however you like, and everything that was already in it
// stays where it was.
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

		// The catalogue changes: new mutants appear, and some of the old ones
		// are gone. Neither is allowed to move what remains.
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

// TestEveryShardGetsWork is a sanity check on the arithmetic rather than a
// statistical claim.
//
// A modulo written the wrong way round — or a hash reduced to too few bits —
// produces a partition that is technically deterministic and useless, with one
// shard doing everything. A thousand ids over four shards would have to be
// extraordinarily unlucky to leave one empty, so an empty one here means the
// function is broken rather than that the sample was small.
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

// TestShardIndexRefusesAnImpossibleTotal proves the documented answer for a
// total no shard can come from, rather than a division by zero.
func TestShardIndexRefusesAnImpossibleTotal(t *testing.T) {
	t.Parallel()

	for _, total := range []int{0, -1, -8} {
		if got := mutation.ShardIndex(strings.Repeat("ab", 32), total); got != 0 {
			t.Errorf("ShardIndex(.., %d) = %d, want 0", total, got)
		}
	}
}

// TestShardAssignmentIsVersioned holds the published name in place. It is a
// promise to every consumer that can recompute the partition, so it changes
// only when the function does — and then by a new version, never in place.
func TestShardAssignmentIsVersioned(t *testing.T) {
	t.Parallel()

	if mutation.ShardAssignment != "id-hash-v1" {
		t.Errorf("ShardAssignment = %q, want id-hash-v1", mutation.ShardAssignment)
	}
}
