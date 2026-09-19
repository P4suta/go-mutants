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

func recordingDoctor(root string, recorded *[][]string) func([]string) doctorAnswer {
	healthy := healthyDoctor(root)
	return func(arguments []string) doctorAnswer {
		*recorded = append(*recorded, slices.Clone(arguments))
		return healthy(arguments)
	}
}

func doctorWithConfig(t *testing.T, contents string) (root string, recorded *[][]string) {
	t.Helper()
	root = doctorRoot(t)
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"), []byte(contents), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	recorded = &[][]string{}
	return root, recorded
}

func commandWith(recorded [][]string, word string) []string {
	for _, arguments := range recorded {
		if slices.Contains(arguments, word) {
			return arguments
		}
	}
	return nil
}

func TestTheDoctorRunsTheGoItWasGivenAndTheGitOnThePath(t *testing.T) {
	t.Parallel()
	for _, binary := range []string{"", "/usr/local/bin/go"} {
		root := doctorRoot(t)
		recorded := &[][]string{}
		service := Service{
			Root: root, GoBinary: binary, Environment: []string{},
			doctorProcess: scriptedDoctor(recordingDoctor(root, recorded)),
		}
		if _, err := service.doctor(t.Context(), root); err != nil {
			t.Fatal(err)
		}
		want := binary
		if want == "" {
			want = "go"
		}
		version := commandWith(*recorded, "version")
		if version == nil || version[0] != want {
			t.Errorf("the doctor ran %q for its version, want %q", version, want)
		}
		if git := commandWith(*recorded, "rev-parse"); git == nil || git[0] != "git" {
			t.Errorf("the doctor ran %q to ask about the work tree, want git", git)
		}
	}
}

func TestTheDoctorListsWhatTheProjectNamesOrEverythingUnderIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		project  string
		packages []string
	}{
		{name: "a project that names its packages", project: "[project]\npackages = [\"./one\", \"./two\"]\n",
			packages: []string{"./one", "./two"}},
		{name: "a project that names none", packages: []string{"./..."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, recorded := doctorWithConfig(t,
				"version = 1\ncontract = \"standard-v1\"\n\n"+test.project)
			service := Service{Root: root, GoBinary: "go", Environment: []string{},
				doctorProcess: scriptedDoctor(recordingDoctor(root, recorded))}
			if _, err := service.doctor(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			listing := commandWith(*recorded, "-deps")
			if listing == nil {
				t.Fatalf("the doctor listed nothing: %q", *recorded)
			}
			tail := listing[len(listing)-len(test.packages):]
			if !slices.Equal(tail, test.packages) {
				t.Fatalf("the doctor listed %q, want it to end with %q", listing, test.packages)
			}
		})
	}
}

func TestTheDoctorCarriesTheBuildTagsAndTestArgumentsOnlyWhereThereAreSome(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		execution string
		tags      bool
		arguments bool
	}{
		{name: "an execution that names neither"},
		{
			name:      "an execution that names build tags",
			execution: "[execution]\nbuild_tags = [\"integration\", \"slow\"]\n", tags: true,
		},
		{
			name:      "an execution that names test arguments",
			execution: "[execution]\ntest_binary_args = [\"-short\"]\n", arguments: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, recorded := doctorWithConfig(t,
				"version = 1\ncontract = \"standard-v1\"\n\n[project]\npackages = [\"./...\"]\n\n"+test.execution)
			service := Service{Root: root, GoBinary: "go", Environment: []string{},
				doctorProcess: scriptedDoctor(recordingDoctor(root, recorded))}
			if _, err := service.doctor(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			listing, race := commandWith(*recorded, "-deps"), commandWith(*recorded, "-race")
			if listing == nil || race == nil {
				t.Fatalf("the doctor ran %q, want a listing and a race build", *recorded)
			}
			for _, arguments := range [][]string{listing, race} {
				if carried := slices.Contains(arguments, "-tags=integration,slow"); carried != test.tags {
					t.Errorf("%q carries the build tags=%t, want %t", arguments, carried, test.tags)
				}
			}
			if carried := slices.Contains(race, "-args"); carried != test.arguments {
				t.Errorf("the race build %q carries test arguments=%t, want %t", race, carried, test.arguments)
			}
			if slices.Contains(listing, "-args") {
				t.Errorf("the listing %q carries test arguments, which belong to a test binary", listing)
			}
		})
	}
}

func TestTheDoctorSaysWhetherCgoIsOn(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		enabled string
		want    string
	}{
		{name: "cgo that is on", enabled: "1", want: "ready"},
		{name: "cgo that is off", enabled: "0", want: "disabled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := doctorRoot(t)
			healthy := healthyDoctor(root)
			result := runDoctor(t, root, func(arguments []string) doctorAnswer {
				if arguments[0] != "git" && arguments[1] == "env" {
					return doctorAnswer{output: "/fixture/go.mod\n\n" + test.enabled + "\ndarwin\narm64\n"}
				}
				return healthy(arguments)
			})
			item, named := doctorEvidence(result, "cgo")
			if !named || item.Status != test.want {
				t.Fatalf("the doctor recorded %+v for %s, want status %q", item, test.name, test.want)
			}
			if !strings.Contains(item.Detail, "CGO_ENABLED="+test.enabled) {
				t.Errorf("the doctor said %q, want it to name what it read", item.Detail)
			}
		})
	}
}

func TestTheDoctorRecordsGitOnlyWhereItAnswersFromInsideAWorkTree(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		answer doctorAnswer
		want   string
	}{
		{name: "a work tree git knows", answer: doctorAnswer{output: "true\n"}, want: "ready"},
		{name: "a directory git does not track", answer: doctorAnswer{output: "false\n"}, want: "unavailable"},
		{name: "a git that answers nothing", answer: doctorAnswer{}, want: "unavailable"},
		{name: "a git that fails", answer: doctorAnswer{code: 1}, want: "unavailable"},
		{name: "a git that cannot be started", answer: doctorAnswer{start: os.ErrPermission}, want: "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := doctorRoot(t)
			healthy := healthyDoctor(root)
			result := runDoctor(t, root, func(arguments []string) doctorAnswer {
				if arguments[0] == "git" {
					return test.answer
				}
				return healthy(arguments)
			})
			item, named := doctorEvidence(result, "git")
			if !named || item.Status != test.want {
				t.Fatalf("the doctor recorded %+v for %s, want status %q", item, test.name, test.want)
			}
			limited := slices.ContainsFunc(result.Limitations, func(limitation report.Limitation) bool {
				return limitation.Code == report.LimitationGitMetadataUnavailable
			})
			if limited != (test.want == "unavailable") {
				t.Errorf("the doctor recorded a git limitation=%t for %s", limited, test.name)
			}
		})
	}
}

func TestTheDoctorChecksAProviderOnlyWhereOneIsNamed(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		config  string
		checked string
	}{
		{name: "a project that names no provider at all"},
		{
			name:    "a project that names a resource provider",
			config:  "[resources.database]\ncommand = [\"./absent-provider\"]\n",
			checked: "resource-provider-database",
		},
		{
			name:    "a project that names a generation provider",
			config:  "[generation]\ncommand = [\"./absent-provider\"]\n",
			checked: "generation-provider",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, _ := doctorWithConfig(t,
				"version = 1\ncontract = \"standard-v1\"\n\n[project]\npackages = [\"./...\"]\n\n"+test.config)
			result := runDoctor(t, root, healthyDoctor(root))
			item, named := doctorEvidence(result, test.checked)
			if test.checked == "" {
				if result.Verdict != report.VerdictCompleted {
					t.Fatalf("a project that names no provider was given %q", result.Verdict)
				}
				return
			}
			if !named || item.Status != "failed" {
				t.Fatalf("the doctor recorded %+v for a provider that is not there, want it failed", item)
			}
		})
	}
}

func TestTheDoctorRefusesADiskItCannotMeasureOrOneWithNoRoomLeft(t *testing.T) {
	t.Parallel()
	failure := errors.New("the filesystem could not be measured")
	for _, test := range []struct {
		name string
		free uint64
		err  error
		id   string
	}{
		{name: "room to spare", free: doctorMinimumFreeBytes + 1},
		{name: "exactly the room it asks for", free: doctorMinimumFreeBytes},
		{name: "one byte short", free: doctorMinimumFreeBytes - 1, id: "disk-capacity"},
		{name: "no room at all", id: "disk-capacity"},
		{name: "a disk it cannot measure", err: failure, id: "disk"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := doctorRoot(t)
			service := Service{
				Root: root, GoBinary: "go", Environment: []string{},
				doctorProcess:  scriptedDoctor(healthyDoctor(root)),
				doctorDiskFree: func(string) (uint64, error) { return test.free, test.err },
			}
			result, err := service.doctor(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			if test.id == "" {
				if result.Verdict != report.VerdictCompleted {
					t.Fatalf("a disk with %s was given %q: %+v", test.name, result.Verdict, result.Findings)
				}
				return
			}
			item, named := doctorEvidence(result, test.id)
			if !named || item.Status != "failed" {
				t.Fatalf("the doctor recorded %+v for %s, want %q failed", item, test.name, test.id)
			}
		})
	}
}
