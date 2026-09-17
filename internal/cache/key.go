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

const (
	KeyDomain = "go-mutants-cache-v2"

	KeyHexLength = 64

	ContextKeyLength = 16
)

var keyEnv = []string{"CGO_ENABLED", "GOARCH", "GODEBUG", "GOEXPERIMENT", "GOFLAGS", "GOOS"}

func KeyEnv() []string { return slices.Clone(keyEnv) }

type EnvValue struct {
	Value string
	Set   bool
}

func CurrentEnv() map[string]EnvValue {
	return EnvFrom(os.LookupEnv)
}

func EnvFrom(lookup func(string) (string, bool)) map[string]EnvValue {
	env := make(map[string]EnvValue, len(keyEnv))
	for _, name := range keyEnv {
		value, set := lookup(name)
		env[name] = EnvValue{Value: value, Set: set}
	}
	return env
}

type Context struct {
	ToolVersion       string
	ToolDigest        string
	ToolchainVersion  string
	WorkspaceDigest   string
	CatalogDigest     string
	TestCommand       []string
	ConfiguredTimeout time.Duration
	Env               map[string]EnvValue
}

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

func (c Context) ContextKey() (string, error) {
	key, err := c.Key()
	if err != nil {
		return "", err
	}
	return key[:ContextKeyLength], nil
}

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

func invalidContext(what string) error {
	return &Error{
		Code:    CodeInvalidContext,
		Message: "the outcome cache key has " + what + ", so it would not identify this run",
	}
}

func ToolDigest() (string, error) {
	path, err := executablePath()
	if err != nil {
		return "", &Error{
			Code:    CodeExecutableUnreadable,
			Message: "the running go-mutants executable could not be located, so a cache key cannot name this build",
			Err:     err,
		}
	}
	data, err := readExecutable(path)
	if err != nil {
		return "", &Error{
			Code:    CodeExecutableUnreadable,
			Message: "the running go-mutants executable " + path + " could not be read, so a cache key cannot name this build",
			Err:     err,
		}
	}
	return mutation.Digest(data), nil
}

func presence(set bool) string {
	if set {
		return "set"
	}
	return "unset"
}

func timeoutSource(configured time.Duration) string {
	if configured > 0 {
		return "explicit"
	}
	return "derived"
}

func write(h hash.Hash, s string) error { return mutation.WriteLengthPrefixed(h, s) }

func milliseconds(d time.Duration) int64 { return max(0, d.Milliseconds()) }
