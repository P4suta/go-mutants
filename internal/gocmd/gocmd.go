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
	"github.com/P4suta/go-mutants/trace"
)

const DefaultProbeTimeout = 30 * time.Second

const notFoundRemedy = "install Go and put it on PATH, or run go-mutants through the toolchain manager that owns it (`mise exec -- go-mutants ...`)"

type Options struct {
	Explicit string

	Env []string

	Timeout time.Duration

	Trace *trace.Recorder
}

type Toolchain struct {
	GoBin   string
	Version Version
}

func (t Toolchain) String() string { return t.GoBin + " (" + t.Version.Raw + ")" }

func (t Toolchain) Command(args ...string) runner.Spec {
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, t.GoBin)
	argv = append(argv, args...)
	return runner.Spec{Argv: argv}
}

func Locate(opts Options) (Toolchain, error) {
	return LocateContext(context.Background(), opts)
}

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
	spec.Trace = opts.Trace
	spec.Kind = trace.ExecKindGoVersion

	result := runner.Run(ctx, spec)
	invocation := runner.InvocationOf(spec, result)
	probed := func(failure *Error) error {
		failure.Invocation = &invocation
		failure.Output = string(result.Output)
		return failure
	}

	switch {
	case result.Err != nil:
		return Toolchain{}, probed(&Error{
			Code:    CodeVersionProbeFailed,
			Message: "could not run `" + goBin + " version`",
			Err:     result.Err,
		})
	case result.TimedOut:
		return Toolchain{}, probed(&Error{
			Code:    CodeVersionProbeFailed,
			Message: "`" + goBin + " version` did not answer within " + timeout.String(),
		})
	case ctx.Err() != nil:
		return Toolchain{}, probed(&Error{
			Code:    CodeVersionProbeFailed,
			Message: "`" + goBin + " version` was cancelled",
			Err:     ctx.Err(),
		})
	case result.ExitCode != 0:
		return Toolchain{}, probed(&Error{
			Code: CodeVersionProbeFailed,
			Message: "`" + goBin + " version` exited with status " +
				strconv.Itoa(result.ExitCode) + ": " + quote(string(result.Output)),
		})
	}

	version, err := parseVersion(string(result.Output))
	if err != nil {
		var unparsable *Error
		if errors.As(err, &unparsable) {
			return Toolchain{}, probed(unparsable)
		}
		return Toolchain{}, err
	}
	return Toolchain{GoBin: goBin, Version: version}, nil
}

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

var absolutePath = filepath.Abs

func absolute(path string) (string, error) {
	abs, err := absolutePath(path)
	if err != nil {
		return "", &Error{
			Code:    CodeToolchainNotFound,
			Message: "could not resolve the go executable " + quotePath(path) + " against the working directory",
			Err:     err,
		}
	}
	return abs, nil
}
