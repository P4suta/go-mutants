// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package snapshot

import (
	"path/filepath"
	"strings"
)

const longPathThreshold = 240

func ExtendedPath(p string) string {
	if len(p) < longPathThreshold {
		return p
	}
	if strings.HasPrefix(p, `\\?\`) || strings.HasPrefix(p, `\\.\`) {
		return p
	}
	if !filepath.IsAbs(p) {
		return p
	}
	cleaned := filepath.Clean(p)
	if strings.HasPrefix(cleaned, `\\`) {
		return `\\?\UNC\` + cleaned[2:]
	}
	return `\\?\` + cleaned
}
