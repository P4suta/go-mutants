// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The one claim in this package a fake cannot support: that `go clean -cache`
// really empties the directory this tool points it at.
//
// Every other test here replaces the cleaner, because what those tests are
// about is when it is called and what happens around it — the budget, the
// retry, the exit status — and a real `go` command in each of them would cost
// seconds to prove nothing extra. This one is the opposite: it is entirely
// about the go command agreeing, so it warms a real cache with a real build and
// then empties it for real.
package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
)

// TestCleanRunsGoCleanCacheAgainstTheDirectory is the whole loop: fill the
// harness's cache with a real build, empty it the way its owner does, and leave
// nothing behind.
//
// The environment comes from testkit with GO_MUTANTS_TEST_GOCACHE pointed at
// this test's own directory, which makes the first half of the test an
// assertion about the two halves of this feature agreeing: if the tool resolved
// a different directory from the harness, the build would warm one directory
// and the clean would empty another, and this would fail on the first check
// rather than in somebody's disk usage a month later.
//
// The test imports testkit, which the import gate allows and production code
// does not: the gate parses non-test files, and this is a _test.go. That is the
// point of the split — main.go may not import the harness, so it resolves the
// path itself, and a test proves the two rules are the same rule.
//
// It exercises the ownership marker end to end as a side effect, and that is
// worth naming: nothing here stamps anything, so the only reason `clean` agrees
// to empty this directory is that testkit stamped it when Compose resolved the
// cache. If the harness stopped writing the marker, or wrote a different name,
// this test would fail here rather than in somebody's disk usage a month later.
func TestCleanRunsGoCleanCacheAgainstTheDirectory(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "go-build")
	// Before Compose, which reads it: the composed environment then carries this
	// directory as GOCACHE, and the child build fills it.
	t.Setenv(testkit.BuildCacheEnv, cache)

	gobin := testkit.GoBinary(t)
	module := testkit.Copy(t, "simple")
	env := testkit.Compose(t, t.TempDir())

	build := testkit.Exec(t, module, env, gobin, "build", "./...")
	testkit.RequireExit(t, build, 0, "`go build ./...` in a copy of the simple fixture")

	// A few kilobytes rather than the hundreds of megabytes a real cache holds:
	// fixtures/simple imports nothing at all, so building it compiles one package
	// and never touches the standard library. That is deliberate — what this test
	// needs is a directory the go command filled and recognises as its own, and
	// paying for a stdlib compile to get it would put a minute on every run of
	// the integration suite for nothing.
	warm, err := measure(cache)
	if err != nil {
		t.Fatalf("measuring the warmed cache: %v", err)
	}
	if warm.files == 0 {
		t.Fatalf("`go build` left nothing in %s, so this test would prove nothing about emptying it: "+
			"the child did not use the cache this tool resolves", cache)
	}
	t.Logf("the build left %d files (%d bytes) in %s", warm.files, warm.bytes, cache)

	// The real cleaner, on its own first, so that a failure says whether the go
	// command or the removal after it was the part that did not work.
	if err := goCleanCache(cache); err != nil {
		t.Fatalf("`go clean -cache` against %s: %v", cache, err)
	}
	emptied, err := measure(cache)
	if err != nil {
		t.Fatalf("measuring the emptied cache: %v", err)
	}
	if emptied.bytes >= warm.bytes {
		t.Errorf("`go clean -cache` left %d bytes in %d files, want fewer than the %d bytes it found",
			emptied.bytes, emptied.files, warm.bytes)
	}

	// And then the subcommand a developer runs, which also removes what the go
	// command leaves behind.
	var stdout, stderr bytes.Buffer
	env2 := map[string]string{
		testkit.BuildCacheEnv: cache,
		keepDirEnv:            filepath.Join(t.TempDir(), "kept"),
	}
	if code := run([]string{"clean"}, &stdout, &stderr, env2, deps{}); code != 0 {
		t.Fatalf("`clean` exited %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(cache); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s still exists after `clean`: %v", cache, err)
	}
	if stderr.Len() != 0 {
		t.Errorf("`clean` reported a problem it should not have had:\n%s", stderr.String())
	}
	t.Log(stdout.String())
}
