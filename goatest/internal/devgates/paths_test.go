// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"regexp"
	"strings"
)

var backquotedPattern = regexp.MustCompile("`([^`\n]+)`")

var pathishPattern = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+/[A-Za-z0-9_.\-]+\.[A-Za-z0-9]+$`)

func repositoryPaths(text string) []string {
	var found []string
	for _, match := range backquotedPattern.FindAllStringSubmatch(text, -1) {
		token := strings.TrimSpace(match[1])
		if strings.Contains(token, "://") || !pathishPattern.MatchString(token) {
			continue
		}
		if first, _, _ := strings.Cut(token, "/"); strings.Contains(first, ".") {
			continue
		}
		found = append(found, token)
	}
	return found
}
