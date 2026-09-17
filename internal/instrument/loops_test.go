// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
)

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

	if !regexp.MustCompile(`:= uint64\(0\), __gm\d*\.Limit\[\d+\]; outer:`).MatchString(text) {
		t.Errorf("the labelled loop's declaration did not land in front of its label:\n%s", text)
	}

	jump := text[strings.Index(text, "func jumping"):]
	if strings.Contains(jump, "__gm_n") {
		t.Errorf("a loop inside a function holding a goto was counted:\n%s", jump)
	}
}

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

	runtime := string(testkit.ReadFile(t, filepath.Join(root, "gomutants_rt", "gomutants_rt.go")))
	if !strings.Contains(runtime, "var Limit = [2]uint64") && !strings.Contains(runtime, "Limit [2]uint64") {
		t.Errorf("the generated runtime does not hold a two-entry limit table:\n%s", runtime)
	}
}

func candidatesInFile(t *testing.T, path string, src []byte) []mutation.Candidate {
	t.Helper()
	out := candidatesIn(t, src)
	for i := range out {
		out[i].Path = path
	}
	return out
}

const loopInsideAMutatedStatement = `package sample

import "errors"

func each(f func() error) error { return f() }

func run(n int) error {
	return each(func() error {
		for i := 0; i < n; i++ {
			if i > 2 {
				return errors.New("too big")
			}
		}
		return nil
	})
}
`

func TestALoopInsideARewriteSiteIsStillCounted(t *testing.T) {
	t.Parallel()

	src := []byte(loopInsideAMutatedStatement)
	root := t.TempDir()
	testkit.WriteFile(t, filepath.Join(root, sampleFile), src)

	candidates := append(candidatesIn(t, src), returnedCallCandidate(t, src))
	catalog := catalogOf(t, candidates)
	instrumentSnapshot(t, root, catalog)
	out := testkit.ReadFile(t, filepath.Join(root, sampleFile))

	assertWellFormed(t, src, out, catalog)

	text := string(out)
	declarations := regexp.MustCompile(`__gm_n(\d+), __gm_k(\d+) :=`).FindAllStringSubmatch(text, -1)
	if len(declarations) == 0 {
		t.Fatalf("the one loop in this file was left uncounted:\n%s", text)
	}
	for _, d := range declarations {
		if want := "__gm_n" + d[1] + " > __gm_k" + d[1]; !strings.Contains(text, want) {
			t.Errorf("counter %s is declared and never tested; %q is not in the file", d[1], want)
		}
	}
}

func returnedCallCandidate(t *testing.T, src []byte) mutation.Candidate {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, sampleFile, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	tok := fset.File(file.Package)
	var found *ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		ret, ok := node.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 || found != nil {
			return true
		}
		call, ok := ret.Results[0].(*ast.CallExpr)
		if ok && len(call.Args) == 1 {
			if _, isLiteral := call.Args[0].(*ast.FuncLit); isLiteral {
				found = call
			}
		}
		return true
	})
	if found == nil {
		t.Fatal("the fixture has no returned call taking a function literal")
	}
	start, end := uint32(tok.Offset(found.Pos())), uint32(tok.Offset(found.End()))
	return mutation.Candidate{
		Path:         sampleFile,
		Rule:         lookupRule(t, "return-err-to-nil"),
		Span:         mutation.Span{StartByte: start, EndByte: end},
		Original:     string(src[start:end]),
		Replacement:  "nil",
		SourceDigest: mutation.Digest(src),
	}
}
