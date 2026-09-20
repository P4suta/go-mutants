// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package repair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/provider"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const (
	secondCommitRename   = 2
	rollbackRenameCount  = 3
	applicationsInABatch = 2
)

func TestNormalizeCanonicalizesLocalPathsAndRejectsEveryEscapeForm(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path string
		want string
		ok   bool
	}{
		{path: "a_test.go", want: "a_test.go", ok: true},
		{path: "./pkg/../pkg/a_test.go", want: "pkg/a_test.go", ok: true},
		{path: "pkg//a_test.go", want: "pkg/a_test.go", ok: true},
		{path: ""},
		{path: "."},
		{path: "a/.."},
		{path: ".."},
		{path: "../a_test.go"},
		{path: "a/../.."},
		{path: "/a_test.go"},
		{path: `C:/a_test.go`},
		{path: `pkg\a_test.go`},
		{path: "pkg/\x00_test.go"},
	} {
		got, ok := normalize(test.path)
		if got != test.want || ok != test.ok {
			t.Errorf("normalize(%q) = (%q, %t), want (%q, %t)", test.path, got, ok, test.want, test.ok)
		}
	}
}

func TestConfinedPathHandlesMissingFinalPathsAndRejectsNonDirectories(t *testing.T) {
	t.Parallel()
	root := resolvedTempDir(t)
	want := filepath.Join(root, "new", "value_test.go")
	got, err := confinedPath(root, "new/value_test.go")
	if err != nil || got != want {
		t.Fatalf("confinedPath = (%q, %v), want %q", got, err, want)
	}
	final := filepath.Join(root, "existing_test.go")
	if err := os.WriteFile(final, []byte("existing"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if got, err := confinedPath(root, "existing_test.go"); err != nil || got != final {
		t.Fatalf("existing confinedPath = (%q, %v)", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "blocked"), []byte("file"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	_, err = confinedPath(root, "blocked/value_test.go")
	const wantError = `goatest: repair path "blocked/value_test.go" crosses non-directory "blocked"`
	if err == nil || err.Error() != wantError {
		t.Fatalf("confinedPath error = %v, want %q", err, wantError)
	}
	fileRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(fileRoot, []byte("file"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if _, err := confinedPath(fileRoot, "value_test.go"); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("file root error = %v", err)
	}
	if _, err := confinedPath(filepath.Join(t.TempDir(), "missing"), "value_test.go"); err == nil || !strings.HasPrefix(err.Error(), "goatest: resolve repair root: ") {
		t.Fatalf("missing root error = %v", err)
	}
}

func TestConfinedPathPropagatesEveryFilesystemFailure(t *testing.T) {
	for _, stage := range []string{"absolute", "evaluate", "stat", "lstat"} {
		t.Run(stage, func(t *testing.T) {
			preserveRepairHooks(t)
			root := t.TempDir()
			sentinel := errors.New(stage + " failed")
			switch stage {
			case "absolute":
				absoluteRepairPath = func(string) (string, error) { return "", sentinel }
			case "evaluate":
				evaluateRepairSymlinks = func(string) (string, error) { return "", sentinel }
			case "stat":
				statRepairPath = func(string) (os.FileInfo, error) { return nil, sentinel }
			case "lstat":
				lstatRepairPath = func(string) (os.FileInfo, error) { return nil, sentinel }
			}
			_, err := confinedPath(root, "value_test.go")
			if !errors.Is(err, sentinel) {
				t.Fatalf("confinedPath error = %v", err)
			}
		})
	}
}

func TestMatchesPreimageCoversMissingMatchingDirtyAndFaults(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing_test.go")
	for _, test := range []struct {
		expected string
		match    bool
	}{
		{expected: "", match: true},
		{expected: strings.Repeat("0", hex.EncodedLen(sha256.Size)), match: false},
	} {
		match, mode, err := matchesPreimage(missing, test.expected)
		if err != nil || match != test.match || mode != filemode.ReadableFile {
			t.Fatalf("matchesPreimage(missing, %q) = (%t, %o, %v)", test.expected, match, mode, err)
		}
	}
	path := filepath.Join(root, "existing_test.go")
	contents := []byte("package fixture\n")
	if err := os.WriteFile(path, contents, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	match, mode, err := matchesPreimage(path, sha256Hex(contents))
	if err != nil || !match || mode != info.Mode().Perm() {
		t.Fatalf("matching preimage = (%t, %o, %v), want mode %o", match, mode, err, info.Mode().Perm())
	}
	match, _, err = matchesPreimage(path, strings.Repeat("f", hex.EncodedLen(sha256.Size)))
	if err != nil || match {
		t.Fatalf("dirty preimage = (%t, %v)", match, err)
	}

	for _, stage := range []string{"read", "stat"} {
		t.Run(stage, func(t *testing.T) {
			preserveRepairHooks(t)
			sentinel := errors.New(stage + " failed")
			if stage == "read" {
				readRepairFile = func(string) ([]byte, error) { return nil, sentinel }
			} else {
				statRepairPath = func(string) (os.FileInfo, error) { return nil, sentinel }
			}
			_, _, err := matchesPreimage(path, sha256Hex(contents))
			if !errors.Is(err, sentinel) {
				t.Fatalf("matchesPreimage error = %v", err)
			}
		})
	}
}

func TestAtomicWritePropagatesEveryStageAndCleansTemporaryFile(t *testing.T) {
	for _, stage := range []string{"confine", "mkdir", "create", "write", "sync", "chmod", "close", "rename"} {
		t.Run(stage, func(t *testing.T) {
			preserveRepairHooks(t)
			root := t.TempDir()
			if stage == "confine" {
				root = filepath.Join(root, "missing")
			}
			sentinel := errors.New(stage + " failed")
			file := &fakeRepairFile{name: filepath.Join(root, "temporary")}
			removed := ""
			removeRepairFile = func(path string) error { removed = path; return nil }
			createRepairTemp = func(string, string) (repairWritableFile, error) {
				if stage == "create" {
					return nil, sentinel
				}
				return file, nil
			}
			if stage == "mkdir" {
				mkdirRepairAll = func(string, os.FileMode) error { return sentinel }
			}
			file.failure = stage
			file.err = sentinel
			renameRepairFile = func(string, string) error {
				if stage == "rename" {
					return sentinel
				}
				return nil
			}
			err := atomicWrite(root, "nested/value_test.go", []byte("contents"), filemode.GroupReadableFile)
			if !errors.Is(err, sentinel) && stage != "confine" {
				t.Fatalf("atomicWrite error = %v", err)
			}
			if stage == "confine" && err == nil {
				t.Fatal("atomicWrite accepted missing root")
			}
			if stage != "confine" && stage != "mkdir" && stage != "create" && removed != file.name {
				t.Fatalf("temporary removed = %q, want %q", removed, file.name)
			}
			if stage == "write" || stage == "sync" || stage == "chmod" {
				if file.closes != 1 {
					t.Fatalf("close calls = %d", file.closes)
				}
			}
			if stage == "rename" && (file.mode != filemode.GroupReadableFile || !slices.Equal(file.data, []byte("contents"))) {
				t.Fatalf("file mode=%o data=%q", file.mode, file.data)
			}
		})
	}
}

func TestArtifactMarshalAndApplyFailuresArePropagated(t *testing.T) {
	t.Run("marshal", func(t *testing.T) {
		preserveRepairHooks(t)
		sentinel := errors.New("marshal failed")
		marshalRepairArtifact = func(any, string, string) ([]byte, error) { return nil, sentinel }
		_, err := writeArtifact(t.TempDir(), report.Finding{ID: "finding-a"}, provider.Candidate{Kind: "patch", Path: "value_test.go"})
		if !errors.Is(err, sentinel) {
			t.Fatalf("writeArtifact error = %v", err)
		}
	})
	t.Run("artifact write", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".goatest"), []byte("blocked"), filemode.ReadableFile); err != nil {
			t.Fatal(err)
		}
		_, err := writeArtifact(root, report.Finding{ID: "finding-a"}, provider.Candidate{Kind: "patch", Path: "value_test.go"})
		if err == nil || !strings.Contains(err.Error(), "crosses non-directory") {
			t.Fatalf("writeArtifact error = %v", err)
		}
	})
	t.Run("apply", func(t *testing.T) {
		preserveRepairHooks(t)
		sentinel := errors.New("rename failed")
		renameRepairFile = func(string, string) error { return sentinel }
		result, err := ValidateAndApply(context.Background(), t.TempDir(), report.Finding{ID: "finding-a"}, provider.Candidate{
			Kind: "patch", Path: "value_test.go", Content: []byte("package fixture\n"),
		}, successfulValidator{})
		if result != (Result{}) || !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), "goatest: apply repair value_test.go: ") {
			t.Fatalf("ValidateAndApply = (%+v, %v)", result, err)
		}
	})
}

func TestApplyCandidatesCommitsAllFilesAndRejectsDuplicatePaths(t *testing.T) {
	root := t.TempDir()
	applications := []Application{
		{Finding: report.Finding{ID: "finding-a"}, Candidate: provider.Candidate{Kind: "patch", Path: "a_test.go", Content: []byte("package fixture\n")}},
		{Finding: report.Finding{ID: "finding-b"}, Candidate: provider.Candidate{Kind: "corpus", Path: "testdata/fuzz/FuzzValue/seed", Content: []byte("go test fuzz v1\nstring(\"seed\")\n")}},
	}
	results, err := ApplyCandidates(root, applications)
	if err != nil || len(results) != 2 || results[0].Status != StatusApplied || results[1].Status != StatusApplied {
		t.Fatalf("ApplyCandidates = (%+v, %v)", results, err)
	}
	for index, application := range applications {
		data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(application.Candidate.Path)))
		if readErr != nil || !slices.Equal(data, application.Candidate.Content) {
			t.Fatalf("applied file %d = %q, %v", index, data, readErr)
		}
	}
	_, err = ApplyCandidates(root, []Application{
		{Finding: report.Finding{ID: "finding-a"}, Candidate: provider.Candidate{Kind: "patch", Path: "same_test.go"}},
		{Finding: report.Finding{ID: "finding-b"}, Candidate: provider.Candidate{Kind: "patch", Path: "SAME_test.go"}},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate path") {
		t.Fatalf("duplicate batch error = %v", err)
	}
}

func TestApplyCandidatesPreimageMismatchAppliesNothing(t *testing.T) {
	root := t.TempDir()
	originalA, originalB := []byte("package fixture\n\nvar a = true\n"), []byte("package fixture\n\nvar userEdit = true\n")
	if err := os.WriteFile(filepath.Join(root, "a_test.go"), originalA, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b_test.go"), originalB, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	results, err := ApplyCandidates(root, []Application{
		{Finding: report.Finding{ID: "finding-a"}, Candidate: provider.Candidate{Kind: "patch", Path: "a_test.go", PreimageSHA256: sha256Hex(originalA), Content: []byte("new a")}},
		{Finding: report.Finding{ID: "finding-b"}, Candidate: provider.Candidate{Kind: "patch", Path: "b_test.go", PreimageSHA256: sha256Hex([]byte("stale b")), Content: []byte("new b")}},
	})
	if err != nil || results[0].Status != StatusCandidate || results[1].Status != StatusArtifact || results[1].Artifact == "" {
		t.Fatalf("ApplyCandidates = (%+v, %v)", results, err)
	}
	for path, want := range map[string][]byte{"a_test.go": originalA, "b_test.go": originalB} {
		got, readErr := os.ReadFile(filepath.Join(root, path))
		if readErr != nil || !slices.Equal(got, want) {
			t.Fatalf("%s changed = %q, %v", path, got, readErr)
		}
	}
}

func TestApplyCandidatesRollsBackEarlierWritesAfterLaterFailure(t *testing.T) {
	preserveRepairHooks(t)
	root := t.TempDir()
	originalA, originalB := []byte("package fixture\n\nvar a = true\n"), []byte("package fixture\n\nvar b = true\n")
	for path, content := range map[string][]byte{"a_test.go": originalA, "b_test.go": originalB} {
		if err := os.WriteFile(filepath.Join(root, path), content, filemode.ReadableFile); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := errors.New("second commit failed")
	originalRename := renameRepairFile
	renames := 0
	renameRepairFile = func(source, target string) error {
		renames++
		if renames == secondCommitRename {
			return sentinel
		}
		return originalRename(source, target)
	}
	results, err := ApplyCandidates(root, []Application{
		{Finding: report.Finding{ID: "finding-a"}, Candidate: provider.Candidate{Kind: "patch", Path: "a_test.go", PreimageSHA256: sha256Hex(originalA), Content: []byte("new a")}},
		{Finding: report.Finding{ID: "finding-b"}, Candidate: provider.Candidate{Kind: "patch", Path: "b_test.go", PreimageSHA256: sha256Hex(originalB), Content: []byte("new b")}},
	})
	if !errors.Is(err, sentinel) || results[0].Status != StatusCandidate || renames != rollbackRenameCount {
		t.Fatalf("ApplyCandidates = (%+v, %v), renames=%d", results, err, renames)
	}
	for path, want := range map[string][]byte{"a_test.go": originalA, "b_test.go": originalB} {
		got, readErr := os.ReadFile(filepath.Join(root, path))
		if readErr != nil || !slices.Equal(got, want) {
			t.Fatalf("%s after rollback = %q, %v", path, got, readErr)
		}
	}
}

func TestValidateAndApplyRechecksConfinementAfterValidation(t *testing.T) {
	root := t.TempDir()
	moved := root + "-moved"
	validator := callbackValidator{suite: func() error {
		if err := os.Rename(root, moved); err != nil {
			t.Fatalf("move repair root: %v", err)
		}
		return nil
	}}
	t.Cleanup(func() { _ = os.Rename(moved, root) })
	_, err := ValidateAndApply(context.Background(), root, report.Finding{ID: "finding-a"}, provider.Candidate{
		Kind: "patch", Path: "value_test.go", Content: []byte("package fixture\n"),
	}, validator)
	if err == nil || !strings.HasPrefix(err.Error(), "goatest: resolve repair root: ") {
		t.Fatalf("ValidateAndApply error = %v", err)
	}
}

func TestValidateAndApplyPropagatesPostValidationReadAndArtifactFailures(t *testing.T) {
	t.Run("preimage read", func(t *testing.T) {
		preserveRepairHooks(t)
		sentinel := errors.New("read failed")
		readRepairFile = func(string) ([]byte, error) { return nil, sentinel }
		result, err := ValidateAndApply(context.Background(), t.TempDir(), report.Finding{ID: "finding-a"}, provider.Candidate{
			Kind: "patch", Path: "value_test.go", Content: []byte("package fixture\n"),
		}, successfulValidator{})
		if result != (Result{}) || !errors.Is(err, sentinel) {
			t.Fatalf("ValidateAndApply = (%+v, %v)", result, err)
		}
	})
	t.Run("artifact write", func(t *testing.T) {
		preserveRepairHooks(t)
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "value_test.go"), []byte("user edit"), filemode.ReadableFile); err != nil {
			t.Fatal(err)
		}
		sentinel := errors.New("artifact rename failed")
		renameRepairFile = func(string, string) error { return sentinel }
		result, err := ValidateAndApply(context.Background(), root, report.Finding{ID: "finding-a"}, provider.Candidate{
			Kind: "patch", Path: "value_test.go", PreimageSHA256: sha256Hex([]byte("old")), Content: []byte("candidate"),
		}, successfulValidator{})
		if result != (Result{}) || !errors.Is(err, sentinel) {
			t.Fatalf("ValidateAndApply = (%+v, %v)", result, err)
		}
	})
}

type successfulValidator struct{}

func (successfulValidator) OriginalPasses(context.Context, provider.Candidate) error { return nil }
func (successfulValidator) Kills(context.Context, report.Finding, provider.Candidate) error {
	return nil
}
func (successfulValidator) Suite(context.Context, provider.Candidate) error { return nil }

type callbackValidator struct{ suite func() error }

func (callbackValidator) OriginalPasses(context.Context, provider.Candidate) error { return nil }
func (callbackValidator) Kills(context.Context, report.Finding, provider.Candidate) error {
	return nil
}
func (validator callbackValidator) Suite(context.Context, provider.Candidate) error {
	return validator.suite()
}

type fakeRepairFile struct {
	name    string
	failure string
	err     error
	data    []byte
	mode    os.FileMode
	closes  int
}

func (file *fakeRepairFile) Name() string { return file.name }
func (file *fakeRepairFile) Write(data []byte) (int, error) {
	if file.failure == "write" {
		return 0, file.err
	}
	file.data = slices.Clone(data)
	return len(data), nil
}
func (file *fakeRepairFile) Sync() error {
	if file.failure == "sync" {
		return file.err
	}
	return nil
}
func (file *fakeRepairFile) Chmod(mode os.FileMode) error {
	file.mode = mode
	if file.failure == "chmod" {
		return file.err
	}
	return nil
}
func (file *fakeRepairFile) Close() error {
	file.closes++
	if file.failure == "close" {
		return file.err
	}
	return nil
}

func preserveRepairHooks(t *testing.T) {
	t.Helper()
	abs, eval := absoluteRepairPath, evaluateRepairSymlinks
	stat, lstat, read := statRepairPath, lstatRepairPath, readRepairFile
	mkdir, create, remove := mkdirRepairAll, createRepairTemp, removeRepairFile
	rename, marshal := renameRepairFile, marshalRepairArtifact
	t.Cleanup(func() {
		absoluteRepairPath, evaluateRepairSymlinks = abs, eval
		statRepairPath, lstatRepairPath, readRepairFile = stat, lstat, read
		mkdirRepairAll, createRepairTemp, removeRepairFile = mkdir, create, remove
		renameRepairFile, marshalRepairArtifact = rename, marshal
	})
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestASafeCandidateIDIsSixteenLowercaseHexDigits(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		id   string
		want bool
	}{
		{name: "every digit and letter it admits", id: "0123456789abcdef", want: true},
		{name: "the lowest identity", id: "0000000000000000", want: true},
		{name: "the highest identity", id: "ffffffffffffffff", want: true},
		{name: "nothing at all"},
		{name: "one character short", id: "0123456789abcde"},
		{name: "one character long", id: "0123456789abcdef0"},
		{name: "the same digits in capitals", id: "0123456789ABCDEF"},
		{name: "a letter past f", id: "0123456789abcdeg"},
		{name: "the character below zero", id: "0123456789abcde/"},
		{name: "the character above nine", id: "0123456789abcde:"},
		{name: "the character below a", id: "0123456789abcde`"},
		{name: "a path separator", id: "0123456789abcde/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := safeCandidateID(test.id); got != test.want {
				t.Fatalf("safeCandidateID(%q) = %t, want %t", test.id, got, test.want)
			}
		})
	}
}

func TestApplyCandidatesAnswersAnEmptyBatchWithNoResults(t *testing.T) {
	results, err := ApplyCandidates(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if results == nil {
		t.Fatal("an empty batch answered with no slice rather than no results")
	}
	if len(results) != 0 {
		t.Fatalf("an empty batch produced %d results", len(results))
	}
}

func TestApplyCandidatesRefusesEveryPathNoRepairMayTouch(t *testing.T) {
	for _, path := range []string{"main.go", "internal/app/service.go", "../outside_test.go", ""} {
		t.Run("path "+path, func(t *testing.T) {
			root := resolvedTempDir(t)
			_, err := ApplyCandidates(root, []Application{{
				Candidate: provider.Candidate{Kind: "patch", Path: path, Content: []byte("package fixture\n")},
			}})
			if err == nil {
				t.Fatalf("ApplyCandidates wrote %q, which no repair may touch", path)
			}
			if !strings.Contains(err.Error(), "is invalid") {
				t.Errorf("the error is %q, want it to refuse the path", err)
			}
		})
	}
}

const candidateRecordCeiling = 8 << 20

func recordOfExactly(t *testing.T, size int) CandidateRecord {
	t.Helper()
	record := CandidateRecord{
		ID: "0123456789abcdef",
		Finding: report.Finding{
			ID: "finding-a", Kind: "surviving-mutant", Summary: "survived",
		},
		Candidate: provider.Candidate{
			Kind: "patch", Path: "generated_test.go", Content: []byte("package fixture\n"),
		},
	}
	base, err := json.MarshalIndent(candidateDocument{Version: candidateVersion, CandidateRecord: record}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	padding := size - len(base) - len("\n")
	if padding < 0 {
		t.Fatalf("a record of %d bytes cannot be shrunk to %d", len(base)+1, size)
	}
	record.Snapshot = strings.Repeat("x", padding)
	written, err := json.MarshalIndent(candidateDocument{Version: candidateVersion, CandidateRecord: record}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(written)+len("\n") != size {
		t.Fatalf("the padded record is %d bytes, want %d", len(written)+1, size)
	}
	return record
}

func TestTheCandidateStoreHoldsARecordOfExactlyItsCeiling(t *testing.T) {
	root := resolvedTempDir(t)
	record := recordOfExactly(t, candidateRecordCeiling)

	if _, err := StoreCandidate(root, record); err != nil {
		t.Fatalf("a record of exactly %d bytes was refused: %v", candidateRecordCeiling, err)
	}
	loaded, err := LoadCandidate(root, record.ID)
	if err != nil {
		t.Fatalf("a record of exactly %d bytes could not be read back: %v", candidateRecordCeiling, err)
	}
	if loaded.Snapshot != record.Snapshot {
		t.Fatal("the record read back is not the one that was stored")
	}
}

func TestTheCandidateStoreRefusesARecordOneByteOverItsCeiling(t *testing.T) {
	root := resolvedTempDir(t)
	record := recordOfExactly(t, candidateRecordCeiling+1)

	if _, err := StoreCandidate(root, record); err == nil {
		t.Fatalf("a record of %d bytes was stored", candidateRecordCeiling+1)
	} else if !strings.Contains(err.Error(), "exceeds 8 MiB") {
		t.Errorf("the error is %q, want it to name the ceiling it refused", err)
	}
}

func TestApplyCandidatesWritesAnArtifactForEveryPreimageThatDoesNotMatch(t *testing.T) {
	for _, test := range []struct {
		name     string
		existing []byte
		preimage func([]byte) string
	}{
		{
			name:     "a file that is not there and a preimage that says it should be",
			preimage: func([]byte) string { return strings.Repeat("0", hex.EncodedLen(sha256.Size)) },
		},
		{
			name:     "a file whose contents somebody else changed",
			existing: []byte("package fixture // edited\n"),
			preimage: func([]byte) string { return sha256Hex([]byte("package fixture\n")) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := resolvedTempDir(t)
			target := filepath.Join(root, "candidate_test.go")
			if test.existing != nil {
				if err := os.WriteFile(target, test.existing, filemode.PrivateFile); err != nil {
					t.Fatal(err)
				}
			}
			results, err := ApplyCandidates(root, []Application{{
				Finding: report.Finding{ID: "finding-a"},
				Candidate: provider.Candidate{
					Kind: "patch", Path: "candidate_test.go",
					PreimageSHA256: test.preimage(test.existing), Content: []byte("package repaired\n"),
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Status != StatusArtifact || results[0].Artifact == "" {
				t.Fatalf("ApplyCandidates answered %+v, want one artifact", results)
			}
			switch current, readErr := os.ReadFile(target); {
			case test.existing == nil && !errors.Is(readErr, os.ErrNotExist):
				t.Errorf("a refused repair created the file it was not allowed to write: %v", readErr)
			case test.existing != nil && string(current) != string(test.existing):
				t.Errorf("a refused repair rewrote the file as %q, want %q", current, test.existing)
			}
		})
	}
}

func TestApplyCandidatesWritesNothingWhenOneOfABatchDoesNotMatch(t *testing.T) {
	original := []byte("package fixture\n")
	for _, test := range []struct {
		name    string
		present []byte
	}{
		{name: "a file the batch expected to be there"},
		{name: "a file somebody else has edited since", present: []byte("package edited\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			preserveRepairHooks(t)
			root := resolvedTempDir(t)
			matching := filepath.Join(root, "matching_test.go")
			if err := os.WriteFile(matching, original, filemode.PrivateFile); err != nil {
				t.Fatal(err)
			}
			if test.present != nil {
				if err := os.WriteFile(filepath.Join(root, "mismatching_test.go"),
					test.present, filemode.PrivateFile); err != nil {
					t.Fatal(err)
				}
			}
			written := 0
			originalRename := renameRepairFile
			renameRepairFile = func(source, target string) error {
				if target == matching {
					written++
				}
				return originalRename(source, target)
			}

			results, err := ApplyCandidates(root, []Application{
				{
					Finding: report.Finding{ID: "finding-a"},
					Candidate: provider.Candidate{
						Kind: "patch", Path: "matching_test.go",
						PreimageSHA256: sha256Hex(original), Content: []byte("package repaired\n"),
					},
				},
				{
					Finding: report.Finding{ID: "finding-b"},
					Candidate: provider.Candidate{
						Kind: "patch", Path: "mismatching_test.go",
						PreimageSHA256: strings.Repeat("0", hex.EncodedLen(sha256.Size)),
						Content:        []byte("package repaired\n"),
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != applicationsInABatch ||
				results[0].Status != StatusCandidate || results[1].Status != StatusArtifact {
				t.Fatalf("ApplyCandidates answered %+v, want a candidate then an artifact", results)
			}
			if current, readErr := os.ReadFile(matching); readErr != nil || string(current) != string(original) {
				t.Fatalf("the matching file of a refused batch reads %q (%v), want %q", current, readErr, original)
			}
			if written != 0 {
				t.Fatalf("a batch nobody could apply wrote the matching file %d times, want none", written)
			}
		})
	}
}
func applyOneMatching(t *testing.T, root string, original []byte) ([]Result, error) {
	t.Helper()
	return ApplyCandidates(root, []Application{{
		Finding: report.Finding{ID: "finding-a"},
		Candidate: provider.Candidate{
			Kind: "patch", Path: "moving_test.go",
			PreimageSHA256: sha256Hex(original), Content: []byte("package repaired\n"),
		},
	}})
}

func TestApplyCandidatesRefusesAFileThatChangedAfterItWasInspected(t *testing.T) {
	preserveRepairHooks(t)
	root := resolvedTempDir(t)
	original := []byte("package fixture\n")
	target := filepath.Join(root, "moving_test.go")
	if err := os.WriteFile(target, original, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	reads := 0
	readRepairFile = func(path string) ([]byte, error) {
		reads++
		if reads == 1 {
			return slices.Clone(original), nil
		}
		return []byte("package edited\n"), nil
	}
	written := 0
	originalRename := renameRepairFile
	renameRepairFile = func(source, destination string) error {
		if destination == target {
			written++
		}
		return originalRename(source, destination)
	}

	results, err := applyOneMatching(t, root, original)
	if err != nil {
		t.Fatalf("a file that changed under the repair reported %v, want an artifact instead", err)
	}
	if len(results) != 1 || results[0].Status != StatusArtifact || results[0].Artifact == "" {
		t.Fatalf("ApplyCandidates answered %+v, want one artifact", results)
	}
	if written != 0 {
		t.Fatalf("a repair of a file that changed wrote it %d times, want none", written)
	}
}

func TestApplyCandidatesReportsAFileItCannotReadTheSecondTime(t *testing.T) {
	preserveRepairHooks(t)
	root := resolvedTempDir(t)
	original := []byte("package fixture\n")
	if err := os.WriteFile(filepath.Join(root, "moving_test.go"), original, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("the second read failed")
	reads := 0
	readRepairFile = func(path string) ([]byte, error) {
		reads++
		if reads == 1 {
			return slices.Clone(original), nil
		}
		return nil, sentinel
	}

	results, err := applyOneMatching(t, root, original)
	if !errors.Is(err, sentinel) {
		t.Fatalf("ApplyCandidates reported %v, want the read failure", err)
	}
	if results == nil {
		t.Fatal("a failed batch answered with no results at all, want the candidate it did not apply")
	}
	if len(results) != 1 || results[0].Status != StatusCandidate {
		t.Fatalf("ApplyCandidates answered %+v, want the candidate it did not apply", results)
	}
}

func TestApplyCandidatesReportsAPathItCannotConfine(t *testing.T) {
	preserveRepairHooks(t)
	root := resolvedTempDir(t)
	sentinel := errors.New("the root could not be inspected")
	statRepairPath = func(string) (os.FileInfo, error) { return nil, sentinel }

	results, err := applyOneMatching(t, root, []byte("package fixture\n"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("ApplyCandidates reported %v, want the failure of the root it could not confine", err)
	}
	if results != nil {
		t.Fatalf("a batch that never started answered with %+v, want no results", results)
	}
}

func rollbackState(root, path string, original []byte, existed bool, content []byte) applicationState {
	return applicationState{
		application: Application{Candidate: provider.Candidate{Kind: "patch", Path: path, Content: content}},
		target:      filepath.Join(root, path),
		original:    original, existed: existed, mode: filemode.ReadableFile,
	}
}

func TestRollbackUndoesWhatItWroteAndRefusesWhatSomebodyElseChanged(t *testing.T) {
	applied := []byte("package repaired\n")
	original := []byte("package fixture\n")
	for _, test := range []struct {
		name    string
		present []byte
		existed bool
		want    string
		left    []byte
	}{
		{name: "a file it replaced", present: applied, existed: true, left: original},
		{name: "a file it created", present: applied},
		{name: "a file it created and somebody removed"},
		{
			name: "a file it replaced and somebody removed", existed: true,
			want: "rollback bystander_test.go",
		},
		{
			name: "a file somebody else has edited since", present: []byte("package edited\n"), existed: true,
			want: "refused a concurrent edit", left: []byte("package edited\n"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := resolvedTempDir(t)
			target := filepath.Join(root, "bystander_test.go")
			if test.present != nil {
				if err := os.WriteFile(target, test.present, filemode.ReadableFile); err != nil {
					t.Fatal(err)
				}
			}
			err := rollbackApplications(root,
				[]applicationState{rollbackState(root, "bystander_test.go", original, test.existed, applied)})
			switch {
			case test.want == "" && err != nil:
				t.Fatalf("rollback reported %v, want none", err)
			case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
				t.Fatalf("rollback reported %v, want it to say %q", err, test.want)
			}
			current, readErr := os.ReadFile(target)
			switch {
			case test.left == nil && !errors.Is(readErr, os.ErrNotExist):
				t.Errorf("rollback left %q (%v), want no file at all", current, readErr)
			case test.left != nil && (readErr != nil || string(current) != string(test.left)):
				t.Errorf("rollback left %q (%v), want %q", current, readErr, test.left)
			}
		})
	}
}

func TestRollbackReportsAFileItCannotReadOrRestore(t *testing.T) {
	applied := []byte("package repaired\n")
	original := []byte("package fixture\n")
	for _, stage := range []string{"read", "restore"} {
		t.Run(stage, func(t *testing.T) {
			preserveRepairHooks(t)
			root := resolvedTempDir(t)
			if err := os.WriteFile(filepath.Join(root, "bystander_test.go"), applied, filemode.ReadableFile); err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New(stage + " failed")
			if stage == "read" {
				readRepairFile = func(string) ([]byte, error) { return nil, sentinel }
			} else {
				renameRepairFile = func(string, string) error { return sentinel }
			}
			err := rollbackApplications(root,
				[]applicationState{rollbackState(root, "bystander_test.go", original, true, applied)})
			if !errors.Is(err, sentinel) {
				t.Fatalf("rollback reported %v, want the %s failure", err, stage)
			}
		})
	}
}

func TestStoreCandidatePropagatesEveryFailureOfTheStoreItWritesTo(t *testing.T) {
	record := CandidateRecord{
		ID: "0123456789abcdef",
		Finding: report.Finding{
			ID: "finding-a", Kind: "surviving-mutant", Summary: "survived",
		},
		Candidate: provider.Candidate{
			Kind: "patch", Path: "generated_test.go", Content: []byte("package fixture\n"),
		},
	}
	for _, stage := range []string{"confine", "read", "write"} {
		t.Run(stage, func(t *testing.T) {
			preserveRepairHooks(t)
			root := resolvedTempDir(t)
			sentinel := errors.New(stage + " failed")
			switch stage {
			case "confine":
				statRepairPath = func(string) (os.FileInfo, error) { return nil, sentinel }
			case "read":
				readRepairFile = func(string) ([]byte, error) { return nil, sentinel }
			case "write":
				renameRepairFile = func(string, string) error { return sentinel }
			}
			if _, err := StoreCandidate(root, record); !errors.Is(err, sentinel) {
				t.Fatalf("StoreCandidate reported %v, want the %s failure", err, stage)
			}
		})
	}
}

func TestLoadCandidateReportsTheStageThatRefusedTheFile(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents []byte
		absent   bool
		want     string
	}{
		{name: "a record nobody stored", absent: true, want: "read repair candidate"},
		{name: "a record that is not JSON", contents: []byte("{\n"), want: "decode repair candidate"},
		{
			name:     "a record over the ceiling",
			contents: append(bytes.Repeat([]byte("x"), candidateRecordCeiling+1), '\n'),
			want:     "exceeds 8 MiB",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := resolvedTempDir(t)
			directory := filepath.Join(root, ".goatest", "candidates")
			if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
				t.Fatal(err)
			}
			if !test.absent {
				if err := os.WriteFile(filepath.Join(directory, "0123456789abcdef.json"),
					test.contents, filemode.PrivateFile); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadCandidate(root, "0123456789abcdef"); err == nil {
				t.Fatal("LoadCandidate read a record it should have refused")
			} else if !strings.Contains(err.Error(), test.want) {
				t.Errorf("the error is %q, want it to say %q", err, test.want)
			}
		})
	}
}

func TestListCandidatesReportsAStoreItCannotRead(t *testing.T) {
	root := resolvedTempDir(t)
	if err := os.MkdirAll(filepath.Join(root, ".goatest"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".goatest", "candidates"),
		[]byte("not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}

	if _, err := ListCandidates(root); err == nil {
		t.Fatal("a candidate store that is not a directory was listed")
	} else if !strings.Contains(err.Error(), "read repair candidates") {
		t.Errorf("the error is %q, want it to say it could not read the store", err)
	}
}

func TestCurrentContentSaysNothingExistsForEveryFailureItReports(t *testing.T) {
	root := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "present_test.go"),
		[]byte("package fixture\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"read", "confine"} {
		t.Run(stage, func(t *testing.T) {
			preserveRepairHooks(t)
			sentinel := errors.New("the " + stage + " failed")
			if stage == "read" {
				readRepairFile = func(string) ([]byte, error) { return nil, sentinel }
			} else {
				statRepairPath = func(string) (os.FileInfo, error) { return nil, sentinel }
			}
			content, exists, err := CurrentContent(root, "present_test.go")
			if !errors.Is(err, sentinel) {
				t.Fatalf("CurrentContent reported %v, want the %s failure", err, stage)
			}
			if exists || content != nil {
				t.Fatalf("a failed %s answered (%q, %t), want nothing and no file", stage, content, exists)
			}
		})
	}
}

func TestConfinedPathRefusesASymbolicLinkInsideTheRepository(t *testing.T) {
	root := resolvedTempDir(t)
	if err := os.MkdirAll(filepath.Join(root, "real"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linked")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	if _, err := confinedPath(root, "linked/inside_test.go"); err == nil {
		t.Fatal("a path through a symbolic link inside the repository was confined")
	} else if !strings.Contains(err.Error(), "crosses symbolic link") {
		t.Errorf("the error is %q, want it to name the link it refused", err)
	}
}

func failLstatFor(name string, sentinel error) {
	original := lstatRepairPath
	lstatRepairPath = func(path string) (os.FileInfo, error) {
		if filepath.Base(path) == name {
			return nil, sentinel
		}
		return original(path)
	}
}

func TestApplyCandidatesReportsAPathItCannotInspect(t *testing.T) {
	preserveRepairHooks(t)
	root := resolvedTempDir(t)
	sentinel := errors.New("the candidate path could not be inspected")
	failLstatFor("moving_test.go", sentinel)

	results, err := applyOneMatching(t, root, []byte("package fixture\n"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("ApplyCandidates reported %v, want the failure of the path it could not inspect", err)
	}
	if results != nil {
		t.Fatalf("a batch that never started answered with %+v, want no results", results)
	}
}

func TestApplyCandidatesReportsAFileItCannotDescribe(t *testing.T) {
	preserveRepairHooks(t)
	root := resolvedTempDir(t)
	target := filepath.Join(root, "moving_test.go")
	if err := os.WriteFile(target, []byte("package fixture\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("the file could not be described")
	originalStat := statRepairPath
	statRepairPath = func(path string) (os.FileInfo, error) {
		if path == target {
			return nil, sentinel
		}
		return originalStat(path)
	}

	results, err := applyOneMatching(t, root, []byte("package fixture\n"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("ApplyCandidates reported %v, want the failure of the file it could not describe", err)
	}
	if results != nil {
		t.Fatalf("a batch that never started answered with %+v, want no results", results)
	}
}

func TestApplyCandidatesReportsAnArtifactItCannotWrite(t *testing.T) {
	preserveRepairHooks(t)
	root := resolvedTempDir(t)
	original := []byte("package fixture\n")
	if err := os.WriteFile(filepath.Join(root, "moving_test.go"), original, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	reads := 0
	readRepairFile = func(string) ([]byte, error) {
		reads++
		if reads == 1 {
			return slices.Clone(original), nil
		}
		return []byte("package edited\n"), nil
	}
	sentinel := errors.New("the artifact could not be written")
	marshalRepairArtifact = func(any, string, string) ([]byte, error) { return nil, sentinel }

	results, err := applyOneMatching(t, root, original)
	if !errors.Is(err, sentinel) {
		t.Fatalf("ApplyCandidates reported %v, want the failure of the artifact it could not write", err)
	}
	if results == nil {
		t.Fatal("a failed batch answered with no results at all, want the candidate it did not apply")
	}
}

func TestRollbackReportsAFileItCannotRemove(t *testing.T) {
	preserveRepairHooks(t)
	root := resolvedTempDir(t)
	applied := []byte("package repaired\n")
	if err := os.WriteFile(filepath.Join(root, "bystander_test.go"), applied, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("the file could not be removed")
	removeRepairFile = func(string) error { return sentinel }

	err := rollbackApplications(root,
		[]applicationState{rollbackState(root, "bystander_test.go", nil, false, applied)})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback reported %v, want the removal failure", err)
	}
}
