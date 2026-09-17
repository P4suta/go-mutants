// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const summaryEnv = "GITHUB_STEP_SUMMARY"

const maxScanLine = 10 << 20

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, environment()))
}

func environment() map[string]string {
	env := make(map[string]string, len(os.Environ()))
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok {
			env[name] = value
		}
	}
	return env
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, env map[string]string) int {
	flagArgs, command := splitCommand(args)

	flags := flag.NewFlagSet("testcost", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { usage(stderr) }
	noSkips := flags.Bool("no-skips", false, "exit non-zero if any test skipped")
	verbose := flags.Bool("verbose", false, "replay every test's output, not only a failure's")
	if err := flags.Parse(flagArgs); err != nil {
		return exitUsage
	}
	if rest := flags.Args(); len(rest) != 0 {
		printf(stderr, "testcost: unexpected argument %q; give a command after `--`, "+
			"or pipe a `go test -json` stream in on standard input\n", rest[0])
		usage(stderr)
		return exitUsage
	}
	if command != nil && len(command) == 0 {
		printf(stderr, "testcost: `--` was given with no command after it\n")
		usage(stderr)
		return exitUsage
	}

	source := stdin
	wait := func() int { return 0 }
	if command != nil {
		child, reap, err := start(command, stderr)
		if err != nil {
			printf(stderr, "testcost: starting %s: %v\n", strings.Join(command, " "), err)
			return exitFailure
		}
		source, wait = child, reap
	}

	measured := read(source, *verbose)
	childStatus := wait()

	report, closeReport, err := destination(stdout, env)
	if err != nil {
		printf(stderr, "testcost: opening %s: %v\n", summaryEnv, err)
		return exitFailure
	}
	render(report, measured)
	if err := closeReport(); err != nil {
		printf(stderr, "testcost: writing %s: %v\n", summaryEnv, err)
		return exitFailure
	}

	for _, said := range measured.verbose {
		replay(stderr, "=== "+said.name, said)
	}
	for _, failed := range measured.failures {
		replay(stderr, "--- FAIL: "+failed.name, failed)
	}
	if measured.malformedCount != 0 {
		printf(stderr, "testcost: %d unreadable line(s) in the test stream:\n\t%s\n",
			measured.malformedCount, strings.Join(measured.malformed, "\n\t"))
	}
	if len(measured.failures) != 0 {
		printf(stderr, "testcost: %d failure(s):\n\t%s\n",
			len(measured.failures), strings.Join(failureNames(measured.failures), "\n\t"))
	}
	if *noSkips && len(measured.skips) != 0 {
		printf(stderr, "testcost: %d test(s) skipped and --no-skips was given:\n\t%s\n",
			len(measured.skips), strings.Join(measured.skips, "\n\t"))
	}
	if childStatus != 0 {
		printf(stderr, "testcost: the command exited with status %d\n", childStatus)
	}
	return verdict(measured, childStatus, *noSkips)
}

func fileOutput(name string, held *buffer) failure {
	return failure{name: name, output: held.lines, elided: held.elided}
}

func replay(w io.Writer, header string, filed failure) {
	printf(w, "%s\n", header)
	for _, line := range filed.output {
		printf(w, "%s", line)
	}
	if filed.elided != 0 {
		printf(w, "\t... %d more line(s) elided; run this package on its own to see them\n", filed.elided)
	}
}

func failureNames(failures []failure) []string {
	names := make([]string, len(failures))
	for index, failed := range failures {
		names[index] = failed.name
	}
	return names
}

func verdict(measured results, childStatus int, noSkips bool) int {
	switch {
	case measured.malformedCount != 0,
		len(measured.failures) != 0,
		childStatus != 0,
		noSkips && len(measured.skips) != 0:
		return exitFailure
	default:
		return exitOK
	}
}

func splitCommand(args []string) (flagArgs []string, command []string) {
	for index, arg := range args {
		if arg == "--" {
			return args[:index], args[index+1:]
		}
	}
	return args, nil
}

func start(command []string, stderr io.Writer) (io.Reader, func() int, error) {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return out, func() int {
		err := cmd.Wait()
		if err == nil {
			return 0
		}
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			return exited.ExitCode()
		}
		printf(stderr, "testcost: waiting for the command: %v\n", err)
		return exitFailure
	}, nil
}

func usage(w io.Writer) {
	printf(w, "%s", `testcost turns a `+"`go test -json`"+` stream into a per-package cost table.

usage:
  testcost [--no-skips] -- <command...>   run it, read its stdout, report
  <command...> | testcost [--no-skips]    read a stream somebody already has

  --no-skips   exit non-zero, naming them, if any test skipped
  --verbose    replay every test's output, not only a failure's

Running the command is the form the tasks use, because a pipeline reports the
last command's status: piped, a `+"`go`"+` that failed before it started testing would
be a green report of a run that never happened. With `+"`--`"+`, the command's status
and the stream's own verdict are both honoured.

The table is appended to $`+summaryEnv+` when that names a file, and
written to standard output otherwise. A failing test, a failing package or a
package that did not build always exits non-zero, so a task ending in this tool
gates exactly as the bare `+"`go test`"+` it replaces.
`)
}

func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func destination(stdout io.Writer, env map[string]string) (io.Writer, func() error, error) {
	path := env[summaryEnv]
	if path == "" {
		return stdout, func() error { return nil }, nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	printf(file, "\n")
	return file, file.Close, nil
}

type event struct {
	Action     string  `json:"Action"`
	Package    string  `json:"Package"`
	ImportPath string  `json:"ImportPath"`
	Test       string  `json:"Test"`
	Elapsed    float64 `json:"Elapsed"`
	Output     string  `json:"Output"`
}

type failure struct {
	name   string
	output []string
	elided int
}

type buffer struct {
	lines  []string
	bytes  int
	elided int
}

func (b *buffer) add(line string) {
	if b.bytes+len(line) > maxRetainedBytes {
		b.elided++
		return
	}
	b.bytes += len(line)
	b.lines = append(b.lines, line)
}

const maxRetainedBytes = 256 << 10

type packageCost struct {
	name    string
	tests   int
	skipped int
	elapsed time.Duration
}

type results struct {
	packages       []packageCost
	skips          []string
	failures       []failure
	malformed      []string
	malformedCount int
	verbose        []failure
	tests          int
	elapsed        time.Duration
}

const maxMalformedReported = 5

func read(r io.Reader, verbose bool) results {
	var run results
	buffered := make(map[string]*buffer)
	retain := func(key, line string) {
		held, ok := buffered[key]
		if !ok {
			held = &buffer{}
			buffered[key] = held
		}
		held.add(line)
	}
	take := func(key string) *buffer {
		held := buffered[key]
		delete(buffered, key)
		if held == nil {
			return &buffer{}
		}
		return held
	}
	byPackage := make(map[string]*packageCost)
	order := make([]string, 0, 32)
	costOf := func(name string) *packageCost {
		cost, ok := byPackage[name]
		if !ok {
			cost = &packageCost{name: name}
			byPackage[name] = cost
			order = append(order, name)
		}
		return cost
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			run.malformedCount++
			if len(run.malformed) < maxMalformedReported {
				run.malformed = append(run.malformed,
					fmt.Sprintf("%q: %v", truncate(line), err))
			}
			continue
		}
		switch e.Action {
		case "output":
			owner := cmp.Or(e.Package, e.ImportPath)
			retain(owner+"\x00"+e.Test, e.Output)
			continue
		case "build-output":
			retain(e.ImportPath+"\x00", e.Output)
			continue
		case "build-fail":
			run.failures = append(run.failures, fileOutput(e.ImportPath+" [build failed]", take(e.ImportPath+"\x00")))
			continue
		case "pass", "fail", "skip":
		default:
			continue
		}
		if e.Package == "" {
			continue
		}
		if e.Action == "skip" && e.Test == "" {
			take(e.Package + "\x00")
			continue
		}
		said := take(e.Package + "\x00" + e.Test)
		file := func(name string) {
			switch {
			case e.Action == "fail":
				run.failures = append(run.failures, fileOutput(name, said))
			case verbose && len(said.lines) != 0:
				run.verbose = append(run.verbose, fileOutput(name, said))
			}
		}
		cost := costOf(e.Package)
		switch {
		case e.Test == "":
			if e.Action == "fail" {
				file(e.Package + " [package failed]")
			} else {
				file(e.Package)
			}
		case e.Action == "skip":
			file(e.Package + "." + e.Test)
			cost.skipped++
			run.skips = append(run.skips, e.Package+"."+e.Test)
			if !strings.Contains(e.Test, "/") {
				cost.tests++
			}
		default:
			file(e.Package + "." + e.Test)
			if strings.Contains(e.Test, "/") {
				break
			}
			cost.tests++
			cost.elapsed += seconds(e.Elapsed)
		}
	}
	if err := scanner.Err(); err != nil {
		run.malformedCount++
		run.malformed = append(run.malformed, "reading the stream: "+err.Error())
	}

	run.packages = make([]packageCost, 0, len(order))
	for _, name := range order {
		cost := byPackage[name]
		run.packages = append(run.packages, *cost)
		run.tests += cost.tests
		run.elapsed += cost.elapsed
	}
	slices.SortFunc(run.packages, func(a, b packageCost) int {
		if c := cmp.Compare(b.elapsed, a.elapsed); c != 0 {
			return c
		}
		return cmp.Compare(a.name, b.name)
	})
	return run
}

func seconds(elapsed float64) time.Duration {
	if elapsed <= 0 {
		return 0
	}
	return time.Duration(elapsed * float64(time.Second))
}

func truncate(line string) string {
	const limit = 200
	if len(line) <= limit {
		return line
	}
	return line[:limit] + "…"
}

func render(w io.Writer, run results) {
	printf(w, "### Test cost\n\n")
	if len(run.packages) == 0 {
		printf(w, "No packages were tested.\n")
		return
	}
	printf(w, "| package | tests | skipped | elapsed |\n")
	printf(w, "| --- | ---: | ---: | ---: |\n")
	skipped := 0
	for _, cost := range run.packages {
		skipped += cost.skipped
		printf(w, "| `%s` | %d | %d | %s |\n", cost.name, cost.tests, cost.skipped, duration(cost.elapsed))
	}
	printf(w, "| **%d packages** | **%d** | **%d** | **%s** |\n",
		len(run.packages), run.tests, skipped, duration(run.elapsed))
	if len(run.skips) != 0 {
		printf(w, "\n<details><summary>%d skipped</summary>\n\n", len(run.skips))
		for _, name := range run.skips {
			printf(w, "- `%s`\n", name)
		}
		printf(w, "\n</details>\n")
	}
	if len(run.failures) != 0 {
		printf(w, "\n**%d failure(s):**\n\n", len(run.failures))
		for _, failed := range run.failures {
			printf(w, "- `%s`\n", failed.name)
		}
	}
	if run.malformedCount != 0 {
		printf(w, "\n**%d unreadable line(s) in the test stream, so this table is incomplete.**\n",
			run.malformedCount)
	}
}

func duration(d time.Duration) string {
	return fmt.Sprintf("%.2fs", d.Seconds())
}
