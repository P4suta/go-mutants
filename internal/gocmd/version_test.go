// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
)

// TestParseVersion is the table the `go version` contract is pinned by.
//
// The accepted rows are shapes real toolchains print, including the ones that
// grew extra fields in the middle; the rejected rows are what a PATH entry
// that is not the Go toolchain prints. Between them they say what the parser
// promises: strict at the two ends of the line, indifferent to everything
// between them.
func TestParseVersion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		output  string
		release string
		goos    string
		goarch  string
		devel   bool
	}{
		{
			name:    "a release toolchain",
			output:  "go version go1.26.5 windows/amd64\n",
			release: "go1.26.5", goos: "windows", goarch: "amd64",
		},
		{
			name:    "a two-component release",
			output:  "go version go1.22 linux/amd64\n",
			release: "go1.22", goos: "linux", goarch: "amd64",
		},
		{
			name:    "no trailing newline",
			output:  "go version go1.26.5 darwin/arm64",
			release: "go1.26.5", goos: "darwin", goarch: "arm64",
		},
		{
			name:    "CRLF line endings",
			output:  "go version go1.26.5 windows/386\r\n",
			release: "go1.26.5", goos: "windows", goarch: "386",
		},
		{
			name:    "leading and trailing whitespace",
			output:  "  go version go1.26.5 linux/arm64  \n",
			release: "go1.26.5", goos: "linux", goarch: "arm64",
		},
		{
			name:    "a devel build keeps its pseudo-version",
			output:  "go version devel go1.27-a1b2c3d4e5 Wed Jan 01 12:00:00 2026 +0000 linux/amd64\n",
			release: "devel go1.27-a1b2c3d4e5", goos: "linux", goarch: "amd64", devel: true,
		},
		{
			name:    "a GOEXPERIMENT marker in the middle is ignored",
			output:  "go version go1.26.5 X:cgocheck2 linux/amd64\n",
			release: "go1.26.5", goos: "linux", goarch: "amd64",
		},
		{
			name:    "a gccgo banner in the middle is ignored",
			output:  "go version go1.18 gccgo (GCC) 12.2.1 20221121 linux/amd64\n",
			release: "go1.18", goos: "linux", goarch: "amd64",
		},
		{
			name:    "only the first line is read",
			output:  "go version go1.26.5 linux/amd64\nwarning: something else entirely\n",
			release: "go1.26.5", goos: "linux", goarch: "amd64",
		},
		{
			name:    "an unusual target still splits at the slash",
			output:  "go version go1.26.5 js/wasm\n",
			release: "go1.26.5", goos: "js", goarch: "wasm",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := gocmd.ParseVersion(tc.output)
			if err != nil {
				t.Fatalf("ParseVersion(%q) = error %v, want a version", tc.output, err)
			}
			if got.Release != tc.release {
				t.Errorf("Release = %q, want %q", got.Release, tc.release)
			}
			if got.GOOS != tc.goos {
				t.Errorf("GOOS = %q, want %q", got.GOOS, tc.goos)
			}
			if got.GOARCH != tc.goarch {
				t.Errorf("GOARCH = %q, want %q", got.GOARCH, tc.goarch)
			}
			if got.IsDevel() != tc.devel {
				t.Errorf("IsDevel() = %v, want %v", got.IsDevel(), tc.devel)
			}
			if want := strings.TrimSpace(strings.SplitN(tc.output, "\n", 2)[0]); got.Raw != want {
				t.Errorf("Raw = %q, want the trimmed first line %q", got.Raw, want)
			}
			if got.String() != got.Raw {
				t.Errorf("String() = %q, want Raw %q", got.String(), got.Raw)
			}
		})
	}
}

// TestParseVersionRejects covers the outputs that must not become a version.
// Inventing one would put a number into a report that claims to describe the
// toolchain the run actually used.
func TestParseVersionRejects(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		output string
	}{
		{name: "empty", output: ""},
		{name: "whitespace only", output: "   \n\n"},
		{name: "not the go toolchain", output: "GNU bash, version 5.2.15(1)-release\n"},
		{name: "the wrong first word", output: "golang version go1.26.5 linux/amd64\n"},
		{name: "the wrong second word", output: "go build go1.26.5 linux/amd64\n"},
		{name: "too few fields", output: "go version go1.26.5\n"},
		{name: "no target at the end", output: "go version go1.26.5 linux amd64\n"},
		{name: "an empty target half", output: "go version go1.26.5 linux/\n"},
		{name: "an empty target half at the front", output: "go version go1.26.5 /amd64\n"},
		{name: "a devel build with nothing after it", output: "go version devel linux/amd64\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := gocmd.ParseVersion(tc.output)
			if err == nil {
				t.Fatalf("ParseVersion(%q) = %+v, want an error", tc.output, got)
			}
			if code := gocmd.CodeOf(err); code != gocmd.CodeVersionUnparsable {
				t.Errorf("CodeOf(err) = %q (err %v), want %q", code, err, gocmd.CodeVersionUnparsable)
			}
		})
	}
}

// TestParseVersionReadsTheShortestDevelLine is the boundary the devel guard
// sits on, and the table above cannot stand on it.
//
// "devel" alone names every unreleased build there has ever been, so the parser
// requires a pseudo-version after it — which it spells as "there are at least
// five fields". Five is the shortest line that has one: `go`, `version`,
// `devel`, the pseudo-version, and the target. Every devel row in the table
// carries a build date as well and so has eight or more, which is why a guard
// that rejected the minimum too would pass all of them.
func TestParseVersionReadsTheShortestDevelLine(t *testing.T) {
	t.Parallel()

	const line = "go version devel go1.27-a1b2c3d4e5 linux/amd64"
	got, err := gocmd.ParseVersion(line + "\n")
	if err != nil {
		t.Fatalf("ParseVersion(%q) = error %v, want a version: five fields is a devel line with a "+
			"pseudo-version after it and nothing else", line, err)
	}
	if want := "devel go1.27-a1b2c3d4e5"; got.Release != want {
		t.Errorf("Release = %q, want %q", got.Release, want)
	}
	if !got.IsDevel() {
		t.Errorf("IsDevel() = false for %q", got.Release)
	}
	if got.GOOS != "linux" || got.GOARCH != "amd64" {
		t.Errorf("target = %s/%s, want linux/amd64", got.GOOS, got.GOARCH)
	}
}

// TestParseVersionQuotesWhatWasPrintedAndBoundsIt pins the rendering the
// message is built from, which is what makes a rejected line diagnosable: the
// reader has to see the bytes that were rejected, and has to see all of them
// unless there are too many.
//
// The limit is restated here rather than read out of the package, because a
// test that took the constant from the code could not tell a changed limit from
// a changed rule — and the rule is what the two cases below are about. It is
// the boundary that needs saying out loud: a line of exactly the limit fits, so
// cutting it would relay 200 bytes and an ellipsis where 200 bytes were asked
// for, and no length assertion would notice.
func TestParseVersionQuotesWhatWasPrintedAndBoundsIt(t *testing.T) {
	t.Parallel()

	// The length quote() cuts above, as version.go spells it.
	const limit = 200
	// The marker quote() leaves in place of what it dropped.
	const cut = "…"

	t.Run("a short line is relayed whole and escaped", func(t *testing.T) {
		t.Parallel()

		const printed = "gnu make 4.4.1"
		_, err := gocmd.ParseVersion(printed + "\n")
		if err == nil {
			t.Fatalf("ParseVersion(%q) = nil error, want an error", printed)
		}
		if want := strconv.Quote(printed); !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, want it to quote what was printed, %q", err, want)
		}
	})

	t.Run("a line of exactly the limit is not cut", func(t *testing.T) {
		t.Parallel()

		printed := strings.Repeat("x", limit)
		_, err := gocmd.ParseVersion(printed + "\n")
		if err == nil {
			t.Fatalf("ParseVersion of a %d-byte line = nil error, want an error", limit)
		}
		if want := strconv.Quote(printed); !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, want the whole %d-byte line quoted: the limit is what may be "+
				"relayed, not one byte less", err, limit)
		}
		if strings.Contains(err.Error(), cut) {
			t.Errorf("Error() = %q, want no %q: a line of exactly the limit was not cut", err, cut)
		}
	})

	t.Run("a longer line is cut at the limit and marked", func(t *testing.T) {
		t.Parallel()

		printed := strings.Repeat("x", limit+1)
		_, err := gocmd.ParseVersion(printed + "\n")
		if err == nil {
			t.Fatalf("ParseVersion of a %d-byte line = nil error, want an error", limit+1)
		}
		if want := strconv.Quote(strings.Repeat("x", limit) + cut); !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, want the first %d bytes and %q, %q", err, limit, cut, want)
		}
	})
}

// TestParseVersionErrorIsBounded keeps a chatty impostor from turning an error
// message into a wall of its output.
func TestParseVersionErrorIsBounded(t *testing.T) {
	t.Parallel()

	_, err := gocmd.ParseVersion(strings.Repeat("not a version ", 10_000))
	if err == nil {
		t.Fatal("ParseVersion of a long line = nil error, want an error")
	}
	if len(err.Error()) > 400 {
		t.Errorf("error message is %d bytes; it relays the whole output instead of a sample", len(err.Error()))
	}
}
