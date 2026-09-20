// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/report"
)

func TestWriteReportsEveryStepItCouldNotTake(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		stage       func(t *testing.T, root string) *report.Report
		code        report.Code
		says        string
		wantRunPath bool
	}{
		"a document that cannot be encoded": {
			stage: func(t *testing.T, _ string) *report.Report {
				t.Helper()
				return unencodableReport(t)
			},
			code: report.CodeEncodeFailed,
			says: "the run report could not be encoded as JSON",
		},
		"a file where the run directory has to go": {
			stage: func(t *testing.T, root string) *report.Report {
				t.Helper()
				r := buildFixture(t)
				dir := claimWorkspace(t, root, r.Workspace.WorkspaceDigest)
				writeFile(t, filepath.Join(dir, report.RunsDirName), "not a directory")
				return r
			},
			code: report.CodeHistoryDirectory,
			says: "could not be created",
		},
		"a directory where the run document has to go": {
			stage: func(t *testing.T, root string) *report.Report {
				t.Helper()
				r := buildFixture(t)
				dir := claimWorkspace(t, root, r.Workspace.WorkspaceDigest)
				makeDir(t, filepath.Join(dir, report.RunsDirName, r.RunID+".json"), "in the way")
				return r
			},
			code: report.CodeHistoryWrite,
			says: "the report could not be moved into place at ",
		},
		"a directory where the pointer has to go": {
			stage: func(t *testing.T, root string) *report.Report {
				t.Helper()
				r := buildFixture(t)
				dir := claimWorkspace(t, root, r.Workspace.WorkspaceDigest)
				makeDir(t, filepath.Join(dir, report.LatestFileName), "in the way")
				return r
			},
			code:        report.CodeHistoryWrite,
			says:        "the report could not be moved into place at ",
			wantRunPath: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			r := tc.stage(t, root)
			runPath, latestPath, err := report.History{Root: root}.Write(r)
			if got := report.CodeOf(err); got != tc.code {
				t.Fatalf("Write = %v (code %q), want %s", err, got, tc.code)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the failure does not say %q: %v", tc.says, err)
			}
			if latestPath != "" {
				t.Errorf("a pointer path came back from a write that failed: %q", latestPath)
			}
			switch {
			case tc.wantRunPath && runPath == "":
				t.Error("the run document was filed and its path was not reported, so nobody can find it")
			case tc.wantRunPath:
				if _, statErr := os.Stat(runPath); statErr != nil {
					t.Errorf("the reported run path is not on disk: %v", statErr)
				}
			case runPath != "":
				t.Errorf("a run path came back from a write that filed nothing: %q", runPath)
			}
		})
	}
}

func TestWriteFileIsTheStoresWriteWithoutTheStore(t *testing.T) {
	t.Parallel()

	t.Run("it writes the document", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "merged.json")
		r := buildFixture(t)
		if err := report.WriteFile(path, r); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		want := mustMarshalReport(t, r)
		if got := readFile(t, path); string(got) != string(want) {
			t.Error("WriteFile did not write the marshalled report")
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			t.Fatalf("listing the destination: %v", err)
		}
		if len(entries) != 1 {
			t.Errorf("the destination holds %d files, want 1", len(entries))
		}
	})

	t.Run("no report at all", func(t *testing.T) {
		t.Parallel()
		err := report.WriteFile(filepath.Join(t.TempDir(), "merged.json"), nil)
		if got := report.CodeOf(err); got != report.CodeNoReport {
			t.Fatalf("WriteFile(nil) = %v (code %q), want %s", err, got, report.CodeNoReport)
		}
		if !strings.Contains(err.Error(), "there is no report to write") {
			t.Errorf("the refusal does not say what is missing: %v", err)
		}
	})

	t.Run("a document that cannot be encoded", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "merged.json")
		err := report.WriteFile(path, unencodableReport(t))
		if got := report.CodeOf(err); got != report.CodeEncodeFailed {
			t.Fatalf("WriteFile = %v (code %q), want %s", err, got, report.CodeEncodeFailed)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("a file was created for a document that could not be encoded: %v", statErr)
		}
	})

	t.Run("a destination that is not there", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "never-created")
		err := report.WriteFile(filepath.Join(dir, "merged.json"), buildFixture(t))
		if got := report.CodeOf(err); got != report.CodeHistoryWrite {
			t.Fatalf("WriteFile = %v (code %q), want %s", err, got, report.CodeHistoryWrite)
		}
		if want := "a temporary file for the report could not be created in " + dir; !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q: %v", want, err)
		}
	})
}

func TestWriteTempReportsWhichStepTheFileRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		step report.TempFileStep
		says string
	}{
		"a write that failed": {report.TempWrite, "the report could not be written to "},
		"a flush that failed": {report.TempSync, "could not be flushed to disk"},
		"a close that failed": {report.TempClose, "could not be closed"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "merged.json")

			restore := report.FailTempFiles(tc.step, 0)
			err := report.WriteFile(path, buildFixture(t))
			restore()

			if got := report.CodeOf(err); got != report.CodeHistoryWrite {
				t.Fatalf("WriteFile = %v (code %q), want %s", err, got, report.CodeHistoryWrite)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the failure does not say %q: %v", tc.says, err)
			}
			if !errors.Is(err, report.ErrInjectedIO) {
				t.Error("the cause is not reachable through errors.Is")
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatalf("listing the destination: %v", readErr)
			}
			if len(entries) != 0 {
				t.Errorf("the failed write left %d files behind: %v", len(entries), entries)
			}
		})
	}
}

func TestClaimReportsWhatItCouldNotClaim(t *testing.T) {
	t.Parallel()

	t.Run("a digest that cannot name a directory", func(t *testing.T) {
		t.Parallel()
		for _, digest := range []string{"", "..", "../../etc", strings.ToUpper(fixtureDigest)} {
			_, err := report.History{Root: t.TempDir()}.Claim(digest)
			if got := report.CodeOf(err); got != report.CodeInvalidWorkspaceDigest {
				t.Fatalf("Claim(%q) = %v (code %q), want %s", digest, err, got, report.CodeInvalidWorkspaceDigest)
			}
			if want := "the workspace digest " + strconv.Quote(digest) + " cannot name a workspace directory"; !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not say %q: %v", want, err)
			}
		}
	})

	t.Run("a file where the workspace directory has to go", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(fixtureDigest))
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			t.Fatalf("staging: %v", err)
		}
		writeFile(t, dir, "not a directory")

		_, err := report.History{Root: root}.Claim(fixtureDigest)
		if got := report.CodeOf(err); got != report.CodeHistoryDirectory {
			t.Fatalf("Claim = %v (code %q), want %s", err, got, report.CodeHistoryDirectory)
		}
		if want := "the workspace directory " + dir + " could not be created"; !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	})

	t.Run("a marker that cannot be read", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(fixtureDigest))
		makeDir(t, filepath.Join(dir, report.MarkerFileName), "in the way")

		_, err := report.History{Root: root}.Claim(fixtureDigest)
		if got := report.CodeOf(err); got != report.CodeHistoryDirectory {
			t.Fatalf("Claim = %v (code %q), want %s", err, got, report.CodeHistoryDirectory)
		}
		if want := "the workspace marker " + filepath.Join(dir, report.MarkerFileName) + " could not be read"; !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	})
}

func TestClaimReportsAMarkerItCouldNotWrite(t *testing.T) {
	for name, tc := range map[string]struct {
		step report.TempFileStep
		says string
	}{
		"a write that failed": {report.TempWrite, "the workspace marker could not be written to "},
		"a flush that failed": {report.TempSync, "could not be flushed to disk"},
		"a close that failed": {report.TempClose, "could not be closed"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()

			restore := report.FailTempFiles(tc.step, 0)
			_, err := report.History{Root: root}.Claim(fixtureDigest)
			restore()

			if got := report.CodeOf(err); got != report.CodeHistoryWrite {
				t.Fatalf("Claim = %v (code %q), want %s", err, got, report.CodeHistoryWrite)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the failure does not say %q: %v", tc.says, err)
			}
			dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(fixtureDigest))
			if _, statErr := os.Stat(filepath.Join(dir, report.MarkerFileName)); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("a marker was left behind by a claim that failed: %v", statErr)
			}
		})
	}
}

func TestTheFallbackClaimWritesAndFlushesTheMarker(t *testing.T) {
	const content = "go-mutants-workspace-v1\n" + "cafe"

	t.Run("it creates the marker", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), report.MarkerFileName)
		if err := report.CreateMarkerInPlace(path, content); err != nil {
			t.Fatalf("CreateMarkerInPlace: %v", err)
		}
		if got := string(readFile(t, path)); got != content {
			t.Errorf("the marker holds %q, want %q", got, content)
		}
	})

	for name, step := range map[string]report.TempFileStep{
		"a write that failed": report.TempWrite,
		"a flush that failed": report.TempSync,
		"a close that failed": report.TempClose,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), report.MarkerFileName)

			restore := report.FailTempFiles(step, 0)
			err := report.CreateMarkerInPlace(path, content)
			restore()

			if got := report.CodeOf(err); got != report.CodeHistoryWrite {
				t.Fatalf("CreateMarkerInPlace = %v (code %q), want %s", err, got, report.CodeHistoryWrite)
			}
			if want := "the workspace marker " + path + " could not be written"; !strings.Contains(err.Error(), want) {
				t.Errorf("the failure does not say %q: %v", want, err)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("the marker's name survived a claim that never filled it: %v", statErr)
			}
		})
	}

	t.Run("a name that cannot be created", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "never-created", report.MarkerFileName)
		err := report.CreateMarkerInPlace(path, content)
		if got := report.CodeOf(err); got != report.CodeHistoryWrite {
			t.Fatalf("CreateMarkerInPlace = %v (code %q), want %s", err, got, report.CodeHistoryWrite)
		}
		if want := "the workspace marker " + path + " could not be created"; !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q: %v", want, err)
		}
	})
}

func TestReadMarkerRefusesWhatIsNotAMarker(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		content  string
		accepted bool
	}{
		"the marker this build writes":      {content: "go-mutants-workspace-v1\n" + fixtureDigest + "\n", accepted: true},
		"a marker with no trailing newline": {content: "go-mutants-workspace-v1\n" + fixtureDigest, accepted: true},
		"a marker with Windows line endings": {
			content: "go-mutants-workspace-v1\r\n" + fixtureDigest + "\r\n", accepted: true,
		},
		"a marker with more after it": {
			content: "go-mutants-workspace-v1\n" + fixtureDigest + "\nand a third line\n", accepted: true,
		},
		"nothing but the header":             {content: "go-mutants-workspace-v1"},
		"a header from the future":           {content: "go-mutants-workspace-v2\n" + fixtureDigest + "\n"},
		"a second line that is not a digest": {content: "go-mutants-workspace-v1\nnot-a-digest\n"},
		"an empty file":                      {content: ""},
		"somebody else's file":               {content: "# managed by another tool\n"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, report.MarkerFileName), tc.content)

			digest, err := report.ReadMarker(dir)
			if tc.accepted {
				if err != nil {
					t.Fatalf("ReadMarker: %v", err)
				}
				if digest != fixtureDigest {
					t.Errorf("ReadMarker = %q, want %q", digest, fixtureDigest)
				}
				return
			}
			if got := report.CodeOf(err); got != report.CodeForeignWorkspace {
				t.Fatalf("ReadMarker = %v (code %q), want %s", err, got, report.CodeForeignWorkspace)
			}
			if !strings.Contains(err.Error(), "is not one this build of go-mutants wrote") {
				t.Errorf("the refusal does not say whose marker it is not: %v", err)
			}
			if digest != "" {
				t.Errorf("a digest came back from a marker that was refused: %q", digest)
			}
		})
	}
}

func TestReadMarkerReportsAMarkerItCouldNotRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := filepath.Join(dir, report.MarkerFileName)
	makeDir(t, marker, "in the way")

	digest, err := report.ReadMarker(dir)
	if got := report.CodeOf(err); got != report.CodeHistoryDirectory {
		t.Fatalf("ReadMarker = %v (code %q), want %s", err, got, report.CodeHistoryDirectory)
	}
	if want := "the workspace marker " + marker + " could not be read"; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
	if digest != "" {
		t.Errorf("a digest came back beside the failure: %q", digest)
	}
	if errors.Is(err, report.ErrNoMarker) {
		t.Error("a marker that could not be read was reported as no marker at all")
	}
}

func TestTheDefaultStoreIsUnderTheCacheDirectory(t *testing.T) {
	withCacheDir(t, t.TempDir())
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir under the moved home: %v", err)
	}

	dir, err := report.History{}.WorkspaceDir(fixtureDigest)
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	want := filepath.Join(cache, report.DirName, report.WorkspacesDirName, report.WorkspaceKey(fixtureDigest))
	if dir != want {
		t.Errorf("WorkspaceDir = %q, want %q", dir, want)
	}
	if _, statErr := os.Stat(filepath.Join(cache, report.DirName)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("deriving the path created it: %v", statErr)
	}
}

func TestAMachineWithNoCacheDirectoryIsToldSo(t *testing.T) {
	withoutCacheDir(t)

	store := report.History{}
	if _, err := store.WorkspaceDir(fixtureDigest); report.CodeOf(err) != report.CodeCacheUnavailable {
		t.Errorf("WorkspaceDir = %v, want %s", err, report.CodeCacheUnavailable)
	}
	if _, err := store.Claim(fixtureDigest); report.CodeOf(err) != report.CodeCacheUnavailable {
		t.Errorf("Claim = %v, want %s", err, report.CodeCacheUnavailable)
	}
	if _, _, err := store.Write(buildFixture(t)); report.CodeOf(err) != report.CodeCacheUnavailable {
		t.Errorf("Write = %v, want %s", err, report.CodeCacheUnavailable)
	}
	if _, err := store.List(); report.CodeOf(err) != report.CodeCacheUnavailable {
		t.Errorf("List = %v, want %s", err, report.CodeCacheUnavailable)
	}
	if _, err := store.RemoveRuns(fixtureDigest); report.CodeOf(err) != report.CodeCacheUnavailable {
		t.Errorf("RemoveRuns = %v, want %s", err, report.CodeCacheUnavailable)
	}
	_, err := store.WorkspaceDir(fixtureDigest)
	if !strings.Contains(err.Error(), "the operating system's cache directory could not be determined") {
		t.Errorf("the failure does not say what could not be determined: %v", err)
	}
}

func TestArtifactsReportEveryStepTheyCouldNotTake(t *testing.T) {
	t.Parallel()

	both := []config.ReportFormat{config.FormatJSON, config.FormatHTML}
	for name, tc := range map[string]struct {
		stage func(t *testing.T, opts *report.ArtifactOptions)
		code  report.Code
		says  string
	}{
		"a threshold the published format refuses": {
			stage: func(t *testing.T, o *report.ArtifactOptions) {
				t.Helper()
				o.High = 101
			},
			code: report.CodeProjectionInvalid,
			says: "schema at /thresholds/high",
		},
		"a report directory that cannot be created": {
			stage: func(t *testing.T, o *report.ArtifactOptions) {
				t.Helper()
				writeFile(t, filepath.Join(o.WorkspaceRoot, "reports"), "not a directory")
			},
			code: report.CodeArtifactDirectory,
			says: "could not be created",
		},
		"a mutation.json that cannot be read": {
			stage: func(t *testing.T, o *report.ArtifactOptions) {
				t.Helper()
				dir := filepath.Join(o.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
				makeDir(t, filepath.Join(dir, report.ProjectionFileName), "in the way")
			},
			code: report.CodeArtifactWrite,
			says: "could not be read, so it could not be safely replaced",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := artifactOptions(t, both...)
			tc.stage(t, &opts)

			written, err := report.WriteArtifacts(opts)
			if got := report.CodeOf(err); got != tc.code {
				t.Fatalf("WriteArtifacts = %v (code %q), want %s", err, got, tc.code)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the failure does not say %q: %v", tc.says, err)
			}
			if written.Any() {
				t.Errorf("paths were reported for a publication that failed: %+v", written)
			}
		})
	}
}

func TestArtifactsReportAStagingFailure(t *testing.T) {
	opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
	dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))

	restore := report.FailTempFiles(report.TempCreate, 0)
	written, err := report.WriteArtifacts(opts)
	restore()

	if got := report.CodeOf(err); got != report.CodeArtifactWrite {
		t.Fatalf("WriteArtifacts = %v (code %q), want %s", err, got, report.CodeArtifactWrite)
	}
	if want := report.ProjectionFileName + " could not be staged in " + dir; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
	if !errors.Is(err, report.ErrInjectedIO) {
		t.Error("the cause is not reachable through errors.Is")
	}
	if written.Any() {
		t.Errorf("paths were reported for a publication that failed: %+v", written)
	}
	exists(t, filepath.Join(dir, report.ProjectionFileName), false)
	exists(t, filepath.Join(dir, report.HTMLFileName), false)
}

func TestAFailedRollbackIsTheMoreUrgentFact(t *testing.T) {
	opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
	dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("staging: %v", err)
	}
	const previous = `{"schemaVersion":"2","note":"last week's document"}`
	writeFile(t, filepath.Join(dir, report.ProjectionFileName), previous)

	restoreViewer := report.BreakVendoredViewer(errors.New("the embedded bundle hashes to something else"))
	restore := report.FailTempFiles(report.TempCreate, 1)
	written, err := report.WriteArtifacts(opts)
	restore()
	restoreViewer()

	if got := report.CodeOf(err); got != report.CodeArtifactRollback {
		t.Fatalf("WriteArtifacts = %v (code %q), want %s", err, got, report.CodeArtifactRollback)
	}
	if want := "could not be restored to the contents it had before this run, after " +
		report.HTMLFileName + " could not be written"; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
	if !errors.Is(err, report.ErrInjectedIO) {
		t.Error("the cause of the rollback's own failure is not reachable through errors.Is")
	}
	if written.Any() {
		t.Errorf("paths were reported for a publication that failed: %+v", written)
	}
}

func TestARollbackThatRemovesReportsWhatItCouldNotRemove(t *testing.T) {
	t.Parallel()

	opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
	dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("staging: %v", err)
	}
	makeDir(t, filepath.Join(dir, report.HTMLFileName), "in the way")

	written, err := report.WriteArtifacts(opts)
	if got := report.CodeOf(err); got != report.CodeArtifactWrite {
		t.Fatalf("WriteArtifacts = %v (code %q), want %s — the render's own failure, since the rollback worked",
			err, got, report.CodeArtifactWrite)
	}
	if !strings.Contains(err.Error(), "could not be moved into place at ") {
		t.Errorf("the failure is not the one about the page: %v", err)
	}
	if strings.Contains(err.Error(), "after "+report.HTMLFileName+" could not be written") {
		t.Errorf("a rollback that succeeded was reported as a failure: %v", err)
	}
	if written.Any() {
		t.Errorf("paths were reported for a publication that failed: %+v", written)
	}
	exists(t, filepath.Join(dir, report.ProjectionFileName), false)
}

func TestAPageOnItsOwnHasNothingToRollBack(t *testing.T) {
	t.Parallel()

	opts := artifactOptions(t, config.FormatHTML)
	dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("staging: %v", err)
	}
	makeDir(t, filepath.Join(dir, report.HTMLFileName), "in the way")

	written, err := report.WriteArtifacts(opts)
	if got := report.CodeOf(err); got != report.CodeArtifactWrite {
		t.Fatalf("WriteArtifacts = %v (code %q), want %s", err, got, report.CodeArtifactWrite)
	}
	if strings.Contains(err.Error(), "after ") {
		t.Errorf("a rollback was reported for a run that published nothing to roll back: %v", err)
	}
	if written.Any() {
		t.Errorf("paths were reported for a publication that failed: %+v", written)
	}
}

func unencodableReport(t *testing.T) *report.Report {
	t.Helper()
	r := buildFixture(t)
	notANumber := math.NaN()
	r.Summary.ScorePercent = &notANumber
	return r
}

func claimWorkspace(t *testing.T, root, digest string) string {
	t.Helper()
	dir, err := report.History{Root: root}.Claim(digest)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func makeDir(t *testing.T, path, inner string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("staging a directory at %s: %v", path, err)
	}
	writeFile(t, filepath.Join(path, inner), "so that it cannot be replaced")
}

func withCacheDir(t *testing.T, dir string) {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LocalAppData", dir)
	case "darwin":
		t.Setenv("HOME", dir)
		t.Setenv("XDG_CACHE_HOME", "")
	default:
		t.Setenv("XDG_CACHE_HOME", dir)
	}
}

func withoutCacheDir(t *testing.T) {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LocalAppData", "")
	case "darwin":
		t.Setenv("HOME", "")
	default:
		t.Setenv("XDG_CACHE_HOME", "")
		t.Setenv("HOME", "")
	}
}

func TestARollbackThatCannotRemoveTheDocumentSaysSo(t *testing.T) {
	tampered := errors.New("the embedded bundle hashes to something else")

	t.Run("a document that cannot be removed", func(t *testing.T) {
		opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
		dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
		path := filepath.Join(dir, report.ProjectionFileName)

		restore := report.UseVendoredViewerCheck(func() (string, error) {
			if err := os.Remove(path); err != nil {
				t.Errorf("removing the published document: %v", err)
			}
			makeDir(t, path, "in the way")
			return "", tampered
		})
		written, err := report.WriteArtifacts(opts)
		restore()

		if got := report.CodeOf(err); got != report.CodeArtifactRollback {
			t.Fatalf("WriteArtifacts = %v (code %q), want %s", err, got, report.CodeArtifactRollback)
		}
		if want := path + " was written and could not be removed again"; !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q: %v", want, err)
		}
		if !errors.Is(err, tampered) {
			t.Error("the render's own failure is not reachable through errors.Is")
		}
		if written.Any() {
			t.Errorf("paths were reported for a publication that failed: %+v", written)
		}
	})

	t.Run("a document that is already gone", func(t *testing.T) {
		opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
		dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
		path := filepath.Join(dir, report.ProjectionFileName)

		restore := report.UseVendoredViewerCheck(func() (string, error) {
			if err := os.Remove(path); err != nil {
				t.Errorf("removing the published document: %v", err)
			}
			return "", tampered
		})
		written, err := report.WriteArtifacts(opts)
		restore()

		if got := report.CodeOf(err); got != report.CodeVendoredAssetTampered {
			t.Fatalf("WriteArtifacts = %v (code %q), want the render's own failure %s",
				err, got, report.CodeVendoredAssetTampered)
		}
		if strings.Contains(err.Error(), "could not be removed again") {
			t.Errorf("a file that was already gone was reported as a rollback failure: %v", err)
		}
		if written.Any() {
			t.Errorf("paths were reported for a publication that failed: %+v", written)
		}
	})
}

func TestAClaimReadsBackTheMarkerThatWonTheRace(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(fixtureDigest))
	marker := filepath.Join(dir, report.MarkerFileName)

	restore := report.BeforeTempFile(0, func() { makeDir(t, marker, "in the way") })
	_, err := report.History{Root: root}.Claim(fixtureDigest)
	restore()

	if got := report.CodeOf(err); got != report.CodeHistoryDirectory {
		t.Fatalf("Claim = %v (code %q), want %s", err, got, report.CodeHistoryDirectory)
	}
	if want := "the workspace marker " + marker + " could not be read back"; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("the failure does not wrap the read's own error: %v", err)
	}
}

func TestAClaimRejectsTheForeignMarkerThatWonTheRace(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(fixtureDigest))
	marker := filepath.Join(dir, report.MarkerFileName)
	foreign := "go-mutants-workspace-v1\n" + strings.Repeat("cd", 32) + "\n"

	restore := report.BeforeTempFile(0, func() {
		if err := os.WriteFile(marker, []byte(foreign), 0o600); err != nil {
			t.Fatalf("letting the foreign marker win the race: %v", err)
		}
	})
	t.Cleanup(restore)
	_, err := report.History{Root: root}.Claim(fixtureDigest)

	if got := report.CodeOf(err); got != report.CodeForeignWorkspace {
		t.Fatalf("Claim = %v (code %q), want %s", err, got, report.CodeForeignWorkspace)
	}
	got, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatalf("reading the winning marker: %v", readErr)
	}
	if string(got) != foreign {
		t.Errorf("the claim rewrote the winning marker as %q, want %q", got, foreign)
	}
}

func TestAClaimFallsBackWhenTheTemporaryFileIsSweptAway(t *testing.T) {
	root := t.TempDir()

	restore := report.FailTempFiles(report.TempVanish, 0)
	dir, err := report.History{Root: root}.Claim(fixtureDigest)
	restore()

	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	marker := filepath.Join(dir, report.MarkerFileName)
	got := readFile(t, marker)
	if want := "go-mutants-workspace-v1\n" + fixtureDigest + "\n"; string(got) != want {
		t.Errorf("the marker holds %q, want %q", got, want)
	}
	if _, err = (report.History{Root: root}).Claim(fixtureDigest); err != nil {
		t.Errorf("a second claim of the same workspace = %v, want nil", err)
	}
}

func TestUndoChoosesWhichFailureToReport(t *testing.T) {
	t.Parallel()

	cause := &report.Error{Code: report.CodeVendoredAssetTampered, Message: "the page would not render"}
	plain := errors.New("the rollback failed in some other package's words")
	coded := &report.Error{Code: report.CodeArtifactRollback, Message: "mutation.json could not be restored"}

	t.Run("nothing to roll back", func(t *testing.T) {
		t.Parallel()
		if got := report.Undo(nil, cause); got != error(cause) {
			t.Errorf("Undo(nil rollback) = %v, want the cause itself", got)
		}
	})

	t.Run("a rollback that worked", func(t *testing.T) {
		t.Parallel()
		if got := report.Undo(func() error { return nil }, cause); got != error(cause) {
			t.Errorf("Undo(a rollback that worked) = %v, want the cause itself", got)
		}
	})

	t.Run("a rollback that failed in this package's words", func(t *testing.T) {
		t.Parallel()
		got := report.Undo(func() error { return coded }, cause)
		if code := report.CodeOf(got); code != report.CodeArtifactRollback {
			t.Fatalf("Undo = %v (code %q), want %s", got, code, report.CodeArtifactRollback)
		}
		want := "mutation.json could not be restored, after " + report.HTMLFileName + " could not be written"
		if !strings.Contains(got.Error(), want) {
			t.Errorf("Undo = %q, want it to say %q", got, want)
		}
		if !errors.Is(got, cause) {
			t.Error("the original failure is not reachable through errors.Is")
		}
	})

	t.Run("a rollback that failed in somebody else's", func(t *testing.T) {
		t.Parallel()
		got := report.Undo(func() error { return plain }, cause)
		if !errors.Is(got, plain) || !errors.Is(got, cause) {
			t.Errorf("Undo = %v, want both failures reachable through errors.Is", got)
		}
		if strings.Contains(got.Error(), "after ") {
			t.Errorf("a message was appended to an error that has none of its own: %v", got)
		}
	})
}
