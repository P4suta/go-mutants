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

// TestTheLicenceGateReadsWhatADecoderReads is the strict half of a gate whose
// cheap half runs on every push.
//
// [testkit.ReusePaths] reads the lines of REUSE.toml because the harness may
// not link a TOML decoder -- every test binary in this repository would compile
// it -- and the licence gate has to stay in the unit tier, because a licensing
// manifest checked only where the toolchain runs is one that drifts between the
// pushes that do not check it. Both of those are right, and together they leave
// a gate that depends on how the file is formatted rather than on what it says.
//
// Nothing links this package, so this one may decode. It does not replace the
// cheap reader; it holds the cheap reader to the decoder's answer, which is the
// pattern this repository takes wherever a first line of defence is worth
// keeping and is not the whole of the question.
//
// The failure it exists for is silent. A formatter is free to fold a `path`
// array onto one line or to break it across several, and a line reader that
// then returned fewer patterns would excuse fewer files -- which the licence
// gate reports loudly -- or, if the folding went the other way and a stray
// quoted string on a `path` line were picked up, would excuse one more, which
// nothing else in the repository would ever mention.
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

// TestTheDecoderSeesAPathTheLineReaderWouldMiss is the counterpart, and the
// shape it feeds is the exact one that broke a reader in this repository.
//
// A comparison of two readers passes trivially when both are wrong the same
// way, so the claim worth pinning is that the decoder is the stricter of the
// two: given a manifest whose `path` array is folded onto one line, it still
// reads every entry. If this ever stops being true, the test above becomes two
// line readers agreeing with each other.
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

// decodedReusePaths is every annotated path, read as TOML rather than as text.
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
