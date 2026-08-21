// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// baseContext is one complete, believable key context. Every field is a real
// value of the right shape, because [cache.Context.Key] refuses the rest.
func baseContext() cache.Context {
	return cache.Context{
		ToolVersion:      "0.1.0-dev",
		ToolDigest:       strings.Repeat("11", 32),
		ToolchainVersion: "go1.26.5",
		WorkspaceDigest:  strings.Repeat("ab", 32),
		CatalogDigest:    strings.Repeat("cd", 32),
		TestCommand:      []string{"go", "test", "./..."},
		// Zero would mean "derived", which is the commoner case and the one the
		// key deliberately does not carry a number for; an explicit bound is set
		// here so that both halves of the field are exercised.
		ConfiguredTimeout: 10 * time.Second,
		Env: map[string]cache.EnvValue{
			"CGO_ENABLED":  {Value: "1", Set: true},
			"GOARCH":       {Value: "amd64", Set: true},
			"GODEBUG":      {Value: "", Set: false},
			"GOEXPERIMENT": {Value: "", Set: false},
			"GOFLAGS":      {Value: "", Set: false},
			"GOOS":         {Value: "linux", Set: true},
		},
	}
}

// keyOf computes a key and fails the test if the context will not produce one.
func keyOf(t *testing.T, c cache.Context) string {
	t.Helper()
	key, err := c.Key()
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	return key
}

// TestKeyIsDeterministic is the property the whole store rests on: two runs
// that agree about everything in the context have to agree about the key, on
// any machine and in any order.
func TestKeyIsDeterministic(t *testing.T) {
	t.Parallel()

	first := keyOf(t, baseContext())
	second := keyOf(t, baseContext())
	if first != second {
		t.Errorf("two identical contexts hashed to %s and %s", first, second)
	}
	if !mutation.IsDigest(first) {
		t.Errorf("the key %q is not 64 lowercase hex characters", first)
	}
}

// TestEveryFieldOfTheContextChangesTheKey is the other half of the same
// property, and the more important half: a field that does not reach the hash
// is a field a stale outcome can be adopted across.
//
// The table is written as "one thing differs from the base context", so a field
// added to [cache.Context] without a row here shows up as a missing row rather
// than as a silent hole — there is no way to make the test pass by leaving a
// field out of the hash.
func TestEveryFieldOfTheContextChangesTheKey(t *testing.T) {
	t.Parallel()

	base := keyOf(t, baseContext())
	cases := []struct {
		field  string
		change func(*cache.Context)
	}{
		{"tool version", func(c *cache.Context) { c.ToolVersion = "0.2.0" }},
		{"executable digest", func(c *cache.Context) { c.ToolDigest = strings.Repeat("22", 32) }},
		{
			// The patch release, which is the upgrade nothing else in the key
			// notices: the test command is the literal word `go`, and go.mod
			// pins a language version rather than a toolchain.
			"toolchain version",
			func(c *cache.Context) { c.ToolchainVersion = "go1.26.6" },
		},
		{"workspace digest", func(c *cache.Context) { c.WorkspaceDigest = strings.Repeat("fe", 32) }},
		{"catalogue digest", func(c *cache.Context) { c.CatalogDigest = strings.Repeat("dc", 32) }},
		{"test command", func(c *cache.Context) { c.TestCommand = []string{"go", "test", "./internal/..."} }},
		{
			// The argv length is hashed before the elements precisely so that
			// this cannot collide with the base: "go test ./..." and
			// "go" "test ./..." are different commands.
			"test command regrouped",
			func(c *cache.Context) { c.TestCommand = []string{"go", "test ./..."} },
		},
		{"configured timeout", func(c *cache.Context) { c.ConfiguredTimeout = 11 * time.Second }},
		{
			// Zero is "derive it from the baseline", which is a different
			// statement from any number and has to hash as one.
			"a derived timeout rather than an explicit one",
			func(c *cache.Context) { c.ConfiguredTimeout = 0 },
		},
		{"GOOS", func(c *cache.Context) { c.Env["GOOS"] = cache.EnvValue{Value: "windows", Set: true} }},
		{"GOARCH", func(c *cache.Context) { c.Env["GOARCH"] = cache.EnvValue{Value: "arm64", Set: true} }},
		{"GOFLAGS", func(c *cache.Context) { c.Env["GOFLAGS"] = cache.EnvValue{Value: "-tags=x", Set: true} }},
		{
			// `//go:build cgo` decides which files a package has, exactly as
			// GOOS does, so two images that differ only in whether a C compiler
			// is installed must not share a key.
			"CGO_ENABLED",
			func(c *cache.Context) { c.Env["CGO_ENABLED"] = cache.EnvValue{Value: "0", Set: true} },
		},
		{
			"GOEXPERIMENT",
			func(c *cache.Context) { c.Env["GOEXPERIMENT"] = cache.EnvValue{Value: "newinliner", Set: true} },
		},
		{"GODEBUG", func(c *cache.Context) { c.Env["GODEBUG"] = cache.EnvValue{Value: "httplaxcontentlength=1", Set: true} }},
		{
			// The one that is easy to get wrong: an unset variable and one set
			// to nothing are different to the go command, so they have to be
			// different to the key.
			"GOFLAGS set to nothing rather than unset",
			func(c *cache.Context) { c.Env["GOFLAGS"] = cache.EnvValue{Value: "", Set: true} },
		},
	}
	seen := map[string]string{base: "the base context"}
	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			ctx := baseContext()
			c.change(&ctx)
			key := keyOf(t, ctx)
			if key == base {
				t.Fatalf("changing the %s did not change the key", c.field)
			}
			// Distinct from every other variation as well as from the base: two
			// different runs landing on one key is the same bug as a field that
			// does not hash at all.
			if other, clash := seen[key]; clash {
				t.Fatalf("the %s hashes the same as %s", c.field, other)
			}
			seen[key] = "the " + c.field
		})
	}
	if got, want := len(seen), len(cases)+1; got != want {
		t.Errorf("the table produced %d distinct keys, want %d", got, want)
	}
}

// TestKeyRefusesAContextThatCouldNotIdentifyARun covers the fields whose
// absence would make one key serve two runs.
func TestKeyRefusesAContextThatCouldNotIdentifyARun(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*cache.Context){
		"no tool version":      func(c *cache.Context) { c.ToolVersion = "" },
		"no executable digest": func(c *cache.Context) { c.ToolDigest = "" },
		"a short digest":       func(c *cache.Context) { c.ToolDigest = "abcd" },
		// Without it every toolchain shares one bucket, which is the whole of
		// what the field was added to prevent.
		"no toolchain version": func(c *cache.Context) { c.ToolchainVersion = "" },
		"an uppercase digest":  func(c *cache.Context) { c.WorkspaceDigest = strings.ToUpper(c.WorkspaceDigest) },
		"no workspace digest":  func(c *cache.Context) { c.WorkspaceDigest = "" },
		"no catalogue digest":  func(c *cache.Context) { c.CatalogDigest = "" },
		"no test command":      func(c *cache.Context) { c.TestCommand = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := baseContext()
			change(&ctx)
			if _, err := ctx.Key(); err == nil {
				t.Fatal("the context produced a key")
			} else if code := cache.CodeOf(err); code != cache.CodeInvalidContext {
				t.Errorf("code = %q, want %q (%v)", code, cache.CodeInvalidContext, err)
			}
		})
	}
}

// TestContextKeyIsAPrefixOfTheKey pins the relationship the directory layout
// depends on: the directory name is the key truncated, never a second hash.
func TestContextKeyIsAPrefixOfTheKey(t *testing.T) {
	t.Parallel()

	ctx := baseContext()
	key := keyOf(t, ctx)
	short, err := ctx.ContextKey()
	if err != nil {
		t.Fatalf("ContextKey: %v", err)
	}
	if len(short) != cache.ContextKeyLength {
		t.Errorf("the context key %q is %d characters, want %d", short, len(short), cache.ContextKeyLength)
	}
	if !strings.HasPrefix(key, short) {
		t.Errorf("the context key %q is not a prefix of %q", short, key)
	}
}

// TestKeyEnvIsSortedAndItsOwnCopy checks the list the recipe, the documentation,
// and the tests all read.
func TestKeyEnvIsSortedAndItsOwnCopy(t *testing.T) {
	t.Parallel()

	names := cache.KeyEnv()
	if !slices.IsSorted(names) {
		t.Errorf("KeyEnv is not sorted: %v", names)
	}
	for _, want := range []string{"CGO_ENABLED", "GOARCH", "GODEBUG", "GOEXPERIMENT", "GOFLAGS", "GOOS"} {
		if !slices.Contains(names, want) {
			t.Errorf("KeyEnv does not name %s", want)
		}
	}
	names[0] = "clobbered"
	if slices.Contains(cache.KeyEnv(), "clobbered") {
		t.Error("KeyEnv handed out its own slice")
	}
}

// TestEnvFromReadsExactlyTheKeyVariables proves the reader distinguishes unset
// from empty, which the key relies on and the process environment cannot state.
func TestEnvFromReadsExactlyTheKeyVariables(t *testing.T) {
	t.Parallel()

	env := cache.EnvFrom(func(name string) (string, bool) {
		if name == "GOFLAGS" {
			return "", false
		}
		return "value-of-" + name, true
	})
	if got, want := len(env), len(cache.KeyEnv()); got != want {
		t.Errorf("EnvFrom read %d variables, want %d", got, want)
	}
	if flags := env["GOFLAGS"]; flags.Set {
		t.Errorf("GOFLAGS = %+v, want unset", flags)
	}
	if goos := env["GOOS"]; !goos.Set || goos.Value != "value-of-GOOS" {
		t.Errorf("GOOS = %+v, want the looked-up value", goos)
	}
}

// TestToolDigestIsThisBinary is the one place the executable is really read.
//
// Under `go test` the executable is this package's test binary, which is
// exactly why [cache.ToolDigest] is a function the caller calls once rather
// than something [cache.Context.Key] does for itself: a key that read it would
// change from one test run to the next.
func TestToolDigestIsThisBinary(t *testing.T) {
	t.Parallel()

	digest, err := cache.ToolDigest()
	if err != nil {
		t.Fatalf("ToolDigest: %v", err)
	}
	if !mutation.IsDigest(digest) {
		t.Errorf("the digest %q is not 64 lowercase hex characters", digest)
	}
	again, err := cache.ToolDigest()
	if err != nil {
		t.Fatalf("ToolDigest again: %v", err)
	}
	if digest != again {
		t.Errorf("two readings of one executable gave %s and %s", digest, again)
	}
}
