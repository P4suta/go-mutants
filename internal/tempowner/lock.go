// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner

import "os"

type Lock struct {
	file   *os.File
	unlock func(*os.File) error
}

func Acquire(path string) (*Lock, bool, error) {
	return acquire(path, tryAdvisoryLock, unlockAdvisory)
}

func acquire(path string, lock func(*os.File) (bool, error), unlock func(*os.File) error) (*Lock, bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, lockPerm)
	if err != nil {
		return nil, false, err
	}
	held, err := lock(file)
	if !held {
		return nil, false, closeAfter(file, err)
	}
	return &Lock{file: file, unlock: unlock}, true, nil
}

func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	err := l.unlock(file)
	return closeAfter(file, err)
}

func closeAfter(file *os.File, cause error) error {
	closeErr := file.Close()
	if cause != nil {
		return cause
	}
	return closeErr
}
