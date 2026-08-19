// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd

// This file is compiled only under `go test`. Version parsing is the part of
// this package worth testing exhaustively and the part no caller should be
// able to reach: [Locate] is the only supported way to obtain a [Version],
// because a Version that did not come from a real toolchain has no business
// appearing in a report.

// ParseVersion exposes the `go version` parser to the tests.
func ParseVersion(output string) (Version, error) { return parseVersion(output) }
