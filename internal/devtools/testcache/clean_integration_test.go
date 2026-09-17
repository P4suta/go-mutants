// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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

func TestCleanRunsGoCleanCacheAgainstTheDirectory(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "go-build")
	t.Setenv(testkit.BuildCacheEnv, cache)

	gobin := testkit.GoBinary(t)
	module := testkit.Copy(t, "simple")
	env := testkit.Compose(t, t.TempDir())

	build := testkit.Exec(t, module, env, gobin, "build", "./...")
	testkit.RequireExit(t, build, 0, "`go build ./...` in a copy of the simple fixture")

	warm, err := measure(cache)
	if err != nil {
		t.Fatalf("measuring the warmed cache: %v", err)
	}
	if warm.files == 0 {
		t.Fatalf("`go build` left nothing in %s, so this test would prove nothing about emptying it: "+
			"the child did not use the cache this tool resolves", cache)
	}
	t.Logf("the build left %d files (%d bytes) in %s", warm.files, warm.bytes, cache)

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
