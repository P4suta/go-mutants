// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const helperVariable = "GOATEST_TESTKIT_HELPER_PROBE"

var errHelperStopped = errors.New("the recorded helper stopped")

func TestHelperArgvSelectsExactlyTheTestItNames(t *testing.T) {
	t.Parallel()
	argv := HelperArgv("TestSomething")
	if len(argv) != helperArgvLength || argv[0] != os.Args[0] || argv[1] != "-test.run=^TestSomething$" {
		t.Fatalf("HelperArgv = %q", argv)
	}
}

func TestHelperEnabledIsExactlyTheOneThatSelectsIt(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "1", want: true},
		{value: ""},
		{value: "0"},
		{value: "true"},
		{value: "11"},
	} {
		t.Run("value "+test.value, func(t *testing.T) {
			t.Setenv(helperVariable, test.value)
			if got := HelperEnabled(helperVariable); got != test.want {
				t.Fatalf("HelperEnabled with %q = %t, want %t", test.value, got, test.want)
			}
		})
	}
}

type recordingHelperTB struct {
	testing.TB
	fatals []string
}

func (recorder *recordingHelperTB) Helper() {}

func (recorder *recordingHelperTB) Fatalf(format string, arguments ...any) {
	recorder.fatals = append(recorder.fatals, strings.TrimSpace(format))
	panic(errHelperStopped)
}

func runningAsHelper(t *testing.T, variable, testName string) (result bool, fatals []string) {
	t.Helper()
	recorder := &recordingHelperTB{}
	defer func() {
		if recovered := recover(); recovered != nil && recovered != errHelperStopped {
			panic(recovered)
		}
		fatals = recorder.fatals
	}()
	result = runningAsHelperWith(recorder, variable, testName)
	return result, recorder.fatals
}

func TestRunningAsHelperAnswersForEveryShapeTheVariableCanHave(t *testing.T) {
	for _, test := range []struct {
		name     string
		set      bool
		value    string
		want     bool
		wantStop bool
	}{
		{name: "the one that selects it", set: true, value: "1", want: true},
		{name: "unset"},
		{name: "an absence written out", set: true, value: "0"},
		{name: "empty", set: true},
		{name: "anything else", set: true, value: "yes", wantStop: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.set {
				t.Setenv(helperVariable, test.value)
			} else {
				_ = os.Unsetenv(helperVariable)
			}
			got, fatals := runningAsHelper(t, helperVariable, "TestSomething")
			if stopped := len(fatals) != 0; stopped != test.wantStop {
				t.Fatalf("stopped = %t, want %t (%q)", stopped, test.wantStop, fatals)
			}
			if !test.wantStop && got != test.want {
				t.Fatalf("RunningAsHelper = %t, want %t", got, test.want)
			}
		})
	}
}

func TestRunningAsHelperAnswersForARealTest(t *testing.T) {
	t.Setenv(helperVariable, "1")
	if !RunningAsHelper(t, helperVariable, "TestSomething") {
		t.Fatal("RunningAsHelper denied the process the variable selected")
	}
	t.Setenv(helperVariable, "")
	if RunningAsHelper(t, helperVariable, "TestSomething") {
		t.Fatal("RunningAsHelper claimed a process the variable did not select")
	}
}
