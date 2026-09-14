// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/console"
	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/trace"
)

// The account is gathered once and rendered twice.
//
// That is the whole design, and it is the answer to the objection `--json` used
// to be refused under: a second, independent JSON path really would have been a
// third thing to keep in step with the report and the recording. One value that
// both the prose and the encoder read cannot drift from itself, and a fact that
// appears in one rendering and not the other is a missing line in a renderer
// rather than a missing fact.
//
// What is gathered is a *join*, and it says so. `source` names the report and
// the recording it was read out of, because a consumer that wants the lossless
// claim about a run should be reading the report: this document is derived, and
// a derived document that will not say what it derived from is the one kind
// nobody can check. Five things in it are in neither source, which is why it
// exists at all -- the command to paste, the command to rebuild the binary
// with, this mutant's own share of each stage, the tail of what its last pass
// printed, and the two judgements about whether the first of those can be
// trusted.
//
// Absence is stated rather than omitted. A run that recorded nothing has a null
// `source.trace`, an empty `timeline` and an unavailable `reproduce` carrying
// the reason -- which is [explainOptions]'s own principle, that a section whose
// document is missing says so rather than composing a plausible command, spelt
// as a schema.

// explainSchemaVersion is the version of the explanation document. It moves
// when a consumer that satisfied the old one would break on the new one.
const explainSchemaVersion = 1

// An explainDocument is the whole account of one mutant.
//
// Fields tagged `json:"-"` are gathered facts the prose says and the document
// does not need, because a reader of the document can look them up in the
// sources it names and a reader of the prose is being told where to look.
type explainDocument struct {
	DocumentType  string `json:"document_type"`
	SchemaVersion int    `json:"schema_version"`
	ToolVersion   string `json:"tool_version"`

	Source     accountSource       `json:"source"`
	Subject    accountSubject      `json:"subject"`
	Verdict    accountVerdict      `json:"verdict"`
	Coverage   accountCoverage     `json:"coverage"`
	Executions []accountExecution  `json:"executions"`
	Timeline   []accountStage      `json:"timeline"`
	Reproduce  accountReproduction `json:"reproduce"`

	// ReportHasTiming is whether the report carries a run-wide `timing` block.
	// The prose names it when there is no recording, as the nearest thing to an
	// answer; a document's reader has the report's path and can see for
	// themselves.
	ReportHasTiming bool `json:"-"`
}

// An explainPositionDocument is the account of one place in the source.
type explainPositionDocument struct {
	DocumentType  string `json:"document_type"`
	SchemaVersion int    `json:"schema_version"`
	ToolVersion   string `json:"tool_version"`

	Source    accountSource      `json:"source"`
	Subject   accountPosition    `json:"subject"`
	SkipSites []accountSkipSite  `json:"skip_sites"`
	Mutants   []accountCandidate `json:"mutants"`
}

// An accountSource is the two documents the account was read out of.
type accountSource struct {
	Report *accountReport `json:"report"`
	Trace  *accountTrace  `json:"trace"`
	// Warnings is what the account distrusts about its own sources, in the
	// words the console prints. Never nil.
	Warnings []string `json:"warnings"`
}

// An accountReport names the run report.
type accountReport struct {
	Path   string `json:"path"`
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// An accountTrace names the recording.
type accountTrace struct {
	Stream    string `json:"stream"`
	Directory string `json:"directory"`
	RunID     string `json:"run_id"`
	Kind      string `json:"kind"`
	// DescribesTheReport is false for a recording of another run, which is a
	// loud warning rather than a detail: a run id is content-derived, so two
	// runs can be filed under one.
	DescribesTheReport bool `json:"describes_the_report"`
}

// An accountSubject is what the mutant is.
type accountSubject struct {
	Kind        string  `json:"kind"`
	ID          string  `json:"id"`
	DisplayID   string  `json:"display_id"`
	Path        string  `json:"path"`
	Line        int     `json:"line"`
	Column      int     `json:"column"`
	Family      *string `json:"family"`
	Rule        string  `json:"rule"`
	Original    *string `json:"original"`
	Replacement *string `json:"replacement"`
	Package     *string `json:"package"`
	Rejected    bool    `json:"rejected"`
}

// An accountPosition is the place asked about.
type accountPosition struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line *int   `json:"line"`
}

// An accountVerdict is what became of the mutant.
type accountVerdict struct {
	Outcome      string         `json:"outcome"`
	Summary      string         `json:"summary"`
	Attempts     int            `json:"attempts"`
	Cached       bool           `json:"cached"`
	KilledBy     *string        `json:"killed_by"`
	Memory       *accountMemory `json:"memory"`
	Diagnostic   *string        `json:"diagnostic"`
	NotRunReason *string        `json:"not_run_reason"`
}

// An accountMemory is what the mutant cost the machine and what it was measured
// against. Either figure is null on its own: a platform that cannot say what a
// process held reports no peak, and a document written before the bound existed
// carries no bound.
type accountMemory struct {
	Exceeded   bool   `json:"exceeded"`
	PeakBytes  *int64 `json:"peak_bytes"`
	BoundBytes *int64 `json:"bound_bytes"`
}

// An accountCoverage is which test binaries reach the mutant.
type accountCoverage struct {
	Mode      string           `json:"mode"`
	Summary   string           `json:"summary"`
	Uncovered bool             `json:"uncovered"`
	Packages  []string         `json:"packages"`
	Tests     []accountTestRef `json:"tests"`
}

// An accountTestRef is one test, spelt as the covering line names one.
type accountTestRef struct {
	Package string `json:"package"`
	Name    string `json:"name"`
}

// An accountExecution is one pass over the test binaries.
type accountExecution struct {
	Attempt         int              `json:"attempt"`
	Worker          int              `json:"worker"`
	Outcome         string           `json:"outcome"`
	DurationMS      int64            `json:"duration_ms"`
	PeakMemoryBytes *int64           `json:"peak_memory_bytes"`
	KilledBy        *string          `json:"killed_by"`
	Binaries        []string         `json:"binaries"`
	Commands        []accountCommand `json:"commands"`
}

// An accountCommand is one child process, with what it printed.
type accountCommand struct {
	Seq int64 `json:"seq"`
	// Recorded is false for a sequence number the recording names and does not
	// hold. A gap in a stream is a fact about the stream, so it is reported
	// rather than dropped.
	Recorded    bool     `json:"recorded"`
	Kind        string   `json:"kind"`
	Argv        []string `json:"argv"`
	Dir         string   `json:"dir"`
	DurationMS  int64    `json:"duration_ms"`
	ExitCode    int      `json:"exit_code"`
	TimedOut    bool     `json:"timed_out"`
	OutputPath  string   `json:"output_path"`
	OutputTail  []string `json:"output_tail"`
	OutputError string   `json:"output_error"`

	// event is the recorded event this row came out of, kept so that the prose
	// renders it with [console.TraceLine] -- the same line `run -vv` prints for
	// the same event, which is what makes the two comparable. A document
	// carries the fields instead, because a rendering is not a fact.
	event trace.Event
}

// An accountStage is one stage the mutant took part in, and its own share of it.
type accountStage struct {
	Phase      string `json:"phase"`
	Stage      string `json:"stage"`
	Result     string `json:"result"`
	DurationMS *int64 `json:"duration_ms"`
	ShareMS    int64  `json:"share_ms"`
	Closed     bool   `json:"closed"`
}

// An accountReproduction is how to see the verdict again.
type accountReproduction struct {
	Available         bool               `json:"available"`
	UnavailableReason *string            `json:"unavailable_reason"`
	Command           *string            `json:"command"`
	Dir               *string            `json:"dir"`
	Argv              []string           `json:"argv"`
	Activation        *accountActivation `json:"activation"`
	TemporariesKept   *bool              `json:"temporaries_kept"`
	Note              *string            `json:"note"`
	Rebuild           *string            `json:"rebuild"`
	TestCommand       []string           `json:"test_command"`
	Suggestion        string             `json:"suggestion"`
}

// An accountActivation is the variable that selects the mutant.
type accountActivation struct {
	Variable string `json:"variable"`
	Value    string `json:"value"`
}

// An accountSkipSite is one site discovery declined.
type accountSkipSite struct {
	Path   string  `json:"path"`
	Line   *int    `json:"line"`
	Column *int    `json:"column"`
	Reason string  `json:"reason"`
	Rule   *string `json:"rule"`
}

// An accountCandidate is one catalogued mutant at a position, with what the
// report says became of it.
type accountCandidate struct {
	ID          string  `json:"id"`
	DisplayID   string  `json:"display_id"`
	Path        string  `json:"path"`
	Line        int     `json:"line"`
	Column      int     `json:"column"`
	Family      string  `json:"family"`
	Rule        string  `json:"rule"`
	Original    string  `json:"original"`
	Replacement string  `json:"replacement"`
	Outcome     *string `json:"outcome"`
}

// activationVariable is the environment variable a reproduction sets. It is
// spelt here rather than imported because this is the CLI's own rendering of
// somebody else's contract: the runtime reads it, and this names it.
const activationVariable = "GO_MUTANTS_ACTIVE"

// gatherAccount reads both documents once and builds the whole account.
//
// Everything derived is derived here, so that a renderer is a transcription
// rather than a second reading. The order below is the order the sections are
// printed in, which is also the order somebody reads them: what it is, what
// became of it, what could have reached it, what ran, when, and how to do it
// again.
func gatherAccount(r *report.Report, source string, s subject, rec *recording) explainDocument {
	doc := explainDocument{
		DocumentType:    schemas.ExplainV1,
		SchemaVersion:   explainSchemaVersion,
		ToolVersion:     Version,
		Source:          gatherSource(r, source, rec),
		Subject:         gatherSubject(s),
		Verdict:         gatherVerdict(r, s),
		ReportHasTiming: r != nil && r.Timing != nil,
	}
	if s.rejected != nil {
		// A mutant that does not compile has no coverage and no executions:
		// nothing measured it, because there was nothing to measure. It does
		// have a timeline -- the bisection that refused it -- which is often
		// where a slow run's minutes went.
		doc.Coverage = gatherCoverage(r, nil)
		doc.Executions = []accountExecution{}
		doc.Timeline = gatherTimeline(s, rec)
		doc.Reproduce = rejectedReproduction(s)
		return doc
	}
	doc.Coverage = gatherCoverage(r, s.mutant)
	doc.Executions = gatherExecutions(s.mutant, rec)
	doc.Timeline = gatherTimeline(s, rec)
	doc.Reproduce = gatherReproduction(r, s, rec)
	return doc
}

// gatherSource names the documents, and says what it distrusts about them.
func gatherSource(r *report.Report, source string, rec *recording) accountSource {
	gathered := accountSource{Warnings: []string{}}
	if r != nil {
		gathered.Report = &accountReport{Path: source, RunID: r.RunID, Status: r.Status.String()}
	}
	if !rec.present() {
		return gathered
	}
	runID := ""
	if r != nil {
		runID = r.RunID
	}
	describes := r == nil || rec.describes(runID)
	gathered.Trace = &accountTrace{
		Stream:             rec.stream,
		Directory:          rec.directory,
		RunID:              rec.runID,
		Kind:               rec.kind,
		DescribesTheReport: describes,
	}
	if !describes {
		gathered.Warnings = append(gathered.Warnings, foreignRecordingWarning(rec.runID, runID))
	}
	return gathered
}

// foreignRecordingWarning is the sentence a recording of another run earns.
//
// Loudly, and before anything derived from it. A run id is content-derived, so
// two runs can be filed under one; whatever is underneath came out of that
// recording and is that run's, however convincingly it lines up with this
// report.
func foreignRecordingWarning(recordingRun, reportRun string) string {
	return "that recording is of run " + recordingRun + ", not " + reportRun +
		"; everything below it came out of that run"
}

// gatherSubject is what the mutant is, with the three fields a rejection has
// none of left null rather than blank.
func gatherSubject(s subject) accountSubject {
	gathered := accountSubject{
		Kind:      "mutant",
		ID:        s.id(),
		DisplayID: s.displayID(),
		Rule:      s.rule(),
		Rejected:  s.rejected != nil,
	}
	if m := s.mutant; m != nil {
		gathered.Path, gathered.Line, gathered.Column = m.Path, m.Line, m.Column
		gathered.Family = &m.Family
		gathered.Original, gathered.Replacement = &m.Original, &m.Replacement
		gathered.Package = &m.Package
		return gathered
	}
	gathered.Path = s.rejected.Path
	gathered.Line, gathered.Column = s.rejected.Line, s.rejected.Column
	return gathered
}

// gatherVerdict is the outcome, and the facts the sentence about it was
// composed from.
func gatherVerdict(r *report.Report, s subject) accountVerdict {
	if s.rejected != nil {
		diagnostic := s.rejected.Diagnostic
		return accountVerdict{
			Outcome:    s.outcome(),
			Summary:    rejectedSummary,
			Diagnostic: &diagnostic,
		}
	}
	m := s.mutant
	gathered := accountVerdict{
		Outcome:      m.Outcome.String(),
		Summary:      verdictSentence(*m, r.Test.MemoryBytes),
		Attempts:     m.Attempts,
		Cached:       m.Cached,
		KilledBy:     m.KilledBy,
		NotRunReason: m.NotRunReason,
		Memory: &accountMemory{
			Exceeded:   m.MemoryExceeded,
			PeakBytes:  positive(m.PeakMemoryBytes),
			BoundBytes: positive(r.Test.MemoryBytes),
		},
	}
	return gathered
}

// rejectedSummary is the verdict of a mutant the compiler refused.
const rejectedSummary = "rejected: the instrumented snapshot would not compile with it spliced in"

// positive is a byte count the platform could say, or nothing.
func positive(v int64) *int64 {
	if v <= 0 {
		return nil
	}
	return &v
}

// gatherCoverage is which test binaries reach the mutant, and the sentence that
// says which of the four things is true.
//
// An empty list means two different things depending on the mode, which is why
// the mode is read rather than the length: a run with coverage off asked
// nothing and measured every mutant against every binary, and "no test binary"
// for it would be a claim that run never made.
func gatherCoverage(r *report.Report, m *report.Mutant) accountCoverage {
	gathered := accountCoverage{Packages: []string{}, Tests: []accountTestRef{}}
	if r == nil {
		gathered.Mode = string(report.CoverageOff)
		gathered.Summary = "this account has no report, so it says nothing about coverage"
		return gathered
	}
	gathered.Mode = string(r.Coverage.Mode)
	if m == nil {
		gathered.Summary = "validation refused this mutant, so nothing ever measured what covers it"
		return gathered
	}
	gathered.Uncovered = m.Uncovered
	gathered.Packages = append(gathered.Packages, m.CoveringTestPackages...)
	for _, ref := range m.CoveringTests {
		gathered.Tests = append(gathered.Tests, accountTestRef{Package: ref.Package, Name: ref.Name})
	}
	switch {
	case r.Coverage.Mode == report.CoverageOff:
		gathered.Summary = "coverage off: this run measured every mutant against every test binary"
	case m.Uncovered:
		gathered.Summary = "no test binary reaches line " + strconv.Itoa(m.Line) + " of " + m.Path
	case len(m.CoveringTests) > 0:
		gathered.Summary = "covered by: " + strings.Join(testRefStrings(m.CoveringTests), ", ")
	case len(m.CoveringTestPackages) > 0:
		gathered.Summary = "covered by: " + strings.Join(m.CoveringTestPackages, ", ")
	default:
		gathered.Summary = "this run recorded no covering test package for it"
	}
	return gathered
}

// gatherExecutions is every pass the report records, with the commands the
// recording holds underneath each of them.
//
// The rows come from the report rather than from the recording, because the
// report is the durable claim and the recording is opt-in: a mutant's passes are
// described the same way whether or not anybody asked for a trace, and what a
// trace adds is the argument vectors rather than the passes.
func gatherExecutions(m *report.Mutant, rec *recording) []accountExecution {
	gathered := make([]accountExecution, 0, len(m.Executions))
	recorded := rec.attempts(m.ID)
	for _, execution := range m.Executions {
		row := accountExecution{
			Attempt:         execution.Attempt,
			Worker:          execution.Worker,
			Outcome:         execution.Outcome.String(),
			DurationMS:      execution.DurationMS,
			PeakMemoryBytes: positive(execution.PeakMemoryBytes),
			Binaries:        append([]string{}, execution.Binaries...),
			Commands:        gatherCommands(pass(recorded, execution.Attempt), rec),
		}
		if execution.KilledBy != "" {
			killedBy := execution.KilledBy
			row.KilledBy = &killedBy
		}
		gathered = append(gathered, row)
	}
	return gathered
}

// gatherCommands is the child processes one recorded pass started, with the end
// of what each of them printed.
func gatherCommands(record *trace.MutantRecord, rec *recording) []accountCommand {
	if record == nil {
		return []accountCommand{}
	}
	gathered := make([]accountCommand, 0, len(record.ExecSeqs))
	for _, seq := range record.ExecSeqs {
		event, ok := rec.execEvent(seq)
		if !ok {
			gathered = append(gathered, accountCommand{
				Seq: seq, Argv: []string{}, OutputTail: []string{},
			})
			continue
		}
		row := accountCommand{
			Seq:        seq,
			Recorded:   true,
			Kind:       event.Exec.Kind,
			Argv:       append([]string{}, event.Exec.Argv...),
			Dir:        event.Exec.Dir,
			DurationMS: event.Exec.DurationMS,
			ExitCode:   event.Exec.ExitCode,
			TimedOut:   event.Exec.TimedOut,
			OutputTail: []string{},
			event:      event,
		}
		if event.Exec.OutputPath != "" {
			row.OutputPath = filepath.Join(rec.directory, filepath.FromSlash(event.Exec.OutputPath))
			data, err := os.ReadFile(row.OutputPath)
			if err != nil {
				row.OutputError = "it could not be read: " + err.Error()
			} else {
				row.OutputTail = append(row.OutputTail, tailLines(string(data), outputTailLines)...)
			}
		}
		gathered = append(gathered, row)
	}
	return gathered
}

// gatherTimeline is the stages the mutant took part in, with its own share of
// each of them.
func gatherTimeline(s subject, rec *recording) []accountStage {
	if !rec.present() {
		return []accountStage{}
	}
	spans := rec.stagesOver(s.id(), rec.seqsOf(s.id()))
	gathered := make([]accountStage, 0, len(spans))
	for _, span := range spans {
		row := accountStage{
			Phase:   span.phase,
			Stage:   span.name,
			Result:  span.result,
			ShareMS: max(span.shareMS, 0),
			Closed:  span.closed,
		}
		if span.closed {
			duration := span.durationMS
			row.DurationMS = &duration
		}
		gathered = append(gathered, row)
	}
	return gathered
}

// gatherReproduction is the command to paste, or the reason there is none.
//
// It is derived from the recording's own `exec` event and from nothing else.
// The argument vector is what the child really received -- the binary, the
// paired `-test.timeout` internal/execute derives from the mutant's own budget,
// and whatever the run's test command added -- and the directory is the
// package's own inside the snapshot, because a Go test resolves testdata
// relative to where it runs. Composing one out of the report instead would be
// composing a command that was never run.
//
// TMPDIR is deliberately not part of the line. The run points it at a private
// scratch directory per worker so that two mutants in flight cannot see each
// other's temporary files, and that path is nowhere in the recording -- only the
// variable's *name* is, because a recording never carries a value.
//
// Neither is GOFLAGS, and that one was a bug rather than a judgement: the
// argument vector starts a *prebuilt test binary*, which never reads GOFLAGS.
// The manifest belongs to rebuilding the binary, and that is where it is.
func gatherReproduction(r *report.Report, s subject, rec *recording) accountReproduction {
	gathered := accountReproduction{Argv: []string{}, TestCommand: []string{}}
	recorded := rec.attempts(s.id())
	event, ok := rec.execEvent(lastExecSeq(recorded))
	if !ok || len(event.Exec.Argv) == 0 {
		return unavailableReproduction(gathered, r, s, rec)
	}
	command := "cd " + console.QuoteArgv([]string{event.Exec.Dir}) +
		" && " + activationVariable + "=" + s.id() + " " + console.QuoteArgv(event.Exec.Argv)
	note := temporariesNote(rec)
	kept := rec.keptTemporaries()
	gathered.Available = true
	gathered.Command = &command
	gathered.Dir = &event.Exec.Dir
	gathered.Argv = append(gathered.Argv, event.Exec.Argv...)
	gathered.Activation = &accountActivation{Variable: activationVariable, Value: s.id()}
	gathered.TemporariesKept = &kept
	gathered.Note = &note
	if rebuild := rebuildLine(s, rec); rebuild != "" {
		gathered.Rebuild = &rebuild
	}
	return gathered
}

// unavailableReproduction is what the account says when there is no argument
// vector to paste: why there is none, the command the run measured with, and
// the invocation that would record one.
//
// The two ways there can be none are said apart, because they are two different
// pieces of news. A run that recorded nothing may yet have executed this mutant
// -- run it again with `--trace` and the vector is there. A run that recorded
// everything and holds no command for this mutant started no process for it at
// all, which is a fact about the mutant rather than about the recording, and no
// amount of re-running with `--trace` will produce one.
func unavailableReproduction(
	gathered accountReproduction, r *report.Report, s subject, rec *recording,
) accountReproduction {
	runID := ""
	if r != nil {
		runID = r.RunID
	}
	var reason string
	switch {
	case rec.present() && !rec.describes(runID):
		// A recording of somebody else's run is no evidence about this one:
		// "this run started no process for it" would be a claim about a run
		// whose account nobody has read.
		reason = "the recording read is of another run, so it holds no command for this mutant"
	case rec.present():
		reason = "this run started no process for it, so its recording holds no argument vector to paste"
	default:
		reason = "no recording, so there is no argument vector to paste"
	}
	gathered.UnavailableReason = &reason
	if r != nil {
		gathered.TestCommand = append(gathered.TestCommand, testCommandOf(r)...)
	}
	gathered.Suggestion = "go-mutants run --mutant " + shortID(s.displayID()) + " --keep-temp -vv --trace"
	return gathered
}

// rejectedReproduction is the reproduce block of a mutant that does not
// compile. There is no binary and there never was one, so what is offered is
// the way to make the compiler say it again.
func rejectedReproduction(s subject) accountReproduction {
	reason := "validation refused this mutant, so no test binary was ever built with it in"
	return accountReproduction{
		UnavailableReason: &reason,
		Argv:              []string{},
		TestCommand:       []string{},
		Suggestion:        "go-mutants run --mutant " + shortID(s.displayID()) + " --explain",
	}
}

// rebuildLine is how to compile a library session's test binary again.
//
// A session's instrumented tree is compiled through an overlay whose manifest
// lives in a scratch directory named when the session was prepared, so it
// cannot be derived and has to be carried. It is docs/library.md's own recipe --
// `cd <snapshot> && GOFLAGS=-overlay=<manifest> go test -c …` -- rendered rather
// than assembled by hand, and it is the one place GOFLAGS does anything, because
// here the command really is the `go` command.
//
// A CLI run records no manifest and gets no line: its snapshot is instrumented
// in place, so `go test -c` inside it needs nothing added.
func rebuildLine(s subject, rec *recording) string {
	manifest := rec.artifact(trace.ArtifactOverlayManifest)
	if manifest == "" {
		return ""
	}
	line := "GOFLAGS=-overlay=" + console.QuoteArgv([]string{manifest}) + " go test -c -o mutant.test"
	if s.mutant != nil && s.mutant.Package != "" {
		line += " " + console.QuoteArgv([]string{s.mutant.Package})
	}
	if snapshot := rec.snapshotDir(); snapshot != "" {
		line = "cd " + console.QuoteArgv([]string{snapshot}) + " && " + line
	}
	return line
}

// gatherPosition builds the account of one place in the source.
//
// Both halves are gathered, always, and that is the point of the command in
// this form. Somebody asking about a line is asking "there should be a mutant
// here", and the answer is either "there is, and here is what happened to it"
// or "there is not, and here is the reason discovery gives" -- so an account
// that held only the first would read as though the second did not exist.
func gatherPosition(
	r *report.Report, source string, where position,
	sites []discover.SkipSite, mutants []catalogMutant, outcomes map[string]string,
) explainPositionDocument {
	doc := explainPositionDocument{
		DocumentType:  schemas.ExplainV1,
		SchemaVersion: explainSchemaVersion,
		ToolVersion:   Version,
		Source:        gatherSource(r, source, nil),
		Subject:       accountPosition{Kind: "position", Path: where.path},
		SkipSites:     []accountSkipSite{},
		Mutants:       []accountCandidate{},
	}
	if where.line > 0 {
		line := where.line
		doc.Subject.Line = &line
	}
	for _, site := range sites {
		if !where.holdsSite(site.Path, site.Line) {
			continue
		}
		row := accountSkipSite{Path: site.Path, Reason: string(site.Reason)}
		if site.Line > 0 {
			// A whole-file reason has no coordinate: such a file is never
			// opened, so a line here would be an invention.
			line, column := site.Line, site.Column
			row.Line, row.Column = &line, &column
		}
		if site.Rule != "" {
			rule := site.Rule
			row.Rule = &rule
		}
		doc.SkipSites = append(doc.SkipSites, row)
	}
	for _, m := range mutants {
		if !where.holdsSpan(m.Path, m.Line, coverage.EndLine(m.Line, m.Original)) {
			continue
		}
		row := accountCandidate{
			ID: m.ID, DisplayID: m.DisplayID,
			Path: m.Path, Line: m.Line, Column: m.Column,
			Family: m.Family, Rule: m.Rule,
			Original: m.Original, Replacement: m.Replacement,
		}
		if outcomes != nil {
			if outcome, ok := outcomes[m.ID]; ok {
				row.Outcome = &outcome
			}
		}
		doc.Mutants = append(doc.Mutants, row)
	}
	return doc
}

// writeExplainJSON writes one explanation and nothing else.
//
// HTML escaping is off for [writeCatalogJSON]'s reason: there is no HTML here,
// and with it on every `<` in a comparison operator would be written as `<`
// -- the same string to a parser and unreadable to everybody else.
func writeExplainJSON(w io.Writer, doc any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(doc)
}
