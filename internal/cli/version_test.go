// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
)

func buildInfo(version string) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{
				Path:    "github.com/P4suta/go-mutants",
				Version: version,
			},
		}, true
	}
}

func noBuildInfo() (*debug.BuildInfo, bool) { return nil, false }

const differingStamp = defaultVersion + "+build.1"

func TestResolveVersion(t *testing.T) {
	t.Parallel()

	const pseudo = "v0.1.1-0.20260823101112-21b55cdc95bc"

	tests := []struct {
		name    string
		stamped string
		info    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{
			name:    "a link-time stamp wins over build information",
			stamped: differingStamp,
			info:    buildInfo("v9.9.9"),
			want:    differingStamp,
		},
		{
			name:    "a stamp equal to the default still reports it",
			stamped: defaultVersion,
			info:    buildInfo("(devel)"),
			want:    defaultVersion,
		},
		{
			name:    "a working-tree build stays on the default",
			stamped: defaultVersion,
			info:    buildInfo("(devel)"),
			want:    defaultVersion,
		},
		{
			name:    "an empty module version stays on the default",
			stamped: defaultVersion,
			info:    buildInfo(""),
			want:    defaultVersion,
		},
		{
			name:    "absent build information stays on the default",
			stamped: defaultVersion,
			info:    noBuildInfo,
			want:    defaultVersion,
		},
		{
			name:    "a proxied tag is reported without its v",
			stamped: defaultVersion,
			info:    buildInfo("v0.2.0"),
			want:    "0.2.0",
		},
		{
			name:    "a pseudo-version is reported without its v",
			stamped: defaultVersion,
			info:    buildInfo(pseudo),
			want:    "0.1.1-0.20260823101112-21b55cdc95bc",
		},
		{
			name:    "a stamp survives absent build information",
			stamped: differingStamp,
			info:    noBuildInfo,
			want:    differingStamp,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveVersion(tc.stamped, tc.info); got != tc.want {
				t.Errorf("resolveVersion(%q) = %q, want %q",
					tc.stamped, got, tc.want)
			}
		})
	}
}

func TestVersionIsTheDefaultInAnUnstampedBuild(t *testing.T) {
	t.Parallel()
	if Version != defaultVersion {
		t.Errorf("Version = %q, want the unstamped default %q",
			Version, defaultVersion)
	}
}

func TestTheVersionFilesAgreeWithTheConstant(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	version := strings.TrimSpace(readRepoFile(t, root, "VERSION"))
	if version != defaultVersion {
		t.Errorf("VERSION is %q and defaultVersion is %q;\n"+
			"\trelease-publish.yml compares both with the release tag, so a build with these two "+
			"cannot be published", version, defaultVersion)
	}

	var manifest map[string]string
	if err := json.Unmarshal([]byte(readRepoFile(t, root, ".release-please-manifest.json")), &manifest); err != nil {
		t.Fatalf("reading .release-please-manifest.json: %v", err)
	}
	switch released, recorded := manifest["."]; {
	case len(manifest) == 0:
		if !strings.Contains(version, "-") {
			t.Errorf("VERSION is %q with an empty .release-please-manifest.json;\n"+
				"\tthe manifest says nothing has been released, so the tree may not "+
				"already be spelling a released version", version)
		}
	case recorded && released == version:
	default:
		t.Errorf(".release-please-manifest.json is %v while VERSION says %q;\n"+
			"\tthe manifest is release-please's record of the last release, and a wrong one "+
			"makes the next Release PR propose the wrong version", manifest, version)
	}
}

func readRepoFile(t *testing.T, root, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}
