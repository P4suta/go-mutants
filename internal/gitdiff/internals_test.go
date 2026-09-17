// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/testkit"
)

type step struct {
	needle string
	out    string
	err    error
}

type script struct {
	steps []step
	calls []string
}

func (s *script) run(_ context.Context, args ...string) (string, error) {
	line := strings.Join(args, " ")
	s.calls = append(s.calls, line)
	for _, st := range s.steps {
		if strings.Contains(line, st.needle) {
			return st.out, st.err
		}
	}
	return "", fmt.Errorf("the script has no answer for `git %s`", line)
}

func scripted(dir string, steps ...step) (git, *script) {
	s := &script{steps: steps}
	return git{dir: dir, run: s.run}, s
}

func unavailable() error {
	return &Error{
		Code:    CodeGitUnavailable,
		Message: "`git rev-parse` could not be run",
		Output:  "exec: \"git\": executable file not found in $PATH",
	}
}

func refused(output string) error {
	return &Error{
		Code:    CodeDiffFailed,
		Message: "`git rev-parse` exited with status 128",
		Output:  output,
	}
}

func TestGitUnavailableTravelsThroughEveryStepUnchanged(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		call func(g git) error
	}{{
		name: "the prefix that proves there is a repository",
		call: func(g git) error { _, err := g.prefix(t.Context()); return err },
	}, {
		name: "the upstream lookup",
		call: func(g git) error { _, err := g.resolveRef(t.Context(), ""); return err },
	}, {
		name: "the merge base",
		call: func(g git) error { _, err := g.mergeBase(t.Context(), "origin/main"); return err },
	}, {
		name: "the diff",
		call: func(g git) error { _, err := g.diff(t.Context(), "abc123"); return err },
	}, {
		name: "the untracked listing",
		call: func(g git) error { _, err := g.untracked(t.Context()); return err },
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g, _ := scripted(t.TempDir(), step{needle: "", err: unavailable()})
			err := test.call(g)
			if code := CodeOf(err); code != CodeGitUnavailable {
				t.Fatalf("code = %q, want %q (%v)", code, CodeGitUnavailable, err)
			}
			if !strings.Contains(err.Error(), "could not be run") {
				t.Errorf("the failure was rewritten on the way up: %v", err)
			}
		})
	}
}

func TestThePrefixIsTheProofThereIsARepository(t *testing.T) {
	t.Parallel()

	t.Run("a refusal is not a repository", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "--show-prefix", err: refused("fatal: not a git repository")})
		_, err := g.prefix(t.Context())
		if code := CodeOf(err); code != CodeNotARepository {
			t.Fatalf("code = %q, want %q (%v)", code, CodeNotARepository, err)
		}
		if got := OutputOf(err); got != "fatal: not a git repository" {
			t.Errorf("output = %q, want git's own words carried up", got)
		}
	})

	t.Run("a failure that is not this package's is still not a repository", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "--show-prefix", err: errors.New("something else entirely")})
		_, err := g.prefix(t.Context())
		if code := CodeOf(err); code != CodeNotARepository {
			t.Errorf("code = %q, want %q (%v)", code, CodeNotARepository, err)
		}
	})

	t.Run("the answer is trimmed", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "--show-prefix", out: "sub/dir/\n"})
		got, err := g.prefix(t.Context())
		if err != nil {
			t.Fatalf("prefix: %v", err)
		}
		if got != "sub/dir/" {
			t.Errorf("prefix = %q, want the newline gone", got)
		}
	})
}

func TestResolveRefRecordsTheNameRatherThanTheNotation(t *testing.T) {
	t.Parallel()

	t.Run("a named ref is not looked up", func(t *testing.T) {
		t.Parallel()

		g, s := scripted(t.TempDir())
		got, err := g.resolveRef(t.Context(), "origin/main")
		if err != nil || got != "origin/main" {
			t.Fatalf("resolveRef = %q, %v, want the ref passed through", got, err)
		}
		if len(s.calls) != 0 {
			t.Errorf("a named ref asked git %v", s.calls)
		}
	})

	t.Run("another upstream notation is a ref like any other", func(t *testing.T) {
		t.Parallel()

		g, s := scripted(t.TempDir())
		got, err := g.resolveRef(t.Context(), "@{u}")
		if err != nil || got != "@{u}" {
			t.Fatalf("resolveRef = %q, %v, want the ref passed through", got, err)
		}
		if len(s.calls) != 0 {
			t.Errorf("a named ref asked git %v", s.calls)
		}
	})

	for _, ref := range []string{"", "   ", UpstreamRef} {
		t.Run("silence spelled "+strconv.Quote(ref)+" is looked up", func(t *testing.T) {
			t.Parallel()

			g, s := scripted(t.TempDir(), step{needle: "@{upstream}", out: "origin/main\n"})
			got, err := g.resolveRef(t.Context(), ref)
			if err != nil {
				t.Fatalf("resolveRef: %v", err)
			}
			if got != "origin/main" {
				t.Errorf("resolveRef = %q, want the branch's own name", got)
			}
			if len(s.calls) != 1 {
				t.Errorf("asked git %v, want one lookup", s.calls)
			}
		})
	}

	t.Run("a refusal is a branch with no upstream", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(),
			step{needle: "@{upstream}", err: refused("fatal: no upstream configured")})
		_, err := g.resolveRef(t.Context(), "")
		if code := CodeOf(err); code != CodeNoUpstream {
			t.Fatalf("code = %q, want %q (%v)", code, CodeNoUpstream, err)
		}
		if !strings.Contains(err.Error(), "--changed=origin/main") {
			t.Errorf("the failure does not say what to do instead: %v", err)
		}
	})

	t.Run("an answer with nothing in it is a branch with no upstream", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "@{upstream}", out: "\n"})
		_, err := g.resolveRef(t.Context(), "")
		if code := CodeOf(err); code != CodeNoUpstream {
			t.Fatalf("code = %q, want %q (%v)", code, CodeNoUpstream, err)
		}
	})
}

func TestTheMergeBaseAsksWhetherThereAreAnyCommits(t *testing.T) {
	t.Parallel()

	t.Run("a base is trimmed and returned", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "merge-base", out: "0123456789abcdef\n"})
		got, err := g.mergeBase(t.Context(), "origin/main")
		if err != nil || got != "0123456789abcdef" {
			t.Fatalf("mergeBase = %q, %v", got, err)
		}
	})

	t.Run("an answer with nothing in it is an unknown ref", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "merge-base", out: " \n"})
		_, err := g.mergeBase(t.Context(), "origin/main")
		if code := CodeOf(err); code != CodeUnknownRef {
			t.Fatalf("code = %q, want %q (%v)", code, CodeUnknownRef, err)
		}
		if !strings.Contains(err.Error(), `"origin/main"`) {
			t.Errorf("the failure does not name the ref: %v", err)
		}
	})

	t.Run("no commits at all is said so", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(),
			step{needle: "merge-base", err: refused("fatal: no merge base")},
			step{needle: "--verify", err: refused("")},
		)
		_, err := g.mergeBase(t.Context(), "origin/main")
		if code := CodeOf(err); code != CodeUnknownRef {
			t.Fatalf("code = %q, want %q (%v)", code, CodeUnknownRef, err)
		}
		if !strings.Contains(err.Error(), "no commits yet") {
			t.Errorf("the failure blames the ref rather than the empty repository: %v", err)
		}
	})

	t.Run("a repository with commits blames the ref", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(),
			step{needle: "merge-base", err: refused("fatal: Not a valid object name")},
			step{needle: "--verify", out: "0123456789abcdef\n"},
		)
		_, err := g.mergeBase(t.Context(), "nope")
		if code := CodeOf(err); code != CodeUnknownRef {
			t.Fatalf("code = %q, want %q (%v)", code, CodeUnknownRef, err)
		}
		if !strings.Contains(err.Error(), "share no history") {
			t.Errorf("the failure does not offer the two causes: %v", err)
		}
		if got := OutputOf(err); got != "fatal: Not a valid object name" {
			t.Errorf("output = %q, want the merge-base's own words rather than the HEAD check's", got)
		}
	})
}

func TestTheDiffNamesTheBaseItFailedAgainst(t *testing.T) {
	t.Parallel()

	t.Run("the output is returned as it stands", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "diff", out: "diff --git a/x.go b/x.go\n"})
		got, err := g.diff(t.Context(), "abc")
		if err != nil || got != "diff --git a/x.go b/x.go\n" {
			t.Fatalf("diff = %q, %v", got, err)
		}
	})

	t.Run("a refusal names the base, abbreviated", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "diff", err: refused("fatal: bad object")})
		_, err := g.diff(t.Context(), "0123456789abcdef0123456789abcdef01234567")
		if code := CodeOf(err); code != CodeDiffFailed {
			t.Fatalf("code = %q, want %q (%v)", code, CodeDiffFailed, err)
		}
		if !strings.Contains(err.Error(), "0123456789ab") || strings.Contains(err.Error(), "0123456789abc") {
			t.Errorf("the failure does not name the base at twelve characters: %v", err)
		}
		if got := OutputOf(err); got != "fatal: bad object" {
			t.Errorf("output = %q, want git's own words", got)
		}
	})
}

func TestTheUntrackedListingIsSeparatedByNulAndNothingElse(t *testing.T) {
	t.Parallel()

	t.Run("the trailing separator is not a file", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "ls-files", out: "a.go\x00sub/b.go\x00"})
		got, err := g.untracked(t.Context())
		if err != nil {
			t.Fatalf("untracked: %v", err)
		}
		if want := []string{"a.go", "sub/b.go"}; !slices.Equal(got, want) {
			t.Errorf("untracked = %q, want %q", got, want)
		}
	})

	t.Run("nothing untracked is an empty list", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "ls-files", out: ""})
		got, err := g.untracked(t.Context())
		if err != nil || len(got) != 0 {
			t.Fatalf("untracked = %q, %v, want nothing", got, err)
		}
	})

	t.Run("a refusal is the untracked half failing, not the diff", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "ls-files", err: refused("fatal: this operation must be run in a work tree")})
		_, err := g.untracked(t.Context())
		if code := CodeOf(err); code != CodeUntrackedUnreadable {
			t.Fatalf("code = %q, want %q (%v)", code, CodeUntrackedUnreadable, err)
		}
		if got := OutputOf(err); got == "" {
			t.Error("the failure dropped git's own words")
		}
	})
}

func TestUntrackedFilesAreFoldedInAsWholeFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, filepath.Join(root, "three.go"), "a\nb\nc\n")
	write(t, filepath.Join(root, "empty.go"), "")
	write(t, filepath.Join(root, "unterminated.go"), "only")

	g, _ := scripted(root, step{
		needle: "ls-files",
		out:    "sub/three.go\x00sub/empty.go\x00sub/unterminated.go\x00elsewhere/other.go\x00sub/gone.go\x00",
	})
	files := map[string][]Range{"three.go": {{First: 1, Last: 1}}}
	if err := g.addUntracked(t.Context(), files, "sub/"); err != nil {
		t.Fatalf("addUntracked: %v", err)
	}
	want := map[string][]Range{
		"three.go":        {{First: 1, Last: 3}},
		"unterminated.go": {{First: 1, Last: 1}},
	}
	if !sameFiles(files, want) {
		t.Errorf("addUntracked left %v, want %v", files, want)
	}
}

func TestUntrackedFoldingStopsAtTheFirstThingItCannotAnswer(t *testing.T) {
	t.Parallel()

	t.Run("the listing itself", func(t *testing.T) {
		t.Parallel()

		g, _ := scripted(t.TempDir(), step{needle: "ls-files", err: refused("fatal: no work tree")})
		files := map[string][]Range{}
		err := g.addUntracked(t.Context(), files, "")
		if code := CodeOf(err); code != CodeUntrackedUnreadable {
			t.Fatalf("code = %q, want %q (%v)", code, CodeUntrackedUnreadable, err)
		}
	})

	t.Run("one of the files it named", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		inner := filepath.Join(root, "inner")
		if err := os.Mkdir(inner, 0o700); err != nil {
			t.Fatalf("making a directory: %v", err)
		}
		write(t, filepath.Join(inner, "a.go"), "package a\n")
		unopenableFile(t, filepath.Join(inner, "a.go"))

		g, _ := scripted(root, step{needle: "ls-files", out: "inner/a.go\x00"})
		files := map[string][]Range{}
		err := g.addUntracked(t.Context(), files, "")
		if code := CodeOf(err); code != CodeUntrackedUnreadable {
			t.Fatalf("code = %q, want %q (%v)", code, CodeUntrackedUnreadable, err)
		}
		if len(files) != 0 {
			t.Errorf("a failed fold left %v behind", files)
		}
	})
}

func TestResolveCarriesUpWhicheverStepFailed(t *testing.T) {
	t.Parallel()

	const goodDiff = "diff --git a/x.go b/x.go\n+++ b/x.go\n@@ -0,0 +1,2 @@\n+one\n+two\n"
	base := []step{
		{needle: "--show-prefix", out: "\n"},
		{needle: "merge-base", out: "abc123\n"},
		{needle: "diff", out: goodDiff},
		{needle: "ls-files", out: ""},
	}

	replace := func(needle string, with step) []step {
		out := slices.Clone(base)
		for i := range out {
			if out[i].needle == needle {
				with.needle = needle
				out[i] = with
			}
		}
		return out
	}

	for _, test := range []struct {
		name  string
		steps []step
		want  Code
	}{{
		name:  "the diff",
		steps: replace("diff", step{err: refused("fatal: bad object")}),
		want:  CodeDiffFailed,
	}, {
		name:  "the parse of what the diff printed",
		steps: replace("diff", step{out: "diff --git a/x.go b/x.go\n+++ b/x.go\n@@ nonsense @@\n"}),
		want:  CodeMalformedDiff,
	}, {
		name:  "the untracked listing",
		steps: replace("ls-files", step{err: refused("fatal: no work tree")}),
		want:  CodeUntrackedUnreadable,
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			g, _ := scripted(t.TempDir(), test.steps...)
			got, err := g.resolve(t.Context(), "origin/main")
			if code := CodeOf(err); code != test.want {
				t.Fatalf("code = %q, want %q (%v)", code, test.want, err)
			}
			if len(got.Files) != 0 || got.Ref != "" || got.Base != "" {
				t.Errorf("a failed resolve answered %+v as well", got)
			}
		})
	}

	t.Run("and the whole sequence when nothing fails", func(t *testing.T) {
		t.Parallel()

		g, s := scripted(t.TempDir(), base...)
		got, err := g.resolve(t.Context(), "origin/main")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.Ref != "origin/main" || got.Base != "abc123" {
			t.Errorf("Changed = %+v, want the ref and the base it recorded", got)
		}
		if want := (map[string][]Range{"x.go": {{First: 1, Last: 2}}}); !sameFiles(got.Files, want) {
			t.Errorf("Files = %v, want %v", got.Files, want)
		}
		if len(s.calls) != 4 {
			t.Errorf("asked git %v, want the four steps", s.calls)
		}
	})
}

func TestLineCountAnswersForWhatCannotBeCounted(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, filepath.Join(root, "three.go"), "a\nb\nc\n")
	write(t, filepath.Join(root, "unterminated.go"), "a\nb")
	write(t, filepath.Join(root, "empty.go"), "")
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatalf("making a directory: %v", err)
	}

	for _, test := range []struct {
		name string
		rel  string
		want int
	}{
		{name: "a file ending in a newline", rel: "three.go", want: 3},
		{name: "a last line nobody terminated", rel: "unterminated.go", want: 2},
		{name: "a file with nothing in it", rel: "empty.go", want: 0},
		{name: "a file that is not there", rel: "gone.go", want: 0},
		{name: "a file under a directory that is not there", rel: "nowhere/gone.go", want: 0},
		{name: "something that is not a regular file", rel: "dir", want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := lineCount(root, test.rel)
			if err != nil {
				t.Fatalf("lineCount(%q): %v", test.rel, err)
			}
			if got != test.want {
				t.Errorf("lineCount(%q) = %d, want %d", test.rel, got, test.want)
			}
		})
	}
}

func TestLineCountReportsAFileItCannotOpenOrStat(t *testing.T) {
	t.Parallel()

	t.Run("a file that cannot be opened", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		write(t, filepath.Join(root, "secret.go"), "package secret\n")
		unopenableFile(t, filepath.Join(root, "secret.go"))

		got, err := lineCount(root, "secret.go")
		if code := CodeOf(err); code != CodeUntrackedUnreadable {
			t.Fatalf("code = %q, want %q (%v)", code, CodeUntrackedUnreadable, err)
		}
		if got != 0 {
			t.Errorf("lineCount = %d beside an error, want 0", got)
		}
		if !strings.Contains(err.Error(), `"secret.go"`) {
			t.Errorf("the failure does not name the file: %v", err)
		}
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("the failure does not carry the refusal the open reported: %v", err)
		}
	})

	t.Run("a file that cannot be stat-ed", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		inner := filepath.Join(root, "inner")
		if err := os.Mkdir(inner, 0o700); err != nil {
			t.Fatalf("making a directory: %v", err)
		}
		write(t, filepath.Join(inner, "a.go"), "package a\n")
		unsearchableDir(t, inner)

		_, err := lineCount(root, "inner/a.go")
		if code := CodeOf(err); code != CodeUntrackedUnreadable {
			t.Fatalf("code = %q, want %q (%v)", code, CodeUntrackedUnreadable, err)
		}
	})

	t.Run("a read that stops half way", func(t *testing.T) {
		t.Parallel()

		want := errors.New("the disk went away")
		got, err := countLines("half.go", failingReader{err: want})
		if !errors.Is(err, want) {
			t.Fatalf("countLines = %v, want the read's own failure reachable", err)
		}
		if code := CodeOf(err); code != CodeUntrackedUnreadable {
			t.Errorf("code = %q, want %q", code, CodeUntrackedUnreadable)
		}
		if got != 0 {
			t.Errorf("countLines = %d beside an error, want 0", got)
		}
	})
}

func TestTheLineCounterCountsTheLastLineOnlyWhenNobodyTerminatedIt(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		chunks []string
		want   int
	}{
		{name: "nothing at all", want: 0},
		{name: "one empty write", chunks: []string{""}, want: 0},
		{name: "one terminated line", chunks: []string{"a\n"}, want: 1},
		{name: "one line nobody terminated", chunks: []string{"a"}, want: 1},
		{name: "a lone newline", chunks: []string{"\n"}, want: 1},
		{name: "three terminated lines", chunks: []string{"a\nb\nc\n"}, want: 3},
		{name: "three lines, the last unterminated", chunks: []string{"a\nb\nc"}, want: 3},
		{name: "a line split across two writes", chunks: []string{"a\nb", "c\n"}, want: 2},
		{name: "a newline alone in the second write", chunks: []string{"a\nb", "\n"}, want: 2},
		{name: "an empty write after a terminated one", chunks: []string{"a\n", ""}, want: 1},
		{name: "an empty write after an unterminated one", chunks: []string{"a", ""}, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var counter lineCounter
			for _, chunk := range test.chunks {
				n, err := counter.Write([]byte(chunk))
				if err != nil || n != len(chunk) {
					t.Fatalf("Write(%q) = %d, %v, want %d and no failure", chunk, n, err, len(chunk))
				}
			}
			if got := counter.total(); got != test.want {
				t.Errorf("total() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestTrimOutputKeepsTheEndOfWhatGitSaid(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, in, want string
	}{
		{name: "nothing", in: "", want: ""},
		{name: "only whitespace", in: "\n\n \t\r\n", want: ""},
		{name: "one line", in: "fatal: nope\n", want: "fatal: nope"},
		{name: "a carriage return at the end of a line", in: "a\r\nb\r\n", want: "a\nb"},
		{name: "trailing blank lines", in: "a\n\n\n", want: "a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := trimOutput(test.in); got != test.want {
				t.Errorf("trimOutput(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}

	t.Run("exactly the limit is kept whole", func(t *testing.T) {
		t.Parallel()

		lines := make([]string, outputLines)
		for i := range lines {
			lines[i] = "line " + strconv.Itoa(i)
		}
		got := trimOutput(strings.Join(lines, "\n") + "\n")
		if got != strings.Join(lines, "\n") {
			t.Errorf("trimOutput dropped a line at exactly the limit:\n%s", got)
		}
	})

	t.Run("one line past the limit keeps the end", func(t *testing.T) {
		t.Parallel()

		lines := make([]string, outputLines+1)
		for i := range lines {
			lines[i] = "line " + strconv.Itoa(i)
		}
		got := strings.Split(trimOutput(strings.Join(lines, "\n")+"\n"), "\n")
		if len(got) != outputLines {
			t.Fatalf("trimOutput kept %d lines, want %d", len(got), outputLines)
		}
		if got[0] != "line 1" || got[len(got)-1] != "line "+strconv.Itoa(outputLines) {
			t.Errorf("trimOutput kept the wrong end: %q..%q", got[0], got[len(got)-1])
		}
	})
}

func TestShortHashLeavesAloneWhatIsNotAHash(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ in, want string }{
		{"", ""},
		{"HEAD", "HEAD"},
		{"0123456789ab", "0123456789ab"},
		{"0123456789abc", "0123456789ab"},
		{"0123456789abcdef0123456789abcdef01234567", "0123456789ab"},
	} {
		if got := shortHash(test.in); got != test.want {
			t.Errorf("shortHash(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestOrFallsBackOnlyForAnEmptyValue(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ value, fallback, want string }{
		{"", "git", "git"},
		{"/usr/local/bin/git", "git", "/usr/local/bin/git"},
		{" ", "git", " "},
		{"", "", ""},
	} {
		if got := or(test.value, test.fallback); got != test.want {
			t.Errorf("or(%q, %q) = %q, want %q", test.value, test.fallback, got, test.want)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func unopenableFile(t *testing.T, path string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("a Windows file mode does not refuse a read the way this test needs")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make a read fail")
	}
	if err := os.Chmod(path, 0o200); err != nil {
		t.Fatalf("making %s unreadable: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if file, err := os.Open(path); err == nil {
		_ = file.Close()
		t.Skip("this filesystem does not enforce the file mode this test needs")
	}
}

func unsearchableDir(t *testing.T, dir string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("a Windows directory mode does not separate listing from stat the way this test needs")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make a stat fail")
	}
	if err := os.Chmod(dir, 0o600); err != nil {
		t.Fatalf("making %s unsearchable: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Skipf("this filesystem does not list an unsearchable directory: %v", err)
	}
	if _, err := entries[0].Info(); err == nil {
		t.Skip("this filesystem does not enforce the directory mode this test needs")
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

var _ io.Reader = failingReader{}

func TestAGitCommandSeesTheEnvironmentItWasGivenAndNoOther(t *testing.T) {
	t.Parallel()

	if _, set := os.LookupEnv("GIT_CONFIG_COUNT"); set {
		t.Skip("this process already carries GIT_CONFIG_COUNT, so the two environments are not distinguishable")
	}
	g := git{program: testkit.GitBinary(t), dir: t.TempDir(), timeout: 30 * time.Second}

	given := g
	given.env = []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=gomutants.marker",
		"GIT_CONFIG_VALUE_0=present",
	}
	out, err := given.command(t.Context(), "config", "--get", "gomutants.marker")
	if err != nil {
		t.Fatalf("`git config` under the given environment: %v", err)
	}
	if strings.TrimSpace(out) != "present" {
		t.Errorf("git read %q, want the value handed to it", strings.TrimSpace(out))
	}

	if _, err = g.command(t.Context(), "config", "--get", "gomutants.marker"); err == nil {
		t.Error("`git config` found a key nobody set")
	}
}
