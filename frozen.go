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

const frozenInputsName = "frozen-inputs"

type frozenInputs struct {
	replacements map[string]string
	resolvedRoot string
	files        map[string]fileState
}

func freezeBuildInputs(
	ctx context.Context, root string, manifest []snapshot.Entry, dir string,
) (frozenInputs, error) {
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

func frozenInputsDetail(manifest []snapshot.Entry) string {
	var bytes int64
	for _, entry := range manifest {
		bytes += entry.Size
	}
	return fmt.Sprintf("%d files, %d bytes", len(manifest), bytes)
}

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
