// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package tempowner gives every temporary directory go-mutants creates an owner.
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
	Schema = "go-mutants-temp-owner-v1"

	LockName = "owner.lock"

	MarkerName = "owner.json"

	LegacyMaxAge = 24 * time.Hour

	markerPerm fs.FileMode = 0o600
	lockPerm   fs.FileMode = 0o600
)

type Marker struct {
	Schema  string    `json:"schema"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Kept    bool      `json:"kept"`
}

func LockPath(dir string) string { return filepath.Join(dir, LockName) }

func MarkerPath(dir string) string { return filepath.Join(dir, MarkerName) }

type Owner struct {
	dir    string
	lock   *Lock
	marker Marker
}

var ErrOwned = errors.New("already owned by another process")

func Claim(dir string, now time.Time) (*Owner, error) {
	lock, held, err := Acquire(LockPath(dir))
	if err != nil {
		return nil, fmt.Errorf("locking %s: %w", dir, err)
	}
	if !held {
		return nil, fmt.Errorf("%s is %w", dir, ErrOwned)
	}
	marker := Marker{Schema: Schema, PID: os.Getpid(), Started: now.UTC()}
	if err := writeMarker(dir, marker); err != nil {
		return nil, errors.Join(fmt.Errorf("marking %s: %w", dir, err), lock.Release())
	}
	return &Owner{dir: dir, lock: lock, marker: marker}, nil
}

func (o *Owner) Dir() string {
	if o == nil {
		return ""
	}
	return o.dir
}

func (o *Owner) Release() error {
	if o == nil {
		return nil
	}
	return o.lock.Release()
}

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

type Result struct {
	Removed      []string
	RemovedBytes int64
	Live         int
	Kept         int
}

func Sweep(parent string, prefixes []string, now time.Time) (Result, error) {
	return sweeper{now: now, remove: os.RemoveAll, acquire: Acquire}.sweep(parent, prefixes)
}

type sweeper struct {
	now     time.Time
	remove  func(string) error
	acquire func(string) (*Lock, bool, error)
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

type verdict int

const (
	verdictSpared verdict = iota
	verdictAbandoned
	verdictLive
	verdictKept
)

func (s sweeper) abandoned(dir string, entry fs.DirEntry) (verdict, error) {
	marker, err := ReadMarker(dir)
	switch {
	case err == nil && marker.Kept:
		return verdictKept, nil
	case errors.Is(err, fs.ErrNotExist):
		return s.legacy(dir, entry)
	}

	lock, held, lockErr := s.acquire(LockPath(dir))
	if lockErr != nil {
		return verdictSpared, fmt.Errorf("locking %s: %w", dir, lockErr)
	}
	if !held {
		return verdictLive, nil
	}
	if releaseErr := lock.Release(); releaseErr != nil {
		return verdictSpared, fmt.Errorf("releasing %s: %w", dir, releaseErr)
	}
	return verdictAbandoned, nil
}

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
