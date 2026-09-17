// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"strings"
	"testing"
)

// TestAResultAlreadySpellingItsReplacementIsNotAMutation is the constant-value
// half of the refusal [fileScan.replaceReturn] already makes textually.
//
// `return 0` is refused because the bytes are the same. `return Disjoint`,
// where `Disjoint` is the first name of an iota block, is a different spelling
// of the same constant: go/types folds both to 0, the conversion the `return`
// performs is the same conversion, and the two trees compile to one program.
// No test can tell them apart, so cataloguing one costs a suite the whole
// per-mutant budget to learn nothing, and declaring it costs a reader a ledger
// row arguing about a mutant that was never a question.
//
// It is not a skip, for the same reason the textual case is not: a skip site is
// a place go-mutants declined to mutate, and this is a place where the mutation
// and the source are the same program.
func TestAResultAlreadySpellingItsReplacementIsNotAMutation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		src  string
		want []string
		gone []string
	}{{
		// The shape this refusal was found by: the dogfood gate reported it
		// twice in this repository, once for a Relation and once for an
		// Outcome, and both were declared rather than removed.
		name: "the first name of an iota block",
		src: `package pkg

type Relation int

const (
	Disjoint Relation = iota
	Before
)

func First() Relation { return Disjoint }

func Second() Relation { return Before }
`,
		want: []string{"return-zero-numeric Before->0"},
		gone: []string{"return-zero-numeric Disjoint->0"},
	}, {
		// A constant that is zero without being spelled zero, and its
		// counterpart that is not: the refusal is about the value go/types
		// folded, never about how many characters it took to write.
		name: "a named constant folded to zero",
		src: `package pkg

const (
	None  = 0
	Three = 1 + 2
)

func Nothing() int { return None }

func Some() int { return Three }
`,
		want: []string{"return-zero-numeric Three->0"},
		gone: []string{"return-zero-numeric None->0"},
	}, {
		// Untyped float zero. The bytes differ from `0` and the value does
		// not: Go constants are exact, so `0.0` is the same constant as `0`
		// and converts to the same float64.
		name: "a float literal that is zero",
		src: `package pkg

func Zero() float64 { return 0.0 }

func Half() float64 { return 0.5 }
`,
		want: []string{"return-zero-numeric 0.5->0"},
		gone: []string{"return-zero-numeric 0.0->0"},
	}, {
		// A complex constant whose two parts are both zero. The replacement is
		// an integer constant and the result is complex, which is a comparison
		// go/constant makes by promoting both to the wider kind -- the same
		// promotion the compiler would have made.
		name: "a complex constant that is zero",
		src: `package pkg

func Origin() complex128 { return complex(0, 0) }

func Unit() complex128 { return complex(1, 0) }
`,
		want: []string{"return-zero-numeric complex(1, 0)->0"},
		gone: []string{"return-zero-numeric complex(0, 0)->0"},
	}, {
		name: "a named constant folded to the empty string",
		src: `package pkg

type Name string

const (
	Anonymous Name = ""
	Someone   Name = "someone"
)

func Nobody() Name { return Anonymous }

func Somebody() Name { return Someone }
`,
		want: []string{`return-empty-string Someone->""`},
		gone: []string{`return-empty-string Anonymous->""`},
	}, {
		// Only the matching direction is refused. A constantly-true result is
		// still worth settling false: that is a function whose answer stops
		// being the one every caller relies on.
		name: "a boolean constant refuses one of the two rules",
		src: `package pkg

const Always = 1 < 2

func Yes() bool { return Always }
`,
		want: []string{"return-false Always->false"},
		gone: []string{"return-true Always->true"},
	}, {
		// The gate is the folded value and nothing else: a result the checker
		// folded to nothing is a result whose value is decided at run time, and
		// every rule applies to it.
		name: "a result that is not a constant at all",
		src: `package pkg

func Echo(v int) int { return v }
`,
		want: []string{"return-zero-numeric v->0"},
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := scanSource(t, test.src)
			for _, want := range test.want {
				rule, rest, _ := strings.Cut(want, " ")
				original, replacement, _ := strings.Cut(rest, "->")
				if !got.has(rule, original, replacement) {
					t.Errorf("scan found %v, want %q among them", got.rules(), want)
				}
			}
			for _, gone := range test.gone {
				rule, rest, _ := strings.Cut(gone, " ")
				original, replacement, _ := strings.Cut(rest, "->")
				if got.has(rule, original, replacement) {
					t.Errorf("scan found %q, and the mutant is the program it mutates", gone)
				}
			}
			for _, site := range got.sites {
				if strings.HasPrefix(site.Rule, "return-") {
					t.Errorf("scan recorded %v, want no skip: a replacement equal to its "+
						"original is not a site go-mutants declined to mutate", got.skips())
				}
			}
		})
	}
}

// TestTheConstantRefusalLeavesTheNilFamiliesAlone states the boundary from the
// other side.
//
// `nil` is not a constant -- go/types folds no value for it -- so neither
// return-nil nor return-err-to-nil can ever be refused this way, however the
// result is spelled. A gate that answered otherwise would have to be answering
// some question other than "did the checker fold these two to one value", and
// the two rules that write `nil` are the ones where that matters most: a
// function that already returns nil on this path is exactly the function whose
// other paths are worth mutating.
func TestTheConstantRefusalLeavesTheNilFamiliesAlone(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

import "errors"

type Box struct{ n int }

func Wrap(n int) *Box { return &Box{n: n} }

func Fail() error { return errors.New("no") }
`)
	for _, want := range [][3]string{
		{"return-nil", "&Box{n: n}", "nil"},
		{"return-err-to-nil", `errors.New("no")`, "nil"},
	} {
		if !got.has(want[0], want[1], want[2]) {
			t.Errorf("scan found %v, want a %s candidate", got.rules(), want[0])
		}
	}
}
