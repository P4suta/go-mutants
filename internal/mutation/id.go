// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"
	"path"
	"strconv"
	"strings"
)

// Constants of the frozen identity recipe.
const (
	// IDDomain is the domain separator hashed first for every mutant ID. It
	// carries the recipe version: a future recipe becomes
	// "go-mutants-id-v2" so that v1 identities can never be mistaken for v2
	// identities, and both can coexist in one cache.
	IDDomain = "go-mutants-id-v1"

	// IDHexLength is the length of a full mutant ID in lowercase hex
	// characters (SHA-256).
	IDHexLength = 64

	// DisplayIDLength is the length of the short ID shown in the console and
	// accepted by `--mutant`. The catalogue builder proves the prefix is
	// unique within a run before any of them is displayed.
	DisplayIDLength = 20

	// MinPrefixLength is the shortest `--mutant` prefix the catalogue will
	// resolve. Anything shorter is rejected as a typo rather than silently
	// matching half the run.
	MinPrefixLength = 4
)

// Errors reported by identity construction and validation.
var (
	// ErrEmptyPath reports an identity with no source path.
	ErrEmptyPath = errors.New("mutation: source path is empty")
	// ErrAbsolutePath reports a path that is not module-relative.
	ErrAbsolutePath = errors.New("mutation: source path is not module-relative")
	// ErrEscapingPath reports a path that climbs out of the module root.
	ErrEscapingPath = errors.New("mutation: source path escapes the module root")
	// ErrUnnormalizedPath reports a path that is not in its canonical
	// module-relative, forward-slashed form.
	ErrUnnormalizedPath = errors.New("mutation: source path is not normalized")
	// ErrInvalidRuleName reports an empty or malformed rule name.
	ErrInvalidRuleName = errors.New("mutation: rule name is invalid")
	// ErrInvalidRuleVersion reports a rule version below 1.
	ErrInvalidRuleVersion = errors.New("mutation: rule version must be at least 1")
	// ErrInvalidDigest reports a digest that is not 64 lowercase hex digits.
	ErrInvalidDigest = errors.New("mutation: digest must be 64 lowercase hex characters")
	// ErrFieldTooLong reports a field whose byte length overflows the 32-bit
	// length prefix of the identity encoding.
	ErrFieldTooLong = errors.New("mutation: identity field exceeds the 32-bit length prefix")
	// ErrInvalidID reports a string that is not a full 64 hex character ID.
	ErrInvalidID = errors.New("mutation: value is not a 64 hex character mutant id")
)

// Identity is the complete, canonical input to a stable mutant ID.
//
// The identity is deliberately over-specified. Path, rule, and span alone
// would collide across edits of the same file, so the three digests pin the
// mutant to exact bytes: SourceDigest changes when anything in the file
// changes (including a CRLF-to-LF conversion), OriginalDigest pins what is
// being replaced, and ReplacementDigest pins what replaces it. A cached
// outcome therefore can never be reused for a mutant that is not literally
// the same edit to the same bytes.
type Identity struct {
	// Path is the module-relative source path with forward slashes, as
	// produced by NormalizePath.
	Path string
	// RuleName is the operator rule name, for example "eq-to-neq".
	RuleName string
	// RuleVersion is the rule's version. Bumping it re-mints every mutant
	// the rule produces, which is exactly how a behaviour change is meant to
	// invalidate cached outcomes.
	RuleVersion int
	// Span is the byte range of the original text being replaced.
	Span Span
	// SourceDigest is the SHA-256 of the whole file, lowercase hex.
	SourceDigest string
	// OriginalDigest is the SHA-256 of the original span bytes.
	OriginalDigest string
	// ReplacementDigest is the SHA-256 of the replacement bytes. For a
	// deletion this is the digest of the empty string.
	ReplacementDigest string
}

// Digest returns the lowercase hex SHA-256 of b.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DigestString returns the lowercase hex SHA-256 of the bytes of s.
func DigestString(s string) string {
	return Digest([]byte(s))
}

// NormalizePath converts a source path to the canonical module-relative form
// used in identities and reports: forward slashes, cleaned, no leading "./".
//
// Backslashes are treated as separators so that a Windows path lands on the
// same identity as the POSIX spelling of the same file. That is the whole
// point: a mutant must not change its name because the run happened on a
// different operating system.
func NormalizePath(p string) (string, error) {
	if p == "" {
		return "", ErrEmptyPath
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: contains a NUL byte", ErrEmptyPath)
	}
	slashed := strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(slashed, "/") {
		return "", fmt.Errorf("%w: %q", ErrAbsolutePath, p)
	}
	if len(slashed) >= 2 && slashed[1] == ':' && isASCIILetter(slashed[0]) {
		return "", fmt.Errorf("%w: %q has a volume name", ErrAbsolutePath, p)
	}
	cleaned := path.Clean(slashed)
	// Cleaning removes a leading "./". Re-check the canonical spelling so
	// inputs such as "./C:" cannot turn into a Windows volume name only after
	// the first absolute-path check has already passed.
	if len(cleaned) >= 2 && cleaned[1] == ':' && isASCIILetter(cleaned[0]) {
		return "", fmt.Errorf("%w: %q has a volume name", ErrAbsolutePath, p)
	}
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %q", ErrEscapingPath, p)
	}
	return cleaned, nil
}

// Validate reports whether the identity is complete and canonical. Every
// field that feeds the hash is checked, because a malformed field would
// otherwise produce a perfectly plausible looking ID for a mutant that cannot
// be resolved back to a source location.
func (id Identity) Validate() error {
	normalized, err := NormalizePath(id.Path)
	if err != nil {
		return err
	}
	if normalized != id.Path {
		return fmt.Errorf("%w: %q should be %q", ErrUnnormalizedPath, id.Path, normalized)
	}
	if id.RuleName == "" {
		return fmt.Errorf("%w: empty", ErrInvalidRuleName)
	}
	if strings.ContainsAny(id.RuleName, " \t\r\n@") {
		return fmt.Errorf("%w: %q contains whitespace or '@'", ErrInvalidRuleName, id.RuleName)
	}
	if id.RuleVersion < 1 {
		return fmt.Errorf("%w: %d", ErrInvalidRuleVersion, id.RuleVersion)
	}
	if err := id.Span.Validate(); err != nil {
		return err
	}
	for _, d := range []struct {
		field string
		value string
	}{
		{"source", id.SourceDigest},
		{"original", id.OriginalDigest},
		{"replacement", id.ReplacementDigest},
	} {
		if !IsDigest(d.value) {
			return fmt.Errorf("%w: %s digest %q", ErrInvalidDigest, d.field, d.value)
		}
	}
	return nil
}

// ID computes the stable full mutant ID.
//
// The recipe, frozen as of v1: SHA-256 over the concatenation of nine
// length-prefixed fields, in order
//
//	enc("go-mutants-id-v1")
//	enc(path)                  module-relative, forward slashes
//	enc(rule_name)
//	enc(rule_version)          decimal
//	enc(start_byte)            decimal
//	enc(end_byte)              decimal
//	enc(source_sha256_hex)     lowercase 64 hex characters
//	enc(original_sha256_hex)   lowercase 64 hex characters
//	enc(replacement_sha256_hex) lowercase 64 hex characters
//
// where enc(s) is a 4-byte big-endian uint32 of the UTF-8 byte length of s
// followed by the raw UTF-8 bytes of s. Numbers are hashed as their decimal
// text and digests as their hex text, so the encoding is fully described by
// the bytes on the wire and can be reimplemented in any language. Length
// prefixes make the concatenation unambiguous: no combination of path and
// rule name can ever be re-parenthesised into a different identity.
func (id Identity) ID() (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	h := sha256.New()
	fields := [9]string{
		IDDomain,
		id.Path,
		id.RuleName,
		strconv.Itoa(id.RuleVersion),
		strconv.FormatUint(uint64(id.Span.StartByte), 10),
		strconv.FormatUint(uint64(id.Span.EndByte), 10),
		id.SourceDigest,
		id.OriginalDigest,
		id.ReplacementDigest,
	}
	for _, f := range fields {
		if err := WriteLengthPrefixed(h, f); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// WriteLengthPrefixed writes enc(s) to h: a 4-byte big-endian byte length
// followed by the raw bytes.
//
// It is exported because the mutant identity is not the only thing go-mutants
// hashes from a list of fields — the outcome cache key is another — and the
// unambiguity argument above is the reason to have exactly one implementation
// of the encoding rather than one per hash. Anything hashed this way is
// described completely by the bytes on the wire and can be reimplemented
// elsewhere from that description alone.
func WriteLengthPrefixed(h hash.Hash, s string) error {
	if uint64(len(s)) > math.MaxUint32 {
		return fmt.Errorf("%w: %d bytes", ErrFieldTooLong, len(s))
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(s)))
	// hash.Hash never returns an error, as documented on the interface.
	_, _ = h.Write(prefix[:])
	_, _ = h.Write([]byte(s))
	return nil
}

// DisplayIDOf returns the short form of a full mutant ID. Uniqueness of the
// short form is a property of a whole catalogue, not of one ID, so it is the
// catalogue builder that proves it; this function only truncates.
func DisplayIDOf(fullID string) (string, error) {
	if !IsID(fullID) {
		return "", fmt.Errorf("%w: %q", ErrInvalidID, fullID)
	}
	return fullID[:DisplayIDLength], nil
}

// IsID reports whether s is a full mutant ID: 64 lowercase hex characters.
func IsID(s string) bool { return isLowerHex(s, IDHexLength) }

// IsDigest reports whether s is a lowercase hex SHA-256 digest.
func IsDigest(s string) bool { return isLowerHex(s, IDHexLength) }

// isLowerHex reports whether s consists of exactly n lowercase hex digits.
func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	return isLowerHexPrefix(s)
}

// isLowerHexPrefix reports whether every character of s is a lowercase hex
// digit. Uppercase is rejected rather than folded: identities are compared as
// strings in cache keys and report files, so exactly one spelling may exist.
func isLowerHexPrefix(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
