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

const ModeUnset instrument.Mode = 0

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
		modules:  []Module{{Dir: ".", Path: "example.com/mini"}},
		byPath:   make(map[string][]mutation.Mutant),
		pristine: make(map[string][]byte),
		guards:   make(map[string]int),
		files:    make(map[string]fileRef),
	}
	v.apply = v.instrumentFile
	v.build = func(context.Context) (verdict, error) { return verdict{}, nil }

	result, err := v.run(context.Background())
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
		Probe: &discover.ProbeSite{
			Form:  discover.ProbeFormReturn,
			Span:  statement,
			Types: []string{"int", "error"},
			Index: 0,
		},
	}}
}
