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

// envPrefix is the variable prefix no child of this package ever inherits.
//
// Activation is go-mutants' to set and nobody else's. A GO_MUTANTS_ACTIVE left
// in a developer's shell would otherwise turn a mutant on inside a build or
// inside another mutant's run, and the failure that produced would look exactly
// like a detection.
const envPrefix = "GO_MUTANTS_"

// tempKeys are the environment variables redirected at a worker's scratch
// directory. All three are set on every platform: TMPDIR is the POSIX
// spelling, TMP and TEMP the Windows ones, and a test helper may read any of
// them.
var tempKeys = []string{"TMP", "TEMP", "TMPDIR"}

// baseEnv is this process's environment with every GO_MUTANTS_ variable
// removed and, when scratch is not empty, the three temporary-directory
// variables pointed at it.
//
// Inheriting the rest is deliberate. GOFLAGS, GOMODCACHE, GOPROXY, a private
// module's credentials, and the PATH that makes a project's tests work are all
// part of what "the tests pass here" means, and a run that stripped them would
// be measuring a different project.
func baseEnv(scratch string) []string {
	return baseEnvFrom(nil, scratch)
}

// baseEnvFrom is baseEnv with an explicit source environment. A nil source
// preserves os/exec's inheritance convention; a non-nil source is copied and
// sanitised without consulting process-global state.
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

// toolchainEnv is the environment a `go` command issued by this package runs
// with.
//
// It adds two things to [baseEnv], and both are borrowed from internal/discover
// rather than invented here, because the package set `go list` enumerates has
// to be the package set discovery type-checked:
//
//   - GOWORK=off. The go command searches every parent directory for a
//     `go.work` and obeys $GOWORK, so a snapshot placed one level below
//     somebody's workspace would otherwise resolve against a file the snapshot
//     does not contain — and every digest and identity this run mints assumes
//     the snapshot is the whole truth.
//   - The located toolchain's directory in front of PATH. It does not decide
//     which `go` runs — os/exec resolved that from [gocmd.Toolchain.GoBin]
//     already — it decides what that `go` sees, because a toolchain that finds
//     a different one ahead of it on PATH can hand work to it.
func toolchainEnvFrom(source []string, toolchain gocmd.Toolchain, scratch string) []string {
	env := setEnv(baseEnvFrom(source, scratch), "GOWORK", "off")
	return prependPath(env, toolchain)
}

// mutantEnv is the environment one test binary runs with, with the given
// mutant activated.
//
// It is [baseEnv] and nothing else. A test binary is the user's program, and
// the fewer variables go-mutants invents around it the closer the measurement
// is to what `go test` would have produced — so the toolchain settings that
// [toolchainEnvFrom] pins for the build are deliberately not applied here.
func mutantEnv(active, scratch string) []string {
	return mutantEnvFrom(nil, active, scratch)
}

func mutantEnvFrom(source []string, active, scratch string) []string {
	return append(baseEnvFrom(source, scratch), instrument.ActiveEnv+"="+active)
}

// probeEnv is the environment one test binary of the *probe* tree runs with,
// recording into the log at logPath.
//
// It is [mutantEnv] with the other variable, and the difference is the whole
// point of the tree: nothing here activates anything. [instrument.ActiveEnv] is
// not merely left unset, it is stripped by [baseEnvFrom] along with every other
// GO_MUTANTS_ variable, so a value exported in a developer's shell cannot make
// a probe pass measure a program other than the one the user wrote — which is
// the claim the whole layer rests on.
func probeEnv(scratch, logPath string) []string {
	return probeEnvFrom(nil, scratch, logPath)
}

func probeEnvFrom(source []string, scratch, logPath string) []string {
	return append(baseEnvFrom(source, scratch), instrument.ProbeEnv+"="+logPath)
}

// isTempKey reports whether key is one of the temporary-directory variables.
func isTempKey(key string) bool {
	return slices.ContainsFunc(tempKeys, func(k string) bool { return sameEnvKey(key, k) })
}

// setEnv sets one variable in a "KEY=VALUE" environment.
//
// Every entry naming the variable is replaced rather than a second one
// appended: os/exec resolves a duplicate by keeping the last, so appending
// would work, and an environment whose meaning depends on knowing that rule is
// one a maintainer reads wrong.
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

// prependPath puts the toolchain's own directory at the front of PATH, unless
// it is already there.
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

// sameEnvKey compares two environment variable names the way the operating
// system does: case-insensitively on Windows, where a variable answers to any
// spelling of its name — PATH is written "Path" as often as "PATH" — and
// exactly everywhere else.
func sameEnvKey(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
