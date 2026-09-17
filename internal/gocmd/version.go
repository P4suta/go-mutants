// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd

import (
	"strconv"
	"strings"
)

type Version struct {
	Raw          string
	Release      string
	GOOS, GOARCH string
}

func (v Version) String() string { return v.Raw }

func (v Version) IsDevel() bool { return strings.HasPrefix(v.Release, develPrefix) }

const develPrefix = "devel"

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
	if !ok || goos == "" || goarch == "" || strings.Contains(goarch, "/") {
		return Version{}, &Error{
			Code:    CodeVersionUnparsable,
			Message: "`go version` printed " + quote(line) + ", which does not end in a \"os/arch\" target",
		}
	}

	release := fields[2]
	if release == develPrefix {
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

func quote(s string) string {
	const limit = 200
	if len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "") + "…"
	}
	return strconv.Quote(s)
}

func quotePath(p string) string { return "\"" + p + "\"" }
