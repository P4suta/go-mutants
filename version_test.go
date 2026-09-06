// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"runtime/debug"
	"sync"
	"testing"
)

// TestBuildInfoFromReadsEveryShapeBuildInfoCanTake is the whole of the
// identity contract, written against hand-built build information because
// every interesting shape — a replacement, a modified working tree, a module
// named twice — is one no single test run can produce.
//
// The claim in every row is the same one: what this module reports about
// itself is what build information actually says, and `Auditable` is true only
// when what it says names one immutable set of sources.
func TestBuildInfoFromReadsEveryShapeBuildInfoCanTake(t *testing.T) {
	const (
		revision     = "21b55cdc95bcf1a2f0dd3c9dbb0e6a04c7a0f9d1"
		dirtyVersion = "v0.0.0-20260906202401-5950bb89fe10+dirty"
	)

	consumer := debug.Module{Path: "consumer.example/app", Version: "(devel)"}
	for _, testCase := range []struct {
		name   string
		info   *debug.BuildInfo
		wantOK bool
		want   BuildInfo
	}{
		{
			name:   "no build information at all",
			info:   nil,
			wantOK: false,
			want:   BuildInfo{},
		},
		{
			name: "released dependency",
			info: &debug.BuildInfo{
				Main: consumer,
				Deps: []*debug.Module{{Path: ModulePath, Version: "v0.4.1", Sum: "h1:released"}},
			},
			wantOK: true,
			want:   BuildInfo{Version: "v0.4.1", Sum: "h1:released", Auditable: true},
		},
		{
			name: "pseudo-version dependency",
			info: &debug.BuildInfo{
				Main: consumer,
				Deps: []*debug.Module{{
					Path:    ModulePath,
					Version: "v0.1.1-0.20260823101112-21b55cdc95bc",
					Sum:     "h1:pseudo",
				}},
			},
			wantOK: true,
			want: BuildInfo{
				Version:   "v0.1.1-0.20260823101112-21b55cdc95bc",
				Sum:       "h1:pseudo",
				Auditable: true,
			},
		},
		{
			name: "dependency whose checksum build information does not carry",
			info: &debug.BuildInfo{
				Main: consumer,
				Deps: []*debug.Module{{Path: ModulePath, Version: "v0.4.1"}},
			},
			wantOK: true,
			want:   BuildInfo{Version: "v0.4.1", Auditable: true},
		},
		{
			name: "dependency replaced by a directory",
			info: &debug.BuildInfo{
				Main: consumer,
				Deps: []*debug.Module{{
					Path:    ModulePath,
					Version: "v0.4.1",
					Sum:     "h1:released",
					Replace: &debug.Module{Path: "../go-mutants", Version: develVersion},
				}},
			},
			wantOK: true,
			want: BuildInfo{
				Version:        "v0.4.1",
				Replaced:       true,
				ReplacePath:    "../go-mutants",
				ReplaceVersion: develVersion,
			},
		},
		{
			name: "dependency replaced by a versioned fork",
			info: &debug.BuildInfo{
				Main: consumer,
				Deps: []*debug.Module{{
					Path:    ModulePath,
					Version: "v0.4.1",
					Replace: &debug.Module{Path: "example.com/fork", Version: "v0.2.0", Sum: "h1:fork"},
				}},
			},
			wantOK: true,
			want: BuildInfo{
				Version:        "v0.4.1",
				Replaced:       true,
				ReplacePath:    "example.com/fork",
				ReplaceVersion: "v0.2.0",
			},
		},
		{
			name: "dependency in a consumer's own stamped build",
			info: &debug.BuildInfo{
				Main: consumer,
				Deps: []*debug.Module{{Path: ModulePath, Version: "v0.4.1", Sum: "h1:released"}},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: revision},
					{Key: "vcs.modified", Value: "true"},
				},
			},
			wantOK: true,
			want:   BuildInfo{Version: "v0.4.1", Sum: "h1:released", Auditable: true},
		},
		{
			name: "dependency carrying no version",
			info: &debug.BuildInfo{
				Main: consumer,
				Deps: []*debug.Module{{Path: ModulePath}},
			},
			wantOK: true,
			want:   BuildInfo{},
		},
		{
			name: "dependency built from a working tree",
			info: &debug.BuildInfo{
				Main: consumer,
				Deps: []*debug.Module{{Path: ModulePath, Version: "(devel)"}},
			},
			wantOK: true,
			want:   BuildInfo{Version: "(devel)"},
		},
		{
			name: "dependency named twice",
			info: &debug.BuildInfo{
				Main: consumer,
				Deps: []*debug.Module{
					{Path: ModulePath, Version: "v0.4.1", Sum: "h1:released"},
					{Path: ModulePath, Version: "v0.4.2", Sum: "h1:also-released"},
				},
			},
			wantOK: true,
			want:   BuildInfo{},
		},
		{
			name: "nil entry beside the dependency",
			info: &debug.BuildInfo{
				Main: consumer,
				Deps: []*debug.Module{nil, {Path: ModulePath, Version: "v0.4.1", Sum: "h1:released"}},
			},
			wantOK: true,
			want:   BuildInfo{Version: "v0.4.1", Sum: "h1:released", Auditable: true},
		},
		{
			name: "module absent from a populated graph",
			info: &debug.BuildInfo{
				Main: consumer,
				Deps: []*debug.Module{{Path: "example.test/other", Version: "v1.0.0", Sum: "h1:other"}},
			},
			wantOK: true,
			want:   BuildInfo{},
		},
		{
			name:   "build information with no modules at all",
			info:   &debug.BuildInfo{},
			wantOK: true,
			want:   BuildInfo{},
		},
		{
			name: "main module with a clean revision",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: ModulePath, Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs", Value: "git"},
					{Key: "vcs.revision", Value: revision},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			wantOK: true,
			want: BuildInfo{
				Version:     "(devel)",
				Main:        true,
				VCSRevision: revision,
				Auditable:   true,
			},
		},
		{
			name: "main module built from an edited working tree",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: ModulePath, Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: revision},
					{Key: "vcs.modified", Value: "true"},
				},
			},
			wantOK: true,
			want: BuildInfo{
				Version:     "(devel)",
				Main:        true,
				VCSRevision: revision,
				VCSModified: true,
			},
		},
		{
			name: "main module whose modified flag cannot be read",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: ModulePath, Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: revision},
					{Key: "vcs.modified", Value: "probably"},
				},
			},
			wantOK: true,
			want: BuildInfo{
				Version:     "(devel)",
				Main:        true,
				VCSRevision: revision,
				VCSModified: true,
			},
		},
		{
			name: "main module built without version control stamping",
			info: &debug.BuildInfo{
				Main:     debug.Module{Path: ModulePath, Version: "(devel)"},
				Settings: []debug.BuildSetting{{Key: "-buildvcs", Value: "false"}},
			},
			wantOK: true,
			want:   BuildInfo{Version: "(devel)", Main: true},
		},
		{
			name: "main module the proxy resolved",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: ModulePath, Version: "v0.4.1", Sum: "h1:installed"},
			},
			wantOK: true,
			want:   BuildInfo{Version: "v0.4.1", Sum: "h1:installed", Main: true, Auditable: true},
		},
		{
			// The shape go1.24 and later stamp for a main module built out of a
			// git checkout with an edit in it, measured rather than imagined:
			// go build writes the pseudo-version of the last commit and appends
			// "+dirty". It looks like a version and no proxy will ever serve it.
			name: "main module stamped from an edited checkout",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: ModulePath, Version: dirtyVersion},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: revision},
					{Key: "vcs.modified", Value: "true"},
				},
			},
			wantOK: true,
			want: BuildInfo{
				Version:     dirtyVersion,
				Main:        true,
				VCSRevision: revision,
				VCSModified: true,
			},
		},
		{
			// Build information contradicting itself: the "+dirty" suffix and
			// vcs.modified are stamped from the same status, so a clean setting
			// beside a dirty version is not a reason to believe the setting.
			name: "main module stamped dirty beside a clean vcs setting",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: ModulePath, Version: dirtyVersion},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: revision},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			wantOK: true,
			want: BuildInfo{
				Version:     dirtyVersion,
				Main:        true,
				VCSRevision: revision,
			},
		},
		{
			name: "main module replaced by a directory",
			info: &debug.BuildInfo{
				Main: debug.Module{
					Path:    ModulePath,
					Version: "v0.4.1",
					Replace: &debug.Module{Path: "../go-mutants", Version: develVersion},
				},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: revision},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			wantOK: true,
			want: BuildInfo{
				Version:        "v0.4.1",
				Replaced:       true,
				ReplacePath:    "../go-mutants",
				ReplaceVersion: develVersion,
				Main:           true,
				VCSRevision:    revision,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := buildInfoFrom(testCase.info)
			if ok != testCase.wantOK {
				t.Errorf("ok = %v, want %v", ok, testCase.wantOK)
			}
			if got != testCase.want {
				t.Errorf("buildInfoFrom = %+v\nwant                %+v", got, testCase.want)
			}
		})
	}
}

// TestAuditableRefusesEveryVersionNobodyCanFetchAgain is the rule itself,
// tested on values rather than through buildInfoFrom, because two of its
// clauses cannot be reached from any build information a toolchain writes.
//
// The vocabulary matters more than it looks. A version with build metadata —
// anything after a "+" — is not a version the module proxy can serve, with the
// single exception of "+incompatible", which is a real published version of a
// v2+ module without a go.mod. Everything else there is local: go1.24 and
// later stamp "+dirty" onto a main module built from an edited checkout, and
// that string is exactly the build this field exists to refuse.
func TestAuditableRefusesEveryVersionNobodyCanFetchAgain(t *testing.T) {
	const revision = "21b55cdc95bcf1a2f0dd3c9dbb0e6a04c7a0f9d1"

	for _, testCase := range []struct {
		name string
		info BuildInfo
		want bool
	}{
		{name: "tag", info: BuildInfo{Version: "v1.2.3"}, want: true},
		{name: "pseudo-version on a tag", info: BuildInfo{Version: "v1.2.3-0.20260907120000-abcdef123456"}, want: true},
		{name: "pseudo-version with no tag", info: BuildInfo{Version: "v0.0.0-20260907120000-abcdef123456"}, want: true},
		{name: "pre-release", info: BuildInfo{Version: "v2.0.0-rc.1"}, want: true},
		{name: "incompatible major", info: BuildInfo{Version: "v1.2.3+incompatible"}, want: true},
		{name: "dirty checkout", info: BuildInfo{Version: "v0.0.0-20260906202401-5950bb89fe10+dirty"}, want: false},
		{name: "incompatible and dirty", info: BuildInfo{Version: "v1.2.3+incompatible.dirty"}, want: false},
		{name: "working tree", info: BuildInfo{Version: develVersion}, want: false},
		{name: "no version", info: BuildInfo{}, want: false},
		{name: "replaced tag", info: BuildInfo{Version: "v1.2.3", Replaced: true}, want: false},
		{
			name: "main module with a clean revision",
			info: BuildInfo{Version: develVersion, Main: true, VCSRevision: revision},
			want: true,
		},
		{
			name: "main module with a modified tree",
			info: BuildInfo{Version: develVersion, Main: true, VCSRevision: revision, VCSModified: true},
			want: false,
		},
		{
			name: "dirty main module whose settings claim otherwise",
			info: BuildInfo{Version: "v0.0.0-20260906202401-5950bb89fe10+dirty", Main: true, VCSRevision: revision},
			want: false,
		},
		{
			// The Main guard, which buildInfoFrom cannot exercise because it
			// only reads the VCS settings for a main module. A revision on a
			// dependency would be the main module's revision wearing somebody
			// else's name, and it says nothing about the dependency's sources.
			name: "revision on a module that is not the main one",
			info: BuildInfo{Version: develVersion, VCSRevision: revision},
			want: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := auditable(testCase.info); got != testCase.want {
				t.Errorf("auditable(%+v) = %v, want %v", testCase.info, got, testCase.want)
			}
		})
	}
}

// TestVersionOfNamesTheBuildOrSaysItCannot pins the one string a caller that
// wants a label rather than a decision gets, including the two ways of having
// no answer: no build information, and build information that does not name
// this module.
func TestVersionOfNamesTheBuildOrSaysItCannot(t *testing.T) {
	for _, testCase := range []struct {
		name string
		info BuildInfo
		ok   bool
		want string
	}{
		{name: "released", info: BuildInfo{Version: "v0.4.1", Auditable: true}, ok: true, want: "v0.4.1"},
		{name: "working tree", info: BuildInfo{Version: "(devel)", Main: true}, ok: true, want: "(devel)"},
		{name: "replaced", info: BuildInfo{Version: "v0.4.1", Replaced: true}, ok: true, want: "v0.4.1"},
		{name: "module unnamed", info: BuildInfo{}, ok: true, want: "unknown"},
		{name: "no build information", info: BuildInfo{}, ok: false, want: "unknown"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := versionOf(testCase.info, testCase.ok); got != testCase.want {
				t.Errorf("versionOf = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestVersionNeverPanicsAndAlwaysSaysSomething is the guarantee a caller
// putting the string into a report header relies on: there is no build in
// which reading it fails or yields nothing to print.
func TestVersionNeverPanicsAndAlwaysSaysSomething(t *testing.T) {
	if got := Version(); got == "" {
		t.Error("Version() is empty; every build has a name, even \"unknown\"")
	}
}

// TestReadBuildInfoUnderGoTestNamesTheMainModule reads the real thing once.
//
// This test binary is built from this module, so go-mutants is its main module
// and not one of its dependencies — the one shape the table above cannot prove
// a real toolchain produces.
//
// Auditable is false here whatever the checkout, and that is measured, not
// hoped for: the go command does not stamp VCS settings into a *test* binary —
// `go version -m` on one shows no vcs.revision even in a clean git checkout —
// so the main module's version stays "(devel)", which names no sources, and
// there is no clean revision to fall back on. A run out of a git worktree, the
// way this project develops, could not be stamped anyway: the go command wants
// .git to be a directory, and in a worktree it is a file.
func TestReadBuildInfoUnderGoTestNamesTheMainModule(t *testing.T) {
	info, ok := ReadBuildInfo()
	if !ok {
		t.Fatal("a test binary the go command built carries no build information")
	}
	if !info.Main {
		t.Errorf("Main = false, want true; %+v", info)
	}
	if info.Version == "" {
		t.Errorf("Version is empty; a main module is always named, %+v", info)
	}
	if info.Replaced {
		t.Errorf("Replaced = true in this module's own test binary; %+v", info)
	}
	if info.Auditable {
		t.Errorf("Auditable = true in a test binary, which carries no VCS settings; %+v", info)
	}
	if got := Version(); got != info.Version {
		t.Errorf("Version() = %q, want %q", got, info.Version)
	}
}

// TestReadBuildInfoIsReadOnceAndSharedSafely pins the caching: build
// information cannot change while a process runs, so it is read once, and
// every caller — concurrent ones included — gets the same answer as the first.
func TestReadBuildInfoIsReadOnceAndSharedSafely(t *testing.T) {
	want, wantOK := ReadBuildInfo()
	wantVersion := Version()

	const callers = 8
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for range callers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			got, ok := ReadBuildInfo()
			if got != want || ok != wantOK {
				t.Errorf("concurrent ReadBuildInfo = (%+v, %v), want (%+v, %v)", got, ok, want, wantOK)
			}
			if version := Version(); version != wantVersion {
				t.Errorf("concurrent Version() = %q, want %q", version, wantVersion)
			}
		}()
	}
	start.Done()
	done.Wait()
}
