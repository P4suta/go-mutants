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

func Claim(dir, workspaceDigest string) error { return claim(dir, workspaceDigest) }

func CreateMarkerInPlace(path, content string) error { return createMarkerInPlace(path, content) }

var ErrMarkerExists = errMarkerExists

func UTF16Position(src []byte, offset int) Position {
	return newSourceIndex(src).position(offset)
}

func UTF16OffsetAt(src []byte, line, column int) int {
	return newSourceIndex(src).offsetAt(line, column)
}

func UTF16Units(s string) int { return utf16Units(s) }

func EscapeScriptData(document []byte) []byte { return escapeScriptData(document) }

const Bootstrap = bootstrap

func BreakVendoredViewer(cause error) func() {
	return UseVendoredViewerCheck(func() (string, error) { return "", cause })
}

func UseVendoredViewerCheck(check func() (string, error)) func() {
	previous := verifyViewer
	verifyViewer = check
	return func() { verifyViewer = previous }
}

func UseStrykerSchema(source func() []byte) func() {
	resetStrykerSchema()
	strykerSchemaSource = source
	return func() {
		strykerSchemaSource = stryker.Schema
		resetStrykerSchema()
	}
}

func resetStrykerSchema() {
	strykerSchemaOnce = sync.Once{}
	strykerSchema = nil
	strykerSchemaErr = nil
}

func FirstFailure(err error) string { return firstFailure(err) }

func PointerOf(tokens []string) string { return pointerOf(tokens) }

type TempFileStep string

const (
	TempCreate TempFileStep = "create"
	TempWrite  TempFileStep = "write"
	TempSync   TempFileStep = "sync"
	TempClose  TempFileStep = "close"
	TempVanish TempFileStep = "vanish"
)

var ErrInjectedIO = errors.New("the operating system refused")

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
		if step == TempCreate {
			name := file.Name()
			_ = file.Close()
			_ = os.Remove(name)
			return nil, ErrInjectedIO
		}
		if step == TempVanish {
			return &vanishingFile{real: file}, nil
		}
		return &refusingFile{real: file, step: step}, nil
	}
	createTemp = func(dir, pattern string) (tempFile, error) { return stage(previousTemp(dir, pattern)) }
	openMarker = func(path string) (tempFile, error) { return stage(previousMarker(path)) }
	return func() { createTemp, openMarker = previousTemp, previousMarker }
}

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

type vanishingFile struct{ real tempFile }

func (f *vanishingFile) Name() string                { return f.real.Name() }
func (f *vanishingFile) Write(p []byte) (int, error) { return f.real.Write(p) }
func (f *vanishingFile) Sync() error                 { return f.real.Sync() }

func (f *vanishingFile) Close() error {
	err := f.real.Close()
	_ = os.Remove(f.real.Name())
	return err
}

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

func Within(path, root string) (bool, error) { return within(path, root) }

func ResolvePath(path string) (string, error) { return resolvePath(path) }

func ResolveParent(path string) (string, error) { return resolveParent(path) }

func RemoveInside(path, root string) error { return removeInside(path, root) }

func TrimExtendedPrefix(path string) string { return trimExtendedPrefix(path) }

func ReasonOf(err error) string { return reasonOf(err) }

func ShardName(r *Report) string { return shardName(r) }

func Undo(rollback func() error, cause error) error { return undo(rollback, cause) }
