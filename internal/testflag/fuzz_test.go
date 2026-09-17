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
