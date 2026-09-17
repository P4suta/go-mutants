// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testsupport"
	vendorassets "github.com/P4suta/go-mutants/vendor-assets"
)

func artifactWorkspace(t *testing.T) string {
	t.Helper()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "fixtures", "killable"))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	testsupport.CacheDir(t)
	t.Setenv(console.GitHubSummaryEnv, "")

	root := filepath.Join(t.TempDir(), "killable")
	if err = os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating the workspace: %v", err)
	}
	if err = os.CopyFS(root, os.DirFS(fixture)); err != nil {
		t.Fatalf("copying the killable fixture: %v", err)
	}
	if err = os.RemoveAll(filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory))); err != nil {
		t.Fatalf("clearing the copied report directory: %v", err)
	}
	t.Chdir(root)
	return root
}

func artifactPaths(root string) (projection, page string) {
	dir := filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory))
	return filepath.Join(dir, report.ProjectionFileName), filepath.Join(dir, report.HTMLFileName)
}

func TestRunPublishesBothArtefacts(t *testing.T) {
	root := artifactWorkspace(t)

	code, stdout, stderr := execute(t, "run", "--report", "json,html", "--no-color", "--no-tui")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants run --report json,html` exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	projectionPath, pagePath := artifactPaths(root)

	for _, want := range []string{"report json: " + projectionPath, "report html: " + pagePath} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the run did not print %q\nstdout:\n%s", want, stdout)
		}
	}

	document, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatalf("reading the projection: %v", err)
	}
	if err = report.ValidateProjection(document); err != nil {
		t.Fatalf("the published %s does not satisfy the vendored schema: %v", report.ProjectionFileName, err)
	}

	var projection report.Projection
	if err = json.Unmarshal(document, &projection); err != nil {
		t.Fatalf("decoding the projection: %v", err)
	}
	if projection.SchemaVersion != "2" {
		t.Errorf("schemaVersion = %q, want \"2\"", projection.SchemaVersion)
	}
	if len(projection.Files) == 0 {
		t.Fatal("the projection describes no files at all")
	}
	killed := 0
	for path, file := range projection.Files {
		if file.Source == "" {
			t.Errorf("%s was published with no source", path)
		}
		onDisk, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if readErr != nil {
			t.Errorf("the projection names %s, which is not in the workspace: %v", path, readErr)
			continue
		}
		if file.Source != string(onDisk) {
			t.Errorf("the source published for %s is not the file in the workspace", path)
		}
		for _, m := range file.Mutants {
			if m.Status == report.StatusKilled {
				killed++
			}
		}
	}
	if killed == 0 {
		t.Error("the projection records no killed mutant, and the killable fixture has several")
	}
}

func TestPublishedPageIsSelfContained(t *testing.T) {
	root := artifactWorkspace(t)

	if code, stdout, stderr := execute(t, "run", "--report", "html", "--no-color", "--no-tui"); code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants run --report html` exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	projectionPath, pagePath := artifactPaths(root)
	if _, err := os.Stat(projectionPath); err == nil {
		t.Errorf("--report html wrote %s as well", report.ProjectionFileName)
	}
	data, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatalf("reading the page: %v", err)
	}
	page := string(data)

	if !strings.Contains(page, `<meta http-equiv="Content-Security-Policy" content="default-src 'none'`) {
		t.Error("the page carries no default-src 'none' policy")
	}
	if !strings.Contains(page, "'sha256-") {
		t.Error("the policy allows no script by hash, so either the scripts are unhashed or the policy is wrong")
	}
	ours := strings.Replace(page, string(vendorassets.Bundle()), "", 1)
	if len(ours) == len(page) {
		t.Fatal("the page does not contain the vendored bundle verbatim")
	}
	for _, forbidden := range []string{"src=", "href=", "<link", "@import"} {
		if strings.Contains(ours, forbidden) {
			t.Errorf("the page contains %q outside the vendored bundle", forbidden)
		}
	}
	for i, line := range strings.Split(ours, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "<!--") {
			continue
		}
		if strings.Contains(line, "http://") || strings.Contains(line, "https://") {
			t.Errorf("line %d holds a URL outside the vendored bundle and outside a comment", i+1)
		}
	}
	island := page[strings.Index(page, `type="application/json">`)+len(`type="application/json">`):]
	island = island[:strings.Index(island, "</script>")]
	if strings.Contains(island, "<") {
		t.Error("the JSON island contains a literal '<'")
	}
	var decoded report.Projection
	if err = json.Unmarshal([]byte(island), &decoded); err != nil {
		t.Fatalf("the island is not JSON: %v", err)
	}
	if len(decoded.Files) == 0 {
		t.Error("the island describes no files")
	}
}

func TestReportNoneWritesNothing(t *testing.T) {
	root := artifactWorkspace(t)

	code, stdout, stderr := execute(t, "run", "--report", "none", "--no-color", "--no-tui")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants run --report none` exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	dir := filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory))
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("--report none created %s", dir)
	}
	for _, unwanted := range []string{"report json:", "report html:"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("the run printed %q for a format it did not write", unwanted)
		}
	}
	if !strings.Contains(stdout, "report run: ") {
		t.Errorf("the run report was not published:\n%s", stdout)
	}
}

func TestJSONModeStillWritesTheArtefacts(t *testing.T) {
	root := artifactWorkspace(t)

	code, stdout, stderr := execute(t, "run", "--json", "--no-color", "--no-tui")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants run --json` exited %d\nstderr:\n%s", code, stderr)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("standard output is not one JSON document: %v\n%s", err, stdout)
	}
	if document["document_type"] != report.DocumentType {
		t.Errorf("standard output holds a %v", document["document_type"])
	}
	projectionPath, pagePath := artifactPaths(root)
	for _, path := range []string{projectionPath, pagePath} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("--json suppressed %s: %v", path, err)
		}
	}
	if !strings.Contains(stderr, "report html: "+pagePath) {
		t.Errorf("the artefact paths did not reach standard error:\n%s", stderr)
	}
}

func TestGitHubOutputInsideAJob(t *testing.T) {
	artifactWorkspace(t)
	summary := filepath.Join(t.TempDir(), "step-summary.md")
	t.Setenv(console.GitHubSummaryEnv, summary)

	code, stdout, stderr := execute(t, "run", "--report", "none", "--no-color", "--no-tui")
	if code != int(mutation.ExitOK) {
		t.Fatalf("the run exited %d\nstderr:\n%s", code, stderr)
	}
	data, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("reading the step summary: %v", err)
	}
	for _, want := range []string{"## go-mutants", "| Mutants | Killed |", "**Score "} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the step summary does not contain %q:\n%s", want, data)
		}
	}
	if !strings.Contains(stdout, "::warning file=") {
		t.Errorf("no annotation reached standard output:\n%s", stdout)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if !strings.HasPrefix(line, "::warning ") {
			continue
		}
		if !strings.Contains(line, ",line=") || !strings.Contains(line, ",col=") {
			t.Errorf("an annotation is missing its coordinates: %s", line)
		}
		if !strings.Contains(line, "survived (") {
			t.Errorf("an annotation does not say what happened: %s", line)
		}
	}
}

func TestGitHubOutputIsSuppressedUnderJSON(t *testing.T) {
	artifactWorkspace(t)
	summary := filepath.Join(t.TempDir(), "step-summary.md")
	t.Setenv(console.GitHubSummaryEnv, summary)

	code, stdout, stderr := execute(t, "run", "--json", "--report", "none", "--no-color", "--no-tui")
	if code != int(mutation.ExitOK) {
		t.Fatalf("the run exited %d\nstderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout, "::warning") {
		t.Errorf("an annotation was printed into the document:\n%s", stdout)
	}
	if _, err := os.Stat(summary); err == nil {
		t.Error("a step summary was written under --json")
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("standard output is not one JSON document: %v", err)
	}
}
