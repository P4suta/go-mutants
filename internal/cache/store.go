// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

const (
	OutcomesDirName = "outcomes"

	EntryVersion = 2

	entrySuffix = ".json"

	tempPattern = "go-mutants-cache-*.tmp"

	MaxOutputTail = 16 << 10
)

var renameDelays = []time.Duration{5 * time.Millisecond, 20 * time.Millisecond, 100 * time.Millisecond}

func Root(directory string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", &Error{
			Code:    CodeUnavailable,
			Message: "the operating system's cache directory could not be determined, so there is nowhere to keep outcomes",
			Err:     err,
		}
	}
	if directory == "" {
		return filepath.Join(base, report.DirName), nil
	}
	relative, err := mutation.NormalizePath(directory)
	if err != nil {
		return "", &Error{
			Code:    CodeUnavailable,
			Message: "cache.directory " + directory + " is not a relative path under the cache root",
			Err:     err,
		}
	}
	return filepath.Join(base, filepath.FromSlash(relative)), nil
}

type Options struct {
	Root        string
	Directory   string
	Context     Context
	Timeout     time.Duration
	MemoryLimit int64
}

type Cache struct {
	root    string
	dir     string
	key     string
	context string
	timeout time.Duration
	memory  int64
}

func Open(opts Options) (*Cache, error) {
	root := opts.Root
	if root == "" {
		resolved, err := Root(opts.Directory)
		if err != nil {
			return nil, err
		}
		root = resolved
	}
	key, err := opts.Context.Key()
	if err != nil {
		return nil, err
	}
	context := key[:ContextKeyLength]
	workspace, err := report.History{Root: root}.Claim(opts.Context.WorkspaceDigest)
	if err != nil {
		return nil, &Error{
			Code:    CodeUnavailable,
			Message: "the outcome cache directory could not be claimed",
			Err:     err,
		}
	}
	dir := filepath.Join(workspace, OutcomesDirName, context)
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return nil, &Error{
			Code:    CodeUnavailable,
			Message: "the outcome cache directory " + dir + " could not be created",
			Err:     mkErr,
		}
	}
	return &Cache{
		root: root, dir: dir, key: key, context: context,
		timeout: opts.Timeout, memory: opts.MemoryLimit,
	}, nil
}

func (c *Cache) Root() string { return c.root }

func (c *Cache) Dir() string { return c.dir }

func (c *Cache) Key() string { return c.key }

func (c *Cache) ContextKey() string { return c.context }

type Entry struct {
	Version        int              `json:"version"`
	Key            string           `json:"key"`
	Context        string           `json:"context"`
	ID             string           `json:"id"`
	Outcome        mutation.Outcome `json:"outcome"`
	DurationMS     int64            `json:"duration_ms"`
	TimeoutMS      int64            `json:"timeout_ms"`
	KilledBy       string           `json:"killed_by,omitempty"`
	Attempts       int              `json:"attempts"`
	OutputTail     string           `json:"output_tail,omitempty"`
	MemoryBytes    int64            `json:"memory_bytes,omitempty"`
	MemoryExceeded bool             `json:"memory_exceeded,omitempty"`
	PeakMemory     int64            `json:"peak_memory_bytes,omitempty"`
	Diverged       bool             `json:"diverged,omitempty"`
}

func (e Entry) Duration() time.Duration { return time.Duration(e.DurationMS) * time.Millisecond }

func (e Entry) Timeout() time.Duration { return time.Duration(e.TimeoutMS) * time.Millisecond }

func (e Entry) UsableUnder(timeout time.Duration) bool {
	bound := milliseconds(timeout)
	if bound <= 0 {
		return false
	}
	if e.Diverged {
		return true
	}
	if e.Outcome == mutation.OutcomeTimedOut {
		return bound <= e.TimeoutMS
	}
	return e.DurationMS <= bound
}

func (e Entry) UsableWithin(limit int64) bool {
	switch {
	case e.MemoryExceeded:
		return limit > 0 && limit <= e.MemoryBytes
	case e.MemoryBytes <= 0:
		return true
	case e.Outcome == mutation.OutcomeKilled:
		return true
	default:
		return limit <= 0 || limit >= e.MemoryBytes
	}
}

func Cacheable(o mutation.Outcome) bool {
	switch o {
	case mutation.OutcomeKilled, mutation.OutcomeSurvived, mutation.OutcomeTimedOut:
		return true
	case mutation.OutcomeErrored, mutation.OutcomeInconclusive, mutation.OutcomeNotRun:
	}
	return false
}

func (c *Cache) Lookup(id string) (Entry, bool, error) {
	path, err := c.path(id)
	if err != nil {
		return Entry{}, false, err
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Entry{}, false, nil
	case err != nil:
		return Entry{}, false, &Error{
			Code:    CodeCorruptEntry,
			Message: "the cached outcome " + path + " could not be read, so the mutant will be measured again",
			Err:     err,
		}
	}
	var entry Entry
	if err = json.Unmarshal(data, &entry); err != nil {
		return Entry{}, false, &Error{
			Code:    CodeCorruptEntry,
			Message: "the cached outcome " + path + " is not a cache entry, so the mutant will be measured again",
			Err:     err,
		}
	}
	if err = entry.check(c.key, c.context, id); err != nil {
		return Entry{}, false, &Error{
			Code:    CodeCorruptEntry,
			Message: "the cached outcome " + path + " will not be reused: " + err.Error(),
		}
	}
	if !entry.UsableUnder(c.timeout) || !entry.UsableWithin(c.memory) {
		return Entry{}, false, nil
	}
	return entry, true, nil
}

func (e Entry) check(key, context, id string) error {
	switch {
	case e.Version != EntryVersion:
		return versionError(e.Version)
	case e.Key != key:
		return errors.New("it was written under another cache key")
	case e.Context != context:
		return errors.New("it was written for another cache context")
	case e.ID != id:
		return errors.New("it holds the outcome of another mutant")
	case !Cacheable(e.Outcome):
		return errors.New("it holds " + e.Outcome.String() + ", which is not a reusable outcome")
	case e.DurationMS < 0 || e.Attempts < 1 || e.TimeoutMS <= 0 || e.MemoryBytes < 0 || e.PeakMemory < 0:
		return errors.New("its measurement is not one that could have happened")
	case e.MemoryExceeded && (e.MemoryBytes <= 0 || e.Outcome != mutation.OutcomeKilled):
		return errors.New("it says a memory bound settled it and records no bound, or an outcome a bound cannot produce")
	case e.Diverged && e.Outcome != mutation.OutcomeTimedOut:
		return errors.New("it says a counted loop settled it beside an outcome a loop cannot produce")
	}
	return nil
}

func versionError(version int) error {
	return errors.New("it is a version " + strconv.Itoa(version) +
		" entry and this build writes version " + strconv.Itoa(EntryVersion))
}

func (c *Cache) Put(id string, entry Entry) error {
	path, err := c.path(id)
	if err != nil {
		return err
	}
	entry.Version = EntryVersion
	entry.Key = c.key
	entry.Context = c.context
	entry.ID = id
	entry.TimeoutMS = milliseconds(c.timeout)
	entry.MemoryBytes = c.memory
	entry.OutputTail = truncateTail(entry.OutputTail)
	if !Cacheable(entry.Outcome) {
		return &Error{
			Code: CodeEntryNotWritten,
			Message: "the outcome " + entry.Outcome.String() + " of mutant " + display(id) +
				" is not one a later run may reuse, so it was not stored",
		}
	}
	if err = entry.check(c.key, c.context, id); err != nil {
		return &Error{
			Code:    CodeEntryNotWritten,
			Message: "the outcome of mutant " + display(id) + " was not stored: " + err.Error(),
		}
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return &Error{
			Code:    CodeEntryNotWritten,
			Message: "the outcome of mutant " + display(id) + " could not be encoded",
			Err:     err,
		}
	}
	return writeAtomic(path, append(data, '\n'))
}

func (c *Cache) path(id string) (string, error) {
	if !mutation.IsID(id) {
		return "", &Error{
			Code:    CodeInvalidContext,
			Message: "an outcome cache entry was addressed by " + display(id) + ", which is not a mutant id",
		}
	}
	return filepath.Join(c.dir, id+entrySuffix), nil
}

func truncateTail(tail string) string {
	if len(tail) <= MaxOutputTail {
		return tail
	}
	const marker = "[... truncated by the outcome cache ...]\n"
	return marker + tail[len(tail)-MaxOutputTail:]
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return &Error{
			Code:    CodeEntryNotWritten,
			Message: "a temporary file for a cached outcome could not be created in " + dir,
			Err:     err,
		}
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err = writeTemp(temp, data); err == nil {
		err = syncTemp(temp)
	}
	if closeErr := closeTemp(temp); err == nil {
		err = closeErr
	}
	if err != nil {
		return &Error{
			Code:    CodeEntryNotWritten,
			Message: "a cached outcome could not be written to " + name,
			Err:     err,
		}
	}
	if err = rename(name, path); err != nil {
		return &Error{
			Code:    CodeEntryNotWritten,
			Message: "a cached outcome could not be moved into place at " + path,
			Err:     err,
		}
	}
	return nil
}

func rename(from, to string) error {
	err := os.Rename(from, to)
	for _, delay := range renameDelays {
		if err == nil {
			return nil
		}
		time.Sleep(delay)
		err = os.Rename(from, to)
	}
	return err
}

func display(id string) string { return id[:min(len(id), mutation.DisplayIDLength)] }
