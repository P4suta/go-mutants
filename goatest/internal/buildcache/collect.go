// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func (layer Layer) collectionMarkerPath() string {
	return filepath.Join(layer.Dir, collectedName)
}

func (layer Layer) HoldCollection() (func() error, bool, error) {
	return layer.holdCollectionWithHooks(layerHooks{})
}

func (layer Layer) holdCollectionWithHooks(hooks layerHooks) (func() error, bool, error) {
	hooks = hooks.resolved()
	if layer.Dir == "" {
		return func() error { return nil }, false, nil
	}
	file, err := os.OpenFile(layer.collectionMarkerPath(), os.O_CREATE|os.O_RDWR, filemode.ReadableFile)
	if err != nil {
		return func() error { return nil }, false, fmt.Errorf("goatest: open build cache collection lock: %w", err)
	}
	locked, err := hooks.lockFile(file)
	if err != nil {
		_ = file.Close()
		return func() error { return nil }, false, fmt.Errorf("goatest: lock build cache collection: %w", err)
	}
	if !locked {
		_ = file.Close()
		return func() error { return nil }, false, nil
	}
	return func() error {
		unlockErr := hooks.unlockFile(file)
		closeErr := file.Close()
		if joined := errors.Join(unlockErr, closeErr); joined != nil {
			return fmt.Errorf("goatest: release build cache collection lock: %w", joined)
		}
		return nil
	}, true, nil
}

func (layer Layer) CollectLocked(policy Policy, interval time.Duration, now time.Time) (Collected, bool, error) {
	return layer.collectLockedWithHooks(policy, interval, now, layerHooks{})
}

func (layer Layer) collectLockedWithHooks(policy Policy, interval time.Duration, now time.Time, hooks layerHooks) (Collected, bool, error) {
	hooks = hooks.resolved()
	if err := policy.validate(); err != nil {
		return Collected{}, false, err
	}
	release, held, err := layer.holdCollectionWithHooks(hooks)
	if errors.Is(err, os.ErrNotExist) && directoryIsAbsent(layer.collectionMarkerPath(), layer.Dir, hooks) {
		return Collected{}, false, nil
	}
	if err != nil {
		return Collected{}, false, err
	}
	if !held {
		return Collected{}, false, nil
	}
	defer func() { _ = release() }()

	if layer.collectedRecently(interval, now, hooks) {
		return Collected{}, false, nil
	}
	collected, err := layer.collectWithHooks(policy, now, hooks)
	if err != nil {
		return Collected{}, false, err
	}

	_ = hooks.chtimes(layer.collectionMarkerPath(), now, now)
	return collected, true, nil
}

func (layer Layer) collectedRecently(interval time.Duration, now time.Time, hooks layerHooks) bool {
	if interval <= 0 || now.IsZero() {
		return false
	}
	info, err := hooks.stat(layer.collectionMarkerPath())
	if err != nil {
		return false
	}
	elapsed := now.Sub(info.ModTime())
	return elapsed >= 0 && elapsed < interval
}
