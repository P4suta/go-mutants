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

	// Env is the complete base environment used by the package loader. Nil
	// inherits the current process environment. Discovery still forces
	// GOWORK=off and prepends the located toolchain's directory to PATH. The
	// field allows a long-lived public workspace to freeze all other build
	// inputs at Open time instead of observing later process-global changes.
	Env []string

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
// byte offset. [Located.Guard] is neither: it is the site hint the
// instrumentation phase consumes, and it is documented as a contract on
// [Guard].
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
	// Guard is the rewrite site the instrumenter has to use for this candidate.
	// Every candidate carries one; a candidate for which no guard form could be
	// determined is not emitted at all, it is a [SkipUnnameableDeclType] skip.
	Guard Guard
}

// A GuardForm names one of the three rewrite shapes instrumentation composes a
// dormant mutant from. The design plan calls them Form S, Form C, and Form D,
// and these are those three and no others.
type GuardForm string

// The three guard forms.
const (
	// GuardFormC is the bool selector:
	//
	//	(__gm.M[3] && (<mutated>) || !(__gm.M[3]) && (<original>))
	//
	// It wraps an expression whose static type is exactly the universe `bool`,
	// so that both branches are ordinary expressions in the site's own context
	// and the compiler settles typing, evaluation order, and short-circuiting.
	// A named boolean type is deliberately not a Form C site: the selector
	// evaluates to `bool`, which is not assignable to `type Flag bool`.
	GuardFormC GuardForm = "C"
	// GuardFormS is the statement guard:
	//
	//	if __gm.M[7] { <mutated statement, flattened> } else { <original bytes> }
	//
	// It is used where the edit is not inside any bool-valued expression. The
	// site is a statement that declares nothing, so wrapping it in a block
	// changes no scope.
	GuardFormS GuardForm = "S"
	// GuardFormD is the declaration rewrite:
	//
	//	var x T; if __gm.M[9] { x = <mutated> } else { x = <original> }
	//
	// It is used where the site is a statement that *does* declare something —
	// `x := e` or `var x = e` — because Form S would bury those declarations
	// inside a block and the code after them would stop compiling. The declared
	// types the rewrite needs are in [Guard.DeclTypes]; discovery computes them
	// because it is the only phase that has the type information.
	GuardFormD GuardForm = "D"
)

// A DeclType is one identifier a Form D site declares, together with the source
// spelling of its type.
//
// Type is what [types.TypeString] produced against a qualifier built from the
// file's own import declarations, so it can be written into that file verbatim.
// Discovery never invents an import to make a type nameable: a type that cannot
// be spelled with what the file already imports makes the whole candidate a
// [SkipUnnameableDeclType] skip instead.
type DeclType struct {
	// Name is the identifier as it is spelled in the declaration.
	Name string
	// Type is the type as it must be written in this file.
	Type string
}

// A Guard is the Form D site hint: the contract between discovery, which has
// the type information, and instrumentation, which has none.
//
// # Why the hint is computed here
//
// Choosing a guard form needs answers only a type checker holds — is this
// expression the universe `bool` or a named boolean type, what type does `x :=
// f()` declare, is this value an `error` — and instrumentation deliberately
// parses the snapshot without type checking it. Handing the decision down as
// data keeps that split: the instrumenter stays a byte rewriter that can be
// tested with no toolchain in the loop, and the phase that already paid for
// go/types answers the questions once.
//
// # How the form is chosen
//
// Walking outward from the edit, in this order:
//
//  1. The nearest enclosing expression whose static type is exactly the
//     universe `bool` — `types.Typ[types.Bool]`, or an untyped bool that
//     materialised as one — and that sits in a position where a parenthesised
//     expression is legal, is a [GuardFormC] site. The search stops at the
//     first ancestor that is not an expression, so it never crosses out of a
//     function literal into the expression the literal sits in.
//  2. Otherwise the nearest enclosing statement, which must be an expression
//     statement, a `return`, an assignment that is not `:=`, an `++`/`--`, a
//     send, a `defer` or a `go` for [GuardFormS], or a `:=` or a `var`
//     declaration for [GuardFormD]. The search stops at the enclosing function,
//     for the same reason.
//
// Anything else is refused, and a refused candidate is never emitted. The
// refusals are all reported as [SkipUnnameableDeclType], which this phase reads
// as "v1's guard forms cannot express this site":
//
//   - the nearest statement is one no form covers — a `switch` tag, a `range`
//     clause, an `if` whose condition is a named boolean type;
//   - the statement sits where a block is not legal Go, which is an `if`,
//     `switch` or `for` initialiser, a `for` post statement, or a type switch
//     guard: `for i := 0; i < n; if __gm.M[3] { … }` does not parse;
//   - a Form D site declares a type that cannot be spelled with the file's own
//     imports;
//   - a `:=` redeclares an existing variable instead of declaring every name on
//     its left afresh. Form D would have to know which names to declare and
//     which to leave alone, so v1 declines the whole site;
//   - an initialiser of a Form D site mentions a name that same site declares.
//     Go begins a declared name's scope at the end of its own specification, so
//     `total := total * 2` and `err := fmt.Errorf("…: %w", err)` read the
//     enclosing declaration; hoisting the new one out in front would rebind
//     them to a zero value and quietly change what the program computes;
//   - a Form D site is a `var` whose declaration tokens cannot be cut without
//     moving a line: a spec with no initialiser, or a spelled-out type, written
//     across more than one line. The whole of the first and the type of the
//     second are what the rewrite removes, and removing a line break moves
//     every line after it.
type Guard struct {
	// Form is the rewrite shape to use.
	Form GuardForm
	// SiteSpan is the byte range the guard replaces: the bool expression for
	// Form C, the statement for Form S and Form D. It always contains the
	// candidate's own span.
	SiteSpan mutation.Span
	// DeclTypes are the identifiers a Form D site declares, in source order,
	// with the type each one must be declared as. It is empty for Form C and
	// Form S, and may be empty for a Form D site whose every name is the blank
	// identifier, which declares nothing.
	DeclTypes []DeclType
}

// A SkipReason names why discovery passed something over. Reasons are part of
// the report format and of `--explain` output, so each string is fixed.
type SkipReason string

// The v1 skip reasons: three that remove a whole file, five that suppress an
// expression because of the context it sits in, and one that refuses a site no
// guard form can express.
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

	// SkipUnnameableDeclType marks a candidate whose rewrite site none of the
	// three guard forms can express. The name comes from the case the design
	// plan called out — a Form D declaration whose type cannot be spelled with
	// the imports the file already has — and it has since become the single
	// reason for every such refusal, because they are one fact for a user:
	// go-mutants knows what it would like to mutate here and cannot say it in
	// Go. [Guard] enumerates them.
	SkipUnnameableDeclType SkipReason = "unnameable-decl-type"
)

// explanations is one sentence per reason, for `--explain`.
//
// They live here rather than in the command line package because they are
// statements about what discovery decided, and a second copy of that prose
// would go stale the first time a reason's meaning was sharpened. They are
// deliberately shorter than the doc comments above: a listing prints one per
// reason, and a paragraph each would bury the counts they annotate.
var explanations = map[SkipReason]string{
	SkipGenerated:          "the file says it is generated, so an edit here would measure the generator's tests and be overwritten by its next run",
	SkipCgo:                "the package imports \"C\", and v1 does not put its rewrites through the cgo preprocessor",
	SkipExcluded:           "mutation.include and mutation.exclude removed the file",
	SkipConstDecl:          "the expression is inside a const declaration, where a constant has to stay constant and one edit can renumber a whole iota block",
	SkipArrayLength:        "the expression is an array length, which is part of a type and is evaluated by the compiler rather than at run time",
	SkipCaseLabel:          "the expression labels a switch case or a select clause, which v1 leaves alone; the bodies underneath them are mutated",
	SkipPackageVarInit:     "the expression initialises a package-level variable, where initialisation order is a global property a per-mutant guard cannot express in v1",
	SkipTypeParam:          "the expression is inside a type parameter list, a constraint, or a type argument, which hold types rather than values",
	SkipUnnameableDeclType: "none of the three guard forms can express a rewrite here, usually a declared type that cannot be spelled with the file's own imports",
}

// Explanation is one sentence saying what a reason means, or "" for a reason
// this build does not define.
//
// The empty answer is for a document rather than for a run: `--explain` reads
// reasons out of a report, which may have been written by another version, and
// a reason nobody here recognises is still a row worth printing with its counts.
func (r SkipReason) Explanation() string { return explanations[r] }

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
		SkipUnnameableDeclType,
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

	loaded, err := load(ctx, root, opts.Toolchain, opts.Env)
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
