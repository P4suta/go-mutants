// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitenv

import "strings"

func ForRoot(base []string) []string {
	return filter(base, false)
}

func Inherited(base []string) []string {
	return filter(base, true)
}

func filter(base []string, inherited bool) []string {
	result := make([]string, 0, len(base))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
			"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG",
			"GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE", "GIT_NO_REPLACE_OBJECTS",
			"GIT_REPLACE_REF_BASE", "GIT_PREFIX", "GIT_SHALLOW_FILE", "GIT_NAMESPACE":
			continue
		}
		if inherited && (name == "GIT_CONFIG_COUNT" || name == "GIT_CONFIG_PARAMETERS" ||
			strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")) {
			continue
		}
		result = append(result, entry)
	}
	return result
}
