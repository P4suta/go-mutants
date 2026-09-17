// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"go/ast"
	"go/token"
	"slices"

	"github.com/P4suta/go-mutants/internal/interval"
	"github.com/P4suta/go-mutants/internal/mutation"

	"github.com/P4suta/go-mutants/internal/discover"
)

func CheckFlat(out []byte) error { return checkFlat(out) }

func VerifyTokensAgainst(out, want []byte) error {
	tokens, err := scanFragment(want)
	if err != nil {
		return err
	}
	return verifyTokens(out, dropTrailingImplicitSemicolons(tokens))
}

func FlattenLiteral(tok token.Token, lit string) (string, error) {
	return flattenLiteral(tok, lit)
}

func PlaceSites(srcPath string, spans []mutation.Span) error {
	items := make([]interval.Item[mutation.Mutant], len(spans))
	for i, span := range spans {
		items[i] = interval.Item[mutation.Mutant]{Span: span}
	}
	_, err := placeSites(srcPath, items)
	return err
}

func CheckLineCount(srcPath string, src, out []byte) error {
	return checkLineCount(srcPath, src, out)
}

func ParseSnapshot(srcPath string, src []byte) (*ast.File, *token.File, error) {
	return parseSnapshotFile(srcPath, src)
}

func ImportSplices(
	file *ast.File, tok *token.File, srcPath, alias, importPath string, completions ...discover.Completion,
) ([]Splice, error) {
	return importSplices(file, tok, srcPath, alias, importPath, completions)
}

func AliasFor(file *ast.File, reserved []string) string {
	taken := make(map[string]bool, len(reserved))
	for _, name := range reserved {
		taken[name] = true
	}
	return aliasFor(file, taken)
}

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

func WriteRuntime(root, dir string, catalog *mutation.Catalog) error {
	return writeRuntime(root, dir, "example.com/mini", catalog, nil)
}

func WrappableStatement(stmt ast.Stmt) bool { return wrappableStatement(stmt) }

func ClosurableStatement(stmt ast.Stmt) bool { return closurableStatement(stmt) }
