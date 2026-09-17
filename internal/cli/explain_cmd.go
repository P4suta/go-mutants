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
	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/trace"
)

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

type explainOptions struct {
	report  string
	run     string
	trace   string
	json    bool
	noColor bool
}

func newExplainCommand() *cobra.Command {
	o := &explainOptions{}
	cmd := &cobra.Command{
		Use:   "explain TARGET [flags]",
		Short: "Say why one mutant got its verdict, and how to reproduce it",
		Long:  explainLong,
		Args:  cobra.ArbitraryArgs,
		RunE:  o.execute,
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
		"print a go-mutants/explain v1 document instead of the account in prose")
	flags.BoolVar(&o.noColor, "no-color", false,
		"never colourise output, even on a terminal")
	return cmd
}

func (o *explainOptions) execute(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return usagef("explain takes exactly one target, as in `go-mutants explain bf513c0d` or " +
			"`go-mutants explain internal/clamp.go:41`")
	}
	if err := o.checkFlags(); err != nil {
		return err
	}

	where, isPosition := parsePosition(args[0])
	document, source, err := o.readReport(o.listingsTo(cmd), isPosition)
	if err != nil {
		return err
	}
	color := console.ColorEnabled(cmd.OutOrStdout(), o.noColor)

	if isPosition {
		return o.explainAt(cmd, document, source, where, color)
	}
	return o.explainMutant(cmd, document, source, args[0], color)
}

func (o *explainOptions) listingsTo(cmd *cobra.Command) io.Writer {
	if o.json {
		return cmd.ErrOrStderr()
	}
	return cmd.OutOrStdout()
}

func (o *explainOptions) checkFlags() error {
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
			return nil, "", nil
		}
		return document, source, err
	}
}

func absentHistory(err error) bool {
	var coded *Error
	if !errors.As(err, &coded) {
		return false
	}
	return coded.Code == CodeNoStoredRun || coded.Code == CodeNotAModuleRoot
}

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

func writeRunMatches(w io.Writer, matches []report.StoredRun) error {
	var b strings.Builder
	b.WriteString("matched " + countNoun(len(matches), "recorded run") + "\n")
	for _, run := range matches {
		b.WriteString("  " + run.RunID + "  " + formatMoment(run.FinishedAt) +
			"  " + formatScore(run) + "  " + run.Status.String() + "\n")
	}
	return emit(w, b.String())
}

func parseReport(path string) (*report.Report, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	documentType, err := report.DocumentTypeOf(data)
	if err != nil {
		return nil, notAReport(path, "read", err)
	}
	if documentType == report.WorkspaceDocumentType {
		workspace, parseErr := report.ParseWorkspace(data)
		if parseErr != nil {
			return nil, notAReport(path, "read", parseErr)
		}
		return flattenWorkspace(workspace), nil
	}
	document, err := report.Parse(data)
	if err != nil {
		return nil, notAReport(path, "read", err)
	}
	return document, nil
}

func flattenWorkspace(workspace *report.WorkspaceReport) *report.Report {
	if len(workspace.Modules) == 0 {
		return &report.Report{
			DocumentType:  report.DocumentType,
			SchemaVersion: report.SchemaVersion,
			RunID:         workspace.RunID,
			Status:        report.Status(workspace.Status),
		}
	}
	flat := *workspace.Modules[0].Report
	flat.Mutants = nil
	flat.Rejected = nil
	flat.Skips = nil
	flat.Expectations = append([]report.Expectation(nil), workspace.Expectations...)
	for _, module := range workspace.Modules {
		for _, m := range module.Report.Mutants {
			m.Path = engine.WorkspaceLocation(module.Dir, m.Path)
			flat.Mutants = append(flat.Mutants, m)
		}
		for _, r := range module.Report.Rejected {
			r.Path = engine.WorkspaceLocation(module.Dir, r.Path)
			flat.Rejected = append(flat.Rejected, r)
		}
		for _, skip := range module.Report.Skips {
			skip.Path = engine.WorkspaceLocation(module.Dir, skip.Path)
			flat.Skips = append(flat.Skips, skip)
		}
		flat.Expectations = append(flat.Expectations, module.Report.Expectations...)
	}
	flat.Summary = workspace.Summary
	return &flat
}

type position struct {
	path string
	line int
}

func (p position) String() string {
	if p.line == 0 {
		return p.path
	}
	return p.path + ":" + strconv.Itoa(p.line)
}

func parsePosition(target string) (position, bool) {
	raw, line := stripCoordinates(target)
	slashed := filepath.ToSlash(raw)
	if slashed == "" || (!strings.Contains(slashed, "/") && !strings.HasSuffix(slashed, ".go")) {
		return position{}, false
	}
	return position{path: path.Clean(slashed), line: line}, true
}

func stripCoordinates(target string) (string, int) {
	rest, line := target, 0
	for range 2 {
		cut := strings.LastIndexByte(rest, ':')
		if cut <= 0 {
			break
		}
		head, tail := rest[:cut], rest[cut+1:]
		number, err := strconv.Atoi(tail)
		if err != nil || number < 1 || strconv.Itoa(number) != tail {
			break
		}
		rest, line = head, number
	}
	return rest, line
}

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

func (p position) notInWorkspace(why string) error {
	return &Error{
		Code:    CodeUsage,
		Message: strconv.Quote(p.path) + " is not a file in this workspace: " + why,
		Hint:    "name a path relative to the module root, as in `go-mutants explain internal/clamp.go:41`",
	}
}

func (p position) holdsSite(path string, line int) bool {
	return path == p.path && (line == 0 || p.line == 0 || p.line == line)
}

func (p position) holdsSpan(path string, start, end int) bool {
	return path == p.path && (p.line == 0 || (p.line >= start && p.line <= end))
}

type subject struct {
	mutant   *report.Mutant
	rejected *report.Rejected
}

func (s subject) id() string {
	if s.mutant != nil {
		return s.mutant.ID
	}
	return s.rejected.ID
}

func (s subject) displayID() string {
	if s.mutant != nil {
		return s.mutant.DisplayID
	}
	return s.rejected.DisplayID
}

func (s subject) location() string {
	if s.mutant != nil {
		return s.mutant.Path + ":" + strconv.Itoa(s.mutant.Line) + ":" + strconv.Itoa(s.mutant.Column)
	}
	return s.rejected.Path + ":" + strconv.Itoa(s.rejected.Line) + ":" + strconv.Itoa(s.rejected.Column)
}

func (s subject) rule() string {
	if s.mutant != nil {
		return s.mutant.Family + "/" + s.mutant.Rule
	}
	return s.rejected.Rule
}

func (s subject) outcome() string {
	if s.mutant != nil {
		return s.mutant.Outcome.String()
	}
	return "rejected"
}

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

func (o *explainOptions) explainMutant(
	cmd *cobra.Command, r *report.Report, source, prefix string, color bool,
) error {
	found, matches, err := resolveSubject(r, prefix)
	if err != nil {
		if len(matches) > 0 {
			if writeErr := writeMatches(o.listingsTo(cmd), color, matches); writeErr != nil {
				return writeErr
			}
		}
		return err
	}

	rec, err := o.openRecording(r.RunID)
	if err != nil {
		return err
	}
	document := gatherAccount(r, source, found, rec)
	if o.json {
		return writeExplainJSON(cmd.OutOrStdout(), document)
	}
	e := newExplainer(cmd.OutOrStdout(), color)
	e.account(document)
	return e.out.Flush()
}

func writeMatches(w io.Writer, color bool, matches []subject) error {
	e := newExplainer(w, color)
	e.printf("%s\n", e.paint(styleExplainHeader, "matched "+countNoun(len(matches), "mutant")))
	for _, m := range matches {
		e.printf("  %s  %s  %s\n", m.displayID(), m.location(), e.paint(styleListRule, m.outcome()))
	}
	return e.out.Flush()
}

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
	ctx, watch, stop := watchSignals(cmd.Context())
	defer stop()
	found, err := discoverCatalog(ctx, root, cfg, cmd.ErrOrStderr())
	if err != nil {
		return interpret(err, watch.Signal())
	}
	catalogue, err := found.document(cfg, "")
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	document := gatherPosition(r, source, where, found.skipSites(), catalogue.Mutants, outcomesOf(r))
	if o.json {
		return writeExplainJSON(out, document)
	}
	if err = emit(out, sourceHeader(document.Source.Report)); err != nil {
		return err
	}
	return explainPosition(out, color, document)
}

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

func sourceHeader(source *accountReport) string {
	if source == nil {
		return ""
	}
	return "run " + source.RunID + "  " + source.Status + "\nreport " + source.Path + "\n"
}

func explainPosition(w io.Writer, color bool, doc explainPositionDocument) error {
	e := newExplainer(w, color)
	e.printf("position %s\n", positionOf(doc.Subject))

	e.section("skip sites")
	for _, site := range doc.SkipSites {
		e.printf("  %s  %s\n", siteLocationOf(site), e.paint(styleListRule, site.Reason))
	}
	if len(doc.SkipSites) == 0 {
		e.printf("  %s\n", e.paint(styleExplainDetail, "discovery passed nothing over here"))
	}

	e.section("mutants")
	for _, m := range doc.Mutants {
		e.printf("  %s  %s:%d:%d  %s  %s -> %s  %s\n",
			m.DisplayID, m.Path, m.Line, m.Column,
			e.paint(styleListRule, m.Family+"/"+m.Rule),
			oneLine(m.Original), oneLine(m.Replacement),
			outcomeIn(doc.Source.Report, m.Outcome))
	}
	if len(doc.Mutants) == 0 {
		e.printf("  %s\n", e.paint(styleExplainDetail, "this catalogue has no mutant here"))
	}
	return e.out.Flush()
}

func positionOf(subject accountPosition) string {
	if subject.Line == nil {
		return subject.Path
	}
	return subject.Path + ":" + strconv.Itoa(*subject.Line)
}

func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

func outcomeIn(source *accountReport, outcome *string) string {
	if source == nil {
		return "no report"
	}
	if outcome == nil {
		return "not in this run"
	}
	return *outcome
}

type recording struct {
	stream    string
	directory string
	events    []trace.Event
	kind      string
	runID     string
}

func (rec *recording) present() bool { return rec != nil && len(rec.events) > 0 }

func (rec *recording) describes(runID string) bool {
	return !rec.present() || rec.runID == "" || rec.runID == runID
}

func (o *explainOptions) openRecording(runID string) (*recording, error) {
	if o.trace != "" {
		return readRecordingAt(o.trace, true)
	}
	dir, cfg, err := workspaceConfig()
	if err != nil {
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

type stageSpan struct {
	phase      string
	name       string
	result     string
	durationMS int64
	start, end int64
	closed     bool
	shareMS    int64
}

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

func (e *explainer) account(doc explainDocument) {
	e.printf("%s", sourceHeader(doc.Source.Report))
	if doc.Source.Trace == nil {
		e.printf("%s\n", noRecording(doc.Source.Report.RunID))
	} else {
		e.printf("trace %s\n", doc.Source.Trace.Stream)
	}
	for _, warning := range doc.Source.Warnings {
		e.printf("warning: %s\n", warning)
	}

	e.identity(doc.Subject)
	e.verdict(doc.Verdict)
	if doc.Subject.Rejected {
		e.timeline(doc)
		e.reproduction(doc.Reproduce)
		return
	}
	e.coverage(doc.Coverage)
	e.executions(doc)
	e.timeline(doc)
	e.reproduction(doc.Reproduce)
}

func noRecording(runID string) string {
	return "no trace recorded for run " + runID + "; re-run with --trace"
}

func (e *explainer) section(title string) {
	e.printf("\n%s\n", e.paint(styleExplainHeader, title))
}

func (e *explainer) field(label, value string) {
	e.printf("  %-10s  %s\n", label, value)
}

func (e *explainer) identity(subject accountSubject) {
	e.section("mutant")
	e.field("display id", subject.DisplayID)
	e.field("id", subject.ID)
	e.field("rule", e.paint(styleListRule, subject.Rule))
	e.field("position", subject.Path+":"+strconv.Itoa(subject.Line)+":"+strconv.Itoa(subject.Column))
	if subject.Original != nil && subject.Replacement != nil {
		e.field("change", *subject.Original+" -> "+*subject.Replacement)
	}
	if subject.Package != nil {
		e.field("package", *subject.Package)
	}
}

func (e *explainer) verdict(v accountVerdict) {
	e.section("outcome")
	e.printf("  %s\n", v.Summary)
	if v.Diagnostic != nil {
		e.printf("%s\n", e.paint(styleExplainDetail, indent(*v.Diagnostic)))
		return
	}
	if v.Cached {
		e.printf("  %s\n", e.paint(styleExplainDetail,
			"reused from the outcome cache rather than measured by this run, so the duration, the attempts "+
				"and the killer above are the ones the run that did measure it recorded"))
	}
}

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
		if m.Diverged {
			if killedBy != "" {
				return "did not return: a loop in " + killedBy +
					" ran past what the original does, after " + countNoun(m.Attempts, "attempt")
			}
			return "did not return: a loop ran past what the original does, after " +
				countNoun(m.Attempts, "attempt")
		}
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

func memoryClause(m report.Mutant, memoryBound int64) string {
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

func (e *explainer) coverage(c accountCoverage) {
	e.section("coverage")
	e.printf("  %s\n", c.Summary)
}

func testRefStrings(refs []report.TestRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Package+" "+ref.Name)
	}
	return out
}

func (e *explainer) executions(doc explainDocument) {
	e.section("executions")
	if len(doc.Executions) == 0 {
		e.printf("  %s\n", e.paint(styleExplainDetail, notExecuted(doc)))
		return
	}
	for _, execution := range doc.Executions {
		e.printf("  attempt %d  worker %d  %s  %s%s%s\n",
			execution.Attempt, execution.Worker, execution.Outcome,
			console.FormatDuration(milliseconds(execution.DurationMS)),
			peakClause(execution.PeakMemoryBytes),
			attribution(execution.Outcome, execution.KilledBy, execution.Diverged))
		if len(execution.Binaries) > 0 {
			e.printf("    binaries: %s\n", strings.Join(execution.Binaries, ", "))
		}
		e.commands(execution.Commands)
	}
	if doc.Source.Trace == nil {
		e.printf("  %s\n", e.paint(styleExplainDetail,
			"no recording, so the commands these passes started are not in this account"))
	}
}

func peakClause(peak *int64) string {
	if peak == nil {
		return ""
	}
	return "  peak " + console.FormatBytes(*peak)
}

func notExecuted(doc explainDocument) string {
	switch {
	case doc.Verdict.Cached:
		return "this run started no process for it: the outcome was reused from the outcome cache"
	case doc.Coverage.Uncovered:
		return "this run started no process for it: no test binary reaches its lines"
	case doc.Verdict.Outcome == string(report.OutcomeNotRun):
		return "this run started no process for it: it was never selected"
	default:
		return "this document records no executions for it"
	}
}

func attribution(outcome string, killedBy *string, diverged bool) string {
	if killedBy == nil {
		return ""
	}
	if outcome == string(report.OutcomeTimedOut) {
		if diverged {
			return "  a loop in " + *killedBy + " ran away"
		}
		return "  hung in " + *killedBy
	}
	return "  killed by " + *killedBy
}

func pass(recorded []*trace.MutantRecord, attempt int) *trace.MutantRecord {
	for _, record := range recorded {
		if record.Attempt == attempt {
			return record
		}
	}
	return nil
}

func (e *explainer) commands(commands []accountCommand) {
	for _, command := range commands {
		if !command.Recorded {
			e.printf("    %s\n", e.paint(styleExplainDetail,
				"the command recorded at seq "+strconv.FormatInt(command.Seq, 10)+" is not in this recording"))
			continue
		}
		e.printf("    %s\n", console.TraceLine(command.event))
		if command.Dir != "" {
			e.printf("      dir: %s\n", command.Dir)
		}
		e.preservedOutput(command)
	}
}

const outputTailLines = 10

func (e *explainer) preservedOutput(command accountCommand) {
	if command.OutputPath == "" {
		return
	}
	e.printf("      output: %s\n", command.OutputPath)
	if command.OutputError != "" {
		e.printf("        %s\n", e.paint(styleExplainDetail, command.OutputError))
		return
	}
	for _, line := range command.OutputTail {
		e.printf("        %s\n", e.paint(styleExplainDetail, line))
	}
}

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

func (e *explainer) timeline(doc explainDocument) {
	e.section("timeline")
	if doc.Source.Trace == nil {
		e.printf("  %s\n", e.paint(styleExplainDetail,
			"no recording, so the stages this mutant took part in are not in this account"))
		if doc.ReportHasTiming {
			e.printf("  %s\n", e.paint(styleExplainDetail,
				"`timing` in the report is the whole run's, phase by phase and stage by stage"))
		}
		return
	}
	if len(doc.Timeline) == 0 {
		e.printf("  %s\n", e.paint(styleExplainDetail,
			"the recording holds no stage this mutant was measured inside"))
		return
	}
	for _, span := range doc.Timeline {
		if !span.Closed {
			e.printf("  %s  %s\n", qualifiedStage(span.Phase, span.Stage),
				e.paint(styleExplainDetail, "still open at the end of the recording"))
			continue
		}
		e.printf("  %s  %s  %s%s\n",
			qualifiedStage(span.Phase, span.Stage), span.Result,
			console.FormatDuration(milliseconds(*span.DurationMS)), share(span.ShareMS))
	}
}

func share(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return "  (this mutant: " + console.FormatDuration(milliseconds(ms)) + ")"
}

func qualifiedStage(phase, name string) string {
	if phase == "" {
		return name
	}
	return phase + "/" + name
}

func (e *explainer) reproduction(reproduce accountReproduction) {
	e.section("reproduce")
	if reproduce.Available {
		e.printf("  %s\n", *reproduce.Command)
		e.printf("  %s\n", e.paint(styleExplainDetail, *reproduce.Note))
		if reproduce.Rebuild != nil {
			e.printf("  to rebuild the binary: %s\n", *reproduce.Rebuild)
		}
		return
	}
	if reproduce.UnavailableReason != nil {
		e.printf("  %s\n", e.paint(styleExplainDetail, *reproduce.UnavailableReason))
	}
	if len(reproduce.TestCommand) > 0 {
		e.printf("  the run's tests were: %s\n", console.QuoteArgv(reproduce.TestCommand))
	}
	if reproduce.Suggestion != "" {
		e.printf("  %s\n", reproduce.Suggestion)
	}
}

func temporariesNote(rec *recording) string {
	if rec.keptTemporaries() {
		return "the run kept its temporaries: the binary and directory above are still there"
	}
	return "this run did not keep its temporaries, so the binary and the directory above no longer " +
		"exist; re-run with `--trace --keep-temp` to get a command that can be pasted"
}

func testCommandOf(r *report.Report) []string {
	if len(r.Test.ResolvedCommand) > 0 {
		return r.Test.ResolvedCommand
	}
	return r.Test.Command
}

func lastExecSeq(recorded []*trace.MutantRecord) int64 {
	for i := len(recorded) - 1; i >= 0; i-- {
		if seqs := recorded[i].ExecSeqs; len(seqs) > 0 {
			return seqs[len(seqs)-1]
		}
	}
	return 0
}

func milliseconds(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }
