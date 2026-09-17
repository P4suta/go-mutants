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

// buildInfo returns a [debug.ReadBuildInfo] stand-in reporting version as the
// main module's. A nil pointer with ok false is the "no build information at
// all" case, which is what a binary stripped of its module data reports.
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

// differingStamp is a stamp that cannot equal [defaultVersion], whatever
// release-please last wrote there.
//
// It is derived rather than written out, and that is the whole point. A
// literal — "0.1.0", "0.3.0" — expresses "the stamp differs from the default"
// only until the release that makes [defaultVersion] equal to it, at which
// point the case silently starts exercising the build-info branch instead and
// goes on passing. release-please rewrites that constant on every release, so
// a literal here is not a hypothetical collision; it is a scheduled one. The
// `+build` suffix is semver build metadata, which no released version of this
// tool will ever carry.
const differingStamp = defaultVersion + "+build.1"

func TestResolveVersion(t *testing.T) {
	t.Parallel()

	// The pseudo-version is the shape `go install …@main` produces: a
	// version nothing tagged, built from the commit it names.
	const pseudo = "v0.1.1-0.20260823101112-21b55cdc95bc"

	tests := []struct {
		name    string
		stamped string
		info    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{
			// goreleaser's -X reached its target and produced something the
			// source tree does not say. The stamp outranks anything the
			// module graph has to offer.
			name:    "a link-time stamp wins over build information",
			stamped: differingStamp,
			info:    buildInfo("v9.9.9"),
			want:    differingStamp,
		},
		{
			// The ordinary release build, and the reason this function can
			// afford to compare against [defaultVersion] at all: goreleaser
			// stamps the tag, release-please has already written that same
			// tag into [defaultVersion], so the two agree and the comparison
			// falls through. Build information then says "(devel)" — the
			// release is built from a tag checkout — and the answer is the
			// version either way. Indistinguishable inputs, correct output.
			name:    "a stamp equal to the default still reports it",
			stamped: defaultVersion,
			info:    buildInfo("(devel)"),
			want:    defaultVersion,
		},
		{
			// `go build`, `go run`, and this very test binary.
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
			// Nothing to read: no module information was embedded.
			name:    "absent build information stays on the default",
			stamped: defaultVersion,
			info:    noBuildInfo,
			want:    defaultVersion,
		},
		{
			// `go install …/cmd/go-mutants@v0.2.0` on a tree whose checked-in
			// default is still the previous release.
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
			// A stamp is a stamp even when build information is missing.
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

// TestVersionIsTheDefaultInAnUnstampedBuild pins the one thing the table above
// cannot: that [init] left [Version] alone in this build.
//
// A test binary is never linked with goreleaser's -X and its main module
// version is "(devel)", so both of [resolveVersion]'s escape hatches are shut
// and [Version] must still be [defaultVersion]. Every other test in this
// package compares real output against [Version]; if [init] ever started
// resolving to something else here, they would go red for a reason none of
// them names. This one names it.
func TestVersionIsTheDefaultInAnUnstampedBuild(t *testing.T) {
	t.Parallel()
	if Version != defaultVersion {
		t.Errorf("Version = %q, want the unstamped default %q",
			Version, defaultVersion)
	}
}

// TestTheVersionFilesAgreeWithTheConstant pins the three files release-please
// rewrites to each other, which nothing did.
//
// The same fact is spelt in three places: [defaultVersion] here, the `VERSION`
// file, and `.release-please-manifest.json`. Two of them are what
// `release-publish.yml` compares against the tag before it will publish, and
// the third is what decides which version the first Release PR proposes. They
// had disagreed -- `VERSION` said `0.0.0` while this constant said `0.1.0-dev`
// -- and the way that was found was by reading the files, which is not a way
// anything gets found twice.
//
// The manifest is the one with two legal states rather than one value, so it
// is checked as a rule rather than as a string: before the first release it
// records no release at all, and after one it records the version the other
// two carry. Both are true statements about the same moment, and what this
// refuses is the third state -- a manifest naming a version the tree does not
// have, which is how a Release PR proposes a number nobody meant.
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
		// Nothing released yet, which is what `initial-version` in
		// release-please-config.json is for. Then the tree may not be calling
		// itself a released version: a bare `0.1.0` with an empty manifest is
		// two files disagreeing about whether `0.1.0` happened.
		if !strings.Contains(version, "-") {
			t.Errorf("VERSION is %q with an empty .release-please-manifest.json;\n"+
				"\tthe manifest says nothing has been released, so the tree may not "+
				"already be spelling a released version", version)
		}
	case recorded && released == version:
		// A release happened and the tree carries it, which is the state
		// release-please leaves behind when its pull request merges.
	default:
		t.Errorf(".release-please-manifest.json is %v while VERSION says %q;\n"+
			"\tthe manifest is release-please's record of the last release, and a wrong one "+
			"makes the next Release PR propose the wrong version", manifest, version)
	}
}

// readRepoFile reads one file of the repository, as text.
func readRepoFile(t *testing.T, root, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}
