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

// coverageWarning is what a coverage-instrumented process prints on its way out
// when it has nowhere to write.
//
// It is the whole subject of the test below, because it is a line that appears
// on a stream somebody else is reading: internal/gocmd asserts the exact bytes
// a scripted `go` printed, and a warning appended to them is a test that fails
// for a reason that has nothing to do with the code under test.
const coverageWarning = "warning: GOCOVERDIR not set, no coverage data emitted"

// TestHelperUnderTheGocoverdirFlagKeepsItsChildrenQuiet runs this package's own
// suite the way go-mutants runs a mutant's test binary, which is the one way
// nothing else here runs it.
//
// A mutant's test binary is built with `go test -c -cover` and told where to
// leave its coverage data with the `-test.gocoverdir` *flag*, deliberately not
// with GOCOVERDIR: a test binary emits through testing's coverTearDown, which
// is handed the flag's value and does not read the variable — see
// internal/execute's coverDirFlag. Since a child no longer inherits a parent's
// GOCOVERDIR either, the variable is simply absent from such a run while the
// binary is instrumented all the same.
//
// That is the shape [runSuite] used to get wrong, and it could not have been
// caught anywhere else: every other test in this package runs under a `go test`
// the developer typed, which either exports GOCOVERDIR or instruments nothing.
// Reading the variable therefore answered "there is no coverage to keep apart"
// for a binary that was full of it, no private root was published, and every
// helper child left through its exit hook printing [coverageWarning] onto
// stderr. In a dogfood run that made 91 of internal/gocmd's 104 mutants look
// killed by a mutation none of them had anything to do with.
//
// So this compiles the binary and runs it, and asserts the two halves of the
// rule: the child's output is exactly what the child wrote, and the private
// root was made under the run's own temporary directory and taken away again
// with the suite that made it.
func TestHelperUnderTheGocoverdirFlagKeepsItsChildrenQuiet(t *testing.T) {
	t.Parallel()

	// The binary is out of the kept scratch on purpose: it is this test binary
	// again, several megabytes of it, and what a reader of a failure needs is
	// the run's output and its temporary directory rather than a copy of the
	// program that produced them.
	binary := filepath.Join(t.TempDir(), "testkit.test")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := Exec(t, Root(t), Compose(t, Scratch(t)), GoBinary(t),
		"test", "-c", "-cover", "-o", binary, "./internal/testkit")
	RequireExit(t, build, 0, "compiling this package's own test binary with coverage")

	// The run's own temporary directory, which is where a private root is
	// carved out of and which nothing else writes to — so whatever is left in it
	// afterwards is this run's doing and nobody else's.
	run := Scratch(t)
	profile := filepath.Join(run, "coverdata")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatalf("creating the directory the run leaves its coverage data in: %v", err)
	}

	// Through [HelperArgv], so that the anchors and the escaping are written
	// down in one place; only the binary differs, because the child here is the
	// one just compiled rather than the one running this test.
	argv := HelperArgv(gocoverdirProbeTest)
	argv[0] = binary
	argv = append(argv, "-test.gocoverdir="+profile)
	// Both variables are cleared rather than assumed absent: this suite may
	// itself be running under `go test -cover`, and either one inherited would
	// hand the child the answer it is supposed to work out for itself.
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
