// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

var toolchain = gocmd.Toolchain{GoBin: filepath.Join("tools", "bin", "go")}

var binaryName = regexp.MustCompile(`^[0-9a-f]{8}(-\d+)?\.test$`)

func listing(entries ...string) string { return strings.Join(entries, "\n") + "\n" }

func pkgJSON(importPath, dir string, tests, xtests bool) string {
	var b strings.Builder
	b.WriteString("{\n\t\"Dir\": \"" + dir + "\",\n\t\"ImportPath\": \"" + importPath + "\"")
	if tests {
		b.WriteString(",\n\t\"TestGoFiles\": [\n\t\t\"a_test.go\"\n\t]")
	}
	if xtests {
		b.WriteString(",\n\t\"XTestGoFiles\": [\n\t\t\"b_test.go\"\n\t]")
	}
	b.WriteString("\n}")
	return b.String()
}

func buildOptions(t *testing.T, f *fake, jobs int) (execute.Options, string) {
	t.Helper()
	binDir := filepath.Join(t.TempDir(), "bin")
	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: t.TempDir(),
		BinDir:       binDir,
		Jobs:         jobs,
		Timeout:      time.Minute,
	}
	return execute.WithRunner(opts, f.run), binDir
}

func isList(c call) bool { return len(c.Argv) > 1 && c.Argv[1] == "list" }

func TestBuildTestBinariesBuildsOnlyPackagesWithTests(t *testing.T) {
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(
				pkgJSON("example.com/m/zeta", "/snap/zeta", true, false),
				pkgJSON("example.com/m/none", "/snap/none", false, false),
				pkgJSON("example.com/m/alpha", "/snap/alpha", false, true),
				pkgJSON("example.com/m/beta", "/snap/beta", true, true),
			))}
		}
		return runner.Result{}
	}}
	opts, binDir := buildOptions(t, f, 2)

	binaries, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the test binaries: %v", err)
	}

	want := []string{"example.com/m/alpha", "example.com/m/beta", "example.com/m/zeta"}
	got := make([]string, len(binaries))
	for i, bin := range binaries {
		got[i] = bin.ImportPath
	}
	if !slices.Equal(got, want) {
		t.Errorf("built %q, want %q sorted by import path with the test-free package skipped", got, want)
	}
	if binaries[0].Dir != "/snap/alpha" {
		t.Errorf("package directory = %q, want the snapshot's own %q", binaries[0].Dir, "/snap/alpha")
	}
	for _, bin := range binaries {
		if filepath.Dir(bin.BinPath) != binDir {
			t.Errorf("%s was built into %q, want the binary directory %q", bin.ImportPath, bin.BinPath, binDir)
		}
		if name := filepath.Base(bin.BinPath); !binaryName.MatchString(name) {
			t.Errorf("%s was named %q, want eight hex characters and %q", bin.ImportPath, name, ".test")
		}
	}
	if ok, statErr := statDir(binDir); statErr != nil || !ok {
		t.Errorf("the binary directory was not created: %v", statErr)
	}
}

func TestBuildTestBinariesIssuesTheExpectedCommands(t *testing.T) {
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(
				pkgJSON("example.com/m/pkg", "/snap/pkg", true, false),
			))}
		}
		return runner.Result{}
	}}
	opts, _ := buildOptions(t, f, 1)

	binaries, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the test binaries: %v", err)
	}

	seen := f.seen()
	if len(seen) != 2 {
		t.Fatalf("issued %d commands, want a listing and one compile", len(seen))
	}

	list := seen[0]
	wantList := []string{
		toolchain.GoBin, "list", "-json=ImportPath,Dir,TestGoFiles,XTestGoFiles", "./...",
	}
	if !slices.Equal(list.Argv, wantList) {
		t.Errorf("listing argv = %q, want %q", list.Argv, wantList)
	}
	if list.Dir != opts.SnapshotRoot {
		t.Errorf("listing ran in %q, want the snapshot root %q", list.Dir, opts.SnapshotRoot)
	}
	if got := envValue(list.Env, "GOWORK"); got != "off" {
		t.Errorf("GOWORK = %q, want %q so a workspace above the snapshot cannot change the package set", got, "off")
	}
	if got := envValue(list.Env, "GOFLAGS"); strings.Contains(got, gocmd.VetOff) {
		t.Errorf("the listing carries GOFLAGS %q, want no %s: `go list` runs no vet pass", got, gocmd.VetOff)
	}

	compile := seen[1]
	wantCompile := []string{toolchain.GoBin, "test", "-c", "-o", binaries[0].BinPath, "example.com/m/pkg"}
	if !slices.Equal(compile.Argv, wantCompile) {
		t.Errorf("compile argv = %q, want %q", compile.Argv, wantCompile)
	}
	if compile.Dir != opts.SnapshotRoot {
		t.Errorf("compile ran in %q, want the snapshot root %q", compile.Dir, opts.SnapshotRoot)
	}
	if compile.Timeout != opts.Timeout {
		t.Errorf("compile timeout = %s, want %s", compile.Timeout, opts.Timeout)
	}
	if got := envValue(compile.Env, "GOFLAGS"); !strings.Contains(got, gocmd.VetOff) {
		t.Errorf("compile GOFLAGS = %q, want it to carry %s", got, gocmd.VetOff)
	}
}

func TestBuildTestBinariesListsOnlyTheScopedPackages(t *testing.T) {
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(
				pkgJSON("example.com/m/alpha", "/snap/alpha", true, false),
			))}
		}
		return runner.Result{}
	}}
	opts, _ := buildOptions(t, f, 1)
	opts.Packages = []string{"./alpha/...", "./beta"}

	binaries, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the test binaries: %v", err)
	}
	if len(binaries) != 1 || binaries[0].ImportPath != "example.com/m/alpha" {
		t.Fatalf("built %+v, want only the one package the scope listed", binaries)
	}

	seen := f.seen()
	if len(seen) != 2 {
		t.Fatalf("issued %d commands, want a listing and one compile", len(seen))
	}
	wantList := []string{
		toolchain.GoBin, "list", "-json=ImportPath,Dir,TestGoFiles,XTestGoFiles",
		"./alpha/...", "./beta",
	}
	if !slices.Equal(seen[0].Argv, wantList) {
		t.Errorf("listing argv = %q, want %q", seen[0].Argv, wantList)
	}
}

func TestBuildTestBinariesRefusesABlankPattern(t *testing.T) {
	f := &fake{}
	opts, _ := buildOptions(t, f, 1)
	opts.Packages = []string{"./alpha/...", "  "}

	_, err := execute.BuildTestBinaries(t.Context(), opts)
	if code := execute.CodeOf(err); code != execute.CodeOptions {
		t.Fatalf("code = %s, want %s: %v", code, execute.CodeOptions, err)
	}
	if got := len(f.seen()); got != 0 {
		t.Errorf("issued %d commands for options that cannot describe a build, want none", got)
	}
}

func TestBuildTestBinariesTurnsVetOffWithoutLosingInheritedGoflags(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=readonly")

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(
				pkgJSON("example.com/m/pkg", "/snap/pkg", true, false),
			))}
		}
		return runner.Result{}
	}}
	opts, _ := buildOptions(t, f, 1)

	if _, err := execute.BuildTestBinaries(t.Context(), opts); err != nil {
		t.Fatalf("building the test binaries: %v", err)
	}

	seen := f.seen()
	if len(seen) != 2 {
		t.Fatalf("issued %d commands, want a listing and one compile", len(seen))
	}
	if got, want := envValue(seen[1].Env, "GOFLAGS"), "-mod=readonly "+gocmd.VetOff; got != want {
		t.Errorf("compile GOFLAGS = %q, want %q", got, want)
	}
	if got, want := envValue(seen[0].Env, "GOFLAGS"), "-mod=readonly"; got != want {
		t.Errorf("listing GOFLAGS = %q, want the inherited %q", got, want)
	}
}

func TestBuildTestBinariesBuildsInParallelWithinTheJobLimit(t *testing.T) {
	const jobs = 2

	var inFlight, peak atomic.Int64
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(
				pkgJSON("example.com/m/a", "/snap/a", true, false),
				pkgJSON("example.com/m/b", "/snap/b", true, false),
				pkgJSON("example.com/m/c", "/snap/c", true, false),
				pkgJSON("example.com/m/d", "/snap/d", true, false),
			))}
		}
		concurrent := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			seen := peak.Load()
			if concurrent <= seen || peak.CompareAndSwap(seen, concurrent) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		return runner.Result{}
	}}
	opts, _ := buildOptions(t, f, jobs)

	if _, err := execute.BuildTestBinaries(t.Context(), opts); err != nil {
		t.Fatalf("building the test binaries: %v", err)
	}
	if got := peak.Load(); got > jobs {
		t.Errorf("%d compiles ran at once, want at most %d", got, jobs)
	}
	if got := peak.Load(); got < 2 {
		t.Errorf("compiles never overlapped (peak %d); the builds are running one at a time", got)
	}
}

func TestBuildTestBinariesReportsToolchainFailures(t *testing.T) {
	good := listing(pkgJSON("example.com/m/pkg", "/snap/pkg", true, false))
	cases := []struct {
		name     string
		respond  func(context.Context, call) runner.Result
		code     execute.Code
		inOutput string
	}{
		{
			name: "the listing exits non-zero",
			respond: func(_ context.Context, c call) runner.Result {
				if isList(c) {
					return runner.Result{ExitCode: 1, Output: []byte("go: cannot load package\n")}
				}
				return runner.Result{}
			},
			code:     execute.CodeListFailed,
			inOutput: "cannot load package",
		},
		{
			name: "the listing is not JSON",
			respond: func(_ context.Context, c call) runner.Result {
				if isList(c) {
					return runner.Result{Output: []byte("go: downloading something\nnot json at all\n")}
				}
				return runner.Result{}
			},
			code:     execute.CodeListUnreadable,
			inOutput: "not json at all",
		},
		{
			name: "a test binary does not compile",
			respond: func(_ context.Context, c call) runner.Result {
				if isList(c) {
					return runner.Result{Output: []byte(good)}
				}
				return runner.Result{ExitCode: 2, Output: []byte("./a_test.go:9:2: undefined: Missing\n")}
			},
			code:     execute.CodeTestBuildFailed,
			inOutput: "undefined: Missing",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{respond: c.respond}
			opts, _ := buildOptions(t, f, 1)

			binaries, err := execute.BuildTestBinaries(t.Context(), opts)
			if err == nil {
				t.Fatalf("building reported success and returned %d binaries", len(binaries))
			}
			if got := execute.CodeOf(err); got != c.code {
				t.Errorf("code = %q, want %q (%v)", got, c.code, err)
			}
			if got := execute.OutputOf(err); !strings.Contains(got, c.inOutput) {
				t.Errorf("retained output = %q, want it to quote %q", got, c.inOutput)
			}
			if strings.Contains(err.Error(), c.inOutput) {
				t.Error("the command output was folded into the one-line message")
			}
		})
	}
}

func outputPath(c call) string {
	for i, arg := range c.Argv {
		if arg == "-o" && i+1 < len(c.Argv) {
			return c.Argv[i+1]
		}
	}
	return ""
}

func TestBuildTestBinariesResolvesARelativeBinaryDirectory(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)

	snapshot := t.TempDir()
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(
				pkgJSON("example.com/m/pkg", "/snap/pkg", true, false),
			))}
		}
		return runner.Result{}
	}}
	opts := execute.WithRunner(execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snapshot,
		BinDir:       "bin",
		Jobs:         1,
		Timeout:      time.Minute,
	}, f.run)

	binaries, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the test binaries: %v", err)
	}
	if len(binaries) != 1 {
		t.Fatalf("built %d binaries, want 1", len(binaries))
	}
	if !filepath.IsAbs(binaries[0].BinPath) {
		t.Errorf("BinPath = %q, want the absolute path the documentation promises", binaries[0].BinPath)
	}

	seen := f.seen()
	if len(seen) != 2 {
		t.Fatalf("issued %d commands, want a listing and one compile", len(seen))
	}
	out := outputPath(seen[1])
	if !filepath.IsAbs(out) {
		t.Errorf("compiled with -o %q, want an absolute path: the compile runs in %q, so a relative one lands in the snapshot",
			out, snapshot)
	}
	if out != binaries[0].BinPath {
		t.Errorf("compiled to %q but reported %q; one binary must have one path", out, binaries[0].BinPath)
	}
	if ok, _ := statDir(filepath.Join(snapshot, "bin")); ok {
		t.Errorf("a binary directory was created inside the snapshot %q", snapshot)
	}
	if ok, statErr := statDir(filepath.Join(work, "bin")); statErr != nil || !ok {
		t.Errorf("the binary directory was not created under the working directory: %v", statErr)
	}
}

func TestBuildTestBinariesRefusesOptionsItCannotBuildFrom(t *testing.T) {
	snapshot := t.TempDir()
	cases := []struct {
		name string
		opts execute.Options
	}{
		{"no toolchain", execute.Options{SnapshotRoot: snapshot, BinDir: t.TempDir()}},
		{"no snapshot root", execute.Options{Toolchain: toolchain, BinDir: t.TempDir()}},
		{"no binary directory", execute.Options{Toolchain: toolchain, SnapshotRoot: snapshot}},
		{
			name: "the binary directory is inside the snapshot",
			opts: execute.Options{
				Toolchain:    toolchain,
				SnapshotRoot: snapshot,
				BinDir:       filepath.Join(snapshot, "bin"),
			},
		},
		{
			name: "the binary directory is the snapshot",
			opts: execute.Options{Toolchain: toolchain, SnapshotRoot: snapshot, BinDir: snapshot},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{}
			if _, err := execute.BuildTestBinaries(t.Context(), execute.WithRunner(c.opts, f.run)); err == nil {
				t.Fatal("building reported success")
			} else if got := execute.CodeOf(err); got != execute.CodeOptions {
				t.Errorf("code = %q, want %q (%v)", got, execute.CodeOptions, err)
			}
			if got := len(f.seen()); got != 0 {
				t.Errorf("issued %d commands before refusing, want none", got)
			}
		})
	}
}

func TestBuildTestBinariesAcceptsASnapshotWithNoTestsAtAll(t *testing.T) {
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(pkgJSON("example.com/m/pkg", "/snap/pkg", false, false)))}
		}
		return runner.Result{}
	}}
	opts, _ := buildOptions(t, f, 1)

	binaries, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the test binaries: %v", err)
	}
	if len(binaries) != 0 {
		t.Errorf("built %d binaries for a tree with no tests", len(binaries))
	}
	if got := len(f.seen()); got != 1 {
		t.Errorf("issued %d commands, want only the listing", got)
	}
}

func TestPlanNamesBinariesDeterministicallyAndResolvesCollisions(t *testing.T) {
	paths := []string{"example.com/m/a", "example.com/m/a", "example.com/m/a"}
	dirs := []string{"/snap/a", "/snap/a", "/snap/a"}
	tests := []bool{true, true, true}

	first := execute.PlanBinaries(paths, dirs, tests, "bin")
	second := execute.PlanBinaries(paths, dirs, tests, "bin")

	if len(first) != 3 {
		t.Fatalf("planned %d binaries, want 3", len(first))
	}
	names := map[string]bool{}
	for _, bin := range first {
		name := filepath.Base(bin.BinPath)
		if names[name] {
			t.Errorf("two packages were both named %q; one would overwrite the other", name)
		}
		names[name] = true
		if !binaryName.MatchString(name) {
			t.Errorf("name %q does not match the naming rule", name)
		}
	}
	for i := range first {
		if first[i].BinPath != second[i].BinPath {
			t.Errorf("planning twice named binary %d %q then %q", i, first[i].BinPath, second[i].BinPath)
		}
	}
}

func TestCommandFailureCarriesTheInvocation(t *testing.T) {
	good := listing(pkgJSON("example.com/m/pkg", "/snap/pkg", true, false))
	cases := []struct {
		name    string
		respond func(context.Context, call) runner.Result
		code    execute.Code
		argv1   string
	}{
		{
			name: "the listing exits non-zero",
			respond: func(_ context.Context, c call) runner.Result {
				if isList(c) {
					return runner.Result{ExitCode: 1, Output: []byte("go: cannot load package\n")}
				}
				return runner.Result{}
			},
			code:  execute.CodeListFailed,
			argv1: "list",
		},
		{
			name: "the listing is not JSON",
			respond: func(_ context.Context, c call) runner.Result {
				if isList(c) {
					return runner.Result{Output: []byte("not json at all\n")}
				}
				return runner.Result{}
			},
			code:  execute.CodeListUnreadable,
			argv1: "list",
		},
		{
			name: "a test binary does not compile",
			respond: func(_ context.Context, c call) runner.Result {
				if isList(c) {
					return runner.Result{Output: []byte(good)}
				}
				return runner.Result{ExitCode: 2, Output: []byte("./a_test.go:9:2: undefined: Missing\n")}
			},
			code:  execute.CodeTestBuildFailed,
			argv1: "test",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{respond: c.respond}
			opts, _ := buildOptions(t, f, 1)

			_, err := execute.BuildTestBinaries(t.Context(), opts)
			if got := execute.CodeOf(err); got != c.code {
				t.Fatalf("code = %q, want %q (%v)", got, c.code, err)
			}
			var failure *execute.Error
			if !errors.As(err, &failure) {
				t.Fatalf("err = %v, want an *execute.Error", err)
			}
			command := failure.Command()
			if command == nil {
				t.Fatal("Command() = nil, want the toolchain command that failed")
			}
			if len(command.Argv) < 2 || command.Argv[0] != toolchain.GoBin || command.Argv[1] != c.argv1 {
				t.Errorf("Command().Argv = %q, want the located toolchain running %q", command.Argv, c.argv1)
			}
			if command.Dir != opts.SnapshotRoot {
				t.Errorf("Command().Dir = %q, want the snapshot root %q", command.Dir, opts.SnapshotRoot)
			}
		})
	}
}

func TestBuildTestBinariesLabelsTheListingAndEachCompile(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(
				pkgJSON("example.com/m/a", "/snap/a", true, false),
				pkgJSON("example.com/m/b", "/snap/b", false, true),
			))}
		}
		return runner.Result{}
	}}
	built, _ := buildOptions(t, f, 1)
	opts, sink := traced(t, f, built)

	if _, err := execute.BuildTestBinaries(t.Context(), opts); err != nil {
		t.Fatalf("BuildTestBinaries: %v", err)
	}

	type label struct{ kind, subject string }
	want := []label{
		{trace.ExecKindGoList, ""},
		{trace.ExecKindGoTestC, "example.com/m/a"},
		{trace.ExecKindGoTestC, "example.com/m/b"},
	}
	var got []label
	for _, event := range eventsOf(sink, trace.TypeExec) {
		got = append(got, label{event.Exec.Kind, event.Exec.Subject})
	}
	if !slices.Equal(got, want) {
		t.Errorf("the recording holds %+v, want %+v", got, want)
	}
	for i, c := range f.seen() {
		if c.Kind != got[i].kind || c.Subject != got[i].subject {
			t.Errorf("call %d was labelled {%q, %q}, want {%q, %q}",
				i, c.Kind, c.Subject, got[i].kind, got[i].subject)
		}
	}
}
