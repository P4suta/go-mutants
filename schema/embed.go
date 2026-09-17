// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package schema carries the JSON Schema documents go-mutants publishes, and.
package schema

import "embed"

//go:embed *.json
var FS embed.FS
