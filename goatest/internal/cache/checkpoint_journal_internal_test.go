// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/checkpoint"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

func journaledStore(t *testing.T, digest string, withCatalog bool) (*Store, string, string) {
	t.Helper()
	root := t.TempDir()
	store := New(root)
	state := checkpointFixture(digest)
	if withCatalog {
		state.Mutation = &checkpoint.Mutation{CatalogFingerprint: cacheTestDigest("f")}
	}
	if err := store.PutCheckpoint(digest, state); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "v1", digest)
	return store, directory, checkpointFileDigest(mustRead(t, filepath.Join(directory, CheckpointFileName)))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func journalLine(t *testing.T, record checkpointJournalRecord, sign bool) []byte {
	t.Helper()
	if sign {
		record.Checksum = checkpointJournalChecksum(record)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func writeJournal(t *testing.T, directory string, lines ...[]byte) {
	t.Helper()
	var data []byte
	for _, line := range lines {
		data = append(data, line...)
	}
	if err := os.WriteFile(filepath.Join(directory, CheckpointJournalFileName), data, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointJournalAcceptsNothingToApply(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		journal []byte
	}{
		{name: "an empty file", journal: []byte{}},
		{name: "a line nobody committed", journal: []byte(`{"schema":"x"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			digest := cacheTestDigest("a")
			store, directory, _ := journaledStore(t, digest, false)
			writeJournal(t, directory, test.journal)
			state, found, err := store.GetCheckpoint(digest)
			if err != nil || !found || len(state.Baseline.Targets) != 0 {
				t.Fatalf("GetCheckpoint = (%+v, %t, %v), want the base state unchanged", state, found, err)
			}
		})
	}
}

func TestCheckpointJournalReadsALineThatIsOnlyItsTerminator(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		journal []byte
	}{
		{name: "a terminator and nothing else", journal: []byte("\n")},
		{name: "a terminator before a committed line", journal: []byte("\n{}\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			digest := cacheTestDigest("a")
			store, directory, _ := journaledStore(t, digest, false)
			writeJournal(t, directory, test.journal)
			state, found, err := store.GetCheckpoint(digest)
			if err == nil || found || !strings.Contains(err.Error(), "journal line 1") {
				t.Fatalf("GetCheckpoint = (%+v, %t, %v), want the empty first line refused", state, found, err)
			}
		})
	}
}

func TestCheckpointJournalNamesTheLineItRefusedAndWhy(t *testing.T) {
	t.Parallel()
	digest := cacheTestDigest("a")
	target := func(id string) *checkpoint.BaselineTarget {
		return &checkpoint.BaselineTarget{
			ID: id, Executed: true,
			Inventory: report.TargetDisposition{ID: id, Name: "Test" + id, Kind: "test", Package: "example.test/p", Status: "passed"},
		}
	}
	for _, test := range []struct {
		name    string
		catalog bool
		lines   func(t *testing.T, base string) [][]byte
		message string
	}{
		{
			name: "undecodable",
			lines: func(*testing.T, string) [][]byte {
				return [][]byte{[]byte("{\n"), []byte("{\n")}
			},
			message: "journal line 1:",
		},
		{
			name: "trailing data",
			lines: func(t *testing.T, base string) [][]byte {
				first := journalLine(t, checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base, BaselineTarget: target("a"),
				}, true)
				second := append([]byte{}, first[:len(first)-1]...)
				return [][]byte{first, append(append(second, []byte(" {}")...), '\n')}
			},
			message: "journal line 2 has trailing data",
		},
		{
			name: "an unsigned line",
			lines: func(t *testing.T, base string) [][]byte {
				return [][]byte{journalLine(t, checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base, BaselineTarget: target("a"),
				}, false)}
			},
			message: "journal line 1 has an invalid identity or checksum",
		},
		{
			name: "a signature from another record",
			lines: func(t *testing.T, base string) [][]byte {
				record := checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base, BaselineTarget: target("a"),
				}
				record.Checksum = checkpointJournalChecksum(checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base, BaselineTarget: target("b"),
				})
				return [][]byte{journalLine(t, record, false)}
			},
			message: "journal line 1 has an invalid identity or checksum",
		},
		{
			name: "another schema",
			lines: func(t *testing.T, base string) [][]byte {
				return [][]byte{journalLine(t, checkpointJournalRecord{
					Schema: "another-schema", InputDigest: digest, BaseDigest: base, BaselineTarget: target("a"),
				}, true)}
			},
			message: "journal line 1 has an invalid identity or checksum",
		},
		{
			name: "another input digest",
			lines: func(t *testing.T, base string) [][]byte {
				return [][]byte{journalLine(t, checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: cacheTestDigest("b"), BaseDigest: base, BaselineTarget: target("a"),
				}, true)}
			},
			message: "journal line 1 has an invalid identity or checksum",
		},
		{
			name: "a base identity that changes back",
			lines: func(t *testing.T, base string) [][]byte {
				current := journalLine(t, checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base, BaselineTarget: target("a"),
				}, true)
				stale := journalLine(t, checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: cacheTestDigest("c"), BaselineTarget: target("b"),
				}, true)
				return [][]byte{current, stale}
			},
			message: "journal line 2 changed base identity",
		},
		{
			name: "no unit at all",
			lines: func(t *testing.T, base string) [][]byte {
				return [][]byte{journalLine(t, checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base,
				}, true)}
			},
			message: "journal line 1 does not contain exactly one unit",
		},
		{
			name: "two units at once",
			lines: func(t *testing.T, base string) [][]byte {
				return [][]byte{journalLine(t, checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base,
					BaselineTarget: target("a"),
					BaselineSuite:  &checkpoint.BaselineSuite{Package: "example.test/p"},
				}, true)}
			},
			message: "journal line 1 does not contain exactly one unit",
		},
		{
			name: "a duplicated target",
			lines: func(t *testing.T, base string) [][]byte {
				line := journalLine(t, checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base, BaselineTarget: target("a"),
				}, true)
				return [][]byte{line, line}
			},
			message: "journal line 2 duplicates baseline target a",
		},
		{
			name: "a duplicated suite",
			lines: func(t *testing.T, base string) [][]byte {
				line := journalLine(t, checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base,
					BaselineSuite: &checkpoint.BaselineSuite{Package: "example.test/p"},
				}, true)
				return [][]byte{line, line}
			},
			message: "journal line 2 duplicates baseline suite example.test/p",
		},
		{
			name: "a mutant before its catalog",
			lines: func(t *testing.T, base string) [][]byte {
				return [][]byte{journalLine(t, checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base,
					MutationResult: &checkpoint.MutationResult{ID: "mutant-a"},
				}, true)}
			},
			message: "journal line 1 records a mutant before its catalog",
		},
		{
			name:    "a duplicated mutant",
			catalog: true,
			lines: func(t *testing.T, base string) [][]byte {
				line := journalLine(t, checkpointJournalRecord{
					Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base,
					MutationResult: &checkpoint.MutationResult{
						ID: "mutant-a", Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed"}},
					},
				}, true)
				return [][]byte{line, line}
			},
			message: "journal line 2 duplicates mutant mutant-a",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, directory, base := journaledStore(t, digest, test.catalog)
			writeJournal(t, directory, test.lines(t, base)...)
			state, found, err := store.GetCheckpoint(digest)
			if err == nil || found || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("GetCheckpoint = (%+v, %t, %v), want %q", state, found, err, test.message)
			}
		})
	}
}

func TestCheckpointJournalRefusesAReplayThatDoesNotValidate(t *testing.T) {
	t.Parallel()
	digest := cacheTestDigest("a")
	store, directory, base := journaledStore(t, digest, false)
	writeJournal(t, directory, journalLine(t, checkpointJournalRecord{
		Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base,
		BaselineTarget: &checkpoint.BaselineTarget{ID: ""},
	}, true))
	state, found, err := store.GetCheckpoint(digest)
	if err == nil || found || !strings.Contains(err.Error(), "apply checkpoint journal") {
		t.Fatalf("GetCheckpoint = (%+v, %t, %v), want the replayed state refused", state, found, err)
	}
}

func TestCheckpointJournalReplaysIntoACanonicalOrder(t *testing.T) {
	t.Parallel()
	digest := cacheTestDigest("a")
	store, directory, base := journaledStore(t, digest, true)
	target := func(id string) []byte {
		return journalLine(t, checkpointJournalRecord{
			Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base,
			BaselineTarget: &checkpoint.BaselineTarget{
				ID: id, Executed: true,
				Inventory: report.TargetDisposition{ID: id, Name: "Test" + id, Kind: "test", Package: "example.test/p", Status: "passed"},
			},
		}, true)
	}
	suite := func(pkg string) []byte {
		return journalLine(t, checkpointJournalRecord{
			Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base,
			BaselineSuite: &checkpoint.BaselineSuite{Package: pkg},
		}, true)
	}
	mutant := func(id string) []byte {
		return journalLine(t, checkpointJournalRecord{
			Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base,
			MutationResult: &checkpoint.MutationResult{
				ID: id, Evidence: []report.Evidence{{Kind: "mutation", ID: id, Status: "killed"}},
			},
		}, true)
	}
	writeJournal(t, directory,
		target("target-z"), target("target-a"),
		suite("example.test/z"), suite("example.test/a"),
		mutant("mutant-z"), mutant("mutant-a"),
	)
	state, found, err := store.GetCheckpoint(digest)
	if err != nil || !found {
		t.Fatalf("GetCheckpoint = (%+v, %t, %v)", state, found, err)
	}
	targets := []string{state.Baseline.Targets[0].ID, state.Baseline.Targets[1].ID}
	suites := []string{state.Baseline.Suites[0].Package, state.Baseline.Suites[1].Package}
	mutants := []string{state.Mutation.Results[0].ID, state.Mutation.Results[1].ID}
	if !slices.Equal(targets, []string{"target-a", "target-z"}) ||
		!slices.Equal(suites, []string{"example.test/a", "example.test/z"}) ||
		!slices.Equal(mutants, []string{"mutant-a", "mutant-z"}) {
		t.Fatalf("replayed order = %v %v %v, want each sorted by its identity", targets, suites, mutants)
	}
}

func TestCheckpointJournalRefusesAMutantTheCatalogAlreadyHolds(t *testing.T) {
	t.Parallel()
	digest := cacheTestDigest("a")
	root := t.TempDir()
	store := New(root)
	state := checkpointFixture(digest)
	state.Mutation = &checkpoint.Mutation{
		CatalogFingerprint: cacheTestDigest("f"),
		Results: []checkpoint.MutationResult{{
			ID: "mutant-a", Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed"}},
		}},
	}
	if err := store.PutCheckpoint(digest, state); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "v1", digest)
	base := checkpointFileDigest(mustRead(t, filepath.Join(directory, CheckpointFileName)))
	writeJournal(t, directory, journalLine(t, checkpointJournalRecord{
		Schema: checkpointJournalSchema, InputDigest: digest, BaseDigest: base,
		MutationResult: &checkpoint.MutationResult{
			ID: "mutant-a", Evidence: []report.Evidence{{Kind: "mutation", ID: "mutant-a", Status: "killed"}},
		},
	}, true))
	loaded, found, err := store.GetCheckpoint(digest)
	if err == nil || found || !strings.Contains(err.Error(), "journal line 1 duplicates mutant mutant-a") {
		t.Fatalf("GetCheckpoint = (%+v, %t, %v), want the catalogued mutant refused", loaded, found, err)
	}
}
