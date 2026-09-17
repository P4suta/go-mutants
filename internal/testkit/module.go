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

type Module struct {
	t    testing.TB
	root string
}

func NewModule(t testing.TB) *Module {
	t.Helper()
	root := filepath.Join(Scratch(t), "module")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating the module directory %s: %v", root, err)
	}
	logInputs(t, "scratch="+root)
	return &Module{t: t, root: root}
}

func NewModuleAt(t testing.TB, root string) *Module {
	t.Helper()
	return &Module{t: t, root: root}
}

func (m *Module) Root() string { return m.root }

func (m *Module) Path(rel string) string {
	return filepath.Join(m.root, filepath.FromSlash(rel))
}

func (m *Module) Module(path string) *Module {
	m.t.Helper()
	directive, err := goDirective(Root(m.t))
	if err != nil {
		m.t.Fatalf("reading this repository's go directive: %v", err)
	}
	return m.File("go.mod", SPDXHeader+"module "+path+"\n\ngo "+directive+"\n")
}

func (m *Module) File(rel, contents string) *Module {
	m.t.Helper()
	path := m.Path(rel)
	WriteFile(m.t, path, []byte(contents))
	agePath(m.t, m.root, path)
	return m
}

func (m *Module) Source(rel, body string) *Module {
	m.t.Helper()
	return m.File(rel, SPDXHeader+body)
}

func (m *Module) From(name string) *Module {
	m.t.Helper()
	CopyTree(m.t, Fixture(m.t, name), m.root)
	AgeTree(m.t, m.root)
	return m
}

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
			continue
		}
		return version, nil
	}
	return "", fmt.Errorf("%s has no go directive", gomod)
}
