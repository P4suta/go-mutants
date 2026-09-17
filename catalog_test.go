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
				Selected:     true,
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
				Selected:     true,
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
				Selected:     true,
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
	write(strings.Repeat("2", 64))
	write(strings.Repeat("1", 64))
	write("example.com/m")
	write("1.26")
	write("go1.26.6")
	write("balanced")
	write("2")
	write("example.com/m/alpha")
	write("example.com/m/beta")
	write("3")
	write(strings.Repeat("a", 64))
	write("example.com/m/alpha")
	write("aps")
	write(strings.Repeat("b", 64))
	write("example.com/m/alpha")
	write("a-s")
	write(strings.Repeat("c", 64))
	write("example.com/m/beta")
	write("--s")
	write("1")
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

		{name: "the prepared digest already on the value", mutate: func(c *Catalog) {
			c.PreparedDigest = strings.Repeat("e", 64)
		}},
		{name: "the selected flag", mutate: func(c *Catalog) {
			c.Mutants[0].Selected = false
		}},
		{name: "the selection ranges it came from", mutate: func(c *Catalog) {
			c.Selection = &Selection{Lines: map[string][]LineRange{"alpha/alpha.go": {{First: 1, Last: 400}}}}
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

func TestPreparedDigestIsUnchangedBySelection(t *testing.T) {
	t.Parallel()

	const beforeSelectionExisted = "80f70d7825f17f43121bbe7c0619d635b6106110f6d77594275e3cb027d72f80"

	full := samplePreparedCatalog()
	for i, mutant := range full.Mutants {
		if !mutant.Selected {
			t.Fatalf("mutant %d of the sample catalogue is not Selected; the sample stands for a"+
				" preparation with no selection, where every mutant is", i)
		}
	}
	if got := preparedDigest(full); got != beforeSelectionExisted {
		t.Errorf("preparedDigest() = %s, want %s — the value it had before a selection could"+
			" narrow a session. Every consumer keyed on this has just missed for a session"+
			" that has not changed", got, beforeSelectionExisted)
	}

	for _, test := range []struct {
		name   string
		narrow func(*Catalog)
	}{
		{name: "a mutant left out of the selection", narrow: func(c *Catalog) {
			c.Mutants[1].Selected = false
		}},
		{name: "every mutant left out of the selection", narrow: func(c *Catalog) {
			for i := range c.Mutants {
				c.Mutants[i].Selected = false
			}
		}},
		{name: "the ranges the caller asked for", narrow: func(c *Catalog) {
			c.Selection = &Selection{Lines: map[string][]LineRange{
				"alpha/alpha.go": {{First: 1, Last: 400}},
			}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			c := cloneCatalog(samplePreparedCatalog())
			test.narrow(&c)
			if got := preparedDigest(c); got != beforeSelectionExisted {
				t.Errorf("preparedDigest() = %s after narrowing by %s, want the unchanged %s;"+
					" a consumer's per-mutant evidence is about the tree and the mutant, and"+
					" moving its key for a plan it did not act on costs it every stored row",
					got, test.name, beforeSelectionExisted)
			}
		})
	}
}

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
		want    int
	}{
		{name: "no facts at all", indices: nil, want: -1},
		{name: "a measured empty set", indices: []uint32{}, want: -1},
		{name: "one probed mutant", indices: []uint32{0}, want: -1},
		{name: "every probed mutant", indices: []uint32{0, 2}, want: -1},
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

func probeInfected(raw []uint32, mutants []Mutant) error {
	if err := checkInfectedShape(raw, len(mutants)); err != nil {
		return err
	}
	return checkInfectedProbed(filterInfected(raw, mutants), mutants)
}

func TestProbeRefusesARawIndexTheFilterWouldHaveHidden(t *testing.T) {
	t.Parallel()

	mutants := []Mutant{
		{Index: 0, DisplayID: "aaaa", Accepted: true, Probed: true},
		{Index: 1, DisplayID: "bbbb", Accepted: true, Probed: true},
	}
	raw := []uint32{0, 7}

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

func TestCheckInfectedRendersTheSessionProbeMessage(t *testing.T) {
	t.Parallel()

	const want = "gomutants: session probe: the probe log names a mutant the catalogue" +
		" cannot account for: index 7"
	for name, err := range map[string]error{
		"shape":  checkInfectedShape([]uint32{7}, 1),
		"probed": checkInfectedProbed([]uint32{7}, make([]Mutant, 8)),
	} {
		if err == nil || err.Error() != want {
			t.Errorf("the %s check reported %v, want %q", name, err, want)
		}
	}
}
