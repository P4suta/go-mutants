// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"strings"
	"testing"
)

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

func canonicalRule(name string) Rule {
	for _, rule := range CanonicalRegistry().Rules() {
		if rule.Name == name {
			return rule
		}
	}
	panic("mutation: the canonical registry has no " + name)
}

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
	if got := mutants[0].ModulePath; got != "example.com/ws/app" {
		t.Errorf("the first mutant belongs to %s, want the module that sorts first", got)
	}
	if got := mutants[1].ModulePath; got != "example.com/ws/lib" {
		t.Errorf("the second mutant belongs to %s, want the module that sorts second", got)
	}
}

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
		for _, candidate := range order {
			if !strings.Contains(err.Error(), candidate.Where()) {
				t.Errorf("the refusal does not name %s: %v", candidate.Where(), err)
			}
		}
	}
}

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
