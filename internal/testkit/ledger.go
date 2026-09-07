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

// KeptFileName is the account a kept directory carries: which test filed it,
// over which fixture, with which toolchain, what else it kept, and what it ran.
const KeptFileName = "KEPT.txt"

// ledgerArgvLimit is how many child commands one test's account holds.
//
// It is bounded for the same reason the trace ring is: an execution phase runs
// thousands of children, the account is kept for the whole life of the test
// binary, and the last two hundred are what a reader wants. What fell off is
// counted, so the account says how much of itself is missing.
const ledgerArgvLimit = 200

// A ledger is what one test did, kept until the test ends so that a directory
// filed at the end of it can say so.
//
// The policy is captured when the ledger is made rather than read again from the
// environment, so every directory of one test is created, judged and reported
// under the same rule.
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

// A keptSection is a titled block another package contributes to the account.
type keptSection struct {
	title string
	lines func() []string
}

// ledgers holds one ledger per test, for as long as the test runs.
//
// A map keyed by the test's name rather than a value threaded through every
// helper, because the helpers that record — [Exec], [Copy], [GoBinary] — are
// spread across the package and take a [testing.TB] and nothing else. The
// entry is dropped by a cleanup, and the directory that writes the account
// holds the pointer rather than the key: cleanups run last-registered-first, so
// a key looked up at the end would have been dropped by whichever helper
// created the entry.
var ledgers struct {
	mu     sync.Mutex
	byTest map[string]*ledger
}

// ledgerFor returns the ledger of one test, creating it on first use.
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

// policyFor is the policy this test's directories were made under, or the
// environment's answer for a test that has taken none.
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

// KeepPath marks a directory the test does not own as worth keeping, with the
// reason it is.
//
// It adds a line to [KeptFileName] rather than moving anything: the path is
// somewhere else on purpose — a session's temporary parent, a snapshot the
// engine kept — and a reader who has the account has the path. A directory that
// is already under a [Scratch] is covered by that directory being kept and needs
// no call.
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

// KeepSection adds a titled block to [KeptFileName].
//
// The lines are produced when the account is written rather than when the
// section is added, because what a section holds — the tail of a recording, the
// state of a run — is only interesting as it was at the end. It is how
// internal/testkit/mutantkit puts a trace tail in the account without this
// package knowing what a trace is.
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

// rememberChild records one command a test ran, for the account a kept
// directory carries.
//
// Nothing is recorded when nothing is being kept, which is the default: a
// developer's `go test ./...` pays for none of this.
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

// rememberFixture and rememberToolchain record the two facts a reader asks for
// first, from the constructors that resolved them.
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

// account is one ledger as the report renders it, taken under the lock and read
// outside it.
//
// The copy exists so that the [KeepSection] callbacks — which are somebody
// else's code, and in mutantkit's case walk a whole recording — run with no lock
// held. A callback that reached back into the harness while the ledger was
// locked would deadlock in a cleanup, which is the least debuggable place in a
// test run.
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

// snapshot copies what the report needs, and the siblings of one directory.
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

// testReport is the account one kept directory carries.
//
// The four facts at the top are the ones a failure in CI is diagnosed from, and
// each is something the harness knew and the test never printed: which fixture,
// which toolchain, which scratch directory, which build cache. The commands
// under them are what makes the directory reproducible rather than merely
// present.
//
// The `Also kept:` block is what makes several directories one piece of
// evidence. A real integration test takes a scratch for its environment, one for
// each snapshot and one for a fixture copy; they are siblings under one package
// directory, named after the same test with different suffixes, and a reader who
// opens one has no way to know the others exist. Every account names every
// other.
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

// packageReport is [testReport] for a directory a TestMain owns, which has a
// package rather than a test and a status rather than a verdict.
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

// writeField writes one aligned `name  value` row, and nothing at all for a
// fact nobody recorded — an empty row would read as a fact whose value was the
// empty string.
func writeField(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "  %-10s %s\n", name, value)
}

const keptReportHeader = "This directory was kept because the go-mutants test harness was asked to keep it.\n\n"

const keptReportFooter = "\nNothing here is precious: `mise run test-clean` empties the whole kept root.\n"

// writeReport writes an account beside what it is about, and says nothing when
// it cannot: a directory that was kept is worth more than the note explaining
// it, and a failure here must not turn a kept directory into a failed cleanup.
//
// It is written to a neighbouring file and renamed, so that a reader — or the
// artifact upload of a job that is being cancelled — never finds half an
// account. Rename is atomic on both filesystems this runs on when the two paths
// share a directory, which is why the temporary file is a sibling rather than a
// name in TMPDIR.
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
