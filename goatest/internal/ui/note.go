// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package ui

import "github.com/P4suta/go-mutants/goatest/internal/report"

func NoteDetail(detail string) string {
	return report.WithoutToolPrefix(detail)
}
