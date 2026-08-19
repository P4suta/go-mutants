// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package first belongs to a module that only resolves inside the workspace
// one directory above it: it imports example.com/second, and nothing but the
// workspace file's `use` lines puts that module anywhere the go command can
// find it — first/go.mod requires it neither directly nor through a replace.
//
// That is deliberate. Discovery pinned to GOWORK=off cannot load this module,
// whatever a parent go.work or a $GOWORK pointing straight at it may say, and
// "cannot load it" is the observable difference between obeying a workspace
// file outside the snapshot and ignoring it.
package first

import "example.com/second"

// Equal is never discovered, because the module it lives in never loads.
func Equal(a, b int) bool { return a == b+second.Zero() }
