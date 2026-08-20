// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// Options configures [Instrument]. The zero value is not usable: every field is
// required, because none of them has a default that could not be somebody's
// working tree.
type Options struct {
	// SnapshotRoot is the directory holding the copy of the module to rewrite.
	// It is the snapshot and never the user's own tree: instrumentation edits
	// files in place.
	SnapshotRoot string

	// ModulePath is the import path of the main module at the snapshot root. It
	// is what the generated runtime package's import path is built from, and it
	// is passed in rather than read back out of go.mod because the caller
	// already learned it while discovering.
	ModulePath string

	// Catalog is the mutant set to instrument. Its dense indices are the
	// indices the generated activation array is sized by and the guards read,
	// so the catalogue instrumented here and the catalogue the runner activates
	// against must be the same one.
	Catalog *mutation.Catalog
}

// Result reports what one instrumentation pass did.
type Result struct {
	// RuntimeDir is the generated package's directory, relative to the snapshot
	// root and spelled with forward slashes.
	RuntimeDir string
	// RuntimeImport is that package's import path.
	RuntimeImport string
	// FilesInstrumented lists the module-relative paths that received guards,
	// in catalogue order.
	FilesInstrumented []string
	// GuardsByFile counts the guards written into each of those files. A guard
	// is one rewrite site: several mutants of one expression share a single
	// guard, so this is never simply the number of mutants in the file.
	GuardsByFile map[string]int
}

// Instrument rewrites a snapshot so that every catalogued mutant is present in
// it at once, dormant behind a guard.
//
// The rewrite is in place. That is what the snapshot is for: it is a disposable
// copy, and rewriting it lets one build serve every mutant, with activation
// costing an environment variable per test process instead of a rebuild.
//
// Files are edited only where the catalogue points, guards preserve the line
// number of every original byte, and each file that gains a guard also gains
// the import of the generated runtime package. Everything else in the tree —
// files with no candidates, comments, formatting, line endings — is left byte
// for byte as it was.
//
// The output is a function of the input alone: instrumenting the same snapshot
// with the same catalogue twice produces the same bytes, down to the choice of
// alias and the order of alternatives inside a guard. It is not idempotent, and
// is not meant to be: instrumenting an already-instrumented tree finds bytes
// the catalogue no longer describes and fails rather than nesting guards inside
// guards.
//
// # What this phase does not do
//
// Nothing here checks that the rewritten tree compiles. Form C produces a
// typed bool where the site may have needed a named boolean type, and that is
// left to the compile validation and bisection that follow. See the package
// documentation for why rejecting those candidates belongs to that phase and
// not to this one.
func Instrument(opts Options) (Result, error) {
	if err := opts.validate(); err != nil {
		return Result{}, err
	}

	dir, err := chooseRuntimeDir(opts.SnapshotRoot)
	if err != nil {
		return Result{}, err
	}
	importPath := opts.ModulePath + "/" + dir

	result := Result{
		RuntimeDir:    dir,
		RuntimeImport: importPath,
		GuardsByFile:  make(map[string]int),
	}
	// One cache for the whole pass: the runtime import alias each file gets has
	// to dodge every name its package already binds, and reading a directory
	// once per package rather than once per file is the difference between a
	// directory read and a quadratic one.
	names := newPackageNames()
	for _, group := range groupByPath(opts.Catalog) {
		guards, err := instrumentFile(opts.SnapshotRoot, group.path, group.mutants, importPath, names)
		if err != nil {
			return Result{}, err
		}
		if guards == 0 {
			continue
		}
		result.FilesInstrumented = append(result.FilesInstrumented, group.path)
		result.GuardsByFile[group.path] = guards
	}

	// The runtime package is written last so that a failure part way through
	// leaves a snapshot that is obviously half-rewritten rather than one that
	// looks instrumented and is not. Its directory name was settled first,
	// because every file that was rewritten imports it by that name.
	if err := writeRuntime(opts.SnapshotRoot, dir, opts.Catalog); err != nil {
		return Result{}, err
	}
	return result, nil
}

// validate checks the options and the catalogue's paths.
func (o Options) validate() error {
	if strings.TrimSpace(o.SnapshotRoot) == "" {
		return &Error{Code: CodeOptions, Message: "no snapshot root was given"}
	}
	info, err := os.Stat(o.SnapshotRoot)
	if err != nil {
		return &Error{
			Code:    CodeOptions,
			Message: "cannot read the snapshot root " + strconv.Quote(o.SnapshotRoot),
			Err:     err,
		}
	}
	if !info.IsDir() {
		return &Error{
			Code:    CodeOptions,
			Message: "the snapshot root " + strconv.Quote(o.SnapshotRoot) + " is not a directory",
		}
	}
	if strings.TrimSpace(o.ModulePath) == "" {
		return &Error{Code: CodeOptions, Message: "no module path was given"}
	}
	if o.Catalog == nil {
		return &Error{Code: CodeOptions, Message: "no catalogue was given"}
	}
	for _, m := range o.Catalog.Mutants() {
		if !insideSnapshot(m.Path) {
			return &Error{
				Code: CodeOptions,
				Message: "the catalogue names " + strconv.Quote(m.Path) +
					", which is not a module-relative path inside the snapshot",
			}
		}
	}
	return nil
}

// insideSnapshot reports whether a catalogue path names a file the snapshot
// root contains.
//
// A catalogued path is already normalized and already proved not to escape the
// module root — [mutation.Identity] refuses to hash anything else — so this is
// the second lock on the same door, and it is here because this is the package
// that writes. A path that climbed out of the snapshot would have the
// instrumenter rewriting somebody's real source instead of the copy, which is
// the one thing the snapshot exists to prevent, and it would do so with an edit
// that looks entirely routine in a log.
func insideSnapshot(p string) bool {
	if p == "" || strings.ContainsRune(p, '\\') || strings.ContainsRune(p, 0) {
		return false
	}
	if path.IsAbs(p) || filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return false
	}
	clean := path.Clean(p)
	return clean != ".." && !strings.HasPrefix(clean, "../") && clean != "."
}

// A fileGroup is one file's share of the catalogue, in catalogue order.
type fileGroup struct {
	path    string
	mutants []mutation.Mutant
}

// groupByPath splits the catalogue into per-file groups, one group per path and
// catalogue order inside each.
//
// The catalogue already sorts by path, so its mutants arrive grouped and this
// could have been a single contiguity-based pass. It is not, deliberately: that
// pass would produce two groups for one file the moment anything upstream
// ordered the catalogue differently, and the second group would re-read a file
// this pass had already rewritten. The failure would surface as a splice
// mismatch naming bytes rather than the ordering that caused it. Grouping
// through a map costs one allocation per file and cannot express that state at
// all.
//
// Paths are sorted so the result stays a pure function of the catalogue, which
// is also what makes [Result.FilesInstrumented] deterministic.
func groupByPath(catalog *mutation.Catalog) []fileGroup {
	mutants := catalog.Mutants()
	byPath := make(map[string][]mutation.Mutant, len(mutants))
	paths := make([]string, 0, len(mutants))
	for _, m := range mutants {
		if _, seen := byPath[m.Path]; !seen {
			paths = append(paths, m.Path)
		}
		byPath[m.Path] = append(byPath[m.Path], m)
	}
	slices.Sort(paths)

	groups := make([]fileGroup, 0, len(paths))
	for _, p := range paths {
		groups = append(groups, fileGroup{path: p, mutants: byPath[p]})
	}
	return groups
}

// instrumentFile rewrites one snapshot file at its own path and reports how
// many guards it received. The rewrite goes through [replaceFile] rather than
// straight onto the file, for the reason set out there.
func instrumentFile(root, srcPath string, mutants []mutation.Mutant, importPath string, names *packageNames) (int, error) {
	file := filepath.Join(root, filepath.FromSlash(srcPath))
	info, err := os.Stat(file)
	if err != nil {
		return 0, &Error{
			Code:    CodeSourceUnreadable,
			Message: "cannot read " + strconv.Quote(srcPath) + " in the snapshot",
			Err:     err,
		}
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return 0, &Error{
			Code:    CodeSourceUnreadable,
			Message: "cannot read " + strconv.Quote(srcPath) + " in the snapshot",
			Err:     err,
		}
	}

	// The package block this file's alias has to dodge lives in the directory
	// beside it, and which package that is only becomes known once the file has
	// parsed — hence a lookup the rewrite calls rather than a set it is handed.
	dir := filepath.Dir(file)
	reserved := func(pkg string) (map[string]bool, error) { return names.namesIn(dir, pkg) }

	out, guards, err := instrumentSource(srcPath, src, mutants, importPath, reserved)
	if err != nil {
		return 0, err
	}
	if guards == 0 {
		return 0, nil
	}
	if err := replaceFile(file, out, info.Mode().Perm()); err != nil {
		return 0, &Error{
			Code:    CodeWriteFailed,
			Message: "cannot write the instrumented " + strconv.Quote(srcPath),
			Err:     err,
		}
	}
	return guards, nil
}

// replaceFile writes out over file, as a temporary file in the same directory
// followed by a rename over the target.
//
// The obvious os.WriteFile is not usable here, and the reason is a precondition
// internal/snapshot states out loud on behalf of this package. A snapshot copy
// preserves the source file's permission bits on POSIX, so a repository holding
// a read-only .go file — a Perforce workspace marks unopened files read-only,
// generators emit 0444, `chmod -w` is a convention in some trees — lands that
// file read-only in the snapshot. An in-place write to it fails EACCES for
// anybody but root, aborting a whole run over a file mode that was never about
// us. A rename needs write permission on the containing directory and none at
// all on the file being replaced, which is why snapshot's dirPerm forces every
// copied directory writable.
//
// The original mode is carried over to the replacement, so instrumenting a
// snapshot does not quietly relax what its files allow.
func replaceFile(file string, out []byte, perm fs.FileMode) error {
	dir := filepath.Dir(file)
	// The name begins with a dot so the go tool ignores it, and ends in ".tmp"
	// rather than ".go" so nothing tries to compile it, in the window before
	// the rename and in the unlikely one where a crash leaves it behind.
	tmp, err := os.CreateTemp(dir, ".gomutants-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// A no-op once the rename has moved the file out from under the name, and
	// the one thing that keeps a failed write from leaving litter in a tree the
	// next phase is about to build.
	defer func() { _ = os.Remove(name) }()

	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(name, file); err != nil {
		// Windows refuses to replace a file carrying the read-only attribute,
		// whatever the directory permits. The snapshot is a disposable copy
		// this process made, so the attribute is cleared and the rename tried
		// once more; the first failure is still what gets reported if that does
		// not help, because it is the one that describes the problem.
		if chmodErr := os.Chmod(file, 0o600); chmodErr != nil {
			return err
		}
		if retry := os.Rename(name, file); retry != nil {
			return err
		}
	}

	// Through the path rather than the descriptor, so the bits land on whatever
	// now carries the name, and after the rename so that a mode with no write
	// bit cannot get in the way of it. os.Chmod is not filtered through the
	// umask the way a creation mode is, so this is exact.
	return os.Chmod(file, perm)
}

// A reservedNames reports the identifiers already bound in the package block of
// the package a file declares, so that the runtime import alias can avoid all
// of them. It is a lookup rather than a set because the package a file belongs
// to is only known once the file has been parsed, and a nil one — no directory
// to consult — means nothing outside the file is reserved.
type reservedNames func(pkg string) (map[string]bool, error)

// instrumentSource is the whole rewrite of one file, in memory: find the sites,
// compose the guards, inject the import, and prove the result before it is
// allowed anywhere near the disk.
//
// The guards and the import are applied in a single [Apply] pass over the
// pristine bytes. Every span involved was minted against those same bytes, so
// one pass keeps them all in one coordinate system, and it is [Apply] itself
// that then proves the edits do not overlap — the import section and a
// bool-valued expression cannot be the same bytes, and if they ever were, the
// splicer says so instead of writing a file that depends on which edit landed
// first.
func instrumentSource(
	srcPath string,
	src []byte,
	mutants []mutation.Mutant,
	importPath string,
	reserved reservedNames,
) ([]byte, int, error) {
	if len(mutants) == 0 {
		return src, 0, nil
	}
	file, tok, err := parseSnapshotFile(srcPath, src)
	if err != nil {
		return nil, 0, err
	}
	forest, err := buildSites(newSiteIndex(tok, file), srcPath, mutants)
	if err != nil {
		return nil, 0, err
	}

	var taken map[string]bool
	if reserved != nil && file.Name != nil {
		if taken, err = reserved(file.Name.Name); err != nil {
			return nil, 0, err
		}
	}
	renderer := &guardRenderer{path: srcPath, src: src, alias: aliasFor(file, taken)}
	splices, guards, err := renderer.render(forest)
	if err != nil {
		return nil, 0, err
	}
	if guards == 0 {
		return src, 0, nil
	}
	imports, err := importSplices(file, tok, srcPath, renderer.alias, importPath)
	if err != nil {
		return nil, 0, err
	}
	splices = append(splices, imports...)

	if !LinePreserving(splices) {
		return nil, 0, &Error{
			Code: CodeLineDrift,
			Message: "internal error: instrumenting " + strconv.Quote(srcPath) +
				" would move a line: a guard or the injected import does not replace as many line breaks as it writes",
		}
	}
	out, _, err := Apply(src, splices)
	if err != nil {
		return nil, 0, err
	}

	// Two postconditions, both cheap and both guarding an invariant that
	// nothing downstream re-checks. The line count is what the coverage mapping
	// and every reported coordinate rest on; parsing is what stands between a
	// guard-rendering bug and a snapshot that fails to build with a syntax
	// error nobody can attribute.
	if err := checkLineCount(srcPath, src, out); err != nil {
		return nil, 0, err
	}
	if err := checkParses(srcPath, out); err != nil {
		return nil, 0, err
	}
	return out, guards, nil
}

// checkLineCount is the file-level half of the line-preservation invariant:
// whatever the splices did, the instrumented file holds exactly as many line
// breaks as the file it was built from, so line N of one is line N of the
// other.
func checkLineCount(srcPath string, src, out []byte) error {
	got, want := CountLines(out), CountLines(src)
	if got == want {
		return nil
	}
	return &Error{
		Code: CodeLineDrift,
		Message: "internal error: the instrumented " + strconv.Quote(srcPath) + " holds " +
			strconv.Itoa(got) + " line breaks, the original holds " + strconv.Itoa(want),
	}
}
