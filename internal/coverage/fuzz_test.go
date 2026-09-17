// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
)

func FuzzParseTextfmt(f *testing.F) {
	f.Add("mode: set\n")
	f.Add("mode: count\nexample.com/a/a.go:3.31,4.12 1 1\n")
	f.Add("mode: atomic\nexample.com/a/a.go:1.1,2.2 0 0\n")
	f.Add("")
	f.Add("\n")
	f.Add("\n\n\n")
	f.Add("mode: set")
	f.Add("mode:set\n")
	f.Add("mode: \n")
	f.Add("example.com/a.go:1.1,2.2 1 1\n")
	f.Add("mode: set\nmode: set\n")
	f.Add("mode: set\n\nexample.com/a.go:1.1,2.2 1 1\n\n")
	f.Add("mode: set\r\nexample.com/a.go:1.1,2.2 1 1\r\n")
	f.Add("mode: set\nexample.com/a.go:1.1,2.2 1\n")
	f.Add("mode: set\nexample.com/a.go:1.1,2.2 1 1 1\n")
	f.Add("mode: set\nexample.com/a.go:x.1,2.2 1 1\n")
	f.Add("mode: set\nexample.com/a.go:1.1,2.2 -1 -1\n")
	f.Add("mode: set\nexample.com/a.go:99999999999999999999.1,2.2 1 1\n")
	f.Add("mode: set\na b c:1.1,2.2 1 1\n")
	f.Add("mode: set\n:1.1,2.2 1 1\n")
	f.Add("mode: set\n\x00\n")
	f.Add("mode: set\n\xff\xfe\n")

	f.Fuzz(func(t *testing.T, document string) {
		profile, err := coverage.ParseTextfmt(strings.NewReader(document))
		if err != nil {
			var refusal *coverage.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("ParseTextfmt returned %T, want a *coverage.Error a caller can branch on: %v", err, err)
			}
			if refusal.Code != coverage.CodeMalformedProfile {
				t.Fatalf("ParseTextfmt refused with %s, want %s", refusal.Code, coverage.CodeMalformedProfile)
			}
			if refusal.Error() == "" {
				t.Fatalf("ParseTextfmt refused with an empty message")
			}
			return
		}

		if profile.Mode == "" {
			t.Fatalf("ParseTextfmt accepted a document and reported no mode:\n%q", document)
		}
		rendered := renderProfile(profile)
		again, err := coverage.ParseTextfmt(strings.NewReader(rendered))
		if err != nil {
			t.Fatalf("a rendered profile does not re-parse: %v\n%q", err, rendered)
		}
		if renderProfile(again) != rendered {
			t.Fatalf("the round trip is not stable:\nfirst  %q\nsecond %q", rendered, renderProfile(again))
		}
		if len(again.Blocks) != len(profile.Blocks) {
			t.Fatalf("the round trip changed the block count: %d then %d", len(profile.Blocks), len(again.Blocks))
		}
	})
}

func renderProfile(p coverage.Profile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mode: %s\n", p.Mode)
	for _, block := range p.Blocks {
		fmt.Fprintf(&b, "%s:%d.%d,%d.%d %d %d\n",
			block.File, block.StartLine, block.StartCol, block.EndLine, block.EndCol,
			block.NumStmt, block.Count)
	}
	return b.String()
}
