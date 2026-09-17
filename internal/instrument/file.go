// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/mutation"
)

type FileOptions struct {
	SnapshotRoot string

	RuntimeImport string

	Path string

	Source []byte

	Mutants []mutation.Mutant

	Hints Hints

	Mode Mode

	LoopBase uint32
}

func InstrumentFile(opts FileOptions) (int, error) {
	if err := opts.validate(); err != nil {
		return 0, err
	}

	file := filepath.Join(opts.SnapshotRoot, filepath.FromSlash(opts.Path))
	info, err := os.Stat(file)
	if err != nil {
		return 0, &Error{
			Code:    CodeSourceUnreadable,
			Message: "cannot read " + strconv.Quote(opts.Path) + " in the snapshot",
			Err:     err,
		}
	}

	names := newPackageNames()
	dir := filepath.Dir(file)
	reserved := func(pkg string) (map[string]bool, error) { return names.namesIn(dir, pkg) }

	out, guards, _, err := instrumentSource(
		opts.Path, opts.Source, opts.Mutants, opts.Hints, opts.RuntimeImport, reserved, opts.Mode,
		opts.LoopBase)
	if err != nil {
		return 0, err
	}
	if err := replaceFile(file, out, info.Mode().Perm()); err != nil {
		return 0, &Error{
			Code:    CodeWriteFailed,
			Message: "cannot write the instrumented " + strconv.Quote(opts.Path),
			Err:     err,
		}
	}
	return guards, nil
}

func (o FileOptions) validate() error {
	if strings.TrimSpace(o.SnapshotRoot) == "" {
		return &Error{Code: CodeOptions, Message: "no snapshot root was given"}
	}
	if strings.TrimSpace(o.RuntimeImport) == "" {
		return &Error{Code: CodeOptions, Message: "no runtime import path was given"}
	}
	if !insideSnapshot(o.Path) {
		return &Error{
			Code: CodeOptions,
			Message: "the path " + strconv.Quote(o.Path) +
				" is not a module-relative path inside the snapshot",
		}
	}
	if o.Source == nil {
		return &Error{
			Code:    CodeOptions,
			Message: "no pristine source was given for " + strconv.Quote(o.Path),
		}
	}
	if o.Mode != ModeMutant && o.Mode != ModeProbe {
		return &Error{
			Code:    CodeOptions,
			Message: "the instrumentation mode " + strconv.Itoa(int(o.Mode)) + " is not one this package knows",
		}
	}
	for _, m := range o.Mutants {
		if m.Path != o.Path {
			return &Error{
				Code: CodeOptions,
				Message: "mutant " + m.DisplayID + " belongs to " + strconv.Quote(m.Path) +
					", not to " + strconv.Quote(o.Path),
			}
		}
	}
	return nil
}
