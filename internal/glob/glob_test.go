// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package glob_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/glob"
)

type matchCase struct {
	name    string
	pattern string
	path    string
	want    bool
}

var matchCases = []matchCase{
	{"literal file", "a.go", "a.go", true},
	{"literal file mismatch", "a.go", "b.go", false},
	{"literal path", "internal/glob/glob.go", "internal/glob/glob.go", true},
	{"literal path mismatch in the middle", "internal/glob/glob.go", "internal/cli/glob.go", false},
	{"literal is not a prefix match", "internal", "internal/glob", false},
	{"literal is not a suffix match", "glob", "internal/glob", false},

	{"upper pattern does not match lower path", "A.go", "a.go", false},
	{"lower pattern does not match upper path", "a.go", "A.go", false},
	{"case sensitive directory", "Internal/glob.go", "internal/glob.go", false},

	{"star matches a run", "*.go", "a.go", true},
	{"star matches a long run", "*.go", "deeply_named_file.go", true},
	{"star matches an empty run", "*.go", ".go", true},
	{"star does not cross a separator", "*.go", "a/b.go", false},
	{"star matches a whole element", "*", "a", true},
	{"star does not match two elements", "*", "a/b", false},
	{"trailing star matches an empty run", "a*", "a", true},
	{"star on both sides", "*a*", "bab", true},
	{"star on both sides mismatch", "*a*", "bbb", false},
	{"two stars with literals between", "a*b*c", "aXXbYYc", true},
	{"two stars matching empty runs", "a*b*c", "abc", true},
	{"stars cannot invent a missing byte", "a*b*c", "abd", false},
	{"backtracking shape stays correct", "a*a*a*a*b", "aaaaaaaaaa", false},
	{"backtracking shape finds the match", "a*a*a*a*b", "aaaaaaaaab", true},
	{"leading dot is not special for star", "*", ".hidden", true},
	{"leading dot is not special for double star", "**", ".git/config", true},
	{"star matches a literal asterisk in a name", "*", "*", true},
	{"no escape means a star cannot be demanded", "a*b", "a*b", true},

	{"question matches one byte", "?.go", "a.go", true},
	{"question does not match two bytes", "?.go", "ab.go", false},
	{"question does not match zero bytes", "?.go", ".go", false},
	{"question inside an element", "a?c", "abc", true},
	{"question does not match a separator", "a?c", "a/c", false},
	{"question and star together", "?*.go", "ab.go", true},
	{"question and star together at minimum length", "?*.go", "a.go", true},
	{"question is a byte not a rune", "?", "é", false},
	{"two questions cover a two-byte rune", "??", "é", true},
	{"literal multi-byte rune matches itself", "é.go", "é.go", true},

	{"double star matches one element", "**", "a", true},
	{"double star matches many elements", "**", "a/b/c", true},
	{"double star with suffix matches zero directories", "**/*.go", "a.go", true},
	{"double star with suffix matches one directory", "**/*.go", "x/a.go", true},
	{"double star with suffix matches many directories", "**/*.go", "x/y/a.go", true},
	{"double star with suffix still checks the suffix", "**/*.go", "x/y/a.txt", false},
	{"double star in the middle matches zero", "a/**/b", "a/b", true},
	{"double star in the middle matches one", "a/**/b", "a/x/b", true},
	{"double star in the middle matches many", "a/**/b", "a/x/y/b", true},
	{"double star in the middle still anchors the tail", "a/**/b", "a/x/c", false},
	{"double star in the middle still anchors the head", "a/**/b", "z/x/b", false},
	{"double star does not skip the tail", "a/**/b", "a/b/c", false},
	{"adjacent double stars behave as one", "**/**", "a", true},
	{"adjacent double stars over many elements", "**/**/**", "a/b/c", true},
	{"double star between anchors", "**/vendor/**", "x/vendor/y", true},
	{"double star between anchors with zero on both sides", "**/vendor/**", "vendor", true},
	{"real world test file pattern", "internal/**/*_test.go", "internal/glob/glob_test.go", true},
	{"real world test file pattern at zero depth", "internal/**/*_test.go", "internal/glob_test.go", true},
	{"real world test file pattern rejects a non-test file", "internal/**/*_test.go", "internal/glob/glob.go", false},

	{"trailing double star matches the directory itself", "vendor/**", "vendor", true},
	{"trailing double star matches a child", "vendor/**", "vendor/a.go", true},
	{"trailing double star matches a grandchild", "vendor/**", "vendor/x/a.go", true},
	{"trailing double star is anchored at the head", "vendor/**", "vendorx", false},
	{"trailing double star does not float", "vendor/**", "x/vendor/a.go", false},
	{"trailing double star does not match a prefix of the name", "vendor/**", "vend", false},

	{"too few pattern elements", "a/*", "a/b/c", false},
	{"too many pattern elements", "a/*", "a", false},
	{"single element pattern against a path", "*", "a/b", false},
	{"literal shorter than path", "a/b", "a", false},
	{"literal longer than path", "a", "a/b", false},
	{"exact element count matches", "a/*/c", "a/b/c", true},

	{"embedded double star behaves as one star", "a**b", "aXXb", true},
	{"embedded double star matches an empty run", "a**b", "ab", true},
	{"embedded double star does not cross a separator", "a**b", "a/b", false},
	{"leading double star inside an element", "**.go", "a.go", true},
	{"leading double star inside an element does not cross", "**.go", "x/a.go", false},
	{"three stars collapse", "***", "abc", true},
	{"three stars do not cross a separator", "***", "a/b", false},
	{"double star element beside an embedded one", "**/a**b", "x/y/aZb", true},

	{"backslash is literal", `a\b`, `a\b`, true},
	{"backslash is not a separator", `a\b`, "a/b", false},
	{"star does not stop at a backslash", "a*b", `a\b`, true},

	{"character class is not expanded", "[ab].go", "a.go", false},
	{"character class matches itself literally", "[ab].go", "[ab].go", true},
	{"brace expansion does not happen", "{a,b}.go", "a.go", false},
	{"brace matches itself literally", "{a,b}.go", "{a,b}.go", true},
	{"backslash does not escape a star", `a\*b`, "a*b", false},
	{"backslash leaves the star a wildcard", `a\*b`, `a\Xb`, true},

	{"empty path matches nothing", "**", "", false},
	{"empty path matches no star either", "*", "", false},
	{"leading separator in a path matches nothing", "**", "/a", false},
	{"trailing separator in a path matches nothing", "**", "a/", false},
	{"doubled separator in a path matches nothing", "**", "a//b", false},
	{"lone separator path matches nothing", "**", "/", false},
	{"trailing separator defeats an otherwise exact match", "vendor/**", "vendor/", false},
}

func TestPatternMatch(t *testing.T) {
	t.Parallel()
	for _, testCase := range matchCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			pattern, err := glob.Compile(testCase.pattern)
			if err != nil {
				t.Fatalf("Compile(%q) returned an unexpected error: %v", testCase.pattern, err)
			}
			if got := pattern.Match(testCase.path); got != testCase.want {
				t.Errorf("Compile(%q).Match(%q) = %t, want %t", testCase.pattern, testCase.path, got, testCase.want)
			}
		})
	}
}

func TestPatternMatchIsStable(t *testing.T) {
	t.Parallel()
	for _, testCase := range matchCases {
		pattern := glob.MustCompile(testCase.pattern)
		other := glob.MustCompile(testCase.pattern)
		for repeat := range 3 {
			if got := pattern.Match(testCase.path); got != testCase.want {
				t.Fatalf("%q.Match(%q) call %d = %t, want %t", testCase.pattern, testCase.path, repeat, got, testCase.want)
			}
		}
		if got := other.Match(testCase.path); got != testCase.want {
			t.Fatalf("second Compile(%q).Match(%q) = %t, want %t", testCase.pattern, testCase.path, got, testCase.want)
		}
	}
}

var compileErrorCases = []struct {
	name    string
	pattern string
	column  int
	message string
}{
	{"empty pattern", "", 1, "empty pattern"},
	{"lone separator", "/", 1, "leading '/'"},
	{"leading separator", "/a.go", 1, "leading '/'"},
	{"leading separator on a deep pattern", "/internal/glob", 1, "leading '/'"},
	{"doubled leading separator", "//a", 1, "leading '/'"},
	{"trailing separator", "a/", 2, "trailing '/'"},
	{"trailing separator on a deep pattern", "internal/glob/", 14, "trailing '/'"},
	{"trailing separator after a wildcard", "**/", 3, "trailing '/'"},
	{"empty element in the middle", "a//b", 3, "empty path element"},
	{"empty element deeper in", "internal//glob/x", 10, "empty path element"},
	{"two empty elements in the middle", "a///b", 3, "empty path element"},
}

func TestCompileRejects(t *testing.T) {
	t.Parallel()
	for _, testCase := range compileErrorCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			pattern, err := glob.Compile(testCase.pattern)
			if err == nil {
				t.Fatalf("Compile(%q) = %q, want an error", testCase.pattern, pattern.String())
			}
			if pattern.String() != "" {
				t.Errorf("Compile(%q) returned a non-zero Pattern %q alongside its error", testCase.pattern, pattern.String())
			}

			var syntaxErr *glob.SyntaxError
			if !errors.As(err, &syntaxErr) {
				t.Fatalf("Compile(%q) returned %T, want *glob.SyntaxError", testCase.pattern, err)
			}
			if syntaxErr.Pattern != testCase.pattern {
				t.Errorf("SyntaxError.Pattern = %q, want %q", syntaxErr.Pattern, testCase.pattern)
			}
			if syntaxErr.Column != testCase.column {
				t.Errorf("Compile(%q) blamed column %d, want %d", testCase.pattern, syntaxErr.Column, testCase.column)
			}
			if !strings.Contains(syntaxErr.Message, testCase.message) {
				t.Errorf("SyntaxError.Message = %q, want it to contain %q", syntaxErr.Message, testCase.message)
			}
			if text := syntaxErr.Error(); !strings.Contains(text, syntaxErr.Message) || !strings.Contains(text, testCase.pattern) {
				t.Errorf("SyntaxError.Error() = %q, want it to name both the pattern and the message", text)
			}
		})
	}
}

func TestCompileAcceptsUnusualButLegalPatterns(t *testing.T) {
	t.Parallel()
	legal := []string{
		"*",
		"**",
		"?",
		"a**b",
		"**.go",
		"***",
		"**/**",
		`a\b`,
		".",
		"..",
		"a b.go",
		"a*?*b",
	}
	for _, pattern := range legal {
		compiled, err := glob.Compile(pattern)
		if err != nil {
			t.Errorf("Compile(%q) returned an error: %v", pattern, err)
			continue
		}
		if compiled.String() != pattern {
			t.Errorf("Compile(%q).String() = %q, want the original text", pattern, compiled.String())
		}
	}
}

func TestZeroPatternMatchesNothing(t *testing.T) {
	t.Parallel()
	var zero glob.Pattern
	if zero.String() != "" {
		t.Errorf("zero Pattern.String() = %q, want the empty string", zero.String())
	}
	for _, path := range []string{"a", "a/b", "", "a.go", "vendor/x/y.go"} {
		if zero.Match(path) {
			t.Errorf("zero Pattern.Match(%q) = true, want false", path)
		}
	}
}

func TestMustCompilePanicsOnAnInvalidPattern(t *testing.T) {
	t.Parallel()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("MustCompile(\"a/\") returned normally, want a panic")
		}
		err, ok := recovered.(*glob.SyntaxError)
		if !ok {
			t.Fatalf("MustCompile panicked with %T, want *glob.SyntaxError", recovered)
		}
		if err.Pattern != "a/" {
			t.Errorf("panic value carried pattern %q, want %q", err.Pattern, "a/")
		}
	}()
	glob.MustCompile("a/")
}

func TestMatchStaysLinearOnPathologicalPatterns(t *testing.T) {
	t.Parallel()

	const depth = 200
	segments := make([]string, depth)
	for i := range segments {
		segments[i] = "b"
	}
	deepPath := strings.Join(segments, "/")

	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{
			name:    "nested double stars with no match",
			pattern: strings.Repeat("**/", depth) + "*a",
			path:    deepPath,
			want:    false,
		},
		{
			name:    "nested double stars with a match",
			pattern: strings.Repeat("**/", depth) + "*a",
			path:    deepPath + "/za",
			want:    true,
		},
		{
			name:    "star separated literals with no match",
			pattern: strings.Repeat("a*", 100) + "b",
			path:    strings.Repeat("a", 400),
			want:    false,
		},
		{
			name:    "star separated literals with a match",
			pattern: strings.Repeat("a*", 100) + "b",
			path:    strings.Repeat("a", 400) + "b",
			want:    true,
		},
		{
			name:    "double stars against a long path with a wildcard tail",
			pattern: strings.Repeat("**/", depth) + strings.Repeat("?", 1) + "*",
			path:    deepPath,
			want:    true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			pattern := glob.MustCompile(testCase.pattern)

			result := make(chan bool, 1)
			go func() { result <- pattern.Match(testCase.path) }()

			select {
			case got := <-result:
				if got != testCase.want {
					t.Fatalf("Match(%q) = %t, want %t", testCase.path, got, testCase.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("Match did not finish within 10s on a %d-byte pattern; the matcher is no longer linear",
					len(testCase.pattern))
			}
		})
	}
}

func BenchmarkMatch(b *testing.B) {
	benchmarks := []struct {
		name    string
		pattern string
		path    string
	}{
		{"literal", "internal/glob/glob.go", "internal/glob/glob.go"},
		{"wildcard element", "internal/glob/*.go", "internal/glob/glob.go"},
		{"double star", "**/*_test.go", "internal/glob/glob_test.go"},
		{"double star miss", "**/*_test.go", "internal/instrument/flatten.go"},
	}
	for _, benchmark := range benchmarks {
		pattern := glob.MustCompile(benchmark.pattern)
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = pattern.Match(benchmark.path)
			}
		})
	}
}
