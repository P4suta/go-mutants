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
	"github.com/P4suta/go-mutants/internal/testkit"
)

func TestInstrumentFileRewritesOneFileWithASubset(t *testing.T) {
	t.Parallel()

	pristine := testkit.ReadFile(t, filepath.Join("testdata", "nested.input"))
	root := t.TempDir()
	file := filepath.Join(root, sampleFile)
	testkit.WriteFile(t, file, pristine)

	catalog := catalogOf(t, candidatesIn(t, pristine))
	result := instrumentSnapshot(t, root, catalog)
	runtimeFile := filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go")
	generated := testkit.ReadFile(t, runtimeFile)

	mutants := catalog.Mutants()
	if len(mutants) < 2 {
		t.Fatalf("the fixture catalogues %d mutants, want at least 2", len(mutants))
	}
	dropped, kept := mutants[0], mutants[1:]

	guards, err := instrument.InstrumentFile(instrument.FileOptions{
		SnapshotRoot:  root,
		RuntimeImport: result.RuntimeImport,
		Path:          sampleFile,
		Source:        pristine,
		Mutants:       kept,
		Hints:         hintsInSource(t, pristine, catalog, hintOptions{}),
	})
	if err != nil {
		t.Fatalf("InstrumentFile: %v", err)
	}
	if guards <= 0 {
		t.Errorf("InstrumentFile reported %d guards, want at least one", guards)
	}

	out := testkit.ReadFile(t, file)
	for _, m := range kept {
		if flag := fmt.Sprintf(".M[%d]", m.Index); !bytes.Contains(out, []byte(flag)) {
			t.Errorf("the subset's mutant %s is not guarded: no %s in\n%s", m.DisplayID, flag, out)
		}
	}
	if flag := fmt.Sprintf(".M[%d]", dropped.Index); bytes.Contains(out, []byte(flag)) {
		t.Errorf("the dropped mutant %s is still guarded: %s survived in\n%s", dropped.DisplayID, flag, out)
	}

	if _, parseErr := parser.ParseFile(token.NewFileSet(), sampleFile, out, parser.SkipObjectResolution); parseErr != nil {
		t.Errorf("the rewritten file does not parse: %v\n%s", parseErr, out)
	}
	if got, want := instrument.CountLines(out), instrument.CountLines(pristine); got != want {
		t.Errorf("the rewritten file holds %d line breaks, the pristine file holds %d", got, want)
	}
	if after := testkit.ReadFile(t, runtimeFile); !bytes.Equal(after, generated) {
		t.Errorf("the generated runtime changed under InstrumentFile:\n%s", after)
	}
}

func TestInstrumentFileWithNoMutantsRestoresThePristineFile(t *testing.T) {
	t.Parallel()

	pristine := testkit.ReadFile(t, filepath.Join("testdata", "comparison.input"))
	root := t.TempDir()
	file := filepath.Join(root, sampleFile)
	testkit.WriteFile(t, file, pristine)

	catalog := catalogOf(t, candidatesIn(t, pristine))
	result := instrumentSnapshot(t, root, catalog)
	if guarded := testkit.ReadFile(t, file); bytes.Equal(guarded, pristine) {
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
	if out := testkit.ReadFile(t, file); !bytes.Equal(out, pristine) {
		t.Errorf("an empty subset did not restore the pristine file:\n%s", out)
	}
}

func TestInstrumentFileRefusesBadOptions(t *testing.T) {
	t.Parallel()

	src := testkit.ReadFile(t, filepath.Join("testdata", "comparison.input"))
	root := t.TempDir()
	testkit.WriteFile(t, filepath.Join(root, sampleFile), src)

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

func TestInstrumentFileRefusesAlreadyInstrumentedBytes(t *testing.T) {
	t.Parallel()

	pristine := testkit.ReadFile(t, filepath.Join("testdata", "comparison.input"))
	root := t.TempDir()
	file := filepath.Join(root, sampleFile)
	testkit.WriteFile(t, file, pristine)

	catalog := catalogOf(t, candidatesIn(t, pristine))
	result := instrumentSnapshot(t, root, catalog)
	instrumented := testkit.ReadFile(t, file)

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
	if got := instrument.CodeOf(err); got != instrument.CodeSiteNotFound && got != instrument.CodeSpliceMismatch {
		t.Errorf("InstrumentFile failed with %s, want %s or %s: %v",
			got, instrument.CodeSiteNotFound, instrument.CodeSpliceMismatch, err)
	}
	if out := testkit.ReadFile(t, file); !bytes.Equal(out, instrumented) {
		t.Error("the refused rewrite still changed the file")
	}
}
