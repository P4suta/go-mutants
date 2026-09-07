// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"errors"
	"io/fs"
	"os"
	"sync"

	"github.com/P4suta/go-mutants/schema/stryker"
)

// This file is compiled only under `go test`.
//
// The ownership claim is the one part of the history store the public API
// cannot be made to exercise properly. [History.WorkspaceDir] derives the
// directory from the workspace digest, so two different workspaces never name
// one directory and the collision the marker exists to catch cannot be staged
// through [History.Write] at all. The tests reach past that to the claim
// itself, which is the only place the two-workspaces-one-directory race can be
// written down.

// Claim exposes the ownership claim to the tests. See [History].
func Claim(dir, workspaceDigest string) error { return claim(dir, workspaceDigest) }

// CreateMarkerInPlace exposes the claim's fallback, which no test could
// otherwise reach: it is used only on a filesystem that refuses to hard-link,
// and the machines these tests run on do not have one. What it must still be is
// a refusal rather than a replacement, and that is checkable here.
func CreateMarkerInPlace(path, content string) error { return createMarkerInPlace(path, content) }

// ErrMarkerExists exposes the sentinel both create paths answer an already
// claimed directory with.
var ErrMarkerExists = errMarkerExists

// The projection's coordinate arithmetic and the HTML page's escaping are
// exposed for the same reason as the claim: they are the two places where being
// almost right produces a document that validates, renders, and points at the
// wrong characters. A test that could only reach them through a whole run would
// have to work backwards from a rendered page to find out which of the two was
// wrong.

// UTF16Position converts a byte offset in src into the published coordinate.
func UTF16Position(src []byte, offset int) Position {
	return newSourceIndex(src).position(offset)
}

// UTF16OffsetAt converts a 1-based line and 1-based byte column back into a
// byte offset, which is how a rejected mutant's coordinate is projected.
func UTF16OffsetAt(src []byte, line, column int) int {
	return newSourceIndex(src).offsetAt(line, column)
}

// UTF16Units counts the UTF-16 code units in s.
func UTF16Units(s string) int { return utf16Units(s) }

// EscapeScriptData exposes the JSON island's escaping.
func EscapeScriptData(document []byte) []byte { return escapeScriptData(document) }

// Bootstrap is the page's own script, whose bytes the Content-Security-Policy
// hashes.
const Bootstrap = bootstrap

// BreakVendoredViewer makes the vendored-asset check fail, and returns the
// function that puts it back. See [verifyViewer] for why the seam exists; a
// test that uses it cannot be parallel.
func BreakVendoredViewer(cause error) func() {
	return UseVendoredViewerCheck(func() (string, error) { return "", cause })
}

// UseVendoredViewerCheck replaces the vendored-asset check outright, so that a
// test can act at the one moment the two artefacts are half published: the
// projection is on disk and the page has not been rendered yet. It is what
// stages a rollback that fails. A test that uses it cannot be parallel.
func UseVendoredViewerCheck(check func() (string, error)) func() {
	previous := verifyViewer
	verifyViewer = check
	return func() { verifyViewer = previous }
}

// UseStrykerSchema compiles the projection's validator from source instead of
// from the vendored file, and returns the function that puts it back. See
// [strykerSchemaSource] for why the seam exists; a test that uses it cannot be
// parallel.
//
// The compilation is behind a [sync.Once], so both the swap and the restore
// reset it: a schema compiled from one source and then read back under another
// would answer for whichever test ran first. Restoring resets rather than
// remembers, because the vendored schema is what the next caller wants and a
// sync.Once cannot be copied back.
func UseStrykerSchema(source func() []byte) func() {
	resetStrykerSchema()
	strykerSchemaSource = source
	return func() {
		strykerSchemaSource = stryker.Schema
		resetStrykerSchema()
	}
}

// resetStrykerSchema forgets whatever the last compilation reached, so that the
// next call to compiledStrykerSchema runs again.
func resetStrykerSchema() {
	strykerSchemaOnce = sync.Once{}
	strykerSchema = nil
	strykerSchemaErr = nil
}

// The projection's schema diagnostics are exposed for the reason the coordinate
// arithmetic above is: they are what a reader of a refusal acts on, and being
// almost right produces a sentence that looks like a location and names the
// wrong one. Neither can be reached through [ValidateProjection] with an
// argument a test chooses — the validator decides which error tree it hands
// back, and it branches in map iteration order.

// FirstFailure exposes the choice of which violation to name.
func FirstFailure(err error) string { return firstFailure(err) }

// PointerOf exposes the RFC 6901 rendering, including the answer for a
// violation at the document root.
func PointerOf(tokens []string) string { return pointerOf(tokens) }

// The store's failure injection, and the path arithmetic underneath it.
//
// Everything below is compiled only under `go test`. The two seams it drives —
// [tempFile]'s creations and [readDir] — are the ones whose comments say why
// they exist; the path helpers are exported because they are pure functions of
// two strings with a promise written out at length above each of them, and
// because the only caller reaches them with a store root and a workspace
// directory it has just opened a file under, so no argument a test could choose
// gets past that caller.

// A TempFileStep names one of the four things this package does to a file it
// has just created.
type TempFileStep string

// The steps [FailTempFiles] can make fail.
const (
	TempCreate TempFileStep = "create"
	TempWrite  TempFileStep = "write"
	TempSync   TempFileStep = "sync"
	TempClose  TempFileStep = "close"
	// TempVanish is not a refusal: the file is created, written, flushed and
	// closed for real, and is then removed, so the name the caller is handed
	// back is not there any more. It is somebody's cache cleaner sweeping a
	// recognisable temporary file out from under a write in progress.
	TempVanish TempFileStep = "vanish"
)

// ErrInjectedIO is what a staged file failure reports, so that a test can prove
// the cause is still reachable through errors.Is.
var ErrInjectedIO = errors.New("the operating system refused")

// FailTempFiles makes the (skip+1)-th file this package creates fail at step,
// and returns the function that puts the real creations back. A test that uses
// it cannot be parallel.
//
// It is exactly one creation rather than all of them from there on, because
// several of the writes here fall back to a second one: a claim that cannot
// hard-link creates the marker in place instead, and a publication that cannot
// write its page puts the previous document back. Failing every creation would
// stage both halves at once and prove neither.
func FailTempFiles(step TempFileStep, skip int) func() {
	previousTemp, previousMarker := createTemp, openMarker
	created := 0
	stage := func(file tempFile, err error) (tempFile, error) {
		if err != nil {
			return nil, err
		}
		created++
		if created != skip+1 {
			return file, nil
		}
		switch step {
		case TempCreate:
			name := file.Name()
			_ = file.Close()
			_ = os.Remove(name)
			return nil, ErrInjectedIO
		case TempVanish:
			return &vanishingFile{real: file}, nil
		default:
			return &refusingFile{real: file, step: step}, nil
		}
	}
	createTemp = func(dir, pattern string) (tempFile, error) { return stage(previousTemp(dir, pattern)) }
	openMarker = func(path string) (tempFile, error) { return stage(previousMarker(path)) }
	return func() { createTemp, openMarker = previousTemp, previousMarker }
}

// BeforeTempFile runs act just before the (skip+1)-th file this package
// creates, and returns the function that puts the real creations back.
//
// It is how a test stands in the middle of one of these writes, which is the
// only place some of them can be stood: the ownership claim reads a marker,
// finds none, and creates one, and what it must do when a marker appears
// between those two steps is the whole reason the claim is not a rename. A test
// that uses it cannot be parallel.
func BeforeTempFile(skip int, act func()) func() {
	previousTemp, previousMarker := createTemp, openMarker
	seen := 0
	before := func() {
		seen++
		if seen == skip+1 {
			act()
		}
	}
	createTemp = func(dir, pattern string) (tempFile, error) { before(); return previousTemp(dir, pattern) }
	openMarker = func(path string) (tempFile, error) { before(); return previousMarker(path) }
	return func() { createTemp, openMarker = previousTemp, previousMarker }
}

// A vanishingFile is a real file that is removed once it has been written, so
// that the name handed back names nothing. See [TempVanish].
type vanishingFile struct{ real tempFile }

func (f *vanishingFile) Name() string                { return f.real.Name() }
func (f *vanishingFile) Write(p []byte) (int, error) { return f.real.Write(p) }
func (f *vanishingFile) Sync() error                 { return f.real.Sync() }

func (f *vanishingFile) Close() error {
	err := f.real.Close()
	_ = os.Remove(f.real.Name())
	return err
}

// A refusingFile is a real file that refuses one of the three operations
// performed on it. Everything else is done for real, so that what is left on
// disk afterwards is what a run leaves after that failure.
type refusingFile struct {
	real tempFile
	step TempFileStep
}

func (f *refusingFile) Name() string { return f.real.Name() }

func (f *refusingFile) Write(p []byte) (int, error) {
	if f.step == TempWrite {
		return 0, ErrInjectedIO
	}
	return f.real.Write(p)
}

func (f *refusingFile) Sync() error {
	if f.step == TempSync {
		return ErrInjectedIO
	}
	return f.real.Sync()
}

func (f *refusingFile) Close() error {
	err := f.real.Close()
	if f.step == TempClose {
		return ErrInjectedIO
	}
	return err
}

// FailReadDir makes the directory listings in this package answer err for dir
// and behave normally everywhere else. A test that uses it cannot be parallel.
func FailReadDir(dir string, err error) func() {
	previous := readDir
	readDir = func(name string) ([]fs.DirEntry, error) {
		if name == dir {
			return nil, err
		}
		return previous(name)
	}
	return func() { readDir = previous }
}

// StubReadDir makes the directory listings answer with entries for dir, so that
// a test can hand over an entry whose Info fails — the file that went away
// between the listing and the stat. A test that uses it cannot be parallel.
func StubReadDir(dir string, entries []fs.DirEntry) func() {
	previous := readDir
	readDir = func(name string) ([]fs.DirEntry, error) {
		if name == dir {
			return entries, nil
		}
		return previous(name)
	}
	return func() { readDir = previous }
}

// VanishedEntry is a directory entry whose Info reports the named failure: the
// race the walks have to tolerate, and the one they must not.
func VanishedEntry(name string, cause error) fs.DirEntry {
	return vanishedEntry{name: name, cause: cause}
}

type vanishedEntry struct {
	name  string
	cause error
}

func (e vanishedEntry) Name() string               { return e.name }
func (e vanishedEntry) IsDir() bool                { return false }
func (e vanishedEntry) Type() fs.FileMode          { return 0 }
func (e vanishedEntry) Info() (fs.FileInfo, error) { return nil, e.cause }

// The path arithmetic that decides whether a deletion is inside the store.

// Within reports whether path is strictly inside root, as the filesystem
// resolves the two.
func Within(path, root string) (bool, error) { return within(path, root) }

// ResolvePath resolves every symbolic link in a path, tolerating a path that is
// not all there.
func ResolvePath(path string) (string, error) { return resolvePath(path) }

// ResolveParent resolves the directories leading to path and leaves its last
// element alone.
func ResolveParent(path string) (string, error) { return resolveParent(path) }

// RemoveInside deletes one path, having proved it is inside root.
func RemoveInside(path, root string) error { return removeInside(path, root) }

// TrimExtendedPrefix drops the `\\?\` Windows uses for a long path. It is
// exported because nothing on any other platform can reach the branches it is
// made of, and being wrong there would compare a resolved path and a resolved
// root in two spellings.
func TrimExtendedPrefix(path string) string { return trimExtendedPrefix(path) }

// ReasonOf renders a refusal for a Skipped row. It is exported because the row
// it renders is a line in `report list`, and the only errors the caller can
// hand it are this package's own — so the fallback for an error with no code in
// front of it is unreachable from outside and is still what must happen.
func ReasonOf(err error) string { return reasonOf(err) }

// ShardName and Undo are the two renderings a merge and a publication reach for
// when something has gone wrong, and each has a branch its own caller cannot
// take: every caller of shardName has already refused a report with no `shard`
// block, and the only rollback undo is ever given returns this package's own
// coded error or nothing. Both branches are what must happen anyway.

// ShardName renders a report for a message.
func ShardName(r *Report) string { return shardName(r) }

// Undo runs a rollback and returns the error the caller should report.
func Undo(rollback func() error, cause error) error { return undo(rollback, cause) }
