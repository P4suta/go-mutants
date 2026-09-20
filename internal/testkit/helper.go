// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

const (
	HelperCoverRootEnv = "TESTKIT_HELPER_COVERDIR_ROOT"
	CoverDirEnv        = "GOCOVERDIR"
)

const helperCoverPrefix = "go-mutants-helper-cover-"

const HelperMisuse = 97

func HelperArgv(testName string) []string {
	return []string{TestBinary(), "-test.run=^" + regexp.QuoteMeta(testName) + "$"}
}

func TestBinary() string {
	exe, err := os.Executable()
	return testBinaryFrom(exe, err, os.Args[0])
}

func testBinaryFrom(exe string, err error, arg0 string) string {
	if err == nil {
		return exe
	}
	if absolute, absErr := filepath.Abs(arg0); absErr == nil {
		return absolute
	}
	return arg0
}

func HelperEnabled(variable string) bool {
	return os.Getenv(variable) != ""
}

func SkipUnlessHelper(t testing.TB, variable string) {
	t.Helper()

	if HelperEnabled(variable) {
		return
	}
	t.Skipf("%s is not set, so this process is not the helper it names", variable)
}

func Helper(m *testing.M, variable string, program func(args []string) int) int {
	if HelperEnabled(variable) {
		if err := isolateCoverageOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "helper: %v\n", err)
			return HelperMisuse
		}
		return program(os.Args[1:])
	}
	return runSuite(m)
}

func runSuite(m *testing.M) int {
	if !suitePublishesCoverRoot(testing.CoverMode()) {
		return m.Run()
	}

	root, err := os.MkdirTemp("", helperCoverPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating the helper coverage root: %v\n", err)
		return HelperMisuse
	}
	defer func() { _ = os.RemoveAll(root) }()

	if err := os.Setenv(HelperCoverRootEnv, root); err != nil {
		fmt.Fprintf(os.Stderr, "publishing the helper coverage root: %v\n", err)
		return HelperMisuse
	}
	return m.Run()
}

func suitePublishesCoverRoot(mode string) bool { return mode != "" }

func HelperCoverRoot() string { return os.Getenv(HelperCoverRootEnv) }

func isolateCoverageOutput() error {
	root := HelperCoverRoot()
	switch helperCoverAction(root, os.Getenv(CoverDirEnv)) {
	case coverNothing:
		return nil
	case coverRefuse:
		return fmt.Errorf("%s is unset, so this helper has nowhere private to write coverage output",
			HelperCoverRootEnv)
	case coverCarve:
	}
	dir := filepath.Join(root, strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Setenv(CoverDirEnv, dir)
}

type coverAction int

const (
	coverNothing coverAction = iota
	coverCarve
	coverRefuse
)

func (a coverAction) String() string {
	switch a {
	case coverCarve:
		return "carve a private coverage directory"
	case coverRefuse:
		return "refuse, with the misuse status"
	case coverNothing:
	}
	return "leave the coverage output alone"
}

func helperCoverAction(root, coverDir string) coverAction {
	switch {
	case root != "":
		return coverCarve
	case coverDir != "":
		return coverRefuse
	default:
		return coverNothing
	}
}
