// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
)

const ModulePath = "github.com/P4suta/go-mutants"

const develVersion = "(devel)"

const unknownVersion = "unknown"

type BuildInfo struct {
	Version string

	Sum string

	Replaced bool

	ReplacePath    string
	ReplaceVersion string

	Main bool

	VCSRevision string
	VCSModified bool

	Auditable bool
}

func ReadBuildInfo() (info BuildInfo, ok bool) { return readBuildInfoOnce() }

var readBuildInfoOnce = sync.OnceValues(func() (BuildInfo, bool) {
	raw, _ := debug.ReadBuildInfo()
	return buildInfoFrom(raw)
})

func Version() string { return versionOf(ReadBuildInfo()) }

func versionOf(info BuildInfo, ok bool) string {
	if !ok || info.Version == "" {
		return unknownVersion
	}
	return info.Version
}

func buildInfoFrom(raw *debug.BuildInfo) (BuildInfo, bool) {
	if raw == nil {
		return BuildInfo{}, false
	}
	if raw.Main.Path == ModulePath {
		return mainModuleInfo(raw), true
	}
	var found *debug.Module
	for _, dependency := range raw.Deps {
		if dependency == nil || dependency.Path != ModulePath {
			continue
		}
		if found != nil {
			return BuildInfo{}, true
		}
		found = dependency
	}
	if found == nil {
		return BuildInfo{}, true
	}
	info := moduleInfo(*found)
	info.Auditable = auditable(info)
	return info, true
}

func mainModuleInfo(raw *debug.BuildInfo) BuildInfo {
	info := moduleInfo(raw.Main)
	info.Main = true
	for _, setting := range raw.Settings {
		switch setting.Key {
		case "vcs.revision":
			info.VCSRevision = setting.Value
		case "vcs.modified":
			modified, err := strconv.ParseBool(setting.Value)
			info.VCSModified = err != nil || modified
		}
	}
	info.Auditable = auditable(info)
	return info
}

func moduleInfo(module debug.Module) BuildInfo {
	info := BuildInfo{Version: module.Version, Sum: module.Sum}
	if module.Replace == nil {
		return info
	}
	info.Sum = ""
	info.Replaced = true
	info.ReplacePath = module.Replace.Path
	info.ReplaceVersion = module.Replace.Version
	return info
}

func auditable(info BuildInfo) bool {
	switch {
	case info.Replaced, hasLocalMetadata(info.Version):
		return false
	case namesSources(info.Version):
		return true
	}
	return info.Main && info.VCSRevision != "" && !info.VCSModified
}

func namesSources(version string) bool {
	return version != "" && version != develVersion
}

func hasLocalMetadata(version string) bool {
	plus := strings.IndexByte(version, '+')
	return plus >= 0 && version[plus:] != "+incompatible"
}
