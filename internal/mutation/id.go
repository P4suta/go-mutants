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

const (
	IDDomain = "go-mutants-id-v1"

	IDDomainWorkspace = "go-mutants-workspace-id-v1"

	IDHexLength = 64

	DisplayIDLength = 20

	MinPrefixLength = 4
)

var (
	ErrEmptyPath          = errors.New("mutation: source path is empty")
	ErrAbsolutePath       = errors.New("mutation: source path is not module-relative")
	ErrEscapingPath       = errors.New("mutation: source path escapes the module root")
	ErrUnnormalizedPath   = errors.New("mutation: source path is not normalized")
	ErrInvalidRuleName    = errors.New("mutation: rule name is invalid")
	ErrInvalidModulePath  = errors.New("mutation: module path is invalid")
	ErrInvalidRuleVersion = errors.New("mutation: rule version must be at least 1")
	ErrInvalidDigest      = errors.New("mutation: digest must be 64 lowercase hex characters")
	ErrFieldTooLong       = errors.New("mutation: identity field exceeds the 32-bit length prefix")
	ErrInvalidID          = errors.New("mutation: value is not a 64 hex character mutant id")
)

type Identity struct {
	Path              string
	RuleName          string
	RuleVersion       int
	Span              Span
	SourceDigest      string
	OriginalDigest    string
	ReplacementDigest string
	ModulePath        string
}

func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func DigestString(s string) string {
	return Digest([]byte(s))
}

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
	if len(cleaned) >= 2 && cleaned[1] == ':' && isASCIILetter(cleaned[0]) {
		return "", fmt.Errorf("%w: %q has a volume name", ErrAbsolutePath, p)
	}
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %q", ErrEscapingPath, p)
	}
	return cleaned, nil
}

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
	if id.ModulePath != "" && strings.ContainsAny(id.ModulePath, " \t\r\n@\x00") {
		return fmt.Errorf("%w: %q contains whitespace, '@' or a NUL byte", ErrInvalidModulePath, id.ModulePath)
	}
	return nil
}

func (id Identity) ID() (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	h := sha256.New()
	fields := []string{
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
	if id.ModulePath != "" {
		fields[0] = IDDomainWorkspace
		fields = append(fields, id.ModulePath)
	}
	for _, f := range fields {
		if err := WriteLengthPrefixed(h, f); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func WriteLengthPrefixed(h hash.Hash, s string) error {
	if uint64(len(s)) > math.MaxUint32 {
		return fmt.Errorf("%w: %d bytes", ErrFieldTooLong, len(s))
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(s)))
	_, _ = h.Write(prefix[:])
	_, _ = h.Write([]byte(s))
	return nil
}

func DisplayIDOf(fullID string) (string, error) {
	if !IsID(fullID) {
		return "", fmt.Errorf("%w: %q", ErrInvalidID, fullID)
	}
	return fullID[:DisplayIDLength], nil
}

func IsID(s string) bool { return isLowerHex(s, IDHexLength) }

func IsDigest(s string) bool { return isLowerHex(s, IDHexLength) }

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	return isLowerHexPrefix(s)
}

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
