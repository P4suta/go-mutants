// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package tempowner gives every temporary directory go-mutants creates an
// owner and a collector.
//
// # Why a directory needs an owner
//
// A run copies the whole module into the temporary area — hundreds of megabytes
// for a real project — and removes it when it finishes. "When it finishes" is
// the problem. A SIGKILL, an out-of-memory kill, a closed terminal or a machine
// that lost power all end the process somewhere between the copy and the
// removal, and what is left behind is a full copy of somebody's module that
// nothing will ever delete. On the machine this package was written for, nine
// such directories had accumulated at a quarter of a gigabyte each.
//
// The rule this package implements is that no byte a run writes is anonymous:
// every top-level temporary directory says who made it, and the next run
// collects the ones whose maker is gone.
//
// # The pair
//
// A claimed directory holds two files:
//
//   - [LockName] is an exclusive advisory lock, held open for as long as the
//     directory is in use. It is the liveness signal, and it is the only one.
//     A lock that can be taken means the process that held it no longer exists,
//     whatever it was called and whatever its pid has been reused for since.
//   - [MarkerName] is a small JSON document naming the schema, the process, the
//     start time, and whether the directory was kept deliberately. It is read
//     by people, and by [Sweep] for exactly one bit: `kept`.
//
// Both live inside the directory, so they disappear with it and there is no
// second place to tidy up. A pid is deliberately *not* used for liveness: it
// wraps, it is reused, and asking whether it is alive answers a question about
// some other process on a long-lived machine.
//
// # What Sweep will not do
//
// Sweep removes a directory only when it is under the named parent, its name
// begins with one of the named prefixes, it is a directory rather than a file,
// and either its lock is free and its marker does not say kept, or it carries
// no marker at all and nothing has touched it for [LegacyMaxAge]. Everything
// else in the parent — every unrelated name, every file wearing a prefix, every
// live and every kept directory — is left exactly as it was found.
//
// The legacy rule exists for one release: directories created before this
// package are unowned, and a day of inactivity is the only evidence available
// that nobody is using one. A young unowned directory is left alone precisely
// because it might be a run in progress under an older binary.
package tempowner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// Schema is the marker's schema field. It carries the version, so that a
	// later document shape can never be read as this one.
	Schema = "go-mutants-temp-owner-v1"

	// LockName is the advisory lock file inside a claimed directory.
	LockName = "owner.lock"

	// MarkerName is the JSON marker file inside a claimed directory.
	MarkerName = "owner.json"

	// LegacyMaxAge is how long an unowned directory must have been untouched
	// before [Sweep] treats it as a leftover. It is generous because the cost
	// of waiting is disk and the cost of being wrong is deleting the temporary
	// directory of a run that is still using it.
	LegacyMaxAge = 24 * time.Hour

	// markerPerm and lockPerm are owner-only: a temporary directory's
	// bookkeeping is nobody else's business, and a lock file another user could
	// truncate is not a lock.
	markerPerm fs.FileMode = 0o600
	lockPerm   fs.FileMode = 0o600
)

// A Marker is the JSON document in a claimed directory. It is written once at
// creation and rewritten only to record a deliberate keep.
type Marker struct {
	// Schema is [Schema].
	Schema string `json:"schema"`
	// PID is the process that claimed the directory. It is diagnostic only:
	// see the package documentation on why liveness is the lock's job.
	PID int `json:"pid"`
	// Started is when the directory was claimed, in UTC.
	Started time.Time `json:"started"`
	// Kept says the directory was preserved on purpose and is not an orphan.
	Kept bool `json:"kept"`
}

// LockPath is the lock file inside dir.
func LockPath(dir string) string { return filepath.Join(dir, LockName) }

// MarkerPath is the marker file inside dir.
func MarkerPath(dir string) string { return filepath.Join(dir, MarkerName) }

// An Owner is a claimed directory: the lock is held open and the marker is
// written. Releasing or keeping it closes the lock; neither removes anything,
// because the directory's lifetime belongs to whoever created it.
type Owner struct {
	dir    string
	lock   *Lock
	marker Marker
}

// Claim writes the marker pair into an existing directory and takes its lock.
//
// The lock comes first and the marker second, so that a directory caught
// half-claimed by a concurrent [Sweep] has no marker and a modification time of
// a moment ago — which is the case the legacy rule leaves alone.
func Claim(dir string, now time.Time) (*Owner, error) {
	lock, held, err := Acquire(LockPath(dir))
	if err != nil {
		return nil, fmt.Errorf("locking %s: %w", dir, err)
	}
	if !held {
		return nil, fmt.Errorf("%s is already owned by another process", dir)
	}
	marker := Marker{Schema: Schema, PID: os.Getpid(), Started: now.UTC()}
	if err := writeMarker(dir, marker); err != nil {
		return nil, errors.Join(fmt.Errorf("marking %s: %w", dir, err), lock.Release())
	}
	return &Owner{dir: dir, lock: lock, marker: marker}, nil
}

// Dir is the claimed directory.
func (o *Owner) Dir() string {
	if o == nil {
		return ""
	}
	return o.dir
}

// Release closes the lock without touching the directory. It is idempotent, and
// it must be called before the directory is removed: on Windows an open handle
// inside a directory is what makes the removal fail.
func (o *Owner) Release() error {
	if o == nil {
		return nil
	}
	return o.lock.Release()
}

// Keep records that the directory was preserved on purpose and releases the
// lock, so that a later [Sweep] reads the marker rather than finding a lock
// nobody holds and concluding the directory was abandoned.
func (o *Owner) Keep() error {
	if o == nil {
		return nil
	}
	marker := o.marker
	marker.Kept = true
	if err := writeMarker(o.dir, marker); err != nil {
		return errors.Join(fmt.Errorf("keeping %s: %w", o.dir, err), o.Release())
	}
	o.marker = marker
	return o.Release()
}

// ReadMarker decodes the marker in dir.
func ReadMarker(dir string) (Marker, error) {
	raw, err := os.ReadFile(MarkerPath(dir))
	if err != nil {
		return Marker{}, err
	}
	var marker Marker
	if err := json.Unmarshal(raw, &marker); err != nil {
		return Marker{}, err
	}
	return marker, nil
}

func writeMarker(dir string, marker Marker) error {
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return os.WriteFile(MarkerPath(dir), append(raw, '\n'), markerPerm)
}

// A Result is what one [Sweep] did. It is diagnostic: no report, no schema and
// no exit code depends on it, because a run's job is to measure mutants and
// collecting somebody else's leftovers is housekeeping it does on the way.
type Result struct {
	// Removed holds the absolute path of every directory the sweep deleted.
	Removed []string
	// RemovedBytes is what they held, as far as the walk could measure.
	RemovedBytes int64
	// Live is how many directories were still locked by a running process.
	Live int
	// Kept is how many were preserved on purpose.
	Kept int
}

// Sweep removes every abandoned go-mutants directory directly under parent.
//
// A parent that does not exist is not an error: it is a machine on which
// nothing has run yet. A directory that cannot be removed does not stop the
// sweep of the others — leaving a gigabyte on disk because of an unrelated
// permission problem would be the wrong trade — and every such failure is
// joined into the returned error after the loop.
func Sweep(parent string, prefixes []string, now time.Time) (Result, error) {
	return sweeper{now: now, remove: os.RemoveAll}.sweep(parent, prefixes)
}

// A sweeper is [Sweep] with its removal seam exposed, so that the "one
// directory refuses to go" case can be tested without a filesystem that has to
// be persuaded into failing.
type sweeper struct {
	now    time.Time
	remove func(string) error
}

func (s sweeper) sweep(parent string, prefixes []string) (Result, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Result{}, nil
		}
		return Result{}, fmt.Errorf("reading %s: %w", parent, err)
	}

	var result Result
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() || !hasAnyPrefix(entry.Name(), prefixes) {
			continue
		}
		dir := filepath.Join(parent, entry.Name())
		collect, err := s.abandoned(dir, entry)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		switch collect {
		case verdictLive:
			result.Live++
			continue
		case verdictKept:
			result.Kept++
			continue
		case verdictSpared:
			continue
		case verdictAbandoned:
		}
		size := directorySize(dir)
		if err := s.remove(dir); err != nil {
			failures = append(failures, fmt.Errorf("removing %s: %w", dir, err))
			continue
		}
		result.Removed = append(result.Removed, dir)
		result.RemovedBytes += size
	}
	return result, errors.Join(failures...)
}

// A verdict is what the sweep decided about one directory.
type verdict int

const (
	// verdictAbandoned is the only one that removes anything.
	verdictAbandoned verdict = iota
	// verdictLive is a directory whose lock somebody holds.
	verdictLive
	// verdictKept is a directory whose marker says it was preserved.
	verdictKept
	// verdictSpared is left alone without being counted as either: an unowned
	// directory too young to judge, and the directory a failure was reported
	// about. Neither is a fact about a live owner, and reporting one as though
	// it were would put a number in Result that nothing on disk backs up.
	verdictSpared
)

// abandoned decides whether one directory is the sweep's to remove.
//
// A marker that cannot be read at all is treated as a marker that does not say
// kept, deliberately: the lock has already answered the only question that
// matters, and a half-written marker must not make a dead directory immortal.
func (s sweeper) abandoned(dir string, entry fs.DirEntry) (verdict, error) {
	marker, err := ReadMarker(dir)
	switch {
	case err == nil && marker.Kept:
		return verdictKept, nil
	case errors.Is(err, fs.ErrNotExist):
		return s.legacy(dir, entry)
	}

	lock, held, lockErr := Acquire(LockPath(dir))
	if lockErr != nil {
		return verdictSpared, fmt.Errorf("locking %s: %w", dir, lockErr)
	}
	if !held {
		return verdictLive, nil
	}
	// Closed before the removal rather than after it: on Windows the open
	// handle inside the directory is itself what would refuse the delete.
	if releaseErr := lock.Release(); releaseErr != nil {
		return verdictSpared, fmt.Errorf("releasing %s: %w", dir, releaseErr)
	}
	return verdictAbandoned, nil
}

// legacy decides about a directory with no marker at all: one created before
// this package existed, or one whose marker was lost. Age is the only evidence
// there is, and a young one is left alone because it may be a run in progress.
func (s sweeper) legacy(dir string, entry fs.DirEntry) (verdict, error) {
	info, err := entry.Info()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return verdictSpared, nil
		}
		return verdictSpared, fmt.Errorf("reading %s: %w", dir, err)
	}
	if s.now.Sub(info.ModTime()) < LegacyMaxAge {
		return verdictSpared, nil
	}
	return verdictAbandoned, nil
}

func hasAnyPrefix(name string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// directorySize adds up the regular files under dir, best effort: the number is
// for a human reading a log line, and a run must not fail to reclaim a
// directory because it could not measure one file inside it.
func directorySize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}
