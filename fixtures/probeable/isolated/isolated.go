// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package isolated holds nothing a mutation operator can rewrite, and imports
// nothing that does.
//
// That absence is the specimen. A test binary built from this package links no
// instrumented file, so it links no generated runtime, so a probe pass of it
// writes no infection log at all — and the pass has to read that missing file
// as the *empty* set of infections rather than as a measurement that failed.
// The reading is sound precisely because the runtime writes its header in
// `init`, before any test code runs: a log that is not there is a process that
// never linked a probe, and a process that never linked a probe cannot have run
// a probed site.
//
// The name is a constant rather than a function because a `return` here would
// be a return-value candidate, which would instrument the file, link the
// runtime, and leave the fixture proving the opposite of what it exists for.
package isolated

// Name is what this package answers to.
const Name = "isolated"
