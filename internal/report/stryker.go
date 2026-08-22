// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/schema/stryker"
)

// The projection is one-way, lossy, and deterministic, and each of those three
// words is a decision worth stating.
//
// **One-way.** [Report] is the source of truth and the only document go-mutants
// reads back. Nothing in this package parses a mutation-testing-report file,
// and nothing should: a format designed for a viewer is a poor place to keep
// the facts a run established, and a round trip through it would quietly become
// the thing everything else trusts.
//
// **Lossy, and named as such.** Six outcomes become five statuses, the
// expectations ledger disappears, the cache accounting disappears, coverage
// disappears, and a rejected mutant arrives with its rule but not its family
// because that is all the rejection row carries. Every one of those facts is in
// `mutation.json`'s sibling, the run report, which is why losing them here
// costs nothing.
//
// **Deterministic.** Two runs that reach the same outcomes over the same tree
// produce byte-identical projections. Map keys are sorted by encoding/json, and
// every array is sorted explicitly before it is encoded — never left in the
// order a map iteration or a worker pool happened to produce.

// The file names the two project artefacts are written under. They are the
// sibling projects' names, so a CI job that already collects
// `reports/mutation/mutation.json` from a Gleam or OCaml repository collects
// this one without being told about it.
const (
	// ProjectionFileName is the mutation-testing-report document.
	ProjectionFileName = "mutation.json"
	// HTMLFileName is the self-contained viewer.
	HTMLFileName = "mutation.html"
)

// ProjectionLanguage is the `language` every file in the projection carries.
// It selects the viewer's syntax highlighting and nothing else.
const ProjectionLanguage = "go"

// The mutant statuses the published format defines, of which the projection
// emits six. `NoCoverage` and `Pending` are deliberately never written: see
// [statusOf].
const (
	StatusKilled       = "Killed"
	StatusSurvived     = "Survived"
	StatusTimeout      = "Timeout"
	StatusIgnored      = "Ignored"
	StatusRuntimeError = "RuntimeError"
	StatusCompileError = "CompileError"
)

// A Projection is one run in the mutation-testing-report format.
//
// The field order is the document's order, and `projectRoot` is deliberately
// absent: it is optional in the format and it is an absolute path on the
// machine that produced the report, which would make two otherwise identical
// runs produce different documents and would leak a developer's directory
// layout into a file that gets attached to pull requests.
type Projection struct {
	SchemaVersion string                     `json:"schemaVersion"`
	Thresholds    ProjectionThresholds       `json:"thresholds"`
	Files         map[string]*ProjectionFile `json:"files"`
}

// ProjectionThresholds are the viewer's colouring thresholds, as percentages.
// They come from `report.high` and `report.low` and are independent of
// [config.Policy]: making a report prettier must never change whether CI passes.
type ProjectionThresholds struct {
	High int `json:"high"`
	Low  int `json:"low"`
}

// A ProjectionFile is one source file and everything proposed for it.
//
// Source is the *pristine* text — the file as the user's tree holds it, not as
// the instrumented snapshot did — because every location in the file refers to
// it and a viewer highlights ranges of it. See [ProjectionOptions.WorkspaceRoot].
type ProjectionFile struct {
	Language string             `json:"language"`
	Source   string             `json:"source"`
	Mutants  []ProjectionMutant `json:"mutants"`
}

// A ProjectionMutant is one mutant as the published format states it.
type ProjectionMutant struct {
	ID          string   `json:"id"`
	MutatorName string   `json:"mutatorName"`
	Description string   `json:"description,omitempty"`
	Location    Location `json:"location"`
	Status      string   `json:"status"`
	// StatusReason explains a status a reader cannot act on: why a mutant was
	// ignored, and what the compiler said about one that would not build. It is
	// omitted where the status speaks for itself.
	StatusReason string `json:"statusReason,omitempty"`
}

// ProjectionOptions is everything [Project] needs.
type ProjectionOptions struct {
	// Report is the run to project. It is read and never modified.
	Report *Report
	// WorkspaceRoot is the directory the report's paths are relative to, and
	// the tree the pristine source is read from.
	//
	// The *user's* tree, deliberately — not the snapshot, which is instrumented
	// by the time there are outcomes to report and deleted by the time there is
	// a report to write. A projection built from instrumented source would show
	// a reader the guard scaffolding instead of their own code, with every
	// location pointing into it.
	WorkspaceRoot string
	// High and Low are the viewer's colouring thresholds.
	High int
	Low  int
}

// Project builds the mutation-testing-report projection of one run.
//
// It reads every file the run has something to say about, and refuses rather
// than guesses when the tree has moved underneath it: a span that no longer
// covers the text the report says it covers means the file was edited while the
// run was in flight, and every coordinate derived from it would be wrong in a
// document that would still validate. See [CodeProjectionSourceDrift].
func Project(opts ProjectionOptions) (*Projection, error) {
	if opts.Report == nil {
		return nil, &Error{
			Code:    CodeNoReport,
			Message: "there is no report to project into the mutation-testing-report format",
		}
	}
	sources := &sourceReader{root: opts.WorkspaceRoot, indexes: map[string]*sourceIndex{}}
	files := make(map[string]*ProjectionFile, len(opts.Report.Mutants))

	for i := range opts.Report.Mutants {
		m := &opts.Report.Mutants[i]
		index, err := sources.index(m.Path)
		if err != nil {
			return nil, err
		}
		if err = checkSpan(m, index); err != nil {
			return nil, err
		}
		file := fileFor(files, m.Path, index)
		file.Mutants = append(file.Mutants, projectMutant(m, index))
	}
	for i := range opts.Report.Rejected {
		r := &opts.Report.Rejected[i]
		index, err := sources.index(r.Path)
		if err != nil {
			return nil, err
		}
		file := fileFor(files, r.Path, index)
		file.Mutants = append(file.Mutants, projectRejection(r, index))
	}
	for _, file := range files {
		slices.SortFunc(file.Mutants, compareProjected)
	}

	return &Projection{
		// The report format's major version, which is 2. It is *not* the
		// version of the npm package the schema was vendored from; see
		// [stryker.ReportSchemaVersion], which is where the trap is written
		// down.
		SchemaVersion: stryker.ReportSchemaVersion,
		Thresholds:    ProjectionThresholds{High: opts.High, Low: opts.Low},
		Files:         files,
	}, nil
}

// Marshal encodes the projection as the bytes `mutation.json` holds.
//
// The settings match [Report.Marshal] — no HTML escaping, two spaces of
// indentation, the encoder's trailing newline — so that the two documents a run
// writes are diffable the same way. The HTML report does not use these bytes
// unaltered; see [escapeScriptData] for the four characters a document embedded
// in a page has to spell differently.
func (p *Projection) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(p); err != nil {
		return nil, &Error{
			Code:    CodeEncodeFailed,
			Message: "the mutation-testing-report projection could not be encoded as JSON",
			Err:     err,
		}
	}
	return buf.Bytes(), nil
}

// fileFor returns the projected file for a path, creating it from the indexed
// source the first time.
func fileFor(files map[string]*ProjectionFile, path string, index *sourceIndex) *ProjectionFile {
	if file, ok := files[path]; ok {
		return file
	}
	file := &ProjectionFile{
		Language: ProjectionLanguage,
		Source:   index.src,
		// Non-nil so that a file with only rejections still encodes `[]`
		// rather than `null`, which the format's `mutants` array requires.
		Mutants: []ProjectionMutant{},
	}
	files[path] = file
	return file
}

// projectMutant converts one measured mutant.
func projectMutant(m *Mutant, index *sourceIndex) ProjectionMutant {
	status, reason := statusOf(m)
	return ProjectionMutant{
		ID:          m.DisplayID,
		MutatorName: m.Family + "/" + m.Rule,
		Description: describeEdit(m.Original, m.Replacement),
		Location: Location{
			Start: index.position(int(m.StartByte)),
			End:   index.position(int(m.EndByte)),
		},
		Status:       status,
		StatusReason: reason,
	}
}

// projectRejection converts one mutant validation refused.
//
// Two things are thinner here than for a measured mutant, and both are the
// rejection row's own limits rather than choices. The row records the rule but
// not the family it belongs to, so `mutatorName` is the rule alone. And it
// records the coordinate discovery printed rather than the span, so the
// location is the zero-width point where the edit would have started: claiming
// an end the document does not know would be inventing a range, and a viewer
// marks a zero-width location at exactly the character the compiler complained
// about.
func projectRejection(r *Rejected, index *sourceIndex) ProjectionMutant {
	at := index.position(index.offsetAt(r.Line, r.Column))
	return ProjectionMutant{
		ID:           r.DisplayID,
		MutatorName:  r.Rule,
		Location:     Location{Start: at, End: at},
		Status:       StatusCompileError,
		StatusReason: firstLine(r.Diagnostic),
	}
}

// statusOf maps one outcome onto the published format's status, and says why
// where the status alone would not.
//
// Two of the format's statuses are never written and their absence is
// deliberate. `Pending` describes a run still in progress, and every document
// this package writes describes a run that has stopped. `NoCoverage` looks like
// the right answer for a mutant no test binary reaches — and it is not the one
// this projection gives, because the run report's own vocabulary calls that
// mutant a survivor and the two documents must agree about how many survivors
// there were. The uncovered half of the survivors is a fact the run report
// keeps and this one drops, which is what "lossy" means.
func statusOf(m *Mutant) (status, reason string) {
	switch m.Outcome {
	case OutcomeKilled:
		return StatusKilled, ""
	case OutcomeSurvived:
		return StatusSurvived, ""
	case OutcomeTimedOut:
		return StatusTimeout, ""
	case OutcomeErrored:
		return StatusRuntimeError, ""
	case OutcomeInconclusive:
		return StatusIgnored, "go-mutants could not settle this mutant: it timed out once and did not " +
			"time out again when it was retried on its own, so it counts neither as a detection nor as a survivor"
	case OutcomeNotRun:
		return StatusIgnored, notRunExplanation(m.NotRunReason)
	default:
		// Unreachable for a document [Build] produced, which refuses an
		// outcome outside the six. Ignored rather than guessed at, because a
		// status invented here would be a claim about a mutant nothing knows
		// anything about.
		return StatusIgnored, "go-mutants recorded an outcome this projection does not know: " +
			strconv.Quote(string(m.Outcome))
	}
}

// notRunExplanation words a not-run reason for a reader of the viewer, who has
// no run report in front of them and no way to guess which of three quite
// different things "ignored" meant.
func notRunExplanation(reason *string) string {
	if reason == nil {
		return "this mutant was not executed"
	}
	switch NotRunReason(*reason) {
	case NotRunInterrupted:
		return "the run was interrupted before this mutant was measured"
	case NotRunOutOfSelection:
		return "this run narrowed itself with --mutant or --changed and did not select this mutant"
	case NotRunOtherShard:
		return "another shard of this run measured this mutant; `go-mutants report merge` combines the shards"
	default:
		return "this mutant was not executed: " + *reason
	}
}

// describeEdit renders the edit for the viewer's mutant list.
//
// The rule is [console.FormatText]'s, and it is written twice on purpose: one
// line of clean text is printed as it stands, so `== -> !=` reads like the edit
// it is, and anything with a newline, a tab, or padding in it is quoted so that
// a statement deletion cannot break the line it is shown on. internal/console
// cannot be imported here — it imports this package — and a shared helper
// package for six lines would be a worse trade than the duplication.
func describeEdit(original, replacement string) string {
	return quoteEdit(original) + " -> " + quoteEdit(replacement)
}

// quoteEdit renders one side of an edit. See [describeEdit].
func quoteEdit(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, "\n\r\t") || strings.TrimSpace(s) != s {
		return strconv.Quote(s)
	}
	return s
}

// compareProjected orders the mutants within one file.
//
// By position first, because that is the order a reader scrolling the file
// meets them in, and then by the two fields that break a tie between mutants at
// the same place: several rules can propose an edit at one byte, and each
// mutant's id is unique. The order is total, so the encoded array is a function
// of the run and not of the order the workers finished in.
func compareProjected(x, y ProjectionMutant) int {
	if c := comparePositions(x.Location.Start, y.Location.Start); c != 0 {
		return c
	}
	if c := comparePositions(x.Location.End, y.Location.End); c != 0 {
		return c
	}
	if c := strings.Compare(x.MutatorName, y.MutatorName); c != 0 {
		return c
	}
	return strings.Compare(x.ID, y.ID)
}

// comparePositions orders two positions by line and then column.
func comparePositions(x, y Position) int {
	if c := x.Line - y.Line; c != 0 {
		return c
	}
	return x.Column - y.Column
}

// checkSpan proves the pristine file still holds the text the report says the
// mutant covers.
//
// It is the one check that turns a silently wrong document into a refusal. Every
// location in the projection is derived from the span, the schema requires
// nothing of a line number beyond being at least 1, and a viewer given a
// coordinate into a file that has since been edited highlights whatever happens
// to be there now. Editing a source file while a run is in flight is an ordinary
// thing to do — it is what a developer does while waiting — so the case is real
// rather than theoretical, and it is caught here because this is the first step
// that reads the tree again after the run copied it.
func checkSpan(m *Mutant, index *sourceIndex) error {
	start, end := int(m.StartByte), int(m.EndByte)
	if start > end || end > index.size() || index.src[start:end] != m.Original {
		return &Error{
			Code: CodeProjectionSourceDrift,
			Message: fmt.Sprintf(
				"%s no longer holds the text mutant %s was built from at bytes [%d,%d): the file changed after the run read it, "+
					"so no location in a report about it could be trusted",
				m.Path, m.DisplayID, start, end),
		}
	}
	return nil
}

// A sourceReader reads and indexes each pristine file once.
type sourceReader struct {
	root    string
	indexes map[string]*sourceIndex
}

// index returns the index for one module-relative path, reading the file the
// first time it is asked for.
//
// The path is rejoined against the workspace root with [filepath.Join], which
// also cleans it, and the result is checked to be inside the root: the paths
// come from a document, a document can be edited, and a report that could be
// made to read `../../.ssh/id_rsa` into a file somebody publishes would be a
// poor thing to have written.
func (s *sourceReader) index(path string) (*sourceIndex, error) {
	if index, ok := s.indexes[path]; ok {
		return index, nil
	}
	full, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		what := "could not be read"
		if errors.Is(err, fs.ErrNotExist) {
			what = "is not there any more"
		}
		return nil, &Error{
			Code: CodeProjectionSourceUnreadable,
			Message: "the mutation report needs the source of " + path + ", and " + full + " " + what +
				": a report whose files are empty would show a reader mutants pointing into nothing",
			Err: err,
		}
	}
	index := newSourceIndex(data)
	s.indexes[path] = index
	return index, nil
}

// resolve turns a module-relative path from the document into an absolute one
// inside the workspace.
func (s *sourceReader) resolve(path string) (string, error) {
	full := filepath.Join(s.root, filepath.FromSlash(path))
	rel, err := filepath.Rel(s.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", &Error{
			Code: CodeProjectionSourceUnreadable,
			Message: "the report names a source file outside the workspace (" + strconv.Quote(path) +
				"), which no run go-mutants performed could have produced",
		}
	}
	return full, nil
}

// firstLine reduces a compiler diagnostic to the one line a status reason can
// carry, so that a multi-line build failure cannot turn the viewer's mutant
// list into a wall of text. The whole diagnostic stays in the run report's
// `rejected[]`, which is where somebody debugging a rejection is looking.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
