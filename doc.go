// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package gomutants exposes go-mutants' reusable mutation engine.
//
// Open freezes a source tree in a disposable snapshot. A Workspace can run
// baseline commands against that snapshot and can be prepared exactly once.
// Preparing discovers, validates, and instruments the selected mutants and
// compiles the selected packages' test binaries once. The resulting Session
// then executes any number of mutant and test-target combinations without
// rebuilding or rewriting the user's source tree.
//
// Commands are argv vectors and never pass through a shell. Directories are
// module-relative, GO_MUTANTS_ activation variables are reserved, temporary
// files and compiled binaries live outside the snapshot, and every child is
// supervised as a process tree. Workspace and Session both own temporary
// resources and should be closed.
//
// Three things a consumer is likeliest to get wrong, each written out where it
// is defined: [PreparePhase] is an *open* vocabulary and an unknown phase must
// be accepted rather than refused ([KnownPreparePhases] is what to pin);
// [Catalog.Digest] identifies the set of mutants and nothing else, so two
// sessions prepared over different modules or toolchains can share it and
// [Catalog.PreparedDigest] is the value to key stored evidence on; and absence
// from [ProbeResult.Infected] licenses skipping an execution only for a mutant
// whose [Mutant.Probed] is true.
//
// [Session.Probe] proves that last set before returning it, so a caller needs
// no defence of its own against a malformed one: an index the catalogue cannot
// account for is [ErrProbeInconsistent], which is always an engine bug and
// never a measurement.
//
// [ReadBuildInfo] names the engine build itself — the version, any replacement
// in effect, and [BuildInfo.Auditable], which is true only when that version
// names one immutable set of sources — so that stored evidence can record
// which go-mutants produced it without every consumer rewriting the scan over
// [runtime/debug.BuildInfo] and its fail-closed rules.
//
// docs/library.md is the long form: the lifecycle and its locking, every option
// field with its default, the invariants of every result, the guarantees the
// engine makes about temporary directories, reserved variables and paired
// timeouts, and a `go list` passthrough recipe.
package gomutants
