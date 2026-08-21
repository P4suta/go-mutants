// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
)

// TestInstrumentFileRewritesOneFileWithASubset is the property compile
// validation is built on: a file can be re-guarded with fewer of its mutants
// while every index in the tree keeps meaning what it meant.
//
// The mutant that is dropped is the catalogue's *first*, which is what makes
// the second half of that sentence testable. The survivors keep the indices
// 1..n-1 that the generated runtime — written once, from the full catalogue,
// and never rewritten — hands out; an implementation that renumbered a subset
// from zero would produce a file that still compiles, still guards the right
// expressions, and activates the wrong mutant every time. The absent `.M[0]`
// and the present `.M[1]` are the difference.
func TestInstrumentFileRewritesOneFileWithASubset(t *testing.T) {
	t.Parallel()

	pristine := readFile(t, filepath.Join("testdata", "nested.input"))
	root := t.TempDir()
	file := filepath.Join(root, sampleFile)
	writeFile(t, file, pristine)

	catalog := catalogOf(t, candidatesIn(t, pristine))
	result := instrumentSnapshot(t, root, catalog)
	runtimeFile := filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go")
	generated := readFile(t, runtimeFile)

	mutants := catalog.Mutants()
	if len(mutants) < 2 {
		t.Fatalf("the fixture catalogues %d mutants, want at least 2", len(mutants))
	}
	dropped, kept := mutants[0], mutants[1:]

	// The file on disk is the fully instrumented one; the rewrite is composed
	// against the pristine bytes the caller kept, and the restore is the same
	// write as the rewrite.
	guards, err := instrument.InstrumentFile(instrument.FileOptions{
		SnapshotRoot:  root,
		RuntimeImport: result.RuntimeImport,
		Path:          sampleFile,
		Source:        pristine,
		Mutants:       kept,
		// The whole run's hints, not the subset's: they are an index by mutant
		// id, and a bisection narrows the mutants rather than the hints.
		Hints: hintsInSource(t, pristine, catalog, hintOptions{}),
	})
	if err != nil {
		t.Fatalf("InstrumentFile: %v", err)
	}
	if guards <= 0 {
		t.Errorf("InstrumentFile reported %d guards, want at least one", guards)
	}

	out := readFile(t, file)
	for _, m := range kept {
		if flag := fmt.Sprintf(".M[%d]", m.Index); !bytes.Contains(out, []byte(flag)) {
			t.Errorf("the subset's mutant %s is not guarded: no %s in\n%s", m.DisplayID, flag, out)
		}
	}
	if flag := fmt.Sprintf(".M[%d]", dropped.Index); bytes.Contains(out, []byte(flag)) {
		t.Errorf("the dropped mutant %s is still guarded: %s survived in\n%s", dropped.DisplayID, flag, out)
	}

	// The invariants a full pass owes are owed by a partial one too.
	if _, parseErr := parser.ParseFile(token.NewFileSet(), sampleFile, out, parser.SkipObjectResolution); parseErr != nil {
		t.Errorf("the rewritten file does not parse: %v\n%s", parseErr, out)
	}
	if got, want := instrument.CountLines(out), instrument.CountLines(pristine); got != want {
		t.Errorf("the rewritten file holds %d line breaks, the pristine file holds %d", got, want)
	}
	if after := readFile(t, runtimeFile); !bytes.Equal(after, generated) {
		t.Errorf("the generated runtime changed under InstrumentFile:\n%s", after)
	}
}

// TestInstrumentFileWithNoMutantsRestoresThePristineFile pins what "every
// candidate in this file was rejected" writes: the file the user wrote, over
// whatever guards were there before.
//
// The result has to be byte-identical rather than merely guard-free. A file
// that gained the runtime import and no guards would not compile — an unused
// import is an error in Go — so an empty subset that still injected one would
// turn a fully rejected file into a broken build that no further bisection
// could explain.
func TestInstrumentFileWithNoMutantsRestoresThePristineFile(t *testing.T) {
	t.Parallel()

	pristine := readFile(t, filepath.Join("testdata", "comparison.input"))
	root := t.TempDir()
	file := filepath.Join(root, sampleFile)
	writeFile(t, file, pristine)

	catalog := catalogOf(t, candidatesIn(t, pristine))
	result := instrumentSnapshot(t, root, catalog)
	if guarded := readFile(t, file); bytes.Equal(guarded, pristine) {
		t.Fatal("the full pass left the file unchanged, so the restore below would prove nothing")
	}

	guards, err := instrument.InstrumentFile(instrument.FileOptions{
		SnapshotRoot:  root,
		RuntimeImport: result.RuntimeImport,
		Path:          sampleFile,
		Source:        pristine,
	})
	if err != nil {
		t.Fatalf("InstrumentFile: %v", err)
	}
	if guards != 0 {
		t.Errorf("InstrumentFile wrote %d guards for an empty subset, want 0", guards)
	}
	if out := readFile(t, file); !bytes.Equal(out, pristine) {
		t.Errorf("an empty subset did not restore the pristine file:\n%s", out)
	}
}

// TestInstrumentFileRefusesBadOptions covers the five ways a caller can point
// this function at something it must not rewrite.
//
// The mutant-from-another-file case is the one worth having. The subsets a
// bisection passes here are slices of a catalogue being split by file and by
// half, and a slice taken from the wrong group would carry spans measured
// against a different file: they would usually miss, and when they did not they
// would splice an edit no mutant identity describes.
func TestInstrumentFileRefusesBadOptions(t *testing.T) {
	t.Parallel()

	src := readFile(t, filepath.Join("testdata", "comparison.input"))
	root := t.TempDir()
	writeFile(t, filepath.Join(root, sampleFile), src)

	elsewhere := catalogOf(t, candidatesIn(t, src)).Mutants()
	for i := range elsewhere {
		elsewhere[i].Path = "other.go"
	}

	cases := []struct {
		name string
		opts instrument.FileOptions
	}{
		{"no snapshot root", instrument.FileOptions{RuntimeImport: "m/rt", Path: sampleFile, Source: src}},
		{"no runtime import", instrument.FileOptions{SnapshotRoot: root, Path: sampleFile, Source: src}},
		{"a path that leaves the snapshot", instrument.FileOptions{
			SnapshotRoot: root, RuntimeImport: "m/rt", Path: "../escape.go", Source: src,
		}},
		{"no pristine source", instrument.FileOptions{
			SnapshotRoot: root, RuntimeImport: "m/rt", Path: sampleFile,
		}},
		{"a mutant from another file", instrument.FileOptions{
			SnapshotRoot: root, RuntimeImport: "m/rt", Path: sampleFile, Source: src, Mutants: elsewhere,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := instrument.InstrumentFile(c.opts); err == nil {
				t.Fatal("InstrumentFile accepted the options, want a refusal")
			} else if got := instrument.CodeOf(err); got != instrument.CodeOptions {
				t.Errorf("InstrumentFile failed with %s, want %s: %v", got, instrument.CodeOptions, err)
			}
		})
	}
}

// TestInstrumentFileRefusesAlreadyInstrumentedBytes proves the safety net under
// the one thing a caller still has to get right.
//
// A caller that hands over the file's current bytes instead of the pristine
// ones is asking for guards nested inside guards, and the spans it would use
// describe bytes that are no longer where they were. Two checks stand in the
// way and either may be the one that speaks first — the site lookup, which asks
// whether the operator a mutant names really starts at that offset, and the
// splicer, which asks whether the bytes a splice claims to replace are the
// bytes actually there — so the assertion is that the refusal is one of them
// rather than which. What matters is that the mistake is a coded refusal naming
// the file rather than a rewrite that quietly means something else.
func TestInstrumentFileRefusesAlreadyInstrumentedBytes(t *testing.T) {
	t.Parallel()

	pristine := readFile(t, filepath.Join("testdata", "comparison.input"))
	root := t.TempDir()
	file := filepath.Join(root, sampleFile)
	writeFile(t, file, pristine)

	catalog := catalogOf(t, candidatesIn(t, pristine))
	result := instrumentSnapshot(t, root, catalog)
	instrumented := readFile(t, file)

	// The instrumented bytes as Source, deliberately.
	_, err := instrument.InstrumentFile(instrument.FileOptions{
		SnapshotRoot:  root,
		RuntimeImport: result.RuntimeImport,
		Path:          sampleFile,
		Source:        instrumented,
		Mutants:       catalog.Mutants(),
		Hints:         hintsInSource(t, pristine, catalog, hintOptions{}),
	})
	if err == nil {
		t.Fatal("InstrumentFile rewrote an already-instrumented file, want a refusal")
	}
	switch got := instrument.CodeOf(err); got {
	case instrument.CodeSiteNotFound, instrument.CodeSpliceMismatch:
	default:
		t.Errorf("InstrumentFile failed with %s, want %s or %s: %v",
			got, instrument.CodeSiteNotFound, instrument.CodeSpliceMismatch, err)
	}
	if out := readFile(t, file); !bytes.Equal(out, instrumented) {
		t.Error("the refused rewrite still changed the file")
	}
}
