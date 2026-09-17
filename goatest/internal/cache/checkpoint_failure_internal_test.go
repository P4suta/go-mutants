// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/checkpoint"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func checkpointFixture(digest string) checkpoint.State {
	return checkpoint.State{Schema: checkpoint.SchemaV1, InputDigest: digest, Attempts: 1}
}

func checkpointDirectory(t *testing.T, root, digest string) string {
	t.Helper()
	directory := filepath.Join(root, "v1", digest)
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestGetCheckpointRefusesAnInvalidDigestBeforeReadingAnything(t *testing.T) {
	t.Parallel()
	state, found, err := New(t.TempDir()).GetCheckpoint("")
	if err == nil || found || !strings.Contains(err.Error(), "invalid cache digest") {
		t.Fatalf("GetCheckpoint(\"\") = (%+v, %t, %v)", state, found, err)
	}
}

func TestGetCheckpointReportsAReadFailureThatIsNotAbsence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	digest := cacheTestDigest("a")
	directory := checkpointDirectory(t, root, digest)
	if err := os.MkdirAll(filepath.Join(directory, CheckpointFileName), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	state, found, err := New(root).GetCheckpoint(digest)
	if err == nil || found || !strings.Contains(err.Error(), "read checkpoint") {
		t.Fatalf("GetCheckpoint over a directory = (%+v, %t, %v)", state, found, err)
	}
}

func TestGetCheckpointReportsUndecodableAndMisidentifiedState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		stored  []byte
		message string
	}{
		{name: "undecodable", stored: []byte("{"), message: "checkpoint decode"},
		{
			name:    "another input digest",
			stored:  checkpoint.JSON(checkpointFixture(cacheTestDigest("b"))),
			message: "checkpoint input identity mismatch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			digest := cacheTestDigest("a")
			directory := checkpointDirectory(t, root, digest)
			if err := os.WriteFile(filepath.Join(directory, CheckpointFileName), test.stored, filemode.ReadableFile); err != nil {
				t.Fatal(err)
			}
			state, found, err := New(root).GetCheckpoint(digest)
			if err == nil || found || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("GetCheckpoint = (%+v, %t, %v), want %q", state, found, err, test.message)
			}
		})
	}
}

func TestPendingCheckpointReportsAnInspectionFailureThatIsNotAbsence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "v1"), []byte("not a directory"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	pending, err := New(root).PendingCheckpoint()
	if err == nil || pending || !strings.Contains(err.Error(), "inspect checkpoints") {
		t.Fatalf("PendingCheckpoint over a file = (%t, %v)", pending, err)
	}
}

func TestPendingCheckpointIgnoresEntriesThatAreNotDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	version := filepath.Join(root, "v1")
	if err := os.MkdirAll(version, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(version, "stray"), []byte("loose file"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	pending, err := New(root).PendingCheckpoint()
	if err != nil || pending {
		t.Fatalf("PendingCheckpoint with only a loose file = (%t, %v)", pending, err)
	}
}

func TestPendingCheckpointReportsAStatFailureThatIsNotAbsence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := checkpointDirectory(t, root, cacheTestDigest("a"))
	if err := os.Symlink(CheckpointFileName, filepath.Join(directory, CheckpointFileName)); err != nil {
		t.Fatal(err)
	}
	pending, err := New(root).PendingCheckpoint()
	if err == nil || pending || !strings.Contains(err.Error(), "inspect checkpoint") {
		t.Fatalf("PendingCheckpoint over a symlink loop = (%t, %v)", pending, err)
	}
}

func TestPutCheckpointRefusesStateThatDoesNotBelongToItsEntry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	digest := cacheTestDigest("a")
	err := New(root).PutCheckpoint(digest, checkpointFixture(cacheTestDigest("b")))
	if err == nil || !strings.Contains(err.Error(), "does not match its cache entry") {
		t.Fatalf("PutCheckpoint of foreign state = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "v1")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused PutCheckpoint created the cache: %v", statErr)
	}
}

func TestPutCheckpointRefusesADigestThatIsNotAnEntry(t *testing.T) {
	t.Parallel()
	state := checkpointFixture("")
	if err := New(t.TempDir()).PutCheckpoint("", state); err == nil || !strings.Contains(err.Error(), "invalid cache digest") {
		t.Fatalf("PutCheckpoint with no digest = %v", err)
	}
}

func TestPutCheckpointRefusesStateThatDoesNotValidate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	digest := cacheTestDigest("a")
	state := checkpointFixture(digest)
	state.Attempts = 0
	if err := New(root).PutCheckpoint(digest, state); err == nil {
		t.Fatal("PutCheckpoint accepted state that does not validate")
	}
}

func TestPutCheckpointReportsEveryStageOfItsAtomicWrite(t *testing.T) {
	t.Parallel()
	failure := errors.New("stage failure")
	for _, test := range []struct {
		name    string
		hooks   func(*stubCacheFile) storeHooks
		message string
	}{
		{
			name: "directory",
			hooks: func(*stubCacheFile) storeHooks {
				return storeHooks{mkdirAll: func(string, os.FileMode) error { return failure }}
			},
			message: "create checkpoint directory",
		},
		{
			name: "temporary file",
			hooks: func(*stubCacheFile) storeHooks {
				return storeHooks{createTemporary: func(string, string) (cacheWritableFile, error) { return nil, failure }}
			},
			message: "create checkpoint temporary file",
		},
		{
			name: "write",
			hooks: func(file *stubCacheFile) storeHooks {
				file.writeErr = failure
				return storeHooks{createTemporary: func(string, string) (cacheWritableFile, error) { return file, nil }}
			},
			message: "write checkpoint",
		},
		{
			name: "sync",
			hooks: func(file *stubCacheFile) storeHooks {
				file.syncErr = failure
				return storeHooks{createTemporary: func(string, string) (cacheWritableFile, error) { return file, nil }}
			},
			message: "sync checkpoint",
		},
		{
			name: "close",
			hooks: func(file *stubCacheFile) storeHooks {
				file.closeErr = failure
				return storeHooks{createTemporary: func(string, string) (cacheWritableFile, error) { return file, nil }}
			},
			message: "close checkpoint",
		},
		{
			name: "publish",
			hooks: func(file *stubCacheFile) storeHooks {
				return storeHooks{
					createTemporary: func(string, string) (cacheWritableFile, error) { return file, nil },
					rename:          func(string, string) error { return failure },
				}
			},
			message: "atomically publish checkpoint",
		},
		{
			name: "compacted journal",
			hooks: func(file *stubCacheFile) storeHooks {
				return storeHooks{
					createTemporary: func(string, string) (cacheWritableFile, error) { return file, nil },
					rename:          func(string, string) error { return nil },
					remove: func(path string) error {
						if filepath.Base(path) == CheckpointJournalFileName {
							return failure
						}
						return nil
					},
				}
			},
			message: "remove compacted checkpoint journal",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			digest := cacheTestDigest("a")
			file := &stubCacheFile{name: filepath.Join(root, "temporary")}
			err := New(root).putCheckpointWithHooks(digest, checkpointFixture(digest), test.hooks(file))
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("PutCheckpoint = %v, want %q wrapping %v", err, test.message, failure)
			}
		})
	}
}

func TestPutCheckpointRemovesAnAbsentCompactedJournalWithoutComplaint(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	digest := cacheTestDigest("a")
	if err := New(root).PutCheckpoint(digest, checkpointFixture(digest)); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(root, "v1", digest, CheckpointJournalFileName)
	if _, err := os.Stat(journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat of an absent journal = %v", err)
	}
}

func TestAppendCheckpointRefusesAnInvalidDigestBeforeReadingAnything(t *testing.T) {
	t.Parallel()
	err := New(t.TempDir()).AppendBaselineCheckpoint("", checkpoint.BaselineTarget{ID: "target"})
	if err == nil || !strings.Contains(err.Error(), "invalid cache digest") {
		t.Fatalf("AppendBaselineCheckpoint with no digest = %v", err)
	}
}

func TestAppendCheckpointNeedsTheCheckpointItJournalsAgainst(t *testing.T) {
	t.Parallel()
	digest := cacheTestDigest("a")
	err := New(t.TempDir()).AppendBaselineCheckpoint(digest, checkpoint.BaselineTarget{ID: "target"})
	if err == nil || !strings.Contains(err.Error(), "read checkpoint before journaling") {
		t.Fatalf("AppendBaselineCheckpoint without a checkpoint = %v", err)
	}
}

func TestAppendCheckpointReportsEveryStageOfItsJournalWrite(t *testing.T) {
	t.Parallel()
	failure := errors.New("stage failure")
	for _, test := range []struct {
		name    string
		prepare func(*stubCacheFile)
		open    bool
		message string
	}{
		{name: "open", message: "open checkpoint journal"},
		{name: "write", prepare: func(file *stubCacheFile) { file.writeErr = failure }, open: true, message: "append checkpoint journal"},
		{name: "sync", prepare: func(file *stubCacheFile) { file.syncErr = failure }, open: true, message: "sync checkpoint journal"},
		{name: "close", prepare: func(file *stubCacheFile) { file.closeErr = failure }, open: true, message: "close checkpoint journal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			digest := cacheTestDigest("a")
			store := New(root)
			if err := store.PutCheckpoint(digest, checkpointFixture(digest)); err != nil {
				t.Fatal(err)
			}
			file := &stubCacheFile{name: filepath.Join(root, "journal")}
			if test.prepare != nil {
				test.prepare(file)
			}
			hooks := storeHooks{openAppend: func(string, os.FileMode) (cacheWritableFile, error) {
				if !test.open {
					return nil, failure
				}
				return file, nil
			}}
			record := checkpointJournalRecord{BaselineTarget: &checkpoint.BaselineTarget{ID: "target"}}
			err := store.appendCheckpointRecordWithHooks(digest, record, hooks)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("append = %v, want %q wrapping %v", err, test.message, failure)
			}
		})
	}
}

func TestAppendCheckpointReadsTheBaseIdentityOnceAndRemembersIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	digest := cacheTestDigest("a")
	directory := checkpointDirectory(t, root, digest)
	stored := checkpoint.JSON(checkpointFixture(digest))
	if err := os.WriteFile(filepath.Join(directory, CheckpointFileName), stored, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	reads := 0
	hooks := storeHooks{read: func(name string) ([]byte, error) {
		reads++
		return os.ReadFile(name)
	}}
	store := New(root)
	for index := range 2 {
		record := checkpointJournalRecord{
			BaselineTarget: &checkpoint.BaselineTarget{ID: "target" + string(rune('a'+index))},
		}
		if err := store.appendCheckpointRecordWithHooks(digest, record, hooks); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Fatalf("checkpoint reads = %d, want the base identity read once and remembered", reads)
	}
}
func TestDeleteCheckpointRefusesAnInvalidDigestBeforeTouchingAnything(t *testing.T) {
	t.Parallel()
	if err := New(t.TempDir()).DeleteCheckpoint(""); err == nil || !strings.Contains(err.Error(), "invalid cache digest") {
		t.Fatalf("DeleteCheckpoint(\"\") = %v", err)
	}
}

func TestDeleteCheckpointAcceptsAnEntryThatIsAlreadyGone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := New(root).DeleteCheckpoint(cacheTestDigest("a")); err != nil {
		t.Fatalf("DeleteCheckpoint of an absent entry = %v", err)
	}
}

func TestDeleteCheckpointReportsWhatItCouldNotRemove(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		obstruct func(t *testing.T, directory string)
		message  string
	}{
		{
			name: "the checkpoint",
			obstruct: func(t *testing.T, directory string) {
				occupyAsDirectory(t, filepath.Join(directory, CheckpointFileName))
			},
			message: "remove checkpoint:",
		},
		{
			name: "its journal",
			obstruct: func(t *testing.T, directory string) {
				occupyAsDirectory(t, filepath.Join(directory, CheckpointJournalFileName))
			},
			message: "remove checkpoint journal:",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			digest := cacheTestDigest("a")
			directory := checkpointDirectory(t, root, digest)
			test.obstruct(t, directory)
			err := New(root).DeleteCheckpoint(digest)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("DeleteCheckpoint = %v, want %q", err, test.message)
			}
		})
	}
}

func occupyAsDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "occupant"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteCheckpointReportsADirectoryItCannotInspectOrRemove(t *testing.T) {
	t.Parallel()
	failure := errors.New("directory failure")
	for _, test := range []struct {
		name    string
		hooks   storeHooks
		message string
	}{
		{
			name:    "it cannot be inspected",
			hooks:   storeHooks{readDir: func(string) ([]os.DirEntry, error) { return nil, failure }},
			message: "inspect checkpoint directory",
		},
		{
			name: "it is empty and cannot be removed",
			hooks: storeHooks{remove: func(path string) error {
				if filepath.Base(path) == CheckpointFileName || filepath.Base(path) == CheckpointJournalFileName {
					return os.ErrNotExist
				}
				return failure
			}},
			message: "remove empty checkpoint directory",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			digest := cacheTestDigest("a")
			checkpointDirectory(t, root, digest)
			err := New(root).deleteCheckpointWithHooks(digest, test.hooks)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("DeleteCheckpoint = %v, want %q wrapping %v", err, test.message, failure)
			}
		})
	}
}

func TestDeleteCheckpointRemovesTheEntryDirectoryOnlyWhenItIsEmpty(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		companion string
		wantGone  bool
	}{
		{name: "nothing else is stored", wantGone: true},
		{name: "a report shares the entry", companion: "report.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			digest := cacheTestDigest("a")
			store := New(root)
			if err := store.PutCheckpoint(digest, checkpointFixture(digest)); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(root, "v1", digest)
			if test.companion != "" {
				if err := os.WriteFile(filepath.Join(directory, test.companion), []byte("{}"), filemode.ReadableFile); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.DeleteCheckpoint(digest); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(directory)
			if gone := errors.Is(err, os.ErrNotExist); gone != test.wantGone {
				t.Fatalf("entry directory gone = %t, want %t (%v)", gone, test.wantGone, err)
			}
		})
	}
}
