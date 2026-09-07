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

// frozenTreeSources is a module with the shape the file set has to be decided
// against: two packages, only one of which any include pattern would select; a
// test file, which is never instrumented; the module files; a document no build
// reads; and a testdata fixture no `//go:embed` names.
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

// TestFreezeBuildInputsCopiesEveryFileTheManifestHolds is the file set, and the
// decision it pins is deliberately the wide one.
//
// The alternative was a list of the extensions a build reads — `.go`, `.s`,
// `.c`, `.h`, `go.mod`, `go.sum` — plus whatever a `//go:embed` names, and
// every miss in a list like that is a file the compiler reads off the disk with
// nothing saying so. A `//go:embed` can name any path in the module, including
// a `testdata/` fixture and a `README.md`, so the list would have to parse
// every source to be sure. The manifest is already exactly the frozen tree, so
// copying all of it is total by construction and costs one tree copy: the
// go command cannot read a byte of the tree during a build, whatever a
// directive in some package says.
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

// TestFreezeBuildInputsRefusesAFileThatIsNotTheManifests is the assertion that
// the copy is of the *frozen* program and not merely of what is lying there.
//
// The tree was proved byte-identical to the manifest by the integrity gate two
// statements earlier, under a lock nothing else holds, so a file that does not
// match here is a defect in this engine and not a command's write. It is
// therefore an ordinary error rather than a [DriftError]: a caller told its own
// tree had drifted would go looking for a write that never happened.
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

// TestFreezeBuildInputsRefusesAFileTheTreeNoLongerHolds is the same claim from
// the other side, and it is the one that would otherwise be a panic: a manifest
// entry with no file behind it.
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

// TestTheInstrumentedSourceWinsOverTheFrozenCopy is the one ordering rule the
// two halves of the overlay have between them.
//
// Both name the same tree path for an instrumented file — the frozen copy
// because the manifest holds it, the instrumentation because it rewrote it —
// and the mutated program is the whole point of the session. So the
// instrumented mapping is written last and wins, and every file it does not
// name keeps the frozen copy.
func TestTheInstrumentedSourceWinsOverTheFrozenCopy(t *testing.T) {
	t.Parallel()

	root := writeFrozenTree(t, frozenTreeSources)
	scratch := t.TempDir()
	frozen, err := freezeBuildInputs(
		t.Context(), root, manifestOf(frozenTreeSources), filepath.Join(scratch, "frozen"))
	if err != nil {
		t.Fatalf("freezeBuildInputs = %v", err)
	}

	// What validation leaves behind: one rewritten source and a generated
	// runtime package, both in the tree.
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
	// And the file instrumentation did not touch still comes from the copy.
	untouched := replace[filepath.Join(root, "pkg", "b.go")]
	if untouched != frozen.replacements[filepath.Join(root, "pkg", "b.go")] {
		t.Errorf("pkg/b.go is backed by %s, want the frozen copy %s",
			untouched, frozen.replacements[filepath.Join(root, "pkg", "b.go")])
	}
	// The generated runtime is in neither the manifest nor the tree's frozen
	// state, and it still has to reach the compiler.
	if replace[filepath.Join(root, "gomutantsruntime", "runtime.go")] == "" {
		t.Error("the overlay does not name the generated runtime, so the mutated program has no switch")
	}
}

// writeFrozenTree lays the given module-relative sources out on disk.
func writeFrozenTree(t *testing.T, sources map[string]string) string {
	t.Helper()
	return writeFrozenTreeAt(t, t.TempDir(), sources)
}

// writeFrozenTreeAt lays the sources out under the given directory, creating it.
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

// readOverlayReplace is the `Replace` map of a written overlay manifest.
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

// TestFreezeBuildInputsRefusesARootItCannotResolve is the failure that would
// otherwise be silent, and silent in the worst possible way.
//
// The overlay is keyed by the path the `go` command looks a file up under, and
// on a platform whose temporary directory is reached through a symbolic link
// that is the *resolved* spelling: cmd/go's child is started with `Dir` set and
// no matching `PWD`, so its `os.Getwd` falls through to the system call and
// returns the resolved path. If resolving the root fails and the failure is
// swallowed, every entry is written under a spelling cmd/go never asks about —
// the overlay matches nothing, the build quietly reads the tree again, and no
// error, no note and no failing test says so.
//
// So an unresolvable root is refused. It is the same bucket as a digest that
// does not match: the tree was walked and re-digested moments earlier, so a
// root that cannot be resolved now is this engine's defect.
func TestFreezeBuildInputsRefusesARootItCannotResolve(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "no-such-tree")
	// An empty manifest is the shape that made the old behaviour invisible:
	// with nothing to copy, a swallowed resolution failure returned no error at
	// all.
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

// TestFreezeBuildInputsNamesBothSpellingsOfASymlinkedRoot pins the two keys.
//
// The go command resolves the package directory through the file system before
// it looks a path up in the overlay, so on macOS — where a temporary directory
// under `/var/folders` is reached through `/private/var/folders` — the key
// written from the snapshot root is not the key looked up. Both spellings name
// the same file and both therefore map to the same copy, and the resolved one
// is derived once for the whole tree rather than once per file, so the two
// halves of the overlay cannot drift apart.
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
	// The baseline is keyed by the module-relative path, so it has one entry
	// per file however many spellings the overlay needs.
	if len(frozen.files) != len(frozenTreeSources) {
		t.Errorf("the baseline holds %d files, want the manifest's %d",
			len(frozen.files), len(frozenTreeSources))
	}
}

// TestTheInstrumentedSourceWinsUnderBothSpellings is [S2]'s other half, and it
// is the failure that would be worst of all: not a build that reads the tree,
// but a session that compiles the *un-mutated* program and reports every mutant
// as a survivor.
//
// It would happen if the two halves of the overlay derived the resolved
// spelling by different routes and the routes ever disagreed — the frozen copy
// landing on the key cmd/go reads and the instrumented copy on one it does not.
// They cannot, because there is one resolved root and both halves are spelled
// from it, and this is the test that says so.
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

// TestFreezeBuildInputsStopsWhenTheContextIsCancelled is the bound on the one
// piece of work in the window that has none of its own.
//
// The copy is proportional to the tree and runs with the tree held
// exclusively, so a caller that has given up has to be able to stop it — and a
// `Prepare` whose context is cancelled must not go on reading a module's worth
// of files before it notices.
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

// TestFreezeBuildInputsRefusesAPathThatIsNotUTF8 closes an entry that would
// otherwise be written and never matched.
//
// The overlay is JSON and `encoding/json` replaces invalid UTF-8 in a map key
// with U+FFFD, so a file whose name is not valid UTF-8 — legal on every Unix
// file system — would be copied, mapped under a key no `go` command ever looks
// up, and read off the disk with nothing saying so. It is refused instead, for
// the same reason a non-local path is.
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

// symlinkedFrozenRoot lays the sources out and returns a path to them through a
// symbolic link, which is the shape macOS hands this package for free.
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
