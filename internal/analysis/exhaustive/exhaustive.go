// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package exhaustive refuses a switch over a closed vocabulary that skips a word.
package exhaustive

import (
	"fmt"
	"go/ast"
	"go/constant"
	gotoken "go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

const modulePrefix = "github.com/P4suta/go-mutants"

const minimumVocabulary = 2

var Analyzer = &analysis.Analyzer{
	Name:     "exhaustive",
	Doc:      "report a switch over one of this repository's closed vocabularies that does not name every word",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
	trees, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, fmt.Errorf("exhaustive: the inspect pass returned %T", pass.ResultOf[inspect.Analyzer])
	}
	trees.Preorder([]ast.Node{(*ast.SwitchStmt)(nil)}, func(node ast.Node) {
		statement, ok := node.(*ast.SwitchStmt)
		if !ok || statement.Tag == nil {
			return
		}
		named, ok := vocabularyOf(pass, statement.Tag)
		if !ok {
			return
		}
		words := wordsOf(named)
		if len(words) < minimumVocabulary {
			return
		}
		missing := missingWords(pass, statement, words)
		if len(missing) == 0 {
			return
		}
		switch reason := exemption(pass, statement); reason {
		case exemptionAbsent:
		case exemptionUnexplained:
			pass.Reportf(statement.Switch,
				"//exhaustive:total here says nothing about why; a reader needs the argument"+
					" for why %s may go unnamed, not the fact that somebody decided it could",
				strings.Join(missing, ", "))
			return
		default:
			return
		}
		pass.Reportf(statement.Switch,
			"switch on %s does not name %s; a vocabulary this repository declares is closed,"+
				" and a default is what turns growing it from a compile error into a run-time one",
			named.Obj().Name(), strings.Join(missing, ", "))
	})
	return nil, nil
}

func vocabularyOf(pass *analysis.Pass, tag ast.Expr) (*types.Named, bool) {
	tagType := pass.TypesInfo.TypeOf(tag)
	if tagType == nil {
		return nil, false
	}
	named, ok := tagType.(*types.Named)
	if !ok {
		return nil, false
	}
	object := named.Obj()
	if object == nil || object.Pkg() == nil {
		return nil, false
	}
	path := object.Pkg().Path()
	if path != modulePrefix && !strings.HasPrefix(path, modulePrefix+"/") {
		return nil, false
	}
	return named, true
}

func wordsOf(named *types.Named) map[string]constant.Value {
	scope := named.Obj().Pkg().Scope()
	words := make(map[string]constant.Value)
	for _, name := range scope.Names() {
		declared, ok := scope.Lookup(name).(*types.Const)
		if !ok || declared.Type() != named.Obj().Type() {
			continue
		}
		words[name] = declared.Val()
	}
	return words
}

func missingWords(pass *analysis.Pass, statement *ast.SwitchStmt, words map[string]constant.Value) []string {
	covered := make(map[string]bool, len(words))
	for _, clause := range statement.Body.List {
		caseClause, ok := clause.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expression := range caseClause.List {
			value := pass.TypesInfo.Types[expression].Value
			if value == nil {
				continue
			}
			for name, word := range words {
				if constant.Compare(value, gotoken.EQL, word) {
					covered[name] = true
				}
			}
		}
	}
	var missing []string
	for name := range words {
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

const (
	exemptionAbsent      = ""
	exemptionUnexplained = "\x00"
)

const directive = "//exhaustive:total"

func exemption(pass *analysis.Pass, statement *ast.SwitchStmt) string {
	group := commentAbove(pass, statement)
	if group == nil {
		return exemptionAbsent
	}
	for _, comment := range group.List {
		line := strings.TrimSpace(comment.Text)
		if !strings.HasPrefix(line, directive) {
			continue
		}
		if reason := strings.TrimSpace(strings.TrimPrefix(line, directive)); reason != "" {
			return reason
		}
		return exemptionUnexplained
	}
	return exemptionAbsent
}

func commentAbove(pass *analysis.Pass, statement *ast.SwitchStmt) *ast.CommentGroup {
	position := pass.Fset.Position(statement.Switch)
	for _, file := range pass.Files {
		if pass.Fset.Position(file.Pos()).Filename != position.Filename {
			continue
		}
		for _, group := range file.Comments {
			end := pass.Fset.Position(group.End())
			if end.Line == position.Line-1 || end.Line == position.Line {
				return group
			}
		}
	}
	return nil
}
