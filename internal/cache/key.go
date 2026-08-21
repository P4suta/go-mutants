// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// Constants of the frozen key recipe.
const (
	// KeyDomain is the domain separator hashed first for every cache key. It
	// carries the recipe version: a future recipe becomes
	// "go-mutants-cache-v3", so that entries filed under v2 can never be read
	// as v3 entries and both can coexist in one directory.
	//
	// v2 added the Go toolchain's own release to the recipe and CGO_ENABLED,
	// GOEXPERIMENT and GODEBUG to [KeyEnv]. Both additions closed holes rather
	// than refining anything — a v1 key was the same across a toolchain upgrade
	// and the same across two images that compile cgo differently — so no v1
	// entry should be read by this build even if the rest of its context
	// matches.
	KeyDomain = "go-mutants-cache-v2"

	// KeyHexLength is the length of a full context key in lowercase hex
	// characters (SHA-256).
	KeyHexLength = 64

	// ContextKeyLength is how many hex characters of the context key name the
	// directory entries are filed in. Sixteen matches the workspace key one
	// level up: short enough for a path on Windows, and long enough that a
	// collision needs about 2^32 distinct contexts on one machine.
	//
	// A collision here would not be silent. Every entry carries the id and the
	// *full* key it was written under — not the truncation that named its
	// directory, which two colliding contexts would agree about — and a read
	// that does not match is a corrupt entry and therefore a miss; see
	// [Entry.check].
	ContextKeyLength = 16
)

// keyEnv are the environment variables that enter the key, sorted.
//
// The list is deliberately short, and every name on it changes what the tests
// compile to or how they are run — which is the only thing a cached outcome is
// entitled to depend on:
//
//   - GOFLAGS, because it is applied to every go command and can change build
//     tags, the linker flags, and therefore the program under test.
//   - GOOS and GOARCH, because build constraints decide which files a package
//     even has. A cached outcome from a linux run says nothing about a windows
//     one.
//   - CGO_ENABLED, for exactly the reason GOOS is here: `//go:build cgo`
//     decides which files a package has. It earns its place twice over because
//     its *default* is not a constant — it is derived from whether a C
//     toolchain is on the machine — so two CI images with identical
//     environments, identical Go versions and identical source can compile
//     different programs.
//   - GOEXPERIMENT, because it changes what the compiler and the runtime do,
//     which is the program under test by another name.
//   - GODEBUG, because since Go 1.21 it selects between old and new behaviour
//     inside the standard library. A test that passes under one setting and
//     fails under the other is exactly the test a mutant is measured by.
//
// Everything else a run reads is already in the key by another route:
// GOMODCACHE and GOPROXY affect what is in the workspace, which the workspace
// digest covers, and GOCACHE affects how fast the build is and not what it
// produces. The one thing that is emphatically *not* an environment variable —
// which Go toolchain the word `go` resolves to — is in the key as
// [Context.ToolchainVersion] rather than being left to this list.
//
// The list is exported through [KeyEnv] so that the recipe, the documentation,
// and the tests read one list rather than three copies of it.
var keyEnv = []string{"CGO_ENABLED", "GOARCH", "GODEBUG", "GOEXPERIMENT", "GOFLAGS", "GOOS"}

// KeyEnv returns the environment variable names that enter the key, sorted.
func KeyEnv() []string { return slices.Clone(keyEnv) }

// An EnvValue is one environment variable as the key saw it.
//
// Set distinguishes an unset variable from one set to the empty string. They
// are not the same to the go command — `GOFLAGS=` overrides a value inherited
// from a `go env -w` file, while an unset GOFLAGS does not — so they must not
// hash alike.
type EnvValue struct {
	Value string
	Set   bool
}

// CurrentEnv reads the key's environment variables from this process.
func CurrentEnv() map[string]EnvValue {
	return EnvFrom(os.LookupEnv)
}

// EnvFrom reads the key's environment variables through lookup, which is what
// lets a test state an environment instead of mutating the process's own.
func EnvFrom(lookup func(string) (string, bool)) map[string]EnvValue {
	env := make(map[string]EnvValue, len(keyEnv))
	for _, name := range keyEnv {
		value, set := lookup(name)
		env[name] = EnvValue{Value: value, Set: set}
	}
	return env
}

// A Context is everything a cached outcome is allowed to depend on except the
// mutant itself.
//
// Two runs with equal contexts would execute the same mutant against the same
// bytes with the same command in the same environment, so one may adopt the
// other's answer. Anything that could make that false has to be in here, and
// the fields are over-specified for exactly that reason: it is far better to
// re-measure a mutant that did not need it than to report a detection that
// never happened.
type Context struct {
	// ToolVersion is the go-mutants version string.
	ToolVersion string
	// ToolDigest is the SHA-256 of the running executable's own bytes, from
	// [ToolDigest]. It is what separates two development builds that call
	// themselves the same version — which is every build between two releases,
	// and exactly when the guard forms and the rule set are changing.
	ToolDigest string
	// ToolchainVersion is the Go toolchain's own release token —
	// `gocmd.Version.Release`, "go1.26.5" or "devel go1.27-a1b2c3d4" — for the
	// toolchain this run located and executed the tests with.
	//
	// It is here because nothing else in the recipe carries it. TestCommand is
	// the argv as the user wrote it, so a project on the default command hashes
	// the literal word "go" and never the compiler that word resolved to;
	// WorkspaceDigest covers go.mod, which pins a language version like
	// `go 1.26` and not a patch release. Without this field a 1.26.5→1.26.6
	// upgrade computes an identical key, and every outcome measured by the old
	// compiler against the old standard library stays reachable.
	//
	// The baseline gate catches an upgrade that breaks the suite outright, but
	// it says nothing about behaviour that differs only on a mutated path, which
	// is the whole of what this cache stores.
	ToolchainVersion string
	// WorkspaceDigest is the snapshot manifest digest: the whole of the code
	// under test, in one field.
	WorkspaceDigest string
	// CatalogDigest is [mutation.Catalog.Digest]: one value over the whole
	// ordered id list, which is exactly the question "are these two runs looking
	// at the same set of mutants?".
	//
	// It is taken from the catalogue rather than recomputed from a list of ids
	// here. internal/mutation already mints it under its own domain separator,
	// `report merge` already compares runs by it, and a second digest of the
	// same set would be a second thing to keep in step — one that could call two
	// catalogues equal that the merge calls different.
	//
	// The whole set is in the key and not merely the mutant being looked up,
	// because instrumentation is a property of the whole tree: every accepted
	// mutant is spliced into the snapshot at once, so a catalogue that gained or
	// lost one produced a different binary for all of them.
	CatalogDigest string
	// TestCommand is the argv the run measures with, as the user wrote it.
	TestCommand []string
	// ConfiguredTimeout is `test.timeout` as the user set it, and zero for a run
	// that derives its own from the baseline.
	//
	// It is the *configured* timeout and deliberately not the effective one,
	// which is the one difference between this recipe and the obvious reading of
	// it. A derived timeout is max(10s, slowest baseline × 5) — a wall-clock
	// measurement — so for any project whose tests take more than two seconds it
	// is a slightly different number on every run. Hashing it would give every
	// run its own context and its own empty directory, which is to say it would
	// switch the cache off for exactly the projects a cache is worth having for,
	// and it would do it silently.
	//
	// The soundness the effective timeout would have bought is not given up. It
	// is bought more precisely at the point of use instead: an entry records the
	// bound its measurement was made under, and one that could not have been
	// reached under this run's bound is not adopted. See [Entry.UsableUnder].
	ConfiguredTimeout time.Duration
	// Env is the key's environment, from [CurrentEnv]. A name missing from the
	// map is hashed as unset.
	Env map[string]EnvValue
}

// Key computes the full context key.
//
// The recipe, frozen as of v2: SHA-256 over the concatenation of
// length-prefixed fields, in order
//
//	enc("go-mutants-cache-v2")
//	enc(tool_version)
//	enc(tool_executable_sha256_hex)
//	enc(toolchain_version)       gocmd.Version.Release
//	enc(workspace_digest_hex)
//	enc(catalog_digest_hex)      mutation.Catalog.Digest()
//	enc(len(test_command))       decimal
//	enc(argv[0]) … enc(argv[n-1])
//	enc("derived") or enc("explicit")
//	enc(configured_timeout_ms)   decimal, 0 when derived
//	for each name in KeyEnv():
//	    enc(name)
//	    enc("set") or enc("unset")
//	    enc(value)               the empty string when unset
//
// where enc(s) is [mutation.WriteLengthPrefixed]: a 4-byte big-endian uint32 of
// the UTF-8 byte length of s followed by the raw bytes. The argv length is
// hashed before the elements so that no regrouping of the command can produce
// another command's key, and the environment is hashed as name/presence/value
// triples so that an unset variable and one set to nothing cannot collide.
func (c Context) Key() (string, error) {
	if err := c.check(); err != nil {
		return "", err
	}
	h := sha256.New()
	fields := []string{
		KeyDomain,
		c.ToolVersion,
		c.ToolDigest,
		c.ToolchainVersion,
		c.WorkspaceDigest,
		c.CatalogDigest,
		strconv.Itoa(len(c.TestCommand)),
	}
	fields = append(fields, c.TestCommand...)
	fields = append(fields, timeoutSource(c.ConfiguredTimeout),
		strconv.FormatInt(milliseconds(c.ConfiguredTimeout), 10))
	for _, name := range keyEnv {
		value := c.Env[name]
		fields = append(fields, name, presence(value.Set), value.Value)
	}
	for _, field := range fields {
		if err := write(h, field); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ContextKey is [Context.Key] truncated to the directory name entries are filed
// under; see [ContextKeyLength].
func (c Context) ContextKey() (string, error) {
	key, err := c.Key()
	if err != nil {
		return "", err
	}
	return key[:ContextKeyLength], nil
}

// check refuses a context that could not honestly identify a run.
//
// The workspace digest and the test command are required because a key without
// either would be shared by runs over different code or different commands,
// which is the one failure a cache must never have. The tool version and digest
// are required because they are what stops a rebuilt go-mutants from reading
// its predecessor's answers, and the toolchain version because it is what stops
// an upgraded Go from reading the old compiler's — a field the caller could
// leave empty would put every toolchain back in one bucket, which is the hole
// [Context.ToolchainVersion] exists to close.
//
// A refusal here is a caller bug rather than a user's problem, and it costs a
// run its cache and nothing else: internal/engine reports it and measures
// everything.
func (c Context) check() error {
	switch {
	case c.ToolVersion == "":
		return invalidContext("no tool version")
	case !mutation.IsDigest(c.ToolDigest):
		return invalidContext("no executable digest")
	case c.ToolchainVersion == "":
		return invalidContext("no Go toolchain version")
	case !mutation.IsDigest(c.WorkspaceDigest):
		return invalidContext("no workspace digest")
	case !mutation.IsDigest(c.CatalogDigest):
		return invalidContext("no catalogue digest")
	case len(c.TestCommand) == 0:
		return invalidContext("no test command")
	}
	return nil
}

// invalidContext builds the error for a context that cannot be hashed.
func invalidContext(what string) error {
	return &Error{
		Code:    CodeInvalidContext,
		Message: "the outcome cache key has " + what + ", so it would not identify this run",
	}
}

// ToolDigest is the SHA-256 of the running executable's own bytes.
//
// It is computed once by the caller and carried in the [Context] rather than
// read here on every key, for two reasons. A digest of a megabyte-scale binary
// is not free, and — the one that matters — a function that read os.Executable
// itself could not be tested: under `go test` the executable is the package's
// own test binary, so the key would change from one test run to the next.
func ToolDigest() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", &Error{
			Code:    CodeExecutableUnreadable,
			Message: "the running go-mutants executable could not be located, so a cache key cannot name this build",
			Err:     err,
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", &Error{
			Code:    CodeExecutableUnreadable,
			Message: "the running go-mutants executable " + path + " could not be read, so a cache key cannot name this build",
			Err:     err,
		}
	}
	return mutation.Digest(data), nil
}

// presence renders whether an environment variable was set at all.
func presence(set bool) string {
	if set {
		return "set"
	}
	return "unset"
}

// timeoutSource renders where the run's per-mutant bound came from.
//
// It is hashed as well as the number so that a project which sets
// `test.timeout` to whatever its derived bound happened to be gets a fresh
// context rather than silently joining the derived one. The two are the same
// number and not the same statement: one is a promise the user made and the
// other is a measurement that will move.
func timeoutSource(configured time.Duration) string {
	if configured > 0 {
		return "explicit"
	}
	return "derived"
}

// write is [mutation.WriteLengthPrefixed] under a shorter name, since this file
// calls it in a loop.
func write(h hash.Hash, s string) error { return mutation.WriteLengthPrefixed(h, s) }

// milliseconds renders a duration the way the key and the entries count one:
// truncated, and never negative.
func milliseconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}
