// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The build phase against a real process.
//
// build_test.go injects a runner and asserts on the [runner.Spec] values this
// package produced, which is the right way to test a policy — which packages are
// compiled, in what order, under what names — and cannot test the one thing that
// only exists once a process has run: the argv and the environment a child
// really received, and the bytes a failing compiler really wrote.
//
// Those are what these tests are about, and the scripted `go` from
// internal/testkit/mutantkit is what makes them a unit-tier test rather than a
// tagged one: no toolchain, no compile, and an answer that can be a compile
// failure on demand.

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

// TestMain turns this binary into the scripted `go` when it is started as one.
// The tests below run this very binary as their toolchain, so without the
// dispatch each compile would run the whole package again.
func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

// fakePackage is the one package the scripted listing reports. Its import path
// is what a failure has to name, so it is spelled once.
const fakePackage = "example.com/m/pkg"

// fakeBuild wires a scripted toolchain into options that describe a plausible
// build of one package, and returns the fake so the test can script the rest.
//
// The binary directory is outside the snapshot on purpose:
// [execute.BuildTestBinaries] refuses one inside it, and a test that happened to
// nest them would be testing the refusal instead of the build.
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
		BinDir:       filepath.Join(t.TempDir(), "bin"),
		Env:          f.Env(testkit.Compose(t, testkit.Scratch(t))),
		Jobs:         1,
		Timeout:      time.Minute,
	}
	return f, opts
}

// compilerOutput is what a `go test -c` that failed really looks like, copied
// from one: the header names the package *and the test binary it was building
// for*, in brackets, and then one located message per problem.
//
// It is written out rather than described because the shape is the assertion —
// internal/validate reads exactly this to attribute a rejected mutant, and a
// capture that arrived reordered, joined or missing its header would be read as
// a different set of diagnostics. The bracketed form is the part a
// hand-invented fixture gets wrong: `go build` prints a bare `# import/path`
// and `go test -c` does not, so a fake that printed the bare one would be
// asserting a shape the phase never sees.
const compilerOutput = "# " + fakePackage + " [" + fakePackage + ".test]\n" +
	"pkg/pkg_test.go:4:2: \"fmt\" imported and not used\n" +
	"pkg/pkg_test.go:9:2: declared and not used: total\n" +
	"pkg/pkg_test.go:10:5: undefined: helper\n"

// TestCompileFailureNamesThePackageAndQuotesTheDiagnostics is the failure this
// package's own documentation calls a go-mutants bug: a snapshot that will not
// compile.
//
// A bug report about one needs three things, and all three used to stop at the
// process boundary: which package it was, what the compiler said, and the exact
// `go test -c` line that said it.
func TestCompileFailureNamesThePackageAndQuotesTheDiagnostics(t *testing.T) {
	t.Parallel()

	f, opts := fakeBuild(t)
	// One, not two: `go test -c` exits 1 for a package it could not build, and
	// 2 is the go command refusing the invocation itself.
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

// TestCompileCarriesVetOffAndListDoesNot is the vet suppression asserted where
// it actually takes effect: in the environment a child process received.
//
// The claim is a pair, and only the pair is worth anything. A compile of an
// instrumented tree has to carry `-vet=off`, because every mutant of an
// expression is spliced in beside the original and a file legitimately holds
// `s == "." && s == ".."` — which vet's `bools` analyzer rejects, stopping the
// run over generated code the user cannot fix. The listing must *not* carry it:
// `go list` runs no vet pass, so handing it the flag would be harmless and would
// still be wrong, since it would say that `go list` is one of the commands this
// rewrite has an opinion about.
//
// build_test.go asserts the same pair on the spec this package built. This one
// asserts it on the GOFLAGS a real child was started with, which is the only
// place the merge with an inherited GOFLAGS can be seen to have happened.
func TestCompileCarriesVetOffAndListDoesNot(t *testing.T) {
	t.Parallel()

	f, opts := fakeBuild(t)
	f.On("test", "-c").CreateOutput()
	// An inherited value, because that is what the merge rule exists for: a
	// project's own GOFLAGS is part of what "the tests build here" means, and
	// the flag is appended to it rather than set over it.
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
	// Nothing that starts a `go` command may carry an activation: a compile run
	// with one would build the wrong program, and the failure would look like a
	// detection.
	for _, call := range calls {
		if value, ok := call.Env["GO_MUTANTS_ACTIVE"]; ok {
			t.Errorf("`go %s` ran with GO_MUTANTS_ACTIVE=%q, want it stripped", call.Argv[0], value)
		}
	}
	if !testkit.SamePath(compile.Dir, opts.SnapshotRoot) {
		t.Errorf("the compile ran in %q, want the snapshot root %q", compile.Dir, opts.SnapshotRoot)
	}
}

// TestBuildFailureDiagnosticsAreParsedFromRealProcessOutput is the assertion an
// injected runner cannot make: that a compiler's output survives the pipe.
//
// Everything downstream of a failed build reads this text as a document —
// internal/validate attributes a rejected mutant by matching `file:line:col:`
// rows against a catalogue, and the renderer prints the block under the message
// — so the block has to arrive whole, in the order the child wrote it, with its
// `# import/path` header still in front of the messages it heads. A capture that
// reordered the two streams, dropped the header or joined two lines would be
// read as a different set of diagnostics, and nothing about it would look like a
// failure.
//
// The go command writes progress and downloads on stdout and diagnostics on
// stderr, so both are scripted and both have to come back, interleaved the way
// internal/runner's single pipe delivers them.
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

	// Every line, in the order the child wrote it, and nothing joined: the
	// header has to stay a line of its own or the messages under it belong to
	// no package.
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

	// And the located rows are still located: a row a parser can read has a
	// file, a line, a column and a message, separated by colons.
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

// TestToolchainCommandsPutTheLocatedToolchainFirstOnPath is the one rule about
// a child's environment this package states and had no way to check.
//
// [gocmd.Toolchain.GoBin] already decides which `go` os/exec starts — the argv
// carries an absolute path — so putting its directory in front of PATH decides
// something else: what that `go` sees. A toolchain that finds a different one
// ahead of itself on PATH can hand work to it, and a run that reported one Go
// version while a second compiled the tree would be reporting about a toolchain
// it never used.
//
// The assertion is on the PATH a real child received, which is the only place
// the rule takes effect: the environment this package composes is a []string
// that nothing else reads.
func TestToolchainCommandsPutTheLocatedToolchainFirstOnPath(t *testing.T) {
	t.Parallel()

	f, opts := fakeBuild(t)
	f.On("test", "-c").CreateOutput()
	// A PATH that already names somewhere else, so "in front" is a claim about
	// order rather than about a PATH with one entry in it. It replaces the
	// entry the harness composed rather than joining it: os/exec keeps the last
	// of two entries naming one variable, and a duplicate would make this a
	// test of that rule instead.
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

// TestAScriptedCompileProducesABinaryTheSchedulerRuns is the phase below the
// build, which used to need a real toolchain for a reason that turns out not to
// be about the toolchain at all.
//
// [execute.RunOne] does not run `go`: it starts a compiled test binary
// directly. What it needed was a binary that exists and behaves, and that is
// what a scripted compile's output is — the fake again, answering the same rule
// table — so a mutant can be scripted killed or survived by exit status and the
// whole path from `go test -c` to a verdict runs with no Go installed.
//
// The two arguments the scheduler adds are asserted along the way, because they
// are what the rules have to match: the `-test.timeout` it owns, doubled to the
// in-process budget, and then the mutant's own arguments.
func TestAScriptedCompileProducesABinaryTheSchedulerRuns(t *testing.T) {
	t.Parallel()

	f, opts := fakeBuild(t)
	f.On("test", "-c").CreateOutput()
	// The binary runs in its package's directory, which a real build would have
	// created. Nothing else here needs the tree.
	packageDir := filepath.Join(opts.SnapshotRoot, "pkg")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatalf("creating the package directory the binary runs in: %v", err)
	}

	const budget = 10 * time.Second
	deadline := "-test.timeout=" + (execute.InProcessTimeoutFactor * budget).String()
	f.On(deadline, "-test.run=^TestCatchesIt$").Stdout("--- FAIL: TestCatchesIt\nFAIL\n").Exit(1)
	f.On(deadline, "-test.run=^TestMissesIt$").Stdout("ok\n")

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

	// What the scheduler really started, read off the log rather than off the
	// spec it built: the compile, then one run per mutant, each with the
	// timeout the supervisor owns in front of the mutant's own arguments and
	// exactly one mutant switched on.
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

// withPath returns env with its PATH entry replaced, however it was spelled.
//
// Replaced rather than appended, because two entries naming one variable are
// resolved by os/exec as "the last wins" and by this package's own prependPath
// as "the first is the one to grow": a duplicate would make the environment a
// child receives depend on which of those two rules ran, which is a thing to
// keep out of an assertion about PATH order.
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
