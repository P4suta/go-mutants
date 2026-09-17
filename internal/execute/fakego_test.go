// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

const fakePackage = "example.com/m/pkg"

func fakeBuild(t *testing.T) (*mutantkit.Fake, execute.Options) {
	t.Helper()
	f := mutantkit.FakeGo(t)
	snapshot := t.TempDir()
	f.ListJSON(mutantkit.ListPackage{
		ImportPath:  fakePackage,
		Dir:         filepath.Join(snapshot, "pkg"),
		TestGoFiles: []string{"pkg_test.go"},
	})
	opts := execute.Options{
		Toolchain:    gocmd.Toolchain{GoBin: f.Bin()},
		SnapshotRoot: snapshot,
		BinDir:       filepath.Join(testkit.Scratch(t), "bin"),
		Env:          f.Env(testkit.Compose(t, testkit.Scratch(t))),
		Jobs:         1,
		Timeout:      time.Minute,
	}
	return f, opts
}

const compilerOutput = "# " + fakePackage + " [" + fakePackage + ".test]\n" +
	"pkg/pkg_test.go:4:2: \"fmt\" imported and not used\n" +
	"pkg/pkg_test.go:9:2: declared and not used: total\n" +
	"pkg/pkg_test.go:10:5: undefined: helper\n"

func TestCompileFailureNamesThePackageAndQuotesTheDiagnostics(t *testing.T) {
	t.Parallel()

	f, opts := fakeBuild(t)
	f.On("test", "-c").Stderr(compilerOutput).Exit(1)

	binaries, err := execute.BuildTestBinaries(t.Context(), opts)
	if err == nil {
		t.Fatalf("BuildTestBinaries over a snapshot that does not compile = %+v, want an error", binaries)
	}
	if code := execute.CodeOf(err); code != execute.CodeTestBuildFailed {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, execute.CodeTestBuildFailed)
	}
	if !strings.Contains(err.Error(), fakePackage) {
		t.Errorf("Error() = %q, want it to name the package that would not build", err)
	}
	if want := "exited with status 1"; !strings.Contains(err.Error(), want) {
		t.Errorf("Error() = %q, want it to say %q", err, want)
	}

	var failure *execute.Error
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want an *execute.Error", err)
	}
	if failure.Package != fakePackage {
		t.Errorf("Package = %q, want %q: an argv is not a subject, and the binary is named after a digest",
			failure.Package, fakePackage)
	}
	if failure.ExitCode != 1 || failure.TimedOut {
		t.Errorf("ExitCode = %d, TimedOut = %v, want 1 and false", failure.ExitCode, failure.TimedOut)
	}
	for _, line := range strings.Split(strings.TrimSpace(compilerOutput), "\n") {
		if !strings.Contains(failure.RetainedOutput(), line) {
			t.Errorf("RetainedOutput() = %q, want it to quote %q", failure.RetainedOutput(), line)
		}
	}

	invocation := failure.Command()
	if invocation == nil {
		t.Fatal("Command() = nil, want the `go test -c` that failed")
	}
	if len(invocation.Argv) < 3 || invocation.Argv[0] != f.Bin() ||
		invocation.Argv[1] != "test" || invocation.Argv[2] != "-c" {
		t.Errorf("Argv = %q, want the scripted toolchain's `test -c`", invocation.Argv)
	}
	if invocation.Dir != opts.SnapshotRoot {
		t.Errorf("Dir = %q, want the snapshot root %q", invocation.Dir, opts.SnapshotRoot)
	}
}

func TestCompileCarriesVetOffAndListDoesNot(t *testing.T) {
	t.Parallel()

	f, opts := fakeBuild(t)
	f.On("test", "-c").CreateOutput()
	opts.Env = append(opts.Env, "GOFLAGS=-mod=readonly")

	binaries, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("BuildTestBinaries: %v\n%s", err, execute.OutputOf(err))
	}
	if len(binaries) != 1 {
		t.Fatalf("built %d binaries, want the one package the listing reported", len(binaries))
	}

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("the toolchain was called %d times, want a listing and one compile: %+v", len(calls), calls)
	}
	list, compile := calls[0], calls[1]

	wantList := []string{"list", "-json=ImportPath,Dir,TestGoFiles,XTestGoFiles", "./..."}
	if !slices.Equal(list.Argv, wantList) {
		t.Errorf("the listing received %q, want %q", list.Argv, wantList)
	}
	wantCompile := []string{"test", "-c", "-o", binaries[0].BinPath, fakePackage}
	if !slices.Equal(compile.Argv, wantCompile) {
		t.Errorf("the compile received %q, want %q", compile.Argv, wantCompile)
	}

	if got := compile.Env["GOFLAGS"]; !strings.Contains(got, gocmd.VetOff) {
		t.Errorf("the compile ran with GOFLAGS %q, want it to carry %s", got, gocmd.VetOff)
	}
	if got := compile.Env["GOFLAGS"]; !strings.Contains(got, "-mod=readonly") {
		t.Errorf("the compile ran with GOFLAGS %q, want the inherited value merged rather than replaced", got)
	}
	if got := list.Env["GOFLAGS"]; strings.Contains(got, gocmd.VetOff) {
		t.Errorf("the listing ran with GOFLAGS %q, want no %s: `go list` runs no vet pass", got, gocmd.VetOff)
	}
	if got := list.Env["GOWORK"]; got != "off" {
		t.Errorf("the listing ran with GOWORK %q, want %q so a workspace above the snapshot cannot "+
			"change the package set", got, "off")
	}
	for _, call := range calls {
		if value, ok := call.Env["GO_MUTANTS_ACTIVE"]; ok {
			t.Errorf("`go %s` ran with GO_MUTANTS_ACTIVE=%q, want it stripped", call.Argv[0], value)
		}
	}
	if !testkit.SamePath(compile.Dir, opts.SnapshotRoot) {
		t.Errorf("the compile ran in %q, want the snapshot root %q", compile.Dir, opts.SnapshotRoot)
	}
}

func TestBuildFailureDiagnosticsAreParsedFromRealProcessOutput(t *testing.T) {
	t.Parallel()

	const chatter = "go: downloading example.com/dep v1.2.3\n"
	f, opts := fakeBuild(t)
	f.On("test", "-c").Stdout(chatter).Stderr(compilerOutput).Exit(1)

	_, err := execute.BuildTestBinaries(t.Context(), opts)
	if err == nil {
		t.Fatal("BuildTestBinaries over a snapshot that does not compile = nil, want an error")
	}
	retained := execute.OutputOf(err)
	if retained == "" {
		t.Fatalf("OutputOf(err) is empty, want what the compiler printed: %v", err)
	}

	want := []string{
		strings.TrimSuffix(chatter, "\n"),
		"# " + fakePackage + " [" + fakePackage + ".test]",
		`pkg/pkg_test.go:4:2: "fmt" imported and not used`,
		"pkg/pkg_test.go:9:2: declared and not used: total",
		"pkg/pkg_test.go:10:5: undefined: helper",
	}
	lines := strings.Split(retained, "\n")
	if !slices.Equal(lines, want) {
		t.Fatalf("the retained output is\n\t%s\nwant\n\t%s",
			strings.Join(lines, "\n\t"), strings.Join(want, "\n\t"))
	}

	for _, line := range lines[2:] {
		file, rest, ok := strings.Cut(line, ":")
		if !ok || !strings.HasSuffix(file, ".go") {
			t.Errorf("the diagnostic %q does not begin with a file", line)
			continue
		}
		if fields := strings.SplitN(rest, ":", 3); len(fields) != 3 {
			t.Errorf("the diagnostic %q does not carry a line, a column and a message", line)
		}
	}
}

func TestToolchainCommandsPutTheLocatedToolchainFirstOnPath(t *testing.T) {
	t.Parallel()

	f, opts := fakeBuild(t)
	f.On("test", "-c").CreateOutput()
	elsewhere := t.TempDir()
	opts.Env = withPath(opts.Env, elsewhere)

	if _, err := execute.BuildTestBinaries(t.Context(), opts); err != nil {
		t.Fatalf("BuildTestBinaries: %v\n%s", err, execute.OutputOf(err))
	}

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("the toolchain was called %d times, want a listing and one compile: %v", len(calls), calls)
	}
	want := filepath.Dir(f.Bin()) + string(filepath.ListSeparator) + elsewhere
	for _, call := range calls {
		if got := call.Env["PATH"]; got != want {
			t.Errorf("`go %s` ran with PATH %q, want %q: the located toolchain's own directory first, "+
				"and everything the caller had after it", call.Argv[0], got, want)
		}
	}
}

func TestAScriptedCompileProducesABinaryTheSchedulerRuns(t *testing.T) {
	t.Parallel()

	f, opts := fakeBuild(t)
	f.On("test", "-c").CreateOutput()
	packageDir := filepath.Join(opts.SnapshotRoot, "pkg")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatalf("creating the package directory the binary runs in: %v", err)
	}

	const budget = 10 * time.Second
	deadline := "-test.timeout=" + (execute.InProcessTimeoutFactor * budget).String()
	f.On(deadline, execute.FailFastFlag, "-test.run=^TestCatchesIt$").
		Stdout("--- FAIL: TestCatchesIt\nFAIL\n").Exit(1)
	f.On(deadline, execute.FailFastFlag, "-test.run=^TestMissesIt$").Stdout("ok\n")

	binaries, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("BuildTestBinaries: %v\n%s", err, execute.OutputOf(err))
	}
	if len(binaries) != 1 {
		t.Fatalf("built %d binaries, want the one the listing reported", len(binaries))
	}

	for _, test := range []struct {
		name    string
		id      string
		args    []string
		outcome mutation.Outcome
	}{
		{"a suite that goes red kills it", "killed-mutant", []string{"-test.run=^TestCatchesIt$"}, mutation.OutcomeKilled},
		{"a suite that stays green does not", "living-mutant", []string{"-test.run=^TestMissesIt$"}, mutation.OutcomeSurvived},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempt := execute.RunOne(t.Context(), opts, execute.MutantRun{
				ID:       test.id,
				Timeout:  budget,
				Binaries: []int{0},
				Args:     test.args,
			}, binaries)
			if attempt.Err != nil {
				t.Fatalf("RunOne: %v", attempt.Err)
			}
			if attempt.Outcome != test.outcome {
				t.Errorf("outcome = %s, want %s (output: %s)", attempt.Outcome, test.outcome, attempt.Output)
			}
		})
	}

	var runs []mutantkit.Call
	for _, call := range f.Calls() {
		if len(call.Argv) > 0 && strings.HasPrefix(call.Argv[0], "-test.timeout=") {
			runs = append(runs, call)
		}
	}
	if len(runs) != 2 {
		t.Fatalf("the scheduler started %d test binaries, want one per mutant: %v", len(runs), f.Calls())
	}
	for i, id := range []string{"killed-mutant", "living-mutant"} {
		if runs[i].Argv[0] != deadline {
			t.Errorf("the target was given %q, want the supervisor's %q", runs[i].Argv[0], deadline)
		}
		if got := runs[i].Env["GO_MUTANTS_ACTIVE"]; got != id {
			t.Errorf("the target ran with GO_MUTANTS_ACTIVE=%q, want %q", got, id)
		}
		if !testkit.SamePath(runs[i].Dir, packageDir) {
			t.Errorf("the target ran in %q, want its own package directory %q", runs[i].Dir, packageDir)
		}
	}
}

func withPath(env []string, dir string) []string {
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, entry := range env {
		if key, _, ok := strings.Cut(entry, "="); ok && strings.EqualFold(key, "PATH") {
			if replaced {
				continue
			}
			entry, replaced = "PATH="+dir, true
		}
		out = append(out, entry)
	}
	if !replaced {
		out = append(out, "PATH="+dir)
	}
	return out
}
