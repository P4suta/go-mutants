// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func ledgerAt(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ledgerName)
	if err := os.WriteFile(path, []byte(contents), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	return path
}

func runAudit(t *testing.T, stream, ledger string) (int, string, string) {
	t.Helper()
	t.Setenv("GOATEST_TESTAUDIT_LEDGER", ledger)
	var out, errorOut bytes.Buffer
	code := run(strings.NewReader(stream), &out, &errorOut)
	return code, out.String(), errorOut.String()
}

func TestRunAcceptsASuiteThatRanAndRecordedEveryStep(t *testing.T) {
	code, out, errorOut := runAudit(t, events(t,
		"pass example/a TestOne",
		"skip example/a TestTwo",
	), ledgerAt(t, "example/a TestTwo\n"))
	if code != 0 || errorOut != "" {
		t.Fatalf("run = %d, stderr %q", code, errorOut)
	}
	if !strings.Contains(out, "1 passed, 0 failed, 1 skipped") {
		t.Fatalf("run wrote %q", out)
	}
}

func TestRunRefusesEverySuiteThatDidNotAnswerForItself(t *testing.T) {
	for _, test := range []struct {
		name    string
		stream  func(t *testing.T) string
		ledger  string
		message string
	}{
		{
			name:    "a package where everything stepped aside",
			stream:  func(t *testing.T) string { return events(t, "skip example/a TestOne") },
			ledger:  "example/a TestOne\n",
			message: "produced no verdict at all",
		},
		{
			name: "a package that ran less than itself",
			stream: func(t *testing.T) string {
				return `{"Action":"output","Package":"example/a","Output":"` + NarrowedFilterMarker + `\n"}` + "\n" +
					events(t, "pass example/a TestOne")
			},
			message: "ran less than all of themselves",
		},
		{
			name:    "a skip nobody wrote down",
			stream:  func(t *testing.T) string { return events(t, "pass example/a TestOne", "skip example/a TestTwo") },
			message: "are not recorded in " + ledgerName,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, _, errorOut := runAudit(t, test.stream(t), ledgerAt(t, test.ledger))
			if code != failureExitCode || !strings.Contains(errorOut, test.message) {
				t.Fatalf("run = %d, stderr %q, want %q", code, errorOut, test.message)
			}
		})
	}
}

func TestRunReportsAStreamAndALedgerItCannotRead(t *testing.T) {
	code, out, errorOut := runAudit(t, "not json\n", ledgerAt(t, ""))
	if code != failureExitCode || out != "" || !strings.Contains(errorOut, "is not JSON") {
		t.Fatalf("run over a stream that is not JSON = %d, stdout %q, stderr %q", code, out, errorOut)
	}
	ledger := filepath.Join(t.TempDir(), ledgerName)
	if err := os.MkdirAll(ledger, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	code, out, errorOut = runAudit(t, events(t, "pass example/a TestOne"), ledger)
	if code != failureExitCode || out == "" || !strings.Contains(errorOut, "goatest: read "+ledgerName) {
		t.Fatalf("run over a ledger it cannot read = %d, stdout %q, stderr %q", code, out, errorOut)
	}
}

func TestTheLedgerPathIsTheRepositoryOneUnlessTheEnvironmentNamesAnother(t *testing.T) {
	t.Setenv("GOATEST_TESTAUDIT_LEDGER", "")
	if got, want := ledgerPath(), filepath.Join("internal", "devtools", "testaudit", ledgerName); got != want {
		t.Fatalf("ledgerPath with no override = %q, want %q", got, want)
	}
	t.Setenv("GOATEST_TESTAUDIT_LEDGER", "/somewhere/else.txt")
	if got := ledgerPath(); got != "/somewhere/else.txt" {
		t.Fatalf("ledgerPath with an override = %q", got)
	}
}
