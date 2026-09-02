// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// This phase serves two trees now, and the only difference between them is
// which one it asks the instrumenter for. Everything else — one build, the
// pristine gate, per-file isolation, rejections in catalogue order — is the
// same work over different bytes, which is the whole reason the mode is a field
// rather than a second phase.
//
// The two tests below are about the field and nothing else, so the compiler is
// a stub that always agrees: what they assert is which bytes ended up in the
// snapshot. Whether the probe form compiles is a question for a compiler, and
// [TestValidateProbeTreeRejectsOnlyTheSiteThatCannotCompile] asks a real one.

// modeSource is the file both tests validate: one `return` with two results and
// one return-value candidate in it.
const modeSource = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package sample

// Measure hands back what it was given, so that one statement holds both a
// value the return-replacement family rewrites and a second result the probe
// has to declare a temporary for anyway.
func Measure(count int, err error) (int, error) {
	return count, err
}
`

// TestValidateDefaultsToTheMutantMode pins the zero value.
//
// Every caller written before there was a probe tree passes no mode at all, and
// has to keep getting the tree it always got: guards, an activation array, and
// a snapshot one build can serve every mutant from.
func TestValidateDefaultsToTheMutantMode(t *testing.T) {
	t.Parallel()

	out := validateModeFixture(t, ModeUnset)
	if !bytes.Contains(out, []byte("__gm.M[0]")) {
		t.Errorf("the default mode did not write a guard:\n%s", out)
	}
	if bytes.Contains(out, []byte("Infect(")) {
		t.Errorf("the default mode wrote a probe:\n%s", out)
	}
}

// TestValidateThreadsTheProbeModeIntoTheRewrite is the other half: the mode
// reaches both instrumentation calls this phase makes, the whole-tree one and
// the per-file one the search rewrites through.
//
// Only the second is visible here, and deliberately so. The search leaves each
// file holding the subset it accepted, so what is on disk when the phase
// returns was written by [validator.instrumentFile] — and a mode that reached
// the first call and not the second would produce a probe tree that quietly
// turned back into a mutant tree the moment anything was bisected.
func TestValidateThreadsTheProbeModeIntoTheRewrite(t *testing.T) {
	t.Parallel()

	out := validateModeFixture(t, instrument.ModeProbe)
	if !bytes.Contains(out, []byte("__gm.Infect(0)")) {
		t.Errorf("probe mode did not write a probe:\n%s", out)
	}
	if bytes.Contains(out, []byte(".M[")) {
		t.Errorf("probe mode wrote an activation flag:\n%s", out)
	}
}

// ModeUnset is the zero [instrument.Mode], named so that a test asking for the
// default says so rather than spelling a constant that means something else.
const ModeUnset instrument.Mode = 0

// validateModeFixture runs the whole phase over [modeSource] in one mode, with
// a compiler that always agrees, and returns the bytes the snapshot was left
// holding.
//
// The build is stubbed rather than run because these tests are about which
// rewrite the phase asked for, and a real toolchain would answer a different
// question far more slowly. The rewrite itself is the real one: the search's
// last act is to write each file's accepted subset, so the file on disk is what
// [validator.instrumentFile] produced.
func validateModeFixture(t *testing.T, mode instrument.Mode) []byte {
	t.Helper()

	root := t.TempDir()
	const rel = "sample.go"
	if err := os.WriteFile(filepath.Join(root, rel), []byte(modeSource), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	catalog, hints := modeCatalog(t, rel)
	v := &validator{
		root:     root,
		catalog:  catalog,
		hints:    hints,
		mode:     mode,
		byPath:   make(map[string][]mutation.Mutant),
		pristine: make(map[string][]byte),
		guards:   make(map[string]int),
	}
	v.apply = v.instrumentFile
	v.build = func(context.Context) (verdict, error) { return verdict{}, nil }

	result, err := v.run(context.Background(), "example.com/mini")
	if err != nil {
		t.Fatalf("validating in mode %d: %v", mode, err)
	}
	if len(result.Rejected) != 0 {
		t.Fatalf("a compiler that always agrees produced %d rejections", len(result.Rejected))
	}

	out, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("reading the rewritten fixture: %v", err)
	}
	return out
}

// modeCatalog is [modeSource]'s one candidate and the hint discovery would have
// computed for it: a Form S statement site, and the probe hint naming both
// results of the statement it sits in.
func modeCatalog(t *testing.T, rel string) (*mutation.Catalog, instrument.Hints) {
	t.Helper()

	rule, ok := mutation.CanonicalRegistry().Lookup("return-zero-numeric")
	if !ok {
		t.Fatal("the canonical registry does not know return-zero-numeric")
	}
	stmt := strings.Index(modeSource, "return count, err")
	if stmt < 0 {
		t.Fatal("the fixture no longer holds the statement these tests are about")
	}
	statement := mutation.Span{StartByte: uint32(stmt), EndByte: uint32(stmt + len("return count, err"))}
	value := mutation.Span{StartByte: uint32(stmt + len("return ")), EndByte: uint32(stmt + len("return count"))}

	candidate := mutation.Candidate{
		Path:         rel,
		Rule:         rule,
		Span:         value,
		Original:     "count",
		Replacement:  "0",
		SourceDigest: mutation.Digest([]byte(modeSource)),
	}
	builder := mutation.NewBuilder()
	if err := builder.Add(candidate); err != nil {
		t.Fatalf("cataloguing the fixture's candidate: %v", err)
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	id, err := candidate.ID()
	if err != nil {
		t.Fatalf("identifying the candidate: %v", err)
	}
	return catalog, instrument.Hints{id: discover.Guard{
		Form:     discover.GuardFormS,
		SiteSpan: statement,
		Return: &discover.ReturnSite{
			Span:  statement,
			Types: []string{"int", "error"},
			Index: 0,
		},
	}}
}
