// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
)

const envPrefix = "GO_MUTANTS_"

const coverDirEnv = "GOCOVERDIR"

var tempKeys = []string{"TMP", "TEMP", "TMPDIR"}

func baseEnv(scratch string) []string {
	return baseEnvFrom(nil, scratch)
}

func baseEnvFrom(source []string, scratch string) []string {
	if source == nil {
		source = os.Environ()
	}
	env := make([]string, 0, len(source)+len(tempKeys)+2)
	for _, entry := range source {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), envPrefix) {
			continue
		}
		if sameEnvKey(key, coverDirEnv) {
			continue
		}
		if scratch != "" && isTempKey(key) {
			continue
		}
		env = append(env, entry)
	}
	if scratch != "" {
		for _, key := range tempKeys {
			env = append(env, key+"="+scratch)
		}
	}
	return env
}

func toolchainEnvFrom(source []string, toolchain gocmd.Toolchain, scratch string, workspace bool) []string {
	env := setEnv(baseEnvFrom(source, scratch), "GOWORK", "off")
	if workspace {
		env = unsetEnv(baseEnvFrom(source, scratch), "GOWORK")
	}
	return prependPath(env, toolchain)
}

func mutantEnv(active, scratch string) []string {
	return mutantEnvFrom(nil, active, scratch)
}

func mutantEnvFrom(source []string, active, scratch string) []string {
	return append(baseEnvFrom(source, scratch), instrument.ActiveEnv+"="+active)
}

func probeEnv(scratch, logPath string) []string {
	return probeEnvFrom(nil, scratch, logPath)
}

func probeEnvFrom(source []string, scratch, logPath string) []string {
	return append(baseEnvFrom(source, scratch), instrument.ProbeEnv+"="+logPath)
}

func controlEnv(scratch string) []string {
	return controlEnvFrom(nil, scratch)
}

func controlEnvFrom(source []string, scratch string) []string {
	return baseEnvFrom(source, scratch)
}

func isTempKey(key string) bool {
	return slices.ContainsFunc(tempKeys, func(k string) bool { return sameEnvKey(key, k) })
}

func unsetEnv(env []string, name string) []string {
	out := make([]string, 0, len(env))
	for _, existing := range env {
		if key, _, ok := strings.Cut(existing, "="); ok && sameEnvKey(key, name) {
			continue
		}
		out = append(out, existing)
	}
	return out
}

func setEnv(env []string, name, value string) []string {
	entry := name + "=" + value
	out := make([]string, 0, len(env)+1)
	set := false
	for _, existing := range env {
		key, _, ok := strings.Cut(existing, "=")
		if ok && sameEnvKey(key, name) {
			if set {
				continue
			}
			existing, set = entry, true
		}
		out = append(out, existing)
	}
	if !set {
		out = append(out, entry)
	}
	return out
}

func prependPath(env []string, toolchain gocmd.Toolchain) []string {
	if toolchain.GoBin == "" {
		return env
	}
	dir := filepath.Dir(toolchain.GoBin)
	if dir == "" || dir == "." {
		return env
	}
	for i, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !sameEnvKey(key, "PATH") {
			continue
		}
		if value == dir || strings.HasPrefix(value, dir+string(filepath.ListSeparator)) {
			return env
		}
		env[i] = key + "=" + dir + string(filepath.ListSeparator) + value
		return env
	}
	return append(env, "PATH="+dir)
}

func sameEnvKey(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
