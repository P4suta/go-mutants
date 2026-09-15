// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testflag_test

import (
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testflag"
)

// FuzzMatchAgreesWithTheFlagPackage is the promise this package makes, stated
// against the only authority there is.
//
// [testflag.Match] exists so that a safety check can ask "does this argv
// element name -count", and every caller of it is a refusal: a `test.command`
// that already passes a flag go-mutants is about to pass itself, a narrowing
// that would be overridden by the user's own `-run`. A recogniser that missed a
// spelling would let such a command through and measure something other than
// what it claims to; one that saw a flag where the test binary will not would
// refuse a command that is fine.
//
// Neither failure can be found by a table, because the set of spellings is the
// standard library's rather than this package's: one dash or two, an attached
// value or none, and the terminators. So the oracle is `flag` itself, declared
// with a flag of the given name and asked whether the argument set it.
//
// A bool-shaped flag is used deliberately. The question is about one argv
// element, and a flag that takes its value from the *next* element would make
// the oracle about two.
func FuzzMatchAgreesWithTheFlagPackage(f *testing.F) {
	f.Add("-count", "count")
	f.Add("--count", "count")
	f.Add("-count=1", "count")
	f.Add("--count=1", "count")
	f.Add("-count=", "count")
	f.Add("-counts", "count")
	f.Add("-count1", "count")
	f.Add("count", "count")
	f.Add("---count", "count")
	f.Add("-", "count")
	f.Add("--", "count")
	f.Add("", "count")
	f.Add("-run", "count")
	f.Add("-COUNT", "count")
	f.Add("-count=-1", "count")
	f.Add("-count==1", "count")
	f.Add("-test.count", "test.count")
	f.Add("--test.run=^$", "test.run")

	f.Fuzz(func(t *testing.T, argument, name string) {
		// The names `flag` itself refuses are not names go-mutants asks about:
		// every call site passes a literal such as "count" or "test.run". A
		// FlagSet panics on the rest, so there is no oracle for them and no
		// claim to make.
		if name == "" || strings.ContainsAny(name, "=") || strings.HasPrefix(name, "-") {
			return
		}

		var seen settable
		set := flag.NewFlagSet("oracle", flag.ContinueOnError)
		set.SetOutput(io.Discard)
		set.Var(&seen, name, "")
		_ = set.Parse([]string{argument})

		if got := testflag.Match(argument, name); got != bool(seen) {
			t.Fatalf("Match(%q, %q) = %v, and the flag package %s it",
				argument, name, got, verb(bool(seen)))
		}
	})
}

// settable is a flag that takes any value, including none, and remembers that
// it was given one.
type settable bool

func (s *settable) String() string   { return "" }
func (s *settable) Set(string) error { *s = true; return nil }
func (s *settable) IsBoolFlag() bool { return true }
func verb(parsed bool) string {
	if parsed {
		return "did read"
	}
	return "did not read"
}
