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

// FileOptions configures [InstrumentFile]. Every field is required except
// Mutants, which may be empty.
type FileOptions struct {
	// SnapshotRoot is the directory holding the copy of the module to rewrite,
	// exactly as it was given to [Instrument].
	SnapshotRoot string

	// RuntimeImport is the import path of the runtime package [Instrument]
	// generated, taken from [Result.RuntimeImport]. It is passed in rather than
	// re-derived because the directory name is a function of what was on disk
	// when the runtime was written, and re-deriving it afterwards — with that
	// directory now present — would choose a different one.
	RuntimeImport string

	// Path is the module-relative path, with forward slashes, of the one file
	// to rewrite.
	Path string

	// Source is the pristine bytes of that file: the source as the user wrote
	// it, which is what the catalogue's spans and digests describe. It is what
	// gets instrumented, and what gets written when no guard comes of it.
	//
	// Handing the bytes in rather than reading the file is what makes this
	// usable more than once on one file. The rewrite has to be composed against
	// pristine source — a catalogue span describes the user's bytes and nothing
	// else — so a caller re-guarding a file it has already guarded would
	// otherwise have to restore the file first and hope nothing read it in
	// between. Here the restore and the rewrite are the same write.
	Source []byte

	// Mutants are the catalogued mutants to guard: a subset of the mutants the
	// full catalogue holds for this file, carrying their original
	// [mutation.Mutant.Index]. An empty subset writes Source back unchanged,
	// which is how "every candidate in this file was rejected" is spelled.
	Mutants []mutation.Mutant

	// Hints are the rewrite sites discovery chose. It is the same index
	// [Options.Hints] carried — the whole run's, not this file's — because a
	// bisection hands over a different subset every time and re-indexing per
	// call would be one more thing to keep in step.
	Hints Hints

	// Mode selects which tree this file belongs to, and must be the mode
	// [Instrument] built the snapshot with. The zero value is [ModeMutant], so a
	// caller written before the probe tree existed keeps rewriting exactly what
	// it always did.
	//
	// Passing the wrong one is not caught here and cannot be: a guard and a
	// probe are both well-formed rewrites of the same bytes, so a file rewritten
	// in the other mode would compile and would measure the wrong thing. The
	// mode belongs to the pass, and the caller that chose it for [Instrument] is
	// the one that has it.
	Mode Mode
}

// InstrumentFile rewrites one file of an already-instrumented snapshot so that
// it carries guards for a subset of its catalogued mutants, and nothing else in
// the snapshot changes.
//
// It exists for compile validation. A guard is composed without type-checking
// anything, so a candidate whose mutated copy the compiler refuses produces a
// file that does not build, and the only way to learn which candidate it was is
// to rebuild the file with fewer of them until it does. That search needs to
// change one file's guards without touching anything else, and in particular
// without touching the
// generated runtime: the activation array is sized by the full catalogue and
// every guard spells its own dense index, so a runtime regenerated from a
// subset would renumber flags that instrumented files elsewhere in the tree
// still read. Nothing here writes the runtime, and nothing here chooses its
// directory — [Instrument] settled both, once, and this function is handed the
// import path it settled on.
//
// The file is always written, guards or none, because writing [FileOptions.Source]
// back is exactly what an empty subset means. It is written the way every
// rewrite in this package is written: a temporary file in the same directory
// followed by a rename, which is what keeps a read-only source file in the
// snapshot from failing the whole run.
//
// The return value is the number of guards written, on the same terms as
// [Result.GuardsByFile]: one per rewrite site, so several mutants of one
// expression count once. Zero means the file now holds its pristine bytes, with
// no runtime import — an unused import would not compile, so a fully rejected
// file has to come out as the file the user wrote.
//
// Passing anything but pristine bytes as Source is refused rather than obeyed:
// the site lookup reports [CodeSiteNotFound] when the node a hint names is no
// longer at its offset, and [Apply] reports [CodeSpliceMismatch] when it is
// there but the bytes a mutant replaces are not what the catalogue recorded.
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

	// One directory read per call rather than a cache that outlives the pass it
	// belongs to. The alias this file gets has to dodge the identifiers its
	// package block binds, and the answer is a property of its neighbours.
	names := newPackageNames()
	dir := filepath.Dir(file)
	reserved := func(pkg string) (map[string]bool, error) { return names.namesIn(dir, pkg) }

	out, guards, err := instrumentSource(
		opts.Path, opts.Source, opts.Mutants, opts.Hints, opts.RuntimeImport, reserved, opts.Mode)
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

// validate checks the options and that every mutant really belongs to the file
// being rewritten.
//
// The last check is not paranoia about types. The subsets a bisection passes
// here are slices of a catalogue being split by file and by half, and a slice
// taken from the wrong group would instrument this file with another file's
// spans: they would usually miss, and in the worst case land on bytes that
// happen to match and produce a mutant no identity in the catalogue describes.
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
	// The same refusal [Options.validate] makes, for the same reason: rewriting
	// a mutant tree for a caller that asked for something else would hand back a
	// file nobody wanted, and nothing downstream would notice.
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
