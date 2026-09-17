// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"

	"github.com/P4suta/go-mutants/internal/mutation"
)

type loopSite struct {
	declareAt uint32
	testAt    uint32
	Position  string
}

func loopSites(file *ast.File, tok *token.File, srcPath string) []loopSite {
	var out []loopSite
	for _, body := range functionBodies(file) {
		if holdsGoto(body) {
			continue
		}
		out = append(out, loopsDirectlyIn(body, tok, srcPath)...)
	}
	slicesSortByOffset(out)
	return out
}

func functionBodies(file *ast.File) []*ast.BlockStmt {
	var out []*ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			if node.Body != nil {
				out = append(out, node.Body)
			}
		case *ast.FuncLit:
			if node.Body != nil {
				out = append(out, node.Body)
			}
		}
		return true
	})
	return out
}

func holdsGoto(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BranchStmt:
			if node.Tok == token.GOTO {
				found = true
			}
		}
		return true
	})
	return found
}

func loopsDirectlyIn(body *ast.BlockStmt, tok *token.File, srcPath string) []loopSite {
	labels := make(map[ast.Stmt]token.Pos)
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.LabeledStmt:
			if loopBody(node.Stmt) != nil {
				labels[node.Stmt] = node.Pos()
			}
		}
		return true
	})

	var out []loopSite
	ast.Inspect(body, func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		stmt, isStmt := n.(ast.Stmt)
		if !isStmt {
			return true
		}
		block := loopBody(stmt)
		if block == nil || !block.Lbrace.IsValid() {
			return true
		}
		at := stmt.Pos()
		if label, ok := labels[stmt]; ok {
			at = label
		}
		out = append(out, loopSite{
			declareAt: uint32(tok.Offset(at)),
			testAt:    uint32(tok.Offset(block.Lbrace)) + 1,
			Position:  srcPath + ":" + strconv.Itoa(tok.Position(stmt.Pos()).Line),
		})
		return true
	})
	return out
}

func loopBody(s ast.Stmt) *ast.BlockStmt {
	switch loop := s.(type) {
	case *ast.ForStmt:
		return loop.Body
	case *ast.RangeStmt:
		return loop.Body
	}
	return nil
}

func slicesSortByOffset(sites []loopSite) {
	for i := 1; i < len(sites); i++ {
		for j := i; j > 0 && sites[j].declareAt < sites[j-1].declareAt; j-- {
			sites[j], sites[j-1] = sites[j-1], sites[j]
		}
	}
}

func loopSplices(sites []loopSite, alias string, base uint32) []Splice {
	out := make([]Splice, 0, 2*len(sites))
	for i, site := range sites {
		index := base + uint32(i)
		n, k := counterName(alias, index), ceilingName(alias, index)
		declare := insertSplice(site.declareAt, fmt.Sprintf(
			"%s, %s := uint64(0), %s.%s[%d]; ", n, k, alias, runtimeLimit, index))
		test := insertSplice(site.testAt, fmt.Sprintf(
			" if %s++; %s > %s { %s = %s.%s(%d, %s) };", n, n, k, k, alias, runtimeOver, index, n))
		declare.Origin = fmt.Sprintf("the ceiling for the loop at %s", site.Position)
		test.Origin = declare.Origin
		out = append(out, declare, test)
	}
	return out
}

func counterName(alias string, index uint32) string {
	return alias + "_n" + strconv.FormatUint(uint64(index), 10)
}

func ceilingName(alias string, index uint32) string {
	return alias + "_k" + strconv.FormatUint(uint64(index), 10)
}

func insertSplice(at uint32, text string) Splice {
	return Splice{
		Span:        mutation.Span{StartByte: at, EndByte: at},
		Original:    []byte{},
		Replacement: []byte(text),
	}
}
