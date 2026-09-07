// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/P4suta/go-mutants/internal/snapshot"
)

// frozenInputsName is the directory, inside the session's scratch, that holds
// the preparation's own copy of the frozen tree. It sits beside the overlay
// manifest that points at it and is removed with the rest of the scratch by
// [Session.Close] — or, for a preparation that fails, by the failure path,
// which is the only other thing that knows the copy exists.
const frozenInputsName = "frozen-inputs"

// frozenInputs is the preparation's copy of every file the snapshot froze,
// together with the three things the rest of a preparation needs from it: the
// overlay mapping that puts the copy in front of the tree, the resolved
// spelling of the tree's root, and the baseline [Session.Changes] measures
// against.
//
// It exists because the test binaries are compiled *after* the instrumentation
// window, while a command may write the tree. The overlay used to name only the
// instrumented sources, so the compiler read every other file where it lay: a
// command's bytes were compiled in, and the re-digest that used to follow the
// build could only see a write that was still there when the build ended. A
// write made and undone while the compiler was between two files was compiled
// in and gone before anything looked.
//
// Naming every frozen file in the overlay closes that for every path the
// manifest holds. What it does not close is a path the manifest does not hold:
// `-overlay` replaces the files it names and the go command still lists the
// real directory, so a file a command *adds* to a package directory during the
// build is still compiled in. [Session.Changes] reports it as an addition, and
// it is the residual stated in
// docs/adr/0007-commands-overlap-preparation.md.
type frozenInputs struct {
	// replacements maps each frozen file's path in the tree to the copy, in
	// the shape a `go` overlay manifest's `Replace` holds: absolute paths on
	// both sides. A file is named twice when [frozenInputs.resolvedRoot]
	// differs from the root it was frozen from — see [freezeBuildInputs].
	replacements map[string]string
	// resolvedRoot is the tree's root with every symbolic link resolved, which
	// is the spelling the go command looks a path up under. It is derived once
	// and handed to [writeInstrumentationOverlay] so that both halves of the
	// overlay are keyed identically; see [freezeBuildInputs] for why that
	// matters more than it looks.
	resolvedRoot string
	// files is what the manifest says the tree held, in the shape [scanFiles]
	// returns, so that [Session.Changes] compares against the bytes the
	// binaries were built from rather than against a scan taken later.
	files map[string]fileState
}

// freezeBuildInputs copies every file the manifest names out of the tree and
// into dir, and returns the overlay mapping and the baseline for them.
//
// The file set is the manifest, whole, and that is the decision rather than an
// accident. A build reads Go sources, `go.mod`, `go.sum`, assembly, the cgo
// inputs, and whatever a `//go:embed` names — which can be any path in the
// module, a `testdata/` fixture or a `README.md` included — so a set built from
// a list of extensions would have to parse every source in the module to be
// sure, and every miss in it is a file the compiler reads off the disk with
// nothing saying so. The manifest is already exactly the frozen tree, so
// copying all of it is total by construction and costs one tree copy: the
// snapshot's own footprint again, held for as long as the session.
//
// # Where it runs, and why it is not a phase
//
// It is called at the top of the instrumentation window, with the tree held
// exclusively and the integrity gate two statements behind it, so the tree it
// reads has just been proved byte-identical to the manifest. The placement is
// deliberate and the alternative is worse: taken *before* the window, under the
// shared lock, a command's transient write would land in the middle of the copy
// and the digest comparison below would fail a preparation for a write the gate
// is designed to tolerate. Inside the window nothing else can write, so a file
// that does not match is this engine's defect rather than a command's — which
// is why a mismatch is an ordinary error and not a [DriftError]. A caller told
// its tree had drifted would go looking for a write nobody made.
//
// It is deliberately **not** a [PreparePhase]. That vocabulary is shared with
// goatest, whose phase enumeration is closed, so adding a member is a
// consumer-side change rather than an addition here. The copy is recorded as a
// trace `stage` instead, which is the open vocabulary for exactly this: a step
// inside a phase that a reader of a slow preparation needs to see.
//
// The context is checked between files. The copy is proportional to the tree
// and runs with the tree held exclusively, so a caller that has given up has to
// be able to stop it rather than wait out a module's worth of reads.
func freezeBuildInputs(
	ctx context.Context, root string, manifest []snapshot.Entry, dir string,
) (frozenInputs, error) {
	// The go command resolves the package directory it is given through the
	// file system before it looks a path up in the overlay, so on a platform
	// whose temporary directory is reached through a symbolic link — macOS
	// reaches /var/folders through /private/var — the key written from the
	// snapshot root is not the key looked up: a build is started with a working
	// directory rather than an inherited PWD, and cmd/go's own getwd then
	// answers with the resolved path. Both spellings name the same file, so
	// both map to the same copy.
	//
	// The failure is returned rather than shrugged off, and that is the point
	// of resolving here rather than per file. A swallowed error would leave
	// every entry keyed under a spelling cmd/go never asks about: the overlay
	// would match nothing, the build would quietly go back to reading the tree,
	// and no error, no note and no test would say so. The root was walked and
	// re-digested moments ago, so one that cannot be resolved now is a defect,
	// in the same bucket as a digest that does not match.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return frozenInputs{}, fmt.Errorf("resolve the frozen tree root %s: %w", root, err)
	}
	if mkdirErr := os.MkdirAll(snapshot.ExtendedPath(dir), privateDirectoryMode); mkdirErr != nil {
		return frozenInputs{}, mkdirErr
	}
	frozen := frozenInputs{
		replacements: make(map[string]string, len(manifest)),
		resolvedRoot: resolvedRoot,
		files:        make(map[string]fileState, len(manifest)),
	}
	made := make(map[string]bool, len(manifest))
	for _, entry := range manifest {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return frozenInputs{}, ctxErr
		}
		relative := filepath.FromSlash(entry.RelPath)
		if !filepath.IsLocal(relative) {
			return frozenInputs{}, fmt.Errorf("frozen path %q is not local", entry.RelPath)
		}
		// The overlay is JSON and encoding/json rewrites invalid UTF-8 in a map
		// key as U+FFFD, so a name that is not valid UTF-8 — legal on every
		// Unix file system — would be copied and then mapped under a key no go
		// command ever looks up. Refused here rather than written and ignored.
		if !utf8.ValidString(entry.RelPath) {
			return frozenInputs{}, fmt.Errorf(
				"frozen path %q is not valid UTF-8, so no overlay key can name it", entry.RelPath)
		}
		source := filepath.Join(root, relative)
		target := filepath.Join(dir, relative)
		if dirErr := makeFrozenDir(dir, filepath.Dir(target), made); dirErr != nil {
			return frozenInputs{}, fmt.Errorf("freeze %s: %w", entry.RelPath, dirErr)
		}
		mode, copyErr := copyFrozenFile(source, target, entry)
		if copyErr != nil {
			return frozenInputs{}, copyErr
		}
		frozen.replacements[source] = target
		if resolvedRoot != root {
			frozen.replacements[filepath.Join(resolvedRoot, relative)] = target
		}
		frozen.files[entry.RelPath] = fileState{digest: entry.SHA256, mode: mode}
	}
	return frozen, nil
}

// frozenInputsDetail is what the recorded stage says it was about, so that a
// reader of a slow preparation sees the size of the copy and not only how long
// it took.
func frozenInputsDetail(manifest []snapshot.Entry) string {
	var bytes int64
	for _, entry := range manifest {
		bytes += entry.Size
	}
	return fmt.Sprintf("%d files, %d bytes", len(manifest), bytes)
}

// makeFrozenDir creates dir and every ancestor of it below root, one os.Mkdir
// at a time and each through [snapshot.ExtendedPath].
//
// os.MkdirAll is not used, and the reason is Windows. The extended-length form
// is what keeps a copy some thirty characters deeper than the snapshot inside
// MAX_PATH, and MkdirAll reaches a parent by scanning the string backwards —
// behaviour that is delicate around the `\\?\` prefix and has moved between
// releases. Creating the ancestors explicitly, which is what internal/snapshot
// does for the same reason, is the same work with none of that.
func makeFrozenDir(root, dir string, made map[string]bool) error {
	if dir == root || made[dir] {
		return nil
	}
	if parent := filepath.Dir(dir); parent != dir {
		if err := makeFrozenDir(root, parent, made); err != nil {
			return err
		}
	}
	if err := os.Mkdir(snapshot.ExtendedPath(dir), privateDirectoryMode); err != nil &&
		!errors.Is(err, fs.ErrExist) {
		return err
	}
	made[dir] = true
	return nil
}

// copyFrozenFile copies one manifest entry and refuses a copy that is not the
// bytes the manifest froze, returning the mode [scanFiles] would have recorded.
//
// The digest is taken from the bytes as they are written rather than from the
// file afterwards, so what is checked is what landed in the copy and not a
// second read of a file something could have moved in between.
func copyFrozenFile(source, target string, entry snapshot.Entry) (fs.FileMode, error) {
	in, err := os.Open(snapshot.ExtendedPath(source))
	if err != nil {
		return 0, fmt.Errorf("freeze %s: %w", entry.RelPath, err)
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return 0, fmt.Errorf("freeze %s: %w", entry.RelPath, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("freeze %s: the manifest froze a regular file and the tree holds %s",
			entry.RelPath, info.Mode())
	}
	out, err := os.OpenFile(
		snapshot.ExtendedPath(target), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return 0, fmt.Errorf("freeze %s: %w", entry.RelPath, err)
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(out, hash), in)
	closeErr := out.Close()
	if copyErr != nil {
		return 0, fmt.Errorf("freeze %s: %w", entry.RelPath, copyErr)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("freeze %s: %w", entry.RelPath, closeErr)
	}
	if digest := hex.EncodeToString(hash.Sum(nil)); digest != entry.SHA256 || size != entry.Size {
		return 0, fmt.Errorf(
			"freeze %s: the tree holds %s (%d bytes) and the manifest froze %s (%d bytes)",
			entry.RelPath, digest, size, entry.SHA256, entry.Size)
	}
	return info.Mode().Type() | info.Mode().Perm(), nil
}
