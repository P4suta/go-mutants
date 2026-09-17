// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/advisorylock"
)

type layerWritableFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type layerHooks struct {
	mkdirAll func(path string, perm os.FileMode) error

	createTemporary func(directory, pattern string) (layerWritableFile, error)

	copyBody func(destination io.Writer, source io.Reader) (int64, error)

	readFile func(path string) ([]byte, error)

	stat func(path string) (fs.FileInfo, error)

	readDir func(path string) ([]os.DirEntry, error)

	chtimes func(path string, accessed, modified time.Time) error

	remove func(path string) error

	rename func(oldPath, newPath string) error

	now func() time.Time

	lockFile func(file *os.File) (bool, error)

	unlockFile func(file *os.File) error
}

func (hooks layerHooks) resolved() layerHooks {
	if hooks.mkdirAll == nil {
		hooks.mkdirAll = os.MkdirAll
	}
	if hooks.createTemporary == nil {
		hooks.createTemporary = func(directory, pattern string) (layerWritableFile, error) {
			return os.CreateTemp(directory, pattern)
		}
	}
	if hooks.copyBody == nil {
		hooks.copyBody = io.Copy
	}
	if hooks.readFile == nil {
		hooks.readFile = os.ReadFile
	}
	if hooks.stat == nil {
		hooks.stat = func(path string) (fs.FileInfo, error) { return os.Stat(path) }
	}
	if hooks.readDir == nil {
		hooks.readDir = os.ReadDir
	}
	if hooks.chtimes == nil {
		hooks.chtimes = os.Chtimes
	}
	if hooks.remove == nil {
		hooks.remove = os.Remove
	}
	if hooks.rename == nil {
		hooks.rename = os.Rename
	}
	if hooks.now == nil {
		hooks.now = time.Now
	}
	if hooks.lockFile == nil {
		hooks.lockFile = advisorylock.Try
	}
	if hooks.unlockFile == nil {
		hooks.unlockFile = advisorylock.Release
	}
	return hooks
}

type serveHooks struct {
	now func() time.Time

	statsName func() string

	layer layerHooks
}

func (hooks serveHooks) resolved() serveHooks {
	if hooks.now == nil {
		hooks.now = time.Now
	}
	if hooks.statsName == nil {
		hooks.statsName = defaultStatsName
	}
	hooks.layer = hooks.layer.resolved()
	return hooks
}
