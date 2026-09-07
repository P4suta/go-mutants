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

// What happens when the filesystem says no.
//
// Every write in this package is a temporary file, a flush, and a rename, and
// every step of it carries a message written for somebody whose disk has just
// filled up. history_test.go and artifacts_test.go prove the happy paths and
// the two rollbacks; this file proves that each of those failures is reported
// rather than swallowed, and reported with the file's name in it.
//
// Two kinds of failure are staged here. The ones the operating system will
// produce on demand — a directory where a file has to go, a parent that is not
// a directory, a path under one — are staged for real, because a real ENOTDIR
// is worth more than an injected one. The three it will not produce on demand —
// a write, a flush and a close that fail on a file it has just created — go
// through [report.FailTempFiles]; see the seam's own comment for why it exists.
// Tests that use it cannot be parallel.

// TestWriteReportsEveryStepItCouldNotTake walks the four places
// [report.History.Write] can stop, in the order it reaches them.
//
// Each of them leaves the store in a different state, and the state is the
// point: the run document and the pointer are written in that order precisely
// so that a failure on the second leaves a history with a stale pointer rather
// than one with a missing run.
func TestWriteReportsEveryStepItCouldNotTake(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		// stage prepares the store and returns the report to write.
		stage func(t *testing.T, root string) *report.Report
		code  report.Code
		says  string
		// wantRunPath is whether the run document's path comes back beside the
		// failure, which is what says the run itself was filed.
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

// TestWriteFileIsTheStoresWriteWithoutTheStore covers [report.WriteFile], which
// is what `report merge --output` publishes with.
//
// It has the same three answers as the store's own write and none of its
// ownership machinery: no report at all is the caller's slip, a document that
// cannot be encoded is refused before anything is created, and a destination
// nothing can be staged in is reported with the directory in the message.
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
		// Nothing is left beside it: the staging file is renamed into place,
		// never copied.
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

// TestWriteTempReportsWhichStepTheFileRefused is the three failures the
// operating system will not produce on demand.
//
// They are three separate messages because they are three separate things to do
// about it: a write that failed is usually a full disk, a flush that failed is
// usually the device, and a close that failed is a write that had been buffered
// and is now lost. A report file that is correctly named and full of nothing is
// worse than no report file, which is why each of them is a failure rather than
// a shrug.
//
// It cannot be parallel; see [report.FailTempFiles].
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
			// Nothing is left behind under either name: a half-written file
			// that survives is the thing the temporary-file dance is for.
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

// TestClaimReportsWhatItCouldNotClaim covers the ownership claim's own
// failures, which come before anything at all is written into a directory
// go-mutants does not yet own.
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

// TestClaimReportsAMarkerItCouldNotWrite is the claim's other half: the
// directory is go-mutants' to take and the marker will not go into it.
//
// It cannot be parallel; see [report.FailTempFiles].
func TestClaimReportsAMarkerItCouldNotWrite(t *testing.T) {
	for name, tc := range map[string]struct {
		step report.TempFileStep
		says string
	}{
		// The staging file's own messages, not the marker's: a claim that
		// cannot stage its marker must not fall through to creating the name in
		// place, because the name would then be there with nothing behind it.
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
			// The directory keeps no marker, so the next run may still claim
			// it: a name with nothing behind it would refuse the directory to
			// everybody for ever.
			dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(fixtureDigest))
			if _, statErr := os.Stat(filepath.Join(dir, report.MarkerFileName)); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("a marker was left behind by a claim that failed: %v", statErr)
			}
		})
	}
}

// TestTheFallbackClaimWritesAndFlushesTheMarker is the other create path, on
// the filesystems that will not hard-link.
//
// The exclusive create is only half of what a claim has to do: the name has to
// have the contents behind it before anybody reads it, and the contents have to
// be on the device before the process that wrote them goes away. Both are
// checked here because the fallback is unreachable on the machines these tests
// run on — nothing local refuses a hard link — so the only way to know it works
// is to call it.
//
// It cannot be parallel; see [report.FailTempFiles].
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

// TestReadMarkerRefusesWhatIsNotAMarker walks the marker's shape check.
//
// The two-line file is the whole of the ownership proof, and every one of these
// is a directory in somebody's cache that go-mutants must not write to or
// delete. The truncated file is the one worth spelling out: a marker whose
// second line was never written is not a marker naming nothing, it is a file
// that must be left alone — and reading it has to survive there being no second
// line at all.
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

// TestReadMarkerReportsAMarkerItCouldNotRead is the one answer that is neither
// "this directory is ours" nor "this directory is not ours".
//
// A file called `go-mutants.marker` that this build cannot read means something
// is there that nobody should be deleting, and the walks that ask about every
// directory in the cache root have to be told that rather than shown an empty
// digest they would read as "no marker".
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

// TestTheDefaultStoreIsUnderTheCacheDirectory covers the one branch of the
// store's root that every real run takes and no other test does: the empty
// Root, which means the operating system's cache directory.
//
// Nothing is created — [report.History.WorkspaceDir] derives a path and does not
// touch the disk — so this asks only where the answer is, and where it is when
// the operating system will not say.
func TestTheDefaultStoreIsUnderTheCacheDirectory(t *testing.T) {
	cache := t.TempDir()
	withCacheDir(t, cache)

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

// TestAMachineWithNoCacheDirectoryIsToldSo is the other answer, and it has to
// travel through everything that asks for a root.
//
// A run on a machine whose cache directory cannot be determined is a run that
// cannot be kept in the history, and each of these has to say that rather than
// fall back to a path it made up — a store rooted at "" would file somebody's
// run history in whatever directory the process happened to be started in.
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

// TestArtifactsReportEveryStepTheyCouldNotTake walks the places
// [report.WriteArtifacts] can stop before it has published anything.
//
// The order is the contract, and each of these proves one step of it happened
// before the next: a threshold the format refuses costs nothing and creates no
// directory, a directory that cannot be made is reported rather than written
// around, and a `mutation.json` that cannot be read is never overwritten —
// because a file that cannot be read cannot be put back.
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

// TestArtifactsReportAStagingFailure is the write itself refusing, which the
// operating system will not do on demand.
//
// The message has to name the directory rather than only the file: a staging
// failure is almost always the directory — full, read-only, gone — and the file
// it was for is the part a reader already knows.
//
// It cannot be parallel; see [report.FailTempFiles].
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

// TestAFailedRollbackIsTheMoreUrgentFact is the house rule at its worst
// moment: the page would not write, and putting the document back failed too.
//
// What a caller gets then is the rollback's failure rather than the render's,
// because a half-published pair is what they have to act on and "the HTML could
// not be written" would not tell them that `mutation.json` is now this run's
// while `mutation.html` is last week's. The original failure stays reachable
// through errors.Is, so nothing is lost by choosing.
//
// It cannot be parallel; see [report.FailTempFiles].
func TestAFailedRollbackIsTheMoreUrgentFact(t *testing.T) {
	opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
	dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("staging: %v", err)
	}
	const previous = `{"schemaVersion":"2","note":"last week's document"}`
	writeFile(t, filepath.Join(dir, report.ProjectionFileName), previous)

	// The page never gets as far as being written: the viewer check refuses,
	// which is the failure that leaves the projection published on its own. The
	// first staging is this run's `mutation.json` and is allowed through; the
	// second is the rollback putting the old one back, and is refused.
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

// TestARollbackThatRemovesReportsWhatItCouldNotRemove is the other rollback,
// where there was no previous document and the one just written has to go.
//
// It cannot be parallel; see [report.FailTempFiles].
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

// TestAPageOnItsOwnHasNothingToRollBack is the third answer [report.WriteArtifacts]
// can give a failed render: when no document was published beside it, the
// render's own failure is the whole story.
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

// unencodableReport is a report the JSON encoder refuses, which is the only way
// to ask what a writer does when the document will not encode. See
// TestMarshalRefusesADocumentJSONCannotHold.
func unencodableReport(t *testing.T) *report.Report {
	t.Helper()
	r := buildFixture(t)
	notANumber := math.NaN()
	r.Summary.ScorePercent = &notANumber
	return r
}

// claimWorkspace creates one workspace directory with its marker and returns
// its path, so that a test can put something in the way of the next step.
func claimWorkspace(t *testing.T, root, digest string) string {
	t.Helper()
	dir, err := report.History{Root: root}.Claim(digest)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return dir
}

// writeFile writes one file, creating the directories above it.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// makeDir puts a non-empty directory where a file has to go, which is the
// failure every platform go-mutants targets produces for a read and for a
// rename onto it.
func makeDir(t *testing.T, path, inner string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("staging a directory at %s: %v", path, err)
	}
	writeFile(t, filepath.Join(path, inner), "so that it cannot be replaced")
}

// withCacheDir points os.UserCacheDir at dir for the duration of the test. A
// test that uses it cannot be parallel.
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

// withoutCacheDir makes os.UserCacheDir fail, which is the one way this
// package's root resolution can. A test that uses it cannot be parallel.
//
// macOS derives the cache directory from $HOME alone and appends
// "Library/Caches" to it, so an empty HOME is the only spelling of "there is
// none" there; elsewhere both the XDG variable and the home directory have to
// be empty before the answer is a failure rather than a default.
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

// TestARollbackThatCannotRemoveTheDocumentSaysSo is the removing half of the
// rollback failing.
//
// A first run that publishes `mutation.json` and then cannot render its page
// has to take the document away again — a lone `mutation.json` looks like a
// successful publication — and the one thing it must not do is report that it
// did when it did not. The two failures are told apart on purpose: a file that
// is already gone is a rollback that has nothing left to do, and anything else
// is a half-published pair somebody has to be told about.
//
// The moment between the two artefacts is not a moment a test can otherwise be
// at, so the viewer check is where the staging happens; see
// [report.UseVendoredViewerCheck].
func TestARollbackThatCannotRemoveTheDocumentSaysSo(t *testing.T) {
	tampered := errors.New("the embedded bundle hashes to something else")

	t.Run("a document that cannot be removed", func(t *testing.T) {
		opts := artifactOptions(t, config.FormatJSON, config.FormatHTML)
		dir := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(config.DefaultReportDirectory))
		path := filepath.Join(dir, report.ProjectionFileName)

		restore := report.UseVendoredViewerCheck(func() (string, error) {
			// The projection is published and the page is not. Something takes
			// the document's name for a directory it will not give up.
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
			// Somebody else's cleaner got there first. The rollback wanted the
			// file gone and it is gone, which is not a failure.
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

// TestAClaimReadsBackTheMarkerThatWonTheRace is the race the ownership marker
// exists to settle, staged at the one instant it happens.
//
// Between the read that finds no marker and the create that would make one,
// another process may claim the same directory. The claim does not go through a
// rename precisely so that exactly one racer creates the file — and the losers
// then have to read back what the winner left. A marker that appeared in that
// window and cannot be read is not a directory anybody may write to.
//
// It cannot be parallel; see [report.BeforeTempFile].
func TestAClaimReadsBackTheMarkerThatWonTheRace(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(fixtureDigest))
	marker := filepath.Join(dir, report.MarkerFileName)

	// The claim has read the directory and found no marker. This is the moment
	// before it creates one.
	restore := report.BeforeTempFile(0, func() { makeDir(t, marker, "in the way") })
	_, err := report.History{Root: root}.Claim(fixtureDigest)
	restore()

	if got := report.CodeOf(err); got != report.CodeHistoryDirectory {
		t.Fatalf("Claim = %v (code %q), want %s", err, got, report.CodeHistoryDirectory)
	}
	if want := "the workspace marker " + marker + " could not be read back"; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
}

// TestAClaimFallsBackWhenTheTemporaryFileIsSweptAway is the other half of
// [createMarker]: the hard link is not the only way to make the name.
//
// The temporary file is created under a deliberately recognisable pattern —
// "anything matching it is this package's leftovers and is safe to delete" —
// which is an invitation for somebody's cache cleaner to take it. When the link
// then has nothing to link, the claim has to create the marker in place rather
// than report a directory it could have claimed as unclaimable.
//
// It cannot be parallel; see [report.FailTempFiles].
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
	// And the claim is a claim: a second one over the same workspace agrees,
	// and one over another workspace is refused.
	if _, err = (report.History{Root: root}).Claim(fixtureDigest); err != nil {
		t.Errorf("a second claim of the same workspace = %v, want nil", err)
	}
}

// TestUndoChoosesWhichFailureToReport states the rule the artefact pair is
// published under, over all four shapes its rollback can have.
//
// The original failure is what a user acts on when the rollback worked, and the
// rollback's own failure is the more urgent fact when it did not — a
// half-published pair is worse than a page that would not render. Either way
// the other one stays reachable through errors.Is, which is what makes choosing
// safe. The last case is the one no caller can produce: every rollback this
// package hands over returns a coded error or nothing, and an uncoded one has
// no message to append to, so both are joined instead.
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
