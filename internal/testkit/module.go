// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Module builds a Go module for one test: from nothing, from a corpus fixture,
// or from a fixture with files added on top.
//
// It exists because the corpus cannot hold every module a test needs. A fixture
// is checked in, and some of the modules the engine has to be proven against
// are things this repository refuses to check in — a CRLF tree, which
// `.gitattributes` would deliver as CRLF everywhere and which would change every
// mutant ID that covers it; a module whose `go` directive has to match whatever
// toolchain the run is using; a one-file module that exists to make a single
// diagnostic happen. Those are synthesized, and everything a synthesized module
// needs to be indistinguishable from a real one — the SPDX header, the
// repository's own `go` directive, an aged tree — is applied by the builder
// rather than remembered by each caller.
//
// Every method that writes returns the builder, so a module is one expression;
// every method that writes also ages what it wrote, for the reason [TreeAge]
// gives. Later writes win over earlier ones, which is what makes
// `.From("simple").Source("simple.go", …)` mean "the fixture with this file
// replaced".
type Module struct {
	t    testing.TB
	root string
}

// NewModule starts an empty module under a directory of the test's own.
func NewModule(t testing.TB) *Module {
	t.Helper()
	root := filepath.Join(t.TempDir(), "module")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating the module directory %s: %v", root, err)
	}
	logInputs(t, "scratch="+root)
	return &Module{t: t, root: root}
}

// NewModuleAt wraps a directory the test already has — a [Copy] of a fixture,
// most often — so that the builder's writers can be used on it.
//
// It creates nothing and copies nothing: the directory is the module, and the
// caller owns its lifetime.
func NewModuleAt(t testing.TB, root string) *Module {
	t.Helper()
	return &Module{t: t, root: root}
}

// Root returns the module's directory.
func (m *Module) Root() string { return m.root }

// Path resolves one slash-separated path inside the module.
//
// Callers spell these the way Go spells them — `pkg/inner/inner.go` — and this
// is the one place that turns such a path into a native one, so no call site
// has a filepath.Join of its own or a `\` in a literal.
func (m *Module) Path(rel string) string {
	return filepath.Join(m.root, filepath.FromSlash(rel))
}

// Module writes the go.mod, naming the module path and carrying this
// repository's own `go` directive.
//
// The directive is read from the repository's go.mod rather than written down
// here, and that is the point: a directive newer than the toolchain in use makes
// every command against the module ask to download another one, which
// GOTOOLCHAIN=local — set by the environment policy, so that no test can reach
// the network for a compiler — turns into an error. A second copy of the version
// number would be a second thing to bump.
func (m *Module) Module(path string) *Module {
	m.t.Helper()
	directive, err := goDirective(Root(m.t))
	if err != nil {
		m.t.Fatalf("reading this repository's go directive: %v", err)
	}
	// The header goes on go.mod because every corpus module carries one — see
	// fixtures/simple/go.mod — and fixtures/README.md says why: the licensing
	// check and `gofmt -l .` walk the filesystem rather than the module graph, so
	// a synthesized module promoted into the corpus is already compliant. (This
	// repository's own go.mod is the exception, annotated in REUSE.toml instead,
	// because release tooling rewrites it.) A `go mod tidy` run against the
	// module may drop the comment, which is a reason to write it here rather than
	// to expect it to survive one.
	return m.File("go.mod", SPDXHeader+"module "+path+"\n\ngo "+directive+"\n")
}

// File writes one file into the module, creating the directories above it.
//
// Only what was written is aged — the file and the directories above it — so a
// module built up file by file costs one stamp per directory rather than a walk
// of the whole tree per write.
func (m *Module) File(rel, contents string) *Module {
	m.t.Helper()
	path := m.Path(rel)
	WriteFile(m.t, path, []byte(contents))
	agePath(m.t, m.root, path)
	return m
}

// Source writes one Go file into the module, with the [SPDXHeader] in front of
// the body.
func (m *Module) Source(rel, body string) *Module {
	m.t.Helper()
	return m.File(rel, SPDXHeader+body)
}

// From copies a corpus fixture into the module.
//
// It is how a synthesized module starts from a real one: the fixture's go.mod
// and sources arrive as they are, and anything written afterwards replaces what
// the fixture had at that path.
func (m *Module) From(name string) *Module {
	m.t.Helper()
	CopyTree(m.t, Fixture(m.t, name), m.root)
	AgeTree(m.t, m.root)
	return m
}

// CRLF rewrites every Go source file and go.mod in the module to CRLF line
// endings, changing nothing else.
//
// This is a fixture that cannot be checked in. `.gitattributes` pins `* -text`,
// so a CRLF file in the corpus would be CRLF on every platform — and line
// endings are part of a file's bytes, so it would change every mutant ID that
// covers it and make the corpus a worse test of the instrumenter rather than a
// better one. The instrumenter is a byte rewriter whose claim is that it
// preserves everything it did not mutate, and CRLF is the sharpest way to state
// that claim, so the module gets built and converted here instead.
//
// Only the line endings move. The bytes between them are untouched, files that
// are not Go source are left alone, and a file that is already CRLF is returned
// unchanged rather than turned into CR CR LF, so the call is idempotent.
func (m *Module) CRLF() *Module {
	m.t.Helper()
	err := filepath.WalkDir(m.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if name := entry.Name(); name != "go.mod" && !strings.HasSuffix(name, ".go") {
			return nil
		}
		// os.WriteFile follows a symlink, so a `.go` link in the module would
		// have had CRLF written onto whatever it points at — a file outside the
		// module. Refused, by [CopyTree]'s rule.
		if irregular := refuseIrregular(path, entry); irregular != nil {
			return irregular
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		converted := bytes.ReplaceAll(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
		if bytes.Equal(converted, data) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(path, converted, info.Mode().Perm())
	})
	if err != nil {
		m.t.Fatalf("rewriting the module at %s to CRLF: %v", m.root, err)
	}
	AgeTree(m.t, m.root)
	return m
}

// goDirective reads the `go` version out of a module's go.mod.
func goDirective(root string) (string, error) {
	gomod := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(gomod)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", gomod, err)
	}
	for line := range strings.Lines(string(data)) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "go")
		if !ok {
			continue
		}
		version := strings.TrimSpace(rest)
		if version == rest || version == "" {
			// "gopath" rather than "go 1.26": a different directive.
			continue
		}
		return version, nil
	}
	return "", fmt.Errorf("%s has no go directive", gomod)
}
