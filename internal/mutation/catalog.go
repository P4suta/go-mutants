// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// CatalogDomain is the domain separator for the catalogue digest. Like the
// mutant ID domain it carries a version, so a future catalogue encoding
// cannot be confused with this one.
const CatalogDomain = "go-mutants-catalog-v1"

// Errors reported while building or querying a catalogue.
var (
	// ErrOriginalLengthMismatch reports a candidate whose original text is
	// not the length of its span.
	ErrOriginalLengthMismatch = errors.New("mutation: original text length does not match the span")
	// ErrNoOpReplacement reports a candidate whose replacement is
	// byte-identical to the original, which would splice the source back
	// into itself instead of mutating it.
	ErrNoOpReplacement = errors.New("mutation: replacement is identical to the original")
	// ErrSourceDigestConflict reports two candidates that claim different
	// digests for the same file.
	ErrSourceDigestConflict = errors.New("mutation: conflicting source digests for one path")
	// ErrOriginalConflict reports two candidates that claim different
	// original text for the same span of one file.
	ErrOriginalConflict = errors.New("mutation: conflicting original text for one span")
	// ErrCatalogTooLarge reports a catalogue that cannot be addressed by the
	// uint32 runtime index.
	ErrCatalogTooLarge = errors.New("mutation: catalogue exceeds the uint32 index space")
	// ErrInvalidPrefix reports an ID prefix that is too short, too long, or
	// not lowercase hex.
	ErrInvalidPrefix = errors.New("mutation: invalid mutant id prefix")
	// ErrMutantNotFound reports a prefix that matches no mutant.
	ErrMutantNotFound = errors.New("mutation: no mutant matches that id prefix")
	// ErrAmbiguousPrefix reports a prefix that matches more than one mutant.
	ErrAmbiguousPrefix = errors.New("mutation: ambiguous mutant id prefix")
)

// Candidate is one proposed edit: replace the bytes of Span in Path with
// Replacement. It is the unit discovery produces and the catalogue consumes.
//
// Original and Replacement are byte strings, not printable text. They are
// spliced verbatim, so the original keeps whatever whitespace, comments, and
// line endings the file had.
type Candidate struct {
	// Path is the module-relative source path with forward slashes.
	Path string
	// Rule is the operator that proposed the edit.
	Rule Rule
	// Span is the byte range being replaced.
	Span Span
	// Original is exactly the bytes Span covers in the source file.
	Original string
	// Replacement is what those bytes become. Empty for a deletion, and
	// never equal to Original: replacing bytes with themselves is not a
	// mutation. See Validate.
	Replacement string
	// SourceDigest is the lowercase hex SHA-256 of the whole source file.
	SourceDigest string
}

// Validate reports whether the candidate is internally coherent.
//
// Two checks carry the weight, and both exist so that a broken discovery rule
// is caught here rather than three phases later, after instrumentation and the
// runner have each spent a build on it.
//
// The length check proves the span and the original text were taken from the
// same file at the same moment. An off-by-one in a discovery rule would
// otherwise mint a perfectly valid looking ID for an edit that splices
// garbage.
//
// The no-op check proves the edit is an edit at all. A replacement identical
// to the original hashes to a well formed ID and then splices bytes the source
// already had, so it compiles by construction and is guaranteed to survive
// every test: it would inflate the score's denominator and trip
// `policy.strict` for a mutation that does not exist. An empty span is still a
// legal insertion point, so what this rejects is exactly the pair that changes
// nothing, deletions of nothing included.
func (c Candidate) Validate() error {
	if err := c.Rule.Validate(); err != nil {
		return err
	}
	if err := c.Identity().Validate(); err != nil {
		return err
	}
	if uint64(len(c.Original)) != uint64(c.Span.Len()) {
		return fmt.Errorf("%w: %s %s covers %d bytes, original text is %d bytes",
			ErrOriginalLengthMismatch, c.Path, c.Span, c.Span.Len(), len(c.Original))
	}
	if c.Replacement == c.Original {
		return fmt.Errorf("%w: %s %s replaces %q with itself",
			ErrNoOpReplacement, c.Path, c.Span, c.Original)
	}
	return nil
}

// Identity returns the identity this candidate hashes to, digesting the
// original and replacement text.
func (c Candidate) Identity() Identity {
	return Identity{
		Path:              c.Path,
		RuleName:          c.Rule.Name,
		RuleVersion:       c.Rule.Version,
		Span:              c.Span,
		SourceDigest:      c.SourceDigest,
		OriginalDigest:    DigestString(c.Original),
		ReplacementDigest: DigestString(c.Replacement),
	}
}

// ID computes the candidate's stable mutant ID.
func (c Candidate) ID() (string, error) { return c.Identity().ID() }

// Mutant is a catalogued candidate: identified, deduplicated, and assigned
// its dense runtime index.
type Mutant struct {
	// Index is the position in the generated runtime's activation array. It
	// is the catalogue's own order, densely assigned from zero.
	Index uint32
	// ID is the full 64 hex character stable identity.
	ID string
	// DisplayID is the short form, proven unique within this catalogue.
	DisplayID string
	// Candidate is the edit itself.
	Candidate
}

// DuplicateReason explains why a candidate lost deduplication.
type DuplicateReason string

// The v1 duplicate reasons.
const (
	// DuplicateIdentical means the same rule proposed the same edit twice;
	// both candidates carry the same mutant ID.
	DuplicateIdentical DuplicateReason = "identical-candidate"
	// DuplicateShadowed means a different rule proposed the same byte edit
	// at the same span, and the more local rule won.
	DuplicateShadowed DuplicateReason = "shadowed-by-more-local-rule"
)

// Duplicate records a candidate that the catalogue dropped. Duplicates are
// kept rather than discarded so `--explain` can answer "why is there no
// mutant for this rule here?" without re-running discovery.
type Duplicate struct {
	// Reason is why the candidate lost.
	Reason DuplicateReason
	// Dropped is the losing candidate.
	Dropped Candidate
	// DroppedID is the losing candidate's mutant ID. For an identical
	// duplicate it equals WinnerID.
	DroppedID string
	// WinnerID is the ID of the mutant that was kept.
	WinnerID string
	// WinnerRule is the rule that won.
	WinnerRule Rule
}

// DisplayCollision is one short ID shared by several mutants.
type DisplayCollision struct {
	// DisplayID is the colliding short form.
	DisplayID string
	// IDs are the colliding full IDs, sorted.
	IDs []string
}

// DisplayCollisionError reports that truncating full IDs to the display
// length would produce an ambiguous short form.
//
// This is returned, never panicked: a collision among 20 hex characters is
// astronomically unlikely but not impossible, and the honest response is a
// diagnosable error rather than a crash or a silently ambiguous `--mutant`
// selector.
type DisplayCollisionError struct {
	// Length is the display length that collided.
	Length int
	// Collisions are the colliding short forms, sorted by short form.
	Collisions []DisplayCollision
}

// Error implements error.
func (e *DisplayCollisionError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mutation: %d display id collision(s) at %d hex characters", len(e.Collisions), e.Length)
	for _, c := range e.Collisions {
		fmt.Fprintf(&b, "; %s shared by %s", c.DisplayID, strings.Join(c.IDs, ", "))
	}
	return b.String()
}

// Builder accumulates candidates and produces a catalogue.
//
// Build is a pipeline, in this order: validate, identify, deduplicate, sort
// canonically, assign dense indices, then check display IDs. Deduplication
// has to precede the display check, otherwise two identical candidates from
// one rule would be reported as a hash collision instead of as the duplicate
// they are.
type Builder struct {
	registry   *Registry
	candidates []Candidate
	displayLen int
	// digests remembers one source digest per path, and originals remembers
	// one original text per (path, span), so contradictions are caught where
	// they are introduced instead of surfacing as an unexplainable ID.
	digests   map[string]string
	originals map[originalKey]string
}

type originalKey struct {
	path string
	span Span
}

// NewBuilder returns a builder backed by the canonical v1 registry.
func NewBuilder() *Builder { return NewBuilderWithRegistry(nil) }

// NewBuilderWithRegistry returns a builder backed by the given registry. A
// nil registry means the canonical one.
func NewBuilderWithRegistry(r *Registry) *Builder {
	if r == nil {
		r = CanonicalRegistry()
	}
	return &Builder{
		registry:   r,
		displayLen: DisplayIDLength,
		digests:    make(map[string]string),
		originals:  make(map[originalKey]string),
	}
}

// setDisplayLength overrides the display ID length. It exists so the tests
// can force the collision path that real SHA-256 output will not produce; no
// production caller changes it.
func (b *Builder) setDisplayLength(n int) *Builder {
	b.displayLen = n
	return b
}

// Len returns the number of candidates added so far, before deduplication.
func (b *Builder) Len() int { return len(b.candidates) }

// Add validates a candidate and queues it. Insertion order does not affect
// the resulting catalogue.
func (b *Builder) Add(c Candidate) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := b.registry.Verify(c.Rule); err != nil {
		return err
	}
	if prev, ok := b.digests[c.Path]; ok && prev != c.SourceDigest {
		return fmt.Errorf("%w: %s has %s and %s", ErrSourceDigestConflict, c.Path, prev, c.SourceDigest)
	}
	b.digests[c.Path] = c.SourceDigest

	key := originalKey{path: c.Path, span: c.Span}
	if prev, ok := b.originals[key]; ok && prev != c.Original {
		return fmt.Errorf("%w: %s %s is both %q and %q", ErrOriginalConflict, c.Path, c.Span, prev, c.Original)
	}
	b.originals[key] = c.Original

	b.candidates = append(b.candidates, c)
	return nil
}

// AddAll adds candidates in order, stopping at the first invalid one.
func (b *Builder) AddAll(cs []Candidate) error {
	for _, c := range cs {
		if err := b.Add(c); err != nil {
			return err
		}
	}
	return nil
}

// entry is a candidate with everything Build needs to order it.
type entry struct {
	candidate Candidate
	id        string
	position  int
}

// Build produces the catalogue.
func (b *Builder) Build() (*Catalog, error) {
	if uint64(len(b.candidates)) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: %d candidates", ErrCatalogTooLarge, len(b.candidates))
	}
	entries := make([]entry, 0, len(b.candidates))
	for _, c := range b.candidates {
		id, err := c.ID()
		if err != nil {
			return nil, err
		}
		position, ok := b.registry.Position(c.Rule.Name)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownRule, c.Rule.Name)
		}
		entries = append(entries, entry{candidate: c, id: id, position: position})
	}

	// Canonical order first, so that everything downstream — deduplication,
	// dense indices, the catalogue digest — is a pure function of the set of
	// candidates and never of the order they were discovered in.
	slices.SortFunc(entries, compareEntries)

	kept, duplicates := dedup(entries)

	mutants := make([]Mutant, len(kept))
	for i, e := range kept {
		mutants[i] = Mutant{
			Index:     uint32(i),
			ID:        e.id,
			Candidate: e.candidate,
		}
	}
	displayLen := b.effectiveDisplayLength()
	if err := assignDisplayIDs(mutants, displayLen); err != nil {
		return nil, err
	}

	c := &Catalog{
		mutants:     mutants,
		duplicates:  duplicates,
		displayLen:  displayLen,
		byID:        make(map[string]int, len(mutants)),
		byDisplayID: make(map[string]int, len(mutants)),
	}
	for i, m := range mutants {
		c.byID[m.ID] = i
		c.byDisplayID[m.DisplayID] = i
	}
	c.digest = catalogDigest(mutants)
	return c, nil
}

// compareEntries is the canonical catalogue order: by path, then by span,
// then by registry position, then by replacement, then by ID. Paths are
// compared byte-wise; no locale or Unicode collation is involved anywhere,
// because the order has to be identical on every machine that runs a shard.
func compareEntries(x, y entry) int {
	if c := strings.Compare(x.candidate.Path, y.candidate.Path); c != 0 {
		return c
	}
	if c := x.candidate.Span.Compare(y.candidate.Span); c != 0 {
		return c
	}
	if x.position != y.position {
		if x.position < y.position {
			return -1
		}
		return 1
	}
	if c := strings.Compare(x.candidate.Replacement, y.candidate.Replacement); c != 0 {
		return c
	}
	return strings.Compare(x.id, y.id)
}

// dedupKey is what makes two candidates the same edit: the same bytes
// replaced by the same bytes in the same file. The rule that proposed the
// edit is deliberately not part of the key.
type dedupKey struct {
	path        string
	span        Span
	replacement string
}

// dedup keeps one candidate per distinct edit and records the rest.
//
// The winner is the candidate that comes first in canonical order, which for
// one edit means the lowest registry position: the earlier row of the
// operator table. That is the v1 reading of "the more local rule wins" —
// operator families are listed from the most local edit (a boolean literal)
// to the least local (deleting a whole statement), so table position is a
// usable, explainable, and above all stable proxy for locality.
func dedup(sorted []entry) ([]entry, []Duplicate) {
	winners := make(map[dedupKey]entry, len(sorted))
	kept := make([]entry, 0, len(sorted))
	var duplicates []Duplicate
	for _, e := range sorted {
		key := dedupKey{
			path:        e.candidate.Path,
			span:        e.candidate.Span,
			replacement: e.candidate.Replacement,
		}
		winner, seen := winners[key]
		if !seen {
			winners[key] = e
			kept = append(kept, e)
			continue
		}
		reason := DuplicateShadowed
		if winner.candidate.Rule == e.candidate.Rule {
			reason = DuplicateIdentical
		}
		duplicates = append(duplicates, Duplicate{
			Reason:     reason,
			Dropped:    e.candidate,
			DroppedID:  e.id,
			WinnerID:   winner.id,
			WinnerRule: winner.candidate.Rule,
		})
	}
	return kept, duplicates
}

// effectiveDisplayLength is the display length Build will actually use. An
// out-of-range value falls back to the default, and the catalogue records the
// length it really proved unique rather than the one it was asked for.
func (b *Builder) effectiveDisplayLength() int {
	if b.displayLen <= 0 || b.displayLen > IDHexLength {
		return DisplayIDLength
	}
	return b.displayLen
}

// assignDisplayIDs fills in the short IDs and proves they are unambiguous.
func assignDisplayIDs(mutants []Mutant, length int) error {
	byPrefix := make(map[string][]string, len(mutants))
	for i := range mutants {
		short := mutants[i].ID[:length]
		mutants[i].DisplayID = short
		byPrefix[short] = append(byPrefix[short], mutants[i].ID)
	}
	var collisions []DisplayCollision
	for short, ids := range byPrefix {
		if len(ids) < 2 {
			continue
		}
		sorted := slices.Clone(ids)
		slices.Sort(sorted)
		collisions = append(collisions, DisplayCollision{DisplayID: short, IDs: sorted})
	}
	if len(collisions) == 0 {
		return nil
	}
	slices.SortFunc(collisions, func(x, y DisplayCollision) int {
		return strings.Compare(x.DisplayID, y.DisplayID)
	})
	return &DisplayCollisionError{Length: length, Collisions: collisions}
}

// catalogDigest hashes the catalogue's identity: the ordered list of mutant
// IDs. Shard merging and the outcome cache both need a single value that
// answers "are these two runs looking at the same set of mutants?".
func catalogDigest(mutants []Mutant) string {
	h := sha256.New()
	// Errors are impossible here: every field is a short, fixed-width hex
	// string, far below the 32-bit length prefix limit.
	_ = writeLengthPrefixed(h, CatalogDomain)
	_ = writeLengthPrefixed(h, strconv.Itoa(len(mutants)))
	for _, m := range mutants {
		_ = writeLengthPrefixed(h, m.ID)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Catalog is the immutable, ordered set of mutants for one run.
//
// Catalogue order is canonical rather than chronological: it is a pure
// function of the candidate set, so two discovery passes over the same
// workspace produce the same order, the same dense indices, and therefore the
// same generated runtime array.
type Catalog struct {
	mutants     []Mutant
	duplicates  []Duplicate
	byID        map[string]int
	byDisplayID map[string]int
	displayLen  int
	digest      string
}

// Len returns the number of mutants.
func (c *Catalog) Len() int { return len(c.mutants) }

// Empty reports whether the catalogue holds no mutants.
func (c *Catalog) Empty() bool { return len(c.mutants) == 0 }

// Mutants returns every mutant in catalogue order.
func (c *Catalog) Mutants() []Mutant { return slices.Clone(c.mutants) }

// Duplicates returns the candidates deduplication dropped, in catalogue
// order of the dropped candidate.
func (c *Catalog) Duplicates() []Duplicate { return slices.Clone(c.duplicates) }

// DisplayLength returns the display ID length this catalogue proved unique.
func (c *Catalog) DisplayLength() int { return c.displayLen }

// Digest returns the catalogue digest: a SHA-256 over the domain separator,
// the mutant count, and every mutant ID in order, all length-prefixed exactly
// as in the mutant ID recipe.
func (c *Catalog) Digest() string { return c.digest }

// At returns the mutant at a catalogue position.
func (c *Catalog) At(i int) (Mutant, bool) {
	if i < 0 || i >= len(c.mutants) {
		return Mutant{}, false
	}
	return c.mutants[i], true
}

// ByIndex returns the mutant with the given dense runtime index.
func (c *Catalog) ByIndex(index uint32) (Mutant, bool) {
	if uint64(index) >= uint64(len(c.mutants)) {
		return Mutant{}, false
	}
	return c.mutants[index], true
}

// ByID returns the mutant with the given full ID.
func (c *Catalog) ByID(id string) (Mutant, bool) {
	i, ok := c.byID[id]
	if !ok {
		return Mutant{}, false
	}
	return c.mutants[i], true
}

// ByDisplayID returns the mutant with the given short ID.
func (c *Catalog) ByDisplayID(displayID string) (Mutant, bool) {
	i, ok := c.byDisplayID[displayID]
	if !ok {
		return Mutant{}, false
	}
	return c.mutants[i], true
}

// ResolvePrefix resolves a user-supplied ID prefix, as accepted by
// `--mutant`. It resolves against the whole catalogue regardless of which
// profile selected which rules, and it refuses to guess: a prefix matching
// two mutants is an error naming both, never the first match.
func (c *Catalog) ResolvePrefix(prefix string) (Mutant, error) {
	if len(prefix) < MinPrefixLength || len(prefix) > IDHexLength || !isLowerHexPrefix(prefix) {
		return Mutant{}, fmt.Errorf("%w: %q must be %d to %d lowercase hex characters",
			ErrInvalidPrefix, prefix, MinPrefixLength, IDHexLength)
	}
	var matches []Mutant
	for _, m := range c.mutants {
		if strings.HasPrefix(m.ID, prefix) {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return Mutant{}, fmt.Errorf("%w: %q", ErrMutantNotFound, prefix)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.DisplayID)
		}
		return Mutant{}, fmt.Errorf("%w: %q matches %d mutants: %s",
			ErrAmbiguousPrefix, prefix, len(matches), strings.Join(ids, ", "))
	}
}
