// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/trace"
)

const (
	diagnosticsDirectoryName = "diagnostics"

	errorFileName          = "error.txt"
	environmentFileName    = "environment.txt"
	doctorFileName         = "doctor.txt"
	reportFileName         = "report.json"
	manifestFileName       = "manifest.json"
	preservedPathsFileName = "preserved-paths.txt"

	environmentHeading = "environment:"

	nothingKept = "# this run left nothing behind"

	bundleDirPerm  fs.FileMode = 0o755
	bundleFilePerm fs.FileMode = 0o644
)

const diagnosticsBudget = 30 * time.Second

type diagnosticsRequest struct {
	workspace       string
	reportDirectory string
	runID           string
	traceDirectory  string
	events          []trace.Event
	err             error
	outcome         engine.RunOutcome
	environ         []string
	hooks           trace.Filesystem
}

var bundleContents = map[string]string{
	errorFileName:             "the failure as the console printed it, then in full, then the typed chain underneath it",
	environmentFileName:       "the build, the platform, the working directory, the toolchain, and the names -- never the values -- of the environment variables the process was started with",
	doctorFileName:            "`go-mutants doctor` run against this workspace after the failure",
	reportFileName:            "the run report, when the run got far enough to build one",
	manifestFileName:          "this index",
	preservedPathsFileName:    "the temporary directories the run was asked to keep, and the sentence that says so when it kept none",
	trace.FileName:            "the recording of the run, as JSON Lines answering schema/trace-v1.schema.json",
	trace.OutputDirectoryName: "the captured output of the commands the recording digested, one file per command, tail-truncated",
}

type diagnosticsManifest struct {
	DocumentType  string           `json:"document_type"`
	SchemaVersion int              `json:"schema_version"`
	ToolVersion   string           `json:"tool_version"`
	RunID         string           `json:"run_id"`
	Platform      manifestPlatform `json:"platform"`
	Failure       manifestFailure  `json:"failure"`
	Files         []manifestFile   `json:"files"`
	Preserved     []string         `json:"preserved,omitempty"`
}

type manifestPlatform struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

type manifestFailure struct {
	Code    string `json:"code,omitempty"`
	Summary string `json:"summary"`
}

type manifestFile struct {
	Name  string `json:"name"`
	Holds string `json:"holds"`
}

func manifestFor(request diagnosticsRequest, written []string) diagnosticsManifest {
	rendered := strings.TrimRight(func() string {
		var b strings.Builder
		RenderError(&b, request.err)
		return b.String()
	}(), "\n")
	summary := firstLine(rendered)
	summary = strings.TrimPrefix(summary, "error ")
	code, rest, coded := splitCode(summary)
	if coded {
		summary = rest
	} else {
		code = ""
	}
	if summary == "" {
		summary = "the run failed without a message the console prints"
	}
	files := make([]manifestFile, 0, len(written))
	names := slices.Clone(written)
	slices.Sort(names)
	for _, name := range names {
		holds, ok := bundleContents[name]
		if !ok {
			holds = "a file this build has no description for, which is a bug in bundleContents"
		}
		files = append(files, manifestFile{Name: name, Holds: holds})
	}
	preserved := make([]string, 0, len(request.outcome.Preserved))
	for _, dir := range request.outcome.Preserved {
		preserved = append(preserved, dir.Path)
	}
	slices.Sort(preserved)
	return diagnosticsManifest{
		DocumentType:  schemas.DiagnosticsV1,
		SchemaVersion: 1,
		ToolVersion:   Version,
		RunID:         request.runID,
		Platform:      manifestPlatform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		Failure:       manifestFailure{Code: code, Summary: summary},
		Files:         files,
		Preserved:     preserved,
	}
}

func bundleEntries(directory string, written []string) []string {
	names := append(slices.Clone(written), manifestFileName, preservedPathsFileName)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return names
	}
	for _, entry := range entries {
		if !slices.Contains(names, entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names
}

func writeDiagnostics(ctx context.Context, request diagnosticsRequest) (path string, err error) {
	mkdirAll, writeFile := bundleHooks(request.hooks)
	directory, ours, err := bundleDirectory(request, mkdirAll)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil && ours {
			_ = os.Remove(directory)
		}
	}()

	var written []string
	write := func(name, content string) error {
		if content == "" {
			return nil
		}
		if writeErr := writeFile(filepath.Join(directory, name), []byte(content), bundleFilePerm); writeErr != nil {
			return writeErr
		}
		written = append(written, name)
		return nil
	}
	if err = write(errorFileName, errorReport(request.err)); err != nil {
		return "", err
	}
	if err = write(environmentFileName, environmentReport(request)); err != nil {
		return "", err
	}
	if err = write(doctorFileName, renderChecks(diagnose(ctx, request.workspace))); err != nil {
		return "", err
	}
	if request.traceDirectory == "" {
		if err = write(trace.FileName, eventStream(request.events)); err != nil {
			return "", err
		}
	}
	if published := publishedDocument(request.outcome); published != nil {
		document, marshalErr := published.Marshal()
		if marshalErr != nil {
			return "", marshalErr
		}
		if err = write(reportFileName, string(document)); err != nil {
			return "", err
		}
	}
	manifest, marshalErr := json.MarshalIndent(
		manifestFor(request, bundleEntries(directory, written)), "", "  ")
	if marshalErr != nil {
		return "", marshalErr
	}
	if err = write(manifestFileName, string(manifest)+"\n"); err != nil {
		return "", err
	}
	if err = write(preservedPathsFileName, preservedReport(request.outcome.Preserved)); err != nil {
		return "", err
	}
	return directory, nil
}

func bundleDirectory(request diagnosticsRequest, mkdirAll func(string, fs.FileMode) error) (
	directory string, ours bool, err error,
) {
	if request.traceDirectory != "" {
		return request.traceDirectory, false, nil
	}
	root, err := diagnosticsRoot(request.workspace, request.reportDirectory)
	if err != nil {
		return "", false, err
	}
	_, _ = collect(diagnosticsRootAt(root), retention{keep: trace.RetainRuns})

	directory = filepath.Join(root, request.runID)
	if err = mkdirAll(directory, bundleDirPerm); err != nil {
		return "", false, err
	}
	return directory, true, nil
}

func bundleHooks(hooks trace.Filesystem) (
	mkdirAll func(string, fs.FileMode) error,
	writeFile func(string, []byte, fs.FileMode) error,
) {
	mkdirAll, writeFile = hooks.MkdirAll, hooks.WriteFile
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	return mkdirAll, writeFile
}

func errorReport(err error) string {
	if err == nil {
		return ""
	}
	var b strings.Builder
	RenderError(&b, err)
	b.WriteString("\n")
	fmt.Fprintf(&b, "%+v\n", err)
	b.WriteString("\n")
	writeErrorChain(&b, err, 0)
	return b.String()
}

func writeErrorChain(b *strings.Builder, err error, depth int) {
	for err != nil {
		fmt.Fprintf(b, "%s%T: %s\n", strings.Repeat("  ", depth), err, err.Error())
		switch cause := err.(type) {
		case interface{ Unwrap() error }:
			err = cause.Unwrap()
			depth++
		case interface{ Unwrap() []error }:
			for _, branch := range cause.Unwrap() {
				writeErrorChain(b, branch, depth+1)
			}
			return
		default:
			return
		}
	}
}

func environmentReport(request diagnosticsRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "go-mutants %s\n", Version)
	fmt.Fprintf(&b, "run: %s\n", request.runID)
	fmt.Fprintf(&b, "go binary: %s\n", goBinaryLine(request.outcome.Toolchain))
	fmt.Fprintf(&b, "toolchain: %s\n", toolchainLine(request.outcome.Toolchain))
	fmt.Fprintf(&b, "runtime: %s\n", runtime.Version())
	fmt.Fprintf(&b, "platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "cwd: %s\n", workingDirectory())
	fmt.Fprintf(&b, "temp: %s\n", os.TempDir())
	fmt.Fprintf(&b, "report directory: %s\n",
		filepath.Join(request.workspace, filepath.FromSlash(request.reportDirectory)))
	b.WriteString(environmentHeading + "\n")
	for _, name := range environmentNames(request.environ) {
		b.WriteString("  " + name + "\n")
	}
	return b.String()
}

func goBinaryLine(toolchain gocmd.Toolchain) string {
	if toolchain.GoBin == "" {
		return "not located"
	}
	return toolchain.GoBin
}

func toolchainLine(toolchain gocmd.Toolchain) string {
	if toolchain.GoBin == "" {
		return "the run failed before a toolchain was located"
	}
	return toolchain.String()
}

func workingDirectory() string {
	dir, err := os.Getwd()
	if err != nil {
		return "unreadable: " + err.Error()
	}
	return dir
}

func environmentNames(environ []string) []string {
	names := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if name != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func eventStream(events []trace.Event) string {
	var b strings.Builder
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func preservedReport(preserved []engine.PreservedDir) string {
	if len(preserved) == 0 {
		return nothingKept + "\n"
	}
	var b strings.Builder
	for _, directory := range preserved {
		b.WriteString(directory.Kind + "\t" + directory.Path + "\n")
	}
	return b.String()
}
