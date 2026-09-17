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

const explainSchemaVersion = 1

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

	ReportHasTiming bool `json:"-"`
}

type explainPositionDocument struct {
	DocumentType  string `json:"document_type"`
	SchemaVersion int    `json:"schema_version"`
	ToolVersion   string `json:"tool_version"`

	Source    accountSource      `json:"source"`
	Subject   accountPosition    `json:"subject"`
	SkipSites []accountSkipSite  `json:"skip_sites"`
	Mutants   []accountCandidate `json:"mutants"`
}

type accountSource struct {
	Report   *accountReport `json:"report"`
	Trace    *accountTrace  `json:"trace"`
	Warnings []string       `json:"warnings"`
}

type accountReport struct {
	Path   string `json:"path"`
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

type accountTrace struct {
	Stream             string `json:"stream"`
	Directory          string `json:"directory"`
	RunID              string `json:"run_id"`
	Kind               string `json:"kind"`
	DescribesTheReport bool   `json:"describes_the_report"`
}

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

type accountPosition struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line *int   `json:"line"`
}

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

type accountMemory struct {
	Exceeded   bool   `json:"exceeded"`
	PeakBytes  *int64 `json:"peak_bytes"`
	BoundBytes *int64 `json:"bound_bytes"`
}

type accountCoverage struct {
	Mode      string           `json:"mode"`
	Summary   string           `json:"summary"`
	Uncovered bool             `json:"uncovered"`
	Packages  []string         `json:"packages"`
	Tests     []accountTestRef `json:"tests"`
}

type accountTestRef struct {
	Package string `json:"package"`
	Name    string `json:"name"`
}

type accountExecution struct {
	Attempt         int              `json:"attempt"`
	Worker          int              `json:"worker"`
	Outcome         string           `json:"outcome"`
	DurationMS      int64            `json:"duration_ms"`
	PeakMemoryBytes *int64           `json:"peak_memory_bytes"`
	KilledBy        *string          `json:"killed_by"`
	Diverged        bool             `json:"diverged"`
	Binaries        []string         `json:"binaries"`
	Commands        []accountCommand `json:"commands"`
}

type accountCommand struct {
	Seq         int64    `json:"seq"`
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

	event trace.Event
}

type accountStage struct {
	Phase      string `json:"phase"`
	Stage      string `json:"stage"`
	Result     string `json:"result"`
	DurationMS *int64 `json:"duration_ms"`
	ShareMS    int64  `json:"share_ms"`
	Closed     bool   `json:"closed"`
}

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

type accountActivation struct {
	Variable string `json:"variable"`
	Value    string `json:"value"`
}

type accountSkipSite struct {
	Path   string  `json:"path"`
	Line   *int    `json:"line"`
	Column *int    `json:"column"`
	Reason string  `json:"reason"`
	Rule   *string `json:"rule"`
}

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

const activationVariable = "GO_MUTANTS_ACTIVE"

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

func foreignRecordingWarning(recordingRun, reportRun string) string {
	return "that recording is of run " + recordingRun + ", not " + reportRun +
		"; everything below it came out of that run"
}

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

const rejectedSummary = "rejected: the instrumented snapshot would not compile with it spliced in"

func positive(v int64) *int64 {
	if v <= 0 {
		return nil
	}
	return &v
}

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
			Diverged:        execution.Diverged,
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

func rejectedReproduction(s subject) accountReproduction {
	reason := "validation refused this mutant, so no test binary was ever built with it in"
	return accountReproduction{
		UnavailableReason: &reason,
		Argv:              []string{},
		TestCommand:       []string{},
		Suggestion:        "go-mutants run --mutant " + shortID(s.displayID()) + " --explain",
	}
}

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

func writeExplainJSON(w io.Writer, doc any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(doc)
}
