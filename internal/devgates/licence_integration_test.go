// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package devgates

import (
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pelletier/go-toml/v2"

	"github.com/P4suta/go-mutants/internal/testkit"
)

func TestTheLicenceGateReadsWhatADecoderReads(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	read := testkit.ReusePaths(t, root)
	if len(read) == 0 {
		t.Fatal("the line reader found no annotated path, and REUSE.toml annotates several")
	}

	decoded := decodedReusePaths(t, root)
	if diff := cmp.Diff(decoded, read); diff != "" {
		t.Errorf("the licence gate and a TOML decoder disagree about %s (-decoded +read):\n%s",
			testkit.ReuseFile, diff)
	}
}

func TestTheDecoderSeesAPathTheLineReaderWouldMiss(t *testing.T) {
	t.Parallel()

	const folded = "version = 1\n\n[[annotations]]\npath = [\"one.bin\", \"two.bin\"]\n" +
		"precedence = \"aggregate\"\nSPDX-FileCopyrightText = \"nobody\"\nSPDX-License-Identifier = \"MIT\"\n"

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, testkit.ReuseFile), folded)
	if got := decodedReusePaths(t, dir); len(got) != 2 || got[0] != "one.bin" || got[1] != "two.bin" {
		t.Errorf("the decoder read %q from a folded path array", got)
	}
}

func decodedReusePaths(t testing.TB, root string) []string {
	t.Helper()
	text := readFile(t, filepath.Join(root, testkit.ReuseFile))
	var manifest struct {
		Annotations []struct {
			Path []string `toml:"path"`
		} `toml:"annotations"`
	}
	if err := toml.Unmarshal([]byte(text), &manifest); err != nil {
		t.Fatalf("decoding %s: %v", testkit.ReuseFile, err)
	}
	var paths []string
	for _, a := range manifest.Annotations {
		paths = append(paths, a.Path...)
	}
	return paths
}
