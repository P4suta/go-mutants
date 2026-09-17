// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package exhaustive refuses a switch that reads one of this repository's own
// closed vocabularies and does not name every word of it.
//
// It exists because of the failure this repository is most exposed to and least
// able to see. The engine publishes [gomutants.Outcome]; the runner switches on
// it and ends in a `default` that raises an error. Add a seventh outcome and
// every test in both modules still passes — the switch compiles, the default is
// reachable only by a value that does not exist yet — and the failure arrives
// later, at run time, on somebody else's machine. Two agreeing programs, a
// green build, and a defect nothing in either one could have reported.
//
// A `default` therefore does not excuse a missing word. That is the whole point
// rather than a strictness setting: a default is what turns "this vocabulary
// grew" from a compile error into a run-time one, and the compile error is the
// thing worth having.
//
// A switch whose default really is the right answer for every word says so
// above itself:
//
//	//exhaustive:total a colour is a rendering, and an unstyled new outcome is
//	// the right thing to render
//	switch outcome {
//
// The directive is a comment on the switch and not a line in a ledger
// elsewhere, for one reason: it cannot go stale. Delete the switch and the
// exemption goes with it, which is the failure every path-keyed allowlist in
// this repository has had to be taught to catch. It must carry a reason, so
// that switching the check off is a sentence somebody wrote rather than a
// token somebody copied.
//
// Only vocabularies this repository declares are checked. A switch on
// [token.Token] or [reflect.Kind] is somebody else's set, growing on somebody
// else's schedule, and demanding every case of it would make the check
// unusable — which is how a check ends up switched off.
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

// modulePrefix is the import path every package of this repository starts with,
// and so the test for "a vocabulary we are responsible for".
const modulePrefix = "github.com/P4suta/go-mutants"

// minimumVocabulary is how many declared constants a named type needs before a
// switch on it is read as a vocabulary rather than as arithmetic.
//
// Two, because one constant of a type is a sentinel and says nothing about a
// set; two or more is a set somebody enumerated, which is the thing a switch can
// be incomplete about.
const minimumVocabulary = 2

// Analyzer is the check. It is a value, which is the whole reason this is a
// go/analysis pass and not another scan under internal/devgates: the same value
// loads into `go vet -vettool`, into golangci-lint, and into a driver of our
// own, and it carries type information that no text scan can.
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

// vocabularyOf reports the named type a switch reads, when that type is one of
// this repository's own.
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

// wordsOf is every constant of a named type, declared at the top level of the
// package that declares the type.
//
// The package scope rather than the file, because a vocabulary is the package's
// and a const block may sit anywhere in it; and the declaring package rather
// than the switch's, because a constant added next door is not a word of the
// set.
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

// missingWords is the words no case clause names, in declaration-value order so
// that a diagnostic reads the same on every run.
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

// The two answers exemption gives that are not a reason.
const (
	// exemptionAbsent is a switch that claimed nothing.
	exemptionAbsent = ""
	// exemptionUnexplained is the directive with no sentence after it.
	exemptionUnexplained = "\x00"
)

// directive is the marker a switch uses to say its default is total.
const directive = "//exhaustive:total"

// exemption reads the directive above a switch, and reports the reason written
// with it.
//
// The comment has to be the switch's own -- the group immediately above it --
// rather than anywhere in the function. A marker that could sit five statements
// away would exempt whichever switch came next, which is how a reader ends up
// trusting a sentence that was written about something else.
func exemption(pass *analysis.Pass, statement *ast.SwitchStmt) string {
	group := commentAbove(pass, statement)
	if group == nil {
		return exemptionAbsent
	}
	// The raw comment text, one line at a time, and never [ast.CommentGroup.Text].
	// That method drops comment directives -- anything shaped //name:args, which
	// is exactly this marker's shape and exactly why it has that shape: gofmt
	// leaves a directive where it was written and does not reflow it. Reading
	// the group through Text() returned prose with the marker already deleted,
	// so every exemption read as absent and the check reported all of them.
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

// commentAbove is the comment group that ends on the line before a switch,
// found in the file the switch is in.
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
