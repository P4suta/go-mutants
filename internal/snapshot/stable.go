// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/P4suta/go-mutants/internal/tempowner"
)

const stableNameHexLength = 16

func StableName(absSourceRoot string) string {
	sum := sha256.Sum256([]byte(absSourceRoot))
	return DirPrefix + hex.EncodeToString(sum[:stableNameHexLength/2])
}

func destination(destParent, absSrc string) (string, bool, error) {
	parent := destParent
	if parent == "" {
		parent = os.TempDir()
	}
	parent, err := absPath(parent)
	if err != nil {
		return "", false, &Error{Code: CodeDestination, Path: destParent, Message: "cannot resolve the destination parent", Err: err}
	}

	name := StableName(absSrc)
	dir := filepath.Join(parent, name)
	claim := os.Mkdir(ExtendedPath(dir), 0o700)
	if claim == nil {
		return dir, true, nil
	}
	if !errors.Is(claim, fs.ErrExist) {
		return "", false, &Error{Code: CodeDestination, Path: dir, Message: "cannot create the snapshot directory", Err: claim}
	}

	_, _ = tempowner.Sweep(parent, []string{name}, time.Now())
	if os.Mkdir(ExtendedPath(dir), 0o700) == nil {
		return dir, true, nil
	}

	created, err := os.MkdirTemp(parent, DirPrefix)
	if err != nil {
		return "", false, &Error{Code: CodeDestination, Path: parent, Message: "cannot create the snapshot directory", Err: err}
	}
	return created, false, nil
}

func claimDestination(dir string, now time.Time) (*tempowner.Owner, error) {
	owner, err := tempowner.Claim(dir, now)
	if err == nil {
		return owner, nil
	}
	if !errors.Is(err, tempowner.ErrOwned) {
		_ = os.RemoveAll(dir)
	}
	return nil, &Error{Code: CodeDestination, Path: dir, Message: "cannot claim the snapshot directory", Err: err}
}
