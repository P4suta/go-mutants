// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of the diagnostics bundle and `--keep-temp`: what a
// whole run leaves behind, judged against the run that left it.
//
// It lives here rather than in internal/engine because what is under test is
// the sentence a user types. The engine can be told to keep its directories;
// only the command line decides where the bundle goes, what is in it, and
// whether the run that wrote it still exits the way it would have.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/cli/...
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/tempowner"
	"github.com/P4suta/go-mutants/trace"
)

// TestAFailingBaselineLeavesADiagnosticsBundleWithTheTraceAndTheCommand is the
// bundle read as what it is for: the whole of one failure, in one directory,
// without running anything again.
//
// Two joins are the point. The command that failed is in `error.txt`, because
// "the baseline failed" without the argv is a sentence nobody can act on — and
// it is the one part of an error go-mutants does not word itself. And the
// stream ends with the run-end, because a bundle whose account of the run stops
// early is a bundle that cannot say what the run was doing when it stopped.
func TestAFailingBaselineLeavesADiagnosticsBundleWithTheTraceAndTheCommand(t *testing.T) {
	root := inFailingBaseline(t)

	code, _, stderr := execute(t, "run", "--no-tui", "--no-color")
	if code != int(mutation.ExitInfrastructure) {
		t.Fatalf("exit = %d, want 2\n%s", code, stderr)
	}

	bundleRoot := diagnosticsRootOf(root)
	directory := filepath.Join(bundleRoot, onlyEntry(t, bundleRoot))
	errorText := readBundleFile(t, directory, errorFileName)
	if !strings.Contains(errorText, "    command: ") {
		t.Errorf("%s does not name the command that failed:\n%s", errorFileName, errorText)
	}
	if !strings.Contains(errorText, "    dir: ") {
		t.Errorf("%s does not say where the command ran:\n%s", errorFileName, errorText)
	}

	events, err := trace.Read(filepath.Join(directory, trace.FileName))
	if err != nil {
		t.Fatalf("the bundled stream does not read back: %v", err)
	}
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd {
		t.Fatalf("the bundled stream ends with a %s, want the run-end", last.Type)
	}
	if last.Run.Verdict == "" || last.Run.Error == "" {
		t.Errorf("the run-end says verdict %q and error %q, want both filled in for a failed run",
			last.Run.Verdict, last.Run.Error)
	}
	// The account reaches the failure rather than stopping at the phase before
	// it: the baseline test that did not pass is in there as a command.
	var baselineRuns int
	for _, event := range events {
		if event.Type == trace.TypeExec && event.Exec.Kind == trace.ExecKindBaselineTest {
			baselineRuns++
		}
	}
	if baselineRuns == 0 {
		t.Errorf("the bundled stream holds no baseline test run, so it stops short of the failure:\n%s",
			describeEvents(events))
	}
}

// TestKeepTempAlwaysOnASuccessfulRunKeepsAndPrints is the mode a person types
// at a terminal, on the run it is usually typed for.
//
// `always` means always, and a run that went fine is exactly the run somebody
// keeps the tree of: the question "what did the snapshot this survivor was
// measured in actually look like" is asked of successful runs far more often
// than of failed ones. A successful run writes no bundle, though — there is no
// failure to explain — so this is the one path where a keep and a diagnosis are
// deliberately not the same decision.
func TestKeepTempAlwaysOnASuccessfulRunKeepsAndPrints(t *testing.T) {
	root := inCopyOf(t, "simple")

	code, stdout, stderr := execute(t, "run", "--keep-temp", "--no-tui", "--no-color")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}

	kept := keptPaths(t, stdout)
	if len(kept) != 2 {
		t.Fatalf("the run named %v, want the snapshot and the scratch directory:\n%s", kept, stdout)
	}
	for kind, directory := range kept {
		marker, err := tempowner.ReadMarker(directory)
		if err != nil {
			t.Fatalf("reading the owner marker of the kept %s in %s: %v", kind, directory, err)
		}
		if !marker.Kept {
			t.Errorf("the kept %s at %s carries no keep marker", kind, directory)
		}
	}
	// And nothing was diagnosed, because nothing failed.
	if _, err := os.Stat(diagnosticsRootOf(root)); !os.IsNotExist(err) {
		t.Errorf("a successful run wrote a diagnostics bundle (%v)", err)
	}
	if strings.Contains(stderr, "diagnostics: ") {
		t.Errorf("a successful run said where a bundle went:\n%s", stderr)
	}
}
