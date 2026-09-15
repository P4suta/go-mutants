// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"strings"
	"testing"
)

// FuzzParseDiagnostics is the promise this package makes about the compiler's
// output.
//
// What this parser reads is written by cmd/compile and cmd/go, which go-mutants
// neither controls nor pins: a toolchain release may change how a diagnostic is
// worded, indented, or located, and a run under a toolchain manager may be
// reading output from a version nobody here has seen. What the parser's answer
// decides is which mutant is blamed for a snapshot that will not build, and
// therefore which mutants a run goes on to measure — so a reader that panicked
// on an unfamiliar line would take the run down, and one that invented a
// coordinate would blame the wrong file.
//
// Four properties, and the last two are what a table of real compiler output
// cannot state:
//
//   - it never panics, whatever the build printed;
//   - every diagnostic it produces carries the line it was read from, because
//     a rejection quotes it back to the user;
//   - a coordinate is never negative, since the pattern admits digits only and
//     a negative line is one no file has;
//   - a path it reports as inside the snapshot is slash-separated and
//     relative, which is the coordinate system a catalogue path lives in. A
//     diagnostic that claimed to be inside and named an absolute path would be
//     compared against catalogue paths that can never match it, and the mutant
//     that broke the build would go unblamed.
func FuzzParseDiagnostics(f *testing.F) {
	const root = "/tmp/snap"

	f.Add("./pkg/a.go:12:4: undefined: x\n", root)
	f.Add("# example.com/m/pkg\n./pkg/a.go:12:4: undefined: x\n", root)
	f.Add("./pkg/a.go:12: too many errors\n", root)
	f.Add("./pkg/a.go:12:4: cannot use x\n\thave (int)\n\twant (string)\n", root)
	f.Add("/tmp/snap/pkg/a.go:1:1: oops\n", root)
	f.Add(`.\pkg\a.go:1:1: oops`+"\n", root)
	f.Add("/elsewhere/a.go:1:1: oops\n", root)
	f.Add("too many errors\n", root)
	f.Add("", root)
	f.Add("\n\n\n", root)
	f.Add("\t indented with no diagnostic above it\n", root)
	f.Add("a.go:99999999999999999999:1: oops\n", root)
	f.Add("a.go:-1:1: oops\n", root)
	f.Add(":1:1: oops\n", root)
	f.Add("a.go:1:1:\n", root)
	f.Add("a.go:1:1:no space after the colon\n", root)
	f.Add("../outside.go:1:1: oops\n", root)
	f.Add("\x00:1:1: oops\n", root)
	f.Add("./pkg/a.go:1:1: oops\r\n", root)
	f.Add("./pkg/a.go:1:1: oops\n", "")

	f.Fuzz(func(t *testing.T, output, snapshotRoot string) {
		diags := parseDiagnostics(output, snapshotRoot)
		// The same reading the parser does: split on newlines and drop a
		// trailing carriage return from each line, which is what makes output
		// written on Windows and read here one text rather than two.
		var lines []string
		for _, line := range strings.Split(output, "\n") {
			lines = append(lines, strings.TrimSuffix(line, "\r"))
		}

		for i, d := range diags {
			if d.Line < 0 || d.Column < 0 {
				t.Fatalf("diagnostic %d is at %d:%d, and a file has neither", i, d.Line, d.Column)
			}
			if d.Text == "" {
				t.Fatalf("diagnostic %d carries no text, and a rejection quotes it back", i)
			}
			first, _, _ := strings.Cut(d.Text, "\n")
			if !contains(lines, first) {
				t.Fatalf("diagnostic %d was read from %q, which the output does not hold", i, first)
			}
			if !d.Inside {
				continue
			}
			if strings.Contains(d.Path, `\`) {
				t.Fatalf("diagnostic %d is inside the snapshot at %q, which is not slash-separated", i, d.Path)
			}
			if strings.HasPrefix(d.Path, "/") || isAbsolutePath(d.Path) {
				t.Fatalf("diagnostic %d is inside the snapshot at the absolute path %q", i, d.Path)
			}
			if d.Path == "" || strings.HasPrefix(d.Path, "../") {
				t.Fatalf("diagnostic %d is inside the snapshot at %q, which leaves it", i, d.Path)
			}
		}

		// And the consumer of all this: the search asks which file to blame,
		// and it has to answer with something it was given or with nothing.
		blamed := blamedPaths(diags)
		for _, path := range blamed {
			if path == "" {
				t.Fatal("blamedPaths named the empty path")
			}
		}
		if len(blamed) > len(diags) {
			t.Fatalf("blamedPaths named %d files from %d diagnostics", len(blamed), len(diags))
		}
	})
}

// contains reports whether a line is one of the output's own.
func contains(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}
