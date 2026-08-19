// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd

import (
	"strconv"
	"strings"
)

// Version is what `go version` said, parsed into the parts go-mutants reports.
//
// Only three things are promised, because only three have been stable across
// every Go release: the line begins "go version", the token after it names the
// release, and the last token is GOOS/GOARCH. Everything a particular build
// inserts between them — a devel pseudo-version and its commit date, an
// X:experiment marker, gccgo's compiler banner — is preserved in [Version.Raw]
// and otherwise ignored.
//
// There is no comparison, ordering, or minimum-version check here on purpose.
// Deciding that a toolchain is too old is a policy question, it belongs to
// `doctor` and to the configuration layer, and a package this low in the
// dependency graph should not be the one that gets edited when the policy
// changes.
type Version struct {
	// Raw is the whole first line of output, trimmed. It is what reports
	// quote, because it is the only field that cannot be wrong.
	Raw string
	// Release is the release token: "go1.26.5", or "devel go1.27-a1b2c3d4"
	// for an unreleased toolchain.
	Release string
	// GOOS and GOARCH are the target the toolchain reports for itself, split
	// from the trailing "os/arch" token.
	GOOS, GOARCH string
}

// String returns the raw version line.
func (v Version) String() string { return v.Raw }

// IsDevel reports whether the toolchain is an unreleased build. It is a fact
// about the string rather than a judgement about it: reports mark such a run
// so that a surprising result can be attributed later.
func (v Version) IsDevel() bool { return strings.HasPrefix(v.Release, develPrefix) }

// develPrefix is the token cmd/go prints in place of a release number for a
// toolchain built from the development branch.
const develPrefix = "devel"

// parseVersion reads the output of `go version`.
//
// The strictness is at the ends of the line and nowhere else, for the reasons
// [Version] gives. A line that does not start "go version" or does not end in
// a well-formed "os/arch" is rejected: at that point the executable is
// answering with something no Go toolchain has printed, and guessing would
// mean putting an invented version into a report that claims to describe the
// run.
func parseVersion(output string) (Version, error) {
	line, _, _ := strings.Cut(output, "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return Version{}, &Error{
			Code:    CodeVersionUnparsable,
			Message: "`go version` printed nothing",
		}
	}

	fields := strings.Fields(line)
	if len(fields) < 4 || fields[0] != "go" || fields[1] != "version" {
		return Version{}, &Error{
			Code:    CodeVersionUnparsable,
			Message: "`go version` printed " + quote(line) + ", which does not begin with a release after \"go version\"",
		}
	}

	goos, goarch, ok := strings.Cut(fields[len(fields)-1], "/")
	if !ok || goos == "" || goarch == "" {
		return Version{}, &Error{
			Code:    CodeVersionUnparsable,
			Message: "`go version` printed " + quote(line) + ", which does not end in a \"os/arch\" target",
		}
	}

	release := fields[2]
	if release == develPrefix {
		// "go version devel go1.27-a1b2c3d4 <date...> linux/amd64". The
		// pseudo-version is the identity; "devel" alone names every
		// unreleased build there has ever been.
		if len(fields) < 5 {
			return Version{}, &Error{
				Code:    CodeVersionUnparsable,
				Message: "`go version` printed " + quote(line) + ", which names a devel build without a version after it",
			}
		}
		release += " " + fields[3]
	}

	return Version{Raw: line, Release: release, GOOS: goos, GOARCH: goarch}, nil
}

// quote renders a line for an error message.
//
// It is length-limited because the thing that printed it is, by hypothesis,
// not the Go toolchain, and an error message is not the place to relay however
// many kilobytes some other program decided to emit.
func quote(s string) string {
	const limit = 200
	if len(s) > limit {
		// ToValidUTF8 drops the half rune the cut may have left behind.
		s = strings.ToValidUTF8(s[:limit], "") + "…"
	}
	return strconv.Quote(s)
}

// quotePath renders a filesystem path for an error message.
//
// It deliberately does not escape, where [quote] does. Escaping is right for
// something another program printed, because the point there is to show
// exactly what came back; it is wrong for a path, because a Windows path
// rendered with doubled backslashes is harder to read and harder to paste back
// into the configuration file it came from.
func quotePath(p string) string { return "\"" + p + "\"" }
