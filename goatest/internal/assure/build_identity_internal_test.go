// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func TestDigestGoatestExecutableReadsExactBytes(t *testing.T) {
	t.Parallel()
	contents := []byte("exact executable bytes")
	path := filepath.Join(t.TempDir(), "goatest")
	if err := os.WriteFile(path, contents, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	want := hex.EncodeToString(sum[:])
	got, err := digestGoatestExecutable(path)
	if err != nil || got != want {
		t.Fatalf("digestGoatestExecutable = (%q, %v), want %q", got, err, want)
	}
}

func TestDigestGoatestExecutableFailsClosed(t *testing.T) {
	t.Parallel()
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		if digest, err := digestGoatestExecutable(path); err == nil || digest != "" {
			t.Errorf("digestGoatestExecutable(%q) = (%q, %v)", path, digest, err)
		}
	}
}

func TestResolveGoatestBuildIdentityNamesEachFailureAndReturnsTheDigest(t *testing.T) {
	t.Parallel()
	locateErr := errors.New("locate failed")
	identity, err := resolveGoatestBuildIdentityWith(func() (string, error) {
		return "", locateErr
	}, func(string) (string, error) {
		t.Fatal("digest called after locate failed")
		return "", nil
	})
	if identity != "" || !errors.Is(err, locateErr) || err.Error() != "goatest: locate running executable: locate failed" {
		t.Fatalf("locate failure = (%q, %v)", identity, err)
	}

	digestErr := errors.New("digest failed")
	identity, err = resolveGoatestBuildIdentityWith(func() (string, error) {
		return "/bin/goatest", nil
	}, func(path string) (string, error) {
		if path != "/bin/goatest" {
			t.Fatalf("digest path = %q", path)
		}
		return "", digestErr
	})
	if identity != "" || !errors.Is(err, digestErr) || err.Error() != "goatest: identify running executable: digest failed" {
		t.Fatalf("digest failure = (%q, %v)", identity, err)
	}

	identity, err = resolveGoatestBuildIdentityWith(func() (string, error) {
		return "/bin/goatest", nil
	}, func(string) (string, error) {
		return "build-digest", nil
	})
	if identity != "build-digest" || err != nil {
		t.Fatalf("success = (%q, %v)", identity, err)
	}
}
