// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/snapshot"
)

const (
	catalogDocumentType  = "go-mutants/catalog"
	catalogSchemaVersion = 1
)

const listIDWidth = 8

const listLong = `List the mutants a run would execute, without executing them.

The workspace root is the current directory, and .go-mutants.toml is read from
there. Flags override the file; the file overrides the built-in defaults.

Discovery runs against a disposable snapshot of the workspace, exactly as ` + "`run`" + `
does, so the coordinates and the ids printed here are the ones a run will use.
Your own tree is only ever read, and no test is built or executed.

--operator names families or rules from the whole v1 catalogue rather than from
the profile: ` + "`--operator bitwise`" + ` lists that family even though the balanced
profile would not select it. The profile decides the selection only when no
operator is named at all, so a --profile that ` + config.FileName + `'s own
mutation.operators has already made inert is reported as a ` + string(CodeInertProfile) + ` warning
rather than quietly ignored.

--mutant is a filter, not a selector. A prefix matching several mutants lists
all of them instead of failing, which is what makes it useful for narrowing a
listing down to one file's worth of ids. The mutant and file counts underneath
describe the filtered listing; the skip breakdown describes the whole discovery
pass, because a suppressed candidate never had an id to filter on.

--json writes the catalog-v1 document to standard output and nothing else;
warnings and errors go to standard error, so the document can be piped straight
into a validator. --explain is the opposite half and the two are refused
together: it expands the skip breakdown underneath the listing, saying what each
reason means and which files it accounted for.`

type listOptions struct {
	include   []string
	exclude   []string
	operators []string
	profile   string
	mutant    string
	json      bool
	explain   bool
	quiet     bool
	noColor   bool
}

func newListCommand() *cobra.Command {
	o := &listOptions{}
	cmd := &cobra.Command{
		Use:   "list [flags]",
		Short: "List the mutants a run would execute, without executing them",
		Long:  listLong,
		Args:  cobra.ArbitraryArgs,
		RunE:  o.execute,
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&o.include, "include", nil,
		"`GLOB` a file must match to be mutated; repeat for more (default: mutation.include, or **/*.go)")
	flags.StringArrayVar(&o.exclude, "exclude", nil,
		"`GLOB` that removes a file again; repeat for more (default: mutation.exclude)")
	flags.StringArrayVar(&o.operators, "operator", nil,
		"`NAME` of an operator family or rule, from the whole catalogue rather than from the profile; repeat for more")
	flags.StringVar(&o.profile, "profile", "",
		"operator tier `NAME`: balanced, strong, or all (default: mutation.profile, or balanced)")
	flags.StringVar(&o.mutant, "mutant", "",
		"list only mutants whose id starts with `ID_PREFIX`; a prefix matching several lists all of them")
	flags.BoolVar(&o.json, "json", false,
		"write the catalog-v1 document to standard output and nothing else")
	flags.BoolVar(&o.explain, "explain", false,
		"after the listing, expand the skip breakdown: what each reason means, and the files it accounted for")
	flags.BoolVarP(&o.quiet, "quiet", "q", false,
		"drop the header line; the mutants, the counts, and the skip breakdown are the listing itself")
	flags.BoolVar(&o.noColor, "no-color", false,
		"never colourise output, even on a terminal")
	return cmd
}

func (o *listOptions) execute(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usagef("list takes no positional arguments; narrow a listing with --include, --operator, or --mutant (got %q)", args[0])
	}
	if o.json && o.quiet {
		return &Error{
			Code: CodeConflictingFlags,
			Message: "--json and --quiet cannot be combined: the catalogue document is the whole of what --json writes, " +
				"and there is no shorter version of it",
			Hint: "drop --quiet for the document, or drop --json for the shortened text listing",
		}
	}
	if err := checkExplain(o.explain, o.json); err != nil {
		return err
	}
	prefix, err := listPrefix(o.mutant)
	if err != nil {
		return err
	}

	root, err := os.Getwd()
	if err != nil {
		return &Error{
			Code:    CodeWorkingDirectory,
			Message: "the current working directory cannot be read, so there is no workspace to list",
			Err:     err,
		}
	}
	overlay, err := listOverlay(cmd, o)
	if err != nil {
		return err
	}
	cfg, err := config.Load(filepath.Join(root, config.FileName), overlay)
	if err != nil {
		return err
	}
	flags := cmd.Flags()
	warnInertProfile(cmd.ErrOrStderr(), flags.Changed("profile"), flags.Changed("operator"),
		cfg.Mutation.Profile.String(), cfg.Mutation.Operators)

	ctx, watch, stop := watchSignals(cmd.Context())
	defer stop()

	found, err := discoverCatalog(ctx, root, cfg, cmd.ErrOrStderr())
	if err != nil {
		return interpret(err, watch.Signal())
	}
	doc, err := found.document(cfg, prefix)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if o.json {
		return writeCatalogJSON(out, doc)
	}
	return o.writeListing(out, doc, found.skipSites())
}

func listOverlay(cmd *cobra.Command, o *listOptions) (config.Overlay, error) {
	flags := cmd.Flags()
	overlay := config.Overlay{
		Include:   config.When(flags.Changed("include"), o.include),
		Exclude:   config.When(flags.Changed("exclude"), o.exclude),
		Operators: config.When(flags.Changed("operator"), o.operators),
	}
	if flags.Changed("profile") {
		tier, err := config.ParseProfile(o.profile)
		if err != nil {
			return config.Overlay{}, err
		}
		overlay.Profile = config.Explicit(tier)
	}
	return overlay, nil
}

func listPrefix(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) < mutation.MinPrefixLength || len(value) > mutation.IDHexLength || !isLowerHex(value) {
		return "", &Error{
			Code: CodeInvalidMutantPrefix,
			Message: fmt.Sprintf("%q is not a mutant id prefix: expected between %d and %d lowercase hex characters",
				value, mutation.MinPrefixLength, mutation.IDHexLength),
			Hint: "copy the id from a listing or from the JSON report; the short form printed in a listing is a prefix of the full one",
		}
	}
	return value, nil
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

type discovered struct {
	modules         []discover.WorkspaceModule
	results         []discover.Result
	workspace       bool
	catalog         *mutation.Catalog
	toolchain       gocmd.Toolchain
	workspaceDigest string
}

func discoverCatalog(ctx context.Context, root string, cfg config.Config, stderr io.Writer) (discovered, error) {
	workspace, err := discover.DetectWorkspace(root)
	if err != nil {
		return discovered{}, err
	}
	rules, selectErr := selectRules(cfg)
	if selectErr != nil {
		return discovered{}, selectErr
	}
	warnUnimplemented(stderr, cfg, rules)
	include, err := discover.CompilePatterns(cfg.Mutation.Include)
	if err != nil {
		return discovered{}, err
	}
	exclude, err := discover.CompilePatterns(cfg.Mutation.Exclude)
	if err != nil {
		return discovered{}, err
	}

	toolchain, err := gocmd.LocateContext(ctx, gocmd.Options{})
	if err != nil {
		return discovered{}, err
	}

	snap, err := snapshot.Create(root, snapshot.Options{ReportDir: cfg.Report.Directory})
	if err != nil {
		return discovered{}, err
	}
	defer func() {
		if removeErr := snap.Cleanup(); removeErr != nil {
			_, _ = fmt.Fprintf(stderr, "warning %s: the snapshot directory could not be removed: %v\n",
				engine.CodeSnapshotNotRemoved, removeErr)
		}
	}()

	opts := discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    toolchain,
		Rules:        rules,
		Include:      include,
		Exclude:      exclude,
		Workspace:    workspace != nil,
	}
	found := discovered{
		workspace:       workspace != nil,
		toolchain:       toolchain,
		workspaceDigest: snap.WorkspaceDigest,
	}
	if workspace == nil {
		result, discoverErr := discover.Discover(ctx, opts)
		if discoverErr != nil {
			return discovered{}, discoverErr
		}
		found.modules = []discover.WorkspaceModule{{Dir: ".", Path: result.ModulePath}}
		found.results = []discover.Result{result}
	} else {
		modules, discoverErr := discover.DiscoverWorkspace(ctx, opts)
		if discoverErr != nil {
			return discovered{}, discoverErr
		}
		for _, module := range modules {
			found.modules = append(found.modules, module.Module)
			found.results = append(found.results, module.Result)
		}
	}
	catalog, catalogErr := discover.BuildCatalogOf(found.results)
	if catalogErr != nil {
		return discovered{}, catalogErr
	}
	found.catalog = catalog
	return found, nil
}

func selectRules(cfg config.Config) ([]mutation.Rule, error) {
	return engine.SelectRules(cfg)
}

func operatorRules(registry *mutation.Registry, name string) ([]mutation.Rule, bool) {
	return engine.OperatorRules(registry, name)
}

func warnUnimplemented(stderr io.Writer, cfg config.Config, rules []mutation.Rule) {
	if len(cfg.Mutation.Operators) == 0 {
		if len(implementedRules(rules)) == 0 {
			_, _ = fmt.Fprintf(stderr, "warning %s: none of the selected operators is discovered by this pre-release build, so the listing is empty; implemented so far: %s\n",
				CodeUnimplementedOperators, implementedFamilies())
		}
		return
	}
	registry := mutation.CanonicalRegistry()
	warned := make(map[string]bool, len(cfg.Mutation.Operators))
	for _, name := range cfg.Mutation.Operators {
		if warned[name] {
			continue
		}
		warned[name] = true
		named, ok := operatorRules(registry, name)
		if !ok || len(implementedRules(named)) != 0 {
			continue
		}
		_, _ = fmt.Fprintf(stderr, "warning %s: the operator %s is not discovered by this pre-release build, so nothing it selects is listed; implemented so far: %s\n",
			CodeUnimplementedOperators, strconv.Quote(name), implementedFamilies())
	}
}

func warnInertProfile(stderr io.Writer, profileTyped, operatorTyped bool, profile string, operators []string) {
	if !profileTyped || operatorTyped || len(operators) == 0 {
		return
	}
	_, _ = fmt.Fprintf(stderr, "warning %s: --profile %s selected nothing: %s sets mutation.operators = [%s], and a named operator decides the selection whatever the profile says; pass --operator to override the file, or drop mutation.operators from it\n",
		CodeInertProfile, profile, config.FileName, strings.Join(operators, ", "))
}

func implementedRules(rules []mutation.Rule) []mutation.Rule {
	supported := discover.SupportedRules()
	out := make([]mutation.Rule, 0, len(rules))
	for _, rule := range rules {
		if slices.ContainsFunc(supported, func(s mutation.Rule) bool { return s.Name == rule.Name }) {
			out = append(out, rule)
		}
	}
	return out
}

func implementedFamilies() string {
	var families []string
	for _, rule := range discover.SupportedRules() {
		if !slices.Contains(families, string(rule.Family)) {
			families = append(families, string(rule.Family))
		}
	}
	slices.Sort(families)
	return strings.Join(families, ", ")
}

type catalogDocument struct {
	DocumentType  string           `json:"document_type"`
	SchemaVersion int              `json:"schema_version"`
	ToolVersion   string           `json:"tool_version"`
	Workspace     catalogWorkspace `json:"workspace"`
	Selection     catalogSelection `json:"selection"`
	Mutants       []catalogMutant  `json:"mutants"`
	Skips         []catalogSkip    `json:"skips"`
}

type catalogWorkspace struct {
	ModulePath      string          `json:"module_path,omitempty"`
	Modules         []catalogModule `json:"modules,omitempty"`
	GoVersion       string          `json:"go_version"`
	WorkspaceDigest string          `json:"workspace_digest"`
	Platform        catalogPlatform `json:"platform"`
}

type catalogModule struct {
	Dir        string `json:"dir"`
	ModulePath string `json:"module_path"`
}

type catalogPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type catalogSelection struct {
	Profile   string   `json:"profile"`
	Operators []string `json:"operators"`
	Include   []string `json:"include"`
	Exclude   []string `json:"exclude"`
}

type catalogMutant struct {
	ID          string              `json:"id"`
	DisplayID   string              `json:"display_id"`
	Path        string              `json:"path"`
	ModulePath  string              `json:"module_path,omitempty"`
	Package     string              `json:"package"`
	Family      string              `json:"family"`
	Rule        string              `json:"rule"`
	RuleVersion int                 `json:"rule_version"`
	Line        int                 `json:"line"`
	Column      int                 `json:"column"`
	StartByte   uint32              `json:"start_byte"`
	EndByte     uint32              `json:"end_byte"`
	Original    string              `json:"original"`
	Replacement string              `json:"replacement"`
	Branch      *catalogBranch      `json:"branch,omitempty"`
	Termination *catalogTermination `json:"termination,omitempty"`
}

type catalogTermination struct {
	Verdict    string `json:"verdict"`
	Reason     string `json:"reason"`
	LoopLine   int    `json:"loop_line"`
	LoopColumn int    `json:"loop_column"`
}

func catalogTerminationOf(proof *discover.TerminationProof) *catalogTermination {
	if proof == nil {
		return nil
	}
	return &catalogTermination{
		Verdict:    proof.Verdict,
		Reason:     proof.Reason,
		LoopLine:   proof.LoopLine,
		LoopColumn: proof.LoopColumn,
	}
}

type catalogBranch struct {
	Direction       string `json:"direction"`
	BodyStartLine   int    `json:"body_start_line"`
	BodyStartColumn int    `json:"body_start_column"`
	BodyEndLine     int    `json:"body_end_line"`
	BodyEndColumn   int    `json:"body_end_column"`
}

func catalogBranchOf(proof *discover.BranchProof) *catalogBranch {
	if proof == nil {
		return nil
	}
	return &catalogBranch{
		Direction:       proof.Direction,
		BodyStartLine:   proof.BodyStartLine,
		BodyStartColumn: proof.BodyStartColumn,
		BodyEndLine:     proof.BodyEndLine,
		BodyEndColumn:   proof.BodyEndColumn,
	}
}

type catalogSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type locationKey struct {
	module string
	path   string
	span   mutation.Span
	rule   string
}

func (d discovered) document(cfg config.Config, prefix string) (catalogDocument, error) {
	candidates := d.candidates()
	located := make(map[locationKey]discover.Located, len(candidates))
	for _, candidate := range candidates {
		key := locationKey{
			module: candidate.ModulePath,
			path:   candidate.Path,
			span:   candidate.Span,
			rule:   candidate.Rule.Name,
		}
		if _, seen := located[key]; !seen {
			located[key] = candidate
		}
	}

	mutants := make([]catalogMutant, 0, d.catalog.Len())
	for _, m := range d.catalog.Mutants() {
		if prefix != "" && !strings.HasPrefix(m.ID, prefix) {
			continue
		}
		where, ok := located[locationKey{
			module: m.ModulePath,
			path:   m.Path,
			span:   m.Span,
			rule:   m.Rule.Name,
		}]
		if !ok {
			return catalogDocument{}, &Error{
				Code: CodeCatalogMismatch,
				Message: fmt.Sprintf("internal error: mutant %s (%s at %s %s) is not one of the candidates discovery reported",
					m.DisplayID, m.Rule.Name, m.Path, m.Span),
			}
		}
		mutants = append(mutants, catalogMutant{
			ID:          m.ID,
			DisplayID:   m.DisplayID,
			Path:        m.Path,
			ModulePath:  m.ModulePath,
			Package:     where.Package,
			Family:      string(m.Rule.Family),
			Rule:        m.Rule.Name,
			RuleVersion: m.Rule.Version,
			Line:        where.Line,
			Column:      where.Column,
			StartByte:   m.Span.StartByte,
			EndByte:     m.Span.EndByte,
			Original:    m.Original,
			Replacement: m.Replacement,
			Branch:      catalogBranchOf(where.Branch),
			Termination: catalogTerminationOf(where.Termination),
		})
	}

	skips := make([]catalogSkip, 0, len(d.skips()))
	for _, skip := range d.skips() {
		skips = append(skips, catalogSkip{Path: skip.Path, Reason: string(skip.Reason), Count: skip.Count})
	}

	return catalogDocument{
		DocumentType:  catalogDocumentType,
		SchemaVersion: catalogSchemaVersion,
		ToolVersion:   Version,
		Workspace: catalogWorkspace{
			ModulePath:      d.modulePath(),
			Modules:         d.catalogModules(),
			GoVersion:       goVersion(d.goVersion(), d.toolchain.Version.Release),
			WorkspaceDigest: d.workspaceDigest,
			Platform:        catalogPlatform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		},
		Selection: catalogSelection{
			Profile:   cfg.Mutation.Profile.String(),
			Operators: stringList(cfg.Mutation.Operators),
			Include:   stringList(cfg.Mutation.Include),
			Exclude:   stringList(cfg.Mutation.Exclude),
		},
		Mutants: mutants,
		Skips:   skips,
	}, nil
}

func goVersion(module, toolchain string) string {
	switch {
	case module != "":
		return module
	case toolchain != "":
		return toolchain
	default:
		return "unknown"
	}
}

func stringList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return slices.Clone(values)
}

func writeCatalogJSON(w io.Writer, doc catalogDocument) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(doc)
}

func (o *listOptions) writeListing(w io.Writer, doc catalogDocument, sites []discover.SkipSite) error {
	color := console.ColorEnabled(w, o.noColor)
	r := &listRenderer{
		out:   bufio.NewWriter(w),
		color: color,
		quiet: o.quiet,
	}
	r.render(doc)
	if err := r.out.Flush(); err != nil {
		return err
	}
	if !o.explain {
		return nil
	}
	return explainListing(w, color, doc.Skips, sites)
}

var (
	styleListHeader = lipgloss.NewStyle().Bold(true)
	styleListRule   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleListDetail = lipgloss.NewStyle().Faint(true)
)

type listRenderer struct {
	out   *bufio.Writer
	color bool
	quiet bool
}

func (r *listRenderer) render(doc catalogDocument) {
	if !r.quiet {
		r.printf("%s\n", r.paint(styleListHeader, "go-mutants "+doc.ToolVersion+" (list)"))
	}
	dirs := make(map[string]string, len(doc.Workspace.Modules))
	for _, module := range doc.Workspace.Modules {
		dirs[module.ModulePath] = module.Dir
	}
	files := make([]string, 0, len(doc.Mutants))
	for _, m := range doc.Mutants {
		where := engine.WorkspaceLocation(dirs[m.ModulePath], m.Path)
		if !slices.Contains(files, where) {
			files = append(files, where)
		}
		r.printf("%s\n", r.mutantLine(m, where))
	}
	r.printf("mutants %d  files %d  skips %d\n", len(doc.Mutants), len(files), skipTotal(doc.Skips))
	for _, reason := range skipsByReason(doc.Skips) {
		r.printf("%s\n", r.paint(styleListDetail, fmt.Sprintf("skip %s %d", reason.reason, reason.count)))
	}
}

func (r *listRenderer) mutantLine(m catalogMutant, where string) string {
	return shortID(m.DisplayID) + "  " +
		where + ":" + strconv.Itoa(m.Line) + ":" + strconv.Itoa(m.Column) + "  " +
		r.paint(styleListRule, m.Family+"/"+m.Rule) + "  " +
		console.FormatText(m.Original) + " -> " + console.FormatText(m.Replacement)
}

func (r *listRenderer) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.out, format, args...)
}

func (r *listRenderer) paint(style lipgloss.Style, s string) string {
	if !r.color {
		return s
	}
	return style.Render(s)
}

func shortID(displayID string) string {
	if len(displayID) <= listIDWidth {
		return displayID
	}
	return displayID[:listIDWidth]
}

type reasonCount struct {
	reason string
	count  int
}

func skipsByReason(skips []catalogSkip) []reasonCount {
	totals := make(map[string]int, len(skips))
	for _, skip := range skips {
		totals[skip.Reason] += skip.Count
	}
	out := make([]reasonCount, 0, len(totals))
	for reason, count := range totals {
		out = append(out, reasonCount{reason: reason, count: count})
	}
	slices.SortFunc(out, func(x, y reasonCount) int { return strings.Compare(x.reason, y.reason) })
	return out
}

func skipTotal(skips []catalogSkip) int {
	total := 0
	for _, skip := range skips {
		total += skip.Count
	}
	return total
}

func (d discovered) candidates() []discover.Located {
	if len(d.results) == 1 {
		return d.results[0].Candidates
	}
	var all []discover.Located
	for _, result := range d.results {
		all = append(all, result.Candidates...)
	}
	return all
}

func (d discovered) skips() []discover.Skip {
	if len(d.results) == 1 {
		return d.results[0].Skips
	}
	var all []discover.Skip
	for _, result := range d.results {
		all = append(all, result.Skips...)
	}
	return all
}

func (d discovered) skipSites() []discover.SkipSite {
	if len(d.results) == 1 {
		return d.results[0].SkipSites
	}
	var all []discover.SkipSite
	for _, result := range d.results {
		all = append(all, result.SkipSites...)
	}
	return all
}

func (d discovered) modulePath() string {
	if d.workspace || len(d.modules) != 1 {
		return ""
	}
	return d.modules[0].Path
}

func (d discovered) catalogModules() []catalogModule {
	if !d.workspace {
		return nil
	}
	modules := make([]catalogModule, 0, len(d.modules))
	for _, module := range d.modules {
		modules = append(modules, catalogModule{Dir: module.Dir, ModulePath: module.Path})
	}
	return modules
}

func (d discovered) goVersion() string {
	if len(d.results) == 0 {
		return ""
	}
	return d.results[0].GoVersion
}
