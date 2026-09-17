// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// errorsDoc is the page this file holds to the source.
const errorsDoc = "docs/errors.md"

// codePattern is what a diagnostic code looks like. It is built rather than
// written so that this rule file does not match itself when the scan reads it.
var codePattern = regexp.MustCompile(`^GOM` + `[0-9]{4}$`)

// codeInProse finds a code anywhere in a line of a document.
var codeInProse = regexp.MustCompile(`GOM` + `[0-9]{4}`)

// retiredHeading opens the section listing codes that were spent and will never
// be spent again.
const retiredHeading = "## Retired codes"

// allocationHeading opens the section that says what a leading digit means and
// which package owns each block.
const allocationHeading = "## How the numbers are allocated"

// codeFileName is where a package declares its codes.
//
// Every one of the sixteen packages that has codes follows it, and the scan
// depends on that: reading every non-test .go file in the module to find the
// seventeen that hold them would cost a hundred times as much for the same
// answer.
// TestEveryDiagnosticCodeIsDeclaredInAnErrorsFile is what keeps the shortcut
// honest, by reading everything once and refusing a code found elsewhere.
const codeFileName = "errors.go"

// declaredCode is one code as the source declares it.
type declaredCode struct {
	Code    string
	Name    string
	Doc     string
	Package string
}

// TestErrorDocNamesEveryDiagnosticCode is the forward half of the ledger.
func TestErrorDocNamesEveryDiagnosticCode(t *testing.T) {
	t.Parallel()

	root := Root(t)
	declared := declaredCodes(t, root)
	if len(declared) < 200 {
		t.Fatalf("the scan found %d codes, which is too few to be this repository;"+
			" it has stopped seeing them", len(declared))
	}
	documented := documentedCodes(t, root)

	for _, code := range declared {
		if _, ok := documented[code.Code]; !ok {
			t.Errorf("%s is declared as %s.%s and %s does not name it",
				code.Code, code.Package, code.Name, errorsDoc)
		}
	}
}

// TestEveryCodeTheErrorDocNamesIsOneABuildCanReport is the reverse half.
//
// A one-way check misses the direction that actually rots: a code removed from
// the source leaves its paragraph behind, and a reader searching for what they
// saw in an old report lands on a sentence about something this build cannot
// produce. A retired code is the deliberate case and has its own section.
func TestEveryCodeTheErrorDocNamesIsOneABuildCanReport(t *testing.T) {
	t.Parallel()

	root := Root(t)
	declared := map[string]bool{}
	for _, code := range declaredCodes(t, root) {
		declared[code.Code] = true
	}
	retired := retiredCodes(t, root)

	for code := range documentedCodes(t, root) {
		if declared[code] || retired[code] {
			continue
		}
		t.Errorf("%s names %s, which no package declares;"+
			" move it to `%s` with what it used to report, or delete the row",
			errorsDoc, code, retiredHeading)
	}
}

// TestEveryDiagnosticCodeRowSaysWhatItMeansAndWhatToDo pins the shape of a row.
//
// The remedy column is prose rather than a field on the code, so nothing can
// check that a particular remedy is right. What can be checked is that there is
// one: a row whose remedy cell is empty is a row that stopped at naming the
// failure, and naming it is what the code already did.
func TestEveryDiagnosticCodeRowSaysWhatItMeansAndWhatToDo(t *testing.T) {
	t.Parallel()

	for code, row := range documentedCodes(t, Root(t)) {
		if len(row) != 3 {
			t.Errorf("%s's row for %s has %d cells, want code, meaning and remedy", errorsDoc, code, len(row))
			continue
		}
		if strings.TrimSpace(row[1]) == "" || strings.TrimSpace(row[1]) == "—" {
			t.Errorf("%s's row for %s says nothing about what it means", errorsDoc, code)
		}
		if strings.TrimSpace(row[2]) == "" {
			t.Errorf("%s's row for %s has an empty remedy cell;"+
				" write `—` when there is nothing a reader can do, so that the blank is a decision", errorsDoc, code)
		}
	}
}

// TestNoDiagnosticCodeFallsInAnUnallocatedClass turns the digit table from
// decoration into a fact, in both directions.
func TestNoDiagnosticCodeFallsInAnUnallocatedClass(t *testing.T) {
	t.Parallel()

	root := Root(t)
	allocated := allocatedDigits(t, root)
	if len(allocated) == 0 {
		t.Fatalf("%s allocates no digits; the parser has stopped seeing the table", errorsDoc)
	}
	used := map[string]bool{}
	for _, code := range declaredCodes(t, root) {
		digit := code.Code[3:4]
		used[digit] = true
		if !allocated[digit] {
			t.Errorf("%s (%s.%s) has leading digit %s, which %s does not allocate",
				code.Code, code.Package, code.Name, digit, errorsDoc)
		}
	}
	for digit := range allocated {
		if !used[digit] {
			t.Errorf("%s allocates the digit %s, which no code uses;"+
				" move it to the unallocated row", errorsDoc, digit)
		}
	}
}

// TestTheErrorDocBlockTableNamesEveryPackageThatOwnsCodes keeps the block table
// equal to the blocks the source actually claims.
//
// A block is written as a pattern -- GOM41xx, GOM720x -- rather than at a fixed
// width, because ownership is not at a fixed width. internal/runner and
// internal/gocmd share the seventy-twos and internal/tui and internal/gitdiff
// share the seventy-sevens, so a table that could only say GOM72xx would have
// to name one of two owners and be wrong about the other. The rule is that
// every code matches exactly one row and that row names its package: a code
// matching none is unowned, one matching two is a table that overlaps itself,
// and a row matching nothing is a block this build no longer holds.
func TestTheErrorDocBlockTableNamesEveryPackageThatOwnsCodes(t *testing.T) {
	t.Parallel()

	root := Root(t)
	documented := documentedBlocks(t, root)
	if len(documented) == 0 {
		t.Fatalf("%s names no blocks; the parser has stopped seeing the table", errorsDoc)
	}
	matched := map[string]bool{}
	for _, code := range declaredCodes(t, root) {
		var hits []string
		for block := range documented {
			if blockCovers(block, code.Code) {
				hits = append(hits, block)
			}
		}
		switch len(hits) {
		case 0:
			t.Errorf("%s (%s.%s) falls in no block %s names",
				code.Code, code.Package, code.Name, errorsDoc)
		case 1:
			matched[hits[0]] = true
			if documented[hits[0]] != code.Package {
				t.Errorf("%s names %s as %s's and %s.%s declares %s",
					errorsDoc, hits[0], documented[hits[0]], code.Package, code.Name, code.Code)
			}
		default:
			slices.Sort(hits)
			t.Errorf("%s falls in %d blocks %s names: %s",
				code.Code, len(hits), errorsDoc, strings.Join(hits, ", "))
		}
	}
	for block, pkg := range documented {
		if !matched[block] {
			t.Errorf("%s names block %s as %s's, and no code in this build falls in it",
				errorsDoc, block, pkg)
		}
	}
}

// blockCovers reports whether a block pattern such as `GOM720x` covers a code,
// with `x` standing for any digit.
func blockCovers(block, code string) bool {
	if len(block) != len(code) {
		return false
	}
	for i := range block {
		if block[i] != 'x' && block[i] != code[i] {
			return false
		}
	}
	return true
}

// TestDiagnosticCodesAreUniqueAcrossPackages is the check no package could make
// for itself.
//
// Each of the sixteen packages tests that its own block is complete and
// well-formed, and none of them can see another's. Two packages reaching for
// the same number is therefore the one malformation that would have been caught
// by nobody, and it is the malformation that costs a user most: one identifier,
// two conditions, and a search that answers the wrong question.
func TestDiagnosticCodesAreUniqueAcrossPackages(t *testing.T) {
	t.Parallel()

	seen := map[string]declaredCode{}
	for _, code := range declaredCodes(t, Root(t)) {
		if held, ok := seen[code.Code]; ok {
			t.Errorf("%s is declared twice: %s.%s and %s.%s",
				code.Code, held.Package, held.Name, code.Package, code.Name)
			continue
		}
		seen[code.Code] = code
	}
}

// TestNoRetiredCodeIsDeclaredAnywhere keeps a spent number spent.
func TestNoRetiredCodeIsDeclaredAnywhere(t *testing.T) {
	t.Parallel()

	root := Root(t)
	retired := retiredCodes(t, root)
	if len(retired) == 0 {
		t.Fatalf("%s lists no retired codes; the parser has stopped seeing the section", errorsDoc)
	}
	for _, code := range declaredCodes(t, root) {
		if retired[code.Code] {
			t.Errorf("%s is listed as retired and %s.%s declares it;"+
				" a number is spent once -- allocate a new one rather than reusing it,"+
				" so that a user searching for what an old report said finds it",
				code.Code, code.Package, code.Name)
		}
	}
}

// TestEveryDiagnosticCodeIsDeclaredInAnErrorsFile keeps the fast scan honest.
//
// declaredCodes reads only files named errors.go, which is the convention every
// package with codes follows and which makes the scan sixteen files rather than
// two hundred and fifty. A code declared anywhere else would be invisible to
// every ledger above, so the shortcut needs a check of its own -- and this is
// the one test that pays the full price, once.
func TestEveryDiagnosticCodeIsDeclaredInAnErrorsFile(t *testing.T) {
	t.Parallel()

	root := Root(t)
	var strays []string
	walkGoSources(t, root, func(path string) {
		if filepath.Base(path) == codeFileName {
			return
		}
		for _, constant := range exportedStringConstants(t, path) {
			if codePattern.MatchString(constant.Value) {
				rel, _ := filepath.Rel(root, path)
				strays = append(strays, constant.Value+" in "+filepath.ToSlash(rel))
			}
		}
	})
	if len(strays) != 0 {
		t.Errorf("%d diagnostic code(s) are declared outside a %s:\n\t%s\n"+
			"move them, or the ledgers over %s stop seeing them",
			len(strays), codeFileName, strings.Join(strays, "\n\t"), errorsDoc)
	}
}

// citingPages are the pages that name a diagnostic code in their prose.
//
// A citation is a promise that the code exists, and a page that keeps citing a
// renamed one sends a reader to search for something this build cannot print.
// The list is explicit rather than a glob so that adding a page is a decision:
// a new page that cites a code should be added here in the same change.
var citingPages = []string{
	"README.md",
	"docs/architecture.md",
	"docs/configuration.md",
	"docs/development.md",
	"docs/json-schema.md",
	"docs/operators.md",
	"docs/trace-v1.md",
}

// TestEveryCodeTheOtherPagesCiteIsOneTheErrorDocExplains keeps a citation from
// outliving what it cites.
func TestEveryCodeTheOtherPagesCiteIsOneTheErrorDocExplains(t *testing.T) {
	t.Parallel()

	root := Root(t)
	documented := documentedCodes(t, root)
	retired := retiredCodes(t, root)
	cited := 0
	for _, page := range citingPages {
		for _, code := range codesInProse(t, root, page) {
			cited++
			if _, ok := documented[code]; ok {
				continue
			}
			if retired[code] {
				continue
			}
			t.Errorf("%s cites %s and %s does not explain it", page, code, errorsDoc)
		}
	}
	if cited == 0 {
		t.Fatalf("no page cites a code; the scan has stopped seeing them")
	}
}

// declaredCodes is every diagnostic code this build can print, in code order.
func declaredCodes(t *testing.T, root string) []declaredCode {
	t.Helper()

	var found []declaredCode
	walkGoSources(t, root, func(path string) {
		if filepath.Base(path) != codeFileName {
			return
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			t.Fatalf("locating %s: %v", path, err)
		}
		pkg := filepath.ToSlash(rel)
		for _, constant := range exportedStringConstants(t, path) {
			if !codePattern.MatchString(constant.Value) {
				continue
			}
			found = append(found, declaredCode{
				Code:    constant.Value,
				Name:    constant.Name,
				Doc:     constant.Doc,
				Package: pkg,
			})
		}
	})
	slices.SortFunc(found, func(a, b declaredCode) int { return strings.Compare(a.Code, b.Code) })
	return found
}

// walkGoSources visits every non-test Go source file of this module.
func walkGoSources(t *testing.T, root string, visit func(path string)) {
	t.Helper()

	skip := map[string]bool{".git": true, "fixtures": true, "testdata": true, "vendor-assets": true}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skip[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		visit(path)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}

// documentedCodes is every code the page's tables name, with its row.
func documentedCodes(t *testing.T, root string) map[string][]string {
	t.Helper()

	rows := map[string][]string{}
	for _, row := range tableRows(t, root, func(heading string) bool {
		return heading != retiredHeading && heading != allocationHeading
	}) {
		code := strings.Trim(row[0], "`")
		if codePattern.MatchString(code) {
			rows[code] = row
		}
	}
	return rows
}

// retiredCodes is every code the retired section names.
func retiredCodes(t *testing.T, root string) map[string]bool {
	t.Helper()

	out := map[string]bool{}
	for _, row := range tableRows(t, root, func(heading string) bool { return heading == retiredHeading }) {
		code := strings.Trim(row[0], "`")
		if codePattern.MatchString(code) {
			out[code] = true
		}
	}
	return out
}

// allocatedDigits is the leading digits the allocation table gives a meaning.
//
// A row naming several digits -- the unallocated one -- is read as none of
// them: saying a digit is unallocated is the opposite of allocating it.
func allocatedDigits(t *testing.T, root string) map[string]bool {
	t.Helper()

	out := map[string]bool{}
	for _, row := range tableRows(t, root, func(heading string) bool { return heading == allocationHeading }) {
		first := strings.Trim(row[0], "` ")
		if len(first) == 1 && first[0] >= '0' && first[0] <= '9' {
			out[first] = true
		}
	}
	return out
}

// documentedBlocks is the block-to-package table, as `GOM41xx` ->
// `internal/discover`.
//
// A block ends in at least one `x` and may end in more than one, because
// ownership is not at a fixed width: `GOM720x` is internal/runner's and
// `GOM721x` is internal/gocmd's.
func documentedBlocks(t *testing.T, root string) map[string]string {
	t.Helper()

	out := map[string]string{}
	for _, row := range tableRows(t, root, func(heading string) bool { return heading == allocationHeading }) {
		block := strings.Trim(row[0], "` ")
		if len(row) < 2 || !strings.HasPrefix(block, "GOM") || !strings.HasSuffix(block, "x") {
			continue
		}
		out[block] = strings.Trim(row[1], "` ")
	}
	return out
}

// tableRows is every pipe-table row of the page whose enclosing `## ` heading
// the predicate accepts, as its trimmed cells.
func tableRows(t *testing.T, root string, want func(heading string) bool) [][]string {
	t.Helper()

	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(errorsDoc)))
	if err != nil {
		t.Fatalf("reading %s: %v", errorsDoc, err)
	}
	var rows [][]string
	heading := ""
	for _, line := range strings.Split(string(source), "\n") {
		if strings.HasPrefix(line, "## ") {
			heading = strings.TrimRight(line, " ")
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") || !want(heading) {
			continue
		}
		cells := splitCells(strings.Trim(trimmed, "|"))
		if len(cells) == 0 || strings.HasPrefix(cells[0], "-") || strings.HasSuffix(cells[0], "-:") {
			continue
		}
		rows = append(rows, cells)
	}
	return rows
}

// splitCells divides one table row into its cells, honouring the `\|` a cell
// uses to hold a pipe of its own.
//
// A naive split on every pipe reads `go test -run 'A\|B'` as two cells, and a
// row-shape check over it reports a malformation in the document that is
// actually one in the reader.
func splitCells(row string) []string {
	var cells []string
	var current strings.Builder
	for i := 0; i < len(row); i++ {
		switch {
		case row[i] == '\\' && i+1 < len(row) && row[i+1] == '|':
			current.WriteByte('|')
			i++
		case row[i] == '|':
			cells = append(cells, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteByte(row[i])
		}
	}
	cells = append(cells, strings.TrimSpace(current.String()))
	return cells
}

// codesInProse is every diagnostic code another page mentions, which is what
// keeps a citation from outliving the code it cites.
func codesInProse(t *testing.T, root, page string) []string {
	t.Helper()

	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page)))
	if err != nil {
		t.Fatalf("reading %s: %v", page, err)
	}
	found := codeInProse.FindAllString(string(source), -1)
	slices.Sort(found)
	return slices.Compact(found)
}
