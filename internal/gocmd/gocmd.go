// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/P4suta/go-mutants/internal/runner"
)

// DefaultProbeTimeout bounds `go version`. The command does no work beyond
// printing a constant, so anything approaching this means the executable is
// not answering rather than that it is busy, and a run that hangs before it
// has started is the worst kind of hang to debug.
const DefaultProbeTimeout = 30 * time.Second

// notFoundRemedy is the second half of every [CodeToolchainNotFound] message.
// It is a constant so that the advice cannot drift between the two ways of
// failing to find a toolchain.
const notFoundRemedy = "install Go and put it on PATH, or run go-mutants through the toolchain manager that owns it (`mise exec -- go-mutants ...`)"

// Options configures [Locate].
//
// The zero value is the ordinary case: find `go` on PATH, probe it with this
// process's environment, and give up after [DefaultProbeTimeout].
type Options struct {
	// Explicit names the go executable to use instead of searching PATH. It
	// may be an absolute path, a relative path, or a bare name to resolve
	// through PATH; it comes from configuration or a flag, so it is taken at
	// face value rather than second-guessed.
	//
	// A relative path is interpreted against this process's working directory
	// and recorded as [Toolchain.GoBin] in absolute form, once. Configuration
	// is read where go-mutants was invoked, so that is the directory
	// `./tools/go` meant — never the snapshot a later phase runs commands in.
	Explicit string

	// Env is the environment for the version probe, in "KEY=VALUE" form. Nil
	// inherits this process's environment, which is what a probe wants: the
	// point is to learn about the toolchain as the user's shell sees it.
	Env []string

	// Timeout bounds the version probe. Zero selects [DefaultProbeTimeout].
	Timeout time.Duration
}

// Toolchain is a located Go toolchain.
//
// It is a value, and a cheap one: copy it, store it in a run's workspace
// record, hand it to as many goroutines as there are workers. Everything it
// knows was learned once, when it was located.
type Toolchain struct {
	// GoBin is the resolved absolute path of the go executable. It is a path
	// rather than a name precisely so that later invocations cannot be
	// re-resolved into a different toolchain by a changed PATH.
	GoBin string
	// Version is what `go version` reported.
	Version Version
}

// String renders the toolchain for logs and diagnostics.
func (t Toolchain) String() string { return t.GoBin + " (" + t.Version.Raw + ")" }

// Command builds the [runner.Spec] fragment that invokes this toolchain.
//
// Only Argv is filled in, and that is the whole point: the working directory a
// `go` command needs, the environment it needs, and how long it may take are
// properties of the phase issuing it — a `go test -c` in a snapshot and a
// `go tool covdata` over a coverage directory want none of the same answers —
// so this package refuses to guess at any of them. The caller sets the rest of
// the spec and runs it.
//
//	spec := tc.Command("test", "-c", "-o", out, pkg)
//	spec.Dir = snapshotDir
//	spec.Env = env
//	spec.Timeout = buildTimeout
//	result := runner.Run(ctx, spec)
//
// The returned Argv does not alias args.
func (t Toolchain) Command(args ...string) runner.Spec {
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, t.GoBin)
	argv = append(argv, args...)
	return runner.Spec{Argv: argv}
}

// Locate finds the Go toolchain and reads its version.
//
// It is [LocateContext] with a background context, for the callers — `doctor`,
// configuration validation, the start of a run — that have nothing to cancel.
func Locate(opts Options) (Toolchain, error) {
	return LocateContext(context.Background(), opts)
}

// LocateContext finds the Go toolchain and reads its version, giving up if ctx
// is cancelled.
//
// Success means more than "a file exists": the executable answered
// `go version` with a line this package could read, so a PATH entry that
// happens to be named `go` and is something else is rejected here rather than
// halfway through a build.
func LocateContext(ctx context.Context, opts Options) (Toolchain, error) {
	goBin, err := resolve(opts.Explicit)
	if err != nil {
		return Toolchain{}, err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}

	spec := Toolchain{GoBin: goBin}.Command("version")
	spec.Env = opts.Env
	spec.Timeout = timeout

	result := runner.Run(ctx, spec)
	switch {
	case result.Err != nil:
		return Toolchain{}, &Error{
			Code:    CodeVersionProbeFailed,
			Message: "could not run `" + goBin + " version`",
			Err:     result.Err,
		}
	case result.TimedOut:
		return Toolchain{}, &Error{
			Code:    CodeVersionProbeFailed,
			Message: "`" + goBin + " version` did not answer within " + timeout.String(),
		}
	case ctx.Err() != nil:
		return Toolchain{}, &Error{
			Code:    CodeVersionProbeFailed,
			Message: "`" + goBin + " version` was cancelled",
			Err:     ctx.Err(),
		}
	case result.ExitCode != 0:
		return Toolchain{}, &Error{
			Code: CodeVersionProbeFailed,
			Message: "`" + goBin + " version` exited with status " +
				strconv.Itoa(result.ExitCode) + ": " + quote(string(result.Output)),
		}
	}

	version, err := parseVersion(string(result.Output))
	if err != nil {
		return Toolchain{}, err
	}
	return Toolchain{GoBin: goBin, Version: version}, nil
}

// resolve turns a configured name, or the absence of one, into an absolute
// path.
//
// The two lookup cases differ in one deliberate way. An explicit path that
// resolves inside the current directory is accepted, because someone who
// configured `./tools/go` meant that; a bare `go` found in the current
// directory is not, because os/exec flags it as [exec.ErrDot] for exactly the
// reason it should — running whatever binary happens to sit in the directory
// go-mutants was invoked from is not what "the go on my PATH" means.
//
// Both cases end at [absolute], and that is not cosmetic. [exec.LookPath]
// hands a relative input straight back — `tools/go` resolves to `tools/go` —
// and os/exec resolves a relative argv[0] against Cmd.Dir, which every phase
// after this one sets to somewhere inside the snapshot. A relative GoBin would
// therefore be looked for inside the tree under test: missing, or worse,
// something else entirely. Absolutising here is also what makes the
// [exec.ErrDot] acceptance above mean anything, since exec.Command would
// otherwise flag the very path this function just chose to allow and fail at
// Start.
func resolve(explicit string) (string, error) {
	if explicit == "" {
		path, err := exec.LookPath("go")
		if err != nil {
			return "", &Error{
				Code:    CodeToolchainNotFound,
				Message: "no `go` executable on PATH; " + notFoundRemedy,
				Err:     err,
			}
		}
		return absolute(path)
	}

	path, err := exec.LookPath(explicit)
	if err != nil && !errors.Is(err, exec.ErrDot) {
		return "", &Error{
			Code:    CodeToolchainNotFound,
			Message: "no go executable at the configured path " + quotePath(explicit) + "; " + notFoundRemedy,
			Err:     err,
		}
	}
	return absolute(path)
}

// absolute anchors a resolved path to the current working directory.
//
// [filepath.Abs] fails only when the working directory cannot be read, which is
// a process that has had the ground taken out from under it rather than a
// misconfiguration. It is still reported rather than papered over: returning
// the relative path anyway would hand back exactly the [Toolchain.GoBin] this
// function exists to rule out.
func absolute(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", &Error{
			Code:    CodeToolchainNotFound,
			Message: "could not resolve the go executable " + quotePath(path) + " against the working directory",
			Err:     err,
		}
	}
	return abs, nil
}
