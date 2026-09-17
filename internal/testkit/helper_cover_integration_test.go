// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package testkit

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const coverageWarning = "warning: GOCOVERDIR not set, no coverage data emitted"

func TestHelperUnderTheGocoverdirFlagKeepsItsChildrenQuiet(t *testing.T) {
	t.Parallel()

	binary := filepath.Join(t.TempDir(), "testkit.test")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := Exec(t, Root(t), Compose(t, Scratch(t)), GoBinary(t),
		"test", "-c", "-cover", "-o", binary, "./internal/testkit")
	RequireExit(t, build, 0, "compiling this package's own test binary with coverage")

	run := Scratch(t)
	profile := filepath.Join(run, "coverdata")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatalf("creating the directory the run leaves its coverage data in: %v", err)
	}

	argv := HelperArgv(gocoverdirProbeTest)
	argv[0] = binary
	argv = append(argv, "-test.gocoverdir="+profile)
	env := withEntries(withoutEntries(Compose(t, run), CoverDirEnv, HelperCoverRootEnv),
		gocoverdirProbeEnv+"=1")
	result := Exec(t, run, env, argv...)

	what := "this package's own instrumented test binary, run with -test.gocoverdir"
	RequireNoOutput(t, result, what, coverageWarning)
	root := publishedCoverRoot(t, result)
	switch {
	case root == "":
		t.Errorf("a suite whose binary is coverage-instrumented published no %s, so every helper "+
			"child of it writes its coverage data nowhere and says so on stderr", HelperCoverRootEnv)
	case !SamePath(filepath.Dir(root), run):
		t.Errorf("the coverage root is %q, want one carved out of the temporary directory the run "+
			"was given, %s", root, run)
	}
	requireCoverageDirectoriesIn(t, run, 0)
	RequireExit(t, result, 0, what)
}
