// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// FuzzMutantIdentity is the fuzz half of what the golden vectors pin.
//
// The golden vectors say that nine specific identities hash to nine specific
// strings, which is what freezes the recipe. What they cannot say is that the
// recipe is *injective* over inputs nobody wrote down -- and injectivity is the
// property the whole design rests on. An outcome cache is keyed on the id; a
// `[[mutation.expect]]` row names one; `--mutant` resolves a prefix against
// them. Two different mutants sharing an id would let a cache answer for the
// wrong one.
//
// The length prefixes in the recipe exist for exactly this, and the doc comment
// says so: "no combination of path and rule name can ever be re-parenthesised
// into a different identity". This is that claim, fuzzed.
func FuzzMutantIdentity(f *testing.F) {
	f.Add("a.go", "eq-to-neq", 1, uint32(0), uint32(1), "x", "y", "z")
	f.Add("internal/a/b.go", "add-to-sub", 2, uint32(41), uint32(44), "s", "o", "r")
	// The shapes a length prefix is there to separate: a name that could be
	// read as part of the path before it, and a path that could swallow it.
	f.Add("a", "b-c", 1, uint32(0), uint32(1), "d", "e", "f")
	f.Add("ab", "-c", 1, uint32(0), uint32(1), "d", "e", "f")
	f.Add("a/b", "c", 1, uint32(0), uint32(1), "d", "e", "f")
	f.Add("a", "/b-c", 1, uint32(0), uint32(1), "d", "e", "f")
	f.Add("", "", 1, uint32(0), uint32(0), "", "", "")

	seen := map[string]mutation.Identity{}
	f.Fuzz(func(t *testing.T, path, rule string, version int, start, end uint32, source, original, replacement string) {
		identity := mutation.Identity{
			Path:              path,
			RuleName:          rule,
			RuleVersion:       version,
			Span:              mutation.Span{StartByte: start, EndByte: end},
			SourceDigest:      hexish(source),
			OriginalDigest:    hexish(original),
			ReplacementDigest: hexish(replacement),
		}
		id, err := identity.ID()
		if err != nil {
			// A refusal is the other half of the contract: an identity that
			// does not validate has no id, rather than a plausible one.
			if id != "" {
				t.Fatalf("ID() refused %+v and returned %q anyway", identity, id)
			}
			return
		}
		if len(id) != 64 {
			t.Fatalf("ID() = %q, want 64 hex characters", id)
		}
		if again, _ := identity.ID(); again != id {
			t.Fatalf("ID() is not a function: %q then %q", id, again)
		}
		if held, ok := seen[id]; ok && held != identity {
			t.Fatalf("two identities share one id:\n\t%+v\n\t%+v", held, identity)
		}
		seen[id] = identity
	})
}

// hexish turns arbitrary bytes into something shaped like the digest the recipe
// wants, so that the fuzzer spends its budget on the fields that vary rather
// than on rediscovering that a digest has to be 64 hex characters.
func hexish(s string) string {
	const digits = "0123456789abcdef"
	var b strings.Builder
	for i := range 64 {
		if i < len(s) {
			b.WriteByte(digits[s[i]%16])
			continue
		}
		b.WriteByte('0')
	}
	return b.String()
}
