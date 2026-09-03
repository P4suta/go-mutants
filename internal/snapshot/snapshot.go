// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package snapshot copies a source tree into a disposable working directory so
// that mutation testing never writes to the tree a user is editing.
//
// Every other design for a mutation tester ends up rewriting the sources in
// place and restoring them afterwards. That is a promise no process can keep:
// a crash, a SIGKILL, a full disk, or a second run racing the first leaves the
// user's checkout holding mutated code. go-mutants instead treats the user's
// tree as strictly read-only. It is copied once, instrumented in the copy, and
// the copy is deleted at the end of the run. The worst outcome of a crash is a
// directory left behind in the temporary area.
//
// # What a snapshot is
//
// [Create] walks the source tree, copies it byte for byte into a fresh
// os.MkdirTemp directory, and returns a [Snapshot] holding three things: the
// root of the copy, a sorted [Entry] manifest, and the [Snapshot.WorkspaceDigest]
// that names the tree's exact contents. The digest is what makes the outcome
// cache trustworthy — a cached result is only reusable for a workspace whose
// every byte hashes the same — and what proves two shards of one run looked at
// the same code.
//
// # Refusals
//
// The walk copies directories and regular files. Everything else is refused
// with a typed [Error] naming the path: symbolic links, Windows junctions and
// every other reparse point, devices, sockets, and named pipes.
//
// Refusing rather than skipping is the point. A skipped link is a file that
// silently is not there: the copy still compiles often enough to be believed,
// the mutants that would have lived behind the link never appear in the
// catalogue, and the score comes out flattering and wrong. A link is also the
// one way a copy can escape its own root, which would turn "the tree is
// read-only" into a lie. Neither failure announces itself, so the walk
// announces it instead, and the user decides — usually by adding an exclude
// pattern, which is checked before the entry is even stat'ed.
//
// # Exclusions
//
// [Options.Exclude] holds compiled internal/glob patterns, matched against the
// '/'-normalized path of each entry relative to the source root. A directory
// that matches is not descended into at all, so "vendor/**" — which the glob
// language defines to match the bare path "vendor" as well as everything under
// it — costs one comparison rather than one per file underneath.
//
// Three exclusions are always applied, before the caller's:
//
//   - ".git" at any depth, which is never input to a build and is large;
//   - the conventional report directory "reports/mutation";
//   - [Options.ReportDir], when the caller configured a different one.
//
// The middle one is deliberately unconditional, so a run cannot snapshot the
// reports of a previous run just because the report directory was reconfigured
// after the fact. The cost is that a project keeping real Go source under
// "reports/mutation" would find it missing from the snapshot; that is judged
// less surprising than a snapshot that grows a copy of its own output.
//
// # The workspace digest
//
// The recipe is frozen. Over the manifest in sorted order:
//
//	SHA-256( enc("go-mutants-workspace-v1")
//	         || enc(relPath_1) || enc(sha256hex_1)
//	         || ...
//	         || enc(relPath_n) || enc(sha256hex_n) )
//
// where enc(s) is a 4-byte big-endian uint32 of the UTF-8 byte length of s
// followed by the raw UTF-8 bytes of s. Sizes are not hashed: a file's SHA-256
// already pins its bytes, and the size is carried in the manifest for
// reporting only. Directories are not hashed either, since an empty directory
// cannot change what a build produces. Length prefixes make the concatenation
// unambiguous, exactly as in the mutant identity recipe in internal/mutation.
//
// The domain separator carries the version. A future recipe becomes
// "go-mutants-workspace-v2" so that a v1 digest can never be mistaken for a v2
// digest in a cache that outlived the upgrade.
package snapshot

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/glob"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/tempowner"
)

const (
	// WorkspaceDomain is the domain separator hashed first for every workspace
	// digest. It carries the recipe version; see the package documentation.
	WorkspaceDomain = "go-mutants-workspace-v1"

	// DirPrefix is the os.MkdirTemp prefix of every snapshot directory. It is
	// also load bearing: [Snapshot.Cleanup] refuses to delete a directory
	// whose name does not start with it, and internal/tempowner's sweep
	// collects the abandoned ones by it.
	DirPrefix = "go-mutants-snap-"

	// TreeName is the subdirectory of a snapshot directory that holds the copy.
	//
	// The copy is one level down rather than at the top because the snapshot
	// directory also carries its ownership files, and every byte under
	// [Snapshot.Root] has to be a byte that came from the source tree.
	// [Snapshot.Redigest] deliberately applies no exclusions, so an owner
	// marker beside the sources would be reported as drift by every run that
	// checks; and a snapshot of a snapshot — which is exactly how the probe
	// tree is made — would copy the marker and hash a manifest that no longer
	// described the tree it came from.
	TreeName = "tree"

	// DefaultReportDir is the conventional location of a run's reports, and is
	// excluded from every snapshot whether or not it is the configured one.
	DefaultReportDir = "reports/mutation"
)

// Options configures [Create]. The zero value is usable: it excludes only the
// built-in defaults and puts the snapshot under the operating system's
// temporary directory.
type Options struct {
	// Exclude holds patterns matched against each entry's '/'-normalized path
	// relative to the source root. A matching directory is skipped whole.
	Exclude []glob.Pattern

	// ReportDir is the configured report directory as a source-root-relative
	// path. Empty means the default. It is excluded in addition to, never
	// instead of, [DefaultReportDir].
	ReportDir string

	// DestParent is the directory the snapshot is created in. Empty means the
	// operating system temporary directory, which is what a real run uses;
	// tests set it to keep their debris inside t.TempDir().
	DestParent string
}

// An Entry is one regular file in a snapshot's manifest.
type Entry struct {
	// RelPath is the path relative to the snapshot root, with forward slashes
	// on every platform.
	RelPath string
	// Size is the number of bytes copied.
	Size int64
	// SHA256 is the lowercase hex SHA-256 of the file's bytes.
	SHA256 string
}

// A Snapshot is a disposable copy of a source tree.
//
// A Snapshot is immutable once [Create] returns, apart from what the rest of
// the pipeline writes into the directory itself, and safe for concurrent
// reading. It owns an operating system resource, so a caller that creates one
// is responsible for [Snapshot.Cleanup]; the usual spelling is a deferred call
// on the line after the error check.
type Snapshot struct {
	// SourceRoot is the absolute path of the tree that was copied.
	SourceRoot string

	// Root is the absolute path of the copy. Everything downstream — the
	// build, the test binaries' working directories, the instrumented
	// rewrites — happens under here. It is [TreeName] inside [Snapshot.Dir].
	Root string

	// Manifest lists every regular file in the snapshot, sorted by RelPath.
	// Directories are not listed; they are recreated faithfully in the copy
	// but contribute nothing a build can observe.
	Manifest []Entry

	// WorkspaceDigest is the frozen digest of Manifest described in the
	// package documentation.
	WorkspaceDigest string

	// dir is the temporary directory this package owns: it holds the copy in
	// TreeName and the internal/tempowner lock and marker beside it. It is the
	// path Cleanup removes and the path the guard is about.
	dir string

	// owner is the lock and marker held for dir's whole lifetime. It is nil in
	// a Snapshot a caller assembled by hand, which every method treats as
	// "there is nothing to release".
	owner *tempowner.Owner

	// kept records a Keep, so that the deferred Cleanup calls this codebase is
	// full of cannot undo a deliberate decision to preserve the directory.
	kept bool

	// destParent is the directory dir was created in, remembered for the
	// Cleanup guard rather than re-derived, so that a caller mutating dir
	// cannot talk Cleanup into removing something else.
	destParent string

	// remove and sleep are the two seams the retry loop in Cleanup is tested
	// through. They are nil in a Snapshot a caller assembled by hand, which
	// the accessors below treat as "use the real thing".
	remove func(string) error
	sleep  func(time.Duration)
}

// Dir is the temporary directory this snapshot owns: [Snapshot.Root] and the
// ownership files live in it, and [Snapshot.Cleanup] removes it whole.
func (s *Snapshot) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Parent is the directory [Snapshot.Dir] was created in — the caller's
// DestParent, or the operating system temporary directory when there was none.
//
// It is the answer to "where does a sibling of this snapshot belong", which is
// a question the scratch directories beside it have to ask and must not answer
// with filepath.Dir(Root): that is the snapshot's own directory.
func (s *Snapshot) Parent() string {
	if s == nil {
		return ""
	}
	return s.destParent
}

// Create copies the tree rooted at srcRoot into a fresh temporary directory.
//
// The source tree is only ever read. On any failure after the destination
// exists, the partial copy is removed before the error is returned, so a
// failed Create leaves nothing behind.
func Create(srcRoot string, opts Options) (*Snapshot, error) {
	absSrc, err := filepath.Abs(srcRoot)
	if err != nil {
		return nil, &Error{Code: CodeInvalidOptions, Path: srcRoot, Message: "cannot resolve the source root", Err: err}
	}
	// Stat, not Lstat: a user whose whole checkout lives behind a symlink has
	// made that choice deliberately, and the rejection rule is about links
	// discovered inside the tree, where nobody chose anything.
	info, err := os.Stat(extendedPath(absSrc))
	if err != nil {
		return nil, &Error{Code: CodeSourceRoot, Path: absSrc, Message: "cannot read the source root", Err: err}
	}
	if !info.IsDir() {
		return nil, &Error{Code: CodeSourceRoot, Path: absSrc, Message: "source root is not a directory"}
	}

	patterns, err := exclusions(opts)
	if err != nil {
		return nil, err
	}

	w := &walker{root: absSrc, exclude: patterns}
	if walkErr := w.walk(""); walkErr != nil {
		return nil, walkErr
	}
	if rejected := w.rejection(); rejected != nil {
		return nil, rejected
	}
	slices.SortFunc(w.files, byRelPath)
	slices.SortFunc(w.dirs, byRelPath)

	created, err := os.MkdirTemp(opts.DestParent, DirPrefix)
	if err != nil {
		return nil, &Error{Code: CodeDestination, Path: opts.DestParent, Message: "cannot create the snapshot directory", Err: err}
	}
	// The directory is absolute whatever DestParent was. A relative DestParent
	// would otherwise produce a relative path, which the Cleanup guard refuses
	// on sight — the snapshot would be perfectly usable and impossible to
	// delete. It also means every consumer downstream, which will hand this
	// path to subprocesses running in their own working directories, gets a
	// path that still means the same thing there.
	dir, err := filepath.Abs(created)
	if err != nil {
		_ = os.RemoveAll(created)
		return nil, &Error{Code: CodeDestination, Path: created, Message: "cannot resolve the snapshot directory", Err: err}
	}
	// Claimed before a single byte is copied into it. A directory that holds a
	// copy of somebody's module and says nothing about who is using it is
	// exactly the orphan the sweep exists to collect, and the window in which
	// it could be one is the window between these two lines.
	owner, err := tempowner.Claim(dir, time.Now())
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, &Error{Code: CodeDestination, Path: dir, Message: "cannot claim the snapshot directory", Err: err}
	}
	s := &Snapshot{
		SourceRoot: absSrc,
		Root:       filepath.Join(dir, TreeName),
		dir:        dir,
		owner:      owner,
		// The directory the snapshot was created in, taken from the path that
		// was actually created rather than from the option, so the guard
		// compares against where the snapshot really is and not where it was
		// asked to be. os.MkdirTemp("") lands in the operating system
		// temporary directory, which the guard allows in its own right.
		destParent: filepath.Dir(dir),
		remove:     os.RemoveAll,
		sleep:      time.Sleep,
	}
	// The tree is created explicitly rather than by the first MkdirAll below,
	// so that a source tree with no subdirectories at all still produces a Root
	// that exists.
	if err := os.Mkdir(extendedPath(s.Root), 0o700); err != nil {
		return nil, s.abandon(&Error{Code: CodeDestination, Path: s.Root, Message: "cannot create the snapshot tree", Err: err})
	}

	// Directories first, in sorted order, which puts every parent before its
	// children. Creating them explicitly rather than on demand from the file
	// loop is what keeps an empty directory in the copy: a build can depend on
	// one existing (an embed root, a testdata directory a test writes into)
	// even though nothing in it hashes.
	for _, d := range w.dirs {
		perm := dirPerm(d.mode)
		path := extendedPath(s.pathOf(d.rel))
		if err := os.MkdirAll(path, perm); err != nil {
			return nil, s.abandon(&Error{Code: CodeCopy, Path: d.rel, Message: "cannot create the directory in the snapshot", Err: err})
		}
		// MkdirAll's mode is a request the kernel filters through the process
		// umask, so it is only a ceiling on what was created: under umask 077 a
		// 0o755 source directory lands 0o700. finalizeDirPerm is the explicit
		// chmod that makes it exact, and is a no-op on Windows. Sorted order
		// means MkdirAll created exactly the leaf, so chmod'ing the leaf is the
		// whole of it.
		if err := finalizeDirPerm(path, perm); err != nil {
			return nil, s.abandon(&Error{Code: CodeCopy, Path: d.rel, Message: "cannot set the directory's permissions in the snapshot", Err: err})
		}
	}

	entries := make([]Entry, 0, len(w.files))
	for _, f := range w.files {
		size, sum, err := copyFile(f.abs, s.pathOf(f.rel), f.mode)
		if err != nil {
			return nil, s.abandon(&Error{Code: CodeCopy, Path: f.rel, Message: "cannot copy the file into the snapshot", Err: err})
		}
		entries = append(entries, Entry{RelPath: f.rel, Size: size, SHA256: sum})
	}
	s.Manifest = entries
	s.WorkspaceDigest = WorkspaceDigest(entries)
	return s, nil
}

// abandon removes a half-built snapshot and returns the error that caused it
// to be abandoned.
//
// It goes through Cleanup rather than straight to os.RemoveAll so the guard
// and the retry ladder apply here too — this path runs on a machine that has
// just failed to copy a file, which is exactly where a lock or a permission
// problem is likely. The removal is best effort: reporting a failure to tidy
// up a directory the caller never saw would bury the error that matters.
func (s *Snapshot) abandon(cause error) error {
	_ = s.Cleanup()
	return cause
}

// pathOf turns a '/'-normalized snapshot-relative path into an absolute native
// path inside the snapshot.
func (s *Snapshot) pathOf(rel string) string {
	return filepath.Join(s.Root, filepath.FromSlash(rel))
}

// WorkspaceDigest computes the frozen digest of a manifest. The entries must
// already be sorted by RelPath, which is how [Create] returns them; this
// function hashes what it is given and does not reorder, because a digest that
// quietly repaired its input could not be reimplemented from the recipe.
func WorkspaceDigest(entries []Entry) string {
	h := sha256.New()
	writeLengthPrefixed(h, WorkspaceDomain)
	for _, e := range entries {
		writeLengthPrefixed(h, e.RelPath)
		writeLengthPrefixed(h, e.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeLengthPrefixed writes enc(s) to h: a 4-byte big-endian byte length
// followed by the raw UTF-8 bytes.
//
// This is byte for byte the encoding used by the mutant identity recipe in
// internal/mutation/id.go, and it is duplicated here rather than shared on
// purpose. The two recipes are frozen separately and versioned separately; a
// shared helper would let a change made for one silently re-mint the other,
// which is precisely the accident both version prefixes exist to prevent.
//
// The identity version returns an error for a string whose length overflows
// the 32-bit prefix, because it hashes user-supplied rule metadata. Everything
// hashed here is a path or a 64-character digest that an operating system
// already length-limited far below four gigabytes, so the conversion is exact
// and there is no error to report.
func writeLengthPrefixed(h hash.Hash, s string) {
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(s)))
	// hash.Hash never returns an error, as documented on the interface.
	_, _ = h.Write(prefix[:])
	_, _ = h.Write([]byte(s))
}

// exclusions builds the pattern list: the always-on defaults first, then the
// caller's, since order only affects how quickly a match is found.
func exclusions(opts Options) ([]glob.Pattern, error) {
	patterns := make([]glob.Pattern, 0, len(opts.Exclude)+3)
	// "**/.git" rather than ".git" so a nested module or a submodule's
	// metadata is caught at any depth. "**" matches zero elements, so this
	// still matches the ".git" at the root.
	patterns = append(patterns, glob.MustCompile("**/.git"), glob.MustCompile(DefaultReportDir))
	if opts.ReportDir != "" {
		// NormalizePath is the same canonicalization identities use, so a
		// report directory spelled with backslashes on Windows excludes the
		// same tree it would on POSIX, and an absolute or escaping one is
		// refused here rather than silently excluding nothing.
		normalized, err := mutation.NormalizePath(opts.ReportDir)
		if err != nil {
			return nil, &Error{Code: CodeInvalidOptions, Path: opts.ReportDir, Message: "report directory is not a usable source-root-relative path", Err: err}
		}
		if normalized != DefaultReportDir {
			// A configured directory is compiled as a pattern rather than
			// compared as a literal so that exclusion has exactly one code
			// path. The visible consequence is that a '*' in the configured
			// name behaves as a wildcard.
			p, err := glob.Compile(normalized)
			if err != nil {
				return nil, &Error{Code: CodeInvalidOptions, Path: opts.ReportDir, Message: "report directory is not a usable pattern", Err: err}
			}
			patterns = append(patterns, p)
		}
	}
	return append(patterns, opts.Exclude...), nil
}

// A record is one entry the walk decided to keep.
type record struct {
	rel  string
	abs  string
	mode fs.FileMode
}

func byRelPath(a, b record) int { return strings.Compare(a.rel, b.rel) }

// A walker collects a tree into sorted directory and file lists, and collects
// the entries it refuses instead of failing at the first one.
type walker struct {
	root     string
	exclude  []glob.Pattern
	files    []record
	dirs     []record
	rejected []*Error
}

// walk reads one directory and recurses. os.ReadDir sorts by file name, so the
// traversal is already deterministic; the caller sorts the results by relative
// path afterwards because per-directory name order and whole-path order are
// not the same ordering ("a.go" sorts before "a/b" but is visited after it).
func (w *walker) walk(relDir string) error {
	entries, err := os.ReadDir(extendedPath(w.pathOf(relDir)))
	if err != nil {
		return &Error{Code: CodeWalk, Path: w.errPath(relDir), Message: "cannot read the directory", Err: err}
	}
	for _, de := range entries {
		name := de.Name()
		rel := name
		if relDir != "" {
			rel = relDir + "/" + name
		}
		if w.excluded(rel) {
			continue
		}
		if bad := unsupportedName(name); bad != "" {
			w.reject(CodeUnsupportedName, rel, "refuses a file name containing "+bad)
			continue
		}
		abs := w.pathOf(rel)
		// Lstat rather than de.Info(): the DirEntry a directory listing yields
		// on Windows reconstructs its mode from FILE_ID_BOTH_DIR_INFO, and the
		// whole rejection policy hangs on that mode being exact. One extra
		// syscall per entry buys the question away.
		fi, err := os.Lstat(extendedPath(abs))
		if err != nil {
			return &Error{Code: CodeWalk, Path: w.errPath(rel), Message: "cannot stat the entry", Err: err}
		}
		mode := fi.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			w.reject(CodeSymlink, rel, "refuses to follow a symbolic link")
		case isReparsePoint(fi):
			// A junction reaches here rather than the symlink case: Go reports
			// a name-surrogate reparse point as irregular, not as a link.
			w.reject(CodeReparsePoint, rel, "refuses to follow a reparse point (junction or mount point)")
		case mode.IsDir():
			w.dirs = append(w.dirs, record{rel: rel, abs: abs, mode: mode})
			if err := w.walk(rel); err != nil {
				return err
			}
		case mode.IsRegular():
			w.files = append(w.files, record{rel: rel, abs: abs, mode: mode})
		default:
			w.reject(CodeIrregular, rel, fmt.Sprintf("refuses a file that is neither a directory nor a regular file (mode %s)", mode.Type()))
		}
	}
	return nil
}

func (w *walker) pathOf(rel string) string {
	if rel == "" {
		return w.root
	}
	return filepath.Join(w.root, filepath.FromSlash(rel))
}

// errPath returns the spelling an [Error] about rel should carry: the
// '/'-normalized relative path wherever one exists, and the absolute root of
// the walk when it does not.
//
// The empty relative path is the root itself, and it is the one failure with
// no relative spelling. It is reachable: [Snapshot.Redigest] on a snapshot
// whose directory has already been removed cannot list the root, and an error
// carrying "" for its path renders as a complaint about a directory it never
// names. [Error.Path] documents exactly this case as the one where the path is
// absolute.
//
// Every walk error is built through here so the rule lives in one place, the
// per-entry stat failure included — where it cannot fire, because os.ReadDir
// never yields an empty name and [unsupportedName] has already refused one by
// then. Naming the containing directory there instead would throw away the
// only useful fact in the message, which is which entry failed.
func (w *walker) errPath(rel string) string {
	if rel == "" {
		return w.root
	}
	return rel
}

func (w *walker) excluded(rel string) bool {
	for _, p := range w.exclude {
		if p.Match(rel) {
			return true
		}
	}
	return false
}

func (w *walker) reject(code Code, rel, message string) {
	w.rejected = append(w.rejected, &Error{Code: code, Path: rel, Message: message})
}

// rejection returns the refused entry that sorts first by relative path, or
// nil if the tree was clean. Reporting the first in path order rather than the
// first in visit order means a user who fixes it and runs again is told about
// the next one, in an order that does not depend on how the filesystem
// happened to lay the directory out.
func (w *walker) rejection() error {
	if len(w.rejected) == 0 {
		return nil
	}
	first := w.rejected[0]
	for _, e := range w.rejected[1:] {
		if e.Path < first.Path {
			first = e
		}
	}
	return first
}

// unsupportedName names the reason a directory entry cannot be represented as
// a '/'-normalized relative path, or "" if it can.
//
// A backslash in a name is possible on POSIX filesystems and would make the
// snapshot path "a\b.go" indistinguishable from the directory "a" holding
// "b.go" once internal/mutation normalizes it — two different files with one
// identity. It is refused for the same reason a symlink is: quietly picking
// one of the two meanings is worse than stopping.
func unsupportedName(name string) string {
	switch {
	case name == "" || name == "." || name == "..":
		return "no usable name"
	case strings.ContainsRune(name, '\\'):
		return `a backslash`
	case strings.ContainsRune(name, '/'):
		return `a forward slash`
	case strings.ContainsRune(name, 0):
		return "a NUL byte"
	}
	return ""
}

// copyFile copies one regular file byte for byte, hashing as it goes so the
// bytes are read once, and returns the size and lowercase hex SHA-256 of what
// was written.
//
// There is no newline translation and no byte order mark handling anywhere in
// this package. A CRLF file arrives in the snapshot as a CRLF file, because
// the line ending is part of the source digest that names every mutant in it.
func copyFile(src, dst string, mode fs.FileMode) (int64, string, error) {
	in, err := os.Open(extendedPath(src))
	if err != nil {
		return 0, "", err
	}
	// A read handle that fails to close has nothing to report: no data was at
	// risk, and the copy either produced the right digest or did not.
	defer func() { _ = in.Close() }()

	// O_EXCL: a destination that already exists means the walk produced the
	// same path twice, which is a bug worth surfacing rather than a file to
	// overwrite.
	//
	// The mode here is only a floor. The kernel filters an O_CREATE mode
	// through the process umask, so passing the source's 0o755 produces a
	// 0o750 file under umask 027 and a 0o700 file under umask 077 — the
	// creation mode can never preserve the source bits by itself. The explicit
	// finalizePerm below is what does, and it runs after the bytes are
	// written, which means the file is briefly *less* permissive than its
	// source and never more: chmod at the end can only add bits back, so the
	// safe direction is the one that happens on its own. Do not fold this back
	// into the creation mode.
	perm := copyPerm(mode)
	out, err := os.OpenFile(extendedPath(dst), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(out, h), in)
	if err != nil {
		// The copy already failed; a close error on the way out would only
		// replace a precise diagnostic with a vaguer one.
		_ = out.Close()
		return 0, "", err
	}
	// A file whose permissions could not be set is a failed copy, not a copy
	// with a footnote: a fixture script that lost its executable bit fails
	// inside the snapshot for a reason that has nothing to do with any mutant.
	// It is a no-op on Windows; see platform_windows.go.
	if err := finalizePerm(out, perm); err != nil {
		_ = out.Close()
		return 0, "", err
	}
	// The write handle's close error is checked, unlike the read handles: on a
	// buffered filesystem it is where a failed write is finally reported, and
	// a truncated file whose digest was computed from the bytes we meant to
	// write would be a snapshot that lies about itself.
	if err := out.Close(); err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

// hashFile reports the size and lowercase hex SHA-256 of a file already on
// disk. It is the read-only half of copyFile, used by [Snapshot.Redigest].
func hashFile(abs string) (int64, string, error) {
	f, err := os.Open(extendedPath(abs))
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}
