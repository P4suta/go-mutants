// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"go/types"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"

	"golang.org/x/tools/go/packages"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// generatedMarker is the convention every Go code generator follows:
// https://go.dev/s/generatedcode. The line has to appear before the package
// clause, which is checked separately.
var generatedMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// pkg walks one loaded package.
func (d *discovery) pkg(loaded *loadResult, pkg *packages.Package) error {
	if d.cgo.covers(pkg) {
		d.recordCgoPackage(pkg)
		return nil
	}
	for _, file := range pkg.Syntax {
		if err := d.file(loaded, pkg, file); err != nil {
			return err
		}
	}
	return nil
}

// recordCgoPackage skips every file of a package that imports "C", naming each
// one rather than the package: a skip is something a user looks up by path.
func (d *discovery) recordCgoPackage(pkg *packages.Package) {
	for _, ref := range moduleFiles(pkg, d.root) {
		if d.seen[ref.rel] {
			continue
		}
		d.seen[ref.rel] = true
		if reason, excluded := d.selection(ref.rel); excluded {
			d.record(ref.rel, reason, 1)
			continue
		}
		d.record(ref.rel, SkipCgo, 1)
	}
}

// file walks one syntax tree, or records why it did not.
func (d *discovery) file(loaded *loadResult, pkg *packages.Package, file *ast.File) error {
	tokFile := loaded.fset.File(file.Package)
	if tokFile == nil {
		return nil
	}
	abs := tokFile.Name()
	// A test file is never mutated and never recorded: that is structural,
	// not a decision about this particular file.
	if isTestFile(abs) {
		return nil
	}
	// Anything outside the module root is somebody else's source: a dependency,
	// the standard library, or the cgo preprocessor's output in the build
	// cache. None of it is ours to mutate, and none of it is worth a skip.
	rel, ok := relativePath(d.root, abs)
	if !ok {
		return nil
	}
	if d.seen[rel] {
		return nil
	}
	d.seen[rel] = true

	if reason, excluded := d.selection(rel); excluded {
		d.record(rel, reason, 1)
		return nil
	}
	if isGenerated(file) {
		d.record(rel, SkipGenerated, 1)
		return nil
	}
	if d.matchers.empty() {
		return nil
	}

	src, err := os.ReadFile(abs)
	if err != nil {
		return &Error{Code: CodeFileUnreadable, Message: "cannot read " + strconv.Quote(rel), Err: err}
	}
	if uint64(len(src)) > math.MaxUint32 {
		return &Error{
			Code:    CodeFileUnreadable,
			Message: strconv.Quote(rel) + " is larger than 4 GiB, which mutant spans cannot address",
		}
	}
	scan := &fileScan{
		discovery:    d,
		rel:          rel,
		pkgPath:      packagePath(pkg),
		src:          src,
		digest:       mutation.Digest(src),
		tokFile:      tokFile,
		info:         pkg.TypesInfo,
		suppressions: collectSuppressions(file, pkg.TypesInfo),
	}
	return scan.walk(file)
}

// selection applies the include and exclude patterns to a module-relative
// path. Excludes are applied after includes, so an exclude always wins, and an
// empty include set includes everything.
func (d *discovery) selection(rel string) (SkipReason, bool) {
	if len(d.include) > 0 {
		included := false
		for _, pattern := range d.include {
			if pattern.Match(rel) {
				included = true
				break
			}
		}
		if !included {
			return SkipExcluded, true
		}
	}
	for _, pattern := range d.exclude {
		if pattern.Match(rel) {
			return SkipExcluded, true
		}
	}
	return "", false
}

// isGenerated reports whether a file claims to be generated.
//
// [ast.IsGenerated] answers the same question and answers it correctly, CRLF
// line endings included — go/scanner strips the carriage return from a
// //-comment before the literal is ever stored, so the marker's "DO NOT EDIT."
// suffix matches on a Windows checkout too. The check is spelled out here
// anyway because "generated" is a [SkipReason] this package reports and has to
// keep reporting the same way: both halves of the convention — the anchored
// marker line and the requirement that it precede the package clause — are
// written down in one place under this package's control, rather than tracking
// a standard-library helper whose exact semantics are free to shift.
func isGenerated(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() > file.Package {
			break
		}
		for _, comment := range group.List {
			if comment.Pos() > file.Package {
				break
			}
			if generatedMarker.MatchString(comment.Text) {
				return true
			}
		}
	}
	return false
}

// A suppression is one region of a file discovery refuses to descend into.
type suppression struct {
	start  token.Pos
	end    token.Pos
	reason SkipReason
}

// width is the region's size in bytes, which is how two nested regions are
// ordered.
func (s suppression) width() int { return int(s.end - s.start) }

// collectSuppressions collects every region of a file that cannot hold a mutable
// expression, together with the reason.
//
// Regions are collected rather than enforced during the emitting walk because
// two of them cover only part of a node — an array's length but not its
// element type, a case clause's labels but not its body — and a walk that had
// to remember which child slot it was in would be one `switch` away from
// silently mutating a case label.
func collectSuppressions(file *ast.File, info *types.Info) []suppression {
	var out []suppression
	add := func(from, to token.Pos, reason SkipReason) {
		if from.IsValid() && to.IsValid() && to > from {
			out = append(out, suppression{start: from, end: to, reason: reason})
		}
	}

	// Package-level variable initialisers are read from the declaration list
	// and not from the walk, because a `var` inside a function body is
	// ordinary code that the same syntax node would otherwise catch.
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Values) == 0 {
				continue
			}
			add(value.Values[0].Pos(), value.Values[len(value.Values)-1].End(), SkipPackageVarInit)
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.GenDecl:
			if n.Tok == token.CONST {
				add(n.Pos(), n.End(), SkipConstDecl)
			}
		case *ast.ArrayType:
			if n.Len != nil {
				add(n.Len.Pos(), n.Len.End(), SkipArrayLength)
			}
		case *ast.CaseClause:
			if len(n.List) > 0 {
				add(n.List[0].Pos(), n.List[len(n.List)-1].End(), SkipCaseLabel)
			}
		case *ast.CommClause:
			if n.Comm != nil {
				add(n.Comm.Pos(), n.Comm.End(), SkipCaseLabel)
			}
		case *ast.FuncDecl:
			if n.Type != nil && n.Type.TypeParams != nil {
				add(n.Type.TypeParams.Pos(), n.Type.TypeParams.End(), SkipTypeParam)
			}
		case *ast.TypeSpec:
			if n.TypeParams != nil {
				add(n.TypeParams.Pos(), n.TypeParams.End(), SkipTypeParam)
			}
		case *ast.IndexExpr:
			// A single explicit type argument. The same syntax is an ordinary
			// index expression when the index is a value, which is why this
			// asks the type checker instead of guessing: `m[true]` is runtime
			// code and stays mutable.
			if isTypeExpr(info, n.Index) {
				add(n.Index.Pos(), n.Index.End(), SkipTypeParam)
			}
		case *ast.IndexListExpr:
			for _, index := range n.Indices {
				if isTypeExpr(info, index) {
					add(index.Pos(), index.End(), SkipTypeParam)
				}
			}
		}
		return true
	})

	slices.SortFunc(out, func(x, y suppression) int {
		if x.start != y.start {
			return int(x.start - y.start)
		}
		if x.end != y.end {
			return int(y.end - x.end)
		}
		return reasonRank[x.reason] - reasonRank[y.reason]
	})
	return out
}

// isTypeExpr reports whether an expression denotes a type rather than a value.
func isTypeExpr(info *types.Info, expr ast.Expr) bool {
	if info == nil || expr == nil {
		return false
	}
	tv, ok := info.Types[expr]
	return ok && tv.IsType()
}

// A fileScan is the emitting walk over one file.
type fileScan struct {
	*discovery
	rel          string
	pkgPath      string
	src          []byte
	digest       string
	tokFile      *token.File
	info         *types.Info
	suppressions []suppression
}

// walk emits every candidate in the file, or records why it did not.
func (s *fileScan) walk(file *ast.File) error {
	var failure error
	ast.Inspect(file, func(node ast.Node) bool {
		if failure != nil {
			return false
		}
		switch n := node.(type) {
		case *ast.BinaryExpr:
			matcher, ok := s.matchers.comparison[n.Op]
			if !ok {
				return true
			}
			failure = s.emit(matcher.rule, n.OpPos, matcher.original, matcher.replacement)
		case *ast.Ident:
			matcher, ok := s.matchers.boolean[n.Name]
			if !ok || !s.isUniverseBool(n) {
				return true
			}
			failure = s.emit(matcher.rule, n.Pos(), n.Name, matcher.replacement)
		}
		return failure == nil
	})
	return failure
}

// isUniverseBool reports whether an identifier is the predeclared constant of
// its own name, rather than something shadowing it. `true` is not a keyword in
// Go, and a package that declares its own is entitled to have it left alone.
func (s *fileScan) isUniverseBool(ident *ast.Ident) bool {
	if s.info == nil {
		return false
	}
	obj := s.info.Uses[ident]
	if obj == nil {
		return false
	}
	konst, ok := obj.(*types.Const)
	return ok && konst.Parent() == types.Universe
}

// emit records one candidate, or the reason it was suppressed.
//
// The span invariant is checked here and nowhere else: the bytes the span
// covers in the file on disk must be exactly the text the rule says it is
// replacing. Everything downstream — the identity, the instrumented splice,
// the diff a survivor is displayed as — trusts that, and a mismatch means the
// syntax tree and the file have drifted apart. Failing loudly is the only
// honest answer; splicing a replacement over the wrong bytes is not.
func (s *fileScan) emit(rule mutation.Rule, pos token.Pos, original, replacement string) error {
	if reason, ok := s.suppressed(pos); ok {
		s.record(s.rel, reason, 1)
		return nil
	}
	offset := s.tokFile.Offset(pos)
	end := offset + len(original)
	if offset < 0 || end > len(s.src) {
		return s.spanMismatch(pos, original, "the span reaches past the end of the file")
	}
	span, err := mutation.NewSpan(uint32(offset), uint32(end))
	if err != nil {
		return &Error{Code: CodeSpanMismatch, Message: "in " + s.rel, Err: err}
	}
	covered, err := span.Slice(s.src)
	if err != nil {
		return &Error{Code: CodeSpanMismatch, Message: "in " + s.rel, Err: err}
	}
	if string(covered) != original {
		return s.spanMismatch(pos, original, "the file holds "+strconv.Quote(string(covered)))
	}

	candidate := mutation.Candidate{
		Path:         s.rel,
		Rule:         rule,
		Span:         span,
		Original:     original,
		Replacement:  replacement,
		SourceDigest: s.digest,
	}
	if err := candidate.Validate(); err != nil {
		return &Error{
			Code:    CodeInvalidCandidate,
			Message: "rule " + rule.String() + " proposed an invalid candidate in " + s.rel,
			Err:     err,
		}
	}
	position := s.tokFile.PositionFor(pos, false)
	s.candidates = append(s.candidates, Located{
		Candidate: candidate,
		Line:      position.Line,
		Column:    position.Column,
		Package:   s.pkgPath,
	})
	return nil
}

// spanMismatch builds the internal-invariant error, located the way a user
// would look for it even though only a maintainer should ever see it.
func (s *fileScan) spanMismatch(pos token.Pos, original, detail string) error {
	position := s.tokFile.PositionFor(pos, false)
	return &Error{
		Code: CodeSpanMismatch,
		Message: "internal error: " + s.rel + ":" + strconv.Itoa(position.Line) + ":" +
			strconv.Itoa(position.Column) + " should hold " + strconv.Quote(original) +
			" but " + detail,
	}
}

// suppressed reports the reason a position is off limits, if it is.
//
// The widest containing region wins. That is the region a walker would have
// refused to descend into, so it is the reason that remains true whatever the
// narrower construct inside it turns out to be: a boolean literal inside an
// array length inside a type parameter list is not off limits because array
// lengths are constant, it is off limits because none of a type parameter list
// is value code.
func (s *fileScan) suppressed(pos token.Pos) (SkipReason, bool) {
	best := -1
	for i := range s.suppressions {
		region := s.suppressions[i]
		if pos < region.start || pos >= region.end {
			continue
		}
		if best < 0 || wider(region, s.suppressions[best]) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	return s.suppressions[best].reason, true
}

// wider reports whether x is the outer region of two that both contain a
// position, with a frozen tie-break so that two regions covering exactly the
// same bytes always resolve the same way.
func wider(x, y suppression) bool {
	if x.width() != y.width() {
		return x.width() > y.width()
	}
	if x.start != y.start {
		return x.start < y.start
	}
	return reasonRank[x.reason] < reasonRank[y.reason]
}
