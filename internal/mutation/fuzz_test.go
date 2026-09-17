// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

func FuzzMutantIdentity(f *testing.F) {
	f.Add("a.go", "eq-to-neq", 1, uint32(0), uint32(1), "x", "y", "z")
	f.Add("internal/a/b.go", "add-to-sub", 2, uint32(41), uint32(44), "s", "o", "r")
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
