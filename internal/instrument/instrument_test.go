// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// updateGolden rewrites the fixtures instead of comparing against them. The
// fixtures are byte-exact instrumented output, so they are generated rather
// than typed; every one of them is still read by eye before it is committed,
// which is the whole point of a golden file.
var updateGolden = flag.Bool("update", false, "rewrite the golden instrumentation fixtures")

const (
	// testModule is the module path the fixtures are instrumented against. It
	// decides the generated runtime's import path and so appears in every
	// golden file.
	testModule = "example.com/mini"
	// sampleFile is the module-relative path every fixture is written to.
	sampleFile = "sample.go"
)

// TestInstrumentGolden pins the instrumented bytes of every shape the three
// rewrite forms have to handle.
//
// Byte-exact fixtures are the right assertion here rather than a structural
// one. The output has to compile, preserve lines, and preserve every byte it
// did not deliberately change, and a test that re-derived what it expected
// would re-derive the same mistake; a fixture a human read once and a diff on
// every later change is what actually catches a guard that grew a newline or
// an import that moved.
func TestInstrumentGolden(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// candidates overrides the fixture's catalogue. Nil means every
		// comparison and boolean literal in the file, which is what discovery
		// would produce.
		candidates func(t *testing.T, src []byte) []mutation.Candidate
		// hints are the answers the fixture's guard hints need beyond its own
		// syntax: what a short declaration declares, and which of its
		// bool-valued expressions are of a named boolean type. See
		// [hintOptions].
		hints hintOptions
		// sibling names the file the fixture's ".sibling" half is written to,
		// for a fixture whose point is what the rest of its package holds.
		// Empty means the fixture is one file on its own.
		sibling string
		// guards is the expected number of rewrite sites, which is not the
		// number of mutants.
		guards int
		// extra asserts whatever else this fixture exists to prove.
		extra func(t *testing.T, in, out []byte)
	}{{
		name:   "comparison",
		guards: 1,
		extra: func(t *testing.T, _, out []byte) {
			// The parenthesized import list keeps its shape: the new import is
			// inserted just inside the "(", so "fmt" and "strings" stay on the
			// lines and in the order the author wrote them.
			assertContains(t, out, `import (__gm "example.com/mini/gomutants_rt";`)
			assertContains(t, out, "\n\t\"fmt\"\n\t\"strings\"\n)")
		},
	}, {
		name:   "boolliteral",
		guards: 2,
		extra: func(t *testing.T, _, out []byte) {
			// A single unparenthesized import is parenthesized in place.
			assertContains(t, out, `import ("strings"; __gm "example.com/mini/gomutants_rt")`)
		},
	}, {
		name:       "alternatives",
		candidates: everyAlternative,
		guards:     1,
		extra: func(t *testing.T, _, out []byte) {
			// A file with no imports gets one on its package clause.
			assertContains(t, out, `package sample; import __gm "example.com/mini/gomutants_rt"`)
			// Five mutants, one guard: the alternatives chain rather than
			// nesting five rewrites of the same bytes.
			for i := range 5 {
				assertContains(t, out, fmt.Sprintf("__gm.M[%d] && (", i))
			}
		},
	}, {
		name:   "nested",
		guards: 6,
		extra: func(t *testing.T, _, out []byte) {
			// The enclosing site's mutated copies are rendered from the
			// pristine source, so they carry no inner guard; the branch that
			// keeps the original does carry them.
			assertContains(t, out, "&& ((a>b)!=(c>d))")
			assertContains(t, out, "&& (((__gm.M[")
		},
	}, {
		name:   "multiline",
		guards: 2,
		extra: func(t *testing.T, in, out []byte) {
			// A guard is written onto the first and last line of its site and
			// nowhere else, so every line strictly inside a multi-line site —
			// and every line outside one — survives byte for byte. The touched
			// lines are the package clause, which took the import, and the
			// first and last line of each of the two sites.
			assertLinesUntouched(t, in, out, 5, 10, 12, 18, 19)
			// The flattened copy cannot keep a line comment; the branch holding
			// the original keeps it exactly where it was.
			assertContains(t, out, "(x<limit)")
			assertContains(t, out, "(x <= // the limit is inclusive")
		},
	}, {
		name:   "unicode",
		guards: 2,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "(größe>=grenze)")
			assertContains(t, out, `("日本語"!=s)`)
		},
	}, {
		name:   "aliascollision",
		guards: 2,
		extra: func(t *testing.T, _, out []byte) {
			// __gm is declared at file scope and __gm1 inside a function, so
			// the alias bumps past both.
			assertContains(t, out, `import __gm2 "example.com/mini/gomutants_rt"`)
			assertContains(t, out, "__gm2.M[0]")
			if bytes.Contains(out, []byte("__gm.M[")) || bytes.Contains(out, []byte("__gm1.M[")) {
				t.Error("a guard used an alias the file had already bound")
			}
		},
	}, {
		name:       "statement",
		candidates: statementEdits,
		guards:     4,
		extra: func(t *testing.T, in, out []byte) {
			// One return, two mutants, one guard: the families differ and the
			// statement does not, which is all a chain of alternatives is
			// about.
			assertContains(t, out, "if __gm.M[0] { return 0,err } else if __gm.M[1] { return count,nil } else { return count, err }")
			// The deletion renders as the empty branch, which is what "this
			// statement does not run" has to mean, and it chains with the
			// operator swap on the same statement whatever family either is.
			assertContains(t, out, "if __gm.M[4] { } else if __gm.M[5] { *counter= *counter-2 } else { *counter = *counter + 2 }")
			// A `defer` is wrapped whole. The guard's block does not change
			// when it fires, because `defer` is scoped to the function.
			assertContains(t, out, "if __gm.M[3] { defer done(*counter-1) } else { defer done(*counter + 1) }")
			// The three-line assignment keeps its two interior lines byte for
			// byte: the guard writes on the statement's first and last line and
			// the flattened copy it carries holds no line break at all. Line 31
			// (0-based 30) is the middle of the site and is untouched.
			assertContains(t, out, "{ total=total-step*2-1 } else { total = total +\n")
			assertLinesUntouched(t, in, out, 6, 16, 29, 31, 44, 45)
		},
	}, {
		name:       "declaration",
		candidates: declarationEdits,
		hints:      hintOptions{declared: declaredTypes()},
		guards:     7,
		extra: func(t *testing.T, _, out []byte) {
			// Both names are hoisted out in front of the guard, in source
			// order, and the `:=` inside it is downgraded to an assignment: the
			// right-hand side is the user's own bytes either way.
			assertContains(t, out, "var lo int; var hi int; if __gm.M[")
			assertContains(t, out, "else { lo, hi = n/2, n-n/2 }")
			// A `var` with an explicit type loses the keyword and the type,
			// which the guard writes back in front of itself from the hint.
			assertContains(t, out, "var scaled int; if __gm.M[")
			assertContains(t, out, "else {  scaled  = v * 3 }")
			// The blank identifier is not a name to declare and is not one to
			// drop either: the assignment keeps the left-hand side as written.
			assertContains(t, out, "var head int; if __gm.M[")
			assertContains(t, out, "else { head, _ = values[0], len(values)-1 }")
			// The parenthesized block keeps every line it had: the keyword and
			// the parentheses are cut out where they stand, which leaves the
			// specs as assignments on their own lines, and the flattened copies
			// carry the semicolons those line breaks stood for.
			assertContains(t, out, "var low int; var high int; if __gm.M[")
			assertContains(t, out, "{ low=values[0]+1;high=values[len(values)-1]+1 }")
			assertContains(t, out, "else {  \n\t\tlow  = values[0] - 1\n\t\thigh = values[len(values)-1] + 1\n\t }")
			// An expression site inside a declaration site: the original branch
			// carries the guard the nested site produced, and the declaration's
			// own mutated copy is rendered from the pristine bytes and carries
			// none.
			assertContains(t, out, "{ weight=cost(a>b)-1 } else { weight = cost((__gm.M[")
		},
	}, {
		name:       "mixedforms",
		candidates: mixedFormEdits,
		guards:     4,
		extra: func(t *testing.T, _, out []byte) {
			// Side by side: an expression guard in the condition, a statement
			// guard in the body it decides.
			assertContains(t, out, "if (__gm.M[0] && (v>=limit) || !(__gm.M[0]) && (v > limit)) {")
			assertContains(t, out, "if __gm.M[1] { v=limit+1 } else { v = limit - 1 }")
			// Nested: the statement guard's original branch carries the
			// expression guard, and its own mutated copy carries none.
			assertContains(t, out, "if __gm.M[3] { return a>b,a+b } else { return (__gm.M[2] && (a>=b) || !(__gm.M[2]) && (a > b)), a - b }")
		},
	}, {
		name:       "namedbool",
		candidates: namedBoolEdits,
		hints:      hintOptions{namedBool: namedBoolExprs()},
		guards:     2,
		extra: func(t *testing.T, _, out []byte) {
			// Form S, not Form C: a selector would evaluate to `bool`, which is
			// not assignable to Flag. Both guards are chains of returns, so
			// each is a terminating statement and each function still ends in
			// one.
			assertContains(t, out, "if __gm.M[0] { return x>=y } else { return x > y }")
			assertContains(t, out, "if __gm.M[1] { return false } else { return true }")
			if bytes.Contains(out, []byte("&& (")) {
				t.Errorf("a named boolean type was guarded with a Form C selector:\n%s", out)
			}
		},
	}, {
		name:    "siblingalias",
		sibling: "sibling.go",
		guards:  1,
		extra: func(t *testing.T, _, out []byte) {
			// Nothing in this file binds __gm or __gm1. The sibling binds both
			// in the package block, where a file-scoped import alias of the
			// same name is a redeclaration rather than a shadow — "__gm already
			// declared through import of package …" — so the alias has to bump
			// past names this file cannot see.
			assertContains(t, out, `import __gm2 "example.com/mini/gomutants_rt"`)
			assertContains(t, out, "__gm2.M[0]")
			if bytes.Contains(out, []byte("__gm.M[")) || bytes.Contains(out, []byte("__gm1.M[")) {
				t.Error("a guard used an alias the package block had already bound")
			}
		},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := readFile(t, filepath.Join("testdata", c.name+".input"))
			root := t.TempDir()
			writeFile(t, filepath.Join(root, sampleFile), in)

			var sibling []byte
			if c.sibling != "" {
				sibling = readFile(t, filepath.Join("testdata", c.name+".sibling"))
				writeFile(t, filepath.Join(root, c.sibling), sibling)
			}

			catalog := catalogOf(t, candidatesFor(t, c.candidates, in))
			result := instrumentSnapshotWith(t, root, catalog, c.hints)
			out := readFile(t, filepath.Join(root, sampleFile))

			if c.sibling != "" {
				if got := readFile(t, filepath.Join(root, c.sibling)); !bytes.Equal(got, sibling) {
					t.Errorf("the sibling holds no candidate and was rewritten anyway:\n%s", got)
				}
			}

			golden := filepath.Join("testdata", c.name+".golden")
			if *updateGolden {
				writeFile(t, golden, out)
			}
			if want := readFile(t, golden); !bytes.Equal(out, want) {
				t.Errorf("instrumented %s does not match its fixture\n--- got ---\n%s\n--- want ---\n%s",
					c.name, out, want)
			}

			assertWellFormed(t, in, out, catalog)
			if got := result.GuardsByFile[sampleFile]; got != c.guards {
				t.Errorf("GuardsByFile[%s] = %d, want %d", sampleFile, got, c.guards)
			}
			if got := result.FilesInstrumented; len(got) != 1 || got[0] != sampleFile {
				t.Errorf("FilesInstrumented = %v, want [%s]", got, sampleFile)
			}
			if c.extra != nil {
				c.extra(t, in, out)
			}
		})
	}
}

// TestInstrumentPreservesCRLFOutsideTheGuards instruments a file with Windows
// line endings.
//
// The fixture is synthesised here rather than committed, because the repository
// checks out byte-exact and forbids a committed CRLF file — see .gitattributes,
// where the reason is the same one this test is about. Converting both the
// input and the expected output is exact: every line break in the instrumented
// file comes from bytes the guard copied verbatim, so a CRLF input produces the
// CRLF form of the same golden and nothing else moves.
func TestInstrumentPreservesCRLFOutsideTheGuards(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name       string
		candidates func(*testing.T, []byte) []mutation.Candidate
	}{
		// Both fixtures whose sites span lines, one per form that can hold a
		// line break in the branch that keeps the original: the expression
		// guard's multi-line condition and the statement guard's multi-line
		// assignment.
		{name: "multiline"},
		{name: "statement", candidates: statementEdits},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := toCRLF(readFile(t, filepath.Join("testdata", c.name+".input")))
			root := t.TempDir()
			writeFile(t, filepath.Join(root, sampleFile), in)

			catalog := catalogOf(t, candidatesFor(t, c.candidates, in))
			instrumentSnapshot(t, root, catalog)
			out := readFile(t, filepath.Join(root, sampleFile))

			if want := toCRLF(readFile(t, filepath.Join("testdata", c.name+".golden"))); !bytes.Equal(out, want) {
				t.Errorf("instrumented CRLF output does not match the converted fixture\n--- got ---\n%q\n--- want ---\n%q",
					out, want)
			}
			assertWellFormed(t, in, out, catalog)

			if bytes.Count(out, []byte("\r\n")) != bytes.Count(out, []byte("\n")) {
				t.Error("the instrumented file lost a carriage return: not every line break is a CRLF")
			}
		})
	}
}

// TestInstrumentLeavesUncatalogedFilesAlone proves the instrumenter edits
// where the catalogue points and nowhere else. A file full of comparisons that
// no mutant names must come back byte-identical, down to its line endings.
func TestInstrumentLeavesUncatalogedFilesAlone(t *testing.T) {
	t.Parallel()

	const untouched = "other.go"
	in := readFile(t, filepath.Join("testdata", "comparison.input"))
	other := toCRLF(readFile(t, filepath.Join("testdata", "unicode.input")))

	root := t.TempDir()
	writeFile(t, filepath.Join(root, sampleFile), in)
	writeFile(t, filepath.Join(root, untouched), other)

	result := instrumentSnapshot(t, root, catalogOf(t, candidatesFor(t, nil, in)))

	if got := readFile(t, filepath.Join(root, untouched)); !bytes.Equal(got, other) {
		t.Errorf("a file with no catalogued mutants was rewritten:\n%s", got)
	}
	if _, ok := result.GuardsByFile[untouched]; ok {
		t.Errorf("GuardsByFile mentions %s, which has no mutants", untouched)
	}
	if len(result.FilesInstrumented) != 1 {
		t.Errorf("FilesInstrumented = %v, want only %s", result.FilesInstrumented, sampleFile)
	}
}

// TestInstrumentReplacesAReadOnlyFile instruments a snapshot file that cannot
// be written to, which is a shape real snapshots produce.
//
// internal/snapshot copies POSIX permission bits verbatim, so a repository
// holding a read-only .go file — Perforce marks unopened files read-only, some
// generators emit 0444, `chmod -w` is a convention in some trees — hands the
// instrumenter one too, and its own documentation says that is safe "only
// because every rewrite in go-mutants is a write to a temporary file followed
// by an atomic rename". This is the test that makes the sentence true: a rename
// needs write permission on the directory and none on the file being replaced,
// where an in-place write would fail EACCES for anybody but root and abort a
// whole run before a single mutant was built.
//
// The assertions are the same everywhere, but only POSIX enforces the mode.
// Where it is advisory the test still passes and simply proves less, which is
// the honest form: insisting otherwise would be testing the filesystem.
func TestInstrumentReplacesAReadOnlyFile(t *testing.T) {
	t.Parallel()

	in := readFile(t, filepath.Join("testdata", "comparison.input"))
	root := t.TempDir()
	target := filepath.Join(root, sampleFile)
	writeFile(t, target, in)

	if err := os.Chmod(target, 0o444); err != nil {
		t.Skipf("this filesystem does not take a read-only mode: %v", err)
	}
	// Registered after the temporary directory's own cleanup and so run before
	// it: a read-only file left behind can defeat the removal on Windows.
	t.Cleanup(func() { _ = os.Chmod(target, 0o600) })

	result := instrumentSnapshot(t, root, catalogOf(t, candidatesFor(t, nil, in)))
	if got := result.GuardsByFile[sampleFile]; got != 1 {
		t.Errorf("GuardsByFile[%s] = %d, want 1", sampleFile, got)
	}

	// The same bytes a writable file produces: the mode decides how the write
	// happens and nothing else about it.
	out := readFile(t, target)
	if want := readFile(t, filepath.Join("testdata", "comparison.golden")); !bytes.Equal(out, want) {
		t.Errorf("a read-only file instrumented to different bytes than a writable one\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}

	// The replacement carries the mode of the file it replaced. The snapshot is
	// disposable, but relaxing what its files allow is still a change nobody
	// asked for, and one that would hide the next regression of this kind.
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o200 != 0 {
		t.Errorf("the instrumented file has mode %v, want the read-only mode it was given", info.Mode().Perm())
	}

	// The temporary file the rewrite went through is gone: the next phase
	// digests this tree and builds it, and neither wants to meet it.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading the snapshot root: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != sampleFile && entry.Name() != result.RuntimeDir {
			t.Errorf("the rewrite left %q behind in the snapshot", entry.Name())
		}
	}
}

// TestInstrumentIsDeterministic instruments two fresh copies of one snapshot
// and compares every byte of both.
//
// Two copies rather than two passes: instrumentation is deterministic, not
// idempotent, and re-running it over its own output would find bytes the
// catalogue no longer describes. Determinism is what shard merging and the
// outcome cache rest on — the same catalogue must produce the same tree on
// every machine that instruments it.
func TestInstrumentIsDeterministic(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name       string
		candidates func(*testing.T, []byte) []mutation.Candidate
		hints      hintOptions
	}{
		// One fixture per shape whose rendering has an order in it that could
		// have come out of a map: nested expression sites, a chain of
		// alternatives from two families, and the declarations a Form D guard
		// hoists out in front of itself.
		{name: "nested"},
		{name: "statement", candidates: statementEdits},
		{name: "declaration", candidates: declarationEdits, hints: hintOptions{declared: declaredTypes()}},
		{name: "mixedforms", candidates: mixedFormEdits},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := readFile(t, filepath.Join("testdata", c.name+".input"))
			catalog := catalogOf(t, candidatesFor(t, c.candidates, in))

			run := func() (string, []byte, []byte) {
				root := t.TempDir()
				writeFile(t, filepath.Join(root, sampleFile), in)
				result := instrumentSnapshotWith(t, root, catalog, c.hints)
				runtime := readFile(t, filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go"))
				return result.RuntimeImport, readFile(t, filepath.Join(root, sampleFile)), runtime
			}

			firstImport, firstSource, firstRuntime := run()
			secondImport, secondSource, secondRuntime := run()

			if firstImport != secondImport {
				t.Errorf("runtime import differs between runs: %q and %q", firstImport, secondImport)
			}
			if !bytes.Equal(firstSource, secondSource) {
				t.Errorf("instrumented source differs between runs\n--- first ---\n%s\n--- second ---\n%s",
					firstSource, secondSource)
			}
			if !bytes.Equal(firstRuntime, secondRuntime) {
				t.Errorf("generated runtime differs between runs\n--- first ---\n%s\n--- second ---\n%s",
					firstRuntime, secondRuntime)
			}
		})
	}
}

// TestInstrumentReportsWhatItDid pins the contract of [instrument.Result] on a
// catalogue spanning two files.
func TestInstrumentReportsWhatItDid(t *testing.T) {
	t.Parallel()

	const second = "pkg/second.go"
	first := readFile(t, filepath.Join("testdata", "comparison.input"))
	other := readFile(t, filepath.Join("testdata", "nested.input"))

	root := t.TempDir()
	writeFile(t, filepath.Join(root, sampleFile), first)
	writeFile(t, filepath.Join(root, filepath.FromSlash(second)), other)

	candidates := candidatesFor(t, nil, first)
	for _, c := range candidatesFor(t, nil, other) {
		c.Path = second
		candidates = append(candidates, c)
	}
	result := instrumentSnapshot(t, root, catalogOf(t, candidates))

	if got, want := result.RuntimeDir, "gomutants_rt"; got != want {
		t.Errorf("RuntimeDir = %q, want %q", got, want)
	}
	if got, want := result.RuntimeImport, testModule+"/gomutants_rt"; got != want {
		t.Errorf("RuntimeImport = %q, want %q", got, want)
	}
	// Catalogue order, which is path order: "pkg/second.go" sorts before
	// "sample.go".
	if got, want := result.FilesInstrumented, []string{second, sampleFile}; !equalStrings(got, want) {
		t.Errorf("FilesInstrumented = %v, want %v", got, want)
	}
	if got, want := result.GuardsByFile, map[string]int{second: 6, sampleFile: 1}; !equalCounts(got, want) {
		t.Errorf("GuardsByFile = %v, want %v", got, want)
	}
}

// candidatesFor resolves a fixture's catalogue, defaulting to every comparison
// and boolean literal in the file.
func candidatesFor(t *testing.T, override func(*testing.T, []byte) []mutation.Candidate, src []byte) []mutation.Candidate {
	t.Helper()
	if override != nil {
		return override(t, src)
	}
	return candidatesIn(t, src)
}

// comparisonRules pairs each comparison operator with the rule that rewrites
// it, exactly as internal/discover does. It is spelled out again here so that
// the fixtures' catalogues are the test's own statement of what discovery
// produces rather than a call into the code under test's neighbour.
var comparisonRules = map[token.Token]struct{ rule, replacement string }{
	token.EQL: {"eq-to-neq", "!="},
	token.NEQ: {"neq-to-eq", "=="},
	token.LSS: {"lt-to-le", "<="},
	token.LEQ: {"le-to-lt", "<"},
	token.GTR: {"gt-to-ge", ">="},
	token.GEQ: {"ge-to-gt", ">"},
}

// booleanRules is the boolean-literal family, keyed by the literal.
var booleanRules = map[string]struct{ rule, replacement string }{
	"true":  {"true-to-false", "false"},
	"false": {"false-to-true", "true"},
}

// candidatesIn produces the candidates discovery would find in src: every
// comparison operator and every boolean literal.
func candidatesIn(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, sampleFile, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	tok := fset.File(file.Package)
	digest := mutation.Digest(src)

	var out []mutation.Candidate
	add := func(start uint32, rule, original, replacement string) {
		out = append(out, mutation.Candidate{
			Path:         sampleFile,
			Rule:         lookupRule(t, rule),
			Span:         mutation.Span{StartByte: start, EndByte: start + uint32(len(original))},
			Original:     original,
			Replacement:  replacement,
			SourceDigest: digest,
		})
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.BinaryExpr:
			if swap, ok := comparisonRules[n.Op]; ok {
				add(uint32(tok.Offset(n.OpPos)), swap.rule, n.Op.String(), swap.replacement)
			}
		case *ast.Ident:
			if swap, ok := booleanRules[n.Name]; ok {
				add(uint32(tok.Offset(n.Pos())), swap.rule, n.Name, swap.replacement)
			}
		}
		return true
	})
	return out
}

// everyAlternative points every comparison rule that can produce a distinct
// edit at the fixture's single operator, so that one rewrite site carries a
// whole chain of alternatives.
//
// Five, not six: the operator in the fixture is "<", and le-to-lt would write
// "<" over "<", which the catalogue rejects as a no-op rather than catalogue an
// edit that changes nothing. The rules are otherwise applied to an operator
// they were not written for, which the instrumenter neither knows nor needs to:
// a rewrite site is decided by the operator family and the syntax, and what a
// rule is named is the catalogue's business.
func everyAlternative(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()

	start := uint32(bytes.IndexByte(src, '<'))
	digest := mutation.Digest(src)
	var out []mutation.Candidate
	for _, swap := range []struct{ rule, replacement string }{
		{"eq-to-neq", "!="},
		{"neq-to-eq", "=="},
		{"lt-to-le", "<="},
		{"gt-to-ge", ">="},
		{"ge-to-gt", ">"},
	} {
		out = append(out, mutation.Candidate{
			Path:         sampleFile,
			Rule:         lookupRule(t, swap.rule),
			Span:         mutation.Span{StartByte: start, EndByte: start + 1},
			Original:     "<",
			Replacement:  swap.replacement,
			SourceDigest: digest,
		})
	}
	return out
}

// lookupRule resolves a rule name against the canonical registry.
func lookupRule(t *testing.T, name string) mutation.Rule {
	t.Helper()
	rule, ok := mutation.CanonicalRegistry().Lookup(name)
	if !ok {
		t.Fatalf("unknown rule %q", name)
	}
	return rule
}

// catalogOf builds a catalogue from candidates.
func catalogOf(t *testing.T, candidates []mutation.Candidate) *mutation.Catalog {
	t.Helper()
	builder := mutation.NewBuilder()
	if err := builder.AddAll(candidates); err != nil {
		t.Fatalf("cataloguing: %v", err)
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	return catalog
}

// instrumentSnapshot runs the instrumenter over a snapshot and fails the test
// if it refuses. The guard hints are derived from the snapshot itself; see
// hints_test.go for what that derivation is and is not.
func instrumentSnapshot(t *testing.T, root string, catalog *mutation.Catalog) instrument.Result {
	t.Helper()
	return instrumentSnapshotWith(t, root, catalog, hintOptions{})
}

// instrumentSnapshotWith is [instrumentSnapshot] for a fixture that has to
// state something about its own types.
func instrumentSnapshotWith(
	t *testing.T,
	root string,
	catalog *mutation.Catalog,
	opts hintOptions,
) instrument.Result {
	t.Helper()
	return instrumentSnapshotHinted(t, root, catalog, hintsFor(t, root, catalog, opts))
}

// instrumentSnapshotHinted is [instrumentSnapshot] for a caller that assembled
// the hints itself, which a tree of several fixtures has to.
func instrumentSnapshotHinted(
	t *testing.T,
	root string,
	catalog *mutation.Catalog,
	hints instrument.Hints,
) instrument.Result {
	t.Helper()
	result, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: root,
		ModulePath:   testModule,
		Catalog:      catalog,
		Hints:        hints,
	})
	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}
	return result
}

// declaredTypes is what the declaration fixture's short declarations declare.
// It is stated once here because both the golden test and the compile test have
// to hand it to the hint derivation.
func declaredTypes() map[string]string {
	return map[string]string{
		"lo": "int", "hi": "int", "scaled": "int", "step": "int", "head": "int",
		"low": "int", "high": "int", "weight": "int",
	}
}

// namedBoolExprs is the same for the named boolean fixture: the expressions
// whose type is [Flag] rather than the universe bool.
func namedBoolExprs() []string { return []string{"x > y", "true"} }

// The edit tables of the fixtures whose catalogues are not "every comparison
// and boolean literal". Each states the rule, the bytes it replaces, and what
// it writes, exactly as internal/discover would have proposed them.

// statementEdits catalogues the statement fixture: a return carrying two
// families at once, an operator inside a multi-line assignment, one inside a
// deferred call, and a statement that is both swapped and deleted.
func statementEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-zero-numeric", in: "return count, err", find: "count", with: "0"},
		editSpec{rule: "return-err-to-nil", in: "return count, err", find: "err", with: "nil"},
		editSpec{rule: "add-to-sub", in: "total = total +", find: "+", with: "-"},
		editSpec{rule: "add-to-sub", in: "done(*counter + 1)", find: "+", with: "-"},
		editSpec{rule: "add-to-sub", in: "*counter = *counter + 2", find: "+", with: "-"},
		editSpec{rule: "delete-assignment", in: "*counter = *counter + 2"},
	)
}

// declarationEdits catalogues the declaration fixture: two names, one name, an
// explicit type, and a blank identifier.
func declarationEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "div-to-mul", in: "lo, hi := n/2, n-n/2", find: "/", with: "*"},
		editSpec{rule: "sub-to-add", in: "n-n/2", find: "-", with: "+"},
		editSpec{rule: "mul-to-div", in: "var scaled int = v * 3", find: "*", with: "/"},
		editSpec{rule: "add-to-sub", in: "step := base + 1", find: "+", with: "-"},
		editSpec{rule: "sub-to-add", in: "head, _ := values[0], len(values)-1", find: "-", with: "+"},
		editSpec{rule: "gt-to-ge", in: "cost(a > b) + 1", find: ">", with: ">="},
		editSpec{rule: "add-to-sub", in: "cost(a > b) + 1", find: "+", with: "-"},
		editSpec{rule: "sub-to-add", in: "low  = values[0] - 1", find: "-", with: "+"},
		editSpec{rule: "add-to-sub", in: "values[len(values)-1] + 1", find: "+", with: "-"},
	)
}

// mixedFormEdits catalogues the fixture where the forms meet: a comparison and
// a statement side by side, and a comparison inside a statement.
func mixedFormEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "gt-to-ge", in: "v > limit", find: ">", with: ">="},
		editSpec{rule: "sub-to-add", in: "v = limit - 1", find: "-", with: "+"},
		editSpec{rule: "gt-to-ge", in: "return a > b, a - b", find: ">", with: ">="},
		editSpec{rule: "sub-to-add", in: "a - b", find: "-", with: "+"},
	)
}

// namedBoolEdits catalogues the named boolean fixture: the comparison and the
// literal that a selector cannot produce the type of.
func namedBoolEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "gt-to-ge", in: "return x > y", find: ">", with: ">="},
		editSpec{rule: "true-to-false", in: "return true", find: "true", with: "false"},
	)
}

// assertWellFormed runs the invariants every instrumented file must hold,
// whatever the fixture.
func assertWellFormed(t *testing.T, in, out []byte, catalog *mutation.Catalog) {
	t.Helper()

	if _, err := parser.ParseFile(token.NewFileSet(), sampleFile, out, parser.SkipObjectResolution); err != nil {
		t.Errorf("the instrumented file does not parse: %v\n%s", err, out)
	}
	if got, want := instrument.CountLines(out), instrument.CountLines(in); got != want {
		t.Errorf("the instrumented file holds %d line breaks, the original holds %d", got, want)
	}
	// The package's own predicate, applied to the file-sized edit the whole
	// rewrite amounts to.
	whole := []instrument.Splice{{
		Span:        mutation.Span{StartByte: 0, EndByte: uint32(len(in))},
		Original:    in,
		Replacement: out,
	}}
	if !instrument.LinePreserving(whole) {
		t.Error("the rewrite is not line-preserving")
	}

	// Every mutant's own bytes are still on the line they were written on, and
	// its activation flag is somewhere in the file. Together these say the
	// guard went where the mutant is rather than merely somewhere.
	inLines, outLines := lines(in), lines(out)
	for _, m := range catalog.Mutants() {
		line := instrument.CountLines(in[:m.Span.StartByte])
		if line >= len(outLines) {
			t.Fatalf("mutant %s starts past the end of the file", m.DisplayID)
		}
		if !strings.Contains(outLines[line], m.Original) {
			t.Errorf("mutant %s: line %d was %q and is now %q, which no longer holds %q",
				m.DisplayID, line+1, inLines[line], outLines[line], m.Original)
		}
		if flag := fmt.Sprintf(".M[%d]", m.Index); !bytes.Contains(out, []byte(flag)) {
			t.Errorf("mutant %s: no guard reads %s", m.DisplayID, flag)
		}
	}
}

// assertLinesUntouched checks that every line except the named ones came
// through the rewrite byte for byte.
//
// It is the sharp form of line preservation: equal line counts say the file
// still has the same number of lines, while this says line N of the output is
// line N of the input wherever nothing was inserted, which is what a coverage
// record, a panic trace, and a reported mutant coordinate all depend on.
func assertLinesUntouched(t *testing.T, in, out []byte, touched ...int) {
	t.Helper()

	written := make(map[int]bool, len(touched))
	for _, line := range touched {
		written[line] = true
	}
	inLines, outLines := lines(in), lines(out)
	if len(inLines) != len(outLines) {
		t.Fatalf("the instrumented file has %d lines, the original has %d", len(outLines), len(inLines))
	}
	for i := range inLines {
		if written[i] || inLines[i] == outLines[i] {
			continue
		}
		t.Errorf("line %d was not expected to change: %q became %q", i+1, inLines[i], outLines[i])
	}
	for line := range written {
		if inLines[line] == outLines[line] {
			t.Errorf("line %d was expected to carry instrumentation and is unchanged: %q", line+1, inLines[line])
		}
	}
}

// assertContains fails the test when out is missing a fragment the fixture is
// about, quoting the whole file: a golden diff is unreadable without it.
func assertContains(t *testing.T, out []byte, want string) {
	t.Helper()
	if !bytes.Contains(out, []byte(want)) {
		t.Errorf("the instrumented file does not contain %q:\n%s", want, out)
	}
}

// lines splits a buffer into lines, keeping neither the "\n" nor a trailing
// empty line.
func lines(b []byte) []string {
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
}

// toCRLF rewrites LF line endings as CRLF.
func toCRLF(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
}

// readFile reads a file or fails the test.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return b
}

// writeFile writes a file, creating its directory, or fails the test.
func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// equalStrings compares two string slices element by element.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// equalCounts compares two path-to-count maps.
func equalCounts(got, want map[string]int) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
