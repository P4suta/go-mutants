// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package devgates

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/P4suta/go-mutants/internal/testkit"
)

// gitleaksConfig is the file whose allowlist these tests hold to its claim.
const gitleaksConfig = ".gitleaks.toml"

// allowlistPath is the only shape an allowlisted path may take: a directory,
// optionally at any depth, and nothing else.
//
// The constraint is the gate rather than a tidiness rule. gitleaks takes an
// arbitrary regular expression there, and an arbitrary regular expression is
// exactly how an allowlist stops being auditable -- `.*key.*` would silence the
// finding this repository most wants to see, and would read like housekeeping.
// Holding the entries to a directory prefix is what lets [TestEveryGitleaksAllowlistPathIsAPathGitIgnores]
// turn each one back into a path and ask git about it.
var allowlistPath = regexp.MustCompile(`^\(\^\|/\)([A-Za-z0-9._/-]+)/$`)

// TestEveryGitleaksAllowlistPathIsAPathGitIgnores is the claim .gitleaks.toml
// makes about itself: every path it excuses is outside the repository.
//
// The scan reads the working directory rather than git's history, which is the
// right choice -- a secret is worth catching in the file somebody just wrote --
// and its consequence is that git's own view of what belongs is not applied.
// Excusing a path git would happily commit is the one way this file can hurt,
// and it would not look like a mistake in review.
//
// So each entry is turned back into a path and put to `git check-ignore`. The
// probe name matters: a bare directory that does not exist on a fresh clone is
// still matched by an ignore rule, and asking about a file inside it is the
// question the scanner will actually face.
func TestEveryGitleaksAllowlistPathIsAPathGitIgnores(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	git := testkit.GitBinary(t)
	paths := gitleaksAllowlistPaths(t, root)
	if len(paths) == 0 {
		t.Fatal("the allowlist is empty, and the reader of this file was told it was not")
	}

	for _, raw := range paths {
		dir := allowlistDir(t, raw)
		probe := filepath.Join(dir, "probe.jsonl")
		cmd := exec.Command(git, "check-ignore", "--quiet", "--no-index", probe)
		cmd.Dir = root
		if err := cmd.Run(); err != nil {
			t.Errorf("%s excuses %q, and git does not ignore %q: %v", gitleaksConfig, raw, probe, err)
		}
	}
}

// TestNoGitleaksAllowlistPathCoversAFileGitTracks is the same claim from the
// side that fails loudly instead of quietly.
//
// `git check-ignore` answering yes is about a rule; this is about the files that
// exist. A pattern can be ignored and still match something already committed --
// git tracks what it tracks whatever the ignore file later says -- and such a
// pattern would take a real file out of the scan while the test above stayed
// green. Every tracked path is therefore put to every entry, and an entry that
// matches one is the hole rather than a surprise.
func TestNoGitleaksAllowlistPathCoversAFileGitTracks(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	git := testkit.GitBinary(t)
	cmd := exec.Command(git, "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing the tracked files: %v", err)
	}
	tracked := strings.FieldsFunc(string(out), func(r rune) bool { return r == 0 })
	if len(tracked) == 0 {
		t.Fatal("git tracks nothing here, so this test proved nothing rather than passing")
	}

	for _, raw := range gitleaksAllowlistPaths(t, root) {
		pattern, err := regexp.Compile(raw)
		if err != nil {
			t.Errorf("%s holds %q, which is not a regular expression: %v", gitleaksConfig, raw, err)
			continue
		}
		for _, path := range tracked {
			if pattern.MatchString(path) {
				t.Errorf("%s excuses %q, which covers the tracked file %s", gitleaksConfig, raw, path)
			}
		}
	}
}

// TestTheAllowlistReaderSeesAPathTheConfigDoesNotHold is the ledger on the
// ledger: a reader that returned nothing would make both tests above pass over
// a file that excused the whole repository.
//
// It is the counterpart every ledger here carries, and it is not ceremony. The
// two tests above are loops, and a loop over an empty list is the shape a green
// gate takes when it has stopped looking.
func TestTheAllowlistReaderSeesAPathTheConfigDoesNotHold(t *testing.T) {
	t.Parallel()

	const written = `
[extend]
useDefault = true

[[allowlists]]
description = "a directory this repository does not have"
paths = [
  '''(^|/)no-such-directory/''',
]
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, gitleaksConfig), []byte(written), 0o600); err != nil {
		t.Fatalf("writing the sample config: %v", err)
	}
	got := gitleaksAllowlistPaths(t, dir)
	if len(got) != 1 || got[0] != `(^|/)no-such-directory/` {
		t.Fatalf("the reader found %q in a config holding one path", got)
	}
	if dir := allowlistDir(t, got[0]); dir != "no-such-directory" {
		t.Errorf("the path %q names the directory %q", got[0], dir)
	}
}

// gitleaksAllowlistPaths reads every path pattern the config excuses.
//
// It decodes the TOML rather than reading the lines of the `paths` arrays. The
// first draft read lines, on the reasoning that a decoder would resolve the
// quoting and hand back something other than what a reviewer sees between the
// quotes -- which is not true of TOML: a `”'` literal string is returned
// verbatim, escapes and all, which is exactly why the config writes the
// expressions that way. What line reading really depended on was the
// *formatting*, and `taplo fmt` collapsed the array onto one line the first
// time it saw this file, which would have left the reader finding nothing and
// both gates above looping over an empty list in silence.
func gitleaksAllowlistPaths(t testing.TB, root string) []string {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(root, gitleaksConfig))
	if err != nil {
		t.Fatalf("reading %s: %v", gitleaksConfig, err)
	}
	var config struct {
		Allowlists []struct {
			Paths []string `toml:"paths"`
		} `toml:"allowlists"`
	}
	if err := toml.Unmarshal(text, &config); err != nil {
		t.Fatalf("decoding %s: %v", gitleaksConfig, err)
	}
	var paths []string
	for _, list := range config.Allowlists {
		paths = append(paths, list.Paths...)
	}
	return paths
}

// allowlistDir turns one allowlisted pattern back into the directory it names,
// and fails the test rather than guessing at a pattern of another shape.
func allowlistDir(t testing.TB, raw string) string {
	t.Helper()
	m := allowlistPath.FindStringSubmatch(raw)
	if m == nil {
		t.Fatalf("%s holds %q, and an allowlisted path is a directory written as (^|/)dir/", gitleaksConfig, raw)
	}
	return m[1]
}
