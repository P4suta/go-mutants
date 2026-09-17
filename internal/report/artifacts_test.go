// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/report"
)

func artifactOptions(t *testing.T, formats ...config.ReportFormat) report.ArtifactOptions {
	t.Helper()
	return report.ArtifactOptions{
		Report:        projectionFixture(t),
		WorkspaceRoot: projectionWorkspace(t),
		Directory:     config.DefaultReportDirectory,
		Formats:       formats,
		High:          80,
		Low:           60,
	}
}

func TestWriteArtifactsWritesBoth(t *testing.T) {
	t.Parallel()

	opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
	written, err := report.WriteArtifacts(opts)
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}
	dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
	if want := filepath.Join(dir, report.ProjectionFileName); written.ProjectionPath != want {
		t.Errorf("the projection went to %s, want %s", written.ProjectionPath, want)
	}
	if want := filepath.Join(dir, report.HTMLFileName); written.HTMLPath != want {
		t.Errorf("the page went to %s, want %s", written.HTMLPath, want)
	}

	document := readFile(t, written.ProjectionPath)
	if err = report.ValidateProjection(document); err != nil {
		t.Errorf("the published document does not validate: %v", err)
	}
	page := readFile(t, written.HTMLPath)
	if !strings.Contains(string(page), "Content-Security-Policy") {
		t.Error("the published page carries no policy")
	}
	island := string(report.EscapeScriptData(document))
	if !strings.Contains(string(page), island) {
		t.Error("the page's island is not the document that was written beside it")
	}
}

func TestWriteArtifactsHonoursEachFormat(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		formats  []config.ReportFormat
		wantJSON bool
		wantHTML bool
	}{
		"json only": {formats: []config.ReportFormat{config.FormatJSON}, wantJSON: true},
		"html only": {formats: []config.ReportFormat{config.FormatHTML}, wantHTML: true},
		"none":      {formats: []config.ReportFormat{}},
		"nil":       {formats: nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := artifactOptions(t, tc.formats...)
			written, err := report.WriteArtifacts(opts)
			if err != nil {
				t.Fatalf("WriteArtifacts: %v", err)
			}
			if (written.ProjectionPath != "") != tc.wantJSON {
				t.Errorf("ProjectionPath = %q, want a path: %v", written.ProjectionPath, tc.wantJSON)
			}
			if (written.HTMLPath != "") != tc.wantHTML {
				t.Errorf("HTMLPath = %q, want a path: %v", written.HTMLPath, tc.wantHTML)
			}
			if written.Any() != (tc.wantJSON || tc.wantHTML) {
				t.Errorf("Any() = %v", written.Any())
			}
			dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
			_, statErr := os.Stat(dir)
			if !tc.wantJSON && !tc.wantHTML && !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("a report directory was created for a run that writes nothing: %v", statErr)
			}
			exists(t, filepath.Join(dir, report.ProjectionFileName), tc.wantJSON)
			exists(t, filepath.Join(dir, report.HTMLFileName), tc.wantHTML)
		})
	}
}

func TestWriteArtifactsRollsBackTheJSONWhenTheHTMLFails(t *testing.T) {
	t.Parallel()

	opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
	dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
	if err := os.MkdirAll(filepath.Join(dir, report.HTMLFileName), 0o755); err != nil {
		t.Fatalf("staging the failure: %v", err)
	}
	const previous = `{"schemaVersion":"2","note":"last week's document"}`
	jsonPath := filepath.Join(dir, report.ProjectionFileName)
	if err := os.WriteFile(jsonPath, []byte(previous), 0o600); err != nil {
		t.Fatalf("writing the previous document: %v", err)
	}

	written, err := report.WriteArtifacts(opts)
	if err == nil {
		t.Fatal("WriteArtifacts succeeded with a directory in the way of the page")
	}
	if got := report.CodeOf(err); got != report.CodeArtifactWrite {
		t.Errorf("the failure is %v (code %q), want %s", err, got, report.CodeArtifactWrite)
	}
	if written.Any() {
		t.Errorf("paths were reported for a publication that failed: %+v", written)
	}
	if got := string(readFile(t, jsonPath)); got != previous {
		t.Errorf("the previous document was not restored:\n got %s\nwant %s", got, previous)
	}
}

func TestWriteArtifactsRemovesTheJSONWhenThereWasNoneBefore(t *testing.T) {
	t.Parallel()

	opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
	dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
	if err := os.MkdirAll(filepath.Join(dir, report.HTMLFileName), 0o755); err != nil {
		t.Fatalf("staging the failure: %v", err)
	}

	if _, err := report.WriteArtifacts(opts); err == nil {
		t.Fatal("WriteArtifacts succeeded with a directory in the way of the page")
	}
	exists(t, filepath.Join(dir, report.ProjectionFileName), false)
}

func TestWriteArtifactsLeavesNothingWhenTheProjectionFails(t *testing.T) {
	t.Parallel()

	opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
	if err := os.Remove(filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(alphaProjPath))); err != nil {
		t.Fatalf("removing the fixture: %v", err)
	}
	if _, err := report.WriteArtifacts(opts); report.CodeOf(err) != report.CodeProjectionSourceUnreadable {
		t.Fatalf("WriteArtifacts over a deleted file = %v, want %s", err, report.CodeProjectionSourceUnreadable)
	}
	dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the report directory was created before the document was known to be good: %v", err)
	}
}

func TestWriteArtifactsReplacesAPreviousPair(t *testing.T) {
	t.Parallel()

	opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
	first, err := report.WriteArtifacts(opts)
	if err != nil {
		t.Fatalf("the first publication: %v", err)
	}
	before := readFile(t, first.ProjectionPath)

	second, err := report.WriteArtifacts(opts)
	if err != nil {
		t.Fatalf("the second publication: %v", err)
	}
	if second.ProjectionPath != first.ProjectionPath {
		t.Errorf("the second run wrote to %s, want %s", second.ProjectionPath, first.ProjectionPath)
	}
	if got := readFile(t, second.ProjectionPath); string(got) != string(before) {
		t.Error("the republished document is not the same bytes")
	}
}

func TestWriteArtifactsAcceptsAnAbsoluteDirectory(t *testing.T) {
	t.Parallel()

	elsewhere := filepath.Join(t.TempDir(), "collected")
	opts := artifactOptions(t, config.FormatJSON)
	opts.Directory = elsewhere

	written, err := report.WriteArtifacts(opts)
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}
	if want := filepath.Join(elsewhere, report.ProjectionFileName); written.ProjectionPath != want {
		t.Errorf("the projection went to %s, want %s", written.ProjectionPath, want)
	}
}

func TestWriteArtifactsRefusesNoReport(t *testing.T) {
	t.Parallel()

	_, err := report.WriteArtifacts(report.ArtifactOptions{
		Formats:       []config.ReportFormat{config.FormatJSON},
		WorkspaceRoot: t.TempDir(),
	})
	if got := report.CodeOf(err); got != report.CodeNoReport {
		t.Errorf("WriteArtifacts(no report) = %v (code %q), want %s", err, got, report.CodeNoReport)
	}
	if _, err = report.WriteArtifacts(report.ArtifactOptions{WorkspaceRoot: t.TempDir()}); err != nil {
		t.Errorf("WriteArtifacts with no formats and no report = %v, want nil", err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

func exists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	switch {
	case err == nil && !want:
		t.Errorf("%s exists and should not", path)
	case errors.Is(err, fs.ErrNotExist) && want:
		t.Errorf("%s does not exist and should", path)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		t.Errorf("stat %s: %v", path, err)
	}
}
