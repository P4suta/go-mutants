// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/schemas"
)

const doctorLong = `Check that this machine can run go-mutants, and say what it found.

One line per check: the Go toolchain and its version, the module this directory
is the root of, git, whether the cache directory can be written, the platform,
and whether ` + "`.go-mutants.toml`" + ` parses. Nothing is measured and nothing is
executed beyond two version probes, so it is safe to run anywhere and costs a
second.

A check is ok, warn, or FAIL. A warn is something only an opt-in feature needs
— git is one, since ` + "`run --changed`" + ` is the only thing that asks for it — and it
never fails the command. Any FAIL exits 2, so ` + "`go-mutants doctor`" + ` is usable as
the first step of a CI job.

--json prints the same findings as a go-mutants/doctor v1 document, validated
against the schema this binary carries before it is printed.`

// The check names. They are constants because they are the interface: the table
// prints them, the JSON document carries them, and a consumer may branch on
// them, so renaming one is a change to what go-mutants publishes.
const (
	checkToolchain     = "go toolchain"
	checkModule        = "module"
	checkGit           = "git"
	checkCacheDir      = "cache directory"
	checkPlatform      = "platform"
	checkConfiguration = "configuration"
)

// gitProbeTimeout bounds `git --version`. Like [gocmd.DefaultProbeTimeout], it
// is generous for a command that prints a constant: anything approaching it
// means git is not answering rather than that it is busy.
const gitProbeTimeout = 10 * time.Second

// probePattern is the name the cache-directory check writes its probe file
// under. The prefix is deliberately recognisable: anything matching it is a
// leftover from an interrupted `doctor` and is safe to delete.
const probePattern = "go-mutants-doctor-*.probe"

// A checkStatus is one row's verdict.
type checkStatus string

// The verdicts. They are the schema's spellings, lowercase, because the
// document is the published artefact; the table renders "fail" as FAIL and
// nothing else differs.
const (
	// statusOK is a check that passed.
	statusOK checkStatus = "ok"
	// statusWarn is a check that failed on something only an opt-in feature
	// needs. It is reported and never fatal: a machine with no git can still
	// mutation-test everything except `run --changed`.
	statusWarn checkStatus = "warn"
	// statusFail is a check go-mutants cannot work without.
	statusFail checkStatus = "fail"
)

// A check is one row of the diagnosis.
type check struct {
	Name   string      `json:"name"`
	Status checkStatus `json:"status"`
	Detail string      `json:"detail"`
}

// A doctorDocument is `doctor --json`: the go-mutants/doctor v1 document, in
// the field order the schema documents.
type doctorDocument struct {
	DocumentType  string  `json:"document_type"`
	SchemaVersion int     `json:"schema_version"`
	ToolVersion   string  `json:"tool_version"`
	Checks        []check `json:"checks"`
}

// doctorOptions holds the flag destinations for one `doctor`.
type doctorOptions struct {
	json bool
}

// newDoctorCommand builds `doctor`.
func newDoctorCommand() *cobra.Command {
	o := &doctorOptions{}
	cmd := &cobra.Command{
		Use:   "doctor [flags]",
		Short: "Check that this machine can run go-mutants",
		Long:  doctorLong,
		Args:  cobra.NoArgs,
		RunE:  o.execute,
	}
	cmd.Flags().BoolVar(&o.json, "json", false, "print a go-mutants/doctor v1 document instead of the table")
	return cmd
}

// execute is `doctor`'s body.
//
// The findings are printed and then the failure is returned, which is the shape
// `cache gc` uses for the same reason: the whole point of the command is the
// table, and a machine with two problems should be told about both rather than
// about the fact that it has some. The returned error carries no detail of its
// own — every row has already said its piece — and exists to make the exit
// status 2.
func (o *doctorOptions) execute(cmd *cobra.Command, _ []string) error {
	dir, err := os.Getwd()
	if err != nil {
		return &Error{
			Code:    CodeWorkingDirectory,
			Message: "the current working directory cannot be read, so there is nothing to diagnose",
			Err:     err,
		}
	}
	checks := diagnose(cmd.Context(), dir)

	text, err := o.render(checks)
	if err != nil {
		return err
	}
	if err = emit(cmd.OutOrStdout(), text); err != nil {
		return err
	}
	if failed := countStatus(checks, statusFail); failed > 0 {
		return &Error{
			Code:    CodeEnvironmentUnusable,
			Message: countNoun(failed, "check") + " failed, so go-mutants cannot run here",
			Hint:    "fix the FAIL rows above and run `go-mutants doctor` again",
		}
	}
	return nil
}

// render turns the findings into what this invocation prints.
func (o *doctorOptions) render(checks []check) (string, error) {
	if !o.json {
		return renderChecks(checks), nil
	}
	document := doctorDocument{
		DocumentType:  schemas.DoctorV1,
		SchemaVersion: 1,
		ToolVersion:   Version,
		Checks:        checks,
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return "", err
	}
	// Checked before it is printed, exactly as `report merge` checks the
	// document it is about to publish. A diagnosis that does not satisfy the
	// schema go-mutants itself defines would be a poor thing to hand a script
	// that is deciding whether to trust this machine.
	if err := schemas.Validate(schemas.DoctorV1, buf.Bytes()); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// diagnose runs every check, in the order the table prints them.
//
// Every check runs, whatever the ones before it found: a machine with no Go
// toolchain and an unparsable configuration file has two problems, and being
// told about one of them at a time is how a five-minute fix becomes three CI
// rounds. The toolchain is located once and its answer is shared with the
// platform row, because probing `go version` twice would be the only work here
// worth avoiding.
func diagnose(ctx context.Context, dir string) []check {
	toolchain, toolchainErr := gocmd.LocateContext(ctx, gocmd.Options{})
	return []check{
		toolchainCheck(toolchain, toolchainErr),
		moduleCheck(dir),
		gitCheck(ctx),
		cacheCheck(),
		platformCheck(toolchain, toolchainErr),
		configurationCheck(dir),
	}
}

// withoutCode drops a leading "GOMxxxx: " from one line.
//
// The code belongs on the error path, where [RenderError] puts one in front of
// every line it writes to standard error so that all of them stay greppable. It
// does not belong in a cell of a table whose first column is already the
// verdict, and it does not belong in the `detail` of a published document: a
// consumer of `doctor --json` branches on `name` and `status`, which this
// package promises are stable, and reads `detail` as the sentence it claims to
// be. internal/report's reasonOf drops the code from a listing's rows for the
// same reason.
func withoutCode(line string) string {
	if _, rest, coded := splitCode(line); coded {
		return rest
	}
	return line
}

// detailOf renders a failure for a table cell: its first line, without the code
// in front of it. See [withoutCode].
func detailOf(err error) string { return withoutCode(firstLine(err.Error())) }

// toolchainCheck reports the Go toolchain go-mutants would build and test with.
func toolchainCheck(toolchain gocmd.Toolchain, err error) check {
	if err != nil {
		return check{checkToolchain, statusFail, detailOf(err)}
	}
	detail := toolchain.Version.Release + " at " + toolchain.GoBin
	if toolchain.Version.IsDevel() {
		// Not a warning: an unreleased toolchain is a perfectly good one to
		// mutation-test with. It is said out loud because a surprising result
		// six months from now is easier to attribute when the report says which
		// build produced it.
		detail += " (an unreleased build)"
	}
	return check{checkToolchain, statusOK, detail}
}

// moduleCheck reports the module this directory is the root of.
func moduleCheck(dir string) check {
	module, err := moduleAt(dir)
	if err != nil {
		return check{checkModule, statusFail, detailOf(err)}
	}
	return check{checkModule, statusOK, module + " (" + filepath.Join(dir, moduleFileName) + ")"}
}

// moduleFileName is the file that makes a directory a module root.
const moduleFileName = "go.mod"

// moduleAt returns the module path declared by the go.mod in dir.
//
// There is no search up the directory tree, for the same reason the
// configuration file is not looked for up one: go-mutants measures the module
// it was invoked in, and a tool that quietly walked upwards would snapshot,
// build, and score a tree the user was not standing in — or, for the history
// commands, delete the records of one.
//
// Only the `module` line is read, by golang.org/x/mod, which is the same parser
// the Go tool uses. Everything else in the file — requirements, replacements,
// the toolchain line — is the compiler's business and never this one's.
func moduleAt(dir string) (string, error) {
	path := filepath.Join(dir, moduleFileName)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", &Error{
			Code:    CodeNotAModuleRoot,
			Message: "there is no " + moduleFileName + " in " + dir,
			Hint:    "go-mutants measures one module and is run from its root; change directory to it",
		}
	case err != nil:
		return "", &Error{
			Code:    CodeNotAModuleRoot,
			Message: path + " could not be read",
			Err:     err,
		}
	}
	module := modfile.ModulePath(data)
	if module == "" {
		return "", &Error{
			Code:    CodeNotAModuleRoot,
			Message: path + " declares no module path",
		}
	}
	return module, nil
}

// gitCheck reports whether git is there, and never fails the command.
//
// `run --changed` is the only feature that needs git, so a machine without it
// can still do everything else go-mutants does. Saying that in the row is the
// point: "warn" with no explanation would send somebody installing git to fix a
// run that was never going to ask for it.
func gitCheck(ctx context.Context) check {
	const only = "; only `run --changed` needs it"
	program, err := exec.LookPath("git")
	if err != nil {
		return check{checkGit, statusWarn, "git is not on PATH" + only}
	}
	result := runner.Run(ctx, runner.Spec{
		Argv:    []string{program, "--version"},
		Timeout: gitProbeTimeout,
	})
	switch {
	case result.Err != nil:
		return check{checkGit, statusWarn, program + " could not be run: " + detailOf(result.Err) + only}
	case result.TimedOut:
		return check{checkGit, statusWarn,
			program + " did not answer `git --version` within " + gitProbeTimeout.String() + only}
	case result.ExitCode != 0:
		return check{checkGit, statusWarn,
			program + " exited with status " + strconv.Itoa(result.ExitCode) + only}
	}
	return check{checkGit, statusOK, firstLine(strings.TrimSpace(string(result.Output))) + " at " + program}
}

// cacheCheck proves go-mutants can write where it keeps run history and stored
// outcomes.
//
// The probe is a temporary file created and removed inside go-mutants' own
// directory under the operating system's cache root, and nowhere else. That
// directory is created if it is not there, which is what a first run would do
// anyway; nothing outside it is touched, because the cache root is shared with
// every other tool on the machine and a diagnostic has no business writing into
// somebody else's directory to find out whether it could.
func cacheCheck() check {
	base, err := os.UserCacheDir()
	if err != nil {
		return check{checkCacheDir, statusFail,
			"the operating system will not say where its cache directory is: " + err.Error()}
	}
	dir := filepath.Join(base, report.DirName)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return check{checkCacheDir, statusFail, dir + " could not be created: " + err.Error()}
	}
	probe, err := os.CreateTemp(dir, probePattern)
	if err != nil {
		return check{checkCacheDir, statusFail, dir + " is not writable: " + err.Error()}
	}
	name := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(name)
	switch {
	case closeErr != nil:
		return check{checkCacheDir, statusFail, name + " could not be closed: " + closeErr.Error()}
	case removeErr != nil:
		return check{checkCacheDir, statusFail, name + " could not be removed again: " + removeErr.Error()}
	}
	return check{checkCacheDir, statusOK, "writable: " + dir}
}

// platformCheck reports the host, and notices a toolchain built for another
// one.
//
// Build constraints decide which files a package even has, so a report is a
// statement about one platform — and a `go` on PATH that targets a different
// one would make every such statement about a tree this machine cannot build.
// It is a warning rather than a failure because the toolchain is the authority
// on what it can produce, and a cross-compiling setup that works is not
// go-mutants' business to refuse.
func platformCheck(toolchain gocmd.Toolchain, err error) check {
	host := runtime.GOOS + "/" + runtime.GOARCH
	if err != nil {
		return check{checkPlatform, statusOK, host}
	}
	target := toolchain.Version.GOOS + "/" + toolchain.Version.GOARCH
	if target != host {
		return check{checkPlatform, statusWarn,
			host + ", and the toolchain reports " + target + ": a run would measure the tree as that platform sees it"}
	}
	return check{checkPlatform, statusOK, host}
}

// configurationCheck reports what `.go-mutants.toml` in this directory says, if
// anything.
//
// An absent file is ok rather than a warning: go-mutants works out of the box
// and the defaults are a complete, valid configuration. A file that is there
// and cannot be understood is a failure, and the row carries the position the
// configuration layer worked out — the whole value of that layer is that it
// says which line, and a diagnosis that dropped it would send somebody reading
// the file by eye.
func configurationCheck(dir string) check {
	path := filepath.Join(dir, config.FileName)
	file, err := config.LoadFile(path)
	if err != nil {
		return check{checkConfiguration, statusFail, problemLine(err)}
	}
	if !file.Present {
		return check{checkConfiguration, statusOK,
			"no " + config.FileName + " here, so the built-in defaults apply"}
	}
	// Validated as a resolved configuration and not only as a file, because the
	// two catch different things: `report.low` above `report.high` is a pair of
	// perfectly good values that cannot both be right, and a run started here
	// would refuse it.
	resolved := config.Merge(config.Defaults(), file, config.Overlay{})
	if err = resolved.Validate(); err != nil {
		return check{checkConfiguration, statusFail, problemLine(err)}
	}
	return check{checkConfiguration, statusOK,
		path + ": valid, " + countNoun(len(file.Keys()), "key") + " set"}
}

// problemLine reduces a configuration failure to one line for a table row.
//
// A file with three mistakes renders as three coded lines, and the row keeps
// the first and counts the rest: the table is a diagnosis of the machine, and
// the remedy for all three is the same — open the file at the position named
// and run `go-mutants doctor` again. The code goes and the position stays —
// see [withoutCode] — because the position is the whole value of what the
// configuration layer worked out, and the code is what stderr is for.
func problemLine(err error) string {
	lines := strings.Split(strings.TrimRight(err.Error(), "\n"), "\n")
	first := withoutCode(strings.TrimSpace(lines[0]))
	if len(lines) > 1 {
		return first + " (and " + countNoun(len(lines)-1, "more problem") + ")"
	}
	return first
}

// renderChecks lays the findings out as the aligned table.
//
// The name column is padded to the widest name, which is safe here and would
// not be in a listing: these names are a fixed, constant set, so the width is a
// property of this build rather than of what a particular machine happens to be
// called. A listing of user data pads nothing, for the reason internal/console
// gives.
func renderChecks(checks []check) string {
	width := 0
	for _, c := range checks {
		width = max(width, len(c.Name))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "go-mutants %s (doctor)\n", Version)
	for _, c := range checks {
		fmt.Fprintf(&b, "%-4s  %-*s  %s\n", label(c.Status), width, c.Name, c.Detail)
	}

	summary := countNoun(len(checks), "check") + ": " + strconv.Itoa(countStatus(checks, statusOK)) + " ok"
	if warned := countStatus(checks, statusWarn); warned > 0 {
		summary += ", " + strconv.Itoa(warned) + " warn"
	}
	if failed := countStatus(checks, statusFail); failed > 0 {
		summary += ", " + strconv.Itoa(failed) + " FAIL"
	}
	b.WriteString(summary + "\n")
	return b.String()
}

// label renders a status for the table. A failure is shouted and the other two
// are not: it is the one row a reader must not skim past, and it is the one the
// exit status is about.
func label(status checkStatus) string {
	if status == statusFail {
		return "FAIL"
	}
	return string(status)
}

// countStatus is how many checks reported one verdict.
func countStatus(checks []check, status checkStatus) int {
	n := 0
	for _, c := range checks {
		if c.Status == status {
			n++
		}
	}
	return n
}
