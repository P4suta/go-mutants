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

type Options struct {
	SnapshotRoot string

	Toolchain gocmd.Toolchain

	Env []string

	Workspace bool

	WorkspaceRoot string

	PathPrefix string

	Rules []mutation.Rule

	Include []glob.Pattern

	Exclude []glob.Pattern

	Packages []string
}

type Located struct {
	mutation.Candidate

	Line        int
	Column      int
	Package     string
	Guard       Guard
	Branch      *BranchProof
	Termination *TerminationProof
}

type GuardForm string

const (
	GuardFormC      GuardForm = "C"
	GuardFormS      GuardForm = "S"
	GuardFormD      GuardForm = "D"
	GuardFormCPrime GuardForm = "C'"
	GuardFormF      GuardForm = "F"
	GuardFormE      GuardForm = "E"
)

type DeclType struct {
	Name string
	Type string
}

type Guard struct {
	Form      GuardForm
	SiteSpan  mutation.Span
	DeclTypes []DeclType
	SiteType  string

	Imports []Completion

	Probe *ProbeSite
}

type ProbeForm string

const (
	ProbeFormReturn ProbeForm = "return"

	ProbeFormBool ProbeForm = "bool"

	ProbeFormValue ProbeForm = "value"

	ProbeFormReach ProbeForm = "reach"
)

type ProbeSite struct {
	Form    ProbeForm
	Span    mutation.Span
	Types   []string
	Index   int
	Imports []Completion
}

func (r *ProbeSite) at(index int) *ProbeSite {
	if r == nil {
		return nil
	}
	site := *r
	site.Index = index
	return &site
}

type SkipReason string

const (
	SkipGenerated SkipReason = "generated"
	SkipCgo       SkipReason = "cgo"
	SkipExcluded  SkipReason = "excluded"

	SkipConstDecl      SkipReason = "const-decl"
	SkipArrayLength    SkipReason = "array-length"
	SkipPackageVarInit SkipReason = "package-var-init"
	SkipTypeParam      SkipReason = "type-param"

	SkipLabelOrGoto SkipReason = "label-or-goto"

	SkipUnnameableDeclType SkipReason = "unnameable-decl-type"
)

var explanations = map[SkipReason]string{
	SkipGenerated:          "the file says it is generated, so an edit here would measure the generator's tests and be overwritten by its next run",
	SkipCgo:                "the package imports \"C\", and v1 does not put its rewrites through the cgo preprocessor",
	SkipExcluded:           "mutation.include and mutation.exclude removed the file",
	SkipConstDecl:          "the expression is inside a const declaration, where a constant has to stay constant and one edit can renumber a whole iota block",
	SkipArrayLength:        "the expression is an array length, which is part of a type and is evaluated by the compiler rather than at run time",
	SkipPackageVarInit:     "the expression initialises a package-level variable, where initialisation order is a global property a per-mutant guard cannot express in v1",
	SkipTypeParam:          "the expression is inside a type parameter list, a constraint, or a type argument, which hold types rather than values",
	SkipLabelOrGoto:        "the statement is a goto, whose target cannot be moved without jumping over a declaration or into a block, and whose removal would leave a function reaching its closing brace without returning",
	SkipUnnameableDeclType: "no guard form can express a rewrite here, usually a declared type that cannot be spelled with the file's own imports",
}

func (r SkipReason) Explanation() string { return explanations[r] }

func AllSkipReasons() []SkipReason {
	return []SkipReason{
		SkipGenerated,
		SkipCgo,
		SkipExcluded,
		SkipConstDecl,
		SkipArrayLength,
		SkipPackageVarInit,
		SkipTypeParam,
		SkipLabelOrGoto,
		SkipUnnameableDeclType,
	}
}

var reasonRank = func() map[SkipReason]int {
	reasons := AllSkipReasons()
	ranks := make(map[SkipReason]int, len(reasons))
	for rank, reason := range reasons {
		ranks[reason] = rank
	}
	return ranks
}()

type Skip struct {
	Path   string
	Reason SkipReason
	Count  int
}

type SkipSite struct {
	Path   string
	Reason SkipReason
	Line   int
	Column int
	Rule   string
}

type Result struct {
	Candidates    []Located
	Skips         []Skip
	SkipSites     []SkipSite
	ModulePath    string
	GoVersion     string
	SourceDigests map[string]string
}

func Discover(ctx context.Context, opts Options) (Result, error) {
	root, err := resolveRoot(opts.SnapshotRoot)
	if err != nil {
		return Result{}, err
	}
	if opts.WorkspaceRoot == "" || !samePath(opts.WorkspaceRoot, root) {
		if workspaceErr := CheckWorkspace(root); workspaceErr != nil {
			return Result{}, workspaceErr
		}
	}
	matchers, err := newMatchers(opts.Rules)
	if err != nil {
		return Result{}, err
	}

	loaded, err := load(ctx, root, opts.Toolchain, opts.Env, opts.Workspace, opts.Packages)
	if err != nil {
		return Result{}, err
	}
	module, err := mainModule(loaded, root)
	if err != nil {
		return Result{}, err
	}
	moduleRoot := module.Dir
	cgoPackages := findCgoPackages(loaded, moduleRoot)
	if err := gate(loaded, cgoPackages); err != nil {
		return Result{}, err
	}

	d := &discovery{
		root:     moduleRoot,
		matchers: matchers,
		prefix:   opts.PathPrefix,
		include:  opts.Include,
		exclude:  opts.Exclude,
		cgo:      cgoPackages,
		skips:    make(map[skipKey]int),
		seen:     make(map[string]bool),
		digests:  make(map[string]string),
	}
	if err := d.run(ctx, loaded); err != nil {
		return Result{}, err
	}
	return Result{
		Candidates:    d.sortedCandidates(),
		Skips:         d.sortedSkips(),
		SkipSites:     d.sortedSkipSites(),
		ModulePath:    module.Path,
		GoVersion:     module.GoVersion,
		SourceDigests: d.digests,
	}, nil
}

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

type discovery struct {
	root     string
	matchers matchers
	prefix   string
	include  []glob.Pattern
	exclude  []glob.Pattern
	cgo      cgoExemption

	candidates []Located
	skips      map[skipKey]int
	sites      []SkipSite
	seen       map[string]bool
	digests    map[string]string
	siblings   map[string]map[string]string
}

type skipKey struct {
	path   string
	reason SkipReason
}

func (d *discovery) record(path string, reason SkipReason, n int) {
	if n <= 0 {
		return
	}
	d.skips[skipKey{path: path, reason: reason}] += n
}

func (d *discovery) recordSite(path string, reason SkipReason, rule string, line, column int) {
	d.record(path, reason, 1)
	d.sites = append(d.sites, SkipSite{
		Path:   path,
		Reason: reason,
		Line:   line,
		Column: column,
		Rule:   rule,
	})
}

func (d *discovery) recordFile(path string, reason SkipReason) {
	d.recordSite(path, reason, "", 0, 0)
}

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

func (d *discovery) sortedCandidates() []Located {
	out := slices.Clone(d.candidates)
	registry := mutation.CanonicalRegistry()
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

func (d *discovery) sortedSkipSites() []SkipSite {
	out := slices.Clone(d.sites)
	slices.SortFunc(out, compareSkipSites)
	return out
}

func compareSkipSites(x, y SkipSite) int {
	if c := strings.Compare(x.Path, y.Path); c != 0 {
		return c
	}
	if c := x.Line - y.Line; c != 0 {
		return c
	}
	if c := x.Column - y.Column; c != 0 {
		return c
	}
	if c := strings.Compare(string(x.Reason), string(y.Reason)); c != 0 {
		return c
	}
	return strings.Compare(x.Rule, y.Rule)
}

func BuildCatalog(result Result) (*mutation.Catalog, error) {
	return BuildCatalogOf([]Result{result})
}

func BuildCatalogOf(results []Result) (*mutation.Catalog, error) {
	builder := mutation.NewBuilder()
	for _, result := range results {
		for _, located := range result.Candidates {
			if err := builder.Add(located.Candidate); err != nil {
				return nil, &Error{
					Code:    CodeInvalidCandidate,
					Message: "cataloguing " + located.Where(),
					Err:     err,
				}
			}
		}
	}
	return builder.Build()
}

func packagePath(pkg *packages.Package) string {
	path, _, _ := strings.Cut(pkg.PkgPath, " [")
	return path
}
