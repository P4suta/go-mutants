// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

func TestProbingAWritableDirectoryLeavesTheTreeAsItFoundIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	made := filepath.Join(root, "made")
	if err := probeWritableDirectory(doctorProbeFilesystem{}, made); err != nil {
		t.Fatalf("probing a directory that is not there reported %v", err)
	}
	if _, err := os.Stat(made); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the probe left the directory it made behind: %v", err)
	}
	standing := filepath.Join(root, "standing")
	if err := os.MkdirAll(standing, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := probeWritableDirectory(doctorProbeFilesystem{}, standing); err != nil {
		t.Fatalf("probing a directory that is already there reported %v", err)
	}
	if _, err := os.Stat(standing); err != nil {
		t.Errorf("the probe removed a directory it did not make: %v", err)
	}
	entries, err := os.ReadDir(standing)
	if err != nil || len(entries) != 0 {
		t.Errorf("the probe left %d entries behind: %v", len(entries), err)
	}
}

func TestProbingAWritableDirectoryReportsEveryStepItCouldNotTake(t *testing.T) {
	t.Parallel()
	failure := errors.New("the step failed")
	for _, test := range []struct {
		name    string
		hooks   doctorProbeFilesystem
		removed []string
	}{
		{
			name: "a directory it cannot inspect",
			hooks: doctorProbeFilesystem{
				Stat: func(string) (os.FileInfo, error) { return nil, failure },
			},
		},
		{
			name: "a directory it cannot make",
			hooks: doctorProbeFilesystem{
				Stat:     func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
				MkdirAll: func(string, os.FileMode) error { return failure },
			},
		},
		{
			name: "a directory it made but cannot write to",
			hooks: doctorProbeFilesystem{
				Stat:      func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
				MkdirAll:  func(string, os.FileMode) error { return nil },
				WriteFile: func(string, []byte, os.FileMode) error { return failure },
			},
			removed: []string{"directory"},
		},
		{
			name: "a probe it cannot remove",
			hooks: doctorProbeFilesystem{
				Stat:      func(string) (os.FileInfo, error) { return nil, nil },
				WriteFile: func(string, []byte, os.FileMode) error { return nil },
				Remove:    func(string) error { return failure },
			},
		},
		{
			name: "a directory it made but cannot remove",
			hooks: doctorProbeFilesystem{
				Stat:      func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
				MkdirAll:  func(string, os.FileMode) error { return nil },
				WriteFile: func(string, []byte, os.FileMode) error { return nil },
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			removed := []string{}
			hooks := test.hooks
			if hooks.Remove == nil {
				calls := 0
				hooks.Remove = func(path string) error {
					calls++
					removed = append(removed, filepath.Base(path))
					if calls > 1 {
						return failure
					}
					return nil
				}
			}
			if err := probeWritableDirectory(hooks, filepath.Join(t.TempDir(), "directory")); !errors.Is(err, failure) {
				t.Fatalf("probing %s reported %v, want %v", test.name, err, failure)
			}
			for _, want := range test.removed {
				if !slices.Contains(removed, want) {
					t.Errorf("probing %s removed %q, want it to take back the %q it made",
						test.name, removed, want)
				}
			}
		})
	}
}

func TestAProviderCommandIsLookedUpOrReadFromTheRepository(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runnable := filepath.Join(root, "runnable")
	if err := os.WriteFile(runnable, []byte("#!/bin/sh\n"), filemode.AnyExecute|filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(root, "plain")
	if err := os.WriteFile(plain, nil, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "directory")
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		command string
		want    string
	}{
		{name: "a name on the path", command: "go"},
		{name: "a name nothing on the path answers", command: "goatest-no-such-command", want: "executable file not found"},
		{name: "a path inside the repository", command: "./runnable"},
		{name: "an absolute path", command: runnable},
		{name: "a path that is not there", command: "./absent", want: "no such file"},
		{name: "a path that is a directory", command: "./directory", want: "not a regular file"},
		{name: "a path nothing can run", command: "./plain", want: "not executable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := doctorProviderCommand(root, test.command)
			if test.want == "" {
				if err != nil {
					t.Fatalf("a provider named by %s reported %v, want nothing", test.name, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("a provider named by %s reported %v, want %q", test.name, err, test.want)
			}
		})
	}
}

func TestTheDoctorStopsAtADirectoryItCannotWriteTo(t *testing.T) {
	t.Parallel()
	failure := errors.New("the directory could not be written to")
	root := doctorRoot(t)
	service := Service{
		Root: root, GoBinary: "go", Environment: []string{},
		doctorProcess: scriptedDoctor(healthyDoctor(root)),
		doctorFilesystem: doctorProbeFilesystem{
			WriteFile: func(string, []byte, os.FileMode) error { return failure },
		},
	}
	result, err := service.doctor(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	item, named := doctorEvidence(result, "writable-.goatest")
	if !named || item.Status != "failed" {
		t.Fatalf("the doctor recorded %+v, want the directory it could not write to failed", item)
	}
	if _, reached := doctorEvidence(result, "disk"); reached {
		t.Errorf("the doctor went on to the disk after a directory it could not write to")
	}
}

func TestTheDoctorReadsGitsAnswerOnlyWhereGitSucceeded(t *testing.T) {
	t.Parallel()
	root := doctorRoot(t)
	healthy := healthyDoctor(root)
	result := runDoctor(t, root, func(arguments []string) doctorAnswer {
		if arguments[0] == "git" {
			return doctorAnswer{output: "true\n", code: 1}
		}
		return healthy(arguments)
	})
	item, named := doctorEvidence(result, "git")
	if !named || item.Status != "unavailable" {
		t.Fatalf("the doctor recorded %+v for a git that failed while saying true, want it unavailable", item)
	}
}

func TestTheDoctorCarriesNoEmptyBuildTagFlag(t *testing.T) {
	t.Parallel()
	root := doctorRoot(t)
	recorded := &[][]string{}
	service := Service{Root: root, GoBinary: "go", Environment: []string{},
		doctorProcess: scriptedDoctor(recordingDoctor(root, recorded))}
	if _, err := service.doctor(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range *recorded {
		for _, argument := range arguments {
			if strings.HasPrefix(argument, "-tags=") {
				t.Errorf("a project that names no build tag ran %q", arguments)
			}
		}
	}
}

func TestTheDoctorRefusesAListingItCouldNotReadWhole(t *testing.T) {
	t.Parallel()
	root := doctorRoot(t)
	healthy := healthyDoctor(root)
	result := runDoctor(t, root, func(arguments []string) doctorAnswer {
		if arguments[0] != "git" && arguments[1] == "list" && slices.Contains(arguments, "-json") {
			return doctorAnswer{output: doctorListing(root) + doctorTruncationNotice + "\n"}
		}
		return healthy(arguments)
	})
	if result.Verdict != report.VerdictError {
		t.Fatalf("a listing that says it was cut short was accepted: %q", result.Verdict)
	}
	item, named := doctorEvidence(result, "behaviour-keys")
	if !named || item.Status != "failed" {
		t.Fatalf("the doctor recorded %+v, want the listing it could not read whole failed", item)
	}
}
