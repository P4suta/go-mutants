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

const CatalogDomain = "go-mutants-catalog-v1"

var (
	ErrOriginalLengthMismatch = errors.New("mutation: original text length does not match the span")
	ErrNoOpReplacement        = errors.New("mutation: replacement is identical to the original")
	ErrSourceDigestConflict   = errors.New("mutation: conflicting source digests for one path")
	ErrOriginalConflict       = errors.New("mutation: conflicting original text for one span")
	ErrModulePathMixed        = errors.New("mutation: some candidates name a module and some do not")
	ErrCatalogTooLarge        = errors.New("mutation: catalogue exceeds the uint32 index space")
	ErrInvalidPrefix          = errors.New("mutation: invalid mutant id prefix")
	ErrMutantNotFound         = errors.New("mutation: no mutant matches that id prefix")
	ErrAmbiguousPrefix        = errors.New("mutation: ambiguous mutant id prefix")
)

type Candidate struct {
	ModulePath   string
	Path         string
	Rule         Rule
	Span         Span
	Original     string
	Replacement  string
	SourceDigest string
}

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

func (c Candidate) Identity() Identity {
	return Identity{
		ModulePath:        c.ModulePath,
		Path:              c.Path,
		RuleName:          c.Rule.Name,
		RuleVersion:       c.Rule.Version,
		Span:              c.Span,
		SourceDigest:      c.SourceDigest,
		OriginalDigest:    DigestString(c.Original),
		ReplacementDigest: DigestString(c.Replacement),
	}
}

func (c Candidate) ID() (string, error) { return c.Identity().ID() }

func (c Candidate) Where() string {
	if c.ModulePath == "" {
		return c.Path
	}
	return c.ModulePath + " " + c.Path
}

type Mutant struct {
	Index     uint32
	ID        string
	DisplayID string
	Candidate
}

type DuplicateReason string

const (
	DuplicateIdentical DuplicateReason = "identical-candidate"
	DuplicateShadowed  DuplicateReason = "shadowed-by-more-local-rule"
)

type Duplicate struct {
	Reason     DuplicateReason
	Dropped    Candidate
	DroppedID  string
	WinnerID   string
	WinnerRule Rule
}

type DisplayCollision struct {
	DisplayID string
	IDs       []string
}

type DisplayCollisionError struct {
	Length     int
	Collisions []DisplayCollision
}

func (e *DisplayCollisionError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mutation: %d display id collision(s) at %d hex characters", len(e.Collisions), e.Length)
	for _, c := range e.Collisions {
		fmt.Fprintf(&b, "; %s shared by %s", c.DisplayID, strings.Join(c.IDs, ", "))
	}
	return b.String()
}

type Builder struct {
	registry   *Registry
	candidates []Candidate
	displayLen int
	digests    map[fileKey]string
	originals  map[originalKey]string
}

type fileKey struct {
	module string
	path   string
}

type originalKey struct {
	file fileKey
	span Span
}

func NewBuilder() *Builder { return NewBuilderWithRegistry(nil) }

func NewBuilderWithRegistry(r *Registry) *Builder {
	if r == nil {
		r = CanonicalRegistry()
	}
	return &Builder{
		registry:   r,
		displayLen: DisplayIDLength,
		digests:    make(map[fileKey]string),
		originals:  make(map[originalKey]string),
	}
}

func (b *Builder) setDisplayLength(n int) *Builder {
	b.displayLen = n
	return b
}

func (b *Builder) Len() int { return len(b.candidates) }

func (b *Builder) Add(c Candidate) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := b.registry.Verify(c.Rule); err != nil {
		return err
	}
	if len(b.candidates) > 0 && (b.candidates[0].ModulePath == "") != (c.ModulePath == "") {
		first := b.candidates[0]
		return fmt.Errorf("%w: %s names %q and %s names %q",
			ErrModulePathMixed, c.Where(), c.ModulePath, first.Where(), first.ModulePath)
	}

	file := fileKey{module: c.ModulePath, path: c.Path}
	if prev, ok := b.digests[file]; ok && prev != c.SourceDigest {
		return fmt.Errorf("%w: %s has %s and %s", ErrSourceDigestConflict, c.Where(), prev, c.SourceDigest)
	}
	b.digests[file] = c.SourceDigest

	key := originalKey{file: file, span: c.Span}
	if prev, ok := b.originals[key]; ok && prev != c.Original {
		return fmt.Errorf("%w: %s %s is both %q and %q", ErrOriginalConflict, c.Where(), c.Span, prev, c.Original)
	}
	b.originals[key] = c.Original

	b.candidates = append(b.candidates, c)
	return nil
}

func (b *Builder) AddAll(cs []Candidate) error {
	for _, c := range cs {
		if err := b.Add(c); err != nil {
			return err
		}
	}
	return nil
}

type entry struct {
	candidate Candidate
	id        string
	position  int
}

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

func compareEntries(x, y entry) int {
	if c := strings.Compare(x.candidate.ModulePath, y.candidate.ModulePath); c != 0 {
		return c
	}
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

type dedupKey struct {
	file        fileKey
	span        Span
	replacement string
}

func dedup(sorted []entry) ([]entry, []Duplicate) {
	winners := make(map[dedupKey]entry, len(sorted))
	kept := make([]entry, 0, len(sorted))
	var duplicates []Duplicate
	for _, e := range sorted {
		key := dedupKey{
			file:        fileKey{module: e.candidate.ModulePath, path: e.candidate.Path},
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

func (b *Builder) effectiveDisplayLength() int {
	if b.displayLen <= 0 || b.displayLen > IDHexLength {
		return DisplayIDLength
	}
	return b.displayLen
}

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

func catalogDigest(mutants []Mutant) string {
	h := sha256.New()
	_ = WriteLengthPrefixed(h, CatalogDomain)
	_ = WriteLengthPrefixed(h, strconv.Itoa(len(mutants)))
	for _, m := range mutants {
		_ = WriteLengthPrefixed(h, m.ID)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type Catalog struct {
	mutants     []Mutant
	duplicates  []Duplicate
	byID        map[string]int
	byDisplayID map[string]int
	displayLen  int
	digest      string
}

func (c *Catalog) Len() int { return len(c.mutants) }

func (c *Catalog) Empty() bool { return len(c.mutants) == 0 }

func (c *Catalog) Mutants() []Mutant { return slices.Clone(c.mutants) }

func (c *Catalog) Duplicates() []Duplicate { return slices.Clone(c.duplicates) }

func (c *Catalog) DisplayLength() int { return c.displayLen }

func (c *Catalog) Digest() string { return c.digest }

func (c *Catalog) At(i int) (Mutant, bool) {
	if i < 0 || i >= len(c.mutants) {
		return Mutant{}, false
	}
	return c.mutants[i], true
}

func (c *Catalog) ByIndex(index uint32) (Mutant, bool) {
	if uint64(index) >= uint64(len(c.mutants)) {
		return Mutant{}, false
	}
	return c.mutants[index], true
}

func (c *Catalog) ByID(id string) (Mutant, bool) {
	i, ok := c.byID[id]
	if !ok {
		return Mutant{}, false
	}
	return c.mutants[i], true
}

func (c *Catalog) ByDisplayID(displayID string) (Mutant, bool) {
	i, ok := c.byDisplayID[displayID]
	if !ok {
		return Mutant{}, false
	}
	return c.mutants[i], true
}

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
