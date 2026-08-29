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
package gomutants
