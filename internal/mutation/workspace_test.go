// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"strings"
	"testing"
)

// workspaceCandidate is one edit in one module, for the tests below.
//
// Everything about it is the same in both modules except the module path and
// the digest of the file, which is the shape a workspace produces and the
// shape a path-keyed catalogue cannot tell apart: two modules of one workspace
// routinely hold a file with the same module-relative name, and there is no
// rule anywhere saying they may not.
func workspaceCandidate(module, source string) Candidate {
	return Candidate{
		ModulePath:   module,
		Path:         "app.go",
		Rule:         canonicalRule("eq-to-neq"),
		Span:         Span{StartByte: 40, EndByte: 42},
		Original:     "==",
		Replacement:  "!=",
		SourceDigest: DigestString(source),
	}
}

// canonicalRule is one rule of the canonical registry, by name.
func canonicalRule(name string) Rule {
	for _, rule := range CanonicalRegistry().Rules() {
		if rule.Name == name {
			return rule
		}
	}
	panic("mutation: the canonical registry has no " + name)
}

// TestACatalogueTellsTwoModulesApartByTheirModulePath is the whole of what a
// workspace asks of the catalogue, in the four places that assume a path is a
// coordinate on its own.
//
// Two modules of one workspace can each hold an `app.go`, and their files are
// different files. A builder keyed on the path alone says the opposite four
// times over: it reports conflicting source digests for what it thinks is one
// file, conflicting original text for what it thinks is one span, sorts the
// two modules' mutants into each other, and deduplicates one edit away as a
// repeat of the other. Every one of those is a wrong answer rather than a
// missing feature, which is why they are one test.
func TestACatalogueTellsTwoModulesApartByTheirModulePath(t *testing.T) {
	t.Parallel()

	first := workspaceCandidate("example.com/ws/lib", "package lib\n")
	second := workspaceCandidate("example.com/ws/app", "package app\n")

	builder := NewBuilder()
	if err := builder.AddAll([]Candidate{first, second}); err != nil {
		t.Fatalf("adding one edit from each of two modules: %v", err)
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if catalog.Len() != 2 {
		t.Fatalf("the catalogue holds %d mutants, want 2; two modules' files are two files",
			catalog.Len())
	}
	if len(catalog.Duplicates()) != 0 {
		t.Errorf("one module's edit was dropped as a duplicate of the other's: %+v",
			catalog.Duplicates())
	}
	mutants := catalog.Mutants()
	if mutants[0].ID == mutants[1].ID {
		t.Errorf("both modules' edits minted the identity %s", mutants[0].ID)
	}
	// Module first, so a module's mutants are contiguous in the catalogue and
	// in the dense index it assigns: a workspace run is N module runs, and a
	// run that had to scan the whole catalogue to find its own is a run whose
	// indices say nothing about who they belong to.
	if got := mutants[0].ModulePath; got != "example.com/ws/app" {
		t.Errorf("the first mutant belongs to %s, want the module that sorts first", got)
	}
	if got := mutants[1].ModulePath; got != "example.com/ws/lib" {
		t.Errorf("the second mutant belongs to %s, want the module that sorts second", got)
	}
}

// TestACatalogueStillRefusesTwoAnswersAboutOneFile is the other direction, and
// the reason the module path is added to those keys rather than taken out of
// them.
//
// The conflicts exist because a discovery that reported two digests for one
// file has gone wrong in a way no later phase could diagnose. Widening the key
// by the module keeps exactly that: within one module, one path is still one
// file, and one span of it is still one piece of original text.
func TestACatalogueStillRefusesTwoAnswersAboutOneFile(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		second func(Candidate) Candidate
		want   error
	}{
		{
			name: "two digests for one file",
			second: func(c Candidate) Candidate {
				c.SourceDigest = DigestString("package app // and then an edit\n")
				return c
			},
			want: ErrSourceDigestConflict,
		},
		{
			name: "two originals for one span",
			second: func(c Candidate) Candidate {
				c.Rule = canonicalRule("lt-to-le")
				c.Original = "<<"
				c.Replacement = "<"
				return c
			},
			want: ErrOriginalConflict,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			first := workspaceCandidate("example.com/ws/app", "package app\n")
			builder := NewBuilder()
			if err := builder.Add(first); err != nil {
				t.Fatalf("adding the first candidate: %v", err)
			}
			err := builder.Add(tc.second(first))
			if !errors.Is(err, tc.want) {
				t.Errorf("Add = %v, want %v within one module", err, tc.want)
			}
		})
	}
}

// TestACatalogueRefusesAMixOfWorkspaceAndModuleMutants is fail-closed about the
// one state that cannot be true.
//
// A module path decides which recipe mints a mutant's identity, so a catalogue
// holding some candidates with one and some without is a catalogue whose
// mutants were minted under two different domains. There is no run that
// produces that: a workspace run gives every module a path, and a
// single-module run gives none. It is a bug, and a catalogue that carried it
// would be one nothing downstream could reason about -- neither the report
// keyed on module path, nor the cache, nor the runtime's index table.
func TestACatalogueRefusesAMixOfWorkspaceAndModuleMutants(t *testing.T) {
	t.Parallel()

	workspace := workspaceCandidate("example.com/ws/app", "package app\n")
	alone := workspaceCandidate("", "package app\n")
	alone.Path = "other.go"

	for _, order := range [][]Candidate{{workspace, alone}, {alone, workspace}} {
		builder := NewBuilder()
		err := builder.AddAll(order)
		if err == nil {
			_, err = builder.Build()
		}
		if !errors.Is(err, ErrModulePathMixed) {
			t.Errorf("a catalogue of %q then %q = %v, want %v",
				order[0].ModulePath, order[1].ModulePath, err, ErrModulePathMixed)
			continue
		}
		// Both candidates are named, whichever order they arrived in: the one
		// that was refused and the one it disagrees with. A message naming
		// only the second is a message that sends the reader to the file that
		// is not the surprising one half the time.
		for _, candidate := range order {
			if !strings.Contains(err.Error(), candidate.Where()) {
				t.Errorf("the refusal does not name %s: %v", candidate.Where(), err)
			}
		}
	}
}

// TestAModulePathTheCatalogueCannotHashIsRefusedWhereItIsAdded keeps the
// identity's own rule reachable from the phase that builds on it.
func TestAModulePathTheCatalogueCannotHashIsRefusedWhereItIsAdded(t *testing.T) {
	t.Parallel()

	candidate := workspaceCandidate("example.com/ws/app@v2", "package app\n")
	err := NewBuilder().Add(candidate)
	if !errors.Is(err, ErrInvalidModulePath) {
		t.Errorf("Add with a versioned module path = %v, want %v", err, ErrInvalidModulePath)
	}
	if err != nil && !strings.Contains(err.Error(), "example.com/ws/app@v2") {
		t.Errorf("the message does not name the path: %v", err)
	}
}

// TestWhereNamesTheFileAReaderHasToOpen pins the one sentence every refusal in
// this package builds its message out of.
//
// Outside a workspace a module-relative path is the whole answer, and that is
// what every message here said before workspaces existed. Inside one the path
// is relative to a module that is not the only module, so "app.go" is a
// sentence about two files and the module has to be named too.
func TestWhereNamesTheFileAReaderHasToOpen(t *testing.T) {
	t.Parallel()

	alone := workspaceCandidate("", "package app\n")
	if got := alone.Where(); got != "app.go" {
		t.Errorf("a candidate of a single-module run is at %q, want just its path", got)
	}
	inWorkspace := workspaceCandidate("example.com/ws/app", "package app\n")
	if got := inWorkspace.Where(); got != "example.com/ws/app app.go" {
		t.Errorf("a candidate of a workspace is at %q, want its module and its path", got)
	}
}
