// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
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
)

// toolchain is the located Go toolchain the build tests pretend to have. Only
// the path matters: no process is really started.
var toolchain = gocmd.Toolchain{GoBin: filepath.Join("tools", "bin", "go")}

// binaryName matches the file name rule: eight hex characters and the `.test`
// suffix, with an optional collision counter.
var binaryName = regexp.MustCompile(`^[0-9a-f]{8}(-\d+)?\.test$`)

// listing renders a `go list -json` answer for the given packages. The shape is
// the go command's own: a stream of pretty-printed objects with no enclosing
// array, which is what makes a streaming decoder the right reader for it.
func listing(entries ...string) string { return strings.Join(entries, "\n") + "\n" }

// pkgJSON renders one `go list -json=ImportPath,Dir,TestGoFiles,XTestGoFiles`
// record.
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

// buildOptions wires a fake into options that describe a plausible run.
func buildOptions(t *testing.T, f *fake, jobs int) (execute.Options, string) {
	t.Helper()
	// The snapshot and the binary directory are separate temporary directories
	// on purpose: BuildTestBinaries refuses a binary directory inside the
	// snapshot, and a test that happened to nest them would be testing the
	// refusal instead of the build.
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

// isList reports whether a call is the package listing rather than a compile.
func isList(c call) bool { return len(c.Argv) > 1 && c.Argv[1] == "list" }

// TestBuildTestBinariesBuildsOnlyPackagesWithTests pins what a run compiles and
// in what order.
//
// The skip is not tidiness. `go test -c` produces no binary for a package with
// no test files, so building one would fail or leave nothing behind, and a
// binary containing no tests could only ever report that a mutant survived it.
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

// TestBuildTestBinariesIssuesTheExpectedCommands pins the two toolchain
// invocations, both of which have to be exactly right for a later phase to mean
// anything.
//
// The field filter on the listing is not cosmetic: internal/runner keeps the
// *tail* of a capture, so an unfiltered listing of a large module can overrun
// the capture budget and arrive as a truncation notice followed by half a JSON
// object. GOWORK=off is borrowed from internal/discover for the neighbouring
// reason — the package set built here has to be the package set discovery
// type-checked, and a `go.work` above the snapshot would change it.
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
	// The listing is not a compile and has no vet pass to turn off, so it does
	// not carry the suppression the build below does. Handing it one anyway
	// would be harmless and would still be wrong: it would say that `go list`
	// is one of the commands this rewrite has an opinion about.
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

// TestBuildTestBinariesTurnsVetOffWithoutLosingInheritedGoflags is the whole
// reason the suppression is merged rather than set.
//
// The tree `go test -c` compiles here is instrumented: every mutant of an
// expression sits beside the original, so `s == "." && s == ".."` is a shape
// the snapshot legitimately holds and vet's `bools` analyzer legitimately
// refuses. Turning vet off is what keeps that from stopping the run — but
// GOFLAGS is also how a developer, a CI image or a toolchain manager says
// `-mod=readonly`, and internal/execute inherits that on purpose. Overwriting
// the variable would compile a different program from the one the project
// builds, which is a subtler failure than the one being fixed.
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
	// And the listing keeps exactly what the process had, which is the other
	// half of the same claim: the suppression is scoped to the one command that
	// needs it rather than applied to the phase.
	if got, want := envValue(seen[0].Env, "GOFLAGS"), "-mod=readonly"; got != want {
		t.Errorf("listing GOFLAGS = %q, want the inherited %q", got, want)
	}
}

// TestBuildTestBinariesBuildsInParallelWithinTheJobLimit proves both halves of
// [execute.Options.Jobs]: the compiles really do overlap, and they never
// overlap more than the caller allowed.
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

// TestBuildTestBinariesReportsToolchainFailures covers the three ways the
// toolchain can refuse, each with its own code so a user can tell a listing
// that would not run from output that would not parse from a package that would
// not compile.
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

// outputPath returns the `-o` argument of a compile call, or "" if it has none.
func outputPath(c call) string {
	for i, arg := range c.Argv {
		if arg == "-o" && i+1 < len(c.Argv) {
			return c.Argv[i+1]
		}
	}
	return ""
}

// TestBuildTestBinariesResolvesARelativeBinaryDirectory pins the resolution the
// drift gate depends on.
//
// The binary directory is consumed against two different working directories:
// os.MkdirAll creates it relative to the go-mutants process, while `go test -c
// -o` is issued with the *snapshot* as its working directory. A relative
// "bin" therefore used to clear the not-inside-the-snapshot check, create
// <cwd>/bin, and then write the real binaries into <snapshot>/bin — drift
// indistinguishable from the hazard that check exists to catch — and hand back
// a relative [execute.TestBinary.BinPath] whose meaning as argv[0] differs
// between POSIX and Windows.
//
// So the claim here is one path resolved once: the `-o` the toolchain is given
// is absolute, it is the BinPath the caller is handed, and nothing named "bin"
// appears in the snapshot.
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

// TestBuildTestBinariesRefusesOptionsItCannotBuildFrom covers the fail-closed
// refusals, including the one that protects the drift gate: a test binary
// written inside the snapshot is indistinguishable from a test that wrote into
// the tree every later mutant is measured against.
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

// TestBuildTestBinariesAcceptsASnapshotWithNoTestsAtAll documents that an empty
// result is not an error here. Refusing to *measure* against no binaries is
// [execute.Schedule]'s job, and it is the right place for it: the caller may
// legitimately want to know that a tree has no tests before deciding what to
// say about it.
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

// TestPlanNamesBinariesDeterministicallyAndResolvesCollisions pins the naming
// rule.
//
// Eight hex characters of a digest is short enough to read and long enough that
// a collision takes tens of thousands of packages — but "unlikely" is not
// "impossible", and two packages sharing an output path would overwrite each
// other's binary mid-build. Repeating an import path stands in for the
// collision a digest cannot be made to produce on demand.
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
