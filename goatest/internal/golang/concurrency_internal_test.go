// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import "testing"

func TestAConcurrencyNodeIsEveryShapeTheLanguageStartsOneWith(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		source string
		want   bool
	}{
		{name: "a goroutine", source: "package subject\n\nfunc Subject() { go work() }\n", want: true},
		{
			name:   "a send",
			source: "package subject\n\nfunc Subject(c chan int) { c <- 1 }\n", want: true,
		},
		{
			name:   "a receive",
			source: "package subject\n\nfunc Subject(c chan int) int { return <-c }\n", want: true,
		},
		{
			name:   "a receive in a statement",
			source: "package subject\n\nfunc Subject(c chan int) { <-c }\n", want: true,
		},
		{
			name:   "a channel in a signature",
			source: "package subject\n\nfunc Subject(c chan int) {}\n", want: true,
		},
		{
			name:   "a select",
			source: "package subject\n\nfunc Subject() { select {} }\n", want: true,
		},
		{
			name:   "a negation, which is the other unary operator",
			source: "package subject\n\nfunc Subject(v bool) bool { return !v }\n",
		},
		{
			name:   "an address, which is another one",
			source: "package subject\n\nfunc Subject(v int) *int { return &v }\n",
		},
		{
			name:   "arithmetic nobody waits on",
			source: "package subject\n\nfunc Subject(v int) int { return -v }\n",
		},
		{name: "a package that starts nothing", source: "package subject\n\nfunc Subject() int { return 1 }\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := hasConcurrencyNode(parseSubject(t, test.source)); got != test.want {
				t.Fatalf("hasConcurrencyNode(%q) = %t, want %t", test.source, got, test.want)
			}
		})
	}
}
