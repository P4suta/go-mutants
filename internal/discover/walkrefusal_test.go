// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func corrupting(t *testing.T, src, token string) []byte {
	t.Helper()

	index := strings.Index(src, token)
	if index < 0 {
		t.Fatalf("the fixture does not hold %q:\n%s", token, src)
	}
	out := []byte(src)
	for i := range token {
		out[index+i] = '~'
	}
	return out
}

func TestEveryEmittingArmOfTheWalkCarriesItsRefusalOut(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name    string
		src     string
		corrupt string
	}{
		{
			name:    "a comparison",
			src:     "package pkg\n\nfunc probe(a, b int) {\n\tswitch a < b {\n\t}\n}\n",
			corrupt: "<",
		},
		{
			name:    "a connective",
			src:     "package pkg\n\nfunc probe(a, b bool) {\n\tswitch a && b {\n\t}\n}\n",
			corrupt: "&&",
		},
		{
			name:    "integer arithmetic",
			src:     "package pkg\n\nfunc probe(a, b int) {\n\tswitch a + b {\n\t}\n}\n",
			corrupt: "+",
		},
		{
			name:    "floating arithmetic",
			src:     "package pkg\n\nfunc probe(a, b float64) {\n\tswitch a + b {\n\t}\n}\n",
			corrupt: "+",
		},
		{
			name:    "a bitwise operator",
			src:     "package pkg\n\nfunc probe(a, b int) {\n\tswitch a & b {\n\t}\n}\n",
			corrupt: "&",
		},
		{
			name:    "a boolean literal",
			src:     "package pkg\n\nfunc probe() {\n\tswitch true {\n\t}\n}\n",
			corrupt: "true",
		},
		{
			name:    "an if condition",
			src:     "package pkg\n\nfunc probe(a, b int) {\n\tif a < b {\n\t\treturn\n\t}\n}\n",
			corrupt: "a < b",
		},
		{
			name:    "a loop condition",
			src:     "package pkg\n\nfunc probe(a, b int) {\n\tfor a < b {\n\t\tbreak\n\t}\n}\n",
			corrupt: "a < b",
		},
		{
			name:    "a compound assignment",
			src:     "package pkg\n\nfunc probe(n int) {\n\tn += 1\n\t_ = n\n}\n",
			corrupt: "+=",
		},
		{
			name:    "an increment",
			src:     "package pkg\n\nfunc probe(n int) {\n\tn++\n\t_ = n\n}\n",
			corrupt: "++",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			err := scanBytes(t, c.src, corrupting(t, c.src, c.corrupt))
			if err == nil {
				t.Fatalf("a scan over corrupted bytes succeeded:\n%s", c.src)
			}
			if code := CodeOf(err); code != CodeSpanMismatch {
				t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeSpanMismatch)
			}
		})
	}
}

func TestEveryStatementShapedCandidateRefusesAFileThatGotShorter(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		at   string
	}{
		{
			name: "an assignment",
			src:  "package pkg\n\nfunc probe(n int) {\n\tn = 1\n\t_ = n\n}\n",
			at:   "n = 1",
		},
		{
			name: "an increment",
			src:  "package pkg\n\nfunc probe(n int) {\n\tn++\n\t_ = n\n}\n",
			at:   "n++",
		},
		{
			name: "a call statement",
			src:  "package pkg\n\nfunc use() {}\n\nfunc probe() {\n\tuse()\n}\n",
			at:   "use()\n",
		},
		{
			name: "a labelled branch",
			src: "package pkg\n\nfunc probe() {\nouter:\n\tfor {\n\t\tswitch {\n" +
				"\t\tdefault:\n\t\t\tbreak outer\n\t\t}\n\t}\n}\n",
			at: "break outer",
		},
		{
			name: "a numeric result",
			src:  "package pkg\n\nfunc probe(n int) int {\n\treturn n + 1\n}\n",
			at:   "n + 1",
		},
		{
			name: "a boolean result",
			src:  "package pkg\n\nfunc probe(ok bool) bool {\n\treturn ok\n}\n",
			at:   "ok\n}",
		},
		{
			name: "a nillable result",
			src:  "package pkg\n\nfunc probe(xs []int) []int {\n\treturn xs\n}\n",
			at:   "xs\n}",
		},
		{
			name: "an error result",
			src:  "package pkg\n\nfunc probe(err error) error {\n\treturn err\n}\n",
			at:   "err\n}",
		},
		{
			name: "a string result",
			src:  "package pkg\n\nfunc probe(s string) string {\n\treturn s + \"x\"\n}\n",
			at:   "s + \"x\"",
		},
		{
			name: "a negation",
			src:  "package pkg\n\nfunc probe(a bool) {\n\tswitch !a {\n\t}\n}\n",
			at:   "!a",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			cut := strings.Index(c.src, c.at)
			if cut < 0 {
				t.Fatalf("the fixture does not hold %q:\n%s", c.at, c.src)
			}
			err := scanBytes(t, c.src, []byte(c.src[:cut+1]))
			if err == nil {
				t.Fatalf("a scan over a file that got shorter succeeded:\n%s", c.src)
			}
			if code := CodeOf(err); code != CodeSpanMismatch {
				t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeSpanMismatch)
			}
			if !strings.Contains(err.Error(), "past the end of the file") {
				t.Errorf("the refusal %q does not say the span left the file", err)
			}
		})
	}
}

func TestANodeThatReachesPastTheEndOfTheFileIsRefused(t *testing.T) {
	t.Parallel()

	const src = "package pkg\n\nfunc probe(n int) {\n\tn++\n\t_ = n\n}\n"
	cut := strings.Index(src, "n++") + 1
	err := scanBytes(t, src, []byte(src[:cut]))
	if err == nil {
		t.Fatal("a scan over a file that got shorter succeeded")
	}
	if code := CodeOf(err); code != CodeSpanMismatch {
		t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeSpanMismatch)
	}
	if !strings.Contains(err.Error(), "past the end of the file") {
		t.Errorf("the refusal %q does not say the span left the file", err)
	}
}

func TestARefusalInOneFileStopsThePackage(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := root + "/pkg/widest.go"
	if err := writeSource(t, path, walkFixture); err != nil {
		t.Fatalf("writing the source: %v", err)
	}
	if err := writeSource(t, path, string(corrupting(t, walkFixture, "<="))); err != nil {
		t.Fatalf("corrupting the source: %v", err)
	}

	d := newWalkDiscovery(t, root)
	loaded, pkg := loadedPackage(t, path, walkFixture)
	if err := CodeOf(d.pkg(loaded, pkg)); err != CodeSpanMismatch {
		t.Fatalf("walking the package answered %q, want %q", err, CodeSpanMismatch)
	}
	d = newWalkDiscovery(t, root)
	loaded.packages = []*packages.Package{pkg}
	if err := CodeOf(d.run(t.Context(), loaded)); err != CodeSpanMismatch {
		t.Fatalf("running the discovery answered %q, want %q", err, CodeSpanMismatch)
	}
}
