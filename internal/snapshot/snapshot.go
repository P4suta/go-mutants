// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package snapshot copies a source tree into a disposable working directory so.
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
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/P4suta/go-mutants/internal/glob"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/tempowner"
)

const (
	WorkspaceDomain = "go-mutants-workspace-v1"

	DirPrefix = "go-mutants-snap-"

	TreeName = "tree"

	DefaultReportDir = "reports/mutation"
)

type Options struct {
	Exclude []glob.Pattern

	ReportDir string

	DestParent string
}

type Entry struct {
	RelPath string
	Size    int64
	SHA256  string
}

type Snapshot struct {
	SourceRoot string

	Root string

	Manifest []Entry

	WorkspaceDigest string

	StableDir bool

	dir string

	owner *tempowner.Owner

	kept bool

	destParent string

	remove  func(string) error
	sleep   func(time.Duration)
	release func() error
}

func (s *Snapshot) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

func (s *Snapshot) Parent() string {
	if s == nil {
		return ""
	}
	return s.destParent
}

func Create(srcRoot string, opts Options) (*Snapshot, error) {
	absSrc, err := absPath(srcRoot)
	if err != nil {
		return nil, &Error{Code: CodeInvalidOptions, Path: srcRoot, Message: "cannot resolve the source root", Err: err}
	}
	info, err := os.Stat(ExtendedPath(absSrc))
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

	dir, stable, err := destination(opts.DestParent, absSrc)
	if err != nil {
		return nil, err
	}
	owner, err := claimDir(dir, time.Now())
	if err != nil {
		return nil, err
	}
	s := &Snapshot{
		SourceRoot: absSrc,
		Root:       filepath.Join(dir, TreeName),
		StableDir:  stable,
		dir:        dir,
		owner:      owner,
		destParent: filepath.Dir(dir),
		remove:     os.RemoveAll,
		sleep:      time.Sleep,
		release:    owner.Release,
	}
	if rootErr := makeTreeDir(ExtendedPath(s.Root), 0o700); rootErr != nil {
		return nil, s.abandon(&Error{Code: CodeDestination, Path: s.Root, Message: "cannot create the snapshot tree", Err: rootErr})
	}

	for _, d := range w.dirs {
		perm := dirPerm(d.mode)
		path := ExtendedPath(s.pathOf(d.rel))
		if mkdirErr := makeDirTree(path, perm); mkdirErr != nil {
			return nil, s.abandon(&Error{Code: CodeCopy, Path: d.rel, Message: "cannot create the directory in the snapshot", Err: mkdirErr})
		}
		if permErr := setDirPerm(path, perm); permErr != nil {
			return nil, s.abandon(&Error{Code: CodeCopy, Path: d.rel, Message: "cannot set the directory's permissions in the snapshot", Err: permErr})
		}
	}

	entries, failedPath, err := copySnapshotFiles(w.files, s.Root, snapshotCopyJobs(len(w.files)), copyFile)
	if err != nil {
		return nil, s.abandon(&Error{Code: CodeCopy, Path: failedPath, Message: "cannot copy the file into the snapshot", Err: err})
	}
	if failedPath, err := stampDirectoryTimes(w.dirs, info.ModTime(), s.Root); err != nil {
		return nil, s.abandon(&Error{Code: CodeCopy, Path: failedPath, Message: "cannot set the directory's times in the snapshot", Err: err})
	}
	s.Manifest = entries
	s.WorkspaceDigest = WorkspaceDigest(entries)
	return s, nil
}

func stampDirectoryTimes(dirs []record, rootModified time.Time, root string) (string, error) {
	for index := len(dirs) - 1; index >= 0; index-- {
		target := filepath.Join(root, filepath.FromSlash(dirs[index].rel))
		if err := stampOneDirectory(dirs[index].modTime, target); err != nil {
			return dirs[index].rel, err
		}
	}
	if err := stampOneDirectory(rootModified, root); err != nil {
		return ".", err
	}
	return "", nil
}

func stampOneDirectory(modified time.Time, target string) error {
	return setFileTimes(ExtendedPath(target), modified, modified)
}

type snapshotFileCopy func(string, string, fs.FileMode, time.Time) (int64, string, error)

func snapshotCopyJobs(files int) int {
	return min(max(runtime.GOMAXPROCS(0), 1), max(files, 1))
}

func copySnapshotFiles(files []record, root string, jobs int, copyFile snapshotFileCopy) ([]Entry, string, error) {
	if len(files) == 0 {
		return nil, "", nil
	}
	entries := make([]Entry, len(files))
	errorsByPath := make([]error, len(files))
	workerCount := min(max(jobs, 1), len(files))
	work := make(chan int, workerCount)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range work {
				file := files[index]
				dst := filepath.Join(root, filepath.FromSlash(file.rel))
				size, sum, err := copyFile(file.abs, dst, file.mode, file.modTime)
				if err != nil {
					errorsByPath[index] = err
					continue
				}
				entries[index] = Entry{RelPath: file.rel, Size: size, SHA256: sum}
			}
		}()
	}
	for index := range files {
		work <- index
	}
	close(work)
	workers.Wait()
	for index, err := range errorsByPath {
		if err != nil {
			return nil, files[index].rel, err
		}
	}
	return entries, "", nil
}

func (s *Snapshot) abandon(cause error) error {
	_ = s.Cleanup()
	return cause
}

func (s *Snapshot) pathOf(rel string) string {
	return filepath.Join(s.Root, filepath.FromSlash(rel))
}

func WorkspaceDigest(entries []Entry) string {
	h := sha256.New()
	writeLengthPrefixed(h, WorkspaceDomain)
	for _, e := range entries {
		writeLengthPrefixed(h, e.RelPath)
		writeLengthPrefixed(h, e.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeLengthPrefixed(h hash.Hash, s string) {
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(s)))
	_, _ = h.Write(prefix[:])
	_, _ = h.Write([]byte(s))
}

func exclusions(opts Options) ([]glob.Pattern, error) {
	patterns := make([]glob.Pattern, 0, len(opts.Exclude)+3)
	patterns = append(patterns, glob.MustCompile("**/.git"), glob.MustCompile(DefaultReportDir))
	if opts.ReportDir != "" {
		normalized, err := mutation.NormalizePath(opts.ReportDir)
		if err != nil {
			return nil, &Error{Code: CodeInvalidOptions, Path: opts.ReportDir, Message: "report directory is not a usable source-root-relative path", Err: err}
		}
		if normalized != DefaultReportDir {
			p, err := glob.Compile(normalized)
			if err != nil {
				return nil, &Error{Code: CodeInvalidOptions, Path: opts.ReportDir, Message: "report directory is not a usable pattern", Err: err}
			}
			patterns = append(patterns, p)
		}
	}
	return append(patterns, opts.Exclude...), nil
}

type record struct {
	rel     string
	abs     string
	mode    fs.FileMode
	modTime time.Time
}

func byRelPath(a, b record) int { return strings.Compare(a.rel, b.rel) }

type walker struct {
	root     string
	exclude  []glob.Pattern
	files    []record
	dirs     []record
	rejected []*Error
}

func (w *walker) walk(relDir string) error {
	entries, err := os.ReadDir(ExtendedPath(w.pathOf(relDir)))
	if err != nil {
		return &Error{Code: CodeWalk, Path: w.errPath(relDir), Message: "cannot read the directory", Err: err}
	}
	for _, de := range entries {
		name := de.Name()
		rel := path.Join(relDir, name)
		if w.excluded(rel) {
			continue
		}
		if bad := unsupportedName(name); bad != "" {
			w.reject(CodeUnsupportedName, rel, "refuses a file name containing "+bad)
			continue
		}
		abs := w.pathOf(rel)
		fi, err := os.Lstat(ExtendedPath(abs))
		if err != nil {
			return &Error{Code: CodeWalk, Path: w.errPath(rel), Message: "cannot stat the entry", Err: err}
		}
		mode := fi.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			w.reject(CodeSymlink, rel, "refuses to follow a symbolic link")
		case isReparsePoint(fi):
			w.reject(CodeReparsePoint, rel, "refuses to follow a reparse point (junction or mount point)")
		case mode.IsDir():
			w.dirs = append(w.dirs, record{rel: rel, abs: abs, mode: mode, modTime: fi.ModTime()})
			if err := w.walk(rel); err != nil {
				return err
			}
		case mode.IsRegular():
			w.files = append(w.files, record{rel: rel, abs: abs, mode: mode, modTime: fi.ModTime()})
		default:
			w.reject(CodeIrregular, rel, fmt.Sprintf("refuses a file that is neither a directory nor a regular file (mode %s)", mode.Type()))
		}
	}
	return nil
}

func (w *walker) pathOf(rel string) string {
	return filepath.Join(w.root, filepath.FromSlash(rel))
}

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

func (w *walker) rejection() error {
	if len(w.rejected) == 0 {
		return nil
	}
	return slices.MinFunc(w.rejected, func(a, b *Error) int {
		return strings.Compare(a.Path, b.Path)
	})
}

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

func copyFile(src, dst string, mode fs.FileMode, modified time.Time) (int64, string, error) {
	in, err := os.Open(ExtendedPath(src))
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = in.Close() }()

	perm := copyPerm(mode)
	out, err := os.OpenFile(ExtendedPath(dst), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(out, h), in)
	if err != nil {
		_ = out.Close()
		return 0, "", err
	}
	if err := finalizeCopyPerm(out, perm); err != nil {
		_ = out.Close()
		return 0, "", err
	}
	if err := closeCopy(out); err != nil {
		return 0, "", err
	}
	if err := setFileTimes(ExtendedPath(dst), modified, modified); err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

func hashFile(abs string) (int64, string, error) {
	f, err := os.Open(ExtendedPath(abs))
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
