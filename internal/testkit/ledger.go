// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const KeptFileName = "KEPT.txt"

const ledgerArgvLimit = 200

type ledger struct {
	mu           sync.Mutex
	policy       Keep
	fixture      string
	toolchain    string
	argv         []string
	argvDropped  int
	notes        []string
	dirs         []string
	sections     []keptSection
	kept         string
	dumps        int
	scratchTaken bool
	forced       bool
}

type keptSection struct {
	title string
	lines func() []string
}

var ledgers struct {
	mu     sync.Mutex
	byTest map[string]*ledger
}

func ledgerFor(t testing.TB) *ledger {
	ledgers.mu.Lock()
	defer ledgers.mu.Unlock()
	if ledgers.byTest == nil {
		ledgers.byTest = map[string]*ledger{}
	}
	name := t.Name()
	if existing, ok := ledgers.byTest[name]; ok {
		return existing
	}
	fresh := &ledger{policy: KeepPolicy()}
	ledgers.byTest[name] = fresh
	t.Cleanup(func() {
		ledgers.mu.Lock()
		defer ledgers.mu.Unlock()
		delete(ledgers.byTest, name)
	})
	return fresh
}

func policyFor(t testing.TB) Keep {
	ledgers.mu.Lock()
	existing, ok := ledgers.byTest[t.Name()]
	ledgers.mu.Unlock()
	if !ok {
		return KeepPolicy()
	}
	existing.mu.Lock()
	defer existing.mu.Unlock()
	return existing.policy
}

func KeepPath(t testing.TB, path, why string) {
	t.Helper()
	if KeepPolicy() == KeepNever {
		return
	}
	l := ledgerFor(t)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.notes = append(l.notes, path+" ("+why+")")
}

func KeepSection(t testing.TB, title string, lines func() []string) {
	t.Helper()
	if KeepPolicy() == KeepNever {
		return
	}
	l := ledgerFor(t)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sections = append(l.sections, keptSection{title: title, lines: lines})
}

func rememberChild(t testing.TB, r Result) {
	if KeepPolicy() == KeepNever {
		return
	}
	l := ledgerFor(t)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.argv) >= ledgerArgvLimit {
		l.argv = append(l.argv[:0], l.argv[1:]...)
		l.argvDropped++
	}
	l.argv = append(l.argv, r.command())
}

func rememberFixture(t testing.TB, path string) {
	if KeepPolicy() == KeepNever {
		return
	}
	l := ledgerFor(t)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fixture = path
}

func rememberToolchain(t testing.TB, path string) {
	if KeepPolicy() == KeepNever {
		return
	}
	l := ledgerFor(t)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.toolchain = path
}

type account struct {
	policy      Keep
	fixture     string
	toolchain   string
	argv        []string
	argvDropped int
	notes       []string
	siblings    []string
	sections    []keptSection
}

func (l *ledger) snapshot(dir string) account {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := account{
		policy:      l.policy,
		fixture:     l.fixture,
		toolchain:   l.toolchain,
		argv:        slices.Clone(l.argv),
		argvDropped: l.argvDropped,
		notes:       slices.Clone(l.notes),
		sections:    slices.Clone(l.sections),
	}
	for _, other := range l.dirs {
		if other != dir {
			a.siblings = append(a.siblings, other)
		}
	}
	return a
}

func testReport(t testing.TB, dir string, l *ledger, policy Keep) string {
	a := l.snapshot(dir)

	outcome := "passed"
	if t.Failed() {
		outcome = "failed"
	}
	gocache, err := BuildCache()
	if err != nil {
		gocache = "unresolved: " + err.Error()
	}

	var b strings.Builder
	b.WriteString(keptReportHeader)
	writeField(&b, "test", t.Name())
	writeField(&b, "outcome", outcome)
	writeField(&b, "policy", policy.String()+" ("+KeepEnv+")")
	writeField(&b, "scratch", dir)
	writeField(&b, "fixture", a.fixture)
	writeField(&b, "toolchain", a.toolchain)
	writeField(&b, "gocache", gocache)

	if len(a.siblings) != 0 || len(a.notes) != 0 {
		b.WriteString("\nAlso kept:\n")
		for _, sibling := range a.siblings {
			b.WriteString("  " + sibling + "\n")
		}
		for _, note := range a.notes {
			b.WriteString("  " + note + "\n")
		}
	}
	if len(a.argv) != 0 || a.argvDropped != 0 {
		fmt.Fprintf(&b, "\nChildren run through testkit.Exec (%d, most recent last):\n",
			len(a.argv)+a.argvDropped)
		if a.argvDropped != 0 {
			fmt.Fprintf(&b, "  … %d earlier command(s) elided\n", a.argvDropped)
		}
		for _, line := range a.argv {
			b.WriteString("  " + line + "\n")
		}
	}
	for _, section := range a.sections {
		b.WriteString("\n" + section.title + ":\n")
		for _, line := range section.lines() {
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteString(keptReportFooter)
	return b.String()
}

func packageReport(dir, name string, policy Keep, failed bool) string {
	outcome := "passed"
	if failed {
		outcome = "failed"
	}
	var b strings.Builder
	b.WriteString(keptReportHeader)
	writeField(&b, "package", packageShortName())
	writeField(&b, "shared", name)
	writeField(&b, "outcome", outcome)
	writeField(&b, "policy", policy.String()+" ("+KeepEnv+")")
	writeField(&b, "scratch", dir)
	b.WriteString(keptReportFooter)
	return b.String()
}

func writeField(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "  %-10s %s\n", name, value)
}

const keptReportHeader = "This directory was kept because the go-mutants test harness was asked to keep it.\n\n"

const keptReportFooter = "\nNothing here is precious: `mise run test-clean` empties the whole kept root.\n"

func writeReport(dir, body string) {
	final := filepath.Join(dir, KeptFileName)
	temporary := final + ".writing"
	if err := os.WriteFile(temporary, []byte(body), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "testkit: the account of %s could not be written: %v\n", dir, err)
		return
	}
	if err := os.Rename(temporary, final); err != nil {
		fmt.Fprintf(os.Stderr, "testkit: the account of %s could not be put in place: %v\n", dir, err)
		_ = os.Remove(temporary)
	}
}
