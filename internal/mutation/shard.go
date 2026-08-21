// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"crypto/sha256"
	"encoding/binary"
)

// ShardAssignment names the function [ShardIndex] implements, and is the value
// a report's `shard.assignment` carries.
//
// It is a published promise rather than a label. A consumer — a CI job
// rebalancing its matrix, a `report merge` proving two documents describe one
// run — has to be able to recompute the partition, and a version in the name is
// what makes it safe to ever change the function: a document written by the old
// one says so, and nothing silently reinterprets it.
const ShardAssignment = "id-hash-v1"

// ShardIndex is which of total shards owns the mutant with this id, 1-based.
//
// The function is the first eight bytes of the SHA-256 of the id's ASCII bytes,
// read big-endian, modulo total, plus one. Two properties are what it was
// chosen for, and both are the point of sharding at all:
//
//   - It depends on nothing but the id and total. Adding, removing, or
//     reordering other mutants cannot move an existing one, so a shard's work
//     does not reshuffle every time somebody edits an unrelated file — which a
//     "every nth mutant in catalogue order" assignment would do on every commit.
//   - It is a pure function of a value that is already a content digest, so two
//     machines that discovered the same catalogue partition it identically
//     without talking to each other.
//
// The id is hashed rather than merely truncated even though it is already a
// SHA-256. Truncation would tie the partition to the leading bytes of the id,
// which are also what `--mutant` prefixes and display ids are cut from, and
// tying two unrelated user-facing choices to the same bits is how one of them
// ends up constrained by the other.
//
// total must be at least 1. Zero or negative reports 0, which is not a shard
// index at all: no caller can produce one, because every path to here goes
// through a validated shard specification, and reporting an impossible index is
// better than dividing by zero inside a report builder.
func ShardIndex(id string, total int) int {
	if total < 1 {
		return 0
	}
	sum := sha256.Sum256([]byte(id))
	return int(binary.BigEndian.Uint64(sum[:8])%uint64(total)) + 1
}
