// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// loopShapes is one file holding every shape a counted loop has to survive,
// and one it has to refuse.
//
// Each function is here for a reason the rewrite could get wrong. `counted` is
// the plain three-clause loop. `labelled` is the one that says where the
// declaration goes: written between the label and the `for` it would label the
// declaration instead, and `break outer` would stop naming a loop. `ranged`,
// `bare` and `empty` are the other three spellings a `for` has. `jumping` is
// the refusal: Go forbids a jump over a declaration into its scope, so a
// function holding a `goto` keeps its loops uncounted rather than becoming a
// file that will not compile.
const loopShapes = `package sample

func counted(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i
	}
	return total
}

func labelled(rows [][]int) int {
	total := 0
outer:
	for _, row := range rows {
		for _, v := range row {
			if v < 0 {
				continue outer
			}
			if v > 9 {
				break outer
			}
			total += v
		}
	}
	return total
}

func bare(n int) int {
	i := 0
	for {
		if i > n {
			return i
		}
		i++
	}
}

func empty(n int) int {
	i := 0
	for ; i < n; i++ {
	}
	return i
}

func jumping(n int) int {
	total := 0
	if n < 0 {
		goto done
	}
	for i := 0; i < n; i++ {
		total += i
	}
done:
	return total
}
`

// TestEveryLoopOfAnInstrumentedFileCarriesACounter is the rewrite ADR 0013
// rests on.
//
// A mutant that turns a terminating loop into a spinning one is told apart from
// a mutant that is merely slow by the work it does, not by a stopwatch, and the
// work is counted where it happens: two locals in front of the loop and one
// test at the top of its body. Locals, so that there is nothing shared between
// goroutines to synchronise and nothing for the race detector to find, and so
// that what is counted is one *entry* to the loop rather than every entry there
// has ever been.
//
// The assertions are the three things the rewrite can get wrong and the one it
// has to refuse: a counter per loop, a label that still names its loop, a file
// that still parses on the same lines, and no counter at all inside a function
// a `goto` can jump through.
func TestEveryLoopOfAnInstrumentedFileCarriesACounter(t *testing.T) {
	t.Parallel()

	src := []byte(loopShapes)
	root := t.TempDir()
	testkit.WriteFile(t, filepath.Join(root, sampleFile), src)

	catalog := catalogOf(t, candidatesIn(t, src))
	instrumentSnapshot(t, root, catalog)
	out := testkit.ReadFile(t, filepath.Join(root, sampleFile))

	assertWellFormed(t, src, out, catalog)

	text := string(out)
	declarations := regexp.MustCompile(`__gm_n(\d+), __gm_k(\d+) :=`).FindAllStringSubmatch(text, -1)
	// Five loops outside `jumping`: the three-clause one, the range, the one
	// inside it, the bare one and the empty one.
	if len(declarations) != 5 {
		t.Errorf("the rewrite declared %d loop counters, want 5:\n%s", len(declarations), text)
	}
	for _, d := range declarations {
		if d[1] != d[2] {
			t.Errorf("a loop declared counter %s beside limit %s; the two name one site", d[1], d[2])
		}
		if want := "__gm_n" + d[1] + " > __gm_k" + d[1]; !strings.Contains(text, want) {
			t.Errorf("counter %s is declared and never tested; %q is not in the file", d[1], want)
		}
	}

	// The label still names the loop rather than the declaration in front of
	// it, which is what `break outer` two lines down depends on.
	if !regexp.MustCompile(`:= uint64\(0\), __gm\d*\.Limit\[\d+\]; outer:`).MatchString(text) {
		t.Errorf("the labelled loop's declaration did not land in front of its label:\n%s", text)
	}

	// And the function a goto can jump through keeps the loop it had.
	jump := text[strings.Index(text, "func jumping"):]
	if strings.Contains(jump, "__gm_n") {
		t.Errorf("a loop inside a function holding a goto was counted:\n%s", jump)
	}
}

// TestALoopCounterIsAllocatedOncePerTreeAndNeverReused pins the index space the
// limit table is read with.
//
// The table is one array in one generated package, so a site's index has to be
// a fact about the tree rather than about the file it happens to be in: two
// files each numbering their loops from zero would give one limit to two loops
// and none to a third.
func TestALoopCounterIsAllocatedOncePerTreeAndNeverReused(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first := []byte("package sample\n\nfunc a(n int) int {\n\ttotal := 0\n\tfor i := 0; i < n; i++ {\n\t\ttotal += i\n\t}\n\treturn total\n}\n")
	second := []byte("package sample\n\nfunc b(n int) int {\n\ttotal := 0\n\tfor i := 0; i < n; i++ {\n\t\ttotal -= i\n\t}\n\treturn total\n}\n")
	testkit.WriteFile(t, filepath.Join(root, "a.go"), first)
	testkit.WriteFile(t, filepath.Join(root, "b.go"), second)

	catalog := catalogOf(t, append(
		candidatesInFile(t, "a.go", first),
		candidatesInFile(t, "b.go", second)...))
	instrumentSnapshotHinted(t, root, catalog, hintsFor(t, root, catalog, hintOptions{}))

	sites := map[string]bool{}
	for _, name := range []string{"a.go", "b.go"} {
		out := string(testkit.ReadFile(t, filepath.Join(root, name)))
		for _, m := range regexp.MustCompile(`__gm_n(\d+),`).FindAllStringSubmatch(out, -1) {
			if sites[m[1]] {
				t.Errorf("site %s is used in more than one file; the limit table has one entry for it", m[1])
			}
			sites[m[1]] = true
		}
	}
	if len(sites) != 2 {
		t.Errorf("the tree numbered %d loop sites, want 2 (one per file)", len(sites))
	}
	if !sites["0"] || !sites["1"] {
		t.Errorf("the sites are %v, want 0 and 1: the space is dense and starts at zero", sites)
	}

	// And the generated runtime sizes its table for exactly those.
	runtime := string(testkit.ReadFile(t, filepath.Join(root, "gomutants_rt", "gomutants_rt.go")))
	if !strings.Contains(runtime, "var Limit = [2]uint64") && !strings.Contains(runtime, "Limit [2]uint64") {
		t.Errorf("the generated runtime does not hold a two-entry limit table:\n%s", runtime)
	}
}

// candidatesInFile is [candidatesIn] for a file that is not [sampleFile].
func candidatesInFile(t *testing.T, path string, src []byte) []mutation.Candidate {
	t.Helper()
	out := candidatesIn(t, src)
	for i := range out {
		out[i].Path = path
	}
	return out
}
