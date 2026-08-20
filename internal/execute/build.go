// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
)

// listOutputLimit is the capture budget for `go list -json`.
//
// It is not a guess at how much a project prints. internal/runner keeps the
// *tail* of a capture and prepends a truncation notice, which is exactly right
// for a failed test and exactly wrong for a document that has to be parsed: a
// listing that overran the default one-mebibyte cap would arrive as a notice
// followed by a JSON object sliced through the middle. The field filter below
// already keeps a large module's listing to a few hundred kilobytes; this makes
// the cap something that cannot be the reason parsing failed.
const listOutputLimit = 64 << 20

// listFields is the `go list -json` field filter.
//
// Listing every field of every package costs roughly ten times as many bytes as
// these four, all of it thrown away. The four are what a test binary is: where
// to build it from, what to call it, whether the package has any tests at all,
// and which directory to run it in.
const listFields = "ImportPath,Dir,TestGoFiles,XTestGoFiles"

// binarySuffix is the extension every compiled test binary gets.
//
// It is deliberately not `.exe` on Windows. os/exec passes an absolute path to
// CreateProcess unchanged, and CreateProcess runs a valid executable image
// whatever it is called, so the same name works on every platform — and one
// name means the manifest of a run reads the same everywhere.
const binarySuffix = ".test"

// binaryHashBytes is how many bytes of the import path's digest name its
// binary: four, rendered as the eight hex characters the plan specifies.
const binaryHashBytes = 4

// Options is everything this package needs to build and to run.
//
// The zero value is not usable: a toolchain, a snapshot, and a directory to put
// binaries in all have to come from the caller, because every one of them is a
// decision the run made earlier and none of them can be rediscovered here
// without risking a different answer.
type Options struct {
	// Toolchain is the located Go toolchain. It is used for `go list` and
	// `go test -c`; mutants are executed by starting the compiled binaries
	// directly and never touch it.
	Toolchain gocmd.Toolchain

	// SnapshotRoot is the root of the instrumented snapshot: the working
	// directory every `go` command is issued from, and the tree whose packages
	// are enumerated.
	SnapshotRoot string

	// BinDir is where compiled test binaries are written. It must be **outside**
	// SnapshotRoot, and that is not a style preference: the snapshot is
	// re-digested after the run to detect tests that wrote into the tree they
	// are measured in, and a binary written inside it would show up as drift
	// indistinguishable from the hazard the gate exists to catch.
	//
	// A relative path is resolved against the go-mutants process's working
	// directory *before* that containment check is made and before any binary is
	// named, because `go test -c -o` is issued with the snapshot as its working
	// directory: a relative output path would clear the check and then write the
	// binaries into the snapshot anyway. It is created if it does not exist.
	BinDir string

	// ScratchDir is the parent of the per-worker temporary directories. Each
	// worker gets its own subdirectory, and TMP, TEMP and TMPDIR point at it for
	// every child that worker starts, so two mutants running at once cannot
	// meet in the temporary directory.
	//
	// A relative path is resolved against the go-mutants process's working
	// directory, for the same reason and with more at stake: every child runs in
	// a package directory *inside* the snapshot, so a relative TMPDIR would name
	// a directory inside the tree every later mutant is measured against — the
	// exact hazard this option exists to prevent, arriving through the option
	// meant to prevent it.
	//
	// Empty leaves the inherited temporary directory alone, which is right for a
	// single [RunOne] and wrong for a real run: a caller that schedules should
	// pass a directory outside the snapshot, so that a test writing to TMPDIR is
	// neither workspace drift nor something the snapshot cleanup deletes out
	// from under itself.
	ScratchDir string

	// Jobs is how many test binaries are compiled, and how many mutants are
	// executed, at once. Zero or negative means one: silently serialising is a
	// slow run, and silently parallelising is a machine brought to its knees by
	// a caller that forgot to say.
	Jobs int

	// CoverPkg turns coverage instrumentation on for the build and names the
	// packages it is collected for: a non-empty value compiles every test
	// binary with `-cover -coverpkg=<CoverPkg>`, and an empty one — the
	// default — compiles them plainly. A run doing coverage-guided selection
	// passes `<module>/...`, so that a binary's profile carries every package it
	// links rather than only the one it tests.
	//
	// One build serves both purposes, which is the whole reason coverage is a
	// build option rather than a second set of binaries: the same instrumented
	// binaries are profiled once with [CollectCoverage] and then run thousands
	// of times with a mutant active. That is not free, and the plan's original
	// claim that it is deserves correcting rather than repeating. A test binary
	// built with `-cover` runs its coverage teardown on *every* exit whatever
	// `-test.gocoverdir` says: with no directory named, testing's coverTearDown
	// writes the data into an os.MkdirTemp directory, reads it back, prints the
	// percentage, and removes it. Measured on go1.26.5/windows: about 6 ms per
	// run on a three-file fixture, and 8-16 ms on go-mutants' own
	// internal/mutation binary with `-coverpkg` over the whole module. The
	// temporary directory is a worker's own, since TMP, TEMP and TMPDIR are
	// redirected there, so nothing lands in the snapshot.
	//
	// The teardown can also fail, and a failure is not silent: testing's
	// coverReport exits 2, which [RunOne] reads as a killed mutant like any
	// other non-zero status. It cannot be special-cased — `go test` exits 2 for
	// real failures too — so the mitigation is that the directory it writes into
	// is the same per-worker one every other part of a mutant run already
	// depends on being writable.
	CoverPkg string

	// Timeout bounds each *toolchain* command — one `go list`, one
	// `go test -c` — and each [CollectCoverage] profiling run, and nothing
	// else. A mutant's budget is a different number derived from a different
	// measurement and travels with the mutant, in [MutantRun.Timeout]. Zero
	// means no bound.
	Timeout time.Duration

	// run is the process runner, injected by the package's own tests. Nil means
	// [runner.Run], which is the only value any caller outside this package can
	// produce.
	run runFunc
}

// runFunc is the shape of [runner.Run].
type runFunc func(context.Context, runner.Spec) runner.Result

// runProcess resolves the runner this options value executes with.
func (o Options) runProcess(ctx context.Context, spec runner.Spec) runner.Result {
	if o.run != nil {
		return o.run(ctx, spec)
	}
	return runner.Run(ctx, spec)
}

// workers resolves [Options.Jobs] against its documented default.
func (o Options) workers() int {
	return max(o.Jobs, 1)
}

// resolve rejects options that cannot describe a build and returns them with
// every directory this package writes into made absolute.
//
// Resolving here rather than at each point of use is what makes one string mean
// one directory. The two directories are consumed against two *different*
// working directories — os.MkdirAll and the temporary-directory variables
// resolve against the go-mutants process, while `go test -c -o` is issued with
// the snapshot as its working directory — so a relative BinDir would clear the
// containment check below, create <cwd>/bin, and then write the binaries into
// <SnapshotRoot>/bin: precisely the drift the check exists to prevent. Doing it
// once, before anything is created or named, is also what makes
// [TestBinary.BinPath] the absolute path it is documented to be.
func (o Options) resolve() (Options, error) {
	switch {
	case strings.TrimSpace(o.Toolchain.GoBin) == "":
		return o, &Error{Code: CodeOptions, Message: "no Go toolchain was located"}
	case strings.TrimSpace(o.SnapshotRoot) == "":
		return o, &Error{Code: CodeOptions, Message: "no snapshot root was given"}
	case strings.TrimSpace(o.BinDir) == "":
		return o, &Error{Code: CodeOptions, Message: "no directory for the test binaries was given"}
	}

	binDir, binErr := filepath.Abs(o.BinDir)
	root, rootErr := filepath.Abs(o.SnapshotRoot)
	if err := errors.Join(binErr, rootErr); err != nil {
		return o, &Error{
			Code:    CodeOptions,
			Message: "the snapshot root and the test binary directory cannot be resolved against the working directory",
			Err:     err,
		}
	}
	if insideSnapshot(binDir, root) {
		return o, &Error{
			Code: CodeOptions,
			Message: "the test binary directory " + strconv.Quote(binDir) +
				" is inside the snapshot; a binary written into the tree is indistinguishable from a test that wrote into it",
		}
	}
	o.BinDir = binDir

	// The scratch directory is resolved here too, though nothing in this
	// function uses it. [RunOne] derives each worker's own directory from it and
	// resolves that in turn — it has to, since it is reachable without a build —
	// so this is simply the earliest moment a caller that handed over a path
	// nothing can resolve is told so, before a test binary has been compiled
	// for it.
	if strings.TrimSpace(o.ScratchDir) != "" {
		scratch, err := filepath.Abs(o.ScratchDir)
		if err != nil {
			return o, &Error{
				Code: CodeScratchDir,
				Message: "the scratch directory " + strconv.Quote(o.ScratchDir) +
					" cannot be resolved against the working directory",
				Err: err,
			}
		}
		o.ScratchDir = scratch
	}
	return o, nil
}

// insideSnapshot reports whether dir is the snapshot root or sits under it.
//
// Both arguments are already absolute — [Options.resolve] makes them so before
// asking — which is what lets this answer the question actually being asked: a
// caller that passed a relative BinDir and a relative SnapshotRoot meant two
// directories, not two strings.
func insideSnapshot(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		// Different volumes on Windows. Not relative, therefore not inside.
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// A TestBinary is one compiled test binary and the two facts needed to run it.
type TestBinary struct {
	// ImportPath is the package's import path. It is the binary's public name:
	// what a report renders, what [Attempt.KilledBy] records, and what the
	// binary's file name is derived from.
	ImportPath string
	// Dir is the package's source directory inside the snapshot. It is the
	// working directory the binary runs in, because a Go test resolves testdata
	// relative to where it runs and running it anywhere else would change what
	// the tests can see.
	Dir string
	// BinPath is the absolute path of the compiled binary, whatever
	// [Options.BinDir] was spelled as: [Options.resolve] absolutises the
	// directory before any binary is named. Absolute is the only form that
	// works, because the binary is started with Dir set to the package
	// directory above — and a relative argv[0] means the package directory on
	// POSIX, which chdirs and then executes, but the go-mutants process's own
	// working directory on Windows, where CreateProcess resolves the image
	// against the parent.
	BinPath string
}

// listedPackage is the `go list -json` record this package reads.
type listedPackage struct {
	ImportPath   string
	Dir          string
	TestGoFiles  []string
	XTestGoFiles []string
}

// hasTests reports whether the package has any test files at all. Both kinds
// count: an external test package is where some packages are exercised from,
// and `go test -c` produces one binary covering both.
func (p listedPackage) hasTests() bool {
	return len(p.TestGoFiles) > 0 || len(p.XTestGoFiles) > 0
}

// BuildTestBinaries compiles one test binary per package in the snapshot that
// has tests.
//
// Packages with no test files are skipped rather than built: `go test -c`
// produces nothing for them, and a binary that contains no tests can only ever
// report that a mutant survived it.
//
// The builds run concurrently, bounded by [Options.Jobs]. That is safe by
// construction — the build cache is designed for concurrent use, and each
// invocation writes to its own output path — and it is where the wall-clock
// time of a run's first phase goes. The first failure cancels the rest: a
// snapshot with one uncompilable test package is not a snapshot any mutant can
// be measured in, so finishing the other builds would only delay the report.
//
// The result is sorted by import path, whatever order the builds finished in.
func BuildTestBinaries(ctx context.Context, opts Options) ([]TestBinary, error) {
	opts, err := opts.resolve()
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(opts.BinDir, 0o755); err != nil {
		return nil, &Error{
			Code:    CodeBinDir,
			Message: "the directory for the test binaries " + strconv.Quote(opts.BinDir) + " could not be created",
			Err:     err,
		}
	}

	packages, err := listPackages(ctx, opts)
	if err != nil {
		return nil, err
	}

	binaries := plan(packages, opts.BinDir)
	if len(binaries) == 0 {
		return nil, nil
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(opts.workers())
	for _, bin := range binaries {
		group.Go(func() error { return compile(groupCtx, opts, bin) })
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return binaries, nil
}

// listPackages enumerates the snapshot's packages, in `go list` order.
//
// `-e` is deliberately not passed. By the time this runs the snapshot has
// already built and passed its tests unmutated, so a package the go command
// cannot describe is infrastructure trouble rather than a fact about the user's
// code, and continuing past it would silently drop a package's tests from every
// mutant's measurement.
func listPackages(ctx context.Context, opts Options) ([]listedPackage, error) {
	spec := opts.Toolchain.Command("list", "-json="+listFields, "./...")
	spec.Dir = opts.SnapshotRoot
	spec.Env = toolchainEnv(opts.Toolchain, "")
	spec.Timeout = opts.Timeout
	spec.OutputLimit = listOutputLimit

	result := opts.runProcess(ctx, spec)
	if err := commandFailure(ctx, result, CodeListFailed,
		"the snapshot's packages could not be listed", opts.Timeout); err != nil {
		return nil, err
	}

	// stdout and stderr share one stream here, because that is what
	// internal/runner captures and separating them is not this package's call to
	// make. A `go list` that succeeded normally writes nothing to stderr; one
	// that wrote anyway lands in the decoder as text that is not JSON, and is
	// reported as an unreadable listing with the output attached rather than
	// silently misparsed.
	var packages []listedPackage
	decoder := json.NewDecoder(bytes.NewReader(result.Output))
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, &Error{
				Code:    CodeListUnreadable,
				Message: "the output of `go list -json` could not be decoded",
				Output:  tail(result.Output),
				Err:     err,
			}
		}
		packages = append(packages, pkg)
	}
	return packages, nil
}

// plan turns the listing into the binaries to build, sorted by import path and
// with a file name for each.
//
// Sorting here rather than after the builds is what makes the returned order
// independent of how long each compile took, and the collision handling is what
// keeps the file names a function of the sorted listing rather than of a race.
func plan(packages []listedPackage, binDir string) []TestBinary {
	withTests := make([]listedPackage, 0, len(packages))
	for _, pkg := range packages {
		if pkg.hasTests() {
			withTests = append(withTests, pkg)
		}
	}
	slices.SortFunc(withTests, func(x, y listedPackage) int {
		return strings.Compare(x.ImportPath, y.ImportPath)
	})

	binaries := make([]TestBinary, 0, len(withTests))
	taken := make(map[string]bool, len(withTests))
	for _, pkg := range withTests {
		binaries = append(binaries, TestBinary{
			ImportPath: pkg.ImportPath,
			Dir:        pkg.Dir,
			BinPath:    filepath.Join(binDir, uniqueName(pkg.ImportPath, taken)),
		})
	}
	return binaries
}

// uniqueName names a package's binary after the first eight hex characters of
// its import path's digest, and resolves the collisions that shortening makes
// possible.
//
// Eight characters is short enough to read in a directory listing and long
// enough that a collision needs tens of thousands of packages, but "unlikely"
// is not "impossible" and two packages sharing an output path would have them
// overwriting each other's binary mid-build. The suffix is assigned in the
// caller's sorted order, so the same listing always produces the same names.
func uniqueName(importPath string, taken map[string]bool) string {
	sum := sha256.Sum256([]byte(importPath))
	stem := hex.EncodeToString(sum[:binaryHashBytes])
	name := stem + binarySuffix
	for n := 1; taken[name]; n++ {
		name = stem + "-" + strconv.Itoa(n) + binarySuffix
	}
	taken[name] = true
	return name
}

// compile builds one package's test binary.
func compile(ctx context.Context, opts Options, bin TestBinary) error {
	args := []string{"test", "-c"}
	if opts.CoverPkg != "" {
		args = append(args, "-cover", "-coverpkg="+opts.CoverPkg)
	}
	args = append(args, "-o", bin.BinPath, bin.ImportPath)

	spec := opts.Toolchain.Command(args...)
	spec.Dir = opts.SnapshotRoot
	spec.Env = toolchainEnv(opts.Toolchain, "")
	spec.Timeout = opts.Timeout

	result := opts.runProcess(ctx, spec)
	return commandFailure(ctx, result, CodeTestBuildFailed,
		"the test binary for "+bin.ImportPath+" could not be built", opts.Timeout)
}

// commandFailure turns one [runner.Result] into an error, or nil when the
// command succeeded.
//
// The order of the cases is the contract, and it is internal/engine's. A
// cancelled run comes back from the runner as [runner.ExitCodeUnavailable] with
// no error and no timeout, which is indistinguishable from a failure unless the
// context is asked — so it is asked before the exit status is judged, and after
// the two conditions that are definitely not cancellations.
func commandFailure(ctx context.Context, result runner.Result, code Code, what string, timeout time.Duration) error {
	switch {
	case result.Err != nil:
		return &Error{
			Code:    code,
			Message: what + ": the command could not be run",
			Output:  tail(result.Output),
			Err:     result.Err,
		}
	case result.TimedOut:
		return &Error{
			Code:    code,
			Message: what + ": no answer within " + timeout.String(),
			Output:  tail(result.Output),
		}
	case ctx.Err() != nil:
		return &Error{
			Code:    CodeInterrupted,
			Message: "the execution phase was interrupted",
			Err:     context.Cause(ctx),
		}
	case result.ExitCode != 0:
		return &Error{
			Code:    code,
			Message: what + ": exited with status " + strconv.Itoa(result.ExitCode),
			Output:  tail(result.Output),
		}
	}
	return nil
}
