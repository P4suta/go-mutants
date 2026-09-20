// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
)

var goatestBuildIdentity = sync.OnceValues(resolveGoatestBuildIdentity)

func resolveGoatestBuildIdentity() (string, error) {
	return resolveGoatestBuildIdentityWith(os.Executable, digestGoatestExecutable)
}

func resolveGoatestBuildIdentityWith(locate func() (string, error), digest func(string) (string, error)) (string, error) {
	path, err := locate()
	if err != nil {
		return "", fmt.Errorf("goatest: locate running executable: %w", err)
	}
	identity, err := digest(path)
	if err != nil {
		return "", fmt.Errorf("goatest: identify running executable: %w", err)
	}
	return identity, nil
}

func digestGoatestExecutable(path string) (string, error) {
	return digestGoatestExecutableWith(path, func(path string) (io.ReadCloser, error) {
		return os.Open(path)
	})
}

func digestGoatestExecutableWith(path string, open func(string) (io.ReadCloser, error)) (string, error) {
	file, err := open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
