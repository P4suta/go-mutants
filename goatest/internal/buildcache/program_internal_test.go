// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func TestResolvingANativeSourceRefusesEverythingThatIsNotADirectoryOfItsOwn(t *testing.T) {
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(resolved, "cache")
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(resolved, "file")
	if err := os.WriteFile(file, []byte("not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		source   string
		excluded []string
		want     string
	}{
		{name: "a directory of its own", source: directory, excluded: nil, want: directory},
		{name: "no source at all"},
		{name: "a source that is not there", source: filepath.Join(resolved, "absent")},
		{name: "a source that is a file", source: file},
		{
			name: "a source the run would write to", source: directory,
			excluded: []string{directory},
		},
		{
			name: "a source beside one the run would write to", source: directory,
			excluded: []string{filepath.Join(resolved, "other")}, want: directory,
		},
		{
			name: "an exclusion that names nothing", source: directory,
			excluded: []string{""}, want: directory,
		},
		{
			name: "an exclusion that is not there", source: directory,
			excluded: []string{filepath.Join(resolved, "absent")}, want: directory,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveNativeSource(test.source, test.excluded...); got != test.want {
				t.Fatalf("resolveNativeSource(%q, %q) = %q, want %q",
					test.source, test.excluded, got, test.want)
			}
		})
	}
}

func TestResolvingANativeSourceFollowsALinkToWhatItNames(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "cache")
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(directory, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	if got := resolveNativeSource(link); got != directory {
		t.Fatalf("resolveNativeSource(%q) = %q, want the directory it names", link, directory)
	}
	if got := resolveNativeSource(link, directory); got != "" {
		t.Fatalf("resolveNativeSource(%q) = %q, want none: the link names what the run writes to", link, got)
	}
}

func TestJoiningArgumentsQuotesOnlyWhatNeedsIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		arguments []string
		want      string
		refuses   bool
	}{
		{name: "no argument at all"},
		{name: "one plain word", arguments: []string{"go"}, want: "go"},
		{name: "two plain words", arguments: []string{"go", "test"}, want: "go test"},
		{name: "a word with a space", arguments: []string{"two words"}, want: "'two words'"},
		{name: "a word with a tab", arguments: []string{"two\twords"}, want: "'two\twords'"},
		{name: "a word with a newline", arguments: []string{"two\nwords"}, want: "'two\nwords'"},
		{name: "a word with a carriage return", arguments: []string{"two\rwords"}, want: "'two\rwords'"},
		{name: "a word with a double quote", arguments: []string{`say "this"`}, want: `'say "this"'`},
		{name: "a word with a single quote", arguments: []string{"it's"}, want: `"it's"`},
		{name: "a word with both quotes", arguments: []string{`it's "this"`}, refuses: true},
		{
			name:      "a word outside ASCII, which needs nothing",
			arguments: []string{"café"}, want: "café",
		},
		{
			name:      "a plain word beside one that needs quoting",
			arguments: []string{"go", "two words"}, want: "go 'two words'",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rendered, err := joinQuoted(test.arguments)
			if test.refuses {
				if err == nil || !strings.Contains(err.Error(), "both kinds of quote") {
					t.Fatalf("joinQuoted(%q) = (%q, %v), want it refused", test.arguments, rendered, err)
				}
				if rendered != "" {
					t.Errorf("a refused join answered with %q, want nothing", rendered)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if rendered != test.want {
				t.Fatalf("joinQuoted(%q) = %q, want %q", test.arguments, rendered, test.want)
			}
		})
	}
}

func TestABaseDirectoryIsWhereTheConfigurationSaysOrTheFallback(t *testing.T) {
	t.Parallel()
	absolute := filepath.Join(string(filepath.Separator), "elsewhere", "cache")
	for _, test := range []struct {
		name       string
		configured string
		want       string
	}{
		{name: "nothing configured", want: filepath.Join("fallback", "cache")},
		{name: "an absolute path", configured: absolute, want: absolute},
		{
			name: "a path relative to the repository", configured: "cache/build",
			want: filepath.Join("root", "cache", "build"),
		},
		{
			name: "a path that walks in place", configured: "./cache/./build",
			want: filepath.Join("root", "cache", "build"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := BaseDirectory("root", test.configured, filepath.Join("fallback", "cache"))
			if got != test.want {
				t.Fatalf("BaseDirectory = %q, want %q", got, test.want)
			}
		})
	}
}
