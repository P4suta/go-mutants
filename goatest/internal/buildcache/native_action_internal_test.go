// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const (
	nativeIdentifierHexDigits = 64
	nativeActionObjectSize    = 12
	nativeActionStamp         = 1700000000
	nativeSampleFileSize      = 3
)

func nativeIdentity(character string) string {
	return strings.Repeat(character, nativeIdentifierHexDigits)
}

func writeNativeAction(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "action")
	if err := os.WriteFile(path, []byte(contents), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestANativeIdentifierIsThirtyTwoBytesWrittenInHex(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "a lowercase identity", value: nativeIdentity("a"), want: true},
		{name: "an identity of digits", value: nativeIdentity("0"), want: true},
		{name: "an identity in capitals", value: nativeIdentity("A"), want: true},
		{name: "nothing at all"},
		{name: "one digit short", value: nativeIdentity("a")[1:]},
		{name: "one digit long", value: nativeIdentity("a") + "a"},
		{name: "a digit past f", value: nativeIdentity("a")[1:] + "g"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			decoded, valid := nativeIdentifier(test.value)
			if valid != test.want {
				t.Fatalf("nativeIdentifier(%q) = (%v, %t), want %t", test.value, decoded, valid, test.want)
			}
			if !valid && decoded != nil {
				t.Errorf("an identity it refused decoded to %v, want nothing at all", decoded)
			}
			if valid && len(decoded) != nativeCacheIdentifierBytes {
				t.Errorf("an identity decoded to %d bytes, want %d", len(decoded), nativeCacheIdentifierBytes)
			}
		})
	}
}

func TestANativeCachePathFilesTheIdentityUnderItsOwnPrefix(t *testing.T) {
	t.Parallel()
	identity := nativeIdentity("a")
	path := nativeCachePath(filepath.Join("root", "cache"), identity, "a")
	want := filepath.Join("root", "cache", identity[:nativeCachePrefixHexDigits], identity+"-a")
	if path != want {
		t.Fatalf("nativeCachePath = %q, want %q", path, want)
	}
}

func TestANativeActionIsOneLineOfFiveFieldsThatAgreeWithItsName(t *testing.T) {
	t.Parallel()
	name := nativeIdentity("a")
	output := nativeIdentity("b")
	sound := strings.Join([]string{
		nativeActionFormat, name, output, "12", "1700000000",
	}, " ") + "\n"
	for _, test := range []struct {
		name     string
		contents string
		key      string
		want     bool
	}{
		{name: "an action that agrees with its name", contents: sound, want: true},
		{name: "an action with no trailing newline", contents: strings.TrimSuffix(sound, "\n"), want: true},
		{name: "an action of no line at all", contents: ""},
		{name: "an action of two lines", contents: sound + sound},
		{name: "an action of four fields", contents: strings.Join([]string{nativeActionFormat, name, output, "12"}, " ")},
		{
			name:     "an action of six fields",
			contents: strings.TrimSuffix(sound, "\n") + " extra\n",
		},
		{
			name:     "an action of another format",
			contents: strings.Replace(sound, nativeActionFormat+" ", "v2 ", 1),
		},
		{
			name:     "an action that names another key",
			contents: strings.Replace(sound, name, nativeIdentity("c"), 1),
		},
		{
			name:     "an action whose output is no identity",
			contents: strings.Replace(sound, output, "short", 1),
		},
		{
			name:     "an action of a size that is no number",
			contents: strings.Replace(sound, " 12 ", " twelve ", 1),
		},
		{
			name:     "an action of a size below zero",
			contents: strings.Replace(sound, " 12 ", " -1 ", 1),
		},
		{
			name:     "an action stamped with no number",
			contents: strings.Replace(sound, "1700000000", "whenever", 1),
		},
		{
			name:     "an action stamped before the epoch",
			contents: strings.Replace(sound, "1700000000", "-1", 1),
		},
		{name: "a name that is no identity", contents: sound, key: "short"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			key := test.key
			if key == "" {
				key = name
			}
			action, read := readNativeAction(writeNativeAction(t, test.contents), key, layerHooks{}.resolved())
			if read != test.want {
				t.Fatalf("readNativeAction = (%+v, %t), want %t", action, read, test.want)
			}
			if !read {
				if action != (nativeAction{}) {
					t.Errorf("an action it refused answered with %+v, want none", action)
				}
				return
			}
			if action.output != output || action.size != nativeActionObjectSize {
				t.Fatalf("readNativeAction = %+v, want the output %q of %d bytes",
					action, output, nativeActionObjectSize)
			}
		})
	}
}

func TestANativeActionThatIsNoRegularFileIsNotRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "action")
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if action, read := readNativeAction(directory, nativeIdentity("a"), layerHooks{}.resolved()); read {
		t.Fatalf("a directory was read as the action %+v", action)
	}
	if action, read := readNativeAction(filepath.Join(root, "absent"), nativeIdentity("a"), layerHooks{}.resolved()); read {
		t.Fatalf("a file that is not there was read as the action %+v", action)
	}
}

func TestANativeObjectSizeCountsEveryFileBeneathADirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "object")
	if err := os.WriteFile(file, []byte("abc"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	size, err := nativeObjectSize(file, info)
	if err != nil || size != nativeSampleFileSize {
		t.Fatalf("nativeObjectSize of one file = (%d, %v), want %d", size, err, nativeSampleFileSize)
	}

	tree := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "nested"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", filepath.Join("nested", "two")} {
		if err := os.WriteFile(filepath.Join(tree, name), []byte("abc"), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
	}
	treeInfo, err := os.Lstat(tree)
	if err != nil {
		t.Fatal(err)
	}
	size, err = nativeObjectSize(tree, treeInfo)
	if err != nil || size != nativeSampleFileSize*2 {
		t.Fatalf("nativeObjectSize of a tree = (%d, %v), want %d", size, err, nativeSampleFileSize*2)
	}

	if _, err := nativeObjectSize(filepath.Join(root, "absent"), treeInfo); err == nil {
		t.Fatal("a tree that is not there was measured")
	}
}
