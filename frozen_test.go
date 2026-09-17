// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
)

var frozenTreeSources = map[string]string{
	"go.mod":              "module fixture.example/frozen\n\ngo 1.26\n",
	"go.sum":              "",
	"a.go":                "package frozen\n\nfunc A() int { return 1 }\n",
	"a_test.go":           "package frozen\n",
	"pkg/b.go":            "package pkg\n\nfunc B() int { return 2 }\n",
	"asm_amd64.s":         "// nothing\n",
	"README.md":           "# frozen\n",
	"testdata/golden.txt": "golden\n",
}

func TestFreezeBuildInputsCopiesEveryFileTheManifestHolds(t *testing.T) {
	t.Parallel()

	root := writeFrozenTree(t, frozenTreeSources)
	dir := filepath.Join(t.TempDir(), "frozen")

	frozen, err := freezeBuildInputs(t.Context(), root, manifestOf(frozenTreeSources), dir)
	if err != nil {
		t.Fatalf("freezeBuildInputs = %v, want the copies", err)
	}

	for _, relative := range slices.Sorted(maps.Keys(frozenTreeSources)) {
		source := filepath.Join(root, filepath.FromSlash(relative))
		backing, mapped := frozen.replacements[source]
		if !mapped {
			t.Errorf("the overlay does not name %s, so a build would read it off the disk", relative)
			continue
		}
		if !strings.HasPrefix(backing, dir+string(filepath.Separator)) {
			t.Errorf("%s is backed by %s, want a copy under %s", relative, backing, dir)
		}
		data, readErr := os.ReadFile(backing)
		if readErr != nil {
			t.Errorf("reading the copy of %s: %v", relative, readErr)
			continue
		}
		if got, want := mutation.DigestString(string(data)),
			mutation.DigestString(frozenTreeSources[relative]); got != want {
			t.Errorf("the copy of %s digests as %s, want the manifest's %s", relative, got, want)
		}
		state, recorded := frozen.files[relative]
		if !recorded {
			t.Errorf("%s is not in the prepared baseline, so a write to it would go unreported", relative)
			continue
		}
		if state.digest != mutation.DigestString(frozenTreeSources[relative]) {
			t.Errorf("the baseline digest of %s is %s, want the manifest's", relative, state.digest)
		}
		info, statErr := os.Stat(source)
		if statErr != nil {
			t.Fatalf("stat %s: %v", relative, statErr)
		}
		if want := info.Mode().Type() | info.Mode().Perm(); state.mode != want {
			t.Errorf("the baseline mode of %s is %v, want %v", relative, state.mode, want)
		}
	}
	if len(frozen.files) != len(frozenTreeSources) {
		t.Errorf("the baseline holds %d files, want the manifest's %d",
			len(frozen.files), len(frozenTreeSources))
	}
}

func TestFreezeBuildInputsRefusesAFileThatIsNotTheManifests(t *testing.T) {
	t.Parallel()

	root := writeFrozenTree(t, frozenTreeSources)
	if err := os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package frozen\n\nfunc A() int { return 99 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := freezeBuildInputs(
		t.Context(), root, manifestOf(frozenTreeSources), filepath.Join(t.TempDir(), "frozen"))
	if err == nil {
		t.Fatal("freezeBuildInputs over a tree that is not the manifest = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "a.go") {
		t.Errorf("the refusal does not name the file: %v", err)
	}
	var drift *DriftError
	if errors.As(err, &drift) {
		t.Errorf("freezeBuildInputs = %T, want an ordinary error: the gate has already proved the"+
			" tree matches, so a mismatch here is this engine's bug and not the caller's write", drift)
	}
}

func TestFreezeBuildInputsRefusesAFileTheTreeNoLongerHolds(t *testing.T) {
	t.Parallel()

	root := writeFrozenTree(t, frozenTreeSources)
	if err := os.Remove(filepath.Join(root, "pkg", "b.go")); err != nil {
		t.Fatal(err)
	}

	_, err := freezeBuildInputs(
		t.Context(), root, manifestOf(frozenTreeSources), filepath.Join(t.TempDir(), "frozen"))
	if err == nil {
		t.Fatal("freezeBuildInputs over a tree missing a manifest file = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "b.go") {
		t.Errorf("the refusal does not name the file: %v", err)
	}
}

func TestTheInstrumentedSourceWinsOverTheFrozenCopy(t *testing.T) {
	t.Parallel()

	root := writeFrozenTree(t, frozenTreeSources)
	scratch := t.TempDir()
	frozen, err := freezeBuildInputs(
		t.Context(), root, manifestOf(frozenTreeSources), filepath.Join(scratch, "frozen"))
	if err != nil {
		t.Fatalf("freezeBuildInputs = %v", err)
	}

	instrumented := "package frozen\n\nfunc A() int { return 1 /* instrumented */ }\n"
	if writeErr := os.WriteFile(filepath.Join(root, "a.go"), []byte(instrumented), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	if mkdirErr := os.MkdirAll(filepath.Join(root, "gomutantsruntime"), 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if writeErr := os.WriteFile(filepath.Join(root, "gomutantsruntime", "runtime.go"),
		[]byte("package gomutantsruntime\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	manifestPath, err := writeInstrumentationOverlay(
		root, frozen.resolvedRoot, scratch, mainOverlayName, instrument.Result{
			RuntimeDir:        "gomutantsruntime",
			FilesInstrumented: []string{"a.go"},
		}, frozen.replacements)
	if err != nil {
		t.Fatalf("writeInstrumentationOverlay = %v", err)
	}

	replace := readOverlayReplace(t, manifestPath)
	backing := replace[filepath.Join(root, "a.go")]
	if backing == "" {
		t.Fatal("the overlay does not name the instrumented a.go")
	}
	data, err := os.ReadFile(backing)
	if err != nil {
		t.Fatalf("reading the backing file for a.go: %v", err)
	}
	if string(data) != instrumented {
		t.Errorf("a.go is backed by the frozen copy, want the instrumented source:\n%s", data)
	}
	untouched := replace[filepath.Join(root, "pkg", "b.go")]
	if untouched != frozen.replacements[filepath.Join(root, "pkg", "b.go")] {
		t.Errorf("pkg/b.go is backed by %s, want the frozen copy %s",
			untouched, frozen.replacements[filepath.Join(root, "pkg", "b.go")])
	}
	if replace[filepath.Join(root, "gomutantsruntime", "runtime.go")] == "" {
		t.Error("the overlay does not name the generated runtime, so the mutated program has no switch")
	}
}

func writeFrozenTree(t *testing.T, sources map[string]string) string {
	t.Helper()
	return writeFrozenTreeAt(t, t.TempDir(), sources)
}

func writeFrozenTreeAt(t *testing.T, root string, sources map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, relative := range slices.Sorted(maps.Keys(sources)) {
		name := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(sources[relative]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func readOverlayReplace(t *testing.T, manifestPath string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading the overlay manifest: %v", err)
	}
	var overlay struct {
		Replace map[string]string `json:"Replace"`
	}
	if err := json.Unmarshal(data, &overlay); err != nil {
		t.Fatalf("parsing the overlay manifest: %v", err)
	}
	return overlay.Replace
}

func TestFreezeBuildInputsRefusesARootItCannotResolve(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "no-such-tree")
	if _, err := freezeBuildInputs(t.Context(), missing, nil, filepath.Join(t.TempDir(), "frozen")); err == nil {
		t.Error("freezeBuildInputs over a root that cannot be resolved = nil, want a refusal")
	}
	_, err := freezeBuildInputs(
		t.Context(), missing, manifestOf(frozenTreeSources), filepath.Join(t.TempDir(), "frozen"))
	if err == nil {
		t.Fatal("freezeBuildInputs over a root that cannot be resolved = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the refusal does not name the root it could not resolve: %v", err)
	}
}

func TestFreezeBuildInputsNamesBothSpellingsOfASymlinkedRoot(t *testing.T) {
	t.Parallel()

	root := symlinkedFrozenRoot(t, frozenTreeSources)
	frozen, err := freezeBuildInputs(
		t.Context(), root, manifestOf(frozenTreeSources), filepath.Join(t.TempDir(), "frozen"))
	if err != nil {
		t.Fatalf("freezeBuildInputs = %v", err)
	}
	if frozen.resolvedRoot == root {
		t.Fatalf("resolvedRoot = %s, want the spelling behind the symbolic link", frozen.resolvedRoot)
	}
	for relative := range frozenTreeSources {
		asWritten := frozen.replacements[filepath.Join(root, filepath.FromSlash(relative))]
		asResolved := frozen.replacements[filepath.Join(frozen.resolvedRoot, filepath.FromSlash(relative))]
		if asWritten == "" || asResolved == "" {
			t.Errorf("%s is mapped under %q and %q, want both spellings", relative, asWritten, asResolved)
			continue
		}
		if asWritten != asResolved {
			t.Errorf("%s is backed by %s under the root and by %s under the resolved root",
				relative, asWritten, asResolved)
		}
	}
	if len(frozen.files) != len(frozenTreeSources) {
		t.Errorf("the baseline holds %d files, want the manifest's %d",
			len(frozen.files), len(frozenTreeSources))
	}
}

func TestTheInstrumentedSourceWinsUnderBothSpellings(t *testing.T) {
	t.Parallel()

	root := symlinkedFrozenRoot(t, frozenTreeSources)
	scratch := t.TempDir()
	frozen, err := freezeBuildInputs(
		t.Context(), root, manifestOf(frozenTreeSources), filepath.Join(scratch, "frozen"))
	if err != nil {
		t.Fatalf("freezeBuildInputs = %v", err)
	}

	instrumented := "package frozen\n\nfunc A() int { return 1 /* instrumented */ }\n"
	if writeErr := os.WriteFile(filepath.Join(root, "a.go"), []byte(instrumented), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	if mkdirErr := os.MkdirAll(filepath.Join(root, "gomutantsruntime"), 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if writeErr := os.WriteFile(filepath.Join(root, "gomutantsruntime", "runtime.go"),
		[]byte("package gomutantsruntime\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	manifestPath, err := writeInstrumentationOverlay(
		root, frozen.resolvedRoot, scratch, mainOverlayName, instrument.Result{
			RuntimeDir:        "gomutantsruntime",
			FilesInstrumented: []string{"a.go"},
		}, frozen.replacements)
	if err != nil {
		t.Fatalf("writeInstrumentationOverlay = %v", err)
	}

	replace := readOverlayReplace(t, manifestPath)
	for _, spelling := range []string{root, frozen.resolvedRoot} {
		backing := replace[filepath.Join(spelling, "a.go")]
		if backing == "" {
			t.Errorf("the overlay does not name a.go under %s", spelling)
			continue
		}
		data, readErr := os.ReadFile(backing)
		if readErr != nil {
			t.Errorf("reading the backing file for a.go under %s: %v", spelling, readErr)
			continue
		}
		if string(data) != instrumented {
			t.Errorf("a.go under %s is backed by the frozen copy, want the instrumented source:\n%s",
				spelling, data)
		}
		if replace[filepath.Join(spelling, "gomutantsruntime", "runtime.go")] == "" {
			t.Errorf("the overlay does not name the generated runtime under %s", spelling)
		}
	}
}

func TestFreezeBuildInputsStopsWhenTheContextIsCancelled(t *testing.T) {
	t.Parallel()

	root := writeFrozenTree(t, frozenTreeSources)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := freezeBuildInputs(
		ctx, root, manifestOf(frozenTreeSources), filepath.Join(t.TempDir(), "frozen"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("freezeBuildInputs with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestFreezeBuildInputsRefusesAPathThatIsNotUTF8(t *testing.T) {
	t.Parallel()

	const source = "package frozen\n"
	name := "bad\xff.go"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o644); err != nil {
		t.Skipf("this platform will not hold a file name that is not UTF-8: %v", err)
	}

	_, err := freezeBuildInputs(t.Context(), root,
		manifestOf(map[string]string{name: source}), filepath.Join(t.TempDir(), "frozen"))
	if err == nil {
		t.Fatal("freezeBuildInputs over a path that is not UTF-8 = nil, want a refusal: the overlay" +
			" key would be rewritten by encoding/json and match nothing")
	}
}

func symlinkedFrozenRoot(t *testing.T, sources map[string]string) string {
	t.Helper()
	base := t.TempDir()
	real, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatalf("resolving %s: %v", base, err)
	}
	tree := writeFrozenTreeAt(t, filepath.Join(real, "tree"), sources)
	link := filepath.Join(real, "link")
	if err := os.Symlink(tree, link); err != nil {
		t.Skipf("this platform will not make a symbolic link: %v", err)
	}
	return link
}
