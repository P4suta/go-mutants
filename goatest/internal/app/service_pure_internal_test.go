// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/cli"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const (
	replayJobs      = 4
	replayTimeoutNS = 1000
)

func soundExecution() report.Execution {
	return report.Execution{
		MutationJobs: replayJobs, CommandTimeoutNS: replayTimeoutNS, TargetTimeoutNS: replayTimeoutNS,
	}
}

func TestAReplayRefusesAReportThatDoesNotSayHowItRan(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		execution report.Execution
		replays   bool
	}{
		{name: "an execution that says all three", execution: soundExecution(), replays: true},
		{
			name:      "an execution of no jobs at all",
			execution: report.Execution{CommandTimeoutNS: replayTimeoutNS, TargetTimeoutNS: replayTimeoutNS},
		},
		{
			name:      "an execution of no command timeout",
			execution: report.Execution{MutationJobs: replayJobs, TargetTimeoutNS: replayTimeoutNS},
		},
		{
			name:      "an execution of no target timeout",
			execution: report.Execution{MutationJobs: replayJobs, CommandTimeoutNS: replayTimeoutNS},
		},
		{
			name: "an execution of jobs below zero",
			execution: report.Execution{
				MutationJobs: -1, CommandTimeoutNS: replayTimeoutNS, TargetTimeoutNS: replayTimeoutNS,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			replayed, err := replayRequest(cli.Request{}, report.Report{Execution: test.execution})
			if test.replays {
				if err != nil {
					t.Fatalf("%s was refused: %v", test.name, err)
				}
				if replayed.ReplayExecution == nil {
					t.Fatal("a replay carries no execution to pin")
				}
				return
			}
			if err == nil {
				t.Fatalf("%s was accepted: %+v", test.name, replayed)
			}
			if !strings.Contains(err.Error(), "incomplete execution metadata") {
				t.Errorf("%s was refused with %v, want it named", test.name, err)
			}
		})
	}
}

func TestAReplayTakesThePackagesTheReportNamedOrKeepsItsOwn(t *testing.T) {
	t.Parallel()
	latest := report.Report{Contract: "deep-v1", Execution: soundExecution()}
	latest.Scope.Requested.Packages = []string{"./one", "./two"}
	replayed, err := replayRequest(cli.Request{Packages: []string{"./asked"}}, latest)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(replayed.Packages, []string{"./one", "./two"}) {
		t.Fatalf("the replay asks for %q, want the packages the report recorded", replayed.Packages)
	}
	if replayed.Contract != "deep-v1" {
		t.Errorf("the replay asks for the contract %q, want the one the report recorded", replayed.Contract)
	}

	silent := report.Report{Execution: soundExecution()}
	kept, err := replayRequest(cli.Request{Packages: []string{"./asked"}}, silent)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kept.Packages, []string{"./asked"}) {
		t.Fatalf("the replay asks for %q, want the packages it was asked for", kept.Packages)
	}
}

func TestACheckpointDigestIsThirtyTwoBytesWrittenInLowercaseHex(t *testing.T) {
	t.Parallel()
	sum := sha256.Sum256([]byte("snapshot"))
	digest := hex.EncodeToString(sum[:])
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "a digest", value: digest, want: true},
		{name: "a digest of digits alone", value: strings.Repeat("0", len(digest)), want: true},
		{name: "a digest of letters alone", value: strings.Repeat("f", len(digest)), want: true},
		{name: "nothing at all"},
		{name: "one digit short", value: digest[1:]},
		{name: "one digit long", value: digest + "0"},
		{name: "a digit past f", value: strings.Repeat("g", len(digest))},
		{name: "a digit in capitals", value: strings.ToUpper(digest)},
		{name: "a digit below zero", value: strings.Repeat("/", len(digest))},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := checkpointDigest(test.value); got != test.want {
				t.Fatalf("checkpointDigest(%q) = %t, want %t", test.value, got, test.want)
			}
		})
	}
}

func TestANativeBuildCacheDirectoryReadsTheEnvironmentThenTheUserCache(t *testing.T) {
	t.Parallel()
	absolute := filepath.Join(string(filepath.Separator), "cache", "go-build")
	userCache := filepath.Join(string(filepath.Separator), "home", "cache")
	for _, test := range []struct {
		name        string
		environment []string
		userCache   func() (string, error)
		want        string
	}{
		{
			name:        "an environment that names a directory",
			environment: []string{goCacheEnvironmentVariable + "=" + absolute}, want: absolute,
		},
		{
			name:        "an environment that names it in another case",
			environment: []string{strings.ToLower(goCacheEnvironmentVariable) + "=" + absolute}, want: absolute,
		},
		{
			name:        "an environment that turns the cache off",
			environment: []string{goCacheEnvironmentVariable + "=off"},
		},
		{
			name:        "an environment that names a relative directory",
			environment: []string{goCacheEnvironmentVariable + "=./relative"},
		},
		{
			name:        "an environment that names nothing and no user cache to fall back on",
			environment: []string{"PATH=/bin"},
		},
		{
			name:        "an environment that names nothing beside a user cache",
			environment: []string{"PATH=/bin"},
			userCache:   func() (string, error) { return userCache, nil },
			want:        filepath.Join(userCache, nativeGoCacheDirectoryName),
		},
		{
			name:        "a user cache that answers nothing",
			environment: []string{"PATH=/bin"},
			userCache:   func() (string, error) { return "", nil },
		},
		{
			name:        "the last of two entries that name it",
			environment: []string{goCacheEnvironmentVariable + "=/first", goCacheEnvironmentVariable + "=" + absolute},
			want:        absolute,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := Service{Environment: test.environment, UserCacheDir: test.userCache}
			if got := service.nativeBuildCacheDirectory(); got != test.want {
				t.Fatalf("%s answered %q, want %q", test.name, got, test.want)
			}
		})
	}
}
