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
	// diagnosticsDirectoryName is the directory under `report.directory` that
	// holds bundles, one per failed run. See [ownedRoot] for why it lives there.
	diagnosticsDirectoryName = "diagnostics"

	// The files a bundle holds. Each answers one question a person asks about a
	// failure they cannot reproduce, and each is written only when there is
	// something in it: an empty file is a question a reader has to answer by
	// looking somewhere else.
	//
	// errorFileName is written first and preservedPathsFileName last, which is
	// what makes the pair a marker and a finished-flag for the collector; see
	// [diagnosticsRootAt].
	errorFileName          = "error.txt"
	environmentFileName    = "environment.txt"
	doctorFileName         = "doctor.txt"
	reportFileName         = "report.json"
	manifestFileName       = "manifest.json"
	preservedPathsFileName = "preserved-paths.txt"

	// environmentHeading opens the list of variable names in
	// [environmentFileName]. It is a constant because the file is meant to be
	// read by a person and cut up by a script, and because the tests assert that
	// nothing below it carries a value.
	environmentHeading = "environment:"

	// nothingKept is what [preservedPathsFileName] says for a run that left no
	// temporary directory behind, which is every run that did not ask to.
	nothingKept = "# this run left nothing behind"

	// bundleDirPerm and bundleFilePerm are what a bundle is written with. Like a
	// recording it is meant to be read — attached to a bug report, uploaded by
	// CI — so it is world-readable and never world-writable.
	bundleDirPerm  fs.FileMode = 0o755
	bundleFilePerm fs.FileMode = 0o644
)

// diagnosticsBudget bounds the whole bundle, and exists because the bundle does
// not inherit the run's deadline.
//
// It cannot. A run whose context expired is one of the failures a bundle is most
// worth having, and probing `doctor` through that dead context would record six
// rows of "context deadline exceeded" — a diagnosis of the bundle rather than of
// the machine, produced exactly when the machine is the question. So
// [runOptions.withDiagnostics] detaches the cancellation and puts this bound
// back on, because a diagnostic with no deadline at all can hang a process after
// the run it explains has already finished.
//
// Thirty seconds is generous for what is inside it: two version probes and a
// handful of writes, none of which touches the network. Anything approaching it
// means the machine is not answering rather than that the bundle is large.
const diagnosticsBudget = 30 * time.Second

// A diagnosticsRequest is everything the bundle of one failed run is written
// from.
//
// Every field is a fact the command already had in hand when the run returned.
// Nothing here re-derives anything: a bundle that computed its own answer to a
// question the run had already answered would be a second account of the run,
// and the whole point of the bundle is that there is one.
type diagnosticsRequest struct {
	// workspace is the run's root, which the bundle's own directory is placed
	// against and which `doctor` is run over.
	workspace string
	// reportDirectory is `report.directory`, relative to the workspace.
	reportDirectory string
	// runID names the bundle's own directory, so that a bundle, a recording and
	// a report can be paired afterwards.
	runID string
	// traceDirectory is where this run recorded, and empty for a run that kept
	// its account in memory. A run that has one gets its bundle there, beside
	// the stream it explains.
	traceDirectory string
	// events are the recording an untraced run kept in memory, and nil for one
	// that wrote a file — which already has its stream where the bundle goes.
	events []trace.Event
	// err is the failure the bundle exists to explain.
	err error
	// outcome is what the run established before it stopped.
	outcome engine.RunOutcome
	// environ is the process environment, whose *names* the bundle records.
	environ []string
	// hooks are the filesystem operations the bundle is written through. The
	// zero value is the real filesystem; see [traceFilesystem].
	hooks trace.Filesystem
}

// bundleContents says what each file of a bundle is for, in one line, for the
// manifest to carry.
//
// It is a table rather than a sentence built at the call site, because the
// manifest is read by a program that will show the line to a person: a bundle
// attached to a bug report should need no covering note, and the note is the
// same every time.
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

// A diagnosticsManifest is the one file of a bundle a program reads.
//
// It is an index of the directory rather than an account of the run. The
// account is the recording beside it and the claim is the run report, and
// restating either here would be the same run told twice -- which is the
// mistake [writeDiagnostics] already refuses for the stream.
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

// manifestPlatform is the machine the run was measured on. Build constraints
// decide which files a package even has, so a bundle is a statement about one.
type manifestPlatform struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

// manifestFailure is what stopped the run, as the console said it.
type manifestFailure struct {
	// Code is omitted rather than blank for an error that carries none: cobra
	// and pflag produce plain errors, and inventing a code for one would be a
	// second identifier for a condition that has no first.
	Code    string `json:"code,omitempty"`
	Summary string `json:"summary"`
}

// manifestFile is one file of the bundle and what a reader will find in it.
type manifestFile struct {
	Name  string `json:"name"`
	Holds string `json:"holds"`
}

// manifestFor builds the index of a bundle that holds exactly the named files.
//
// The names come from what the writer actually wrote rather than from a list of
// what a bundle can hold, because those differ every time: a run that published
// no report has no report.json, and a traced run's stream is already in this
// directory rather than written again.
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
		// A silent error renders nothing, and a manifest with no summary is a
		// document the schema refuses. Saying that the bundle exists is more
		// honest than leaving a reader with an empty field.
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

// bundleEntries is what the manifest indexes: everything already in the bundle
// directory, plus the two files still to be written.
//
// The directory is listed rather than trusted to `written`, because a traced
// run's bundle joins its recording and that directory holds things this writer
// did not put there: the stream itself, and the `output/` directory beside it
// holding the captured output of the commands the stream digested. Both are
// what a reader will find, so both are what the index says.
//
// A listing that fails falls back to what was written. The manifest is a
// convenience, and a bundle that lost its index because the directory could not
// be read a moment after being written to would be a failure invented here.
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

// writeDiagnostics writes one failed run's bundle and returns the directory it
// went into.
//
// The order is the whole of the collector's contract: [errorFileName] first,
// because its presence is what makes the directory one of ours, and
// [preservedPathsFileName] last, because its presence is what says the bundle is
// complete. Everything in between may be absent — a run that published no
// report has no document to copy — and nothing in between is ever empty.
//
// A failure here is returned rather than reported, because the caller is the one
// that knows a bundle must never change what a run reports. See
// [runOptions.diagnose].
func writeDiagnostics(ctx context.Context, request diagnosticsRequest) (path string, err error) {
	mkdirAll, writeFile := bundleHooks(request.hooks)
	directory, ours, err := bundleDirectory(request, mkdirAll)
	if err != nil {
		return "", err
	}
	// A bundle whose first write fails leaves an empty directory, and an empty
	// one is nobody's: it has no error.txt, so the retention cannot see it and
	// `trace clean --all` cannot remove it, and while it is there the empty
	// diagnostics root cannot be removed either. Every byte a run writes has an
	// owner and a collector, so a directory that would have neither is taken
	// back here, at the one moment anything still knows it is ours.
	//
	// os.Remove rather than RemoveAll is the whole of the rule. It refuses a
	// directory with anything in it, so a bundle that got as far as its marker
	// survives — that one *is* collectable, as the account of a run that died
	// while writing it, exactly like a recording with no run-end. And only ever
	// a directory this call created: a traced run's bundle lands in the
	// recording's own directory, and removing that would take away the stream
	// the bundle exists to sit beside.
	defer func() {
		if err != nil && ours {
			_ = os.Remove(directory)
		}
	}()

	// written is what the manifest indexes. It is what the writer actually
	// wrote rather than what a bundle can hold, because those differ every
	// time: a run that published no report has no report.json, and a traced
	// run's stream is already in this directory rather than written again.
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
	// The rendered failure first: it is the marker the collector reads, and it
	// is what somebody opens the directory for.
	if err = write(errorFileName, errorReport(request.err)); err != nil {
		return "", err
	}
	if err = write(environmentFileName, environmentReport(request)); err != nil {
		return "", err
	}
	// The diagnosis of the machine as it is now, which is the closest a bundle
	// can get to the machine as it was during the run: `doctor` reads a
	// toolchain, a module and a configuration file, none of which the failure
	// moved.
	if err = write(doctorFileName, renderChecks(diagnose(ctx, request.workspace))); err != nil {
		return "", err
	}
	// The account of the run, but only when it is not already here. A traced run
	// has its stream in this very directory, and writing the ring out beside it
	// would be the same run told twice with one of the tellings truncated.
	if request.traceDirectory == "" {
		if err = write(trace.FileName, eventStream(request.events)); err != nil {
			return "", err
		}
	}
	// Whichever document the run published: a run over one module writes a run
	// report and a run over a `go.work` writes a workspace report, and the
	// bundle carries the one there is under the same name. What it is, is in
	// the document -- `document_type` is the discriminator every consumer of
	// these files checks before decoding.
	if published := publishedDocument(request.outcome); published != nil {
		document, marshalErr := published.Marshal()
		if marshalErr != nil {
			return "", marshalErr
		}
		if err = write(reportFileName, string(document)); err != nil {
			return "", err
		}
	}
	// Second to last, and it names itself and the file after it. The index has
	// to be written before the completion marker, because the marker is what
	// says the bundle is finished -- so a manifest after it would be a file a
	// collector could remove a bundle out from under. A bundle with no
	// preserved-paths.txt is one the writer did not finish, and its manifest is
	// a statement of what it was going to hold.
	manifest, marshalErr := json.MarshalIndent(
		manifestFor(request, bundleEntries(directory, written)), "", "  ")
	if marshalErr != nil {
		return "", marshalErr
	}
	if err = write(manifestFileName, string(manifest)+"\n"); err != nil {
		return "", err
	}
	// Last, always, and never empty: its existence is what tells a collector the
	// bundle is finished, and a zero-length file would leave a reader wondering
	// whether the run kept nothing or the writer stopped.
	if err = write(preservedPathsFileName, preservedReport(request.outcome.Preserved)); err != nil {
		return "", err
	}
	return directory, nil
}

// bundleDirectory decides where one bundle goes, makes sure it is there, and
// says whether this call is what put it there.
//
// A traced run's bundle joins its recording, which is already a directory named
// by this run's id and already collected by the trace root's own retention — so
// `ours` is false for it, and nothing here may take it away. An untraced run's
// goes under `report.directory/diagnostics/<run-id>/`, and the older bundles
// there are collected first — before this run's own directory exists, so that
// the rule needs no exception protecting the bundle being written. That is
// [openTrace]'s argument, applied to the other root.
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
	// Best effort and deliberately silent. The run has already failed, the user
	// is about to read why, and "I could not tidy up some old bundles" is not a
	// sentence that belongs between them and their diagnosis.
	_, _ = collect(diagnosticsRootAt(root), retention{keep: trace.RetainRuns})

	directory = filepath.Join(root, request.runID)
	if err = mkdirAll(directory, bundleDirPerm); err != nil {
		return "", false, err
	}
	return directory, true, nil
}

// bundleHooks resolves the two filesystem operations a bundle performs, each of
// which defaults to the os function of the same name.
//
// It is [trace.Filesystem]'s own arrangement and deliberately the same value: a
// disk that will not take a recording is the disk that will not take a bundle,
// so a test producing one produces both, and there is one seam rather than two
// that have to be pointed at the same failure.
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

// errorReport is the failure written three ways, because three different
// questions are asked of it.
//
// First exactly what the console printed, so that a bug report holding the
// bundle and a bug report holding a paste of the terminal are the same bug
// report. Then the error in full, which is where a formatter with more to say
// than [error.Error] says it. Then the typed chain, one line per error in the
// unwrap tree with the joins indented under it — which is the only rendering
// that answers "which package decided this, and what did it wrap", and the
// question every diagnosis of a wrapped failure starts with.
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

// writeErrorChain writes one line per error in err's tree, indented by depth.
//
// The branches matter as much as the depth. An error joined out of several —
// the engine joins a run's failure with whatever removing the scratch directory
// said — unwraps to a *slice*, and a walk that followed one cause at a time
// would stop at the join with the interesting half one branch away. It is the
// same traversal [walkCauses] performs for the same reason, written out here
// because this one has to render the shape rather than search it.
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

// environmentReport is the machine the run happened on, as far as it decides
// what a run does.
//
// The variables are listed by name and never by value, and that is the one rule
// this file has that is not about diagnosis. A bundle is attached to bug
// reports and uploaded by CI, and the values are where the tokens, the
// credentials and the internal hostnames are. Which variables were *set* is
// nearly always the fact that matters — a GOFLAGS nobody expected, a GOEXPERIMENT
// somebody exported months ago — and the user reading the list can look up the
// one that surprises them.
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

// goBinaryLine and toolchainLine are the two halves of "which go was this".
//
// The path is on its own line because it is what somebody runs by hand to
// reproduce the failure, and the version beside it is
// [gocmd.Toolchain.String]'s own rendering so that a bundle and a `doctor` row
// describe one toolchain in one form. A run that failed before it located one
// says so, which is a fact worth stating rather than an empty pair of brackets
// to puzzle over.
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

// workingDirectory is where the command was typed, or a note saying it could not
// be read — which is itself a diagnosis, and never a reason to lose the rest of
// the file.
func workingDirectory() string {
	dir, err := os.Getwd()
	if err != nil {
		return "unreadable: " + err.Error()
	}
	return dir
}

// environmentNames is the names in an environment, sorted and deduplicated,
// with no value anywhere near them.
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

// eventStream is a recording written as the JSON Lines contract, which is what
// makes the ring an untraced run kept readable by `go-mutants trace` and by
// every consumer of the published schema.
func eventStream(events []trace.Event) string {
	var b strings.Builder
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			// One event that will not encode costs that event. A bundle is a
			// diagnostic, and losing the rest of a stream over one line would be
			// the diagnostic failing at the moment it is needed.
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// preservedReport names what the run left on disk, as tab-separated rows a
// script can cut and a person can read.
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
