// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/P4suta/go-mutants/internal/glob"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// WorkspaceFile is the name of the file whose presence at the snapshot root
// makes a tree a multi-module workspace, which v1 refuses.
const WorkspaceFile = "go.work"

// Options configures [Discover].
//
// The zero value is not usable: [Options.SnapshotRoot] has no sensible
// default, since discovery must never be pointed at the user's own tree by
// accident.
type Options struct {
	// SnapshotRoot is the absolute or relative path of the module root to
	// discover in. It is the snapshot, never the user's working tree: the
	// digests candidates carry are of the bytes found here.
	SnapshotRoot string

	// Toolchain is the located Go toolchain. Its directory is prepended to the
	// child environment's PATH; see the package documentation for what that
	// does and does not achieve.
	Toolchain gocmd.Toolchain

	// Rules selects the operators to apply. Empty means every rule this phase
	// implements, which is what [SupportedRules] returns. Rules the canonical
	// registry does not know are an error; rules it knows but this phase has
	// not implemented yet are ignored, so a caller may pass a whole profile's
	// selection without tracking which families have landed.
	Rules []mutation.Rule

	// Include lists the patterns a file must match to be considered, matched
	// against its '/'-normalized module-relative path. Empty includes
	// everything; a file matching none of a non-empty set is recorded as an
	// "excluded" skip.
	Include []glob.Pattern

	// Exclude lists the patterns that remove a file again. Excludes are
	// applied after includes, so an exclude always wins.
	Exclude []glob.Pattern
}

// A Located is one candidate plus where a human would look for it.
//
// The embedded [mutation.Candidate] is the whole truth for identity and
// instrumentation; the line, column, and package are for the console, the
// report, and the GitHub annotations, none of which can do anything with a
// byte offset.
type Located struct {
	mutation.Candidate

	// Line is the 1-based line the candidate's span starts on.
	Line int
	// Column is the 1-based byte offset of the span's start within that line.
	// Bytes, not runes and not display cells: it is what `file:line:col`
	// consumers — editors, `::warning file=`, a jump-to-mutant — expect.
	Column int
	// Package is the import path of the package owning the file, with the
	// " [pkg.test]" suffix of a test variant removed.
	Package string
}

// A SkipReason names why discovery passed something over. Reasons are part of
// the report format and of `--explain` output, so each string is fixed.
type SkipReason string

// The v1 skip reasons: three that remove a whole file, five that suppress an
// expression because of the context it sits in.
const (
	// SkipGenerated marks a file whose leading comments claim it is generated.
	// Mutating generated code measures the generator's test suite, not this
	// project's, and the edit would be overwritten by the next run of it.
	SkipGenerated SkipReason = "generated"
	// SkipCgo marks every file of a package that imports "C". The instrumented
	// build would have to survive the cgo preprocessor, which v1 does not
	// attempt.
	SkipCgo SkipReason = "cgo"
	// SkipExcluded marks a file the include and exclude patterns removed. When
	// more than one whole-file reason applies to a file, this is the one
	// reported: the others are facts about the code, and this one is the
	// user's own decision, which is the answer they are looking for.
	SkipExcluded SkipReason = "excluded"

	// SkipConstDecl marks an expression inside a `const` declaration. A
	// constant must stay constant, and an `iota` block is one edit away from
	// renumbering everything after it.
	SkipConstDecl SkipReason = "const-decl"
	// SkipArrayLength marks an expression inside an array length. It is part of
	// a type, evaluated by the compiler and never at run time.
	SkipArrayLength SkipReason = "array-length"
	// SkipCaseLabel marks an expression in the label list of a `switch` case or
	// in the communication clause of a `select`. Case *bodies* are ordinary
	// code and are mutated; v2 revisits the labels themselves.
	SkipCaseLabel SkipReason = "case-label"
	// SkipPackageVarInit marks an expression in a package-level variable
	// initialiser, `//go:embed` declarations included. Initialisation order is
	// a global property that a per-mutant guard cannot express in v1.
	SkipPackageVarInit SkipReason = "package-var-init"
	// SkipTypeParam marks an expression inside a type parameter list, a
	// constraint, or an explicit type argument. Those positions hold types, not
	// values, however much a constant array length inside one may look like a
	// value.
	SkipTypeParam SkipReason = "type-param"
)

// AllSkipReasons returns every reason discovery can emit, in the declaration
// order of the constants above. The slice is freshly allocated, so a caller
// may sort or filter it without disturbing anyone else.
//
// This list MUST name every [SkipReason] this package emits. It is the
// canonical enumeration the rest of the tree checks itself against: the
// package's own tests parse these sources and fail when a Skip* constant is
// declared without being listed here, and the tests of internal/report check
// the `reason` enumeration of the run report schema against it. A reason
// missing from this list is a reason nothing guards.
func AllSkipReasons() []SkipReason {
	return []SkipReason{
		SkipGenerated,
		SkipCgo,
		SkipExcluded,
		SkipConstDecl,
		SkipArrayLength,
		SkipCaseLabel,
		SkipPackageVarInit,
		SkipTypeParam,
	}
}

// reasonRank is the tie-break order for two suppressed regions that cover
// exactly the same bytes. It is the order of [AllSkipReasons] — the
// declaration order above — frozen so that the same collision resolves the
// same way on every machine, and derived rather than retyped so that a new
// reason cannot arrive without a rank of its own.
var reasonRank = func() map[SkipReason]int {
	reasons := AllSkipReasons()
	ranks := make(map[SkipReason]int, len(reasons))
	for rank, reason := range reasons {
		ranks[reason] = rank
	}
	return ranks
}()

// A Skip is one recorded reason, aggregated per file.
//
// Count means one of two things, depending on the reason, and the distinction
// is worth stating: for a whole-file reason ([SkipGenerated], [SkipCgo],
// [SkipExcluded]) it is 1, because the file was never opened and counting
// candidates in it would mean guessing. For a context reason it is the number
// of candidates that really were suppressed there.
type Skip struct {
	// Path is the '/'-normalized module-relative path of the file.
	Path string
	// Reason is why discovery passed it over.
	Reason SkipReason
	// Count is the number of suppressed candidates, or 1 for a whole file.
	Count int
}

// A Result is everything one discovery pass learned.
type Result struct {
	// Candidates are the proposed edits, in (path, span start, rule registry
	// position) order.
	Candidates []Located
	// Skips are the recorded reasons, in (path, reason) order.
	Skips []Skip
	// ModulePath is the module path of the main module at the snapshot root.
	ModulePath string
	// GoVersion is that module's `go` directive — "1.26", not "go1.26.5". It
	// is deliberately not the toolchain version: the caller passed the
	// toolchain in and already knows that. An empty string means the module
	// declares no `go` directive, which is reported as the empty string rather
	// than filled in from somewhere else.
	GoVersion string
}

// Discover finds every mutation candidate in the snapshot.
//
// The sequence is fixed: refuse what cannot be discovered at all (a bad root,
// a workspace, an unknown rule), load, prove the tree compiles, and only then
// walk syntax. Each step's failure has its own code, so a user never has to
// guess which half of the phase went wrong.
func Discover(ctx context.Context, opts Options) (Result, error) {
	root, err := resolveRoot(opts.SnapshotRoot)
	if err != nil {
		return Result{}, err
	}
	// Only the snapshot's own workspace file is an error. The go command would
	// also find one in a parent directory or through $GOWORK, and neither is
	// part of the snapshot; the loader runs with GOWORK=off so that neither can
	// decide what this run resolves against. See [environment].
	if _, statErr := os.Stat(filepath.Join(root, WorkspaceFile)); statErr == nil {
		return Result{}, &Error{
			Code: CodeWorkspace,
			Message: "multi-module workspaces are not yet supported: " +
				filepath.Join(root, WorkspaceFile) + " makes this a workspace; " +
				"run go-mutants inside one of its modules instead",
		}
	}
	matchers, err := newMatchers(opts.Rules)
	if err != nil {
		return Result{}, err
	}

	loaded, err := load(ctx, root, opts.Toolchain)
	if err != nil {
		return Result{}, err
	}
	module, err := mainModule(loaded, root)
	if err != nil {
		return Result{}, err
	}
	// Every path from here on is measured against the module directory the go
	// command reported, not against the root as it was configured. The two name
	// the same directory — mainModule proved it — but they need not be spelled
	// the same way, and a temporary directory reached through a symlink or a
	// Windows short name is exactly where that bites: relative paths taken
	// against the other spelling would climb out of the module and every file
	// would silently vanish from the catalogue.
	moduleRoot := module.Dir
	cgoPackages := findCgoPackages(loaded, moduleRoot)
	if err := gate(loaded, cgoPackages); err != nil {
		return Result{}, err
	}

	d := &discovery{
		root:     moduleRoot,
		matchers: matchers,
		include:  opts.Include,
		exclude:  opts.Exclude,
		cgo:      cgoPackages,
		skips:    make(map[skipKey]int),
		seen:     make(map[string]bool),
	}
	if err := d.run(ctx, loaded); err != nil {
		return Result{}, err
	}
	return Result{
		Candidates: d.sortedCandidates(),
		Skips:      d.sortedSkips(),
		ModulePath: module.Path,
		GoVersion:  module.GoVersion,
	}, nil
}

// resolveRoot turns the configured root into an absolute directory path.
func resolveRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", &Error{Code: CodeSnapshotRoot, Message: "no snapshot root was given"}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", &Error{
			Code:    CodeSnapshotRoot,
			Message: "cannot resolve the snapshot root " + strconv.Quote(root),
			Err:     err,
		}
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", &Error{
			Code:    CodeSnapshotRoot,
			Message: "cannot read the snapshot root " + strconv.Quote(abs),
			Err:     err,
		}
	}
	if !info.IsDir() {
		return "", &Error{Code: CodeSnapshotRoot, Message: "the snapshot root " + strconv.Quote(abs) + " is not a directory"}
	}
	return abs, nil
}

// discovery is the mutable state of one pass. It exists so that the walk can
// record a skip from anywhere without threading an accumulator through every
// helper.
type discovery struct {
	root     string
	matchers matchers
	include  []glob.Pattern
	exclude  []glob.Pattern
	// cgo names the packages whose files are excluded wholesale.
	cgo cgoExemption

	candidates []Located
	skips      map[skipKey]int
	// seen deduplicates files across the package variants go/packages returns
	// for one directory: a package and its "[pkg.test]" twin share every
	// non-test file.
	seen map[string]bool
}

// skipKey is the aggregation key of [Skip].
type skipKey struct {
	path   string
	reason SkipReason
}

// record adds n to the count of one (path, reason) pair.
func (d *discovery) record(path string, reason SkipReason, n int) {
	if n <= 0 {
		return
	}
	d.skips[skipKey{path: path, reason: reason}] += n
}

// run walks every package the main module owns, in a fixed order.
//
// Cancellation is checked once per package rather than per node: the walk is
// pure computation over syntax that is already in memory, so a package is the
// smallest unit where stopping early buys anything.
func (d *discovery) run(ctx context.Context, loaded *loadResult) error {
	for _, pkg := range loaded.packages {
		if err := ctx.Err(); err != nil {
			return &Error{Code: CodeLoadFailed, Message: "discovery was cancelled", Err: err}
		}
		if err := d.pkg(loaded, pkg); err != nil {
			return err
		}
	}
	return nil
}

// sortedCandidates returns the candidates in catalogue-compatible order:
// path, then span, then registry position. The last key matters only when two
// rules propose an edit at the same span, which this phase's two families
// never do — it is there so that the order does not have to change when a
// family that does lands.
func (d *discovery) sortedCandidates() []Located {
	out := slices.Clone(d.candidates)
	registry := mutation.CanonicalRegistry()
	// Every rule here was verified against this registry by newMatchers, so
	// the lookup cannot miss; the zero from a miss would still leave the
	// comparison total rather than panicking on a future caller's mistake.
	position := func(l Located) int {
		p, _ := registry.Position(l.Rule.Name)
		return p
	}
	slices.SortFunc(out, func(x, y Located) int {
		if c := strings.Compare(x.Path, y.Path); c != 0 {
			return c
		}
		if c := x.Span.Compare(y.Span); c != 0 {
			return c
		}
		if c := position(x) - position(y); c != 0 {
			return c
		}
		return strings.Compare(x.Replacement, y.Replacement)
	})
	return out
}

// sortedSkips flattens the aggregation map into (path, reason) order.
func (d *discovery) sortedSkips() []Skip {
	out := make([]Skip, 0, len(d.skips))
	for key, count := range d.skips {
		out = append(out, Skip{Path: key.path, Reason: key.reason, Count: count})
	}
	slices.SortFunc(out, func(x, y Skip) int {
		if c := strings.Compare(x.Path, y.Path); c != 0 {
			return c
		}
		return strings.Compare(string(x.Reason), string(y.Reason))
	})
	return out
}

// BuildCatalog feeds a result into the catalogue builder.
//
// It is a convenience and nothing more: [mutation.Builder] sorts, deduplicates,
// and indexes on its own, so the ordering [Discover] produces is not load
// bearing here. Rule selection has already happened — it is [Options.Rules] —
// which is why this takes no selection argument.
func BuildCatalog(result Result) (*mutation.Catalog, error) {
	builder := mutation.NewBuilder()
	for _, located := range result.Candidates {
		if err := builder.Add(located.Candidate); err != nil {
			return nil, &Error{Code: CodeInvalidCandidate, Message: "cataloguing " + located.Path, Err: err}
		}
	}
	return builder.Build()
}

// packagePath strips the test-variant decoration go/packages puts on the
// package path of a package compiled for a test binary, so that a candidate
// reports the import path a user would type.
func packagePath(pkg *packages.Package) string {
	path := pkg.PkgPath
	if i := strings.Index(path, " ["); i >= 0 {
		path = path[:i]
	}
	return path
}
