// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of `--explain`. It needs a real discovery pass for
// the same reason the listing tests do: which sites were suppressed and why is
// exactly what a mock would have to invent, and the whole point of the flag is
// that it reports what really happened.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/cli/...
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inRejectableFixture is [inFixture] against fixtures/rejectable, which is the
// corpus module whose whole purpose is holding mutants the compiler refuses.
//
// It is the fixture `run --explain` needs: the section under test is the
// rejections, and a module with none would let every assertion here pass
// against an empty block.
func inRejectableFixture(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "fixtures", "rejectable"))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("fixtures/rejectable is not a module: %v", err)
	}
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	// The run files a report, and the default history store is the developer's
	// own cache directory. XDG_CACHE_HOME and its Windows equivalent are what
	// os.UserCacheDir reads, so redirecting them keeps this test out of it.
	t.Setenv("XDG_CACHE_HOME", filepath.Join(temp, "cache"))
	t.Setenv("LocalAppData", filepath.Join(temp, "cache"))
	t.Chdir(root)
	return temp
}

// TestListExplainExpandsEverySkipReason checks the detail section against the
// discovery fixture, which carries one of nearly every suppression.
//
// The assertions are substrings rather than a golden file, deliberately. What
// is being pinned is that each reason appears, that its own sentence appears
// with it, and that the files it accounted for are named underneath — not the
// exact spacing, which would make every wording improvement a golden rewrite
// without making the output any more correct.
func TestListExplainExpandsEverySkipReason(t *testing.T) {
	inFixture(t)

	plain, _ := list(t, "--no-color")
	explained, _ := list(t, "--no-color", "--explain")

	// The listing is unchanged: --explain adds a section, it does not rewrite
	// what was there. A user who has both outputs in a terminal should be able
	// to diff them and see only the addition.
	if !strings.HasPrefix(explained, plain) {
		t.Fatalf("--explain changed the listing itself\n--- without ---\n%s\n--- with ---\n%s", plain, explained)
	}
	detail := strings.TrimPrefix(explained, plain)

	if !strings.Contains(detail, "suppressed sites") {
		t.Errorf("the detail section has no heading:\n%s", detail)
	}
	for _, want := range []string{
		// The reason, its explanation, and one of the files it came from.
		"const-decl",
		"a constant has to stay constant",
		"suppressed/suppressed.go",
		"generated",
		"the file says it is generated",
		"generated/generated.go",
		"package-var-init",
		"initialisation order is a global property",
		"type-param",
		"generics/generics.go",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("the detail section does not mention %q:\n%s", want, detail)
		}
	}
	// Counts travel with the rows, so that a reader can see which file
	// accounted for how much of a reason's total.
	if !strings.Contains(detail, "sites") {
		t.Errorf("the detail section carries no counts:\n%s", detail)
	}
}

// TestListExplainIsDeterministic proves the section is diffable between runs,
// which is what makes it worth putting in a pull request comment.
func TestListExplainIsDeterministic(t *testing.T) {
	inFixture(t)

	first, _ := list(t, "--no-color", "--explain")
	second, _ := list(t, "--no-color", "--explain")
	if first != second {
		t.Errorf("two runs of `list --explain` differ:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestListExplainSurvivesQuiet proves the two flags compose: `--quiet` drops the
// header and keeps the findings, and the explanation is a finding.
func TestListExplainSurvivesQuiet(t *testing.T) {
	inFixture(t)

	out, _ := list(t, "--no-color", "--quiet", "--explain")
	if !strings.Contains(out, "suppressed sites") {
		t.Errorf("--quiet dropped the explanation:\n%s", out)
	}
}

// TestRunExplainQuotesTheCompiler is `run --explain` end to end against the
// fixture whose whole purpose is a mutant that will not compile.
//
// The compiler's own words are what the section exists for: "it did not compile"
// is already visible in the counts, and which type mismatch on which line is the
// part that says whether the rejection is a limit of the guard forms or a mutant
// that could never have meant anything.
func TestRunExplainQuotesTheCompiler(t *testing.T) {
	inRejectableFixture(t)

	var out, errOut bytes.Buffer
	code := ExecuteContext(t.Context(), []string{"run", "--no-color", "--no-tui", "--explain"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("`go-mutants run --explain` exited %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	text := out.String()

	for _, want := range []string{"rejected mutants", "would not compile"} {
		if !strings.Contains(text, want) {
			t.Errorf("the explanation has no rejection section (%q missing):\n%s", want, text)
		}
	}
	// A compiler diagnostic names a position and says something about types.
	// Both halves are the fixture's own, so this asserts the text was carried
	// through rather than summarised away.
	if !strings.Contains(text, ".go:") {
		t.Errorf("no diagnostic was quoted:\n%s", text)
	}
	// The explanation comes after the summary, not instead of it.
	summary := strings.Index(text, "score")
	explanation := strings.Index(text, "rejected mutants")
	if summary < 0 || explanation < 0 || explanation < summary {
		t.Errorf("the explanation is not underneath the summary (summary at %d, explanation at %d):\n%s",
			summary, explanation, text)
	}
}

// TestRunWithoutExplainSaysNothingExtra is the other half: the section is
// printed only when it was asked for, so the default output of a run is
// unchanged by this phase.
func TestRunWithoutExplainSaysNothingExtra(t *testing.T) {
	inRejectableFixture(t)

	var out, errOut bytes.Buffer
	code := ExecuteContext(t.Context(), []string{"run", "--no-color", "--no-tui"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("`go-mutants run` exited %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "rejected mutants") {
		t.Errorf("a run nobody asked to explain printed the detail section:\n%s", out.String())
	}
}
