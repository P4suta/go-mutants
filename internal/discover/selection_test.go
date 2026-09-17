// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

const selectionCorpus = `package pkg

import "errors"

type flag bool

var sentinel = errors.New("x")

func widest(a, b int, f float64, s string, xs []int, ok bool, err error) int {
	if a < b && ok || !ok {
		return a
	}
	if err != nil {
		return 0
	}
	n := a + b
	n -= 1
	n++
	m := a & b
	m = a << 2
	total := f * 2.0
	_ = total
	_ = s + "x"
	_ = xs
	_ = true
	_ = flag(ok)
	use(n, m)
outer:
	for i := 0; i < b; i++ {
		switch {
		case i > 3:
			break outer
		}
	}
	return n
}

func use(...int) {}

func strings(s string) string { return s }

func slice(xs []int) []int { return xs }

func fails() error { return sentinel }

func truthy(ok bool) bool { return ok }
`

func TestARuleNobodySelectedProposesNothing(t *testing.T) {
	t.Parallel()

	everything := scanSource(t, selectionCorpus)
	if len(everything.candidates) == 0 {
		t.Fatal("the corpus produces no candidates at all")
	}
	proposed := make(map[string]int)
	for _, c := range everything.candidates {
		proposed[c.Rule.Name]++
	}

	for _, rule := range SupportedRules() {
		t.Run(rule.Name, func(t *testing.T) {
			t.Parallel()

			got := scanWith(t, selectionCorpus, func(other mutation.Rule) bool {
				return other.Name != rule.Name
			})
			for _, c := range got.candidates {
				if c.Rule.Name == "" {
					t.Fatalf("a candidate at %s carries no rule name, which no identity can be minted from", c.Span)
				}
				if c.Rule.Name == rule.Name {
					t.Fatalf("%s proposed %s after being deselected", rule.Name, c.Span)
				}
			}
			left := make(map[string]int)
			for _, c := range got.candidates {
				left[c.Rule.Name]++
			}
			for name, count := range proposed {
				if name == rule.Name {
					continue
				}
				if left[name] != count {
					t.Errorf("deselecting %s changed %s from %d candidates to %d",
						rule.Name, name, count, left[name])
				}
			}
		})
	}
}

func TestAnErrorBranchIsOnlyTheOneThatFires(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		want int
	}{
		{
			name: "err != nil",
			src:  "package pkg\n\nfunc probe(err error) int {\n\tif err != nil {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
			want: 1,
		},
		{
			name: "nil != err",
			src:  "package pkg\n\nfunc probe(err error) int {\n\tif nil != err {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
			want: 1,
		},
		{
			name: "err == nil",
			src:  "package pkg\n\nfunc probe(err error) int {\n\tif err == nil {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
		},
		{
			name: "a comparison of two errors",
			src:  "package pkg\n\nfunc probe(a, b error) int {\n\tif a != b {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
		},
		{
			name: "a comparison of something that is not an error",
			src:  "package pkg\n\nfunc probe(xs []int) int {\n\tif xs != nil {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
		},
		{
			name: "a concrete error implementor",
			src: "package pkg\n\ntype myErr struct{}\n\nfunc (*myErr) Error() string { return \"\" }\n\n" +
				"func probe(e *myErr) int {\n\tif e != nil {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
			want: 1,
		},
		{
			name: "a nil the package declared",
			src:  "package pkg\n\nvar nil = 0\n\nfunc probe(n int) int {\n\tif n != nil {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := 0
			for _, candidate := range scanSource(t, c.src).candidates {
				if candidate.Rule.Name == ruleNilErrorBranch {
					got++
				}
			}
			if got != c.want {
				t.Errorf("%s produced %d nil-error-branch candidates, want %d", c.name, got, c.want)
			}
		})
	}
}
