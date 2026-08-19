// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package gocmd finds the Go toolchain and describes how to invoke it.
//
// It is deliberately the thinnest package in go-mutants. It answers two
// questions — where is `go`, and what is it — and then hands back an argument
// vector for somebody else to run. It builds nothing, decides nothing, and
// knows nothing about mutants, snapshots, or coverage: `go test -c` lives in
// the phase that needs a test binary, and `go tool covdata` lives in the phase
// that reads coverage. Putting those here would make the one package that
// every other phase depends on the one that changes most.
//
// # Why locate at all
//
// go-mutants could invoke "go" and let the operating system resolve it on
// every call. It does not, for three reasons that all point the same way:
//
//   - A run has to be able to say which toolchain produced it. The workspace
//     block of the report names a Go version, and a version read once at the
//     start is a fact about the run; a version implied by whatever PATH said
//     each time is not.
//   - Resolving once means resolving once. Thousands of `go` invocations in a
//     single run should not each pay for a PATH walk, and should not be able
//     to disagree with each other because something changed PATH midway.
//   - `go` missing from PATH is by far the most common way this tool fails on
//     a fresh machine, and it deserves an error that says what to do rather
//     than an exec failure repeated once per package.
//
// That last point is why [CodeToolchainNotFound] exists and why its message
// names mise: the sister projects and this one are all mise-managed, and the
// toolchain being installed but not on the ambient PATH is the normal shape of
// the problem rather than an exotic one.
//
// # The version probe
//
// The version comes from running `go version` and parsing the line it prints.
// That is a subprocess where reading a file might do, and it is the right
// trade: `go env GOVERSION` is another subprocess, the GOROOT layout is not a
// contract, and a binary that answers `go version` with something parseable is
// a binary that really is a Go toolchain. The probe therefore doubles as
// validation — [Locate] returning successfully means the path names something
// that behaves like `go`, not merely something that exists.
//
// The probe runs through internal/runner rather than through os/exec, so a
// toolchain that hangs is killed like anything else this tool starts.
//
// The parser is deliberately loose about the middle of the line and strict
// about its ends. `go version` has grown fields over the years — a devel
// pseudo-version and its date, a GOEXPERIMENT marker, gccgo's own banner —
// while the first two words and the trailing GOOS/GOARCH have never moved. See
// [Version] for exactly what is promised.
package gocmd
