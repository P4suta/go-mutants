// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"crypto/sha256"
	"encoding/binary"
)

const ShardAssignment = "id-hash-v1"

func ShardIndex(id string, total int) int {
	if total < 1 {
		return 0
	}
	sum := sha256.Sum256([]byte(id))
	return int(binary.BigEndian.Uint64(sum[:8])%uint64(total)) + 1
}
