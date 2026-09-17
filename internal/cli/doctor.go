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
	"github.com/P4suta/go-mutants/internal/discover"
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

const (
	checkToolchain     = "go toolchain"
	checkModule        = "module"
	checkGit           = "git"
	checkCacheDir      = "cache directory"
	checkPlatform      = "platform"
	checkMemory        = "memory limit"
	checkConfiguration = "configuration"
)

const gitProbeTimeout = 10 * time.Second

const probePattern = "go-mutants-doctor-*.probe"

type checkStatus string

const (
	statusOK   checkStatus = "ok"
	statusWarn checkStatus = "warn"
	statusFail checkStatus = "fail"
)

type check struct {
	Name   string      `json:"name"`
	Status checkStatus `json:"status"`
	Detail string      `json:"detail"`
}

type doctorDocument struct {
	DocumentType  string  `json:"document_type"`
	SchemaVersion int     `json:"schema_version"`
	ToolVersion   string  `json:"tool_version"`
	Checks        []check `json:"checks"`
}

type doctorOptions struct {
	json bool
}

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
	if err := schemas.Validate(schemas.DoctorV1, buf.Bytes()); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func diagnose(ctx context.Context, dir string) []check {
	toolchain, toolchainErr := gocmd.LocateContext(ctx, gocmd.Options{})
	return []check{
		toolchainCheck(toolchain, toolchainErr),
		moduleCheck(dir),
		gitCheck(ctx),
		cacheCheck(),
		platformCheck(toolchain, toolchainErr),
		memoryCheck(),
		configurationCheck(dir),
	}
}

func withoutCode(line string) string {
	if _, rest, coded := splitCode(line); coded {
		return rest
	}
	return line
}

func detailOf(err error) string { return withoutCode(firstLine(err.Error())) }

func toolchainCheck(toolchain gocmd.Toolchain, err error) check {
	if err != nil {
		return check{checkToolchain, statusFail, detailOf(err)}
	}
	detail := toolchain.Version.Release + " at " + toolchain.GoBin
	if toolchain.Version.IsDevel() {
		detail += " (an unreleased build)"
	}
	return check{checkToolchain, statusOK, detail}
}

func moduleCheck(dir string) check {
	workspace, err := discover.DetectWorkspace(dir)
	if err != nil {
		return check{checkModule, statusFail, detailOf(err)}
	}
	if workspace != nil {
		modules := make([]string, 0, len(workspace.Modules))
		for _, module := range workspace.Modules {
			modules = append(modules, module.Path)
		}
		return check{checkModule, statusOK, countNoun(len(modules), "module") + " (" +
			filepath.Join(dir, discover.WorkspaceFile) + "): " + strings.Join(modules, ", ")}
	}
	module, err := moduleAt(dir)
	if err != nil {
		return check{checkModule, statusFail, detailOf(err)}
	}
	return check{checkModule, statusOK, module + " (" + filepath.Join(dir, moduleFileName) + ")"}
}

const moduleFileName = "go.mod"

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

func memoryCheck() check {
	switch bound := runner.MemoryBound(); bound {
	case runner.MemoryEnforcedByKernel:
		return check{checkMemory, statusOK,
			"enforced by the kernel's job object, with the sampler under it"}
	case runner.MemoryEnforcedBySampler:
		return check{checkMemory, statusOK,
			"enforced by sampling the process tree every " + runner.MemorySampleInterval.String()}
	case runner.MemoryUnenforced:
	}
	return check{checkMemory, statusWarn,
		"not enforced on " + runtime.GOOS + ": a bound is accepted and nothing acts on it, " +
			"so a mutant that runs away is stopped by its timeout rather than by its memory"}
}

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
	resolved := config.Merge(config.Defaults(), file, config.Overlay{})
	if err = resolved.Validate(); err != nil {
		return check{checkConfiguration, statusFail, problemLine(err)}
	}
	return check{checkConfiguration, statusOK,
		path + ": valid, " + countNoun(len(file.Keys()), "key") + " set"}
}

func problemLine(err error) string {
	lines := strings.Split(strings.TrimRight(err.Error(), "\n"), "\n")
	first := withoutCode(strings.TrimSpace(lines[0]))
	if len(lines) > 1 {
		return first + " (and " + countNoun(len(lines)-1, "more problem") + ")"
	}
	return first
}

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

func label(status checkStatus) string {
	if status == statusFail {
		return "FAIL"
	}
	return string(status)
}

func countStatus(checks []check, status checkStatus) int {
	n := 0
	for _, c := range checks {
		if c.Status == status {
			n++
		}
	}
	return n
}
