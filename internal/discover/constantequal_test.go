// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"strings"
	"testing"
)

func TestAResultAlreadySpellingItsReplacementIsNotAMutation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		src  string
		want []string
		gone []string
	}{{
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
		name: "a float literal that is zero",
		src: `package pkg

func Zero() float64 { return 0.0 }

func Half() float64 { return 0.5 }
`,
		want: []string{"return-zero-numeric 0.5->0"},
		gone: []string{"return-zero-numeric 0.0->0"},
	}, {
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
		name: "a boolean constant refuses one of the two rules",
		src: `package pkg

const Always = 1 < 2

func Yes() bool { return Always }
`,
		want: []string{"return-false Always->false"},
		gone: []string{"return-true Always->true"},
	}, {
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
