// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command testcost turns `go test -json` into a per-package cost table.
//
// Two facts about a test run are invisible in an ordinary `go test` log, and
// both of them are the ones that decide what the suite costs. The first is
// where the time went: `ok  <pkg>  12.345s` is printed once per package,
// interleaved with every other package's line, and nobody scrolls a
// forty-package log adding them up — so a package that quietly grew from two
// seconds to ninety is noticed when somebody complains about CI, months later.
// The second is what did not run at all. A skip prints nothing without `-v`,
// so a test that skipped itself because a tool was missing, a symlink could not
// be made or an environment variable was unset reads exactly like a test that
// passed.
//
// This reads the machine-readable form of the same run and prints one Markdown
// table, sorted by elapsed time, with the skipped tests named underneath it. In
// CI it appends to $GITHUB_STEP_SUMMARY, so the table is on the run's summary
// page rather than buried in a log nobody opens; locally it goes to stdout.
//
// What a failing run said goes to stderr, which is the third thing an ordinary
// log loses. cmd/go runs the binary with -test.v under -json, so every line a
// failing test wrote is in the stream and attributed to it; those lines are
// replayed verbatim under a `--- FAIL: <package>.<test>` header, and a passing
// test's are thrown away unless --verbose asks for them. A report that named a
// failing test and showed nothing it said would send its only reader back to a
// runner they cannot reach.
//
// It also carries the run's verdict, which is not a nicety, and it prefers to
// *run* the command rather than be piped its output:
//
//	testcost -- go test -json ./...
//
// A pipeline reports the last command's status in every shell there is, so a
// formatter on the end of one turns a failing suite green — and `set -o
// pipefail` is not a portable answer, since these tasks run under `sh` on two
// platforms and `cmd /c` on the third. Worse, the failures this tool can see are
// the ones cmd/go wrote into the stream: a `go` command that fell over before it
// started testing writes plain text on stderr and nothing on stdout, so the
// verdict would be "nothing failed" about a run that never happened. Starting
// the child means its status is read rather than discarded, and the two verdicts
// are combined: the report fails if the command failed *or* if the stream
// carried a failure.
//
// Reading standard input is still supported, for a stream somebody already has.
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

// The tool's own exit codes.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// summaryEnv is the file GitHub Actions renders on a job's summary page. A step
// appends Markdown to it; the variable is absent everywhere else, which is what
// makes "append when it is set, print otherwise" the whole of the routing rule.
const summaryEnv = "GITHUB_STEP_SUMMARY"

// maxScanLine is the longest line the reader accepts.
//
// bufio.Scanner refuses a line over its buffer and stops, and a `go test -json`
// stream carries the tested program's own output one line at a time: a single
// `output` event holding a megabyte of a failing test's diff is unusual but not
// wrong, and a cost table that silently stopped halfway through such a run
// would be worse than no table. Ten megabytes is far past anything a test
// writes on one line and far below anything that matters for memory.
const maxScanLine = 10 << 20

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, environment()))
}

// environment reads the process's environment as the map [run] resolves the
// summary file from.
func environment() map[string]string {
	env := make(map[string]string, len(os.Environ()))
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok {
			env[name] = value
		}
	}
	return env
}

// run is the whole program, over streams and an environment a test supplies.
//
// The stream is a reader, the two outputs are writers and the environment is a
// map, so every question this tool answers — what the table says, where it goes,
// what the exit code is — is a unit test rather than a shell script nobody runs.
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

	// The stream first, then the child: reading to EOF is what lets the command
	// finish, and a wait before that would deadlock the moment the child filled
	// the pipe.
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

	// Every finding is reported separately, because they are different findings
	// and a reader has to know which one they are looking at: a failure is the
	// suite saying no, a skip under --no-skips is the suite saying nothing at
	// all, and a non-zero child is the command saying something the stream did
	// not carry.
	// The output first and the roll-call last, so that the names are the final
	// thing in the log rather than the top of a screenful of stack traces.
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

// fileOutput turns a finished buffer into the record the report carries.
func fileOutput(name string, held *buffer) failure {
	return failure{name: name, output: held.lines, elided: held.elided}
}

// replay writes one buffered run of output under a header naming it.
//
// The lines go out verbatim, because `go test -v` has already formatted them
// and reformatting somebody else's stack trace loses the alignment that makes it
// readable. The header carries the package as well as the test, which go's own
// `--- FAIL:` line does not: a report covering forty packages has to say which
// one this was.
//
// What was dropped is stated rather than left out, because a reader who cannot
// tell a short failure from a truncated one will read the last line they were
// given as the last thing that happened.
func replay(w io.Writer, header string, filed failure) {
	printf(w, "%s\n", header)
	for _, line := range filed.output {
		printf(w, "%s", line)
	}
	if filed.elided != 0 {
		printf(w, "\t... %d more line(s) elided; run this package on its own to see them\n", filed.elided)
	}
}

// failureNames is the roll-call under the replayed output.
func failureNames(failures []failure) []string {
	names := make([]string, len(failures))
	for index, failed := range failures {
		names[index] = failed.name
	}
	return names
}

// verdict combines everything that can make a run bad into one exit code.
//
// Both directions matter and neither subsumes the other. A command that exited
// non-zero is a failure even when the stream mentions none, which is the whole
// reason this tool starts the command; and a stream carrying a failure is a
// failure even when the command exited zero, which happens whenever a status is
// swallowed on the way out of a suite.
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

// splitCommand separates this tool's own flags from the command to run.
//
// The `--` is the whole rule, and it is the reason `testcost ./...` is still a
// usage error rather than an attempt to execute `./...`: a tool that runs
// whatever it is handed is one typo away from running something nobody meant.
// A nil command means "read standard input"; an empty non-nil one means `--`
// was given with nothing after it, which is a mistake worth naming.
func splitCommand(args []string) (flagArgs []string, command []string) {
	for index, arg := range args {
		if arg == "--" {
			return args[:index], args[index+1:]
		}
	}
	return args, nil
}

// start runs the command and returns its standard output as a stream, together
// with the function that reaps it.
//
// The child's stderr and stdin are this process's own, which is what keeps a
// toolchain diagnostic — `go: cannot load module …` — in front of whoever ran
// the task, and keeps a suite that asks for input able to.
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
			// A child killed by a signal has no status of its own; the shells'
			// 128+N convention is what every reader expects instead, and
			// ExitCode() already reports -1 for one, so anything non-zero is
			// enough for the verdict. It is reported as it stands.
			return exited.ExitCode()
		}
		printf(stderr, "testcost: waiting for the command: %v\n", err)
		return exitFailure
	}, nil
}

// usage is the whole tool in one screen.
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

// printf writes one line of the tool's own output.
//
// The error is dropped for the same reason internal/devtools/testcache drops
// it: the writers are the process's own stdout and stderr, a write to either
// fails only when the caller has closed the stream or filled the disk, and
// there is nowhere left to report that to. The exit code carries the outcome.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// destination is where the table goes, and the function that finishes writing
// it.
//
// The summary file is opened for appending rather than truncated, because a job
// has many steps and every one of them may write to it — this step's table has
// to land under whatever the steps before it said, not on top of it.
func destination(stdout io.Writer, env map[string]string) (io.Writer, func() error, error) {
	path := env[summaryEnv]
	if path == "" {
		return stdout, func() error { return nil }, nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	// A blank line first, because what the previous step left may not have
	// ended in a newline — and a heading welded onto the end of somebody
	// else's paragraph is rendered as part of that paragraph.
	printf(file, "\n")
	return file, file.Close, nil
}

// event is the subset of a `go test -json` record this tool reads.
//
// ImportPath rather than Package is what a build failure carries: cmd/go
// reports one as `build-fail` against an import path, before any package result
// exists, and a table that only knew about `Package` would report a run that
// compiled nothing at all as an empty and entirely successful one.
type event struct {
	Action     string  `json:"Action"`
	Package    string  `json:"Package"`
	ImportPath string  `json:"ImportPath"`
	Test       string  `json:"Test"`
	Elapsed    float64 `json:"Elapsed"`
	Output     string  `json:"Output"`
}

// failure is one thing that went wrong, and everything that was said about it.
//
// The output is the half that matters and the half a count cannot replace. Under
// -json cmd/go runs the binary with -test.v, so the stream carries every line a
// failing test wrote — the assertion with its file and line, the panic, the
// goroutine dump — attributed to that test. A report that consumed those records
// and printed a name told a reader which test to go and run on a machine they do
// not have.
type failure struct {
	// name is `<package>.<test>` for a test, or the package with a bracketed
	// reason when there is no test to name.
	name string
	// output is the buffered lines, verbatim, in the order the binary wrote
	// them. Verbatim because `go test -v` has already formatted them, and
	// reformatting somebody else's diagnostic loses the alignment that makes a
	// stack readable.
	output []string
	// elided is how many lines were dropped by the cap before this was filed,
	// so a reader is told the evidence is incomplete rather than left to
	// assume it is all there.
	elided int
}

// buffer is one owner's retained output, bounded as it arrives.
//
// The bound has to be here rather than at replay, and that is the whole reason
// this is a type instead of a slice. Trimming what is printed bounds the log;
// every line is still held until the owner's result arrives, so a test that
// logs in a loop — which is how a test that is going wrong usually behaves —
// grows this process without limit and can kill the wrapper before it renders
// anything at all. The report exists for exactly that run.
//
// The head is what is kept: the first assertion to fail is almost always the one
// that explains the rest, and a tail would throw it away to keep the noise.
type buffer struct {
	lines  []string
	bytes  int
	elided int
}

// add retains one line, or counts it as elided.
func (b *buffer) add(line string) {
	if b.bytes+len(line) > maxRetainedBytes {
		b.elided++
		return
	}
	b.bytes += len(line)
	b.lines = append(b.lines, line)
}

// maxRetainedBytes bounds one owner's output, in memory and in the log.
//
// A quarter of a megabyte is far more than any assertion, panic or goroutine
// dump this repository produces, and far less than a test that failed inside a
// loop can write. It is per owner rather than in total, which is the bound that
// matters for a report: failures are few, and truncating the second one because
// the first was noisy would hide the very thing somebody is looking for.
const maxRetainedBytes = 256 << 10

// packageCost is one row of the table.
type packageCost struct {
	name    string
	tests   int
	skipped int
	elapsed time.Duration
}

// results is everything the table and the exit code are made of.
type results struct {
	packages []packageCost
	// skips are fully qualified `<package>.<test>` names, and failures the same
	// with the output that explains them, both in the order the run reported
	// them, so that a reader can find one in the log above without sorting
	// anything back.
	skips    []string
	failures []failure
	// malformed holds the first few lines that announced themselves as JSON and
	// were not, and malformedCount how many there were altogether.
	//
	// A few rather than all of them: a stream that is garbage from end to end
	// produces one line of noise per line of garbage, and a report nobody can
	// read is a report. The count is kept separately so the summary still says
	// how bad it was.
	malformed      []string
	malformedCount int
	// verbose holds every other test's output, and is filled only under
	// --verbose.
	verbose []failure
	tests   int
	elapsed time.Duration
}

// maxMalformedReported bounds the quoting above.
const maxMalformedReported = 5

// read consumes a `go test -json` stream.
//
// Two counting rules, and both are deliberate:
//
//	tests    Top-level test functions only. A subtest is part of its parent's
//	         time, so counting `TestX` and `TestX/case` as two tests would both
//	         double the count and double the seconds.
//	skipped  Every skip at any depth, which is why this column can exceed the
//	         one beside it. The reason for the whole column is a case that did
//	         not run, and `FuzzParse/seed#3` not running is exactly as
//	         interesting as `TestParse` not running.
//
// A package that holds no test files is left out altogether: cmd/go reports one
// as a package-level `skip`, and it is neither a row worth printing nor a test
// that stopped running.
//
// A package whose result came out of the build cache emits no test events at
// all — cmd/go replays `ok <pkg> (cached)` and nothing else — so it lands in the
// table as zero tests in zero seconds. That is the honest answer to what this
// run cost, and the reason the elapsed column is a sum of the tests rather than
// the package's own line, which would be zero for a cached package and a
// wall-clock number no individual test can be blamed for in a parallel one.
func read(r io.Reader, verbose bool) results {
	var run results
	// Output waiting to be either printed or thrown away, keyed by package and
	// test name. A test's buffer is dropped the moment it passes, so what is
	// held at any point is the output of the tests currently running rather
	// than of the whole suite; the empty test name is the package's own, which
	// is where a panic, a build diagnostic and a TestMain's writing end up.
	buffered := make(map[string]*buffer)
	// retain adds one line to an owner's bounded buffer.
	retain := func(key, line string) {
		held, ok := buffered[key]
		if !ok {
			held = &buffer{}
			buffered[key] = held
		}
		held.add(line)
	}
	// take removes an owner's buffer and returns it, because every terminal
	// event ends its buffer's life whatever it then does with the contents.
	// One place to drop it is what keeps a skipped test from leaving one behind
	// for the rest of the run.
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
		// cmd/go writes only JSON to stdout under -json, but a `go` command
		// that failed before it started testing (a toolchain download, a
		// missing module) writes plain text there. Skipping it keeps this tool
		// from turning a legible toolchain error into a parse error about it.
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// Reported, never fatal, and never the end of the scan. The reason
			// anybody is reading this output is usually that something already
			// went wrong, so answering "one line was malformed" instead of
			// showing the forty packages that parsed is the least useful
			// possible moment to start withholding results.
			run.malformedCount++
			if len(run.malformed) < maxMalformedReported {
				run.malformed = append(run.malformed,
					fmt.Sprintf("%q: %v", truncate(line), err))
			}
			continue
		}
		switch e.Action {
		case "output":
			// build-output carries no Package, only ImportPath; ordinary output
			// carries Package and, when the line belongs to one, Test.
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
		// A package-level skip is cmd/go saying the package holds no test
		// files. It is not a skipped test and it is not a row: a table whose
		// job is to say what to look at first had a third of its lines reading
		// `0 | 0 | 0.00s` for packages with nothing to run, and a skip list that
		// named them could not be told apart from one naming tests that stopped
		// running.
		if e.Action == "skip" && e.Test == "" {
			take(e.Package + "\x00")
			continue
		}
		// One routing decision per terminal event, and the buffer is dropped
		// either way. A failure's output is evidence and is filed with it;
		// everything else is only interesting under --verbose, where it is
		// filed once and never also as a failure. A green test's output is
		// otherwise thrown away as its result arrives rather than at the end,
		// which is what keeps this bounded: the alternative is holding a
		// `go test -v` transcript of the whole suite in memory to print none of
		// it.
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
			// The package's own verdict. Its elapsed time is deliberately not
			// added: it is the binary's wall clock, and the tests below it have
			// already accounted for the work.
			//
			// Its *output* is the only evidence for the failures no test owns:
			// a panic that took the binary down, a suite that ran out of time,
			// a TestMain that exited non-zero.
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
		// The same treatment as a malformed record, and for the same reason: a
		// stream that ended badly is worth failing over, and the packages that
		// arrived before it ended are worth printing.
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
	// Slowest first, because that is the only order anybody reads this table in;
	// the name breaks a tie so that two runs of the same suite print the same
	// table.
	slices.SortFunc(run.packages, func(a, b packageCost) int {
		if c := cmp.Compare(b.elapsed, a.elapsed); c != 0 {
			return c
		}
		return cmp.Compare(a.name, b.name)
	})
	return run
}

// seconds converts a `go test -json` elapsed field, which is a number of
// seconds with three decimals, into a duration.
func seconds(elapsed float64) time.Duration {
	if elapsed <= 0 {
		return 0
	}
	return time.Duration(elapsed * float64(time.Second))
}

// truncate bounds a line quoted back in an error.
func truncate(line string) string {
	const limit = 200
	if len(line) <= limit {
		return line
	}
	return line[:limit] + "…"
}

// render writes the table, and the skip list under it.
//
// Markdown with right-aligned numeric columns, because the one place this is
// read is a GitHub step summary, which renders it — and a plain-text table
// would be rendered as a paragraph with the columns run together.
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

// duration renders a duration the way `go test` does, so that a row can be
// compared with the line the same run printed.
func duration(d time.Duration) string {
	return fmt.Sprintf("%.2fs", d.Seconds())
}
