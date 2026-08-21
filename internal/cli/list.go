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

// The identity of the document `list --json` writes.
//
// Both constants are spelled out here rather than imported from
// internal/schemas, which is where `report validate` reaches for the validator.
// Naming a document type is not the same act as checking one, and this command
// only writes: the integration tests assert that a document carrying these
// values is the document internal/schemas validates, so the two cannot drift
// apart without a test failing.
const (
	catalogDocumentType  = "go-mutants/catalog"
	catalogSchemaVersion = 1
)

// listIDWidth is how many hex characters of a mutant's display id the text
// listing prints.
//
// It is shorter than the display id itself, which is the id the JSON document
// and `--mutant` speak in. Eight characters is what fits a scanning eye and
// what the run console prints for the same mutant, and it is long enough to
// retype into `--mutant`, which resolves against the whole catalogue rather
// than against what happened to be listed.
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

// listOptions holds the flag destinations for one `list` invocation. It is a
// struct rather than closure variables so that the command can be built more
// than once in one process, which is what the tests do.
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

// newListCommand builds the `list` command.
func newListCommand() *cobra.Command {
	o := &listOptions{}
	cmd := &cobra.Command{
		Use:   "list [flags]",
		Short: "List the mutants a run would execute, without executing them",
		Long:  listLong,
		// Positional arguments are accepted here and rejected in execute, so
		// that the rejection can name the flags that narrow a listing instead
		// of cobra reporting "accepts 0 arg(s), received 3".
		Args: cobra.ArbitraryArgs,
		RunE: o.execute,
	}
	flags := cmd.Flags()
	// StringArrayVar, never StringSliceVar: a pattern is a single opaque value.
	// Splitting on commas would make `--include "a,b/**"` mean something the
	// user did not write, and the glob language has no way to escape a comma.
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

// execute is the `list` command's body.
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
	// Reported before anything is discovered, and therefore ahead of every other
	// warning: this one is about what was asked for, and the rest are about what
	// was found. A fixed order is what lets two listings be diffed.
	flags := cmd.Flags()
	warnInertProfile(cmd.ErrOrStderr(), flags.Changed("profile"), flags.Changed("operator"),
		cfg.Mutation.Profile.String(), cfg.Mutation.Operators)

	// Ctrl-C has to reach the pipeline rather than the process. A listing copies
	// a whole workspace and starts a `go list` under it, and the snapshot is
	// only removed by the deferred cleanup inside discoverCatalog — a default
	// signal disposition would kill this process with that directory still on
	// disk, which is exactly the promise `run` makes and this command must keep
	// too.
	ctx, watch, stop := watchSignals(cmd.Context())
	defer stop()

	// Diagnostics go to standard error on every path, not only under --json.
	// A warning that lands in the middle of a listing is a warning that ends up
	// inside somebody's `grep`, and a listing that is clean on one run and has
	// an extra line on the next is not a listing anybody can diff.
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
	return o.writeListing(out, doc)
}

// listOverlay turns the flags the user actually typed into a configuration
// layer.
//
// Only changed flags are carried, exactly as in `run`: a flag's default is not
// an opinion, so `--profile` left alone must lose to `mutation.profile` in the
// file, and pflag's Changed is the only thing that knows the difference.
//
// The profile is the one flag parsed here rather than in the configuration
// layer, because the overlay carries a tier and the command line carries a
// name. Everything else — every glob, every operator name — is validated by
// internal/config against the same rules the file is held to, so a bad value
// is reported as the flag the user typed and not as the TOML key they never
// wrote.
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

// listPrefix checks a `--mutant` value.
//
// The shape is checked and the meaning is not: this is a filter, so a prefix
// that matches nothing is an empty listing rather than an error, and a prefix
// that matches several mutants lists all of them. What is refused is a value
// that could never match anything — the wrong alphabet, or a prefix so short
// that it would name half the catalogue by accident.
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

// isLowerHex reports whether every byte of s is a lowercase hex digit.
func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// A discovered is one discovery pass: what was found, and what it was found
// against.
type discovered struct {
	// result is discovery's own output, candidates and skips alike.
	result discover.Result
	// catalog is the identified, deduplicated set built from result.
	catalog *mutation.Catalog
	// toolchain is the Go toolchain the loader ran with.
	toolchain gocmd.Toolchain
	// workspaceDigest is the snapshot manifest's digest. The snapshot itself is
	// gone by the time this is read; the digest is what names the bytes the ids
	// were minted from.
	workspaceDigest string
}

// discoverCatalog snapshots the workspace and discovers every candidate in it.
//
// The snapshot is created and removed here rather than by the caller, so that
// the deferred cleanup cannot be forgotten and so that nothing downstream can
// hold a path into a directory that is about to disappear. Everything the
// listing needs is read out before the cleanup runs.
func discoverCatalog(ctx context.Context, root string, cfg config.Config, stderr io.Writer) (discovered, error) {
	rules, err := selectRules(cfg)
	if err != nil {
		return discovered{}, err
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

	// `.git`, the caches, and the report directory are what a snapshot never
	// contains, and internal/snapshot decides that on its own. The mutation
	// include and exclude patterns are deliberately not passed here: they select
	// which files are worth mutating, while the snapshot is the whole workspace
	// as the compiler sees it, and a file that is not copied is a file that
	// cannot be loaded or type-checked. internal/engine's pipeline carries the
	// long form of this argument, and the two must not diverge — a `list` that
	// copied a different tree from `run` would mint different ids for the same
	// code, because the workspace digest is taken over the manifest.
	//
	// Neither side rests on this prose. TestListWorkspaceDigestIgnoresTheSelection
	// holds this copy in place and
	// TestMutationExcludeChangesNeitherTheSnapshotNorItsDigest holds the engine's,
	// so threading a selection setting into either Options moves a digest a test
	// is watching.
	snap, err := snapshot.Create(root, snapshot.Options{ReportDir: cfg.Report.Directory})
	if err != nil {
		return discovered{}, err
	}
	defer func() {
		if removeErr := snap.Cleanup(); removeErr != nil {
			// The same condition internal/engine reports under the same code: a
			// snapshot that survived is one fact, not two, however it was made.
			_, _ = fmt.Fprintf(stderr, "warning %s: the snapshot directory could not be removed: %v\n",
				engine.CodeSnapshotNotRemoved, removeErr)
		}
	}()

	result, err := discover.Discover(ctx, discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    toolchain,
		Rules:        rules,
		Include:      include,
		Exclude:      exclude,
	})
	if err != nil {
		return discovered{}, err
	}
	catalog, err := discover.BuildCatalog(result)
	if err != nil {
		return discovered{}, err
	}
	return discovered{
		result:          result,
		catalog:         catalog,
		toolchain:       toolchain,
		workspaceDigest: snap.WorkspaceDigest,
	}, nil
}

// selectRules resolves the configured selection into the rules discovery runs.
//
// The resolution itself is [engine.SelectRules], and it lives there rather than
// here for one reason: `run` has to select exactly what `list` selected. A
// listing that showed a mutant a run would not execute — or the other way round
// — would make the ids it prints unusable with `--mutant`, which is most of
// what a listing is for. This is the local spelling, and the tests that pin the
// tier and family semantics drive it.
func selectRules(cfg config.Config) ([]mutation.Rule, error) {
	return engine.SelectRules(cfg)
}

// operatorRules resolves one `--operator` name to the rules it stands for: a
// family name stands for the whole family, a rule name for itself.
//
// It is [engine.OperatorRules] for the same reason, and it is reached through
// here so that the warning below asks the catalogue exactly the question the
// selection asked it. Answering "what did *this* name select" twice, in two
// places, is how a warning ends up describing a selection nobody made.
func operatorRules(registry *mutation.Registry, name string) ([]mutation.Rule, bool) {
	return engine.OperatorRules(registry, name)
}

// warnUnimplemented reports a selection this pre-release build cannot discover.
//
// Discovery ignores rules it has not implemented yet, which is what lets a whole
// profile be handed to it. That silence is right for a profile and wrong for a
// selection the user typed: `--operator bitwise` would print an empty listing
// and let them conclude their code has no bitwise operators in it.
//
// So a named selection is judged one name at a time. Warning only when the whole
// selection is unimplemented would leave `--operator comparison --operator
// bitwise` silent — the bitwise half dropped without a word, and the listing
// underneath it exactly the wrong conclusion. Each name gets at most one line,
// in the order it was written, so the diagnostic is as diffable as the listing.
//
// The profile path keeps the aggregate form: a tier is not a list of names the
// user chose between, so naming its unimplemented members would be a wall of
// text about a decision they did not make. No tier reaches it today — every one
// of them includes the families this phase implements — and it is kept because
// "the listing is empty and here is why" must not depend on that staying true.
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
		// An unknown name is not this warning's business: selectRules refuses it
		// first, and it is resolved here only to ask what it selects.
		named, ok := operatorRules(registry, name)
		if !ok || len(implementedRules(named)) != 0 {
			continue
		}
		_, _ = fmt.Fprintf(stderr, "warning %s: the operator %s is not discovered by this pre-release build, so nothing it selects is listed; implemented so far: %s\n",
			CodeUnimplementedOperators, strconv.Quote(name), implementedFamilies())
	}
}

// warnInertProfile reports a `--profile` that decided nothing.
//
// A named operator is looked up in the whole catalogue and a profile is a tier,
// so the two do not combine: whenever any operator is named, the profile selects
// nothing at all. That is the documented rule and it is fine when both were
// typed on one command line — the user can see both. It is not fine when the
// operators came from the configuration file and the profile came from a flag,
// because the help text promises that flags override the file and here the file
// wins, silently, over something typed for this invocation.
//
// That is the one case reported, and the predicate says so exactly: the profile
// flag was typed, the operator flag was not, and operators are in effect anyway
// — which they can only be because the file set them, since the built-in
// defaults name none.
//
// It is a warning and not an error. The listing is a real listing of the
// operators the file asked for, and refusing to produce it would make a
// `.go-mutants.toml` in the working directory break a command that used to work.
func warnInertProfile(stderr io.Writer, profileTyped, operatorTyped bool, profile string, operators []string) {
	if !profileTyped || operatorTyped || len(operators) == 0 {
		return
	}
	_, _ = fmt.Fprintf(stderr, "warning %s: --profile %s selected nothing: %s sets mutation.operators = [%s], and a named operator decides the selection whatever the profile says; pass --operator to override the file, or drop mutation.operators from it\n",
		CodeInertProfile, profile, config.FileName, strings.Join(operators, ", "))
}

// implementedRules returns the subset of rules the discovery phase can find
// today.
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

// implementedFamilies names the families discovery implements, for the warning
// that says a selection found none of them.
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

// A catalogDocument is the catalog-v1 document, and the single source both
// renderings read.
//
// The text listing is composed from this and not from the catalogue directly,
// so that `list` and `list --json` can never disagree about which mutants were
// selected or what they are called.
type catalogDocument struct {
	DocumentType  string           `json:"document_type"`
	SchemaVersion int              `json:"schema_version"`
	ToolVersion   string           `json:"tool_version"`
	Workspace     catalogWorkspace `json:"workspace"`
	Selection     catalogSelection `json:"selection"`
	Mutants       []catalogMutant  `json:"mutants"`
	Skips         []catalogSkip    `json:"skips"`
}

// catalogWorkspace names the tree the ids were minted from.
type catalogWorkspace struct {
	ModulePath      string          `json:"module_path"`
	GoVersion       string          `json:"go_version"`
	WorkspaceDigest string          `json:"workspace_digest"`
	Platform        catalogPlatform `json:"platform"`
}

// catalogPlatform is the host this listing was produced on. It is the running
// process's own GOOS and GOARCH: build constraints decide which files a package
// even has, so a catalogue is a statement about one platform.
type catalogPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// catalogSelection is what was asked for, as the user asked for it.
type catalogSelection struct {
	Profile   string   `json:"profile"`
	Operators []string `json:"operators"`
	Include   []string `json:"include"`
	Exclude   []string `json:"exclude"`
}

// A catalogMutant is one listed mutant.
type catalogMutant struct {
	ID          string `json:"id"`
	DisplayID   string `json:"display_id"`
	Path        string `json:"path"`
	Package     string `json:"package"`
	Family      string `json:"family"`
	Rule        string `json:"rule"`
	RuleVersion int    `json:"rule_version"`
	Line        int    `json:"line"`
	Column      int    `json:"column"`
	StartByte   uint32 `json:"start_byte"`
	EndByte     uint32 `json:"end_byte"`
	Original    string `json:"original"`
	Replacement string `json:"replacement"`
}

// A catalogSkip is one recorded reason, aggregated per file by discovery.
type catalogSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// locationKey identifies a candidate by everything the catalogue keeps, which
// is what lets a catalogued mutant be joined back to the coordinates discovery
// found it at.
type locationKey struct {
	path string
	span mutation.Span
	rule string
}

// document builds the catalog-v1 document, keeping only the mutants whose id
// starts with prefix. An empty prefix keeps everything.
func (d discovered) document(cfg config.Config, prefix string) (catalogDocument, error) {
	located := make(map[locationKey]discover.Located, len(d.result.Candidates))
	for _, candidate := range d.result.Candidates {
		key := locationKey{path: candidate.Path, span: candidate.Span, rule: candidate.Rule.Name}
		if _, seen := located[key]; !seen {
			located[key] = candidate
		}
	}

	mutants := make([]catalogMutant, 0, d.catalog.Len())
	for _, m := range d.catalog.Mutants() {
		// The display id is a prefix of the full id, so one comparison answers
		// both spellings the flag accepts.
		if prefix != "" && !strings.HasPrefix(m.ID, prefix) {
			continue
		}
		where, ok := located[locationKey{path: m.Path, span: m.Span, rule: m.Rule.Name}]
		if !ok {
			// Unreachable: every catalogued mutant is one of the candidates the
			// same pass produced. Reported rather than papered over with a zero
			// line number, which would be a coordinate pointing at nothing.
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
		})
	}

	skips := make([]catalogSkip, 0, len(d.result.Skips))
	for _, skip := range d.result.Skips {
		skips = append(skips, catalogSkip{Path: skip.Path, Reason: string(skip.Reason), Count: skip.Count})
	}

	return catalogDocument{
		DocumentType:  catalogDocumentType,
		SchemaVersion: catalogSchemaVersion,
		ToolVersion:   Version,
		Workspace: catalogWorkspace{
			ModulePath:      d.result.ModulePath,
			GoVersion:       goVersion(d.result.GoVersion, d.toolchain.Version.Release),
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

// goVersion picks what the workspace block reports as the Go version.
//
// The module's own `go` directive is the answer whenever there is one: it is
// what decides the language semantics the sources are read with. A module old
// enough to declare none falls back to the toolchain that loaded it, which is
// the next most honest statement available, and "unknown" is the answer when
// even that is missing rather than an empty string that would read as a fact.
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

// stringList returns a non-nil copy of a list.
//
// Every array in the document is a list that may legitimately be empty, and an
// empty list is `[]`, never `null`: nil and empty mean the same thing in a
// resolved configuration, and a consumer should not have to know that.
func stringList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return slices.Clone(values)
}

// writeCatalogJSON writes the document and nothing else.
//
// HTML escaping is off because there is no HTML here: with it on, every `<` in
// a comparison operator would be written as `<`, which is the same string
// to a parser and unreadable to everybody else. Two spaces of indentation and
// the encoder's trailing newline make the output diffable, and the field order
// is the struct's, so two runs over one workspace produce identical bytes.
func writeCatalogJSON(w io.Writer, doc catalogDocument) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(doc)
}

// writeListing writes the text listing, and the skip detail underneath it when
// `--explain` asked for one.
//
// The detail is written after the listing has been flushed rather than into the
// same buffer, so that a listing somebody is reading is on the screen before
// the explanation of what it left out — and so that a failure to write the
// explanation cannot lose the listing.
func (o *listOptions) writeListing(w io.Writer, doc catalogDocument) error {
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
	return explainListing(w, color, doc.Skips)
}

// The listing styles. As in internal/console these are the eight ANSI colours
// rather than a palette of their own, so that the output is legible whatever
// the terminal behind it looks like.
var (
	styleListHeader = lipgloss.NewStyle().Bold(true)
	styleListRule   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleListDetail = lipgloss.NewStyle().Faint(true)
)

// A listRenderer writes one line per mutant, and the counts and the skip
// breakdown underneath.
//
// Nothing here is padded to a width computed from the data. A column that grows
// because one path in the listing is long is a column that shifts every other
// line the day that file is renamed, and this output is meant to be diffed
// between two runs and grepped in a terminal.
type listRenderer struct {
	out   *bufio.Writer
	color bool
	quiet bool
}

// render writes the whole listing.
func (r *listRenderer) render(doc catalogDocument) {
	if !r.quiet {
		r.printf("%s\n", r.paint(styleListHeader, "go-mutants "+doc.ToolVersion+" (list)"))
	}
	files := make([]string, 0, len(doc.Mutants))
	for _, m := range doc.Mutants {
		if !slices.Contains(files, m.Path) {
			files = append(files, m.Path)
		}
		r.printf("%s\n", r.mutantLine(m))
	}
	r.printf("mutants %d  files %d  skips %d\n", len(doc.Mutants), len(files), skipTotal(doc.Skips))
	// The skip breakdown survives --quiet, and so do the counts. Quiet drops
	// what a run is doing and keeps what it found, exactly as the run console
	// does: a suppressed candidate is a finding about the code, not progress.
	for _, reason := range skipsByReason(doc.Skips) {
		r.printf("%s\n", r.paint(styleListDetail, fmt.Sprintf("skip %s %d", reason.reason, reason.count)))
	}
}

// mutantLine renders one mutant as
// "ID8  path:line:col  family/rule  original -> replacement".
func (r *listRenderer) mutantLine(m catalogMutant) string {
	return shortID(m.DisplayID) + "  " +
		m.Path + ":" + strconv.Itoa(m.Line) + ":" + strconv.Itoa(m.Column) + "  " +
		r.paint(styleListRule, m.Family+"/"+m.Rule) + "  " +
		console.FormatText(m.Original) + " -> " + console.FormatText(m.Replacement)
}

// printf appends to the buffer. The write error is deliberately dropped: a
// bufio.Writer remembers the first failure and returns it from Flush, which is
// the one place this renderer reports one.
func (r *listRenderer) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.out, format, args...)
}

// paint applies a style, or does not.
//
// The guard is at the string level rather than inside a configured lipgloss
// renderer, exactly as in internal/console: with colour off no styling code
// runs at all, so the bytes cannot depend on what lipgloss decided about the
// terminal it thinks it is attached to.
func (r *listRenderer) paint(style lipgloss.Style, s string) string {
	if !r.color {
		return s
	}
	return style.Render(s)
}

// shortID truncates a display id to the listing width, and leaves anything
// shorter alone rather than slicing past its end.
func shortID(displayID string) string {
	if len(displayID) <= listIDWidth {
		return displayID
	}
	return displayID[:listIDWidth]
}

// A reasonCount is one skip reason and how many candidates it accounted for
// across every file.
type reasonCount struct {
	reason string
	count  int
}

// skipsByReason aggregates the per-file skips into per-reason totals, sorted by
// reason.
//
// Per-file rows are what the document carries, because that is where a user
// goes to look; per-reason totals are what a listing shows, because a file at a
// time turns "this tree has four constant expressions in it" into forty lines
// nobody reads.
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

// skipTotal is how many candidates were suppressed in all.
func skipTotal(skips []catalogSkip) int {
	total := 0
	for _, skip := range skips {
		total += skip.Count
	}
	return total
}
