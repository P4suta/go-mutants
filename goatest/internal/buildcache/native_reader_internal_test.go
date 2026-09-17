// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const nativeObjectContents = "package fixture\n"

func nativeDigestOf(data string) []byte {
	sum := sha256.Sum256([]byte(data))
	return sum[:]
}

func TestAVerifiedNativeReaderAcceptsOnlyTheBytesItsDigestNames(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		contents string
		expected []byte
		invalid  bool
	}{
		{
			name: "the object its digest names", contents: nativeObjectContents,
			expected: nativeDigestOf(nativeObjectContents),
		},
		{
			name: "an empty object its digest names", expected: nativeDigestOf(""),
		},
		{
			name: "an object of other bytes", contents: nativeObjectContents,
			expected: nativeDigestOf("something else"), invalid: true,
		},
		{
			name: "an object of the wrong length", contents: nativeObjectContents,
			expected: nativeDigestOf(nativeObjectContents + "more"), invalid: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := &verifiedNativeReader{
				source: strings.NewReader(test.contents), digest: sha256.New(), expected: test.expected,
			}
			read, err := io.ReadAll(reader)
			if test.invalid {
				if err == nil {
					t.Fatalf("a reader whose digest does not match read %q with no complaint", read)
				}
				if !reader.invalid {
					t.Error("a reader whose digest does not match did not say so")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reader.invalid {
				t.Error("a reader whose digest matches said it was invalid")
			}
			if string(read) != test.contents {
				t.Fatalf("the reader read %q, want %q", read, test.contents)
			}
		})
	}
}

func TestAVerifiedNativeReaderReportsAFailureThatIsNotTheEnd(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("the object could not be read")
	reader := &verifiedNativeReader{
		source: failingReader{err: sentinel}, digest: sha256.New(),
		expected: nativeDigestOf(nativeObjectContents),
	}
	if _, err := io.ReadAll(reader); !errors.Is(err, sentinel) {
		t.Fatalf("a reader that could not read reported %v, want the failure", err)
	}
	if !reader.invalid {
		t.Error("a reader that could not read did not say the object was invalid")
	}
}

func TestOpeningANativeObjectRefusesEverythingThatIsNotTheFileItExpects(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "object")
	if err := os.WriteFile(path, []byte(nativeObjectContents), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "directory")
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		path string
		size int64
		open bool
	}{
		{name: "the file it expects", path: path, size: int64(len(nativeObjectContents)), open: true},
		{name: "a file of another size", path: path, size: 1},
		{name: "a file that is not there", path: filepath.Join(root, "absent")},
		{name: "a directory", path: directory},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, opened := openNativeObject(test.path, test.size)
			if opened != test.open {
				t.Fatalf("openNativeObject(%q, %d) = (%v, %t), want %t", test.path, test.size, file, opened, test.open)
			}
			if !opened {
				if file != nil {
					t.Errorf("an object it refused answered with %v, want none", file)
				}
				return
			}
			t.Cleanup(func() { _ = file.Close() })
		})
	}
}

func TestOpeningANativeObjectRefusesALinkToTheFileItExpects(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "object")
	if err := os.WriteFile(path, []byte(nativeObjectContents), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	file, opened := openNativeObject(link, int64(len(nativeObjectContents)))
	if opened {
		_ = file.Close()
		t.Fatal("a symbolic link was opened as a native build cache object")
	}
}
