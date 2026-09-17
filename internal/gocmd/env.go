// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd

import (
	"runtime"
	"slices"
	"strings"
)

const GoflagsKey = "GOFLAGS"

const VetOff = "-vet=off"

const CountOnce = "-count=1"

func AppendGoflags(env []string, flag string) []string {
	flag = strings.TrimSpace(flag)
	if flag == "" {
		return slices.Clone(env)
	}

	value, found := "", false
	for _, entry := range env {
		if key, existing, ok := strings.Cut(entry, "="); ok && sameEnvKey(key, GoflagsKey) {
			value, found = existing, true
		}
	}
	if !found {
		return append(slices.Clone(env), GoflagsKey+"="+flag)
	}

	merged := GoflagsKey + "=" + mergeFlag(value, flag)
	out := make([]string, 0, len(env))
	written := false
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && sameEnvKey(key, GoflagsKey) {
			if written {
				continue
			}
			entry, written = merged, true
		}
		out = append(out, entry)
	}
	return out
}

func mergeFlag(value, flag string) string {
	if slices.Contains(strings.Fields(value), flag) {
		return value
	}
	if strings.TrimSpace(value) == "" {
		return flag
	}
	return value + " " + flag
}

func sameEnvKey(a, b string) bool { return sameEnvKeyOn(runtime.GOOS, a, b) }

func sameEnvKeyOn(goos, a, b string) bool {
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
