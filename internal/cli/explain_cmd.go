// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"errors"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/trace"
)

// `explain` is the join nothing else made.
//
// Every fact it prints was already written down. The report says a mutant
// survived, which packages cover it, how many passes it took, and what the
// tests were; the recording says which binaries ran, with which arguments, in
// which directory, for how long, and where their output was preserved. Nobody
// had joined the two *per mutant*, so answering "why did this one survive, and
// how do I see it for myself" meant opening two documents and matching a
// sixty-four character identity across them by eye.
//
// Nothing here measures anything, and nothing here guesses. A section whose
// document is missing says so — "no recording, so …" — rather than composing a
// plausible command out of the report alone: a reproduction that does not
// reproduce is worse than no reproduction, because somebody will paste it and
// believe what comes back.
//
// The output is prose rather than a document, and `--json` is refused rather
// than implemented, for the reason `list --explain` refuses it: the two
// documents *are* the machine-readable form, and a third encoding of the same
// facts would be a third thing to keep in step with them.

const explainLong = `Say why one mutant got its verdict, and how to reproduce it.

It reads the run report and, when the run recorded one, the trace beside it,
and joins the two for one mutant: what the mutant is, what happened to it,
which test binaries cover it, every pass this run made over them with the
commands underneath, the stages those passes happened inside, and a command to
paste.

The source is the latest run of this module by default — the one
` + "`go-mutants report latest`" + ` names — or ` + "`--report FILE`" + ` for a document you name,
or ` + "`--run RUN-ID`" + ` for another run in this module's history.

The recording is the one filed under report.directory/trace/<run-id>/, or the
one inside the diagnostics bundle of a run that failed, or ` + "`--trace DIR`" + ` for a
recording you were sent. A run that recorded nothing is not an error: the
sections that would have come out of a recording say there is none instead of
guessing, and the account says how to get one.

The target is a mutant id prefix — as short as a listing prints, as long as the
report carries — resolved against the mutants this run measured and the ones
validation refused. A prefix that names several is refused with all of them
listed, because "why did this one survive" is not a question two mutants can
answer.

The target may instead be a position: a path, optionally with a line, as in
` + "`clamp.go:41`" + `. That runs a discovery pass over this workspace and prints every
mutant there and every site discovery passed over, each with what the report
says became of it — which is "there should be a mutant here, where is it" asked
the other way round.

The command it prints to paste is quoted for a POSIX shell. On Windows it is a
line to read rather than one to paste: the directory, the activation and the
argument vector are all there, and PowerShell spells the first two differently.

--json is refused. Everything printed here is already in the run report and in
the recording, which are the machine-readable forms; a v2 with something to say
that neither document carries may add one.`

// explainOptions holds the flag destinations for one `explain`.
type explainOptions struct {
	report  string
	run     string
	trace   string
	json    bool
	noColor bool
}

// newExplainCommand builds the `explain` command.
func newExplainCommand() *cobra.Command {
	o := &explainOptions{}
	cmd := &cobra.Command{
		Use:   "explain TARGET [flags]",
		Short: "Say why one mutant got its verdict, and how to reproduce it",
		Long:  explainLong,
		// Positional arguments are accepted here and judged in execute, so that
		// the refusal can say what a target is instead of cobra reporting
		// "accepts 1 arg(s), received 0".
		Args: cobra.ArbitraryArgs,
		RunE: o.execute,
	}
	flags := cmd.Flags()
	flags.StringVar(&o.report, "report", "",
		"read the run report at `FILE` instead of this module's latest run")
	flags.StringVar(&o.run, "run", "",
		"read the stored report of `RUN_ID`, or of the one run whose id starts with it, "+
			"instead of this module's latest run")
	flags.StringVar(&o.trace, "trace", "",
		"read the recording in `DIR` instead of the one filed beside the report")
	flags.BoolVar(&o.json, "json", false,
		"refused: the report and the recording are the machine-readable forms")
	flags.BoolVar(&o.noColor, "no-color", false,
		"never colourise output, even on a terminal")
	return cmd
}

// execute is `explain`'s body.
func (o *explainOptions) execute(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return usagef("explain takes exactly one target, as in `go-mutants explain bf513c0d` or " +
			"`go-mutants explain internal/clamp.go:41`")
	}
	if err := o.checkFlags(); err != nil {
		return err
	}

	// The target is read before the document, because which target it is
	// decides whether there has to be one. A mutant is a row of a report and
	// cannot be explained without it; a position is a question about the
	// workspace, and a fresh checkout with no run recorded still has an answer.
	where, isPosition := parsePosition(args[0])
	document, source, err := o.readReport(cmd.OutOrStdout(), isPosition)
	if err != nil {
		return err
	}
	color := console.ColorEnabled(cmd.OutOrStdout(), o.noColor)

	if isPosition {
		return o.explainAt(cmd, document, source, where, color)
	}
	return o.explainMutant(cmd, document, source, args[0], color)
}

// checkFlags refuses the two command lines that have no reading.
//
// Both are worded refusals rather than cobra's flag-group message, for the
// reason `--explain` with `--json` is one: neither flag is wrong on its own, so
// the remedy is to drop one rather than to fix a value, and the reason is worth
// a sentence.
func (o *explainOptions) checkFlags() error {
	if o.json {
		return &Error{
			Code: CodeConflictingFlags,
			Message: "explain has no --json: everything it prints is already in the run report and in the " +
				"recording, which are the machine-readable forms, and a third encoding of the same facts " +
				"would be a third document to keep in step with them",
			Hint: "read `mutants[]` and `rejected[]` out of the report and the `mutant-exec` events out of " +
				"the recording; a v2 with something to say that neither document carries may add a --json",
		}
	}
	if o.report != "" && o.run != "" {
		return &Error{
			Code: CodeConflictingFlags,
			Message: "--report and --run cannot be combined: each names the whole of the document to explain, " +
				"one by its path and one by its run id",
			Hint: "drop --run to explain the file you named, or drop --report to explain a run out of this " +
				"module's history",
		}
	}
	return nil
}

// readReport resolves the document to explain, and the name to print it under.
//
// Three sources and one rule: what the user named wins, and with nothing named
// it is the run `report latest` would print. A module with no runs recorded is
// refused with the code and the words that command uses, because it is the same
// absence — this command's whole output is about one run, and there is none.
func (o *explainOptions) readReport(out io.Writer, optional bool) (*report.Report, string, error) {
	switch {
	case o.report != "":
		document, err := parseReport(o.report)
		return document, o.report, err
	case o.run != "":
		return storedRun(out, o.run)
	default:
		document, source, err := storedRun(out, "")
		if err != nil && optional && absentHistory(err) {
			// A position query does not need a run: the discovery pass answers
			// what is there, and the outcome column has an honest answer for a
			// mutant no document mentions. Only the two absences are tolerated
			// — no module, no run — because a store that cannot be read is a
			// problem worth reporting whatever was asked for.
			return nil, "", nil
		}
		return document, source, err
	}
}

// absentHistory reports whether a failure is one of the two ways there is
// simply nothing recorded here yet.
func absentHistory(err error) bool {
	var coded *Error
	if !errors.As(err, &coded) {
		return false
	}
	return coded.Code == CodeNoStoredRun || coded.Code == CodeNotAModuleRoot
}

// storedRun reads one run out of this module's history, or the newest when
// nothing was named.
//
// A named run is resolved as a *prefix*, the way the mutant target is, and for
// the same reason: a run id is a stamp and four hex characters, nobody retypes
// one, and `20260907T120000Z-a1b2` pasted with its last character missing
// should not be a different question. The three answers are the target's three
// as well — one, none, several — and an ambiguous prefix lists what it matched
// on the way out.
func storedRun(out io.Writer, prefix string) (*report.Report, string, error) {
	found, err := readHistory()
	if err != nil {
		return nil, "", err
	}
	if len(found.runs) == 0 {
		return nil, "", &Error{
			Code:    CodeNoStoredRun,
			Message: "no run is recorded for " + found.module + " in " + found.root,
			Hint:    "run `go-mutants run` here first, or `go-mutants report list` to see what is stored",
		}
	}
	stored := found.runs[0]
	if prefix != "" {
		matches := make([]report.StoredRun, 0, 1)
		for _, run := range found.runs {
			if strings.HasPrefix(run.RunID, prefix) {
				matches = append(matches, run)
			}
		}
		switch len(matches) {
		case 1:
			stored = matches[0]
		case 0:
			return nil, "", &Error{
				Code:    CodeNoStoredRun,
				Message: "no run of " + found.module + " has an id starting with " + strconv.Quote(prefix),
				Hint:    "run `go-mutants report list` to see which runs are stored",
			}
		default:
			if writeErr := writeRunMatches(out, matches); writeErr != nil {
				return nil, "", writeErr
			}
			return nil, "", &Error{
				Code:    CodeNoStoredRun,
				Message: strconv.Quote(prefix) + " matches " + countNoun(len(matches), "recorded run"),
				Hint:    "type more of the run id: every match is listed above",
			}
		}
	}
	document, err := parseReport(stored.Path)
	return document, stored.Path, err
}

// writeRunMatches lists every run a `--run` prefix named, in the columns
// `report list` uses so that the two listings can be read as one.
func writeRunMatches(w io.Writer, matches []report.StoredRun) error {
	var b strings.Builder
	b.WriteString("matched " + countNoun(len(matches), "recorded run") + "\n")
	for _, run := range matches {
		b.WriteString("  " + run.RunID + "  " + formatMoment(run.FinishedAt) +
			"  " + formatScore(run) + "  " + run.Status.String() + "\n")
	}
	return emit(w, b.String())
}

// parseReport reads one document and decodes it.
//
// It is deliberately not [readReport], which validates against the published
// schema first. That check is what `report merge` and `report validate` are
// for: they produce or certify documents. This command only reads one, and a
// document an older release wrote — or one a consumer trimmed on its way into a
// bug report — is still worth explaining as far as it goes.
func parseReport(path string) (*report.Report, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	document, err := report.Parse(data)
	if err != nil {
		return nil, notAReport(path, "read", err)
	}
	return document, nil
}

// A position is a `path[:line]` target: which file, and which line of it when
// the user named one.
type position struct {
	path string
	line int
}

// String renders the position as it was named.
func (p position) String() string {
	if p.line == 0 {
		return p.path
	}
	return p.path + ":" + strconv.Itoa(p.line)
}

// parsePosition decides whether a target names a place in the source rather
// than a mutant, and takes it apart when it does.
//
// The rule has to separate a path from a hex prefix, and the two really can
// look alike: `abcd` is a legal prefix and a legal file name. What settles it is
// that a path here means a path *in the workspace*, so it either carries a
// separator or ends in `.go` — and neither is something a mutant id can
// contain. A bare `clamp` is therefore a malformed prefix rather than a file,
// which is the answer that leaves the user something to fix.
//
// The path is cleaned, so `./clamp.go` and `internal/./x/../clamp.go` are the
// spellings a shell's tab completion produces rather than paths nothing
// matches. Cleaning happens *after* the reading above, because `./abcd` is a
// path and `abcd` is a prefix, and a clean that ran first would turn one into
// the other.
func parsePosition(target string) (position, bool) {
	raw, line := stripCoordinates(target)
	slashed := filepath.ToSlash(raw)
	if slashed == "" || (!strings.Contains(slashed, "/") && !strings.HasSuffix(slashed, ".go")) {
		return position{}, false
	}
	return position{path: path.Clean(slashed), line: line}, true
}

// stripCoordinates lifts a trailing `:line` or `:line:col` off a target and
// returns the path and the line.
//
// Twice at most, from the right, and only over digits — which is what lets the
// spelling `explain` itself prints be pasted back in. `clamp.go:41:7` is the
// identity block's own `position` row, and a reader who selects it is asking
// about line 41; the column is dropped because a mutant is matched by the lines
// its span touches and no finer.
//
// Right to left and bounded is also what keeps a Windows path whole:
// `C:\src\clamp.go` ends in no digits, so the drive letter's colon is never
// read as a coordinate's, and `C:\src\clamp.go:41` gives up after the one it
// really carries.
func stripCoordinates(target string) (string, int) {
	rest, line := target, 0
	for range 2 {
		cut := strings.LastIndexByte(rest, ':')
		if cut <= 0 {
			break
		}
		head, tail := rest[:cut], rest[cut+1:]
		number, err := strconv.Atoi(tail)
		// The round trip refuses "007" and "+7", which are not coordinates
		// anything prints and are more likely to be part of a file name.
		if err != nil || number < 1 || strconv.Itoa(number) != tail {
			break
		}
		rest, line = head, number
	}
	return rest, line
}

// inWorkspace resolves the position against the workspace root and refuses one
// that names nothing there.
//
// An absolute path is what an editor's "copy path" gives, so it is accepted and
// relativised rather than refused — but only when it really is inside the
// workspace, since a mutant's path is module-relative and there is nothing to
// compare an outside file against.
//
// A path that names no file is a refusal rather than an empty account, and that
// is the whole point of the check. Two empty sections and exit 0 read as "there
// is nothing here", which is the one answer that is never true of a file that
// does not exist — and it is checked before the discovery pass, so a typo costs
// a message rather than a copy of the workspace.
func (p position) inWorkspace(root string) (position, error) {
	local := p.path
	if filepath.IsAbs(filepath.FromSlash(local)) {
		relative, err := filepath.Rel(root, filepath.FromSlash(local))
		if err != nil {
			return position{}, p.notInWorkspace("it cannot be resolved against " + root)
		}
		local = filepath.ToSlash(relative)
	}
	if local == ".." || strings.HasPrefix(local, "../") {
		return position{}, p.notInWorkspace("it is outside the workspace at " + root)
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(local)))
	switch {
	case err != nil:
		return position{}, p.notInWorkspace("there is no such file under " + root)
	case info.IsDir():
		return position{}, p.notInWorkspace("it is a directory, and a position names a file")
	}
	p.path = local
	return p, nil
}

// notInWorkspace is the refusal for a path this workspace has no file at.
func (p position) notInWorkspace(why string) error {
	return &Error{
		Code:    CodeUsage,
		Message: strconv.Quote(p.path) + " is not a file in this workspace: " + why,
		Hint:    "name a path relative to the module root, as in `go-mutants explain internal/clamp.go:41`",
	}
}

// holdsSite reports whether a suppressed site is at this position.
//
// A whole-file reason — generated, cgo, excluded — carries line 0, because the
// file was never opened and there is no site in it to name. It answers for
// every line of that file rather than for none: somebody asking about line six
// of a generated file is asking exactly the question that reason answers, and
// comparing 0 to 6 hid the one thing there was to say.
func (p position) holdsSite(path string, line int) bool {
	return path == p.path && (line == 0 || p.line == 0 || p.line == line)
}

// holdsSpan reports whether a mutant is at this position.
//
// The interval is the one `--changed` uses, and for the same reason: a mutant
// covers the lines its original bytes touch, so a multi-line condition mutated
// at its first line is a mutant *on* the third line too — which is exactly what
// somebody asking about the third line wants to know. See [coverage.EndLine].
func (p position) holdsSpan(path string, start, end int) bool {
	return path == p.path && (p.line == 0 || (p.line >= start && p.line <= end))
}

// A subject is the mutant being explained: one this run measured, or one
// validation refused.
//
// The two are different rows of one document and are found by one lookup,
// because a prefix copied out of `run --explain` names a rejection and a prefix
// copied out of a listing names a mutant — and a resolver that knew only about
// the second would answer "no such mutant" for a mutant the same file names
// three lines further down.
type subject struct {
	mutant   *report.Mutant
	rejected *report.Rejected
}

// id is the full activation identity.
func (s subject) id() string {
	if s.mutant != nil {
		return s.mutant.ID
	}
	return s.rejected.ID
}

// displayID is the short form the console and the listing print.
func (s subject) displayID() string {
	if s.mutant != nil {
		return s.mutant.DisplayID
	}
	return s.rejected.DisplayID
}

// location is `path:line:col`.
func (s subject) location() string {
	if s.mutant != nil {
		return s.mutant.Path + ":" + strconv.Itoa(s.mutant.Line) + ":" + strconv.Itoa(s.mutant.Column)
	}
	return s.rejected.Path + ":" + strconv.Itoa(s.rejected.Line) + ":" + strconv.Itoa(s.rejected.Column)
}

// rule is `family/rule`, or the rule alone for a rejection, which is all a
// rejection row carries.
func (s subject) rule() string {
	if s.mutant != nil {
		return s.mutant.Family + "/" + s.mutant.Rule
	}
	return s.rejected.Rule
}

// outcome is the verdict as the document spells it, and "rejected" for a mutant
// that has none because it never existed.
func (s subject) outcome() string {
	if s.mutant != nil {
		return s.mutant.Outcome.String()
	}
	return "rejected"
}

// subjectsOf returns every mutant and rejection in the document, in document
// order, so that one walk answers both.
func subjectsOf(r *report.Report) []subject {
	all := make([]subject, 0, len(r.Mutants)+len(r.Rejected))
	for i := range r.Mutants {
		all = append(all, subject{mutant: &r.Mutants[i]})
	}
	for i := range r.Rejected {
		all = append(all, subject{rejected: &r.Rejected[i]})
	}
	return all
}

// resolveSubject finds the one mutant a prefix names, and returns the matches
// when there is not exactly one.
//
// The shape is checked with `run --mutant`'s own rule and for its own reason: a
// value in the wrong alphabet, or shorter than internal/mutation's minimum,
// could never name a mutant, and saying so is a better answer than an empty
// match list. What it matches is then a question about this document, and the
// two ways that can fail — nothing, or several — are the two `run --mutant` has,
// under the same code.
//
// The comparison is against the full identity alone, because a display id is a
// prefix of it: one comparison answers both spellings the target accepts.
func resolveSubject(r *report.Report, prefix string) (subject, []subject, error) {
	if err := checkMutantPrefix(prefix); err != nil {
		return subject{}, nil, err
	}
	var matches []subject
	for _, candidate := range subjectsOf(r) {
		if strings.HasPrefix(candidate.id(), prefix) {
			matches = append(matches, candidate)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil, nil
	case 0:
		return subject{}, nil, &Error{
			Code: CodeMutantUnresolved,
			Message: strconv.Quote(prefix) + " matches no mutant in run " + r.RunID + ", which measured " +
				countNoun(len(r.Mutants), "mutant") + " and refused " + strconv.Itoa(len(r.Rejected)) + " more",
			Hint: "copy the id from the run's own output, from `go-mutants list`, or from `mutants[]` in the report",
		}
	default:
		return subject{}, matches, &Error{
			Code:    CodeMutantUnresolved,
			Message: strconv.Quote(prefix) + " matches " + countNoun(len(matches), "mutant") + " in run " + r.RunID,
			Hint:    "type more of the id: every match is listed above",
		}
	}
}

// explainMutant is the command's main path: one mutant, from both documents.
func (o *explainOptions) explainMutant(
	cmd *cobra.Command, r *report.Report, source, prefix string, color bool,
) error {
	found, matches, err := resolveSubject(r, prefix)
	if err != nil {
		// The matches are written to standard output before the refusal is
		// returned, because they are the answer to "which did you mean" and the
		// refusal is only the reason there has to be one. They are a listing
		// rather than part of the message for the reason a listing is never
		// folded into an error: one mutant per line is what a reader scans and
		// what a `grep` finds.
		if len(matches) > 0 {
			if writeErr := writeMatches(cmd.OutOrStdout(), color, matches); writeErr != nil {
				return writeErr
			}
		}
		return err
	}

	rec, err := o.openRecording(r.RunID)
	if err != nil {
		return err
	}
	e := newExplainer(cmd.OutOrStdout(), color)
	e.mutantAccount(r, source, found, rec)
	return e.out.Flush()
}

// writeMatches lists every mutant a prefix named.
func writeMatches(w io.Writer, color bool, matches []subject) error {
	e := newExplainer(w, color)
	e.printf("%s\n", e.paint(styleExplainHeader, "matched "+countNoun(len(matches), "mutant")))
	for _, m := range matches {
		e.printf("  %s  %s  %s\n", m.displayID(), m.location(), e.paint(styleListRule, m.outcome()))
	}
	return e.out.Flush()
}

// explainAt is the command's other path: a place in the source rather than an
// identity.
//
// It runs a discovery pass over the workspace the command was typed in, which
// is what `list` does and for the same reason: the coordinates of a site
// discovery passed over exist only inside a pass, and a report carries the
// count per file rather than the positions. The report is still read, so that
// every mutant printed carries what became of it.
func (o *explainOptions) explainAt(
	cmd *cobra.Command, r *report.Report, source string, where position, color bool,
) error {
	root, err := os.Getwd()
	if err != nil {
		return &Error{
			Code:    CodeWorkingDirectory,
			Message: "the current working directory cannot be read, so there is no workspace to look in",
			Err:     err,
		}
	}
	where, err = where.inWorkspace(root)
	if err != nil {
		return err
	}
	cfg, err := config.Load(filepath.Join(root, config.FileName), selectionOverlay(r))
	if err != nil {
		return err
	}
	// Ctrl-C has to reach the pipeline rather than the process, exactly as in
	// `list`: a discovery pass copies the whole workspace, and the copy is only
	// removed by the deferred cleanup inside discoverCatalog.
	ctx, watch, stop := watchSignals(cmd.Context())
	defer stop()
	found, err := discoverCatalog(ctx, root, cfg, cmd.ErrOrStderr())
	if err != nil {
		return interpret(err, watch.Signal())
	}
	doc, err := found.document(cfg, "")
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if err = emit(out, sourceHeader(r, source)); err != nil {
		return err
	}
	return explainPosition(out, color, where, found.result.SkipSites, doc.Mutants, outcomesOf(r))
}

// selectionOverlay rebuilds the run's own selection as a configuration layer,
// so that the discovery pass this form makes catalogues what the run
// catalogued.
//
// Without it the pass reads the workspace's `.go-mutants.toml` and the built-in
// defaults, which is a different question: a run narrowed with `--operator
// comparison` would have every other operator's mutants listed underneath it,
// each reported as "not in this run" — a phrase that reads as a mutant the run
// skipped rather than one the reader's own flags excluded.
//
// The values are the ones the run *resolved*, which is what `selection` in the
// report carries — file, flags and defaults already merged — so they are set as
// explicit layers over the file rather than merged with it again. An empty list
// is left unset: a document an older build wrote, or one whose run configured
// nothing, has nothing to say here, and an explicit empty would be a claim it
// never made.
//
// A profile this build does not know is passed over rather than refused. It can
// only come from a document a later release wrote, and answering "what is at
// this line" with a listing from the default tier is better than refusing to
// answer at all — which is why the pass is configured from the report and never
// gated on it.
func selectionOverlay(r *report.Report) config.Overlay {
	if r == nil {
		return config.Overlay{}
	}
	overlay := config.Overlay{
		Include:   config.When(len(r.Selection.Include) > 0, r.Selection.Include),
		Exclude:   config.When(len(r.Selection.Exclude) > 0, r.Selection.Exclude),
		Operators: config.When(len(r.Selection.Operators) > 0, r.Selection.Operators),
	}
	if tier, err := config.ParseProfile(r.Selection.Profile); err == nil {
		overlay.Profile = config.Explicit(tier)
	}
	return overlay
}

// outcomesOf indexes what the report says became of every mutant and rejection
// it carries, by full identity.
//
// One map for the whole listing rather than a walk per row: a file with two
// hundred candidates in it would otherwise re-scan the document two hundred
// times to fill one column. A nil map is a report that is not there — which is
// a different statement from an empty one, and the outcome column says so.
func outcomesOf(r *report.Report) map[string]string {
	if r == nil {
		return nil
	}
	outcomes := make(map[string]string, len(r.Mutants)+len(r.Rejected))
	for _, candidate := range subjectsOf(r) {
		outcomes[candidate.id()] = candidate.outcome()
	}
	return outcomes
}

// sourceHeader is the two lines an account opens with: which run is being
// explained, and which document says so.
//
// A position query may have neither, and then it opens with nothing rather than
// with two lines about a run that does not exist. Everything under it is a
// statement about the workspace, which is there either way.
func sourceHeader(r *report.Report, source string) string {
	if r == nil {
		return ""
	}
	return "run " + r.RunID + "  " + r.Status.String() + "\nreport " + source + "\n"
}

// explainPosition writes the account of one place in the source: every mutant
// there, and every candidate discovery declined.
//
// Both halves are printed, always, and that is the point of the command in this
// form. Somebody asking about a line is asking "there should be a mutant here",
// and the answer is either "there is, and here is what happened to it" or
// "there is not, and here is the reason discovery gives" — so a listing that
// showed only the first would read as though the second did not exist.
func explainPosition(
	w io.Writer, color bool, where position,
	sites []discover.SkipSite, mutants []catalogMutant, outcomes map[string]string,
) error {
	e := newExplainer(w, color)
	e.printf("position %s\n", where)

	e.section("skip sites")
	rows := 0
	for _, site := range sites {
		if !where.holdsSite(site.Path, site.Line) {
			continue
		}
		rows++
		e.printf("  %s  %s\n", siteLocation(site), e.paint(styleListRule, string(site.Reason)))
	}
	if rows == 0 {
		e.printf("  %s\n", e.paint(styleExplainDetail, "discovery passed nothing over here"))
	}

	e.section("mutants")
	rows = 0
	for _, m := range mutants {
		if !where.holdsSpan(m.Path, m.Line, coverage.EndLine(m.Line, m.Original)) {
			continue
		}
		rows++
		e.printf("  %s  %s:%d:%d  %s  %s -> %s  %s\n",
			m.DisplayID, m.Path, m.Line, m.Column,
			e.paint(styleListRule, m.Family+"/"+m.Rule),
			oneLine(m.Original), oneLine(m.Replacement),
			outcomeIn(outcomes, m.ID))
	}
	if rows == 0 {
		e.printf("  %s\n", e.paint(styleExplainDetail, "this catalogue has no mutant here"))
	}
	return e.out.Flush()
}

// oneLine flattens an edit onto the row it belongs to.
//
// A mutant's original bytes may span lines — the interval [holdsSpan] matches
// on is exactly that case — and a row that broke in the middle would put half
// an edit under the outcome column of the row above it.
func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

// outcomeIn is what the report says became of a mutant, or that it says
// nothing.
//
// A discovery pass and a report can legitimately disagree about which mutants
// exist — the workspace has been edited since the run, or a later build
// catalogues differently — and an identity absent from the document is that,
// said plainly, rather than a blank column a reader would take for an outcome.
// A nil map is the third case: there is no document at all.
func outcomeIn(outcomes map[string]string, id string) string {
	if outcomes == nil {
		return "no report"
	}
	if outcome, ok := outcomes[id]; ok {
		return outcome
	}
	return "not in this run"
}

// A recording is the trace one account read, or nothing.
//
// The zero value is a run that recorded nothing where this command could find
// it, which is not a failure: it is the ordinary case, since `--trace` is
// opt-in. Every method below answers for the zero value, so the renderer asks
// its questions unconditionally and each section says what it can.
type recording struct {
	// stream is the file that was read, and directory the run directory holding
	// it — which is what a preserved output path is relative to.
	stream    string
	directory string
	events    []trace.Event
	// kind is the recorder's owner: [trace.StartKindRun] for a CLI run,
	// [trace.StartKindWorkspace] for a library session, whose reproduction needs
	// the overlay manifest a run's own snapshot does not.
	kind string
	// runID is the run the recording says it is of, and is empty for a library
	// session, which has none. See [recording.describes].
	runID string
}

// present reports whether there is a recording to read.
func (rec *recording) present() bool { return rec != nil && len(rec.events) > 0 }

// describes reports whether this recording is of the run the report describes.
//
// It is asked of the stream's own `run-start` rather than of the directory the
// stream was found in, because a directory name is what `--trace DIR` hands
// over and a run id is content-derived: two runs really can be filed under one
// id, and a colleague's bug report is a directory somebody chose the name of.
// A recording with no run id at all — a library session's — cannot answer, and
// is not accused of being somebody else's.
func (rec *recording) describes(runID string) bool {
	return !rec.present() || rec.runID == "" || rec.runID == runID
}

// openRecording finds the recording of a run, or reports that there is none.
//
// Three places, in one order. `--trace` is a directory somebody named — a
// colleague's bug report, a CI artefact — and one that holds no recording is a
// refusal rather than a silence, because the user asked for that one. The other
// two are where a run of this workspace files them: beside the report under
// `trace/`, and, for a run that failed without being traced, in the diagnostics
// bundle, whose `trace.jsonl` is the ring the run kept in memory.
func (o *explainOptions) openRecording(runID string) (*recording, error) {
	if o.trace != "" {
		return readRecordingAt(o.trace, true)
	}
	dir, cfg, err := workspaceConfig()
	if err != nil {
		// A workspace whose configuration cannot be read is a workspace with no
		// recording to find, and never a reason to refuse to explain a document
		// that was named outright. The report is the claim; the recording is
		// the account beside it.
		return &recording{}, nil
	}
	roots := make([]string, 0, 2)
	if recordings, rootErr := traceRoot(dir, cfg.Report.Directory, traceDefaultDirectory); rootErr == nil {
		roots = append(roots, recordings)
	}
	if bundles, rootErr := diagnosticsRoot(dir, cfg.Report.Directory); rootErr == nil {
		roots = append(roots, bundles)
	}
	for _, root := range roots {
		candidate := filepath.Join(root, runID)
		if _, statErr := os.Stat(filepath.Join(candidate, trace.FileName)); statErr != nil {
			continue
		}
		return readRecordingAt(candidate, false)
	}
	return &recording{}, nil
}

// readRecordingAt reads one recording, whether the run directory or the stream
// inside it was named.
//
// named says the path came from the command line, which is the only difference
// it makes: a directory somebody typed and there is nothing in is a mistake
// worth reporting, and one this command went looking in is an absence.
func readRecordingAt(path string, named bool) (*recording, error) {
	stream := streamPath(path)
	if _, err := os.Stat(stream); err != nil {
		if !named {
			return &recording{}, nil
		}
		return nil, missingRecording(path)
	}
	events, err := trace.Read(stream)
	if err != nil {
		return nil, &Error{
			Code:    CodeUnreadableTrace,
			Message: strconv.Quote(stream) + " is not a recording this build can read",
			Err:     err,
		}
	}
	rec := &recording{stream: stream, directory: filepath.Dir(stream), events: events}
	if len(events) > 0 && events[0].Start != nil {
		rec.kind = events[0].Start.Kind
		rec.runID = events[0].Start.RunID
	}
	return rec, nil
}

// attempts returns the recorded passes at one mutant, in attempt order.
func (rec *recording) attempts(id string) []*trace.MutantRecord {
	if !rec.present() {
		return nil
	}
	var found []*trace.MutantRecord
	for _, event := range rec.events {
		if event.Type == trace.TypeMutantExec && event.Mutant != nil && event.Mutant.ID == id {
			found = append(found, event.Mutant)
		}
	}
	return found
}

// execEvent returns the command recorded at one sequence number.
func (rec *recording) execEvent(seq int64) (trace.Event, bool) {
	if !rec.present() {
		return trace.Event{}, false
	}
	for _, event := range rec.events {
		if event.Seq == seq && event.Type == trace.TypeExec && event.Exec != nil {
			return event, true
		}
	}
	return trace.Event{}, false
}

// snapshotDir is the frozen tree a session compiled and ran in, or "".
func (rec *recording) snapshotDir() string {
	if !rec.present() {
		return ""
	}
	for _, event := range rec.events {
		if event.Type == trace.TypeSnapshot && event.Snapshot != nil &&
			event.Snapshot.Kind == trace.SnapshotKindWorkspace && event.Snapshot.Dir != "" {
			return event.Snapshot.Dir
		}
	}
	return ""
}

// keptTemporaries reports whether the run left its temporary directories on
// disk, which is what decides whether the reproduction can be pasted at all.
//
// It is read off the recording rather than assumed, because the recording knows:
// a run that was asked to keep them records an `artifact` for each, and one that
// was not records none. The three kinds are the three things a reproduction
// needs to still exist — the run's scratch, which holds the test binaries, the
// snapshot it ran in, and a library session's own per-call scratch.
func (rec *recording) keptTemporaries() bool {
	for _, kind := range []string{
		trace.ArtifactKeptScratch, trace.ArtifactKeptSnapshot, trace.ArtifactKeptExecScratch,
	} {
		if rec.artifact(kind) != "" {
			return true
		}
	}
	return false
}

// artifact returns the path of the first artifact of a kind, or "".
func (rec *recording) artifact(kind string) string {
	if !rec.present() {
		return ""
	}
	for _, event := range rec.events {
		if event.Type == trace.TypeArtifact && event.Artifact != nil && event.Artifact.Kind == kind {
			return event.Artifact.Path
		}
	}
	return ""
}

// seqsOf is every sequence number one mutant took part in: its own attempts,
// the commands underneath them, and — for a mutant validation refused — the
// step that refused it and the compile that step read.
//
// The second half is why a rejection gets a timeline at all. It was never
// executed, so it has no passes; what it does have is the bisection that found
// it, which is often where a slow run's minutes went, and the `validate` events
// carrying its id are its share of that search.
func (rec *recording) seqsOf(id string) []int64 {
	if !rec.present() {
		return nil
	}
	var seqs []int64
	for _, event := range rec.events {
		switch {
		case event.Type == trace.TypeMutantExec && event.Mutant != nil && event.Mutant.ID == id:
			seqs = append(seqs, event.Seq)
			seqs = append(seqs, event.Mutant.ExecSeqs...)
		case event.Type == trace.TypeValidate && event.Validate != nil && event.Validate.MutantID == id:
			seqs = append(seqs, event.Seq)
			if event.Validate.ExecSeq != 0 {
				seqs = append(seqs, event.Validate.ExecSeq)
			}
		}
	}
	return seqs
}

// A stageSpan is one step of the run, from the event that opened it to the one
// that closed it — or to the end of the recording, when nothing closed it.
type stageSpan struct {
	phase      string
	name       string
	result     string
	durationMS int64
	start, end int64
	// closed says the recording holds the `stage` event that finished this
	// step. A recording can stop in the middle of one — a ring that wrapped, a
	// bundle written from a run that died, a Ctrl-C — and a step nothing closed
	// has no duration and no result rather than a zero of each.
	closed bool
	// shareMS is how much of the step was this mutant's own: the durations of
	// its recorded passes that fall inside the span.
	shareMS int64
}

// stagesOver returns the stages that were open at any of the given sequence
// numbers, in the order they opened.
//
// "Open at" is the whole of the definition, and it is what makes the section an
// answer to "why was this slow": a mutant's passes happened inside stages, and
// a stage that was running while one of them was recorded is a step the mutant
// took part in. A stage that opened and closed before the mutant was touched is
// somebody else's time.
func (rec *recording) stagesOver(id string, seqs []int64) []stageSpan {
	if !rec.present() || len(seqs) == 0 {
		return nil
	}
	var open, spans []stageSpan
	for _, event := range rec.events {
		if event.Type != trace.TypeStage || event.Stage == nil {
			continue
		}
		if event.Stage.State == trace.StateStarted {
			open = append(open, stageSpan{
				phase: event.Stage.Phase, name: event.Stage.Name,
				start: event.Seq, end: math.MaxInt64,
			})
			continue
		}
		// The innermost open stage of that name is the one this closes: a stage
		// name is unique only within its phase and a run passes through a phase
		// once, so matching the most recent is the only rule that cannot pair
		// two unrelated steps.
		for i := len(open) - 1; i >= 0; i-- {
			if open[i].phase != event.Stage.Phase || open[i].name != event.Stage.Name {
				continue
			}
			span := open[i]
			span.end = event.Seq
			span.result = event.Stage.Result
			span.closed = true
			if event.Stage.DurationMS != nil {
				span.durationMS = *event.Stage.DurationMS
			}
			spans = append(spans, span)
			open = slices.Delete(open, i, i+1)
			break
		}
	}
	// A stage nothing closed still holds everything recorded after it started,
	// which is exactly the case a reader most needs it in: a recording that
	// stops mid-stage is a run that was interrupted or died, and the step it
	// died in is the answer. Its end is the end of the recording.
	spans = append(spans, open...)

	var over []stageSpan
	for _, span := range spans {
		if !slices.ContainsFunc(seqs, func(seq int64) bool { return seq > span.start && seq < span.end }) {
			continue
		}
		span.shareMS = rec.shareOf(id, span)
		over = append(over, span)
	}
	slices.SortFunc(over, func(a, b stageSpan) int { return int(a.start - b.start) })
	return over
}

// shareOf is how much of one step went on this mutant: the durations of its own
// recorded passes inside the span.
//
// It is the number the section is read for. Every mutant of a run takes part in
// the same `mutate/execute` stage, so the stage's own duration is the same
// figure on every account and says nothing about the mutant beside it; the
// share is what distinguishes the mutant that took eleven seconds from the four
// hundred that took three milliseconds each.
func (rec *recording) shareOf(id string, span stageSpan) int64 {
	var total int64
	for _, event := range rec.events {
		if event.Type != trace.TypeMutantExec || event.Mutant == nil || event.Mutant.ID != id {
			continue
		}
		if event.Seq > span.start && event.Seq < span.end {
			total += event.Mutant.DurationMS
		}
	}
	return total
}

// mutantAccount writes the whole account of one mutant.
func (e *explainer) mutantAccount(r *report.Report, source string, s subject, rec *recording) {
	e.printf("%s", sourceHeader(r, source))
	switch {
	case !rec.present():
		e.printf("%s\n", noRecording(r.RunID))
	case !rec.describes(r.RunID):
		e.printf("trace %s\n", rec.stream)
		// Loudly, and before anything derived from it. A run id is
		// content-derived, so two runs can be filed under one; whatever is
		// underneath came out of that recording and is that run's, however
		// convincingly it lines up with this report.
		e.printf("warning: that recording is of run %s, not %s; everything below it "+
			"came out of that run\n", rec.runID, r.RunID)
	default:
		e.printf("trace %s\n", rec.stream)
	}

	e.identity(s)
	e.verdict(r, s)
	if s.rejected != nil {
		// A mutant that does not compile has no coverage and no executions:
		// nothing measured it, because there was nothing to measure. It does
		// have a timeline — the bisection that refused it — which is often
		// where a slow run's minutes went.
		e.timeline(r, s, rec)
		e.rejectedReproduction(s)
		return
	}
	e.coverage(r, s.mutant)
	e.executions(s.mutant, rec)
	e.timeline(r, s, rec)
	e.reproduction(r, s, rec)
}

// noRecording is the one line a run with no recording is reported under, and
// the sentence every trace-derived section below shortens.
func noRecording(runID string) string {
	return "no trace recorded for run " + runID + "; re-run with --trace"
}

// section writes a block heading.
func (e *explainer) section(title string) {
	e.printf("\n%s\n", e.paint(styleExplainHeader, title))
}

// field writes one labelled row of the identity block.
func (e *explainer) field(label, value string) {
	e.printf("  %-10s  %s\n", label, value)
}

// identity is what the mutant is, in the order somebody reads it: the id they
// typed a prefix of, the one activation takes, then where it is and what it
// changes.
func (e *explainer) identity(s subject) {
	e.section("mutant")
	e.field("display id", s.displayID())
	e.field("id", s.id())
	e.field("rule", e.paint(styleListRule, s.rule()))
	e.field("position", s.location())
	if s.mutant != nil {
		e.field("change", s.mutant.Original+" -> "+s.mutant.Replacement)
		e.field("package", s.mutant.Package)
	}
}

// verdict is the outcome in one sentence, and the compiler's own words when
// there is no outcome because there was no mutant.
func (e *explainer) verdict(r *report.Report, s subject) {
	e.section("outcome")
	if s.rejected != nil {
		e.printf("  %s\n", "rejected: the instrumented snapshot would not compile with it spliced in")
		e.printf("%s\n", e.paint(styleExplainDetail, indent(s.rejected.Diagnostic)))
		return
	}
	e.printf("  %s\n", verdictSentence(*s.mutant, r.Test.MemoryBytes))
	if s.mutant.Cached {
		e.printf("  %s\n", e.paint(styleExplainDetail,
			"reused from the outcome cache rather than measured by this run, so the duration, the attempts "+
				"and the killer above are the ones the run that did measure it recorded"))
	}
}

// verdictSentence is one mutant's verdict, with the fact that raises the first
// question about it.
//
// The two outcomes that name a binary name it differently, which is
// internal/console's own distinction kept here word for word: a kill was
// *detected* by a test, and a timeout was detected by nothing — the binary is
// the one the mutant hung, and "killed by" would send a reader looking for an
// assertion that does not exist.
func verdictSentence(m report.Mutant, memoryBound int64) string {
	killedBy := ""
	if m.KilledBy != nil {
		killedBy = *m.KilledBy
	}
	switch m.Outcome {
	case report.OutcomeKilled:
		if killedBy != "" {
			return "killed by " + killedBy + memoryClause(m, memoryBound) +
				" after " + countNoun(m.Attempts, "attempt")
		}
		return "killed" + memoryClause(m, memoryBound) + " after " + countNoun(m.Attempts, "attempt")
	case report.OutcomeTimedOut:
		if killedBy != "" {
			return "timed out, hung in " + killedBy + ", after " + countNoun(m.Attempts, "attempt")
		}
		return "timed out after " + countNoun(m.Attempts, "attempt")
	case report.OutcomeSurvived:
		if m.Uncovered {
			return "survived without being executed: no test binary reaches it"
		}
		return "survived after " + countNoun(m.Attempts, "attempt")
	case report.OutcomeNotRun:
		if m.NotRunReason != nil {
			return "not run: " + *m.NotRunReason
		}
		return "not run, and the document does not say why"
	case report.OutcomeInconclusive, report.OutcomeErrored:
		return m.Outcome.String() + " after " + countNoun(m.Attempts, "attempt")
	default:
		return m.Outcome.String()
	}
}

// memoryClause is what a kill by the memory bound adds to its verdict, and
// nothing at all for every other kill.
//
// It is the one place `explain` has to say something the outcome does not. A
// mutant the bound stopped is reported as `killed` and names the suite that was
// running — and that suite's tests all pass, so a reader who goes and looks
// finds nothing. Both numbers are printed because either alone is unactionable:
// the peak says what the mutant did, the bound says what it was measured
// against, and only the pair says whether to fix the mutant or the budget.
//
// The bound is the run's, from `test.memory_bytes`; a document written before
// that field existed carries none, and the clause then names the peak alone
// rather than inventing a number to compare it with.
func memoryClause(m report.Mutant, memoryBound int64) string {
	// The mutant's own fields rather than its rows, because a *cached* mutant
	// has none: this run started no process for it, and reading the rows would
	// make a warm run's account of a memory kill silently thinner than a cold
	// run's. The document carries the same two facts at both levels for exactly
	// this reader.
	if !m.MemoryExceeded {
		return ""
	}
	peak := m.PeakMemoryBytes
	if peak > 0 && memoryBound > 0 {
		return " (memory: " + console.FormatBytes(peak) + " > " + console.FormatBytes(memoryBound) + ")"
	}
	if peak > 0 {
		return " (memory: " + console.FormatBytes(peak) + ")"
	}
	return " (memory bound reached)"
}

// coverage is which test binaries reach the mutant, or the two other things
// that can be true.
//
// An empty list means two different things depending on the mode, which is why
// the mode is read rather than the length: a run with coverage off asked
// nothing and measured every mutant against every binary, and printing "no test
// binary" for it would be a claim that run never made.
func (e *explainer) coverage(r *report.Report, m *report.Mutant) {
	e.section("coverage")
	switch {
	case r.Coverage.Mode == report.CoverageOff:
		e.printf("  %s\n", "coverage off: this run measured every mutant against every test binary")
	case m.Uncovered:
		e.printf("  no test binary reaches line %d of %s\n", m.Line, m.Path)
	case len(m.CoveringTestPackages) > 0:
		e.printf("  covered by: %s\n", strings.Join(m.CoveringTestPackages, ", "))
	default:
		e.printf("  %s\n", "this run recorded no covering test package for it")
	}
}

// executions is every pass this run made over the test binaries, and — when
// there is a recording — the commands underneath each of them.
//
// The rows come from the report rather than from the recording, because the
// report is the durable claim and the recording is opt-in: a mutant's passes
// are described the same way whether or not anybody asked for a trace, and what
// a trace adds is the argument vectors rather than the passes.
func (e *explainer) executions(m *report.Mutant, rec *recording) {
	e.section("executions")
	if len(m.Executions) == 0 {
		e.printf("  %s\n", e.paint(styleExplainDetail, notExecuted(m)))
		return
	}
	recorded := rec.attempts(m.ID)
	for _, execution := range m.Executions {
		e.printf("  attempt %d  worker %d  %s  %s%s%s\n",
			execution.Attempt, execution.Worker, execution.Outcome,
			console.FormatDuration(milliseconds(execution.DurationMS)),
			peakClause(execution),
			attribution(execution.Outcome, execution.KilledBy))
		if len(execution.Binaries) > 0 {
			e.printf("    binaries: %s\n", strings.Join(execution.Binaries, ", "))
		}
		e.commands(pass(recorded, execution.Attempt), rec)
	}
	if !rec.present() {
		e.printf("  %s\n", e.paint(styleExplainDetail,
			"no recording, so the commands these passes started are not in this account"))
	}
}

// peakClause is what a pass cost the machine, and nothing where the platform
// could not say.
//
// It sits beside the duration because the two are the same kind of fact — what
// one pass spent — and it is printed for every pass rather than only the
// bounded ones: "which of my mutants cost the machine most" is a question about
// a run in which nothing went wrong, and a run that recorded only what it
// bounded could not answer it.
func peakClause(execution report.Execution) string {
	if execution.PeakMemoryBytes <= 0 {
		return ""
	}
	return "  peak " + console.FormatBytes(execution.PeakMemoryBytes)
}

// notExecuted says why a mutant has no rows under its attempt count. There are
// exactly three ways for that to be true, and which one it is decides what to
// do about it.
func notExecuted(m *report.Mutant) string {
	switch {
	case m.Cached:
		return "this run started no process for it: the outcome was reused from the outcome cache"
	case m.Uncovered:
		return "this run started no process for it: no test binary reaches its lines"
	case m.Outcome == report.OutcomeNotRun:
		return "this run started no process for it: it was never selected"
	default:
		return "this document records no executions for it"
	}
}

// attribution is the tail of an execution row: the binary the pass named, in
// the words its outcome earns. See [verdictSentence].
func attribution(outcome report.Outcome, killedBy string) string {
	if killedBy == "" {
		return ""
	}
	if outcome == report.OutcomeTimedOut {
		return "  hung in " + killedBy
	}
	return "  killed by " + killedBy
}

// pass finds the recorded attempt with a given number.
func pass(recorded []*trace.MutantRecord, attempt int) *trace.MutantRecord {
	for _, record := range recorded {
		if record.Attempt == attempt {
			return record
		}
	}
	return nil
}

// commands writes the child processes one recorded pass started.
//
// The line itself is [console.TraceLine], which is the line `run -vv` prints
// for the same event: a user comparing what scrolled past during the run with
// what this says about it afterwards is comparing one rendering with itself.
// What is added under it is what a `-vv` line has no room for — where the
// command ran, and where its output was kept.
func (e *explainer) commands(record *trace.MutantRecord, rec *recording) {
	if record == nil {
		return
	}
	for _, seq := range record.ExecSeqs {
		event, ok := rec.execEvent(seq)
		if !ok {
			e.printf("    %s\n", e.paint(styleExplainDetail,
				"the command recorded at seq "+strconv.FormatInt(seq, 10)+" is not in this recording"))
			continue
		}
		e.printf("    %s\n", console.TraceLine(event))
		if event.Exec.Dir != "" {
			e.printf("      dir: %s\n", event.Exec.Dir)
		}
		e.preservedOutput(event.Exec.OutputPath, rec)
	}
}

// outputTailLines is how much of a preserved output is quoted.
//
// Ten lines is where a Go test failure's own report ends — the `--- FAIL`
// banner, the assertion under it, and the `FAIL` line — and the file is named on
// the line above, so a reader who needs more has the path to open. Quoting a
// megabyte into a terminal would bury the five sections around it.
const outputTailLines = 10

// preservedOutput names the file a command's output was kept in and quotes the
// end of it.
func (e *explainer) preservedOutput(path string, rec *recording) {
	if path == "" {
		return
	}
	full := filepath.Join(rec.directory, filepath.FromSlash(path))
	e.printf("      output: %s\n", full)
	data, err := os.ReadFile(full)
	if err != nil {
		e.printf("        %s\n", e.paint(styleExplainDetail, "it could not be read: "+err.Error()))
		return
	}
	for _, line := range tailLines(string(data), outputTailLines) {
		e.printf("        %s\n", e.paint(styleExplainDetail, line))
	}
}

// tailLines returns the last n lines of a text, without its trailing newline.
func tailLines(text string, n int) []string {
	trimmed := strings.TrimRight(text, "\n")
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return lines
}

// timeline is the stages the mutant took part in, which is where a slow run's
// minutes went.
func (e *explainer) timeline(r *report.Report, s subject, rec *recording) {
	e.section("timeline")
	if !rec.present() {
		e.printf("  %s\n", e.paint(styleExplainDetail,
			"no recording, so the stages this mutant took part in are not in this account"))
		if r.Timing != nil {
			e.printf("  %s\n", e.paint(styleExplainDetail,
				"`timing` in the report is the whole run's, phase by phase and stage by stage"))
		}
		return
	}
	spans := rec.stagesOver(s.id(), rec.seqsOf(s.id()))
	if len(spans) == 0 {
		e.printf("  %s\n", e.paint(styleExplainDetail,
			"the recording holds no stage this mutant was measured inside"))
		return
	}
	for _, span := range spans {
		if !span.closed {
			e.printf("  %s  %s\n", qualifiedStage(span.phase, span.name),
				e.paint(styleExplainDetail, "still open at the end of the recording"))
			continue
		}
		e.printf("  %s  %s  %s%s\n",
			qualifiedStage(span.phase, span.name), span.result,
			console.FormatDuration(milliseconds(span.durationMS)), share(span.shareMS))
	}
}

// share is the mutant's own part of a step, written beside the step's total.
//
// It is omitted when it is nothing, which is the honest answer for a stage the
// mutant merely sat inside — a validation bisection, say — rather than a zero
// that reads as a measurement.
func share(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return "  (this mutant: " + console.FormatDuration(milliseconds(ms)) + ")"
}

// qualifiedStage writes "phase/name", or the name alone when the recording
// carried no phase — internal/console's own spelling of the pair.
func qualifiedStage(phase, name string) string {
	if phase == "" {
		return name
	}
	return phase + "/" + name
}

// reproduction is the command to paste, or the reason there is none.
//
// It is derived from the recording's own `exec` event and from nothing else.
// The argument vector is what the child really received — the binary, the
// paired `-test.timeout` internal/execute derives from the mutant's own budget,
// and whatever the run's test command added — and the directory is the
// package's own inside the snapshot, because a Go test resolves testdata
// relative to where it runs. Composing one out of the report instead would be
// composing a command that was never run.
//
// TMPDIR is deliberately not part of the line. The run points it at a private
// scratch directory per worker so that two mutants in flight cannot see each
// other's temporary files, and that path is nowhere in the recording — only the
// variable's *name* is, because a recording never carries a value. Printing a
// directory this command had guessed at would be printing a command that is not
// the one that ran.
//
// Neither is GOFLAGS, and that one was a bug rather than a judgement: the
// argument vector starts a *prebuilt test binary*, which never reads GOFLAGS,
// so an overlay on the run line was a variable that did nothing standing in
// front of the one command in this tool that has to be exactly right. The
// manifest belongs to rebuilding the binary, and that is where it is printed.
func (e *explainer) reproduction(r *report.Report, s subject, rec *recording) {
	e.section("reproduce")
	recorded := rec.attempts(s.id())
	event, ok := rec.execEvent(lastExecSeq(recorded))
	if !ok || len(event.Exec.Argv) == 0 {
		e.unrecordedReproduction(r, s, rec)
		return
	}
	e.printf("  cd %s && GO_MUTANTS_ACTIVE=%s %s\n",
		console.QuoteArgv([]string{event.Exec.Dir}), s.id(), console.QuoteArgv(event.Exec.Argv))
	e.printf("  %s\n", e.paint(styleExplainDetail, temporariesNote(rec)))
	e.rebuild(s, rec)
}

// temporariesNote says whether the binary and the directory the line above
// names are still there.
//
// The recording knows, so this stops being a hedge the reader has to resolve by
// running the command and watching `cd` fail: a run keeps its temporaries only
// when it was asked to, and records an `artifact` for each one it kept.
func temporariesNote(rec *recording) string {
	if rec.keptTemporaries() {
		return "the run kept its temporaries: the binary and directory above are still there"
	}
	return "this run did not keep its temporaries, so the binary and the directory above no longer " +
		"exist; re-run with `--trace --keep-temp` to get a command that can be pasted"
}

// rebuild is the second line a library session's account carries: how to
// compile that test binary again.
//
// A session's instrumented tree is compiled through an overlay whose manifest
// lives in a scratch directory named when the session was prepared, so it
// cannot be derived and has to be carried. It is `docs/library.md`'s own recipe
// — `cd <snapshot> && GOFLAGS=-overlay=<manifest> go test -c …` — printed
// rather than assembled by hand, and it is the one place GOFLAGS does anything,
// because here the command really is the `go` command.
//
// A CLI run records no manifest and gets no line: its snapshot is instrumented
// in place, so `go test -c` inside it needs nothing added.
func (e *explainer) rebuild(s subject, rec *recording) {
	manifest := rec.artifact(trace.ArtifactOverlayManifest)
	if manifest == "" {
		return
	}
	line := "GOFLAGS=-overlay=" + console.QuoteArgv([]string{manifest}) + " go test -c -o mutant.test"
	if s.mutant != nil && s.mutant.Package != "" {
		line += " " + console.QuoteArgv([]string{s.mutant.Package})
	}
	if snapshot := rec.snapshotDir(); snapshot != "" {
		line = "cd " + console.QuoteArgv([]string{snapshot}) + " && " + line
	}
	e.printf("  to rebuild the binary: %s\n", line)
}

// unrecordedReproduction is what the account says when there is no argument
// vector to paste: the command the run measured with, and the invocation that
// would record one.
//
// The two ways there can be none are said apart, because they are two different
// pieces of news. A run that recorded nothing may yet have executed this mutant
// — run it again with `--trace` and the vector is there. A run that recorded
// everything and holds no command for this mutant started no process for it at
// all, which is a fact about the mutant rather than about the recording, and no
// amount of re-running with `--trace` will produce one.
func (e *explainer) unrecordedReproduction(r *report.Report, s subject, rec *recording) {
	switch {
	case rec.present() && !rec.describes(r.RunID):
		// A recording of somebody else's run is no evidence about this one:
		// "this run started no process for it" would be a claim about a run
		// whose account nobody has read.
		e.printf("  %s\n", e.paint(styleExplainDetail,
			"the recording read is of another run, so it holds no command for this mutant"))
	case rec.present():
		e.printf("  %s\n", e.paint(styleExplainDetail,
			"this run started no process for it, so its recording holds no argument vector to paste"))
	default:
		e.printf("  %s\n", e.paint(styleExplainDetail, "no recording, so there is no argument vector to paste"))
	}
	if command := testCommandOf(r); len(command) > 0 {
		e.printf("  the run's tests were: %s\n", console.QuoteArgv(command))
	}
	e.printf("  go-mutants run --mutant %s --keep-temp -vv --trace\n", shortID(s.displayID()))
}

// testCommandOf prefers the argv that was really started to the one that was
// configured. The two differ in exactly one string — the located toolchain in
// place of a bare `go` — and that string is the answer to "which go ran this?".
func testCommandOf(r *report.Report) []string {
	if len(r.Test.ResolvedCommand) > 0 {
		return r.Test.ResolvedCommand
	}
	return r.Test.Command
}

// rejectedReproduction is the reproduce block of a mutant that does not
// compile. There is no binary and there never was one, so what is offered is
// the way to make the compiler say it again.
func (e *explainer) rejectedReproduction(s subject) {
	e.section("reproduce")
	e.printf("  %s\n", e.paint(styleExplainDetail,
		"validation refused this mutant, so no test binary was ever built with it in"))
	e.printf("  go-mutants run --mutant %s --explain\n", shortID(s.displayID()))
}

// lastExecSeq is the command that decided a mutant's verdict: the last one of
// its last recorded pass.
//
// The last rather than the first, because a pass stops where it stopped — a
// mutant killed by the second of three binaries was measured against two — so
// the last command of the deciding pass is the one whose output is the
// evidence, and therefore the one worth pasting.
func lastExecSeq(recorded []*trace.MutantRecord) int64 {
	for i := len(recorded) - 1; i >= 0; i-- {
		if seqs := recorded[i].ExecSeqs; len(seqs) > 0 {
			return seqs[len(seqs)-1]
		}
	}
	return 0
}

// milliseconds turns the unit both documents record durations in back into a
// duration, which is what internal/console's formatters take.
func milliseconds(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }
