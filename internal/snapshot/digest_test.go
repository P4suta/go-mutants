// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"path/filepath"
	"testing"
)

var (
	goldenFiles = map[string]string{
		"go.mod":           "module example.com/fixture\n\ngo 1.26\n",
		"main.go":          "package main\n\nfunc main() {}\n",
		"pkg/util.go":      "package pkg\n\nfunc Add(a, b int) int { return a + b }\n",
		"pkg/util_crlf.go": "package pkg\r\n\r\nfunc Sub(a, b int) int { return a - b }\r\n",
		"日本語/テスト.txt":      "こんにちは\n",
	}
	goldenDirs = []string{"empty"}
)

var goldenManifest = []Entry{
	{RelPath: "go.mod", Size: 36, SHA256: "e92d68a8581bb78e323233c76549f04e341d1e8bbd24f101593027484c8c9990"},
	{RelPath: "main.go", Size: 29, SHA256: "55a60bb97151b2b4b680462447ce60ec34511b14fa10d77440c97b9777101566"},
	{RelPath: "pkg/util.go", Size: 53, SHA256: "1c76ebf49334317812379a4ff3020a7e676a8b770008da153017ccf0df52ff8e"},
	{RelPath: "pkg/util_crlf.go", Size: 56, SHA256: "9cd9baacf01fd1fdcf1ba91712e56b413b5e7effda2163dc04f7a2da2aee0850"},
	{RelPath: "日本語/テスト.txt", Size: 16, SHA256: "24d22f3d5e722ce41d151d7e5202028d808a57eb0fd93d7ff4b8889ef897b6de"},
}

const (
	goldenEmptyDigest  = "6130046b6349a38a6699e6aa1c5d4dec8f3aa75fb2abb06073afb905e0acbc68"
	goldenSingleDigest = "323c0321b10b695699d226046173386e01d69cf00231e70b2feb9eeb9f449a77"
	goldenTreeDigest   = "8be725250b1d609a928018fc1e855b857496a5d34703876995792466b8136ab8"
)

func TestWorkspaceDigestGoldenVectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []Entry
		want    string
	}{
		{
			name:    "empty manifest",
			entries: nil,
			want:    goldenEmptyDigest,
		},
		{
			name: "single entry",
			entries: []Entry{
				{RelPath: "a.go", Size: 10, SHA256: "7b39baa38a2ec2b8d111bbbd8e448e80226477ab40105d9d2123d4dc18067438"},
			},
			want: goldenSingleDigest,
		},
		{
			name:    "fixture tree",
			entries: goldenManifest,
			want:    goldenTreeDigest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := WorkspaceDigest(tt.entries); got != tt.want {
				t.Errorf("WorkspaceDigest() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestWorkspaceDigestIgnoresSize(t *testing.T) {
	t.Parallel()

	entries := []Entry{{RelPath: "a.go", Size: 10, SHA256: goldenEmptyDigest}}
	lying := []Entry{{RelPath: "a.go", Size: 999, SHA256: goldenEmptyDigest}}
	if WorkspaceDigest(entries) != WorkspaceDigest(lying) {
		t.Error("WorkspaceDigest() changed with Size, which is not part of the recipe")
	}
}

func TestWorkspaceDigestIsUnambiguous(t *testing.T) {
	t.Parallel()

	d := goldenEmptyDigest
	first := WorkspaceDigest([]Entry{{RelPath: "ab", SHA256: d}})
	second := WorkspaceDigest([]Entry{{RelPath: "a", SHA256: "b" + d}})
	if first == second {
		t.Error("WorkspaceDigest() is ambiguous across a field boundary")
	}

	twoEntries := WorkspaceDigest([]Entry{{RelPath: "a", SHA256: "x"}, {RelPath: "b", SHA256: "y"}})
	oneEntry := WorkspaceDigest([]Entry{{RelPath: "a", SHA256: "xby"}})
	if twoEntries == oneEntry {
		t.Error("WorkspaceDigest() is ambiguous across an entry boundary")
	}
}

func TestCreateProducesGoldenDigest(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, goldenFiles)
	for _, dir := range goldenDirs {
		mkdirAll(t, src, dir)
	}

	snap := create(t, src, Options{})
	assertManifest(t, snap.Manifest, goldenManifest)
	if snap.WorkspaceDigest != goldenTreeDigest {
		t.Errorf("WorkspaceDigest = %s, want %s", snap.WorkspaceDigest, goldenTreeDigest)
	}
	assertIsDir(t, filepath.Join(snap.Root, "empty"))
}
