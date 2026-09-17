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

const preparedCatalogDomain = "go-mutants-prepared-catalog-v1"

func preparedDigest(c Catalog) string {
	h := sha256.New()
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

func endLine(line int, original string) int {
	return coverage.EndLine(line, original)
}

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

func checkInfectedProbed(indices []uint32, mutants []Mutant) error {
	for _, index := range indices {
		if uint64(index) < uint64(len(mutants)) && mutants[index].Probed {
			continue
		}
		return inconsistentProbe(index)
	}
	return nil
}

func inconsistentProbe(index uint32) error {
	return fmt.Errorf("gomutants: session probe: %w: index %d", ErrProbeInconsistent, index)
}
