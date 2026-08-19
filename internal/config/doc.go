// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package config reads .go-mutants.toml and resolves it against built-in
// defaults and command-line flags.
//
// Configuration lives in its own file next to go.mod. It is never merged into
// go.mod, there is no environment-variable configuration, and there is no
// search up the directory tree: the caller decides which path to read, so that
// a run's inputs are always the ones the user can see.
//
// # Strictness
//
// Decoding uses pelletier/go-toml/v2 with DisallowUnknownFields. A misspelled
// key is an error naming the file, the line, the column, and the key path,
// never a silently ignored setting. That single property is why this
// dependency was chosen over BurntSushi/toml, which cannot report a position.
//
// The same standard applies to values the decoder is happy with. A glob that
// does not compile, a profile that is not a tier, an operator that is not in
// the catalogue, a threshold outside [0,100], a cache directory that climbs
// out of the workspace: each is refused where it was written, with the same
// file:line:column and the same stable GOM#### code. Positions for those come
// from a second, read-only pass over the document that records where every key
// and every array element lives; see [FileConfig.Position].
//
// # Precedence
//
// Built-in defaults, then the file, then flags:
//
//	Merge(Defaults(), file, flags)
//
// Each layer is a whole-value override, not a deep merge. Repeating --include
// replaces the TOML include array rather than appending to it, because a
// configuration that cannot be overridden downwards is worse than one that has
// to be restated. A layer only overrides what it actually set, which is what
// [Set] records: the CLI builds its [Overlay] from pflag's Changed, so a flag
// left at its own zero value never silently overwrites the file.
//
// An explicitly empty array is a value, not an absence. `formats = []` turns
// project reports off and beats the default of both formats, exactly as
// documented in docs/configuration.md, and presence is therefore decided by
// whether a key appeared, never by whether its value is non-empty.
//
// # Validation
//
// Everything that can be judged from one value alone is judged where that
// value was written, so the error can point at it: [LoadFile] validates the
// file, [Overlay.Validate] validates a flag overlay and names flags rather
// than TOML keys. Only genuinely cross-field rules — today, report.low not
// exceeding report.high — wait until after the merge, because the two halves
// can come from different layers. [Config.Validate] runs both, source
// agnostically, and is the gate every caller should pass before acting on a
// configuration. [Load] is the whole sequence in one call.
//
// # Boundaries
//
// This package resolves and validates configuration. It does not decide what
// the values mean: the operator names it accepts are checked against
// internal/mutation's registry but not expanded into rules, the globs it
// compiles are not matched against any tree, and the directories it accepts as
// relative are not created. It reads exactly one file and writes none.
package config
