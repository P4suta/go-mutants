// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	KeepEnv      = "GO_MUTANTS_TEST_KEEP"
	KeepDirEnv   = "GO_MUTANTS_TEST_KEEP_DIR"
	VerboseEnv   = "GO_MUTANTS_TEST_VERBOSE"
	ForceFailEnv = "GO_MUTANTS_TEST_FORCE_FAIL"
)

const (
	harnessDirName    = "go-mutants-test"
	buildCacheDirName = "go-build"
	keptDirName       = "kept"

	KeptMarker = ".go-mutants-kept"
)

type Keep int

const (
	KeepNever Keep = iota
	KeepOnFailure
	KeepAlways
)

func (k Keep) String() string {
	switch k {
	case KeepOnFailure:
		return "on-failure"
	case KeepAlways:
		return "always"
	case KeepNever:
	}
	return "never"
}

var (
	pinnedKeep      = os.Getenv(KeepEnv)
	pinnedKeepDir   = os.Getenv(KeepDirEnv)
	pinnedVerbose   = os.Getenv(VerboseEnv)
	pinnedForceFail = os.Getenv(ForceFailEnv)
)

var forcedAny atomic.Bool

func KeepPolicy() Keep {
	validatePinnedKeep()
	return keepPolicyOf(harnessSetting(KeepEnv, pinnedKeep))
}

var validatePinnedKeep = sync.OnceFunc(func() {
	if pinnedKeep != "" {
		keepPolicyOf(pinnedKeep)
	}
})

func keepPolicyOf(value string) Keep {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "n", "off", "disabled":
		return KeepNever
	case "1", "true", "yes", "y", "on", "enabled", "failed", "on-failure":
		return KeepOnFailure
	case "always":
		return KeepAlways
	default:
		panic(fmt.Sprintf("%s=%s is not a keep policy: write nothing, 0, false, no or off to keep "+
			"nothing, one of 1, true, yes, on, enabled, failed or on-failure to keep what a failing "+
			"test left, or always to keep everything", KeepEnv, value))
	}
}

func Verbose() bool { return truthy(harnessSetting(VerboseEnv, pinnedVerbose)) }

func Keeping(t testing.TB) bool {
	switch policyFor(t) {
	case KeepAlways:
		return true
	case KeepOnFailure:
		return t.Failed()
	case KeepNever:
	}
	return false
}

func KeepRoot() (string, error) {
	named := harnessSetting(KeepDirEnv, pinnedKeepDir)
	if named != "" {
		if !filepath.IsAbs(named) {
			return "", fmt.Errorf("%s=%s is not an absolute path, and a test that started elsewhere "+
				"would file its evidence against its own working directory", KeepDirEnv, named)
		}
		return filepath.Clean(named), nil
	}
	if pinned.userCache == "" {
		return "", errors.New("this platform has no user cache directory, so " + KeepDirEnv + " has to name one")
	}
	return filepath.Join(pinned.userCache, harnessDirName, keptDirName), nil
}

func ForceFail(t testing.TB) {
	t.Helper()
	name := harnessSetting(ForceFailEnv, pinnedForceFail)
	if name == "" || name != t.Name() {
		return
	}
	l := ledgerFor(t)
	l.mu.Lock()
	already := l.forced
	l.forced = true
	l.mu.Unlock()
	if already {
		return
	}
	forcedAny.Store(true)
	t.Errorf("forced failure by %s=%s", ForceFailEnv, name)
}

func warnIfNothingWasForced() {
	name := harnessSetting(ForceFailEnv, pinnedForceFail)
	if name == "" || forcedAny.Load() {
		return
	}
	fmt.Fprintf(os.Stderr, "testkit: %s=%s matched no test that reached this harness. The hook fires "+
		"from the constructors here, so a test that resolves no fixture, toolchain, module or scratch "+
		"directory cannot be forced — and has nothing to keep either.\n", ForceFailEnv, name)
}

func stampKeptRoot(t testing.TB, dir string) {
	t.Helper()
	stamped, err := stampHarnessDirectory(dir, KeptMarker, keptMarkerBody)
	switch {
	case err != nil:
		t.Fatalf("creating the kept scratch root %s: %v", dir, err)
	case !stamped:
		t.Logf("testkit: %s already holds files that are not this harness's, so it was not stamped "+
			"as the kept scratch root and `mise run test-clean` will refuse to empty it. Point %s at "+
			"a directory of its own if that is not what you meant.", dir, KeepDirEnv)
	}
}

const keptMarkerBody = "This directory is the go-mutants test harness's kept scratch root.\n" +
	"It holds what a failing test left behind, one directory per test, each with a KEPT.txt\n" +
	"naming the test, the fixture, the toolchain and the commands it ran.\n" +
	"`mise run test-cache-status` reports it; `mise run test-clean` empties it.\n" +
	"Nothing in it is precious: it is evidence of runs that have already finished.\n" +
	"This file is also what tells the collector the directory is safe to remove,\n" +
	"which is why nothing removes a directory that does not have one.\n"
