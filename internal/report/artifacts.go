// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/P4suta/go-mutants/internal/config"
)

type Artifacts struct {
	ProjectionPath string
	HTMLPath       string
}

func (a Artifacts) Any() bool { return a.ProjectionPath != "" || a.HTMLPath != "" }

type ArtifactOptions struct {
	Report        *Report
	Workspace     *WorkspaceReport
	WorkspaceRoot string
	Directory     string
	Formats       []config.ReportFormat
	High          int
	Low           int
}

func projectionOf(opts ArtifactOptions) (*Projection, error) {
	if opts.Workspace != nil {
		return ProjectWorkspace(WorkspaceProjectionOptions{
			Workspace:     opts.Workspace,
			WorkspaceRoot: opts.WorkspaceRoot,
			High:          opts.High,
			Low:           opts.Low,
		})
	}
	return Project(ProjectionOptions{
		Report:        opts.Report,
		WorkspaceRoot: opts.WorkspaceRoot,
		High:          opts.High,
		Low:           opts.Low,
	})
}

func WriteArtifacts(opts ArtifactOptions) (Artifacts, error) {
	wantJSON := slices.Contains(opts.Formats, config.FormatJSON)
	wantHTML := slices.Contains(opts.Formats, config.FormatHTML)
	if !wantJSON && !wantHTML {
		return Artifacts{}, nil
	}
	if opts.Report == nil && opts.Workspace == nil {
		return Artifacts{}, &Error{
			Code:    CodeNoReport,
			Message: "there is no report to publish into " + opts.Directory,
		}
	}

	projection, err := projectionOf(opts)
	if err != nil {
		return Artifacts{}, err
	}
	document, err := projection.Marshal()
	if err != nil {
		return Artifacts{}, err
	}
	if err = ValidateProjection(document); err != nil {
		return Artifacts{}, err
	}

	dir, err := artifactDirectory(opts)
	if err != nil {
		return Artifacts{}, err
	}

	var (
		written  Artifacts
		rollback func() error
	)
	if wantJSON {
		path := filepath.Join(dir, ProjectionFileName)
		previous, existed, readErr := currentContents(path)
		if readErr != nil {
			return Artifacts{}, readErr
		}
		if err = writeArtifactFile(path, ProjectionFileName, document); err != nil {
			return Artifacts{}, err
		}
		written.ProjectionPath = path
		rollback = func() error { return restoreArtifact(path, previous, existed) }
	}

	if wantHTML {
		path := filepath.Join(dir, HTMLFileName)
		page, renderErr := RenderHTML(document)
		if renderErr == nil {
			renderErr = writeArtifactFile(path, HTMLFileName, page)
		}
		if renderErr != nil {
			return Artifacts{}, undo(rollback, renderErr)
		}
		written.HTMLPath = path
	}
	return written, nil
}

func artifactDirectory(opts ArtifactOptions) (string, error) {
	dir := opts.Directory
	if dir == "" {
		dir = config.DefaultReportDirectory
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(dir))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", &Error{
			Code:    CodeArtifactDirectory,
			Message: "the report directory " + dir + " could not be created",
			Err:     err,
		}
	}
	return dir, nil
}

func writeArtifactFile(path, what string, data []byte) error {
	staged, err := writeTemp(filepath.Dir(path), what, data)
	if err != nil {
		return &Error{
			Code:    CodeArtifactWrite,
			Message: what + " could not be staged in " + filepath.Dir(path),
			Err:     err,
		}
	}
	defer func() { _ = os.Remove(staged) }()

	if err = rename(staged, path); err != nil {
		return &Error{
			Code:    CodeArtifactWrite,
			Message: what + " could not be moved into place at " + path,
			Err:     err,
		}
	}
	return nil
}

func currentContents(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, &Error{
			Code: CodeArtifactWrite,
			Message: path + " could not be read, so it could not be safely replaced: " +
				"the artefacts are published as a pair, and a file that cannot be read cannot be put back",
			Err: err,
		}
	}
	return data, true, nil
}

func restoreArtifact(path string, previous []byte, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return &Error{
				Code:    CodeArtifactRollback,
				Message: path + " was written and could not be removed again",
				Err:     err,
			}
		}
		return nil
	}
	if err := writeArtifactFile(path, filepath.Base(path), previous); err != nil {
		return &Error{
			Code:    CodeArtifactRollback,
			Message: path + " could not be restored to the contents it had before this run",
			Err:     err,
		}
	}
	return nil
}

func undo(rollback func() error, cause error) error {
	if rollback == nil {
		return cause
	}
	rollbackErr := rollback()
	if rollbackErr == nil {
		return cause
	}
	var coded *Error
	if !errors.As(rollbackErr, &coded) {
		return errors.Join(cause, rollbackErr)
	}
	return &Error{
		Code:    coded.Code,
		Message: coded.Message + ", after " + HTMLFileName + " could not be written",
		Err:     errors.Join(cause, coded.Err),
	}
}
