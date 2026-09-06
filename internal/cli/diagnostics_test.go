// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/tempowner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

// bundleFiles is every file a complete bundle holds for an untraced run, in the
// order a listing returns them. It is written out rather than derived, because
// the point of the list is that adding a file to the bundle is a change
// somebody reviews.
var bundleFiles = []string{
	doctorFileName,
	environmentFileName,
	errorFileName,
	preservedPathsFileName,
	trace.FileName,
}

// inCopyOf copies one corpus module into a directory of the test's own, gives
// the test a hermetic environment, and makes the copy the working directory.
//
// A copy, never the corpus module: a run writes `reports/mutation/` into the
// directory it was started in, and a diagnostics bundle is one more thing it
// would leave in a checked-in fixture. The hermetic environment is what puts
// the snapshot and the scratch directory — which these tests deliberately ask
// to be kept — under a temporary directory the test owns and cleans up.
func inCopyOf(t *testing.T, name string) string {
	t.Helper()
	root := testkit.Copy(t, name)
	testkit.Env(t)
	t.Chdir(root)
	return root
}

// inFailingBaseline is [inCopyOf] on the module whose tests do not pass, which
// is the shortest real failure a run can be given.
func inFailingBaseline(t *testing.T) string {
	t.Helper()
	return inCopyOf(t, "failing-baseline")
}

// diagnosticsRootOf is where a run started in this workspace files its bundles.
func diagnosticsRootOf(root string) string {
	return filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory), diagnosticsDirectoryName)
}

// onlyEntry is the single name in a directory, failing the test when there is
// not exactly one.
func onlyEntry(t *testing.T, directory string) string {
	t.Helper()
	names := entriesOf(t, directory)
	if len(names) != 1 {
		t.Fatalf("%s holds %q, want exactly one entry", directory, names)
	}
	return names[0]
}

// errorBlockOf is what [RenderError] wrote to a stream: everything from the
// first coded line up to the diagnostics line the bundle adds underneath it.
func errorBlockOf(t *testing.T, stderr string) string {
	t.Helper()
	lines := strings.Split(stderr, "\n")
	start := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(line, "error GOM") })
	if start < 0 {
		t.Fatalf("stderr carries no coded error:\n%s", stderr)
	}
	end := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(line, "diagnostics: ") })
	if end < 0 {
		t.Fatalf("stderr does not say where the bundle went:\n%s", stderr)
	}
	return strings.Join(lines[start:end], "\n") + "\n"
}

// TestAFailedRunWritesTheBundleUnderTheReportDirectory is the whole of what a
// bundle promises, read off the run that produced it.
//
// It is one test rather than six because the claim is a single one: a run that
// failed left enough behind to be diagnosed without running it again. The
// interesting failures are the joins — a stream nobody can parse, an error file
// that says something other than what the console said, a bundle whose
// finished-marker was written before the files it marks — and each of those is
// invisible from one file alone.
func TestAFailedRunWritesTheBundleUnderTheReportDirectory(t *testing.T) {
	root := inFailingBaseline(t)

	code, _, stderr := execute(t, "run", "--no-tui", "--no-color")
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\n%s", code, stderr)
	}

	bundleRoot := diagnosticsRootOf(root)
	directory := filepath.Join(bundleRoot, onlyEntry(t, bundleRoot))
	if !strings.Contains(stderr, "diagnostics: "+directory+"\n") {
		t.Fatalf("the run did not say where the bundle went:\n%s", stderr)
	}
	if got := entriesOf(t, directory); !slices.Equal(sorted(got), bundleFiles) {
		t.Errorf("the bundle holds %q, want %q", sorted(got), bundleFiles)
	}
	// No report.json: the run stopped in the baseline, so there was never a
	// document to copy — and an empty file would be worse than an absent one.
	if _, err := os.Stat(filepath.Join(directory, reportFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a run that published no report still wrote %s (%v)", reportFileName, err)
	}

	// The stream is the ring the untraced run kept, written out as the
	// published contract. Every line of it, because a consumer reads a stream to
	// the end.
	stream := filepath.Join(directory, trace.FileName)
	raw, err := os.ReadFile(stream)
	if err != nil {
		t.Fatalf("reading the bundled stream: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i, line := range lines {
		if validateErr := schemas.Validate(schemas.TraceEventV1, []byte(line)); validateErr != nil {
			t.Errorf("line %d does not satisfy %s: %v\n%s", i+1, schemas.TraceEventV1, validateErr, line)
		}
	}
	events, err := trace.Read(stream)
	if err != nil {
		t.Fatalf("the bundled stream does not read back: %v", err)
	}
	if last := events[len(events)-1]; last.Type != trace.TypeRunEnd {
		t.Errorf("the bundled stream ends with a %s, want the run-end", last.Type)
	}

	// The error file opens with exactly what the console said, so that a bug
	// report holding the bundle and a bug report holding a paste of the terminal
	// are the same bug report.
	errorText := readBundleFile(t, directory, errorFileName)
	if block := errorBlockOf(t, stderr); !strings.HasPrefix(errorText, block) {
		t.Errorf("%s opens with\n%s\nwant the rendered error\n%s", errorFileName, errorText, block)
	}
	// And then the two things a console never shows: the error printed in full,
	// and the typed chain underneath it.
	if !strings.Contains(errorText, "*engine.Error: ") {
		t.Errorf("%s carries no typed chain:\n%s", errorFileName, errorText)
	}

	// Names, never values. An environment file is attached to bug reports, and
	// the values are where the tokens are.
	environment := readBundleFile(t, directory, environmentFileName)
	_, names, ok := strings.Cut(environment, environmentHeading+"\n")
	if !ok {
		t.Fatalf("%s has no %q section:\n%s", environmentFileName, environmentHeading, environment)
	}
	if strings.Contains(names, "=") {
		t.Errorf("the environment section carries a value:\n%s", names)
	}
	for _, needed := range []string{"go-mutants " + Version, "platform: " + runtime.GOOS + "/" + runtime.GOARCH} {
		if !strings.Contains(environment, needed) {
			t.Errorf("%s does not say %q:\n%s", environmentFileName, needed, environment)
		}
	}

	// The doctor table, run against the workspace rather than reprinted from
	// somewhere: the row that says which module this is has to name this one.
	doctor := readBundleFile(t, directory, doctorFileName)
	if !strings.Contains(doctor, checkModule) || !strings.Contains(doctor, root) {
		t.Errorf("%s is not this workspace's diagnosis:\n%s", doctorFileName, doctor)
	}

	// Written last, because its existence is what says the bundle is complete
	// and therefore collectable. A marker written before the files it marks
	// would let a collector remove a bundle that was still being written.
	requireWrittenLast(t, directory, preservedPathsFileName)
}

// sorted is a sorted copy, so that a listing can be compared with a fixed list.
func sorted(names []string) []string {
	names = slices.Clone(names)
	slices.Sort(names)
	return names
}

// readBundleFile reads one file of a bundle, failing the test when it is not
// there or holds nothing: a bundle never writes an empty file, because an empty
// file is a question a reader has to answer by looking somewhere else.
func readBundleFile(t *testing.T, directory, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	if len(raw) == 0 {
		t.Fatalf("%s is empty", name)
	}
	return string(raw)
}

// requireWrittenLast fails unless name is at least as new as every other file
// beside it.
func requireWrittenLast(t *testing.T, directory, name string) {
	t.Helper()
	marker, err := os.Stat(filepath.Join(directory, name))
	if err != nil {
		t.Fatalf("stat %s: %v", name, err)
	}
	for _, other := range entriesOf(t, directory) {
		if other == name {
			continue
		}
		info, statErr := os.Stat(filepath.Join(directory, other))
		if statErr != nil {
			t.Fatalf("stat %s: %v", other, statErr)
		}
		if marker.ModTime().Before(info.ModTime()) {
			t.Errorf("%s was written before %s, so a collector can see a finished bundle that is not", name, other)
		}
	}
}

// TestATracedFailureWritesTheBundleBesideItsStreamWithoutRepeatingIt keeps the
// two halves of one run's account in one directory, and keeps the account
// itself written once.
//
// A traced run already has its stream on disk, beside the output its events
// digested. Writing the ring out again next to it would produce a second
// `trace.jsonl` that is a bounded suffix of the first — the same run, told
// twice, one of the tellings incomplete — so the bundle joins the recording
// instead of duplicating it.
func TestATracedFailureWritesTheBundleBesideItsStreamWithoutRepeatingIt(t *testing.T) {
	root := inFailingBaseline(t)

	code, _, stderr := execute(t, "run", "--trace", "--no-tui", "--no-color")
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\n%s", code, stderr)
	}

	traceRoot := traceRootOf(root)
	directory := filepath.Join(traceRoot, onlyEntry(t, traceRoot))
	if !strings.Contains(stderr, "diagnostics: "+directory+"\n") {
		t.Fatalf("the traced run did not put the bundle beside its recording:\n%s", stderr)
	}
	if _, err := os.Stat(diagnosticsRootOf(root)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a traced run also made a diagnostics root (%v)", err)
	}
	for _, name := range []string{errorFileName, environmentFileName, doctorFileName, preservedPathsFileName} {
		readBundleFile(t, directory, name)
	}

	// The one `trace.jsonl` there is the recording, not the ring: it opens with
	// the run-start, which a bounded ring's dump need not carry at all.
	events, err := trace.Read(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatalf("the recording does not read back: %v", err)
	}
	if first := events[0]; first.Type != trace.TypeRunStart {
		t.Errorf("the stream opens with a %s, so the bundle overwrote the recording", first.Type)
	}
}

// TestPreservedPathsAlwaysExistsAndSaysWhenNothingWasKept is the file that has
// to be there whatever happened, because its absence is what a collector reads
// as "still being written".
//
// It also has to say something when there is nothing to say. A zero-length file
// is a question — did the run keep nothing, or did the writer stop? — and the
// one sentence costs nothing to write.
func TestPreservedPathsAlwaysExistsAndSaysWhenNothingWasKept(t *testing.T) {
	root := inFailingBaseline(t)

	code, _, stderr := execute(t, "run", "--no-tui", "--no-color")
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\n%s", code, stderr)
	}
	bundleRoot := diagnosticsRootOf(root)
	directory := filepath.Join(bundleRoot, onlyEntry(t, bundleRoot))

	if got := readBundleFile(t, directory, preservedPathsFileName); got != nothingKept+"\n" {
		t.Errorf("%s = %q, want %q", preservedPathsFileName, got, nothingKept+"\n")
	}
}

// TestKeepTempOnFailurePrintsTheKeptPaths is the pair of lines that makes a
// keep usable: a directory somebody cannot find is a directory they did not
// keep.
//
// They are printed by the plain renderer, which owns standard output for a run
// that did not ask for `--json` — the same stream the report paths are printed
// on, and for the same reason: these are paths to be selected and pasted. Under
// `--json` the document owns standard output and every rendered line moves to
// standard error together.
func TestKeepTempOnFailurePrintsTheKeptPaths(t *testing.T) {
	root := inFailingBaseline(t)

	code, stdout, stderr := execute(t, "run", "--keep-temp=on-failure", "--no-tui", "--no-color")
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\n%s", code, stderr)
	}

	kept := keptPaths(t, stdout)
	for _, kind := range []string{engine.KeptSnapshot, engine.KeptScratch} {
		directory, ok := kept[kind]
		if !ok {
			t.Fatalf("the run did not say where it kept the %s:\n%s", kind, stdout)
		}
		if _, err := os.Stat(directory); err != nil {
			t.Errorf("the run says it kept %s and it is not there: %v", directory, err)
		}
		marker, err := tempowner.ReadMarker(directory)
		if err != nil {
			t.Fatalf("reading the owner marker in %s: %v", directory, err)
		}
		if !marker.Kept {
			t.Errorf("%s was left behind without its marker saying kept, so the next run will collect it", directory)
		}
	}

	// And the bundle names the same two directories, so that somebody reading
	// the bundle rather than the console finds them too.
	bundleRoot := diagnosticsRootOf(root)
	directory := filepath.Join(bundleRoot, onlyEntry(t, bundleRoot))
	preserved := readBundleFile(t, directory, preservedPathsFileName)
	for kind, path := range kept {
		if !strings.Contains(preserved, kind+"\t"+path+"\n") {
			t.Errorf("%s does not name the kept %s:\n%s", preservedPathsFileName, kind, preserved)
		}
	}
}

// keptPaths reads the `kept <kind>: <path>` lines out of a run's output.
func keptPaths(t *testing.T, out string) map[string]string {
	t.Helper()
	kept := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, "kept ")
		if !ok {
			continue
		}
		kind, path, split := strings.Cut(rest, ": ")
		if !split {
			t.Fatalf("the line %q is not `kept <kind>: <path>`", line)
		}
		kept[kind] = path
	}
	return kept
}

// TestNoDiagnosticsSuppressesTheBundle is the off switch, in both spellings.
//
// A bundle is written into the user's own tree, and somebody whose CI publishes
// `report.directory` as an artefact may not want a directory per failed run in
// it. The flag and the variable are the same request: the variable exists for
// the invocation nobody can add a flag to.
func TestNoDiagnosticsSuppressesTheBundle(t *testing.T) {
	t.Run("the flag", func(t *testing.T) {
		root := inFailingBaseline(t)
		code, _, stderr := execute(t, "run", "--no-diagnostics", "--no-tui", "--no-color")
		requireNoBundle(t, root, code, stderr)
	})
	t.Run("the environment", func(t *testing.T) {
		root := inFailingBaseline(t)
		args := withEnvironmentFlags([]string{"run", "--no-tui", "--no-color"}, lookupOf(map[string]string{
			diagnosticsEnvironmentVariable: "0",
		}))
		code, _, stderr := execute(t, args...)
		requireNoBundle(t, root, code, stderr)
	})
}

// requireNoBundle fails unless the run failed and left no bundle behind.
func requireNoBundle(t *testing.T, root string, code int, stderr string) {
	t.Helper()
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\n%s", code, stderr)
	}
	if _, err := os.Stat(diagnosticsRootOf(root)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a suppressed run wrote a bundle anyway (%v)", err)
	}
	if strings.Contains(stderr, "diagnostics: ") {
		t.Errorf("a suppressed run said where a bundle went:\n%s", stderr)
	}
}

// lookupOf is [os.LookupEnv] over a map, which is how the environment rules are
// checked without a process-wide variable.
func lookupOf(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

// TestABundleThatCannotBeWrittenIsReportedAndNeverChangesTheExitCode is the
// invariant the whole feature lives or dies by.
//
// A diagnostic that can change what a run reports inverts the point of having
// one. So a disk that will not take the bundle costs the bundle: the same
// failure, the same exit status, and a warning saying the diagnosis is not
// there — which is a far better answer than a CI job reading "exit 2 because
// go-mutants could not write a file" for a run whose tests genuinely failed.
func TestABundleThatCannotBeWrittenIsReportedAndNeverChangesTheExitCode(t *testing.T) {
	inFailingBaseline(t)

	clean, _, cleanStderr := execute(t, "run", "--no-tui", "--no-color")
	if clean != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\n%s", clean, cleanStderr)
	}

	refuseDiagnosticsWrites(t)
	code, _, stderr := execute(t, "run", "--no-tui", "--no-color")
	if code != clean {
		t.Errorf("exit = %d with a disk that refused the bundle, and %d without one", code, clean)
	}
	if !strings.Contains(stderr, "warning "+string(CodeDiagnosticsUnavailable)+":") {
		t.Errorf("stderr does not report the bundle it could not write:\n%s", stderr)
	}
	if strings.Contains(stderr, "diagnostics: ") {
		t.Errorf("the run named a bundle it did not write:\n%s", stderr)
	}
	// The exit status alone is not the claim. Both failures are exit 2, so a
	// run that returned the *bundle's* error instead of its own would pass every
	// assertion above while telling the user go-mutants could not write a file
	// and never that their tests did not pass. What survives has to be the run's
	// own failure, whole: its code, the command it was about, and the child's
	// own words underneath.
	for _, needed := range []string{
		"error GOM4011:",
		"    command: ",
		"this fixture fails on purpose",
	} {
		if !strings.Contains(stderr, needed) {
			t.Errorf("stderr lost the run's own failure (%q):\n%s", needed, stderr)
		}
	}
}

// refuseDiagnosticsWrites points the bundle at a filesystem that refuses it, for
// the length of one test. It is the seam a refused recording already uses,
// because the two are written through one hooks value.
//
// The directory is created and the first write fails, which is the worse of the
// two failures and the one worth producing: a refusal to create the directory
// leaves nothing behind, while a write that fails halfway leaves a directory
// somebody has to account for.
func refuseDiagnosticsWrites(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { traceFilesystem = trace.Filesystem{} })
	traceFilesystem = trace.Filesystem{
		WriteFile: func(string, []byte, fs.FileMode) error { return errors.New("no space left on device") },
	}
}

// TestABundleThatCannotBeWrittenLeavesNoDirectoryBehind is goat cleanliness on
// the failure path, and the one leak this feature can produce.
//
// A bundle whose first write fails leaves `<diagnostics root>/<run-id>/` with
// nothing in it. That directory has no `error.txt`, so it is not one the
// retention can see and not one `trace clean --all` can remove — and while it is
// there, the empty diagnostics root cannot be removed either. Every byte a run
// writes has an owner and a collector; a directory neither can name has neither.
func TestABundleThatCannotBeWrittenLeavesNoDirectoryBehind(t *testing.T) {
	root := inFailingBaseline(t)

	refuseDiagnosticsWrites(t)
	code, _, stderr := execute(t, "run", "--no-tui", "--no-color")
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\n%s", code, stderr)
	}

	bundleRoot := diagnosticsRootOf(root)
	entries, err := os.ReadDir(bundleRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("reading %s: %v", bundleRoot, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("a bundle that could not be written left %q in %s, "+
			"which no collector can name and no `trace clean` can remove", names, bundleRoot)
	}
}

// TestAnInterruptedRunWritesNoBundle keeps the diagnosis for the runs that need
// one.
//
// Nothing went wrong when a user pressed Ctrl-C: there is no failure to explain,
// the run stopped where it was asked to, and a directory per cancelled run in
// somebody's tree is exhaust rather than evidence. It is the same rule
// `--keep-temp=on-failure` obeys, decided by the same predicate.
func TestAnInterruptedRunWritesNoBundle(t *testing.T) {
	root := inFailingBaseline(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	code, _, stderr := executeContext(t, ctx, "run", "--no-tui", "--no-color")

	if code != int(mutation.ExitInterrupted) {
		t.Fatalf("exit = %d, want %d\n%s", code, mutation.ExitInterrupted, stderr)
	}
	if _, err := os.Stat(diagnosticsRootOf(root)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an interrupted run wrote a bundle (%v)", err)
	}
	if strings.Contains(stderr, "diagnostics: ") {
		t.Errorf("an interrupted run said where a bundle went:\n%s", stderr)
	}
}

// TestKeepTempEnvironmentSpellings is the variable's whole vocabulary.
//
// The empty value is the one worth stating: `GO_MUTANTS_KEEP_TEMP=` in a CI
// configuration is how a job switches an inherited request off, so it asks for
// nothing rather than for a mode named by the empty string. Anything else
// becomes the flag's own value and is refused by the flag, which is where a
// mistake in a mode name belongs — one description of what the option accepts,
// and one message when it does not.
func TestKeepTempEnvironmentSpellings(t *testing.T) {
	cases := []struct {
		value string
		flag  string
	}{
		{"", ""},
		{"0", ""},
		{"false", ""},
		{"never", ""},
		// Every spelling [strconv.ParseBool] accepts, in both cases. A CI file
		// written by a person says `TRUE` as readily as `true`, and a boolean
		// that reads one and silently takes the other as a mode name is a
		// booby trap: the value would reach the flag, which would refuse it and
		// fail a run that asked for a directory.
		{"TRUE", "--keep-temp=always"},
		{"True", "--keep-temp=always"},
		{"t", "--keep-temp=always"},
		{"T", "--keep-temp=always"},
		{"FALSE", ""},
		{"False", ""},
		{"f", ""},
		{"F", ""},
		{"1", "--keep-temp=always"},
		{"true", "--keep-temp=always"},
		{"always", "--keep-temp=always"},
		{"on-failure", "--keep-temp=on-failure"},
		{"sometimes", "--keep-temp=sometimes"},
	}
	for _, c := range cases {
		t.Run("GO_MUTANTS_KEEP_TEMP="+c.value, func(t *testing.T) {
			flag, requested := keepTempFlag(c.value)
			if requested != (c.flag != "") || flag != c.flag {
				t.Fatalf("keepTempFlag(%q) = %q/%t, want %q/%t", c.value, flag, requested, c.flag, c.flag != "")
			}
			if c.flag == "" {
				return
			}
			got := withEnvironmentFlags([]string{"run", "--", "go", "test", "./..."},
				lookupOf(map[string]string{keepTempEnvironmentVariable: c.value}))
			want := []string{"run", c.flag, "--", "go", "test", "./..."}
			if !slices.Equal(got, want) {
				t.Errorf("the argument vector is %q, want %q", got, want)
			}
		})
	}

	// An unknown value reaches the flag, which refuses it by name — the same
	// message somebody who typed it would get.
	t.Chdir(t.TempDir())
	code, _, stderr := execute(t, "run", "--keep-temp=sometimes")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeUsage)) || !strings.Contains(stderr, "sometimes") {
		t.Errorf("stderr = %q, want a coded usage error quoting the value", stderr)
	}
}

// TestDiagnosticsEnvironmentSpellings is the same vocabulary for the switch
// that turns the bundle off. The default is on, so the empty value means on.
//
// The two directions are not symmetric in what they cost when they are wrong,
// which is why every spelling is listed. A value read as "on" when the user
// meant "off" writes a directory they did not want; a value read as "off" when
// they meant "on" takes away the diagnosis of a failure they cannot reproduce,
// and says nothing about having done so. `GO_MUTANTS_DIAGNOSTICS=TRUE` must not
// be the second of those.
func TestDiagnosticsEnvironmentSpellings(t *testing.T) {
	on := []string{"", "1", "t", "T", "true", "TRUE", "True"}
	off := []string{"0", "f", "F", "false", "FALSE", "False"}
	for _, value := range on {
		if flag, requested := diagnosticsFlag(value); requested {
			t.Errorf("GO_MUTANTS_DIAGNOSTICS=%q asked for %q, want the bundle left on", value, flag)
		}
	}
	for _, value := range off {
		flag, requested := diagnosticsFlag(value)
		if !requested || flag != "--no-diagnostics" {
			t.Errorf("GO_MUTANTS_DIAGNOSTICS=%q asked for %q/%t, want --no-diagnostics", value, flag, requested)
		}
	}
	// Anything else reaches the flag, which refuses it by name rather than
	// guessing which of the two the user meant.
	flag, requested := diagnosticsFlag("maybe")
	if !requested || flag != "--no-diagnostics=maybe" {
		t.Errorf("diagnosticsFlag(%q) = %q/%t, want the value carried to the flag", "maybe", flag, requested)
	}
	t.Chdir(t.TempDir())
	code, _, stderr := execute(t, "run", "--no-diagnostics=maybe")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeUsage)) || !strings.Contains(stderr, "maybe") {
		t.Errorf("stderr = %q, want a coded usage error quoting the value", stderr)
	}
}

// TestEveryBooleanEnvironmentVariableReadsTheSameSpellings is the rule the three
// variables are worth having together.
//
// A user who learns that `GO_MUTANTS_DIAGNOSTICS=TRUE` works and then finds that
// `GO_MUTANTS_TRACE=TRUE` records into a directory called `TRUE` has learned
// that go-mutants has no rule, only three implementations. [strconv.ParseBool]
// is the rule, and it is the one every Go program on the machine already
// answers to.
//
// `GO_MUTANTS_TRACE` is the one whose non-boolean values mean something — a
// directory to record into — which is exactly why it has to agree about which
// values are boolean in the first place.
func TestEveryBooleanEnvironmentVariableReadsTheSameSpellings(t *testing.T) {
	yes := []string{"1", "t", "T", "true", "TRUE", "True"}
	no := []string{"0", "f", "F", "false", "FALSE", "False"}

	for _, value := range yes {
		if flag, requested := traceFlag(value); !requested || flag != "--trace" {
			t.Errorf("GO_MUTANTS_TRACE=%q asked for %q/%t, want a bare --trace", value, flag, requested)
		}
	}
	for _, value := range no {
		if flag, requested := traceFlag(value); requested {
			t.Errorf("GO_MUTANTS_TRACE=%q asked for %q, want no recording", value, flag)
		}
	}
	// And the unset variable, which is how a job switches an inherited request
	// back off rather than a directory named by nothing.
	if flag, requested := traceFlag(""); requested {
		t.Errorf("GO_MUTANTS_TRACE= asked for %q, want no flag", flag)
	}
	// Anything that is not a boolean is this variable's own vocabulary, and is
	// untouched by the rule above: it names where to record.
	if flag, requested := traceFlag("recordings"); !requested || flag != "--trace=recordings" {
		t.Errorf("GO_MUTANTS_TRACE=recordings asked for %q/%t, want the directory", flag, requested)
	}

	// The three variables answer the same question the same way, which is the
	// claim. Each has its own idea of what "yes" and "no" then *do*, and that is
	// the part that differs and is checked in each variable's own test.
	for _, value := range append(append([]string{}, yes...), no...) {
		_, trace := traceFlag(value)
		_, keep := keepTempFlag(value)
		_, diagnostics := diagnosticsFlag(value)
		on := slices.Contains(yes, value)
		// Trace and keep-temp are off by default and their flag appears for a
		// yes; diagnostics is on by default and its flag appears for a no.
		if trace != on || keep != on || diagnostics == on {
			t.Errorf("%q is read as trace=%t keep=%t diagnostics=%t; the three disagree about whether it is a yes",
				value, trace, keep, !diagnostics)
		}
	}
}

// TestEveryKeepTempModePrintsAWordTheFlagAccepts closes the loop
// [KeepTemp.String] opens.
//
// Its documentation says the mode's spelling and the flag's spelling are one
// string, and `never` was the one that was not: it is what the zero value
// prints, what a `--help` reader would try, and what `parseKeepTemp` refused.
// Round-tripping every value is the assertion that keeps the promise true for
// the next mode somebody adds rather than for the two that exist today.
func TestEveryKeepTempModePrintsAWordTheFlagAccepts(t *testing.T) {
	for _, mode := range []engine.KeepTemp{engine.KeepTempNever, engine.KeepTempAlways, engine.KeepTempOnFailure} {
		got, err := parseKeepTemp(mode.String())
		if err != nil {
			t.Errorf("parseKeepTemp(%q) refused the word KeepTemp(%d) prints: %v", mode.String(), int(mode), err)
			continue
		}
		if got != mode {
			t.Errorf("parseKeepTemp(%q) = %v, want %v", mode.String(), got, mode)
		}
	}
	// And the environment reads it as the request it is: `never` is off, like
	// the other three ways of saying so.
	if flag, requested := keepTempFlag(engine.KeepTempNever.String()); requested {
		t.Errorf("GO_MUTANTS_KEEP_TEMP=never asked for %q, want no flag", flag)
	}
}

// TestKeepTempFlagRequiresAnEqualsSign reports the one mistake a
// correct-looking command line produces, and says how to write it instead.
//
// `--keep-temp` takes an optional value, which pflag can only express as
// `--keep-temp=MODE`: written with a space, the mode becomes a positional
// argument and the run would keep everything instead of keeping on failure. It
// is the mistake `--changed` and `--trace` already make possible, so it gets the
// same answer.
func TestKeepTempFlagRequiresAnEqualsSign(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, stderr := execute(t, "run", "--keep-temp", "on-failure")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeUsage)) {
		t.Errorf("stderr = %q, want a coded usage error", stderr)
	}
	if !strings.Contains(stderr, "--keep-temp=on-failure") {
		t.Errorf("stderr = %q, want the equals-sign spelling", stderr)
	}
}

// TestRunHelpMentionsKeepTempAndDiagnostics keeps the two options discoverable
// where somebody looks for them.
//
// A flag nobody can find is a flag nobody uses, and both of these exist for the
// moment somebody is staring at a failure and wondering what else there is. The
// variables are named too, because the invocation that most needs them — a CI
// step whose command line is generated by somebody else — is the one nobody can
// add a flag to.
func TestRunHelpMentionsKeepTempAndDiagnostics(t *testing.T) {
	code, stdout, _ := execute(t, "run", "--help")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, needle := range []string{
		"--keep-temp",
		"--no-diagnostics",
		keepTempEnvironmentVariable,
		diagnosticsEnvironmentVariable,
		"on-failure",
	} {
		if !strings.Contains(stdout, needle) {
			t.Errorf("run --help does not mention %q:\n%s", needle, stdout)
		}
	}
}

// writeBundle makes one bundle in a diagnostics root, finished or not. A
// finished one carries the file whose existence says so.
func writeBundle(t *testing.T, root, runID string, finished bool) string {
	t.Helper()
	directory := filepath.Join(root, runID)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("creating %s: %v", directory, err)
	}
	testkit.WriteFile(t, filepath.Join(directory, errorFileName), []byte("error GOM4011: the baseline failed\n"))
	if finished {
		testkit.WriteFile(t, filepath.Join(directory, preservedPathsFileName), []byte(nothingKept+"\n"))
	}
	return directory
}

// TestDiagnosticsRetentionKeepsTheNewestTenAndNeverTouchesForeignNames is the
// collector half of "every byte a run writes has an owner and a collector",
// applied to the second directory a run may fill.
//
// The rule is the trace root's, because it is the same rule: only a directory
// named by a run id and holding a bundle of ours is collectable at all, and one
// that was never finished is left alone — that is a run still writing, or one
// that died on the way, and the second is the bundle a reader most wants.
func TestDiagnosticsRetentionKeepsTheNewestTenAndNeverTouchesForeignNames(t *testing.T) {
	root := filepath.Join(t.TempDir(), diagnosticsDirectoryName)

	var bundles []string
	for day := range trace.RetainRuns + 2 {
		id := fmt.Sprintf("202609%02dT120000Z-a1b2", day+1)
		bundles = append(bundles, id)
		writeBundle(t, root, id, true)
	}
	// Everything that is not a bundle, and must survive being beside twelve of
	// them.
	foreign := []string{"notes", "20260907T120000Z-zzzz"}
	for _, name := range foreign {
		if err := os.MkdirAll(filepath.Join(root, name, "keep"), 0o755); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
	}
	// A run-id-shaped directory with nothing of ours in it, and a bundle that
	// was never finished. Neither is the collector's.
	if err := os.MkdirAll(filepath.Join(root, "20260908T120000Z-c3d4"), 0o755); err != nil {
		t.Fatalf("creating the empty directory: %v", err)
	}
	unfinished := writeBundle(t, root, "20260801T120000Z-dead", false)
	testkit.WriteFile(t, filepath.Join(root, "README"), []byte("mine"))

	removed, err := collect(diagnosticsRootAt(root), retention{keep: trace.RetainRuns})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := bundles[:len(bundles)-trace.RetainRuns]
	if !slices.Equal(removed, want) {
		t.Errorf("removed %q, want the oldest %q", removed, want)
	}

	left := entriesOf(t, root)
	for _, name := range append([]string{"README", "20260908T120000Z-c3d4", filepath.Base(unfinished)}, foreign...) {
		if !slices.Contains(left, name) {
			t.Errorf("%q was collected; only a finished bundle is the collector's", name)
		}
	}
	for _, name := range want {
		if slices.Contains(left, name) {
			t.Errorf("the bundle %q survived and is older than the newest %d", name, trace.RetainRuns)
		}
	}
}

// TestTraceCleanAlsoSweepsDiagnostics keeps one command for the exhaust of one
// workspace.
//
// The two directories are filled by the same runs, collected by the same rule,
// and deleted for the same reason, so a second command would be a second thing
// to remember and a second place for somebody to find a full disk. `trace list`
// is deliberately not extended: a listing answers "what recordings are there",
// and bundles are not recordings.
func TestTraceCleanAlsoSweepsDiagnostics(t *testing.T) {
	root := tracedWorkspace(t)
	traceRoot := traceRootOf(root)
	record(t, traceRoot, "20260901T120000Z-0001", false, nil)

	bundleRoot := diagnosticsRootOf(root)
	writeBundle(t, bundleRoot, "20260901T120000Z-0001", true)
	writeBundle(t, bundleRoot, "20260902T120000Z-0002", true)
	testkit.WriteFile(t, filepath.Join(bundleRoot, "NOTES"), []byte("mine"))

	code, stdout, stderr := execute(t, "trace", "clean")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "diagnostics root: "+bundleRoot) {
		t.Errorf("stdout does not say which bundles it swept:\n%s", stdout)
	}
	if !strings.Contains(stdout, "2 bundles") {
		t.Errorf("stdout does not report the bundles it removed:\n%s", stdout)
	}
	if left := entriesOf(t, bundleRoot); !slices.Equal(left, []string{"NOTES"}) {
		t.Errorf("the diagnostics root holds %q, want only the file that is not a bundle", left)
	}
	if _, err := os.Stat(traceRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the trace root survived its last recording (%v)", err)
	}
}

// TestDiagnosticsDirectoryInsideTheWorkspaceOutsideTheReportDirectoryIsRefused
// is the trace's own rule, applied to the other directory a run writes.
//
// internal/snapshot digests the workspace and excludes `report.directory` and
// nothing else inside it. A bundle written anywhere else in the tree is a file
// that appeared during a run, and the next run would report it as drift. The
// check is symlink-aware because the refusal is a statement about where the
// files land rather than about how the path was spelled — and a link at the
// diagnostics directory pointing back into the tree is exactly the spelling
// that hides it.
func TestDiagnosticsDirectoryInsideTheWorkspaceOutsideTheReportDirectoryIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need a privilege this test cannot assume on Windows")
	}
	workspace := resolvedTempDir(t)
	reports := filepath.Join(workspace, filepath.FromSlash(config.DefaultReportDirectory))
	if err := os.MkdirAll(filepath.Join(workspace, "internal"), 0o755); err != nil {
		t.Fatalf("creating the directory inside the workspace: %v", err)
	}
	if err := os.MkdirAll(reports, 0o755); err != nil {
		t.Fatalf("creating the report directory: %v", err)
	}

	// The ordinary answer first, so that the refusal below is about the link and
	// not about the arrangement.
	got, err := diagnosticsRoot(workspace, config.DefaultReportDirectory)
	if err != nil {
		t.Fatalf("diagnosticsRoot: %v", err)
	}
	if want := filepath.Join(reports, diagnosticsDirectoryName); got != want {
		t.Errorf("diagnosticsRoot = %q, want %q", got, want)
	}

	if err = os.Symlink(filepath.Join(workspace, "internal"), got); err != nil {
		t.Fatalf("linking the diagnostics directory into the workspace: %v", err)
	}
	refused, err := diagnosticsRoot(workspace, config.DefaultReportDirectory)
	if err == nil {
		t.Fatalf("diagnosticsRoot through a link into the workspace = %q, want a refusal", refused)
	}
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeDiagnosticsUnavailable {
		t.Errorf("the refusal is %v, want an *Error coded %s", err, CodeDiagnosticsUnavailable)
	}
}
