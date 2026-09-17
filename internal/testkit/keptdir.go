// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const KeptNameLimit = 48

const keptNameBudget = 40

const keptNameAttempts = 5

const (
	keptRemovalAttempts = 3
	keptRemovalDelay    = 100 * time.Millisecond
)

func Scratch(t testing.TB) string {
	t.Helper()
	ForceFail(t)
	if KeepPolicy() == KeepNever {
		return t.TempDir()
	}
	l := ledgerFor(t)
	l.mu.Lock()
	first := !l.scratchTaken
	l.scratchTaken = true
	l.mu.Unlock()
	if first {
		return KeptDir(t)
	}
	return newKeptDir(t, l)
}

func KeptDir(t testing.TB) string {
	t.Helper()
	if KeepPolicy() == KeepNever {
		return ""
	}
	l := ledgerFor(t)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.kept == "" {
		l.kept = newKeptDirLocked(t, l)
	}
	return l.kept
}

func PackageScratch(name string) (dir string, release func(failed bool)) {
	policy := KeepPolicy()
	created, err := "", error(nil)
	if policy != KeepNever {
		created, err = keptPackageScratch(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "testkit: %v; keeping nothing for %s\n", err, name)
			policy = KeepNever
		}
	}
	if policy == KeepNever {
		created = temporaryPackageScratch(name)
	}

	return created, func(failed bool) {
		warnIfNothingWasForced()
		if policy == KeepNever || (policy == KeepOnFailure && !failed) {
			removeQuietly(created)
			return
		}
		writeReport(created, packageReport(created, name, policy, failed))
		fmt.Fprintf(os.Stderr, "testkit: kept: %s\n", created)
	}
}

func keptPackageScratch(name string) (string, error) {
	root, err := KeepRoot()
	if err != nil {
		return "", fmt.Errorf("resolving the kept scratch root: %w", err)
	}
	if _, stampErr := stampHarnessDirectory(root, KeptMarker, keptMarkerBody); stampErr != nil {
		return "", fmt.Errorf("creating the kept scratch root %s: %w", root, stampErr)
	}
	created, err := makeKeptDir(filepath.Join(root, packageShortName()), name)
	if err != nil {
		return "", fmt.Errorf("creating the package scratch directory for %s under %s: %w", name, root, err)
	}
	return created, nil
}

func temporaryPackageScratch(name string) string {
	created, err := os.MkdirTemp("", "go-mutants-"+name+"-")
	if err != nil {
		panic(fmt.Sprintf("testkit: creating the package scratch directory for %s: %v", name, err))
	}
	return created
}

func newKeptDir(t testing.TB, l *ledger) string {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	return newKeptDirLocked(t, l)
}

func newKeptDirLocked(t testing.TB, l *ledger) string {
	t.Helper()
	root, err := KeepRoot()
	if err != nil {
		t.Fatalf("resolving the kept scratch root: %v", err)
		return ""
	}
	stampKeptRoot(t, root)
	dir, err := makeKeptDir(filepath.Join(root, packageShortName()), t.Name())
	if err != nil {
		t.Fatalf("creating a kept scratch directory under %s: %v", root, err)
		return ""
	}
	l.dirs = append(l.dirs, dir)
	policy := l.policy
	t.Cleanup(func() {
		if policy != KeepAlways && !t.Failed() {
			removeKept(t, dir)
			return
		}
		writeReport(dir, testReport(t, dir, l, policy))
		t.Logf("kept: %s", dir)
	})
	return dir
}

func makeKeptDir(parent, name string) (string, error) {
	dir, err := makeKeptDirOnce(parent, name)
	if !errors.Is(err, fs.ErrNotExist) {
		return dir, err
	}
	return makeKeptDirOnce(parent, name)
}

func makeKeptDirOnce(parent, name string) (string, error) {
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	var err error
	for range keptNameAttempts {
		dir := filepath.Join(parent, keptLeaf(name))
		if err = os.Mkdir(dir, 0o755); err == nil {
			return dir, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", err
}

func keptLeaf(name string) string {
	return sanitizedName(name, keptNameBudget) + "-" + randomSuffix()
}

func sanitizedName(name string, budget int) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= budget {
			break
		}
	}
	cut := b.String()
	if len(cut) > budget {
		cut = cut[:budget]
	}
	if cut == "" {
		return "unnamed"
	}
	return cut
}

func randomSuffix() string {
	var raw [3]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(raw[:])
}

var packageShortName = sync.OnceValue(func() string {
	base := filepath.Base(TestBinary())
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".test")
	return sanitizedName(base, keptNameBudget)
})

func removeKept(t testing.TB, dir string) {
	t.Helper()
	var err error
	for attempt := range keptRemovalAttempts {
		if err = os.RemoveAll(dir); err == nil {
			return
		}
		if attempt < keptRemovalAttempts-1 {
			time.Sleep(keptRemovalDelay)
		}
	}
	t.Logf("testkit: the scratch directory %s could not be removed and is left as it is "+
		"(a file in it is probably still open): %v", dir, err)
}

func removeQuietly(dir string) {
	for attempt := range keptRemovalAttempts {
		if err := os.RemoveAll(dir); err == nil {
			return
		}
		if attempt < keptRemovalAttempts-1 {
			time.Sleep(keptRemovalDelay)
		}
	}
	fmt.Fprintf(os.Stderr, "testkit: %s could not be removed and is left as it is\n", dir)
}
