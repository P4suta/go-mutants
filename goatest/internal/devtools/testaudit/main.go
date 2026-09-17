// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	initialLineBuffer = 64 * 1024
	maximumLineBuffer = 8 * 1024 * 1024

	ledgerName = "skip_ledger.txt"

	failureExitCode = 1
)

const NarrowedFilterMarker = "goatest-testaudit: narrowed test filter"

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

func run(input io.Reader, out, errorOut io.Writer) int {
	summary, err := audit(input)
	if err != nil {
		_, _ = fmt.Fprintln(errorOut, err)
		return failureExitCode
	}
	render(out, summary)

	allowed, err := readLedger(ledgerPath())
	if err != nil {
		_, _ = fmt.Fprintf(errorOut, "goatest: read %s: %v\n", ledgerName, err)
		return failureExitCode
	}

	failed := false
	if silent := summary.silentPackages(); len(silent) > 0 {
		_, _ = fmt.Fprintf(errorOut,
			"goatest: %d package(s) produced no verdict at all:\n  %s\n\n"+
				"Every test in them was skipped, which a non-verbose `go test` reports\n"+
				"as `ok`. A suite that stepped aside is not a suite that passed.\n",
			len(silent), strings.Join(silent, "\n  "))
		failed = true
	}
	if narrowed := summary.narrowedPackages(); len(narrowed) > 0 {
		_, _ = fmt.Fprintf(errorOut,
			"goatest: %d package(s) ran less than all of themselves:\n  %s\n\n"+
				"A test excluded before the run started is not a pass, a failure or a\n"+
				"skip, so the accounting balances over a suite that is missing most of\n"+
				"itself. Unset the variable, or run the suite that was asked for.\n",
			len(narrowed), strings.Join(narrowed, "\n  "))
		failed = true
	}
	if unrecorded := summary.unrecordedSkips(allowed); len(unrecorded) > 0 {
		_, _ = fmt.Fprintf(errorOut,
			"goatest: %d skip(s) are not recorded in %s:\n  %s\n\n"+
				"A skip is a test that did not run, and one nobody wrote down is one\n"+
				"nobody decided on. Record it with the reason, or stop skipping.\n",
			len(unrecorded), ledgerName, strings.Join(unrecorded, "\n  "))
		failed = true
	}
	if failed {
		return failureExitCode
	}
	return 0
}

func ledgerPath() string {
	if override := os.Getenv("GOATEST_TESTAUDIT_LEDGER"); override != "" {
		return override
	}
	return filepath.Join("internal", "devtools", "testaudit", ledgerName)
}

func readLedger(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var allowed []string
	for number, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != ledgerFieldCount {
			return nil, fmt.Errorf("%s:%d: want \"package Test\", got %q", path, number+1, trimmed)
		}
		allowed = append(allowed, fields[0]+" "+fields[1])
	}
	return allowed, nil
}

const ledgerFieldCount = 2
