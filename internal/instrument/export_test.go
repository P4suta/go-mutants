// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"go/ast"
	"go/token"
	"slices"

	"github.com/P4suta/go-mutants/internal/interval"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// This file hands the package's external tests the flattener's self-check
// machinery. Nothing here exists outside the test binary.
//
// [CodeNotFlat], [CodeNotIdentical] and [CodeRawStringConversion] are
// postconditions: they report [Flatten] having got its own job wrong, so no
// input produces them and none ever should. That is a reason to run them
// directly rather than a reason to leave them unrun. A check nothing has ever
// executed is not evidence that it fires when it should — it is evidence that
// nobody has looked — and these three are the difference between a spacing bug
// surfacing as a loud error and a mutant that compiles into a different
// program.
//
// The hooks take bytes the flattener would never have emitted, because that is
// the only way to make a postcondition fail.

// CheckFlat runs Flatten's one-line postcondition over arbitrary bytes.
func CheckFlat(out []byte) error { return checkFlat(out) }

// VerifyTokensAgainst runs Flatten's re-tokenization self-check, comparing out
// against the token stream that want scans to. Handing it two sources that
// disagree stands in for the rendering bug the check exists to catch.
func VerifyTokensAgainst(out, want []byte) error {
	tokens, err := scanFragment(want)
	if err != nil {
		return err
	}
	return verifyTokens(out, dropTrailingImplicitSemicolons(tokens))
}

// FlattenLiteral runs the literal rewrite over one literal token's text, as
// scanFragment does for every string and rune token it meets.
func FlattenLiteral(tok token.Token, lit string) (string, error) {
	return flattenLiteral(tok, lit)
}

// The instrumenter's own postconditions follow, for the same reason and on the
// same terms as the flattener's above. Rewrite sites come from a syntax tree
// and import injection from a parsed file, so neither a partially overlapping
// site nor a file without a package clause can arise from any input — which is
// precisely why the checks that reject them have to be run deliberately.

// PlaceSites runs the forest placement over site spans the caller chooses,
// standing in for the site computation having produced spans no syntax tree
// could.
func PlaceSites(srcPath string, spans []mutation.Span) error {
	items := make([]interval.Item[mutation.Mutant], len(spans))
	for i, span := range spans {
		items[i] = interval.Item[mutation.Mutant]{Span: span}
	}
	_, err := placeSites(srcPath, items)
	return err
}

// CheckLineCount runs the file-level line-preservation postcondition over two
// buffers the caller supplies.
func CheckLineCount(srcPath string, src, out []byte) error {
	return checkLineCount(srcPath, src, out)
}

// ParseSnapshot parses bytes the way the instrumenter does, so that a test can
// hand the tree back in a shape no parse produces.
func ParseSnapshot(srcPath string, src []byte) (*ast.File, *token.File, error) {
	return parseSnapshotFile(srcPath, src)
}

// ImportSplices runs the runtime import injection over a syntax tree.
func ImportSplices(file *ast.File, tok *token.File, srcPath, alias, importPath string) ([]Splice, error) {
	return importSplices(file, tok, srcPath, alias, importPath)
}

// AliasFor runs the alias choice over one file and the names the caller says
// its package block already binds.
func AliasFor(file *ast.File, reserved []string) string {
	taken := make(map[string]bool, len(reserved))
	for _, name := range reserved {
		taken[name] = true
	}
	return aliasFor(file, taken)
}

// PackageNames runs the directory scan that finds those names, returning them
// sorted so that a test can state the whole answer.
func PackageNames(dir, pkg string) ([]string, error) {
	names, err := newPackageNames().namesIn(dir, pkg)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}

// WriteRuntime generates the activation package into a directory the caller
// names, so that a write failure can be provoked without depending on file
// modes — which are advice rather than law on some filesystems.
func WriteRuntime(root, dir string, catalog *mutation.Catalog) error {
	return writeRuntime(root, dir, catalog)
}
