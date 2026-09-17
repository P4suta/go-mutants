// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/tempowner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

var bundleFiles = []string{
	doctorFileName,
	environmentFileName,
	errorFileName,
	manifestFileName,
	preservedPathsFileName,
	trace.FileName,
}

func inCopyOf(t *testing.T, name string) string {
	t.Helper()
	root := testkit.Copy(t, name)
	testkit.Env(t)
	t.Chdir(root)
	return root
}

func inFailingBaseline(t *testing.T) string {
	t.Helper()
	return inCopyOf(t, "failing-baseline")
}

func diagnosticsRootOf(root string) string {
	return filepath.Join(root, filepath.FromSlash(config.DefaultReportDirectory), diagnosticsDirectoryName)
}

func onlyEntry(t *testing.T, directory string) string {
	t.Helper()
	names := entriesOf(t, directory)
	if len(names) != 1 {
		t.Fatalf("%s holds %q, want exactly one entry", directory, names)
	}
	return names[0]
}

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
	if _, err := os.Stat(filepath.Join(directory, reportFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a run that published no report still wrote %s (%v)", reportFileName, err)
	}

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

	errorText := readBundleFile(t, directory, errorFileName)
	if block := errorBlockOf(t, stderr); !strings.HasPrefix(errorText, block) {
		t.Errorf("%s opens with\n%s\nwant the rendered error\n%s", errorFileName, errorText, block)
	}
	if !strings.Contains(errorText, "*engine.Error: ") {
		t.Errorf("%s carries no typed chain:\n%s", errorFileName, errorText)
	}

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

	doctor := readBundleFile(t, directory, doctorFileName)
	if !strings.Contains(doctor, checkModule) || !strings.Contains(doctor, root) {
		t.Errorf("%s is not this workspace's diagnosis:\n%s", doctorFileName, doctor)
	}

	manifest := readBundleFile(t, directory, manifestFileName)
	if err := schemas.Validate(schemas.DiagnosticsV1, []byte(manifest)); err != nil {
		t.Errorf("%s does not satisfy %s: %v\n%s", manifestFileName, schemas.DiagnosticsV1, err, manifest)
	}
	var described diagnosticsManifest
	if err := json.Unmarshal([]byte(manifest), &described); err != nil {
		t.Fatalf("%s does not decode: %v\n%s", manifestFileName, err, manifest)
	}
	var named []string
	for _, file := range described.Files {
		named = append(named, file.Name)
	}
	if got := entriesOf(t, directory); !slices.Equal(sorted(named), sorted(got)) {
		t.Errorf("%s names %q and the bundle holds %q", manifestFileName, sorted(named), sorted(got))
	}
	if described.Failure.Code != string(engine.CodeBaselineTestFailed) {
		t.Errorf("%s says the failure was %q, want %s",
			manifestFileName, described.Failure.Code, engine.CodeBaselineTestFailed)
	}
	if described.RunID == "" || described.ToolVersion == "" {
		t.Errorf("%s does not identify the run that wrote it: %+v", manifestFileName, described)
	}

	requireWrittenLast(t, directory, preservedPathsFileName)
}

func sorted(names []string) []string {
	names = slices.Clone(names)
	slices.Sort(names)
	return names
}

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

	var described diagnosticsManifest
	if err := json.Unmarshal([]byte(readBundleFile(t, directory, manifestFileName)), &described); err != nil {
		t.Fatalf("%s does not decode: %v", manifestFileName, err)
	}
	var named []string
	for _, file := range described.Files {
		named = append(named, file.Name)
	}
	if got := entriesOf(t, directory); !slices.Equal(sorted(named), sorted(got)) {
		t.Errorf("%s names %q and the directory holds %q", manifestFileName, sorted(named), sorted(got))
	}

	events, err := trace.Read(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatalf("the recording does not read back: %v", err)
	}
	if first := events[0]; first.Type != trace.TypeRunStart {
		t.Errorf("the stream opens with a %s, so the bundle overwrote the recording", first.Type)
	}
}

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

	bundleRoot := diagnosticsRootOf(root)
	directory := filepath.Join(bundleRoot, onlyEntry(t, bundleRoot))
	preserved := readBundleFile(t, directory, preservedPathsFileName)
	for kind, path := range kept {
		if !strings.Contains(preserved, kind+"\t"+path+"\n") {
			t.Errorf("%s does not name the kept %s:\n%s", preservedPathsFileName, kind, preserved)
		}
	}
}

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

func lookupOf(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

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

func refuseDiagnosticsWrites(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { traceFilesystem = trace.Filesystem{} })
	traceFilesystem = trace.Filesystem{
		WriteFile: func(string, []byte, fs.FileMode) error { return errors.New("no space left on device") },
	}
}

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

func TestKeepTempEnvironmentSpellings(t *testing.T) {
	cases := []struct {
		value string
		flag  string
	}{
		{"", ""},
		{"0", ""},
		{"false", ""},
		{"never", ""},
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

	t.Chdir(t.TempDir())
	code, _, stderr := execute(t, "run", "--keep-temp=sometimes")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, string(CodeUsage)) || !strings.Contains(stderr, "sometimes") {
		t.Errorf("stderr = %q, want a coded usage error quoting the value", stderr)
	}
}

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
	if flag, requested := traceFlag(""); requested {
		t.Errorf("GO_MUTANTS_TRACE= asked for %q, want no flag", flag)
	}
	if flag, requested := traceFlag("recordings"); !requested || flag != "--trace=recordings" {
		t.Errorf("GO_MUTANTS_TRACE=recordings asked for %q/%t, want the directory", flag, requested)
	}

	for _, value := range append(append([]string{}, yes...), no...) {
		_, trace := traceFlag(value)
		_, keep := keepTempFlag(value)
		_, diagnostics := diagnosticsFlag(value)
		on := slices.Contains(yes, value)
		if trace != on || keep != on || diagnostics == on {
			t.Errorf("%q is read as trace=%t keep=%t diagnostics=%t; the three disagree about whether it is a yes",
				value, trace, keep, !diagnostics)
		}
	}
}

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
	if flag, requested := keepTempFlag(engine.KeepTempNever.String()); requested {
		t.Errorf("GO_MUTANTS_KEEP_TEMP=never asked for %q, want no flag", flag)
	}
}

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

func TestDiagnosticsRetentionKeepsTheNewestTenAndNeverTouchesForeignNames(t *testing.T) {
	root := filepath.Join(t.TempDir(), diagnosticsDirectoryName)

	var bundles []string
	for day := range trace.RetainRuns + 2 {
		id := fmt.Sprintf("202609%02dT120000Z-a1b2", day+1)
		bundles = append(bundles, id)
		writeBundle(t, root, id, true)
	}
	foreign := []string{"notes", "20260907T120000Z-zzzz"}
	for _, name := range foreign {
		if err := os.MkdirAll(filepath.Join(root, name, "keep"), 0o755); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
	}
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

func TestTheBundleDiagnosesTheMachineEvenWhenTheRunsContextIsDone(t *testing.T) {
	root := inFailingBaseline(t)

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	code, _, stderr := executeContext(t, ctx, "run", "--no-tui", "--no-color")
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2: a deadline is a failure worth diagnosing\n%s", code, stderr)
	}

	bundleRoot := diagnosticsRootOf(root)
	directory := filepath.Join(bundleRoot, onlyEntry(t, bundleRoot))
	doctor := readBundleFile(t, directory, doctorFileName)

	for _, line := range strings.Split(doctor, "\n") {
		if !strings.Contains(line, checkToolchain) {
			continue
		}
		if strings.Contains(line, "context deadline exceeded") || strings.Contains(line, "context canceled") {
			t.Fatalf("the bundle diagnosed its own context instead of the machine:\n%s", doctor)
		}
		if !strings.HasPrefix(line, string(statusOK)) {
			t.Errorf("the toolchain row is %q, want the toolchain this run actually used", line)
		}
		return
	}
	t.Errorf("%s has no %q row:\n%s", doctorFileName, checkToolchain, doctor)
}

func TestATracedBundleThatWasNeverFinishedHoldsItsRecording(t *testing.T) {
	root := filepath.Join(t.TempDir(), traceDirectoryName)
	finished := "20260901T120000Z-0001"
	interrupted := "20260902T120000Z-0002"
	record(t, root, finished, false, nil)
	directory := record(t, root, interrupted, false, nil)
	testkit.WriteFile(t, filepath.Join(directory, errorFileName), []byte("error GOM4011: the baseline failed\n"))

	removed, err := collect(traceRootAt(root), retention{keep: 0})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !slices.Equal(removed, []string{finished}) {
		t.Errorf("removed %q, want only the recording whose account is whole (%q)", removed, finished)
	}
	if left := entriesOf(t, root); !slices.Contains(left, interrupted) {
		t.Fatalf("the trace root holds %q; the recording whose bundle was never finished was collected", left)
	}

	removed, err = collect(traceRootAt(root), retention{keep: 0, unfinished: true})
	if err != nil {
		t.Fatalf("collect --all: %v", err)
	}
	if !slices.Equal(removed, []string{interrupted}) {
		t.Errorf("--all removed %q, want the recording it was left holding (%q)", removed, interrupted)
	}
}
