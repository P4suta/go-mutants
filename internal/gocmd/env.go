// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd

import (
	"runtime"
	"slices"
	"strings"
)

// GoflagsKey is the environment variable the go command reads default flags
// from. It is named rather than spelled at each point of use so that the one
// merge rule below is the only place that has to know it.
const GoflagsKey = "GOFLAGS"

// VetOff is the GOFLAGS entry that turns off the vet pass `go test` and
// `go test -c` run before compiling.
//
// It is a constant here rather than a literal at its two call sites because the
// two are making the same claim about the same tree — the instrumented snapshot
// is generated code — and a claim spelled twice is one that can drift. The go
// command ignores a GOFLAGS entry the current subcommand does not define, so an
// environment carrying this reaches `go test` and `go test -c` and is inert for
// `go build`, `go list` and `go tool`.
const VetOff = "-vet=off"

// AppendGoflags merges flag into the GOFLAGS entry of a child environment given
// in "KEY=VALUE" form, and returns the result as a new slice.
//
// It exists because a `go` flag go-mutants needs is never the only one that
// matters. GOFLAGS is how a developer, a CI image or a toolchain manager says
// `-mod=readonly` or `-tags=integration`, and internal/engine and
// internal/execute both inherit that on purpose — so the way to add one flag is
// to append to what is there rather than to set a value over it. The three
// cases are:
//
//   - no GOFLAGS entry: one is added, holding flag alone.
//   - a GOFLAGS entry: flag is appended to it after a space, so everything the
//     caller inherited still applies.
//   - flag already among its whitespace-separated fields: the value is left
//     alone. The comparison is by field and not by substring, so a `-vet=off`
//     already present is recognised while a `-vet=offline` is not mistaken for
//     it.
//
// The input is never modified: the entry is rebuilt into a fresh slice, because
// the environment this is handed is the run's own composed one and is shared by
// every other phase that did not ask for this flag.
//
// A duplicate GOFLAGS is resolved the way the operating system will resolve it
// and then written down: os/exec keeps the *last* of two entries naming one
// variable, so the last one's value is what flag is merged into, and the
// duplicates collapse to a single entry in the first one's position. That
// changes no meaning and removes the need for a reader to know the rule —
// internal/execute's own setEnv settles the same question the same way, and two
// answers to it in one process would be worse than either.
//
// A flag that is empty or all whitespace adds nothing, and the environment
// comes back copied but unchanged.
func AppendGoflags(env []string, flag string) []string {
	flag = strings.TrimSpace(flag)
	if flag == "" {
		return slices.Clone(env)
	}

	// The effective value first, before anything is written: with duplicates
	// present, only the last of them is the value the child would have seen.
	value, found := "", false
	for _, entry := range env {
		if key, existing, ok := strings.Cut(entry, "="); ok && sameEnvKey(key, GoflagsKey) {
			value, found = existing, true
		}
	}
	if !found {
		return append(slices.Clone(env), GoflagsKey+"="+flag)
	}

	merged := GoflagsKey + "=" + mergeFlag(value, flag)
	out := make([]string, 0, len(env))
	written := false
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && sameEnvKey(key, GoflagsKey) {
			if written {
				continue
			}
			entry, written = merged, true
		}
		out = append(out, entry)
	}
	return out
}

// mergeFlag is the value half of the rule: what a GOFLAGS value reads as once
// flag is part of it.
//
// An empty value is a deliberate setting rather than an absence — `GOFLAGS=`
// overrides a value inherited from a `go env -w` file, which is why
// internal/cache hashes it differently from an unset one — so it is merged into
// rather than replaced. It simply has nothing to keep, and a leading space
// would be the only difference.
func mergeFlag(value, flag string) string {
	if slices.Contains(strings.Fields(value), flag) {
		return value
	}
	if strings.TrimSpace(value) == "" {
		return flag
	}
	return value + " " + flag
}

// sameEnvKey compares two environment variable names the way the operating
// system does: case-insensitively on Windows, where a variable answers to any
// spelling of its name, and exactly everywhere else.
func sameEnvKey(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
