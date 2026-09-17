// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package first belongs to a module that only resolves inside the workspace
// one directory above it: it imports example.com/second, and nothing but the
// workspace file's `use` lines puts that module anywhere the go command can
// find it — first/go.mod requires it neither directly nor through a replace.
//
// That is deliberate, and it cuts both ways, which is why one fixture serves
// two tests.
//
// Discovery pinned to GOWORK=off cannot load this module, whatever a parent
// go.work or a $GOWORK pointing straight at it may say: "cannot load it" is
// the observable difference between obeying a workspace file outside the
// snapshot and ignoring it. Discovery of the workspace *itself* names the
// snapshot's own workspace file and does load it, and then the candidates
// below are the observation that it did -- not a count, but the only thing
// that can be here at all if the `use` lines were in effect.
package first

import "example.com/second"

// Equal is discovered only when the workspace file is obeyed: `eq-to-neq` on
// the comparison, `add-to-sub` on the sum, and `return-true` and
// `return-false` on the returned expression.
func Equal(a, b int) bool { return a == b+second.Zero() }
