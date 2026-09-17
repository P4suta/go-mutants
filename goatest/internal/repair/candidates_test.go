// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package repair_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/provider"
	"github.com/P4suta/go-mutants/goatest/internal/repair"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const (
	firstCandidateID  = "0123456789abcdef"
	secondCandidateID = "fedcba9876543210"
)

func candidateRecord(id string, change func(*repair.CandidateRecord)) repair.CandidateRecord {
	record := repair.CandidateRecord{
		ID: id, Snapshot: "snapshot-a",
		Finding: report.Finding{ID: "finding-" + id, Kind: "surviving-mutant", Summary: "survived"},
		Candidate: provider.Candidate{
			Kind: "patch", Path: "generated_test.go", Content: []byte("package fixture\n"),
		},
	}
	if change != nil {
		change(&record)
	}
	return record
}

func storedCandidate(t *testing.T, root string, record repair.CandidateRecord) string {
	t.Helper()
	relative, err := repair.StoreCandidate(root, record)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, filepath.FromSlash(relative))
}

func rewriteCandidate(t *testing.T, path string, change func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	change(document)
	rewritten, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, rewritten, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCandidateRefusesARecordThatNamesTooLittleToFindAgain(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*repair.CandidateRecord)
	}{
		{name: "an ID no filename could hold", change: func(r *repair.CandidateRecord) { r.ID = "../escape" }},
		{name: "an ID of the wrong length", change: func(r *repair.CandidateRecord) { r.ID = "abc" }},
		{name: "a finding that names nothing", change: func(r *repair.CandidateRecord) { r.Finding.ID = "" }},
		{name: "a candidate for no path", change: func(r *repair.CandidateRecord) { r.Candidate.Path = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := repair.StoreCandidate(t.TempDir(), candidateRecord(firstCandidateID, test.change)); err == nil {
				t.Fatal("an incomplete candidate record was stored")
			}
		})
	}
}

func TestStoreCandidateIsRepeatableAndRefusesToRewriteAnID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	record := candidateRecord(firstCandidateID, nil)
	first := storedCandidate(t, root, record)

	if again := storedCandidate(t, root, record); again != first {
		t.Fatalf("storing the same record twice named %q then %q", first, again)
	}
	other := candidateRecord(firstCandidateID, func(r *repair.CandidateRecord) {
		r.Candidate.Content = []byte("package other\n")
	})
	if _, err := repair.StoreCandidate(root, other); err == nil {
		t.Fatal("a second record under one ID replaced the first")
	} else if !strings.Contains(err.Error(), "different evidence") {
		t.Errorf("the error is %q, want it to say the ID already stores other evidence", err)
	}
}

func TestLoadCandidateRefusesADocumentThatIsNotTheOneItAskedFor(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "another version", change: func(d map[string]any) { d["version"] = "repair-candidate-v2" }},
		{name: "another identity", change: func(d map[string]any) { d["id"] = secondCandidateID }},
		{
			name:   "a finding that names nothing",
			change: func(d map[string]any) { d["finding"].(map[string]any)["id"] = "" },
		},
		{
			name:   "a candidate for no path",
			change: func(d map[string]any) { d["candidate"].(map[string]any)["path"] = "" },
		},
		{
			name:   "a kind this repository does not repair",
			change: func(d map[string]any) { d["candidate"].(map[string]any)["kind"] = "rewrite" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			rewriteCandidate(t, storedCandidate(t, root, candidateRecord(firstCandidateID, nil)), test.change)
			if _, err := repair.LoadCandidate(root, firstCandidateID); err == nil {
				t.Fatal("a candidate document that is not the one asked for was loaded")
			} else if !strings.Contains(err.Error(), "identity mismatch") {
				t.Errorf("the error is %q, want it to say the identity does not match", err)
			}
		})
	}
}

func TestLoadCandidateAcceptsEveryKindThisRepositoryRepairs(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"patch", "corpus"} {
		t.Run("kind "+kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			record := candidateRecord(firstCandidateID, func(r *repair.CandidateRecord) {
				r.Candidate.Kind = kind
				if kind == "corpus" {
					r.Candidate.Path = "testdata/fuzz/FuzzTarget/seed"
				}
			})
			storedCandidate(t, root, record)
			loaded, err := repair.LoadCandidate(root, firstCandidateID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Candidate.Kind != kind {
				t.Fatalf("the loaded candidate is of kind %q, want %q", loaded.Candidate.Kind, kind)
			}
		})
	}
}

func TestLoadCandidateRefusesAnIDNoStoreCouldHaveWritten(t *testing.T) {
	t.Parallel()
	if _, err := repair.LoadCandidate(t.TempDir(), "../escape"); err == nil {
		t.Fatal("an unsafe candidate ID was read")
	} else if !strings.Contains(err.Error(), "invalid repair candidate ID") {
		t.Errorf("the error is %q, want it to refuse the ID rather than the file", err)
	}
}

func TestLoadCandidateRefusesADocumentWithAnythingAfterIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := storedCandidate(t, root, candidateRecord(firstCandidateID, nil))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("{}\n")...), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if _, err := repair.LoadCandidate(root, firstCandidateID); err == nil {
		t.Fatal("a candidate file holding two documents was loaded")
	} else if !strings.Contains(err.Error(), "trailing data") {
		t.Errorf("the error is %q, want it to say the file holds more than the record", err)
	}
}

func TestListCandidatesReadsEveryStoredRecordInOrderAndNothingElse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	storedCandidate(t, root, candidateRecord(secondCandidateID, nil))
	storedCandidate(t, root, candidateRecord(firstCandidateID, nil))
	directory := filepath.Join(root, ".goatest", "candidates")
	if err := os.WriteFile(filepath.Join(directory, "notes.txt"), []byte("ignored"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(directory, "nested.json"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}

	records, err := repair.ListCandidates(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(records))
	for _, record := range records {
		got = append(got, record.ID)
	}
	if len(got) != 2 || got[0] != firstCandidateID || got[1] != secondCandidateID {
		t.Fatalf("ListCandidates read %q, want %q then %q", got, firstCandidateID, secondCandidateID)
	}
}

func TestListCandidatesAnswersAStoreNobodyHasWrittenToWithNoRecords(t *testing.T) {
	t.Parallel()
	records, err := repair.ListCandidates(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if records == nil {
		t.Fatal("a store nobody has written to answered with no slice rather than no records")
	}
	if len(records) != 0 {
		t.Fatalf("a store nobody has written to holds %d records", len(records))
	}
}

func TestListCandidatesRefusesAFileNoStoreCouldHaveWritten(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	storedCandidate(t, root, candidateRecord(firstCandidateID, nil))
	directory := filepath.Join(root, ".goatest", "candidates")
	if err := os.WriteFile(filepath.Join(directory, "SHOUTING.json"), []byte("{}"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}

	if _, err := repair.ListCandidates(root); err == nil {
		t.Fatal("a candidate file with an unsafe name was listed")
	} else if !strings.Contains(err.Error(), "unsafe repair candidate file") {
		t.Errorf("the error is %q, want it to name the file it refused", err)
	}
}

func TestListCandidatesReportsARecordItCannotLoad(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rewriteCandidate(t, storedCandidate(t, root, candidateRecord(firstCandidateID, nil)),
		func(d map[string]any) { d["version"] = "repair-candidate-v2" })

	if _, err := repair.ListCandidates(root); err == nil {
		t.Fatal("a store holding a record that cannot be loaded was listed")
	} else if !strings.Contains(err.Error(), "identity mismatch") {
		t.Errorf("the error is %q, want the failure of the record it could not load", err)
	}
}

func TestCurrentContentReadsARepairablePathAndSaysWhenThereIsNone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	contents := []byte("package fixture\n")
	if err := os.WriteFile(filepath.Join(root, "present_test.go"), contents, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}

	data, exists, err := repair.CurrentContent(root, "present_test.go")
	if err != nil || !exists || string(data) != string(contents) {
		t.Fatalf("CurrentContent of a file that is there = (%q, %t, %v)", data, exists, err)
	}
	data, exists, err = repair.CurrentContent(root, "absent_test.go")
	if err != nil || exists {
		t.Fatalf("CurrentContent of a file that is not there = (%q, %t, %v)", data, exists, err)
	}
	if data != nil {
		t.Errorf("CurrentContent of a file that is not there answered with %q, want no content at all", data)
	}
}

func TestCurrentContentRefusesAPathNoRepairMayTouch(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"", "main.go", "../outside_test.go", "/absolute_test.go"} {
		t.Run("path "+path, func(t *testing.T) {
			t.Parallel()
			content, exists, err := repair.CurrentContent(t.TempDir(), path)
			if err == nil {
				t.Fatalf("CurrentContent read %q, which no repair may touch", path)
			}
			if exists || content != nil {
				t.Fatalf("a path no repair may touch answered (%q, %t), want nothing and no file", content, exists)
			}
		})
	}
}

func TestValidateCandidateRefusesWhatItCannotValidateBeforeAskingAnybody(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	allowed := provider.Candidate{Kind: "patch", Path: "candidate_test.go", Content: []byte("package fixture\n")}
	for _, test := range []struct {
		name      string
		candidate provider.Candidate
		validator repair.Validator
		want      string
	}{
		{
			name:      "a path outside what a repair may touch",
			candidate: provider.Candidate{Kind: "patch", Path: "main.go", Content: allowed.Content},
			want:      "outside _test.go and standard fuzz corpus",
		},
		{
			name:      "a path that crosses a file",
			candidate: provider.Candidate{Kind: "patch", Path: "occupied/nested_test.go", Content: allowed.Content},
			want:      "crosses non-directory",
		},
		{name: "no validator at all", candidate: allowed, want: "requires a validator"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := os.WriteFile(filepath.Join(root, "occupied"), []byte("not a directory"), filemode.PrivateFile); err != nil &&
				!os.IsExist(err) {
				t.Fatal(err)
			}
			_, err := repair.ValidateCandidate(t.Context(), root,
				report.Finding{ID: "finding-a"}, test.candidate, test.validator)
			if err == nil {
				t.Fatalf("ValidateCandidate accepted %+v", test.candidate)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("the error is %q, want it to say %q", err, test.want)
			}
		})
	}
}
