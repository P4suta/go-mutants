// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The catalogue fixture: two files, three candidates, real byte offsets.
const (
	aSource = "package a\n\nvar flag = true\n\nvar same = x == y\n"
	bSource = "package b\n\nvar n = x + y\n"
)

// Golden identities for the fixture, produced by the same independent
// implementation of the recipe that backs the vectors in id_test.go.
const (
	aTrueID   = "458ab0aa4a5ed705327e39c5bcd384e5411106f9c7f37ef24d909d47ac65ce27"
	aEqID     = "8baf253cad6cb0e68984d68936933bd1d823d0e58b8f28116b7170d1d987550b"
	bAddID    = "cf60b5529224aae2f0cf4da5f2e54169f180a7e572a8e0995b3062aac7c9e926"
	catDigest = "6ebb649e25651f6c4f0c9e781f5f01ad8ba66edb4cc24f8f006b8b3d565eb424"
)

// newCandidate builds a candidate and proves the span really covers the
// original text in the given source, so a fixture can never drift into
// testing a mutation of bytes that are not there.
func newCandidate(t *testing.T, path, ruleName string, start, end uint32, source, original, replacement string) Candidate {
	t.Helper()

	if got := source[start:end]; got != original {
		t.Fatalf("fixture %s[%d:%d] = %q, want %q", path, start, end, got, original)
	}
	return Candidate{
		Path:         path,
		Rule:         mustRule(t, ruleName),
		Span:         Span{StartByte: start, EndByte: end},
		Original:     original,
		Replacement:  replacement,
		SourceDigest: DigestString(source),
	}
}

func fixtureCandidates(t *testing.T) []Candidate {
	t.Helper()

	return []Candidate{
		newCandidate(t, "a.go", "true-to-false", 22, 26, aSource, "true", "false"),
		newCandidate(t, "a.go", "eq-to-neq", 41, 43, aSource, "==", "!="),
		newCandidate(t, "b.go", "add-to-sub", 21, 22, bSource, "+", "-"),
	}
}

func buildCatalog(t *testing.T, candidates []Candidate) *Catalog {
	t.Helper()

	b := NewBuilder()
	if err := b.AddAll(candidates); err != nil {
		t.Fatalf("AddAll() error = %v", err)
	}
	catalog, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	return catalog
}

func TestCatalogGoldenIdentitiesAndOrder(t *testing.T) {
	t.Parallel()

	candidates := fixtureCandidates(t)
	catalog := buildCatalog(t, candidates)

	want := []Mutant{
		{Index: 0, ID: aTrueID, DisplayID: aTrueID[:DisplayIDLength], Candidate: candidates[0]},
		{Index: 1, ID: aEqID, DisplayID: aEqID[:DisplayIDLength], Candidate: candidates[1]},
		{Index: 2, ID: bAddID, DisplayID: bAddID[:DisplayIDLength], Candidate: candidates[2]},
	}
	if diff := cmp.Diff(want, catalog.Mutants()); diff != "" {
		t.Fatalf("catalogue mismatch (-want +got):\n%s", diff)
	}
	if got := catalog.Digest(); got != catDigest {
		t.Errorf("Digest() = %s, want %s", got, catDigest)
	}
	if got := catalog.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}
	if catalog.Empty() {
		t.Error("Empty() = true for a three-mutant catalogue")
	}
	if got := catalog.Duplicates(); len(got) != 0 {
		t.Errorf("Duplicates() = %v, want none", got)
	}
	if got := catalog.DisplayLength(); got != DisplayIDLength {
		t.Errorf("DisplayLength() = %d, want %d", got, DisplayIDLength)
	}
}

func TestEmptyCatalog(t *testing.T) {
	t.Parallel()

	catalog, err := NewBuilder().Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !catalog.Empty() || catalog.Len() != 0 {
		t.Fatalf("Build() of no candidates = %d mutants, want an empty catalogue", catalog.Len())
	}
	// Even an empty catalogue has an identity, so a run that discovered
	// nothing can still be compared against another run that discovered
	// nothing.
	if !IsDigest(catalog.Digest()) {
		t.Errorf("Digest() = %q, want a digest", catalog.Digest())
	}
	if catalog.Digest() == catDigest {
		t.Error("the empty catalogue must not share the fixture's digest")
	}
}

// TestCatalogIsInsertionOrderIndependent is the determinism contract: two
// discovery passes that find the same candidates in different orders must
// produce byte-identical catalogues, because the dense index they assign ends
// up baked into the generated runtime array.
func TestCatalogIsInsertionOrderIndependent(t *testing.T) {
	t.Parallel()

	candidates := manyCandidates(t)
	want := buildCatalog(t, candidates)

	rng := rand.New(rand.NewPCG(0x9e3779b9, 0x7f4a7c15))
	for i := range 25 {
		shuffled := slices.Clone(candidates)
		rng.Shuffle(len(shuffled), func(x, y int) {
			shuffled[x], shuffled[y] = shuffled[y], shuffled[x]
		})
		got := buildCatalog(t, shuffled)

		if diff := cmp.Diff(want.Mutants(), got.Mutants()); diff != "" {
			t.Fatalf("shuffle %d produced a different catalogue (-want +got):\n%s", i, diff)
		}
		if diff := cmp.Diff(want.Duplicates(), got.Duplicates()); diff != "" {
			t.Fatalf("shuffle %d produced different duplicates (-want +got):\n%s", i, diff)
		}
		if got.Digest() != want.Digest() {
			t.Fatalf("shuffle %d produced digest %s, want %s", i, got.Digest(), want.Digest())
		}
	}
}

// manyCandidates returns a candidate set with several files, several rules,
// interleaved spans, and two deduplication conflicts.
func manyCandidates(t *testing.T) []Candidate {
	t.Helper()

	const (
		// Offsets 0..3 hold "true", 4..5 hold "==", 6 holds "+".
		source = "true==+ !x && y"
	)
	trueSpan := Span{StartByte: 0, EndByte: 4}
	eqSpan := Span{StartByte: 4, EndByte: 6}
	addSpan := Span{StartByte: 6, EndByte: 7}
	notSpan := Span{StartByte: 8, EndByte: 10}
	andSpan := Span{StartByte: 11, EndByte: 13}

	digest := DigestString(source)
	mk := func(path, ruleName string, span Span, original, replacement string) Candidate {
		t.Helper()
		if got := source[span.StartByte:span.EndByte]; got != original {
			t.Fatalf("fixture source[%v] = %q, want %q", span, got, original)
		}
		return Candidate{
			Path:         path,
			Rule:         mustRule(t, ruleName),
			Span:         span,
			Original:     original,
			Replacement:  replacement,
			SourceDigest: digest,
		}
	}

	var out []Candidate
	for _, path := range []string{"internal/b.go", "internal/a.go", "cmd/main.go"} {
		out = append(out,
			mk(path, "true-to-false", trueSpan, "true", "false"),
			mk(path, "eq-to-neq", eqSpan, "==", "!="),
			mk(path, "add-to-sub", addSpan, "+", "-"),
			mk(path, "remove-negation", notSpan, "!x", " x"),
			mk(path, "and-to-or", andSpan, "&&", "||"),
			// A second rule proposing the byte-identical edit at the "&&"
			// site: one of the two has to lose deduplication.
			mk(path, "negate-condition", andSpan, "&&", "||"),
			// And an exact repeat of one candidate, as a duplicated
			// discovery pass would produce.
			mk(path, "true-to-false", trueSpan, "true", "false"),
		)
	}
	return out
}

func TestDedupKeepsTheMoreLocalRule(t *testing.T) {
	t.Parallel()

	const source = "a && b"
	span := Span{StartByte: 2, EndByte: 4}
	digest := DigestString(source)

	// "and-to-or" sits in boolean-connective at table position 5;
	// "negate-condition", which can produce the byte-identical edit here,
	// sits earlier in condition-negation at position 2. The earlier table
	// row wins.
	local := Candidate{
		Path: "x.go", Rule: mustRule(t, "negate-condition"), Span: span,
		Original: "&&", Replacement: "||", SourceDigest: digest,
	}
	broad := Candidate{
		Path: "x.go", Rule: mustRule(t, "and-to-or"), Span: span,
		Original: "&&", Replacement: "||", SourceDigest: digest,
	}

	localPos, _ := CanonicalRegistry().Position(local.Rule.Name)
	broadPos, _ := CanonicalRegistry().Position(broad.Rule.Name)
	if localPos >= broadPos {
		t.Fatalf("fixture assumes %q precedes %q in the table", local.Rule.Name, broad.Rule.Name)
	}

	for _, order := range [][]Candidate{{local, broad}, {broad, local}} {
		catalog := buildCatalog(t, order)

		if got := catalog.Len(); got != 1 {
			t.Fatalf("Len() = %d, want 1", got)
		}
		kept, _ := catalog.At(0)
		if kept.Rule != local.Rule {
			t.Errorf("kept %v, want the more local %v", kept.Rule, local.Rule)
		}

		duplicates := catalog.Duplicates()
		if len(duplicates) != 1 {
			t.Fatalf("Duplicates() = %d entries, want 1", len(duplicates))
		}
		dup := duplicates[0]
		if dup.Reason != DuplicateShadowed {
			t.Errorf("Reason = %q, want %q", dup.Reason, DuplicateShadowed)
		}
		if dup.Dropped.Rule != broad.Rule {
			t.Errorf("dropped %v, want %v", dup.Dropped.Rule, broad.Rule)
		}
		if dup.WinnerRule != local.Rule || dup.WinnerID != kept.ID {
			t.Errorf("winner = %v/%s, want %v/%s", dup.WinnerRule, dup.WinnerID, local.Rule, kept.ID)
		}
		if dup.DroppedID == dup.WinnerID {
			t.Error("a shadowed duplicate has its own identity; the ids should differ")
		}
		if !IsID(dup.DroppedID) {
			t.Errorf("DroppedID = %q, want an id", dup.DroppedID)
		}
	}
}

func TestDedupRecordsIdenticalCandidates(t *testing.T) {
	t.Parallel()

	c := fixtureCandidates(t)[0]
	catalog := buildCatalog(t, []Candidate{c, c, c})

	if got := catalog.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
	duplicates := catalog.Duplicates()
	if len(duplicates) != 2 {
		t.Fatalf("Duplicates() = %d entries, want 2", len(duplicates))
	}
	for i, dup := range duplicates {
		if dup.Reason != DuplicateIdentical {
			t.Errorf("duplicate %d reason = %q, want %q", i, dup.Reason, DuplicateIdentical)
		}
		if dup.DroppedID != dup.WinnerID {
			t.Errorf("duplicate %d: identical candidates must share an id, got %s and %s",
				i, dup.DroppedID, dup.WinnerID)
		}
	}
}

// TestDedupIgnoresTheRuleInItsKey pins the other half of the dedup contract:
// two rules that produce *different* replacements at one span are two
// mutants, not a duplicate.
func TestDedupIgnoresTheRuleInItsKey(t *testing.T) {
	t.Parallel()

	// Two return-replacement rules at one `return x` site propose different
	// replacements, which makes them two mutants sharing a span rather than
	// a duplicate. The dedup key is the edit, not the site.
	const source = "\treturn x\n"
	span := Span{StartByte: 1, EndByte: 9}
	digest := DigestString(source)
	if got := source[span.StartByte:span.EndByte]; got != "return x" {
		t.Fatalf("fixture source[%v] = %q", span, got)
	}

	first := Candidate{
		Path: "x.go", Rule: mustRule(t, "return-zero-numeric"), Span: span,
		Original: "return x", Replacement: "return 0", SourceDigest: digest,
	}
	second := Candidate{
		Path: "x.go", Rule: mustRule(t, "return-nil"), Span: span,
		Original: "return x", Replacement: "return nil", SourceDigest: digest,
	}

	catalog := buildCatalog(t, []Candidate{first, second})
	if got := catalog.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2: different replacements are different mutants", got)
	}
	if got := len(catalog.Duplicates()); got != 0 {
		t.Fatalf("Duplicates() = %d entries, want 0", got)
	}
}

// TestCatalogOrderBreaksTiesOnTheReplacement covers the last tiebreak in the
// canonical order: one rule proposing two different replacements at one span
// still has to land in a stable order.
func TestCatalogOrderBreaksTiesOnTheReplacement(t *testing.T) {
	t.Parallel()

	const source = "\treturn x\n"
	span := Span{StartByte: 1, EndByte: 9}
	digest := DigestString(source)

	base := Candidate{
		Path: "x.go", Rule: mustRule(t, "return-zero-numeric"), Span: span,
		Original: "return x", SourceDigest: digest,
	}
	zero, one := base, base
	zero.Replacement = "return 0"
	one.Replacement = "return 1"

	forward := buildCatalog(t, []Candidate{zero, one})
	backward := buildCatalog(t, []Candidate{one, zero})

	if diff := cmp.Diff(forward.Mutants(), backward.Mutants()); diff != "" {
		t.Fatalf("insertion order changed the catalogue (-forward +backward):\n%s", diff)
	}
	if forward.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", forward.Len())
	}
	first, _ := forward.At(0)
	if first.Replacement != "return 0" {
		t.Errorf("first mutant replaces with %q, want the lexicographically smaller %q",
			first.Replacement, "return 0")
	}
}

func TestDisplayLengthFallsBackToTheDefault(t *testing.T) {
	t.Parallel()

	for _, length := range []int{0, -1, IDHexLength + 1} {
		b := NewBuilder().setDisplayLength(length)
		if err := b.AddAll(fixtureCandidates(t)); err != nil {
			t.Fatalf("AddAll() error = %v", err)
		}
		catalog, err := b.Build()
		if err != nil {
			t.Fatalf("Build() with display length %d error = %v", length, err)
		}
		first, _ := catalog.At(0)
		if len(first.DisplayID) != DisplayIDLength {
			t.Errorf("display length %d produced %q, want %d characters",
				length, first.DisplayID, DisplayIDLength)
		}
		// The catalogue reports the length it actually proved unique, not
		// the out-of-range one it was asked for.
		if got := catalog.DisplayLength(); got != DisplayIDLength {
			t.Errorf("DisplayLength() = %d after a request for %d, want %d",
				got, length, DisplayIDLength)
		}
	}
}

func TestDisplayIDCollisionIsReported(t *testing.T) {
	t.Parallel()

	// Twenty mutants truncated to a single hex character cannot all be
	// unique: sixteen buckets, twenty ids. The pigeonhole makes the test
	// deterministic without needing a real SHA-256 collision.
	const count = 20
	source := strings.Repeat("true", count)
	digest := DigestString(source)

	b := NewBuilder().setDisplayLength(1)
	for i := range count {
		start := uint32(i * 4)
		if err := b.Add(Candidate{
			Path:         "collide.go",
			Rule:         mustRule(t, "true-to-false"),
			Span:         Span{StartByte: start, EndByte: start + 4},
			Original:     "true",
			Replacement:  "false",
			SourceDigest: digest,
		}); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	catalog, err := b.Build()
	if err == nil {
		t.Fatalf("Build() = %d mutants, want a display collision error", catalog.Len())
	}

	var collision *DisplayCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("Build() error = %v (%T), want *DisplayCollisionError", err, err)
	}
	if collision.Length != 1 {
		t.Errorf("Length = %d, want 1", collision.Length)
	}
	if len(collision.Collisions) == 0 {
		t.Fatal("Collisions is empty")
	}

	total := 0
	for i, c := range collision.Collisions {
		if len(c.DisplayID) != 1 {
			t.Errorf("collision %d DisplayID = %q, want one character", i, c.DisplayID)
		}
		if len(c.IDs) < 2 {
			t.Errorf("collision %d lists %d ids, want at least 2", i, len(c.IDs))
		}
		if !slices.IsSorted(c.IDs) {
			t.Errorf("collision %d ids are not sorted: %v", i, c.IDs)
		}
		for _, id := range c.IDs {
			if !strings.HasPrefix(id, c.DisplayID) {
				t.Errorf("collision %d: %s does not start with %q", i, id, c.DisplayID)
			}
		}
		total += len(c.IDs)
	}
	if total > count {
		t.Errorf("collisions name %d ids, but only %d mutants exist", total, count)
	}
	if !slices.IsSortedFunc(collision.Collisions, func(x, y DisplayCollision) int {
		return strings.Compare(x.DisplayID, y.DisplayID)
	}) {
		t.Error("collisions are not sorted by display id")
	}
	if !strings.Contains(collision.Error(), "display id collision") {
		t.Errorf("Error() = %q, want it to mention a display id collision", collision.Error())
	}
}

func TestDisplayIDsAreUniqueAtTheDefaultLength(t *testing.T) {
	t.Parallel()

	catalog := buildCatalog(t, manyCandidates(t))
	seen := make(map[string]bool, catalog.Len())
	for _, m := range catalog.Mutants() {
		if len(m.DisplayID) != DisplayIDLength {
			t.Fatalf("DisplayID = %q, want %d characters", m.DisplayID, DisplayIDLength)
		}
		if !strings.HasPrefix(m.ID, m.DisplayID) {
			t.Fatalf("DisplayID %q is not a prefix of %q", m.DisplayID, m.ID)
		}
		if seen[m.DisplayID] {
			t.Fatalf("display id %q appears twice", m.DisplayID)
		}
		seen[m.DisplayID] = true
	}
}

// TestDeduplicationPrecedesTheCollisionCheck guards the Build pipeline order:
// identical candidates share one full ID, so checking display prefixes first
// would report every duplicated discovery as a hash collision.
func TestDeduplicationPrecedesTheCollisionCheck(t *testing.T) {
	t.Parallel()

	c := fixtureCandidates(t)[0]
	b := NewBuilder().setDisplayLength(1)
	if err := b.AddAll([]Candidate{c, c}); err != nil {
		t.Fatalf("AddAll() error = %v", err)
	}
	catalog, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error = %v, want the duplicate to be collapsed", err)
	}
	if catalog.Len() != 1 || len(catalog.Duplicates()) != 1 {
		t.Fatalf("Build() = %d mutants and %d duplicates, want 1 and 1",
			catalog.Len(), len(catalog.Duplicates()))
	}
}

func TestBuilderRejectsIncoherentCandidates(t *testing.T) {
	t.Parallel()

	base := fixtureCandidates(t)[0]

	tests := []struct {
		name    string
		mutate  func(Candidate) Candidate
		wantErr error
	}{
		{
			name:    "original shorter than the span",
			mutate:  func(c Candidate) Candidate { c.Original = c.Original[:len(c.Original)-1]; return c },
			wantErr: ErrOriginalLengthMismatch,
		},
		{
			name:    "original longer than the span",
			mutate:  func(c Candidate) Candidate { c.Span.EndByte = 25; return c },
			wantErr: ErrOriginalLengthMismatch,
		},
		{
			// A no-op splices the file back into itself: it compiles by
			// construction, survives every test, and drags the score down
			// for a mutation that does not exist.
			name:    "replacement identical to the original",
			mutate:  func(c Candidate) Candidate { c.Replacement = c.Original; return c },
			wantErr: ErrNoOpReplacement,
		},
		{
			// The same no-op wearing a different hat. An empty span is a
			// legal insertion point, so the length check is satisfied;
			// deleting nothing from it is still not a mutation.
			name: "empty span that deletes nothing",
			mutate: func(c Candidate) Candidate {
				c.Span = Span{StartByte: c.Span.StartByte, EndByte: c.Span.StartByte}
				c.Original, c.Replacement = "", ""
				return c
			},
			wantErr: ErrNoOpReplacement,
		},
		{
			name:    "unnormalized path",
			mutate:  func(c Candidate) Candidate { c.Path = `internal\a.go`; return c },
			wantErr: ErrUnnormalizedPath,
		},
		{
			name:    "source digest is not a digest",
			mutate:  func(c Candidate) Candidate { c.SourceDigest = "not-a-digest"; return c },
			wantErr: ErrInvalidDigest,
		},
		{
			name: "rule is not registered",
			mutate: func(c Candidate) Candidate {
				c.Rule = Rule{Family: "invented", Name: "true-to-maybe", Version: 1, Tier: TierBalanced}
				return c
			},
			wantErr: ErrUnknownRule,
		},
		{
			name: "rule version disagrees with the registry",
			mutate: func(c Candidate) Candidate {
				c.Rule.Version = 7
				return c
			},
			wantErr: ErrRuleMismatch,
		},
		{
			name: "rule has no family",
			mutate: func(c Candidate) Candidate {
				c.Rule.Family = ""
				return c
			},
			wantErr: ErrInvalidRuleName,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := NewBuilder().Add(tc.mutate(base)); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Add() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestBuilderRejectsContradictoryFacts(t *testing.T) {
	t.Parallel()

	candidates := fixtureCandidates(t)

	t.Run("two digests for one file", func(t *testing.T) {
		t.Parallel()

		b := NewBuilder()
		if err := b.Add(candidates[0]); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		conflicting := candidates[1]
		conflicting.SourceDigest = DigestString("a different file")
		if err := b.Add(conflicting); !errors.Is(err, ErrSourceDigestConflict) {
			t.Fatalf("Add() error = %v, want ErrSourceDigestConflict", err)
		}
	})

	t.Run("two originals for one span", func(t *testing.T) {
		t.Parallel()

		b := NewBuilder()
		if err := b.Add(candidates[0]); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		conflicting := candidates[0]
		conflicting.Rule = mustRule(t, "false-to-true")
		conflicting.Original = "flag"
		conflicting.Replacement = "true"
		if err := b.Add(conflicting); !errors.Is(err, ErrOriginalConflict) {
			t.Fatalf("Add() error = %v, want ErrOriginalConflict", err)
		}
	})
}

func TestCatalogLookups(t *testing.T) {
	t.Parallel()

	catalog := buildCatalog(t, fixtureCandidates(t))
	first, ok := catalog.At(0)
	if !ok {
		t.Fatal("At(0) not found")
	}

	if got, ok := catalog.ByID(first.ID); !ok || got != first {
		t.Errorf("ByID(%s) = %v, %v", first.ID, got, ok)
	}
	if got, ok := catalog.ByDisplayID(first.DisplayID); !ok || got != first {
		t.Errorf("ByDisplayID(%s) = %v, %v", first.DisplayID, got, ok)
	}
	if got, ok := catalog.ByIndex(first.Index); !ok || got != first {
		t.Errorf("ByIndex(%d) = %v, %v", first.Index, got, ok)
	}

	if _, ok := catalog.At(-1); ok {
		t.Error("At(-1) should not resolve")
	}
	if _, ok := catalog.At(catalog.Len()); ok {
		t.Error("At(Len()) should not resolve")
	}
	if _, ok := catalog.ByIndex(uint32(catalog.Len())); ok {
		t.Error("ByIndex past the end should not resolve")
	}
	if _, ok := catalog.ByID(strings.Repeat("0", IDHexLength)); ok {
		t.Error("ByID of an unknown id should not resolve")
	}
	if _, ok := catalog.ByDisplayID("nope"); ok {
		t.Error("ByDisplayID of an unknown display id should not resolve")
	}

	// Indices are dense and follow catalogue order, because they index the
	// generated runtime's activation array directly.
	for i, m := range catalog.Mutants() {
		if m.Index != uint32(i) {
			t.Errorf("mutant %d has index %d", i, m.Index)
		}
	}
}

func TestResolvePrefix(t *testing.T) {
	t.Parallel()

	catalog := buildCatalog(t, fixtureCandidates(t))
	target, _ := catalog.At(1)

	for _, prefix := range []string{target.ID, target.DisplayID, target.ID[:8], target.ID[:MinPrefixLength]} {
		got, err := catalog.ResolvePrefix(prefix)
		if err != nil {
			t.Fatalf("ResolvePrefix(%q) error = %v", prefix, err)
		}
		if got.ID != target.ID {
			t.Errorf("ResolvePrefix(%q) = %s, want %s", prefix, got.ID, target.ID)
		}
	}

	tests := []struct {
		name    string
		prefix  string
		wantErr error
	}{
		{name: "too short", prefix: target.ID[:MinPrefixLength-1], wantErr: ErrInvalidPrefix},
		{name: "empty", prefix: "", wantErr: ErrInvalidPrefix},
		{name: "not hex", prefix: "zzzz", wantErr: ErrInvalidPrefix},
		{name: "uppercase", prefix: strings.ToUpper(target.ID[:8]), wantErr: ErrInvalidPrefix},
		{name: "too long", prefix: target.ID + "0", wantErr: ErrInvalidPrefix},
		{name: "unknown", prefix: strings.Repeat("0", 16), wantErr: ErrMutantNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := catalog.ResolvePrefix(tc.prefix); !errors.Is(err, tc.wantErr) {
				t.Fatalf("ResolvePrefix(%q) error = %v, want %v", tc.prefix, err, tc.wantErr)
			}
		})
	}
}

// TestResolvePrefixRefusesToGuess covers the ambiguous case. The fixture is
// large enough that two of its identities share their first four hex
// characters; because the identities are fixed, so is the collision, and the
// test neither skips nor flakes.
func TestResolvePrefixRefusesToGuess(t *testing.T) {
	t.Parallel()

	const count = 1000
	source := strings.Repeat("true", count)
	digest := DigestString(source)

	b := NewBuilder()
	for i := range count {
		start := uint32(i * 4)
		if err := b.Add(Candidate{
			Path:         "wide.go",
			Rule:         mustRule(t, "true-to-false"),
			Span:         Span{StartByte: start, EndByte: start + 4},
			Original:     "true",
			Replacement:  "false",
			SourceDigest: digest,
		}); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}
	catalog, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	ids := make([]string, 0, catalog.Len())
	for _, m := range catalog.Mutants() {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)

	ambiguous := ""
	for i := 1; i < len(ids); i++ {
		if shared := commonPrefix(ids[i-1], ids[i]); len(shared) >= MinPrefixLength {
			ambiguous = shared
			break
		}
	}
	if ambiguous == "" {
		t.Fatalf("no two of %d identities share %d leading characters; widen the fixture",
			len(ids), MinPrefixLength)
	}

	_, err = catalog.ResolvePrefix(ambiguous)
	if !errors.Is(err, ErrAmbiguousPrefix) {
		t.Fatalf("ResolvePrefix(%q) error = %v, want ErrAmbiguousPrefix", ambiguous, err)
	}
	// The error names the candidates rather than picking one.
	if !strings.Contains(err.Error(), "matches") {
		t.Errorf("Error() = %q, want it to report how many mutants matched", err)
	}
}

func commonPrefix(a, b string) string {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}

func TestCatalogAccessorsCopy(t *testing.T) {
	t.Parallel()

	catalog := buildCatalog(t, fixtureCandidates(t))
	mutants := catalog.Mutants()
	mutants[0].ID = "clobbered"
	if got, _ := catalog.At(0); got.ID == "clobbered" {
		t.Fatal("Mutants() handed out the catalogue's own backing array")
	}
}

func TestCandidateIdentityRoundTrip(t *testing.T) {
	t.Parallel()

	c := fixtureCandidates(t)[0]
	id, err := c.ID()
	if err != nil {
		t.Fatalf("ID() error = %v", err)
	}
	if id != aTrueID {
		t.Fatalf("ID() = %s, want %s", id, aTrueID)
	}

	identity := c.Identity()
	if identity.OriginalDigest != DigestString("true") {
		t.Errorf("OriginalDigest = %s, want the digest of the original text", identity.OriginalDigest)
	}
	if identity.ReplacementDigest != DigestString("false") {
		t.Errorf("ReplacementDigest = %s, want the digest of the replacement text", identity.ReplacementDigest)
	}
	if identity.RuleName != "true-to-false" || identity.RuleVersion != 1 {
		t.Errorf("identity rule = %s@%d, want true-to-false@1", identity.RuleName, identity.RuleVersion)
	}
}

func TestBuilderLen(t *testing.T) {
	t.Parallel()

	b := NewBuilder()
	if got := b.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0", got)
	}
	if err := b.AddAll(fixtureCandidates(t)); err != nil {
		t.Fatalf("AddAll() error = %v", err)
	}
	if got := b.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}
}

func TestBuilderWithCustomRegistry(t *testing.T) {
	t.Parallel()

	custom, err := NewRegistry([]Rule{
		{Family: "house-style", Name: "swap-greeting", Version: 3, Tier: TierAll},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	const source = "hi there"
	c := Candidate{
		Path:         "greet.go",
		Rule:         Rule{Family: "house-style", Name: "swap-greeting", Version: 3, Tier: TierAll},
		Span:         Span{StartByte: 0, EndByte: 2},
		Original:     "hi",
		Replacement:  "yo",
		SourceDigest: DigestString(source),
	}

	b := NewBuilderWithRegistry(custom)
	if addErr := b.Add(c); addErr != nil {
		t.Fatalf("Add() error = %v", addErr)
	}
	catalog, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if catalog.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", catalog.Len())
	}

	// The same candidate is not acceptable to the canonical registry.
	if err := NewBuilder().Add(c); !errors.Is(err, ErrUnknownRule) {
		t.Fatalf("canonical Add() error = %v, want ErrUnknownRule", err)
	}
}

func TestDuplicateOrderIsCanonical(t *testing.T) {
	t.Parallel()

	catalog := buildCatalog(t, manyCandidates(t))
	duplicates := catalog.Duplicates()
	if len(duplicates) == 0 {
		t.Fatal("the fixture should produce duplicates")
	}
	// Duplicates follow the same canonical order as the catalogue itself:
	// path, then span. That is what makes `--explain` output diffable.
	keys := make([]string, 0, len(duplicates))
	for _, d := range duplicates {
		keys = append(keys, fmt.Sprintf("%s|%010d|%010d", d.Dropped.Path, d.Dropped.Span.StartByte, d.Dropped.Span.EndByte))
	}
	if !slices.IsSorted(keys) {
		t.Errorf("duplicates are not in canonical order: %v", keys)
	}
}
