// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package evidence

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mutationFixture() MutationStore {
	return MutationStore{
		ModulePath: "example/module",
		Records: []MutationRecord{{
			MutantID: evidenceTestDigest("a"), Path: "value.go", Package: "example/module/pkg",
			Outcome: MutationOutcomeKilled, Provenance: "snapshot=" + evidenceTestDigest("f"),
			KilledBy: []TargetKey{{
				Package: "example/module/pkg", Name: "TestKills", Kind: "test", Key: evidenceTestDigest("1"),
			}},
		}},
	}
}

func TestSaveMutationPropagatesEverySerializationAndWriteStage(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"validate", "marshal", "unmarshal", "mkdir", "create", "write", "sync", "close"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			failure := errors.New(stage + " failure")
			file := &stubEvidenceFile{name: filepath.Join(root, "temporary")}
			creates := 0
			store := mutationFixture()
			hooks := mutationHooks{
				createTemporary: func(string, string) (evidenceWritableFile, error) {
					creates++
					return file, nil
				},
			}
			switch stage {
			case "validate":
				store.Records[0].Outcome = "flaky"
			case "marshal":
				hooks.marshalStore = func(any, string, string) ([]byte, error) { return nil, failure }
			case "unmarshal":
				hooks.unmarshalStore = func([]byte, any) error { return failure }
			case "mkdir":
				hooks.mkdirAll = func(string, os.FileMode) error { return failure }
			case "create":
				hooks.createTemporary = func(string, string) (evidenceWritableFile, error) {
					creates++
					return nil, failure
				}
			case "write":
				file.writeErr = failure
			case "sync":
				file.syncErr = failure
			case "close":
				file.closeErr = failure
			}
			err := saveMutationWithHooks(filepath.Join(root, "mutation.json"), store, hooks)
			if stage == "validate" {
				if err == nil || !strings.Contains(err.Error(), "is not a reusable outcome") {
					t.Fatalf("SaveMutation error = %v, want a validation refusal", err)
				}
				if creates != 0 {
					t.Fatalf("an inconsistent store reached createTemporary %d time(s)", creates)
				}
				return
			}
			if !errors.Is(err, failure) {
				t.Fatalf("SaveMutation error = %v, want %v", err, failure)
			}
		})
	}
}

func TestSaveMutationSyncsBeforeClosingExactlyOnce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := &stubEvidenceFile{name: filepath.Join(root, "temporary")}
	hooks := mutationHooks{
		createTemporary: func(string, string) (evidenceWritableFile, error) { return file, nil },
		rename:          func(string, string) error { return nil },
		remove:          func(string) error { return nil },
	}
	if err := saveMutationWithHooks(filepath.Join(root, "mutation.json"), mutationFixture(), hooks); err != nil {
		t.Fatal(err)
	}
	if file.writes != 1 || file.syncs != 1 || file.closes != 1 {
		t.Fatalf("writes/syncs/closes = %d/%d/%d, want 1/1/1", file.writes, file.syncs, file.closes)
	}

	closeFailure := errors.New("close failure")
	failing := &stubEvidenceFile{name: filepath.Join(root, "temporary"), closeErr: closeFailure}
	hooks.createTemporary = func(string, string) (evidenceWritableFile, error) { return failing, nil }
	if err := saveMutationWithHooks(filepath.Join(root, "mutation.json"), mutationFixture(), hooks); !errors.Is(err, closeFailure) {
		t.Fatalf("SaveMutation error = %v, want %v", err, closeFailure)
	}
	if failing.syncs != 1 || failing.closes != 1 {
		t.Fatalf("syncs/closes = %d/%d, want 1/1", failing.syncs, failing.closes)
	}
}

func TestSaveMutationRenameFallbackDistinguishesMissingAndRemovalFailures(t *testing.T) {
	t.Parallel()
	firstRename := errors.New("first rename")
	secondRename := errors.New("second rename")
	removeFailure := errors.New("remove destination")
	for _, testCase := range []struct {
		name       string
		removeErr  error
		secondErr  error
		want       error
		wantJoined error
		wantCalls  int
	}{
		{name: "replace", wantCalls: 2},
		{name: "missing-destination", removeErr: os.ErrNotExist, secondErr: secondRename, want: secondRename, wantCalls: 2},
		{name: "remove-failure", removeErr: removeFailure, want: firstRename, wantJoined: removeFailure, wantCalls: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			temporary := filepath.Join(root, "temporary")
			destination := filepath.Join(root, "mutation.json")
			file := &stubEvidenceFile{name: temporary}
			renames := 0
			hooks := mutationHooks{
				createTemporary: func(string, string) (evidenceWritableFile, error) { return file, nil },
				rename: func(oldPath, newPath string) error {
					if oldPath != temporary || newPath != destination {
						t.Fatalf("rename(%q, %q)", oldPath, newPath)
					}
					renames++
					if renames == 1 {
						return firstRename
					}
					return testCase.secondErr
				},
				remove: func(path string) error {
					if path == destination {
						return testCase.removeErr
					}
					return nil
				},
			}
			err := saveMutationWithHooks(destination, mutationFixture(), hooks)
			if testCase.want == nil {
				if err != nil {
					t.Fatalf("SaveMutation error = %v", err)
				}
			} else if !errors.Is(err, testCase.want) || testCase.wantJoined != nil && !errors.Is(err, testCase.wantJoined) {
				t.Fatalf("SaveMutation error = %v, want %v joined with %v", err, testCase.want, testCase.wantJoined)
			}
			if renames != testCase.wantCalls {
				t.Fatalf("rename calls = %d, want %d", renames, testCase.wantCalls)
			}
		})
	}
}

func TestLoadMutationReportsReadFailuresAndDecodeFailuresDistinctly(t *testing.T) {
	t.Parallel()
	failure := errors.New("read failure")
	readHooks := mutationHooks{
		readStore: func(string) ([]byte, error) {
			return []byte(`{"schema":"mutation-evidence-v1"}`), failure
		},
	}
	got, ok, err := loadMutationWithHooks("mutation.json", "example/module", readHooks)
	if !errors.Is(err, failure) || ok || !reflect.DeepEqual(got, MutationStore{}) {
		t.Fatalf("LoadMutation = %+v, ok %v, err %v", got, ok, err)
	}
	decodeHooks := mutationHooks{
		readStore: func(string) ([]byte, error) { return []byte("{"), nil },
	}
	got, ok, err = loadMutationWithHooks("mutation.json", "example/module", decodeHooks)
	if err == nil || !strings.Contains(err.Error(), "decode mutation evidence") || ok || !reflect.DeepEqual(got, MutationStore{}) {
		t.Fatalf("LoadMutation = %+v, ok %v, err %v", got, ok, err)
	}
}

func TestInspectMutationNamesTheIdentityAndTheValidityItRefused(t *testing.T) {
	t.Parallel()
	valid := mutationStoreBytes(t)
	for _, test := range []struct {
		name    string
		damage  func(store *MutationStore)
		problem string
	}{
		{name: "a store that is whole"},
		{
			name:    "another schema",
			damage:  func(store *MutationStore) { store.Schema = "goatest-mutation-evidence-v0" },
			problem: ErrMutationIdentityMismatch.Error(),
		},
		{
			name:    "no module path",
			damage:  func(store *MutationStore) { store.ModulePath = "" },
			problem: ErrMutationIdentityMismatch.Error(),
		},
		{
			name:    "a record that does not validate",
			damage:  func(store *MutationStore) { store.Records[0].Path = "" },
			problem: "requires a path",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := valid
			if test.damage != nil {
				store := mutationFaultStore()
				test.damage(&store)
				encoded, err := json.Marshal(store)
				if err != nil {
					t.Fatal(err)
				}
				data = encoded
			}
			hooks := mutationHooks{
				lstat:     func(string) (os.FileInfo, error) { return storedMutationInfo{size: int64(len(data))}, nil },
				readStore: func(string) ([]byte, error) { return data, nil },
			}
			status, err := inspectMutationWithHooks("mutation.json", hooks)
			if err != nil {
				t.Fatal(err)
			}
			if test.problem == "" {
				if !status.Valid || status.Problem != "" {
					t.Fatalf("status = %+v, want a store reported as whole", status)
				}
				return
			}
			if status.Valid || !strings.Contains(status.Problem, test.problem) {
				t.Fatalf("status = %+v, want a problem naming %q", status, test.problem)
			}
		})
	}
}

func TestFlushMutationCarriesEveryFailureOfTheInspectionsAround(t *testing.T) {
	t.Parallel()
	failure := errors.New("inspection failure")
	inspections := 0
	hooks := mutationHooks{
		lstat: func(string) (os.FileInfo, error) {
			inspections++
			if inspections == 1 {
				return nil, failure
			}
			return storedMutationInfo{}, nil
		},
	}
	if _, err := flushMutationWithHooks("mutation.json", hooks); !errors.Is(err, failure) {
		t.Fatalf("flush over an unreadable path = %v, want %v", err, failure)
	}

	data := mutationStoreBytes(t)
	inspections = 0
	hooks = mutationHooks{
		lstat: func(string) (os.FileInfo, error) {
			inspections++
			if inspections == 1 {
				return storedMutationInfo{size: int64(len(data))}, nil
			}
			return nil, failure
		},
		readStore: func(string) ([]byte, error) { return data, nil },
		remove:    func(string) error { return nil },
	}
	result, err := flushMutationWithHooks("mutation.json", hooks)
	if !errors.Is(err, failure) || !result.Removed {
		t.Fatalf("flush = (%+v, %v), want the removal kept and the second inspection reported", result, err)
	}
}

type storedMutationInfo struct {
	size int64
}

func (info storedMutationInfo) Name() string       { return "mutation.json" }
func (info storedMutationInfo) Size() int64        { return info.size }
func (info storedMutationInfo) Mode() os.FileMode  { return 0 }
func (info storedMutationInfo) ModTime() time.Time { return time.Time{} }
func (info storedMutationInfo) IsDir() bool        { return false }
func (info storedMutationInfo) Sys() any           { return nil }

const faultDigestWidth = 64

func mutationFaultStore() MutationStore {
	return MutationStore{
		Schema: MutationSchemaV1, ModulePath: "example/module",
		Records: []MutationRecord{{
			MutantID: strings.Repeat("a", faultDigestWidth), Path: "value.go", Package: "example/module/pkg",
			Outcome: MutationOutcomeKilled, Provenance: "snapshot=" + strings.Repeat("f", faultDigestWidth),
			KilledBy: []TargetKey{{
				Package: "example/module/pkg", Name: "TestKills", Kind: "test", Key: strings.Repeat("1", faultDigestWidth),
			}},
		}},
	}
}

func mutationStoreBytes(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(mutationFaultStore())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSaveMutationValidatesTheStoreItWasGivenAndTheOneItEncoded(t *testing.T) {
	t.Parallel()
	valid := mutationFaultStore()
	invalid := mutationFaultStore()
	invalid.Records[0].Path = ""
	for _, test := range []struct {
		name     string
		offered  MutationStore
		restored MutationStore
		wrote    bool
	}{
		{name: "a store that does not validate is refused before it is encoded", offered: invalid, restored: valid},
		{name: "a store that does not survive its own encoding is refused", offered: valid, restored: invalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			wrote := false
			hooks := mutationHooks{
				unmarshalStore: func([]byte, any) error { return nil },
				createTemporary: func(string, string) (evidenceWritableFile, error) {
					wrote = true
					return nil, errors.New("a refused store reached the filesystem")
				},
			}
			hooks.unmarshalStore = func(_ []byte, value any) error {
				store, ok := value.(*MutationStore)
				if !ok {
					t.Fatalf("unmarshal target = %T", value)
				}
				*store = test.restored
				return nil
			}
			err := saveMutationWithHooks(filepath.Join(t.TempDir(), "mutation.json"), test.offered, hooks)
			if err == nil || !strings.Contains(err.Error(), "requires a path and a package") {
				t.Fatalf("SaveMutation = %v, want the invalid store refused", err)
			}
			if wrote != test.wrote {
				t.Fatalf("reached the filesystem = %t, want %t", wrote, test.wrote)
			}
		})
	}
}
