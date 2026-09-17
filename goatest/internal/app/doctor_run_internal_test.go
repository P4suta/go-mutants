// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const (
	doctorHelperVariable = "GOATEST_DOCTOR_HELPER"
	doctorHelperOutput   = "GOATEST_DOCTOR_HELPER_OUTPUT"
	doctorHelperCode     = "GOATEST_DOCTOR_HELPER_CODE"
	doctorHelperMisuse   = 97
)

func TestDoctorHelperProcess(t *testing.T) {
	script, selected := os.LookupEnv(doctorHelperVariable)
	if !selected {
		if _, stray := os.LookupEnv(doctorHelperOutput); stray {
			t.Fatalf("%s is set without %s, so this process was selected by half a contract",
				doctorHelperOutput, doctorHelperVariable)
		}
		return
	}
	_, _ = fmt.Fprint(os.Stdout, script)
	code, err := strconv.Atoi(os.Getenv(doctorHelperCode))
	if err != nil {
		code = doctorHelperMisuse
	}
	os.Exit(code)
}

type doctorAnswer struct {
	output string
	code   int
	start  error
}

type doctorTree struct{}

func (doctorTree) Kill() error  { return nil }
func (doctorTree) Close() error { return nil }

func scriptedDoctor(answer func(arguments []string) doctorAnswer) startDoctorProcess {
	return func(command *exec.Cmd) (doctorProcessTree, error) {
		reply := answer(command.Args)
		if reply.start != nil {
			return nil, reply.start
		}
		command.Path = os.Args[0]
		command.Args = []string{os.Args[0], "-test.run=^TestDoctorHelperProcess$"}
		command.Env = append(slices.Clone(command.Env),
			doctorHelperVariable+"="+reply.output,
			doctorHelperCode+"="+strconv.Itoa(reply.code))
		if err := command.Start(); err != nil {
			return nil, err
		}
		return doctorTree{}, nil
	}
}

func doctorListing(root string) string {
	encoded, err := json.Marshal(filepath.ToSlash(root))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(
		`{"ImportPath":"example.test/fixture","Dir":%s,"Module":{"Path":"example.test/fixture","Dir":%s},"Deps":["fmt"]}`,
		encoded, encoded) + "\n"
}

func healthyDoctor(root string) func([]string) doctorAnswer {
	return func(arguments []string) doctorAnswer {
		switch {
		case arguments[0] == "git":
			return doctorAnswer{output: "true\n"}
		case arguments[1] == "version":
			return doctorAnswer{output: "go version go1.26.6 darwin/arm64\n"}
		case arguments[1] == "env":
			return doctorAnswer{output: "/fixture/go.mod\n\n1\ndarwin\narm64\n"}
		case arguments[1] == "list" && slices.Contains(arguments, "-json"):
			return doctorAnswer{output: doctorListing(root)}
		}
		return doctorAnswer{}
	}
}

func doctorRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, contents := range map[string]string{
		".goatest.toml": "version = 1\ncontract = \"standard-v1\"\n\n[project]\npackages = [\"./...\"]\n",
		"go.mod":        "module example.test/fixture\n\ngo 1.26.0\n",
		"quiet.go":      "package fixture\n\nfunc Quiet() int { return 1 }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), filemode.ReadableFile); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func doctorEvidence(result report.Report, id string) (report.Evidence, bool) {
	for _, item := range result.Evidence {
		if item.ID == id {
			return item, true
		}
	}
	return report.Evidence{}, false
}

func runDoctor(t *testing.T, root string, answer func([]string) doctorAnswer) report.Report {
	t.Helper()
	service := Service{Root: root, GoBinary: "go", Environment: []string{}, doctorProcess: scriptedDoctor(answer)}
	result, err := service.doctor(t.Context(), root)
	if err != nil {
		t.Fatalf("the doctor reported an error rather than a verdict: %v", err)
	}
	return result
}

func TestTheDoctorReportsEveryCheckItCompleted(t *testing.T) {
	t.Parallel()
	root := doctorRoot(t)
	result := runDoctor(t, root, healthyDoctor(root))

	if result.Verdict != report.VerdictCompleted {
		t.Fatalf("a healthy repository was given the verdict %q, want %q: %+v",
			result.Verdict, report.VerdictCompleted, result.Findings)
	}
	for _, id := range []string{
		"config", "mutation-profile", "go-version", "module", "workspace", "cgo",
		"offline-dependencies", "behaviour-keys", "race-detector", "git",
		"writable-.goatest", "writable-reports", "disk",
	} {
		item, named := doctorEvidence(result, id)
		if !named {
			t.Errorf("the doctor recorded no evidence for %q", id)
			continue
		}
		if item.Status == "failed" {
			t.Errorf("the doctor failed %q on a healthy repository: %s", id, item.Detail)
		}
	}
	if len(result.Findings) != 0 {
		t.Errorf("a healthy repository produced findings: %+v", result.Findings)
	}
}

func TestTheDoctorNamesTheCheckThatFailedAndStopsThere(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		refuse func(root string, arguments []string) (doctorAnswer, bool)
		failed string
		kind   string
		absent []string
	}{
		{
			name: "a toolchain that cannot say its version",
			refuse: func(_ string, arguments []string) (doctorAnswer, bool) {
				return doctorAnswer{code: 1}, arguments[0] != "git" && arguments[1] == "version"
			},
			failed: "go-version", kind: "doctor-toolchain",
			absent: []string{"module", "offline-dependencies", "race-detector"},
		},
		{
			name: "a toolchain that cannot be started at all",
			refuse: func(_ string, arguments []string) (doctorAnswer, bool) {
				return doctorAnswer{start: os.ErrPermission}, arguments[0] != "git" && arguments[1] == "version"
			},
			failed: "go-version", kind: "doctor-toolchain",
		},
		{
			name: "an environment it cannot read",
			refuse: func(_ string, arguments []string) (doctorAnswer, bool) {
				return doctorAnswer{code: 1}, arguments[0] != "git" && arguments[1] == "env"
			},
			failed: "go-env", kind: "doctor-toolchain",
			absent: []string{"module"},
		},
		{
			name: "an environment that names nothing at all",
			refuse: func(_ string, arguments []string) (doctorAnswer, bool) {
				return doctorAnswer{}, arguments[0] != "git" && arguments[1] == "env"
			},
			failed: "module", kind: "doctor-workspace",
		},
		{
			name: "an environment whose module is the null device",
			refuse: func(_ string, arguments []string) (doctorAnswer, bool) {
				return doctorAnswer{output: os.DevNull + "\n\n1\ndarwin\narm64\n"},
					arguments[0] != "git" && arguments[1] == "env"
			},
			failed: "module", kind: "doctor-workspace",
		},
		{
			name: "dependencies it cannot resolve offline",
			refuse: func(_ string, arguments []string) (doctorAnswer, bool) {
				return doctorAnswer{code: 1}, arguments[0] != "git" && arguments[1] == "list" &&
					slices.Contains(arguments, "-deps")
			},
			failed: "offline-dependencies", kind: "doctor-dependency",
			absent: []string{"behaviour-keys"},
		},
		{
			name: "a package listing it cannot decode",
			refuse: func(_ string, arguments []string) (doctorAnswer, bool) {
				return doctorAnswer{output: "{"}, arguments[0] != "git" && arguments[1] == "list" &&
					slices.Contains(arguments, "-json")
			},
			failed: "behaviour-keys", kind: "doctor-dependency",
			absent: []string{"race-detector"},
		},
		{
			name: "a race detector that refuses to build",
			refuse: func(_ string, arguments []string) (doctorAnswer, bool) {
				return doctorAnswer{code: 1}, arguments[0] != "git" && arguments[1] == "test"
			},
			failed: "race-detector", kind: "doctor-race",
			absent: []string{"git"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := doctorRoot(t)
			healthy := healthyDoctor(root)
			result := runDoctor(t, root, func(arguments []string) doctorAnswer {
				if answer, refused := test.refuse(root, arguments); refused {
					return answer
				}
				return healthy(arguments)
			})

			if result.Verdict != report.VerdictError {
				t.Fatalf("a repository that fails %q was given the verdict %q, want %q",
					test.failed, result.Verdict, report.VerdictError)
			}
			item, named := doctorEvidence(result, test.failed)
			if !named || item.Status != "failed" {
				t.Fatalf("the doctor recorded %+v for %q, want it to have failed", item, test.failed)
			}
			if len(result.Findings) != 1 || result.Findings[0].Kind != test.kind {
				t.Fatalf("the doctor reported %+v, want one %q finding", result.Findings, test.kind)
			}
			for _, id := range test.absent {
				if _, reached := doctorEvidence(result, id); reached {
					t.Errorf("the doctor went on to check %q after %q failed", id, test.failed)
				}
			}
		})
	}
}

func TestTheDoctorRecordsAWorkTreeItCannotSeeAsALimitation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		answer doctorAnswer
	}{
		{name: "a git that refuses", answer: doctorAnswer{code: 1}},
		{name: "a git that says no", answer: doctorAnswer{output: "false\n"}},
		{name: "a git that is not there", answer: doctorAnswer{start: os.ErrNotExist}},
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

			if result.Verdict != report.VerdictCompleted {
				t.Fatalf("a repository outside a work tree was given the verdict %q, want %q",
					result.Verdict, report.VerdictCompleted)
			}
			item, named := doctorEvidence(result, "git")
			if !named || item.Status != "unavailable" {
				t.Fatalf("the doctor recorded %+v for git, want it unavailable", item)
			}
			if len(result.Limitations) != 1 ||
				result.Limitations[0].Code != report.LimitationGitMetadataUnavailable {
				t.Fatalf("the doctor recorded %+v, want the Git metadata limitation", result.Limitations)
			}
		})
	}
}

func TestTheDoctorSaysWhichPackagesWidenTheirBehaviourKey(t *testing.T) {
	t.Parallel()
	root := doctorRoot(t)
	if err := os.WriteFile(filepath.Join(root, "quiet.go"),
		[]byte("package fixture\n\nimport \"os\"\n\nfunc Read(path string) (string, error) { return os.Readlink(path) }\n"),
		filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	result := runDoctor(t, root, healthyDoctor(root))

	item, named := doctorEvidence(result, "behaviour-keys")
	if !named {
		t.Fatal("the doctor recorded no behaviour-key evidence")
	}
	if item.Status != "widened" {
		t.Fatalf("the doctor recorded %+v, want it to say the key was widened", item)
	}
	if !strings.Contains(item.Detail, "os.Readlink") {
		t.Errorf("the doctor said %q, want it to name the call that widened the key", item.Detail)
	}
}
