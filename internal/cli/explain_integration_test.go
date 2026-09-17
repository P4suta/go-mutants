// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testsupport"
)

func inRejectableFixture(t *testing.T) string {
	t.Helper()
	root := testkit.Copy(t, "rejectable")
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	testsupport.CacheDir(t)
	t.Chdir(root)
	return temp
}

func TestListExplainExpandsEverySkipReason(t *testing.T) {
	inFixture(t)

	plain, _ := list(t, "--no-color")
	explained, _ := list(t, "--no-color", "--explain")

	if !strings.HasPrefix(explained, plain) {
		t.Fatalf("--explain changed the listing itself\n--- without ---\n%s\n--- with ---\n%s", plain, explained)
	}
	detail := strings.TrimPrefix(explained, plain)

	if !strings.Contains(detail, "suppressed sites") {
		t.Errorf("the detail section has no heading:\n%s", detail)
	}
	for _, want := range []string{
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
	if !strings.Contains(detail, "const-decl 4 sites") {
		t.Errorf("the detail section carries no per-reason count:\n%s", detail)
	}
}

func TestListExplainIsDeterministic(t *testing.T) {
	inFixture(t)

	first, _ := list(t, "--no-color", "--explain")
	second, _ := list(t, "--no-color", "--explain")
	if first != second {
		t.Errorf("two runs of `list --explain` differ:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestListExplainSurvivesQuiet(t *testing.T) {
	inFixture(t)

	out, _ := list(t, "--no-color", "--quiet", "--explain")
	if !strings.Contains(out, "suppressed sites") {
		t.Errorf("--quiet dropped the explanation:\n%s", out)
	}
}

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
	if !strings.Contains(text, ".go:") {
		t.Errorf("no diagnostic was quoted:\n%s", text)
	}
	summary := strings.Index(text, "score")
	explanation := strings.Index(text, "rejected mutants")
	if summary < 0 || explanation < 0 || explanation < summary {
		t.Errorf("the explanation is not underneath the summary (summary at %d, explanation at %d):\n%s",
			summary, explanation, text)
	}
}

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

func TestListExplainPrintsSkipCoordinates(t *testing.T) {
	inFixture(t)

	plain, _ := list(t, "--no-color")
	explained, _ := list(t, "--no-color", "--explain")
	if !strings.HasPrefix(explained, plain) {
		t.Fatalf("--explain changed the listing itself\n--- without ---\n%s\n--- with ---\n%s", plain, explained)
	}

	if detail := strings.TrimPrefix(explained, plain); detail != wantSkipDetail {
		t.Errorf("the skip detail is not the expected one\n--- got ---\n%s\n--- want ---\n%s", detail, wantSkipDetail)
	}
}
