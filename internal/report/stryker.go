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
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/schema/stryker"
)

const (
	ProjectionFileName = "mutation.json"
	HTMLFileName       = "mutation.html"
)

const ProjectionLanguage = "go"

const (
	StatusKilled       = "Killed"
	StatusSurvived     = "Survived"
	StatusTimeout      = "Timeout"
	StatusIgnored      = "Ignored"
	StatusRuntimeError = "RuntimeError"
	StatusCompileError = "CompileError"
)

type Projection struct {
	SchemaVersion string                     `json:"schemaVersion"`
	Thresholds    ProjectionThresholds       `json:"thresholds"`
	Files         map[string]*ProjectionFile `json:"files"`
}

type ProjectionThresholds struct {
	High int `json:"high"`
	Low  int `json:"low"`
}

type ProjectionFile struct {
	Language string             `json:"language"`
	Source   string             `json:"source"`
	Mutants  []ProjectionMutant `json:"mutants"`
}

type ProjectionMutant struct {
	ID           string   `json:"id"`
	MutatorName  string   `json:"mutatorName"`
	Description  string   `json:"description,omitempty"`
	Location     Location `json:"location"`
	Status       string   `json:"status"`
	StatusReason string   `json:"statusReason,omitempty"`
}

type ProjectionOptions struct {
	Report        *Report
	WorkspaceRoot string
	PathPrefix    string
	High          int
	Low           int
}

func Project(opts ProjectionOptions) (*Projection, error) {
	if opts.Report == nil {
		return nil, &Error{
			Code:    CodeNoReport,
			Message: "there is no report to project into the mutation-testing-report format",
		}
	}
	sources := &sourceReader{root: opts.WorkspaceRoot, indexes: map[string]*sourceIndex{}}
	files := make(map[string]*ProjectionFile, len(opts.Report.Mutants))
	if err := projectInto(files, sources, opts.Report, opts.PathPrefix); err != nil {
		return nil, err
	}
	return &Projection{
		SchemaVersion: stryker.ReportSchemaVersion,
		Thresholds:    ProjectionThresholds{High: opts.High, Low: opts.Low},
		Files:         files,
	}, nil
}

func ProjectWorkspace(opts WorkspaceProjectionOptions) (*Projection, error) {
	if opts.Workspace == nil {
		return nil, &Error{
			Code:    CodeNoReport,
			Message: "there is no workspace report to project into the mutation-testing-report format",
		}
	}
	sources := &sourceReader{root: opts.WorkspaceRoot, indexes: map[string]*sourceIndex{}}
	files := make(map[string]*ProjectionFile)
	for _, module := range opts.Workspace.Modules {
		if err := projectInto(files, sources, module.Report, module.Dir); err != nil {
			return nil, err
		}
	}
	return &Projection{
		SchemaVersion: stryker.ReportSchemaVersion,
		Thresholds:    ProjectionThresholds{High: opts.High, Low: opts.Low},
		Files:         files,
	}, nil
}

type WorkspaceProjectionOptions struct {
	Workspace     *WorkspaceReport
	WorkspaceRoot string
	High          int
	Low           int
}

func projectInto(
	files map[string]*ProjectionFile,
	sources *sourceReader,
	r *Report,
	prefix string,
) error {
	if r == nil {
		return &Error{
			Code:    CodeNoReport,
			Message: "there is no report to project into the mutation-testing-report format",
		}
	}
	for i := range r.Mutants {
		m := &r.Mutants[i]
		where := projectedPath(prefix, m.Path)
		index, err := sources.index(where)
		if err != nil {
			return err
		}
		if err = checkSpan(m, index); err != nil {
			return err
		}
		file := fileFor(files, where, index)
		file.Mutants = append(file.Mutants, projectMutant(m, index))
	}
	for i := range r.Rejected {
		rejected := &r.Rejected[i]
		where := projectedPath(prefix, rejected.Path)
		index, err := sources.index(where)
		if err != nil {
			return err
		}
		file := fileFor(files, where, index)
		file.Mutants = append(file.Mutants, projectRejection(rejected, index))
	}
	for _, file := range files {
		slices.SortFunc(file.Mutants, compareProjected)
	}
	return nil
}

func projectedPath(prefix, rel string) string {
	return path.Join(prefix, rel)
}

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

func fileFor(files map[string]*ProjectionFile, path string, index *sourceIndex) *ProjectionFile {
	if file, ok := files[path]; ok {
		return file
	}
	file := &ProjectionFile{
		Language: ProjectionLanguage,
		Source:   index.src,
		Mutants:  []ProjectionMutant{},
	}
	files[path] = file
	return file
}

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
		return StatusIgnored, "go-mutants recorded an outcome this projection does not know: " +
			strconv.Quote(string(m.Outcome))
	}
}

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

func describeEdit(original, replacement string) string {
	return quoteEdit(original) + " -> " + quoteEdit(replacement)
}

func quoteEdit(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, "\n\r\t") || strings.TrimSpace(s) != s {
		return strconv.Quote(s)
	}
	return s
}

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

func comparePositions(x, y Position) int {
	if c := x.Line - y.Line; c != 0 {
		return c
	}
	return x.Column - y.Column
}

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

type sourceReader struct {
	root    string
	indexes map[string]*sourceIndex
}

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

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
