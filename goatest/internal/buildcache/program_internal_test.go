// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"io"
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

func TestMainRefusesTheFlagsItCannotParseWithoutGoingFurther(t *testing.T) {
	t.Parallel()
	var stderr strings.Builder
	code := Main([]string{"--no-such-flag"}, strings.NewReader(""), io.Discard, &stderr)
	if code != CacheProgramUsageExitCode {
		t.Fatalf("an unparsable invocation exited %d, want %d", code, CacheProgramUsageExitCode)
	}
	if strings.Contains(stderr.String(), "requires --scratch") {
		t.Errorf("an unparsable invocation went on to check its flags: %q", stderr.String())
	}
}

func TestMainRefusesACeilingBelowZero(t *testing.T) {
	t.Parallel()
	var stderr strings.Builder
	code := Main([]string{"--scratch", t.TempDir(), "--max-bytes", "-1"},
		strings.NewReader(""), io.Discard, &stderr)
	if code != CacheProgramUsageExitCode {
		t.Fatalf("a ceiling below zero exited %d, want %d", code, CacheProgramUsageExitCode)
	}
	if !strings.Contains(stderr.String(), "must not be negative") {
		t.Errorf("a ceiling below zero reported %q, want it named", stderr.String())
	}
}

func TestOpeningLayersLeavesTheBaseAloneUntilItIsNamedAndWrittenTo(t *testing.T) {
	t.Parallel()
	t.Run("no base at all", func(t *testing.T) {
		t.Parallel()
		layers, err := openLayers("", t.TempDir(), "", false, 0)
		if err != nil {
			t.Fatal(err)
		}
		if layers.Base.Dir != "" {
			t.Fatalf("opening without a base named %q, want no base layer", layers.Base.Dir)
		}
	})
	for _, test := range []struct {
		name    string
		persist bool
		created bool
	}{
		{name: "a base that is only read", persist: false},
		{name: "a base that is written to", persist: true, created: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			base := filepath.Join(t.TempDir(), "base")
			layers, err := openLayers(base, t.TempDir(), "", test.persist, 0)
			if err != nil {
				t.Fatal(err)
			}
			if layers.Base.Dir != base {
				t.Fatalf("opening named %q as its base, want %q", layers.Base.Dir, base)
			}
			_, statErr := os.Stat(base)
			if created := statErr == nil; created != test.created {
				t.Fatalf("%s was created=%t, want %t", test.name, created, test.created)
			}
		})
	}
}

func TestOpeningLayersReportsABaseItCannotCreate(t *testing.T) {
	t.Parallel()
	base := filepath.Join(t.TempDir(), "base")
	if err := os.WriteFile(base, nil, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if _, err := openLayers(base, t.TempDir(), "", true, 0); err == nil {
		t.Fatal("a base that is a file was opened")
	}
}

func TestResolvingANativeSourceIgnoresAnExclusionThatNamesNothing(t *testing.T) {
	t.Parallel()
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(working)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveNativeSource(working, ""); got != resolved {
		t.Fatalf("resolving against an exclusion that names nothing answered %q, want %q", got, resolved)
	}
}

func TestResolvingANativeSourceExcludesALayerReachedByAnotherName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	native := filepath.Join(root, "native")
	if err := os.MkdirAll(native, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(native, link); err != nil {
		t.Skipf("this platform does not make symbolic links: %v", err)
	}
	if got := resolveNativeSource(native, link); got != "" {
		t.Fatalf("resolving a source another name already covers answered %q, want none", got)
	}
	other := filepath.Join(root, "other")
	if got := resolveNativeSource(native, other); got == "" {
		t.Fatalf("resolving against an exclusion that is not there answered none, want the source")
	}
}
