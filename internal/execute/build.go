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
	"github.com/P4suta/go-mutants/trace"
)

const listOutputLimit = 64 << 20

const listFields = "ImportPath,Dir,TestGoFiles,XTestGoFiles"

const binarySuffix = ".test"

const binaryHashBytes = 4

const allPackages = "./..."

type Options struct {
	Toolchain gocmd.Toolchain

	SnapshotRoot string

	Workspace bool

	Packages []string

	BinDir string

	ScratchDir string

	Trees []string

	Restore func() error

	Restores []func() error

	Tree string

	Env []string

	Jobs int

	CoverPkg string

	LoopLimits string

	Timeout time.Duration

	MemoryLimit int64

	Trace *trace.Recorder

	run runFunc
}

type runFunc func(context.Context, runner.Spec) runner.Result

func (o Options) runProcess(ctx context.Context, spec runner.Spec) runner.Result {
	if o.run != nil {
		return o.run(ctx, spec)
	}
	return runner.Run(ctx, spec)
}

func (o Options) workers() int {
	return max(o.Jobs, 1)
}

func (o Options) patterns() []string {
	if len(o.Packages) == 0 {
		return []string{allPackages}
	}
	return o.Packages
}

func (o Options) resolve() (Options, error) {
	switch {
	case strings.TrimSpace(o.Toolchain.GoBin) == "":
		return o, &Error{Code: CodeOptions, Message: "no Go toolchain was located"}
	case strings.TrimSpace(o.SnapshotRoot) == "":
		return o, &Error{Code: CodeOptions, Message: "no snapshot root was given"}
	case strings.TrimSpace(o.BinDir) == "":
		return o, &Error{Code: CodeOptions, Message: "no directory for the test binaries was given"}
	}

	for _, pattern := range o.Packages {
		if strings.TrimSpace(pattern) == "" {
			return o, &Error{
				Code:    CodeOptions,
				Message: "the package patterns the test binaries are built from contain a blank entry",
			}
		}
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

func insideSnapshot(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type TestBinary struct {
	ImportPath string
	Dir        string
	BinPath    string
}

type listedPackage struct {
	ImportPath   string
	Dir          string
	TestGoFiles  []string
	XTestGoFiles []string
}

func (p listedPackage) hasTests() bool {
	return len(p.TestGoFiles) > 0 || len(p.XTestGoFiles) > 0
}

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

func listPackages(ctx context.Context, opts Options) ([]listedPackage, error) {
	args := append([]string{"list", "-json=" + listFields}, opts.patterns()...)
	spec := opts.Toolchain.Command(args...)
	spec.Dir = opts.SnapshotRoot
	spec.Env = toolchainEnvFrom(opts.Env, opts.Toolchain, "", opts.Workspace)
	spec.Timeout = opts.Timeout
	spec.OutputLimit = listOutputLimit
	spec.Trace = opts.Trace
	spec.Kind = trace.ExecKindGoList

	result := opts.runProcess(ctx, spec)
	if err := commandFailure(ctx, spec, result, CodeListFailed,
		"the snapshot's packages could not be listed", opts.Timeout); err != nil {
		return nil, err
	}

	var packages []listedPackage
	decoder := json.NewDecoder(bytes.NewReader(result.Output))
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, &Error{
				Code:       CodeListUnreadable,
				Message:    "the output of `go list -json` could not be decoded",
				Output:     tail(result.Output),
				Err:        err,
				Invocation: runner.CommandOf(spec, result),
			}
		}
		packages = append(packages, pkg)
	}
	return packages, nil
}

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

func compile(ctx context.Context, opts Options, bin TestBinary) error {
	args := []string{"test", "-c"}
	if opts.CoverPkg != "" {
		args = append(args, "-cover", "-coverpkg="+opts.CoverPkg)
	}
	args = append(args, "-o", bin.BinPath, bin.ImportPath)

	spec := opts.Toolchain.Command(args...)
	spec.Dir = opts.SnapshotRoot
	spec.Env = gocmd.AppendGoflags(
		toolchainEnvFrom(opts.Env, opts.Toolchain, "", opts.Workspace), gocmd.VetOff)
	spec.Timeout = opts.Timeout
	spec.Trace = opts.Trace
	spec.Kind = trace.ExecKindGoTestC
	spec.Subject = bin.ImportPath

	result := opts.runProcess(ctx, spec)
	return commandFailure(ctx, spec, result, CodeTestBuildFailed,
		"the test binary for "+bin.ImportPath+" could not be built", opts.Timeout)
}

func commandFailure(
	ctx context.Context,
	spec runner.Spec,
	result runner.Result,
	code Code,
	what string,
	timeout time.Duration,
) error {
	switch {
	case result.Err != nil:
		return &Error{
			Code:       code,
			Message:    what + ": the command could not be run",
			Output:     tail(result.Output),
			Err:        result.Err,
			Invocation: runner.CommandOf(spec, result),
			Package:    spec.Subject,
		}
	case result.TimedOut:
		return &Error{
			Code:       code,
			Message:    what + ": no answer within " + timeout.String(),
			Output:     tail(result.Output),
			Invocation: runner.CommandOf(spec, result),
			Package:    spec.Subject,
			TimedOut:   true,
		}
	case ctx.Err() != nil:
		return &Error{
			Code:       CodeInterrupted,
			Message:    "the execution phase was interrupted",
			Err:        context.Cause(ctx),
			Invocation: runner.CommandOf(spec, result),
			Package:    spec.Subject,
		}
	case result.ExitCode != 0:
		return &Error{
			Code:       code,
			Message:    what + ": exited with status " + strconv.Itoa(result.ExitCode),
			Output:     tail(result.Output),
			Invocation: runner.CommandOf(spec, result),
			Package:    spec.Subject,
			ExitCode:   result.ExitCode,
		}
	}
	return nil
}
