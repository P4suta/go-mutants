// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// Source fixtures for the golden vectors. lfSource and crlfSource are the
// same program with different line endings, which is the whole point of the
// pair: the mutated bytes, the span, the path, and the rule are identical, so
// the only thing that can separate their identities is the whole-file digest.
const (
	lfSource   = "package score\n\nfunc equal(a, b int) bool {\n\treturn a == b\n}\n"
	crlfSource = "package score\r\n\r\nfunc equal(a, b int) bool {\r\n\treturn a == b\r\n}\r\n"

	unicodeSource = "var ok = true\n"
	deleteSource  = "package discover\n\nfunc walk() {\n\tlogTraversal(\"walk\")\n}\n"
)

// goldenVector is a frozen input-to-ID mapping.
//
// The expected IDs were produced by an independent implementation of the
// recipe written from the specification prose, not by this package. If a
// change to id.go moves any of these values, the change has renamed every
// mutant that go-mutants has ever reported: cached outcomes, `--mutant`
// selectors, and every `[[mutation.expect]]` entry in every user's
// configuration file all break at once. The correct way to change the recipe
// is a new domain separator, "go-mutants-id-v2".
type goldenVector struct {
	name        string
	path        string
	ruleName    string
	ruleVersion int
	span        Span
	source      string
	original    string
	replacement string
	// modulePath is empty for the nine-field recipe and set for the
	// ten-field one, which is the only thing that decides between them.
	modulePath string
	wantID     string
}

var goldenVectors = []goldenVector{
	{
		name:        "lf source",
		path:        "internal/mutation/score.go",
		ruleName:    "eq-to-neq",
		ruleVersion: 1,
		span:        Span{StartByte: 1024, EndByte: 1026},
		source:      lfSource,
		original:    "==",
		replacement: "!=",
		wantID:      "e35d4481eff8c1c4e2915152633bbf53eea3c4bc0a05d3f643eb703f88bb2b18",
	},
	{
		// Byte-for-byte the same edit in a CRLF checkout of the same file.
		// This is why .gitattributes pins `* -text`.
		name:        "crlf source",
		path:        "internal/mutation/score.go",
		ruleName:    "eq-to-neq",
		ruleVersion: 1,
		span:        Span{StartByte: 1024, EndByte: 1026},
		source:      crlfSource,
		original:    "==",
		replacement: "!=",
		wantID:      "857f7ce066220e7cc3b9fa0662ec47043e862e7a3e8ba1daefa28d54943a533b",
	},
	{
		// A non-ASCII path proves the length prefix counts UTF-8 bytes and
		// not runes: "日本語" is three runes and nine bytes.
		name:        "unicode path",
		path:        "internal/mutation/日本語/テスト.go",
		ruleName:    "true-to-false",
		ruleVersion: 1,
		span:        Span{StartByte: 9, EndByte: 13},
		source:      unicodeSource,
		original:    "true",
		replacement: "false",
		wantID:      "c5dca588af89bef7bba20137ce4f6740591b1b3c9a605bf241cd4ba28f92c6ae",
	},
	{
		// Statement deletion: the replacement is empty, so its field is a
		// bare four-byte zero prefix and its digest is the SHA-256 of the
		// empty string. No special case anywhere.
		name:        "empty replacement",
		path:        "internal/discover/walk.go",
		ruleName:    "delete-call-statement",
		ruleVersion: 1,
		span:        Span{StartByte: 2048, EndByte: 2068},
		source:      deleteSource,
		original:    `logTraversal("walk")`,
		replacement: "",
		wantID:      "c813601416657960efe57a6333d2aaf6da49f367fdd706b8dd9cf860b1fed412",
	},
	{
		// Same edit as the first vector with the rule version bumped. A rule
		// that changes what it emits must re-mint its mutants rather than
		// silently inheriting cached outcomes.
		name:        "rule version bump",
		path:        "internal/mutation/score.go",
		ruleName:    "eq-to-neq",
		ruleVersion: 2,
		span:        Span{StartByte: 1024, EndByte: 1026},
		source:      lfSource,
		original:    "==",
		replacement: "!=",
		wantID:      "ee5e971c231e2303c070f53860f12c2f50b5051bac2c3b37171bf923647c67bc",
	},
	{
		// The first vector's edit again, measured as part of a workspace. Every
		// field it hashes is the same and the identity is not, which is the
		// whole of what the second domain is for: a path is no longer a
		// coordinate on its own once two modules can each hold one.
		//
		// It is a vector rather than an assertion about inequality because the
		// bytes are the contract. A reimplementation in another language has to
		// produce this string, and "different from the other one" is not
		// something anybody can implement against.
		name:        "workspace module",
		path:        "internal/mutation/score.go",
		ruleName:    "eq-to-neq",
		ruleVersion: 1,
		span:        Span{StartByte: 1024, EndByte: 1026},
		source:      lfSource,
		original:    "==",
		replacement: "!=",
		modulePath:  "example.com/workspace/core",
		wantID:      "f7abd7f50982c88c90f9d4c6b2594eb8052651dd3df22944c22e0b40631e9d9d",
	},
	{
		// The same mutant of a *different* module of the same workspace. The
		// two modules each normalize this file to the same module-relative
		// path, so without the tenth field these two vectors would be one
		// string -- which is exactly the collision the domain exists to stop.
		name:        "workspace sibling module",
		path:        "internal/mutation/score.go",
		ruleName:    "eq-to-neq",
		ruleVersion: 1,
		span:        Span{StartByte: 1024, EndByte: 1026},
		source:      lfSource,
		original:    "==",
		replacement: "!=",
		modulePath:  "example.com/workspace/app",
		wantID:      "72bff8f39d743fe28fd6012b0ea8acf879477d175c20dacd55ed330af1902159",
	},
}

func (v goldenVector) identity() Identity {
	return Identity{
		Path:              v.path,
		RuleName:          v.ruleName,
		RuleVersion:       v.ruleVersion,
		Span:              v.span,
		SourceDigest:      DigestString(v.source),
		OriginalDigest:    DigestString(v.original),
		ReplacementDigest: DigestString(v.replacement),
		ModulePath:        v.modulePath,
	}
}

func TestGoldenIDVectors(t *testing.T) {
	t.Parallel()

	for _, v := range goldenVectors {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()

			got, err := v.identity().ID()
			if err != nil {
				t.Fatalf("ID() error = %v", err)
			}
			if got != v.wantID {
				t.Fatalf("ID() = %s, want %s", got, v.wantID)
			}
			if !IsID(got) {
				t.Errorf("ID() = %q is not a well-formed id", got)
			}
		})
	}
}

func TestGoldenVectorIDsAreAllDistinct(t *testing.T) {
	t.Parallel()

	seen := make(map[string]string, len(goldenVectors))
	for _, v := range goldenVectors {
		if prev, ok := seen[v.wantID]; ok {
			t.Fatalf("vectors %q and %q share an id", prev, v.name)
		}
		seen[v.wantID] = v.name
	}
}

func TestCRLFAndLFDifferOnlyInTheSourceDigest(t *testing.T) {
	t.Parallel()

	lf, crlf := goldenVectors[0], goldenVectors[1]
	if lf.path != crlf.path || lf.ruleName != crlf.ruleName || lf.span != crlf.span ||
		lf.original != crlf.original || lf.replacement != crlf.replacement {
		t.Fatal("the CRLF vector must vary only the whole-file bytes")
	}
	if lf.source == crlf.source {
		t.Fatal("the CRLF vector must not have the same source bytes as the LF vector")
	}
	if lf.wantID == crlf.wantID {
		t.Fatal("a CRLF checkout must not produce the same mutant id as an LF checkout")
	}
}

func TestEmptyReplacementDigestIsTheEmptySHA256(t *testing.T) {
	t.Parallel()

	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := DigestString(""); got != emptySHA256 {
		t.Fatalf("DigestString(%q) = %s, want %s", "", got, emptySHA256)
	}
}

// TestIDFieldsAreUnambiguouslyFramed is the property the length prefixes
// exist for: no two different field splits may hash to the same identity.
func TestIDFieldsAreUnambiguouslyFramed(t *testing.T) {
	t.Parallel()

	base := goldenVectors[0].identity()

	a := base
	a.Path = "ab/c.go"
	a.RuleName = "de"

	b := base
	b.Path = "ab/c.god"
	b.RuleName = "e"

	idA, err := a.ID()
	if err != nil {
		t.Fatalf("ID() error = %v", err)
	}
	idB, err := b.ID()
	if err != nil {
		t.Fatalf("ID() error = %v", err)
	}
	if idA == idB {
		t.Fatal("concatenating fields without length prefixes: two different identities collided")
	}
}

func TestEveryIdentityFieldChangesTheID(t *testing.T) {
	t.Parallel()

	base := goldenVectors[0].identity()
	baseID, err := base.ID()
	if err != nil {
		t.Fatalf("ID() error = %v", err)
	}

	mutations := map[string]func(Identity) Identity{
		"path":               func(id Identity) Identity { id.Path = "internal/mutation/other.go"; return id },
		"rule name":          func(id Identity) Identity { id.RuleName = "neq-to-eq"; return id },
		"rule version":       func(id Identity) Identity { id.RuleVersion = 3; return id },
		"start byte":         func(id Identity) Identity { id.Span.StartByte = 1025; return id },
		"end byte":           func(id Identity) Identity { id.Span.EndByte = 1027; return id },
		"source digest":      func(id Identity) Identity { id.SourceDigest = DigestString("other"); return id },
		"original digest":    func(id Identity) Identity { id.OriginalDigest = DigestString("!="); return id },
		"replacement digest": func(id Identity) Identity { id.ReplacementDigest = DigestString("=="); return id },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := mutate(base).ID()
			if err != nil {
				t.Fatalf("ID() error = %v", err)
			}
			if got == baseID {
				t.Fatalf("changing the %s did not change the id", name)
			}
		})
	}
}

func TestIdentityValidation(t *testing.T) {
	t.Parallel()

	valid := goldenVectors[0].identity()

	tests := []struct {
		name    string
		mutate  func(Identity) Identity
		wantErr error
	}{
		{name: "valid", mutate: func(id Identity) Identity { return id }},
		{
			name:    "empty path",
			mutate:  func(id Identity) Identity { id.Path = ""; return id },
			wantErr: ErrEmptyPath,
		},
		{
			name:    "absolute path",
			mutate:  func(id Identity) Identity { id.Path = "/etc/passwd"; return id },
			wantErr: ErrAbsolutePath,
		},
		{
			name:    "escaping path",
			mutate:  func(id Identity) Identity { id.Path = "../outside.go"; return id },
			wantErr: ErrEscapingPath,
		},
		{
			name:    "backslash path is not normalized",
			mutate:  func(id Identity) Identity { id.Path = `internal\mutation\score.go`; return id },
			wantErr: ErrUnnormalizedPath,
		},
		{
			name:    "dot-slash path is not normalized",
			mutate:  func(id Identity) Identity { id.Path = "./internal/mutation/score.go"; return id },
			wantErr: ErrUnnormalizedPath,
		},
		{
			name:    "empty rule name",
			mutate:  func(id Identity) Identity { id.RuleName = ""; return id },
			wantErr: ErrInvalidRuleName,
		},
		{
			name:    "rule name with a version suffix",
			mutate:  func(id Identity) Identity { id.RuleName = "eq-to-neq@1"; return id },
			wantErr: ErrInvalidRuleName,
		},
		{
			name:    "zero rule version",
			mutate:  func(id Identity) Identity { id.RuleVersion = 0; return id },
			wantErr: ErrInvalidRuleVersion,
		},
		{
			name:    "reversed span",
			mutate:  func(id Identity) Identity { id.Span = Span{StartByte: 9, EndByte: 4}; return id },
			wantErr: ErrSpanReversed,
		},
		{
			name:    "short source digest",
			mutate:  func(id Identity) Identity { id.SourceDigest = "abc"; return id },
			wantErr: ErrInvalidDigest,
		},
		{
			name:    "uppercase original digest",
			mutate:  func(id Identity) Identity { id.OriginalDigest = strings.ToUpper(id.OriginalDigest); return id },
			wantErr: ErrInvalidDigest,
		},
		{
			name:    "non-hex replacement digest",
			mutate:  func(id Identity) Identity { id.ReplacementDigest = strings.Repeat("z", 64); return id },
			wantErr: ErrInvalidDigest,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			id := tc.mutate(valid)
			err := id.Validate()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, tc.wantErr)
			}
			// An identity that does not validate must not produce an ID
			// either: a plausible-looking hash over garbage is worse than an
			// error.
			if tc.wantErr != nil {
				if got, err := id.ID(); err == nil {
					t.Fatalf("ID() = %s for an invalid identity", got)
				}
			}
		})
	}
}

func TestNormalizePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{name: "already canonical", in: "internal/mutation/score.go", want: "internal/mutation/score.go"},
		{name: "windows separators", in: `internal\mutation\score.go`, want: "internal/mutation/score.go"},
		{name: "leading dot slash", in: "./internal/score.go", want: "internal/score.go"},
		{name: "redundant separators", in: "internal//mutation/./score.go", want: "internal/mutation/score.go"},
		{name: "interior dot dot", in: "internal/discover/../mutation/score.go", want: "internal/mutation/score.go"},
		{name: "unicode", in: "internal/日本語/テスト.go", want: "internal/日本語/テスト.go"},
		{name: "bare file", in: "main.go", want: "main.go"},
		{name: "empty", in: "", wantErr: ErrEmptyPath},
		{name: "nul byte", in: "internal/sco\x00re.go", wantErr: ErrEmptyPath},
		{name: "absolute posix", in: "/internal/score.go", wantErr: ErrAbsolutePath},
		{name: "absolute windows", in: `C:\repo\score.go`, wantErr: ErrAbsolutePath},
		// A volume name is a letter and a colon, and both halves of that are
		// checked here rather than assumed. The four letters are the ends of
		// the two ASCII ranges, because a range that stops one short accepts
		// `z:\...` as a relative path and mints an identity for a file outside
		// the module. "1:" is the counter-example: a colon after something
		// that is not a letter is a directory whose name contains a colon,
		// which POSIX allows, and rejecting it would refuse a legal path.
		{name: "bare volume name", in: "c:", wantErr: ErrAbsolutePath},
		{name: "dot slash before volume name", in: "./A:", wantErr: ErrAbsolutePath},
		{name: "first lowercase volume name", in: `a:\repo\score.go`, wantErr: ErrAbsolutePath},
		{name: "last lowercase volume name", in: `z:\repo\score.go`, wantErr: ErrAbsolutePath},
		{name: "first uppercase volume name", in: `A:\repo\score.go`, wantErr: ErrAbsolutePath},
		{name: "last uppercase volume name", in: `Z:\repo\score.go`, wantErr: ErrAbsolutePath},
		{name: "colon after a digit is not a volume", in: "1:/repo/score.go", want: "1:/repo/score.go"},
		{name: "escaping", in: "../score.go", wantErr: ErrEscapingPath},
		{name: "escaping after cleaning", in: "internal/../../score.go", wantErr: ErrEscapingPath},
		{name: "bare dot", in: ".", wantErr: ErrEscapingPath},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizePath(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("NormalizePath(%q) error = %v, want %v", tc.in, err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if got != tc.want {
				t.Fatalf("NormalizePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Normalization is idempotent, which is what lets Validate
			// compare a path against its own normal form.
			again, err := NormalizePath(got)
			if err != nil || again != got {
				t.Fatalf("NormalizePath(%q) is not idempotent: %q, %v", got, again, err)
			}
		})
	}
}

func TestDisplayIDOf(t *testing.T) {
	t.Parallel()

	full := goldenVectors[0].wantID
	short, err := DisplayIDOf(full)
	if err != nil {
		t.Fatalf("DisplayIDOf() error = %v", err)
	}
	if len(short) != DisplayIDLength {
		t.Fatalf("DisplayIDOf() = %q, want %d characters", short, DisplayIDLength)
	}
	if !strings.HasPrefix(full, short) {
		t.Fatalf("DisplayIDOf() = %q is not a prefix of %q", short, full)
	}
	if short != "e35d4481eff8c1c4e291" {
		t.Fatalf("DisplayIDOf() = %q, want %q", short, "e35d4481eff8c1c4e291")
	}

	for _, bad := range []string{"", "abc", strings.ToUpper(full), full + "0", strings.Repeat("z", 64)} {
		if _, err := DisplayIDOf(bad); !errors.Is(err, ErrInvalidID) {
			t.Errorf("DisplayIDOf(%q) error = %v, want ErrInvalidID", bad, err)
		}
	}
}

func TestIsIDAndIsDigest(t *testing.T) {
	t.Parallel()

	full := goldenVectors[0].wantID
	if !IsID(full) || !IsDigest(full) {
		t.Fatalf("%q should be both an id and a digest", full)
	}
	for _, bad := range []string{"", full[:63], full + "a", strings.ToUpper(full), strings.Repeat("g", 64)} {
		if IsID(bad) {
			t.Errorf("IsID(%q) = true", bad)
		}
	}
}

// TestTheFrozenRecipeIsUntouchedByTheWorkspaceOne is the promise the second
// domain was introduced to keep.
//
// v1 was frozen, and a frozen recipe that grew a field would not have been
// frozen. So the claim is not "the two differ" -- the vectors above say that in
// bytes -- but that adding the second one moved nothing: an identity with no
// module path hashes the same nine fields it always did, and every stored
// outcome, report and expectation minted before the workspace recipe existed
// still names the same mutant.
func TestTheFrozenRecipeIsUntouchedByTheWorkspaceOne(t *testing.T) {
	t.Parallel()

	for _, v := range goldenVectors {
		if v.modulePath != "" {
			continue
		}
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()

			// Stated the long way round, against the field list rather than
			// against the function: what is frozen is the recipe, and a test
			// that only called ID() would pass for a build in which the recipe
			// had been rewritten into something that agreed on these five
			// inputs and nothing else.
			id := v.identity()
			want := hashOf(t,
				IDDomain,
				id.Path,
				id.RuleName,
				strconv.Itoa(id.RuleVersion),
				strconv.FormatUint(uint64(id.Span.StartByte), 10),
				strconv.FormatUint(uint64(id.Span.EndByte), 10),
				id.SourceDigest,
				id.OriginalDigest,
				id.ReplacementDigest,
			)
			if want != v.wantID {
				t.Fatalf("the nine-field recipe hashes to %s and the frozen vector is %s", want, v.wantID)
			}
		})
	}
}

// TestTheWorkspaceRecipeIsTheNineFieldsPlusOne pins the second recipe the same
// way, so that both are described by their bytes rather than by their code.
func TestTheWorkspaceRecipeIsTheNineFieldsPlusOne(t *testing.T) {
	t.Parallel()

	for _, v := range goldenVectors {
		if v.modulePath == "" {
			continue
		}
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()

			id := v.identity()
			want := hashOf(t,
				IDDomainWorkspace,
				id.Path,
				id.RuleName,
				strconv.Itoa(id.RuleVersion),
				strconv.FormatUint(uint64(id.Span.StartByte), 10),
				strconv.FormatUint(uint64(id.Span.EndByte), 10),
				id.SourceDigest,
				id.OriginalDigest,
				id.ReplacementDigest,
				id.ModulePath,
			)
			if want != v.wantID {
				t.Fatalf("the ten-field recipe hashes to %s and the frozen vector is %s", want, v.wantID)
			}
		})
	}
}

// TestAModulePathThatCannotBeHashedIsRefused keeps the tenth field a field.
//
// The encoding is unambiguous whatever the bytes are, so this is not about the
// hash: it is about a module path that could only have arrived by mistake. A
// version suffix is the one worth naming -- `example.com/m@v2` is a request
// nobody made, and minting an identity for it would file a run under a module
// that does not exist.
func TestAModulePathThatCannotBeHashedIsRefused(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"example.com/m@v2", "example.com/m odule", "example.com/m\nx"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()

			id := goldenVectors[0].identity()
			id.ModulePath = bad
			if _, err := id.ID(); !errors.Is(err, ErrInvalidModulePath) {
				t.Errorf("ID() = %v, want %v", err, ErrInvalidModulePath)
			}
		})
	}
}

// hashOf is the identity encoding written out once, so that the two recipe
// tests above compare bytes with bytes rather than one call of ID() with
// another.
func hashOf(t *testing.T, fields ...string) string {
	t.Helper()

	h := sha256.New()
	for _, f := range fields {
		if err := WriteLengthPrefixed(h, f); err != nil {
			t.Fatalf("hashing %q: %v", f, err)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
