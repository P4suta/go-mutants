// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache_test

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/buildcache"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const (
	persistedBodyLength = 11
	nativePrefixDigits  = 2
	nativeIdentityBytes = 32
)

func preparedNativeBase(t *testing.T) (base, native string) {
	t.Helper()
	base, native = t.TempDir(), t.TempDir()
	if err := (buildcache.Layer{Dir: base}).Prepare(); err != nil {
		t.Fatal(err)
	}
	return base, native
}

func writeNativeEntry(t *testing.T, native, actionName, outputName, body, record string) {
	t.Helper()
	if body != "" {
		path := filepath.Join(native, outputName[:nativePrefixDigits], outputName+"-d")
		if err := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(native, actionName[:nativePrefixDigits], actionName+"-a")
	if err := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(record), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
}

func nativeName(fill byte) string {
	return hex.EncodeToString([]byte(strings.Repeat(string(fill), nativeIdentityBytes)))
}

func TestPersistNativeRefusesEveryPairOfDirectoriesItCannotWorkWith(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		base   string
		source string
		want   string
	}{
		{name: "no destination at all", source: root, want: "requires source and destination"},
		{name: "no source at all", base: root, want: "requires source and destination"},
		{name: "one directory for both", base: root, source: root, want: "the same directory"},
		{
			name: "a source that is not there", base: root, source: filepath.Join(root, "absent"),
			want: "inspect native build cache persistence source",
		},
		{name: "a source that is a file", base: root, source: file, want: "is not a directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			persisted, err := buildcache.PersistNative(test.base, test.source, buildcache.NativeSeed{}, time.Now())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PersistNative reported %v, want it to say %q", err, test.want)
			}
			if persisted != (buildcache.NativePersisted{}) {
				t.Errorf("a refused persistence answered with %+v, want nothing", persisted)
			}
		})
	}
}

func TestPersistNativeSkipsEveryEntryItCannotTrust(t *testing.T) {
	t.Parallel()
	actionName := nativeName('a')
	outputName := nativeName('b')
	body := "new archive"
	sound := fmt.Sprintf("v1 %s %s %d %d\n", actionName, outputName, len(body), time.Now().UnixNano())
	for _, test := range []struct {
		name    string
		write   func(*testing.T, string)
		skipped int
		actions int
	}{
		{
			name: "an entry it can trust",
			write: func(t *testing.T, native string) {
				writeNativeEntry(t, native, actionName, outputName, body, sound)
			},
			actions: 1,
		},
		{
			name: "an action record that is not one",
			write: func(t *testing.T, native string) {
				writeNativeEntry(t, native, actionName, outputName, body, "not a record\n")
			},
			skipped: 1,
		},
		{
			name: "an object that is not there",
			write: func(t *testing.T, native string) {
				writeNativeEntry(t, native, actionName, outputName, "", sound)
			},
			skipped: 1,
		},
		{
			name: "an object of another size",
			write: func(t *testing.T, native string) {
				writeNativeEntry(t, native, actionName, outputName, body+" and more", sound)
			},
			skipped: 1,
		},
		{
			name: "a file that is not an action at all",
			write: func(t *testing.T, native string) {
				path := filepath.Join(native, actionName[:nativePrefixDigits], actionName+"-d")
				if err := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), filemode.PrivateFile); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "a directory named like an action",
			write: func(t *testing.T, native string) {
				path := filepath.Join(native, actionName[:nativePrefixDigits], actionName+"-a")
				if err := os.MkdirAll(path, filemode.ReadableDirectory); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			base, native := preparedNativeBase(t)
			test.write(t, native)
			persisted, err := buildcache.PersistNative(base, native, buildcache.NativeSeed{}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Skipped != test.skipped || persisted.Actions != test.actions {
				t.Fatalf("PersistNative = %+v, want %d skipped and %d actions",
					persisted, test.skipped, test.actions)
			}
			if test.actions != 0 && persisted.Bytes != persistedBodyLength {
				t.Fatalf("PersistNative carried %d bytes, want %d", persisted.Bytes, persistedBodyLength)
			}
		})
	}
}

func TestPersistNativeRefusesAPrefixThatIsNotADirectory(t *testing.T) {
	t.Parallel()
	base, native := preparedNativeBase(t)
	if err := os.WriteFile(filepath.Join(native, "ab"), []byte("not a directory"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	if _, err := buildcache.PersistNative(base, native, buildcache.NativeSeed{}, time.Now()); err == nil ||
		!strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("PersistNative reported %v, want it to refuse the prefix", err)
	}
}

func TestPersistNativeSkipsWhatTheBaselineAlreadyHeld(t *testing.T) {
	t.Parallel()
	base, native := preparedNativeBase(t)
	actionName := nativeName('c')
	outputName := nativeName('d')
	body := "new archive"
	writeNativeEntry(t, native, actionName, outputName, body,
		fmt.Sprintf("v1 %s %s %d %d\n", actionName, outputName, len(body), time.Now().UnixNano()))

	first, err := buildcache.PersistNative(base, native, buildcache.NativeSeed{}, time.Now())
	if err != nil || first.Actions != 1 {
		t.Fatalf("the first persistence = (%+v, %v), want one action", first, err)
	}
	again, err := buildcache.PersistNative(base, native, buildcache.NativeSeed{}, time.Now())
	if err != nil || again.Actions != 0 || again.Objects != 0 || again.Skipped != 0 {
		t.Fatalf("persisting the same entry again = (%+v, %v), want nothing new", again, err)
	}
}
