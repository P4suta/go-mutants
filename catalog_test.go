// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// samplePreparedCatalog is a catalogue with one of everything the prepared
// digest hashes and one of everything it does not: three mutants covering the
// three flag combinations a real preparation produces, two test packages so
// that a count and an order are both observable, and one rejection naming the
// mutant that is not accepted.
//
// It is built by hand rather than prepared, because the recipe is a claim about
// the encoding and not about any run: a test that hashed a real catalogue could
// only compare the value to itself.
func samplePreparedCatalog() Catalog {
	return Catalog{
		WorkspaceDigest: strings.Repeat("1", 64),
		Digest:          strings.Repeat("2", 64),
		ModulePath:      "example.com/m",
		GoVersion:       "1.26",
		Toolchain:       "go1.26.6",
		Profile:         "balanced",
		TestPackages:    []string{"example.com/m/alpha", "example.com/m/beta"},
		Mutants: []Mutant{
			{
				Index:        0,
				ID:           strings.Repeat("a", 64),
				DisplayID:    strings.Repeat("a", 20),
				Path:         "alpha/alpha.go",
				Package:      "example.com/m/alpha",
				Line:         7,
				Column:       9,
				EndLine:      7,
				StartByte:    40,
				EndByte:      42,
				Family:       "comparison",
				Rule:         "eq-to-ne",
				RuleVersion:  1,
				SourceDigest: strings.Repeat("3", 64),
				Original:     "==",
				Replacement:  "!=",
				Accepted:     true,
				Probed:       true,
			},
			{
				Index:        1,
				ID:           strings.Repeat("b", 64),
				DisplayID:    strings.Repeat("b", 20),
				Path:         "alpha/alpha.go",
				Package:      "example.com/m/alpha",
				Line:         11,
				Column:       2,
				EndLine:      11,
				StartByte:    60,
				EndByte:      64,
				Family:       "literal",
				Rule:         "true-to-false",
				RuleVersion:  1,
				SourceDigest: strings.Repeat("3", 64),
				Original:     "true",
				Replacement:  "false",
				Accepted:     true,
				Probed:       false,
			},
			{
				Index:        2,
				ID:           strings.Repeat("c", 64),
				DisplayID:    strings.Repeat("c", 20),
				Path:         "beta/beta.go",
				Package:      "example.com/m/beta",
				Line:         3,
				Column:       5,
				EndLine:      3,
				StartByte:    20,
				EndByte:      21,
				Family:       "arithmetic",
				Rule:         "add-to-sub",
				RuleVersion:  1,
				SourceDigest: strings.Repeat("4", 64),
				Original:     "+",
				Replacement:  "-",
				Accepted:     false,
				Probed:       false,
			},
		},
		Rejections: []Rejection{{
			ID:         strings.Repeat("c", 64),
			DisplayID:  strings.Repeat("c", 20),
			Path:       "beta/beta.go",
			Line:       3,
			Column:     5,
			Rule:       "add-to-sub",
			Diagnostic: "beta/beta.go:3:5: invalid operation",
		}},
	}
}

// TestPreparedDigestFollowsTheDocumentedRecipe recomputes the digest here, from
// the words of the documentation rather than from the implementation.
//
// The encoding is written out again in this test — a four-byte big-endian byte
// length and then the bytes — instead of calling the engine's own helper,
// because the value is a wire format. A consumer keying its evidence on it has
// to be able to reproduce it from the recipe alone, and a test that reused the
// engine's encoder would go on passing if the encoder changed under both.
func TestPreparedDigestFollowsTheDocumentedRecipe(t *testing.T) {
	t.Parallel()

	c := samplePreparedCatalog()

	h := sha256.New()
	write := func(field string) {
		t.Helper()
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], uint32(len(field)))
		if _, err := h.Write(prefix[:]); err != nil {
			t.Fatalf("writing the length prefix of %q: %v", field, err)
		}
		if _, err := h.Write([]byte(field)); err != nil {
			t.Fatalf("writing %q: %v", field, err)
		}
	}

	write("go-mutants-prepared-catalog-v1")
	write(strings.Repeat("2", 64)) // Digest
	write(strings.Repeat("1", 64)) // WorkspaceDigest
	write("example.com/m")         // ModulePath
	write("1.26")                  // GoVersion
	write("go1.26.6")              // Toolchain
	write("balanced")              // Profile
	write("2")                     // len(TestPackages)
	write("example.com/m/alpha")
	write("example.com/m/beta")
	write("3") // len(Mutants)
	write(strings.Repeat("a", 64))
	write("example.com/m/alpha")
	write("aps") // accepted, probed, selected
	write(strings.Repeat("b", 64))
	write("example.com/m/alpha")
	write("a-s") // accepted, not probed, selected
	write(strings.Repeat("c", 64))
	write("example.com/m/beta")
	write("--s") // rejected, not probed, selected
	write("1")   // len(Rejections)
	write(strings.Repeat("c", 64))
	want := hex.EncodeToString(h.Sum(nil))

	got := preparedDigest(c)
	if got != want {
		t.Errorf("preparedDigest() = %s, want %s;\nthe recipe in the documentation and the"+
			" implementation have moved apart, and a consumer keyed on this value cannot tell", got, want)
	}
	if len(got) != 64 {
		t.Errorf("preparedDigest() = %q, want 64 lowercase hex characters", got)
	}
}

// TestPreparedDigestIsSensitiveToExactlyItsInputs is the pair of claims that
// makes the digest usable as a cache key.
//
// A field inside the recipe that did not move it would let a consumer reuse
// evidence gathered against a different session; a field outside the recipe
// that did move it would invalidate a cache every time a line number shifted,
// which is the cost the plain [Catalog.Digest] already pays for being narrow.
//
// The table is a *ledger*: every field of [Catalog], [Mutant] and [Rejection]
// appears in one half of it or the other. That is what makes it worth reading
// as the answer to "does X change the key?" — a field missing from a table that
// looked complete would be read as one nobody had needed to decide about, when
// it is in fact one nobody has checked. Adding a field to the API means adding
// a row here, and the recipe on [Catalog.PreparedDigest] lists the same split
// in prose.
func TestPreparedDigestIsSensitiveToExactlyItsInputs(t *testing.T) {
	t.Parallel()

	base := preparedDigest(samplePreparedCatalog())

	for _, test := range []struct {
		name   string
		mutate func(*Catalog)
		moves  bool
	}{
		{name: "the mutant set digest", moves: true, mutate: func(c *Catalog) {
			c.Digest = strings.Repeat("5", 64)
		}},
		{name: "the workspace digest", moves: true, mutate: func(c *Catalog) {
			c.WorkspaceDigest = strings.Repeat("5", 64)
		}},
		{name: "the module path", moves: true, mutate: func(c *Catalog) {
			c.ModulePath = "example.com/other"
		}},
		{name: "the go version", moves: true, mutate: func(c *Catalog) {
			c.GoVersion = "1.27"
		}},
		{name: "the toolchain", moves: true, mutate: func(c *Catalog) {
			c.Toolchain = "go1.27.0"
		}},
		{name: "the profile", moves: true, mutate: func(c *Catalog) {
			c.Profile = "strong"
		}},
		{name: "a test package", moves: true, mutate: func(c *Catalog) {
			c.TestPackages[1] = "example.com/m/gamma"
		}},
		{name: "the number of test packages", moves: true, mutate: func(c *Catalog) {
			c.TestPackages = c.TestPackages[:1]
		}},
		{name: "a mutant identity", moves: true, mutate: func(c *Catalog) {
			c.Mutants[1].ID = strings.Repeat("d", 64)
		}},
		{name: "a mutant package", moves: true, mutate: func(c *Catalog) {
			c.Mutants[1].Package = "example.com/m/gamma"
		}},
		{name: "the accepted flag", moves: true, mutate: func(c *Catalog) {
			c.Mutants[1].Accepted = false
		}},
		{name: "the probed flag", moves: true, mutate: func(c *Catalog) {
			c.Mutants[0].Probed = false
		}},
		{name: "the mutant order", moves: true, mutate: func(c *Catalog) {
			c.Mutants[0], c.Mutants[1] = c.Mutants[1], c.Mutants[0]
		}},
		{name: "a rejection identity", moves: true, mutate: func(c *Catalog) {
			c.Rejections[0].ID = strings.Repeat("d", 64)
		}},
		{name: "the number of rejections", moves: true, mutate: func(c *Catalog) {
			c.Rejections = nil
		}},

		// The digest is not hashed into itself, so a catalogue that already
		// carries one hashes to the same value. That is what lets makeCatalog
		// compute it last, over the finished struct, rather than over a copy
		// with the field blanked.
		{name: "the prepared digest already on the value", mutate: func(c *Catalog) {
			c.PreparedDigest = strings.Repeat("e", 64)
		}},
		{name: "an index", mutate: func(c *Catalog) {
			c.Mutants[0].Index = 999
		}},
		{name: "a line number", mutate: func(c *Catalog) {
			c.Mutants[0].Line = 999
		}},
		{name: "a column", mutate: func(c *Catalog) {
			c.Mutants[0].Column = 999
		}},
		{name: "an end line", mutate: func(c *Catalog) {
			c.Mutants[0].EndLine = 999
		}},
		{name: "a display identity", mutate: func(c *Catalog) {
			c.Mutants[0].DisplayID = strings.Repeat("f", 20)
		}},
		{name: "a source path", mutate: func(c *Catalog) {
			c.Mutants[0].Path = "gamma/gamma.go"
		}},
		{name: "a start byte", mutate: func(c *Catalog) {
			c.Mutants[0].StartByte = 999
		}},
		{name: "an end byte", mutate: func(c *Catalog) {
			c.Mutants[0].EndByte = 999
		}},
		{name: "a family", mutate: func(c *Catalog) {
			c.Mutants[0].Family = "arithmetic"
		}},
		{name: "a rule name", mutate: func(c *Catalog) {
			c.Mutants[0].Rule = "ne-to-eq"
		}},
		{name: "a rule version", mutate: func(c *Catalog) {
			c.Mutants[0].RuleVersion = 7
		}},
		{name: "a source digest", mutate: func(c *Catalog) {
			c.Mutants[0].SourceDigest = strings.Repeat("9", 64)
		}},
		{name: "the original text", mutate: func(c *Catalog) {
			c.Mutants[0].Original = "<="
		}},
		{name: "the replacement text", mutate: func(c *Catalog) {
			c.Mutants[0].Replacement = ">="
		}},
		{name: "a branch proof", mutate: func(c *Catalog) {
			c.Mutants[0].Branch = &BranchProof{
				Direction:       BranchDecreasing,
				BodyStartLine:   7,
				BodyStartColumn: 20,
				BodyEndLine:     9,
				BodyEndColumn:   2,
			}
		}},
		{name: "a rejection display identity", mutate: func(c *Catalog) {
			c.Rejections[0].DisplayID = strings.Repeat("f", 20)
		}},
		{name: "a rejection path", mutate: func(c *Catalog) {
			c.Rejections[0].Path = "gamma/gamma.go"
		}},
		{name: "a rejection line", mutate: func(c *Catalog) {
			c.Rejections[0].Line = 999
		}},
		{name: "a rejection column", mutate: func(c *Catalog) {
			c.Rejections[0].Column = 999
		}},
		{name: "a rejection rule", mutate: func(c *Catalog) {
			c.Rejections[0].Rule = "ne-to-eq"
		}},
		{name: "a rejection diagnostic", mutate: func(c *Catalog) {
			c.Rejections[0].Diagnostic = "beta/beta.go:3:5: something else entirely"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			c := cloneCatalog(samplePreparedCatalog())
			test.mutate(&c)
			got := preparedDigest(c)
			switch {
			case test.moves && got == base:
				t.Errorf("changing %s left the prepared digest at %s; it is part of the recipe,"+
					" so a consumer would reuse evidence from a session this is not", test.name, got)
			case !test.moves && got != base:
				t.Errorf("changing %s moved the prepared digest from %s to %s; it is outside the"+
					" recipe, so every consumer's cache would miss for a fact it already holds",
					test.name, base, got)
			}
		})
	}
}

// TestEndLineCountsNewlines pins the one rule the CLI's `--changed` already
// applies, so that a consumer selecting by line range gets the same answer from
// the catalogue as it would from `go-mutants run --changed`.
//
// The carriage return case is the one worth writing down: a CRLF file's line
// break is one newline preceded by a byte that is not one, so counting "\n" is
// exactly right and counting line terminators would double it.
func TestEndLineCountsNewlines(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		line     int
		original string
		want     int
	}{
		{name: "a span inside one line", line: 10, original: "x", want: 10},
		{name: "an empty original", line: 10, original: "", want: 10},
		{name: "a span over three lines", line: 10, original: "a\nb\nc", want: 12},
		{name: "a span with carriage returns", line: 10, original: "a\r\nb", want: 11},
		{name: "a span ending on a newline", line: 10, original: "a\n", want: 11},
		{name: "the first line", line: 1, original: "if x {\n\treturn 1\n}", want: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := endLine(test.line, test.original); got != test.want {
				t.Errorf("endLine(%d, %q) = %d, want %d", test.line, test.original, got, test.want)
			}
		})
	}
}

// TestCheckInfectedRefusesInconsistentSets is the engine holding itself to the
// promise [ProbeResult.Infected] makes.
//
// Every refusal here is an engine bug rather than a caller's mistake: the
// indices come from go-mutants' own probe runtime and are filtered by
// go-mutants' own catalogue. That is exactly why it is an error and not a
// dropped index — a set quietly repaired would be reported as a measurement,
// and a measurement licenses a consumer to skip executions. The empty set and
// nil are the two shapes that must survive untouched, because they are the two
// answers that mean something.
func TestCheckInfectedRefusesInconsistentSets(t *testing.T) {
	t.Parallel()

	mutants := []Mutant{
		{Index: 0, DisplayID: "aaaa", Accepted: true, Probed: true},
		{Index: 1, DisplayID: "bbbb", Accepted: true, Probed: false},
		{Index: 2, DisplayID: "cccc", Accepted: true, Probed: true},
		{Index: 3, DisplayID: "dddd", Accepted: false, Probed: false},
	}

	for _, test := range []struct {
		name    string
		indices []uint32
		want    int // the index the message must name; -1 for no error
	}{
		{name: "no facts at all", indices: nil, want: -1},
		{name: "a measured empty set", indices: []uint32{}, want: -1},
		{name: "one probed mutant", indices: []uint32{0}, want: -1},
		{name: "every probed mutant", indices: []uint32{0, 2}, want: -1},
		// The one surprising index that is not a bug: the probe tree is
		// instrumented from the whole catalogue, so its log legitimately names a
		// site whose mutation did not compile. That index is dropped, not refused.
		{name: "a mutant the mutant tree rejected", indices: []uint32{0, 3}, want: -1},
		{name: "an unsorted set", indices: []uint32{2, 0}, want: 0},
		{name: "a duplicated index", indices: []uint32{0, 0}, want: 0},
		{name: "an index past the catalogue", indices: []uint32{4}, want: 4},
		{name: "an index far past the catalogue", indices: []uint32{0, 4096}, want: 4096},
		{name: "an unprobed mutant", indices: []uint32{1}, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := probeInfected(test.indices, mutants)
			if test.want < 0 {
				if err != nil {
					t.Fatalf("the probe check of %v = %v, want no error: the set is one the"+
						" catalogue accounts for", test.indices, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("the probe check of %v = nil; an index the catalogue cannot account"+
					" for must surface as an error rather than as a fact", test.indices)
			}
			if !errors.Is(err, ErrProbeInconsistent) {
				t.Errorf("the probe check of %v = %v, which is not ErrProbeInconsistent;"+
					" a consumer classifies this as an engine bug by the sentinel", test.indices, err)
			}
			if named := fmt.Sprintf("index %d", test.want); !strings.Contains(err.Error(), named) {
				t.Errorf("the probe check of %v = %q, which does not name %q; the index is the"+
					" whole of the bug report", test.indices, err, named)
			}
		})
	}
}

// probeInfected runs the checks [Session.Probe] runs, in the order it runs
// them, over a raw set as the probe runtime would have written it.
//
// The order is half the contract, so the tests drive the sequence rather than
// one function of it: proving the shape of the raw set and the probe status of
// what survives filtering are two different claims about two different sets,
// and a test that only ever saw the filtered one could not tell whether the
// first had been made at all.
func probeInfected(raw []uint32, mutants []Mutant) error {
	if err := checkInfectedShape(raw, len(mutants)); err != nil {
		return err
	}
	return checkInfectedProbed(filterInfected(raw, mutants), mutants)
}

// TestProbeRefusesARawIndexTheFilterWouldHaveHidden is the ordering claim, and
// it is the reason the shape of the set is proved before anything is dropped
// from it.
//
// [filterInfected] exists to drop the indices the mutant tree's validation
// rejected, and it is written not to panic on one outside the catalogue — so it
// drops those too, in silence. That is the right behaviour for a filter and the
// wrong place for the only bounds check: an index past the end of the catalogue
// is the probe runtime writing about a catalogue that is not this one, which is
// exactly the engine bug [ErrProbeInconsistent] exists to surface. Checked
// after the filter it is indistinguishable from a rejected mutant, and the pass
// is handed over as a measurement.
func TestProbeRefusesARawIndexTheFilterWouldHaveHidden(t *testing.T) {
	t.Parallel()

	mutants := []Mutant{
		{Index: 0, DisplayID: "aaaa", Accepted: true, Probed: true},
		{Index: 1, DisplayID: "bbbb", Accepted: true, Probed: true},
	}
	raw := []uint32{0, 7}

	// The premise: filtering leaves a set nothing downstream could object to.
	// Without it the claim below could hold for the wrong reason.
	if filtered := filterInfected(raw, mutants); len(filtered) != 1 || filtered[0] != 0 {
		t.Fatalf("filterInfected(%v) = %v; the premise of this test is that the filter"+
			" drops the offending index and leaves a well-formed set", raw, filtered)
	}

	err := probeInfected(raw, mutants)
	if err == nil {
		t.Fatalf("the probe accepted %v as a measurement; index 7 is outside a two-mutant"+
			" catalogue, which is the probe runtime writing about a catalogue that is not"+
			" this one and not a mutant validation rejected", raw)
	}
	if !errors.Is(err, ErrProbeInconsistent) {
		t.Errorf("probe of %v = %v, which is not ErrProbeInconsistent", raw, err)
	}
	if !strings.Contains(err.Error(), "index 7") {
		t.Errorf("probe of %v = %q, which does not name index 7", raw, err)
	}
}

// TestCheckInfectedRendersTheSessionProbeMessage pins the sentence, because it
// is the one a consumer sees and the C.1 prefix every probe failure carries.
func TestCheckInfectedRendersTheSessionProbeMessage(t *testing.T) {
	t.Parallel()

	const want = "gomutants: session probe: the probe log names a mutant the catalogue" +
		" cannot account for: index 7"
	// Both checks render it, and a consumer must not be able to tell which
	// fired: which half of the invariant an engine bug broke is not its business.
	for name, err := range map[string]error{
		"shape":  checkInfectedShape([]uint32{7}, 1),
		"probed": checkInfectedProbed([]uint32{7}, make([]Mutant, 8)),
	} {
		if err == nil || err.Error() != want {
			t.Errorf("the %s check reported %v, want %q", name, err, want)
		}
	}
}
