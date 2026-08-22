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

// The project artefacts are the two files a run leaves in the user's own tree:
// `reports/mutation/mutation.json` and `reports/mutation/mutation.html`.
//
// They are the only files go-mutants writes into a workspace. Everything else
// it produces goes into a disposable snapshot or into the operating system's
// cache directory, and the exception is deliberate: a report nobody can find is
// a report nobody reads, and `reports/mutation/` is the path the sibling
// projects established, is excluded from the snapshot manifest, and is in the
// generated `.gitignore`.
//
// # Published together or not at all
//
// The two files are one publication in two formats. A `mutation.json` from this
// run beside a `mutation.html` from last week is worse than either file alone,
// because the two disagree and nothing in either says which is newer — so if
// the HTML cannot be written, the JSON written moments before is put back
// exactly as it was found, and the run reports the failure rather than a
// half-published pair. That is the same argument the history store makes for
// writing the run document before the `latest.json` pointer, in the one
// direction it can be made here.
//
// Both files are staged in the destination directory and renamed into place, so
// a crash leaves either the previous pair or the new one; see [writeAtomic] for
// why the temporary file has to be in the same directory.

// Artifacts are the paths one publication wrote. A path is empty when the
// format was not asked for — never when it was asked for and failed, because a
// failure is returned instead.
type Artifacts struct {
	// ProjectionPath is the absolute path of `mutation.json`.
	ProjectionPath string
	// HTMLPath is the absolute path of `mutation.html`.
	HTMLPath string
}

// Any reports whether anything was written.
func (a Artifacts) Any() bool { return a.ProjectionPath != "" || a.HTMLPath != "" }

// ArtifactOptions is everything [WriteArtifacts] needs.
type ArtifactOptions struct {
	// Report is the run to publish. It is read and never modified.
	Report *Report
	// WorkspaceRoot is the absolute path of the user's tree: what the report's
	// paths are relative to, what the pristine source is read from, and what a
	// relative Directory resolves against.
	WorkspaceRoot string
	// Directory is `report.directory`. A relative path resolves under
	// WorkspaceRoot, which is every configured value: internal/config refuses
	// an absolute or escaping `report.directory`, because the artefacts are a
	// project's own output and belong beside the project.
	//
	// An absolute path is nonetheless used as it stands rather than rejected
	// again here. This is a library entry point, the resolution rule has to be
	// stated one way or the other, and "joined onto the workspace root" is not
	// a sensible reading of an absolute path.
	Directory string
	// Formats is `report.formats` as the configuration resolved it. An empty
	// slice writes nothing at all and is a supported answer — it is what
	// `--report none` means — and it is honoured before anything is read, so
	// that turning the artefacts off also turns off the work of building them.
	Formats []config.ReportFormat
	// High and Low are the viewer's colouring thresholds.
	High int
	Low  int
}

// WriteArtifacts publishes the project artefacts for one run.
//
// The order is the contract. The projection is built, encoded, and validated
// against the vendored schema *before* the destination directory is touched, so
// a document that would not validate costs nothing and leaves nothing behind;
// see [ValidateProjection]. The HTML is then rendered from those same validated
// bytes, so the page and the file beside it hold one document rather than two
// encodings of one idea.
//
// A failure at any point leaves the directory as it was found. In particular a
// failure to write the HTML puts `mutation.json` back — restored to its previous
// contents, or removed when there were none — because the two files are one
// publication; see the file comment.
func WriteArtifacts(opts ArtifactOptions) (Artifacts, error) {
	wantJSON := slices.Contains(opts.Formats, config.FormatJSON)
	wantHTML := slices.Contains(opts.Formats, config.FormatHTML)
	if !wantJSON && !wantHTML {
		return Artifacts{}, nil
	}
	if opts.Report == nil {
		return Artifacts{}, &Error{
			Code:    CodeNoReport,
			Message: "there is no report to publish into " + opts.Directory,
		}
	}

	projection, err := Project(ProjectionOptions{
		Report:        opts.Report,
		WorkspaceRoot: opts.WorkspaceRoot,
		High:          opts.High,
		Low:           opts.Low,
	})
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

// artifactDirectory resolves `report.directory` and makes sure it is there.
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

// writeArtifactFile stages one artefact beside its destination and renames it
// into place.
//
// It is [writeAtomic]'s argument applied to a file in the user's tree rather
// than in the cache: same directory, therefore same volume, therefore a rename
// that is a rename. The wrapped error keeps the staging failure's own detail —
// which names the file and the directory a full disk stopped — underneath this
// package's artefact code.
func writeArtifactFile(path, what string, data []byte) error {
	staged, err := writeTemp(filepath.Dir(path), what, data)
	if err != nil {
		return &Error{
			Code:    CodeArtifactWrite,
			Message: what + " could not be staged in " + filepath.Dir(path),
			Err:     err,
		}
	}
	// Removed on every failing path. A successful rename makes this a no-op
	// against a name that no longer exists, which is why the error is dropped.
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

// currentContents reads what is at path now, so that it can be put back.
//
// A file that is not there reports (nil, false, nil), which is a rollback that
// removes rather than restores. Anything else that stops the read is returned:
// a file that cannot be read is a file that cannot be restored, and overwriting
// it would destroy something with no way back.
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

// restoreArtifact puts one artefact back the way it was found.
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

// undo runs a rollback and returns the error the caller should report.
//
// The original failure is what the user needs to act on, so it is the one
// returned when the rollback succeeds. When the rollback also fails the answer
// is the rollback's error — a half-published pair is the more urgent fact, and
// somebody who has one needs to be told that rather than why the HTML would not
// write — with the original still reachable through errors.Is and errors.As, so
// nothing is lost.
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
