// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package rootmodule is a module that is also its own workspace root.
//
// `use .` beside `use ./inner` is the layout every repository takes when it
// grows a second module without moving the first: the engine's own tree is one,
// and so is every project that adds a tool module beside the library it
// publishes. Nothing in this corpus had it until this fixture, which is why the
// engine refused it for as long as it did — [discover.Discover] asks
// CheckWorkspace about its own SnapshotRoot, and for the `.` module that root
// *is* the workspace root, so the workspace pass refused the very layout it was
// walking.
package rootmodule

// Positive reports whether n is above zero.
func Positive(n int) bool { return n > 0 }
