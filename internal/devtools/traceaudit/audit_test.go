// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package traceaudit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/devtools/traceaudit"
)

// auditedRuns are the run/recording pairs committed beside this test.
//
// They are committed rather than produced, and that is the decision worth
// stating. A test that made its own run would need a toolchain, would take the
// minutes a full mutation run takes, and would only ever check the engine as it
// is *now* -- so a change that made the report and the recording disagree would
// be checked by a run that already contained the change. A committed pair is a
// run this repository made once, on purpose, and every later commit is audited
// against it in the tier that runs on every push.
//
// Regenerate them when the documents change shape:
//
//	go run ./cmd/go-mutants run --json --trace > report.json
//
// from a copy of the fixture, and copy the report and the `trace.jsonl` beside
// it. A pair that no longer describes its fixture is not wrong -- the audit is
// about two documents agreeing with each other -- but it is less interesting,
// so refresh them when the fixture moves.
var auditedRuns = []string{"killable", "coverage"}

// TestTheReportAndTheRecordingAgree is the standing self-disagreement check.
func TestTheReportAndTheRecordingAgree(t *testing.T) {
	t.Parallel()

	for _, name := range auditedRuns {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result := audit(t, name)

			if violations := result.Violations(); len(violations) != 0 {
				var lines []string
				for _, violation := range violations {
					lines = append(lines, violation.String())
				}
				t.Errorf("the report and the recording of %s disagree:\n\t%s",
					name, strings.Join(lines, "\n\t"))
			}
			for _, finding := range result.Unaudited() {
				t.Logf("%s: %s", name, finding)
			}
			// A pair that settles nothing agrees with everything.
			if result.Audited == 0 {
				t.Fatalf("%s: the recording settled none of its %d mutants, so this checks nothing",
					name, result.Mutants)
			}
			if result.Mutants == 0 {
				t.Fatalf("%s: the report holds no mutants", name)
			}
			t.Logf("%s: %d mutants, %d audited, %d unaudited",
				name, result.Mutants, result.Audited, len(result.Unaudited()))
		})
	}
}

// disagreements are the ways the two documents can contradict each other, each
// with an edit to one of them that produces it.
//
// A gate nobody has watched fail is a gate nobody knows the shape of. These are
// the shapes: each edits the committed report -- never the recording, because
// the recording is the account and editing it would be inventing a run -- and
// requires the audit to say so.
var disagreements = []struct {
	name  string
	layer string
	edit  func(string) string
}{
	{
		name:  "a mutant said to be uncovered that was executed",
		layer: "uncovered",
		edit: func(report string) string {
			return strings.Replace(report, `"uncovered": false`, `"uncovered": true`, 1)
		},
	},
	{
		name:  "a verdict the recording contradicts",
		layer: "verdict",
		edit: func(report string) string {
			return strings.Replace(report, `"outcome": "killed"`, `"outcome": "survived"`, 1)
		},
	},
	{
		name:  "a summary that does not count its own rows",
		layer: "summary",
		edit: func(report string) string {
			return strings.Replace(report, `"total": 13`, `"total": 12`, 1)
		},
	},
}

// TestTheAuditNoticesEachKindOfDisagreement is what makes the test above worth
// having.
func TestTheAuditNoticesEachKindOfDisagreement(t *testing.T) {
	t.Parallel()

	for _, broken := range disagreements {
		t.Run(broken.name, func(t *testing.T) {
			t.Parallel()

			original := read(t, "killable.report.json")
			edited := broken.edit(original)
			if edited == original {
				t.Fatalf("the edit changed nothing, so this case pins no shape")
			}

			directory := t.TempDir()
			reportPath := filepath.Join(directory, "report.json")
			if err := os.WriteFile(reportPath, []byte(edited), 0o600); err != nil {
				t.Fatalf("writing the edited report: %v", err)
			}

			result, err := traceaudit.Audit(reportPath, testdata(t, "killable.trace.jsonl"))
			if err != nil {
				t.Fatalf("auditing the edited report: %v", err)
			}
			var layers []string
			for _, violation := range result.Violations() {
				layers = append(layers, violation.Layer)
			}
			if !contains(layers, broken.layer) {
				t.Errorf("the audit reported %v and this edit should be a %q violation", layers, broken.layer)
			}
		})
	}
}

// TestARecordingThatCannotSettleAnythingIsUnauditedRatherThanClean is the third
// answer.
//
// A truncated recording -- a run killed before it finished, a stream a
// collector caught mid-write -- says nothing about anything. Reporting that as
// agreement would make this gate report green for a run it never read, and
// reporting it as a violation would make a gate that fails on interrupted runs,
// which is the kind that gets switched off.
func TestARecordingThatCannotSettleAnythingIsUnauditedRatherThanClean(t *testing.T) {
	t.Parallel()

	stream := read(t, "killable.trace.jsonl")
	lines := strings.Split(strings.TrimRight(stream, "\n"), "\n")
	truncated := strings.Join(lines[:len(lines)/2], "\n") + "\n"

	directory := t.TempDir()
	tracePath := filepath.Join(directory, "trace.jsonl")
	if err := os.WriteFile(tracePath, []byte(truncated), 0o600); err != nil {
		t.Fatalf("writing the truncated recording: %v", err)
	}

	result, err := traceaudit.Audit(testdata(t, "killable.report.json"), tracePath)
	if err != nil {
		t.Fatalf("auditing against a truncated recording: %v", err)
	}
	if len(result.Violations()) != 0 {
		t.Errorf("a truncated recording produced %d violation(s), want only unaudited findings: %v",
			len(result.Violations()), result.Violations())
	}
	if len(result.Unaudited()) == 0 {
		t.Errorf("a truncated recording produced no unaudited findings, so it read as agreement")
	}
}

// audit runs the audit over one committed pair.
func audit(t *testing.T, name string) traceaudit.Result {
	t.Helper()
	result, err := traceaudit.Audit(testdata(t, name+".report.json"), testdata(t, name+".trace.jsonl"))
	if err != nil {
		t.Fatalf("auditing %s: %v", name, err)
	}
	return result
}

// testdata is the path of one committed file.
func testdata(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name)
}

// read is one committed file's contents.
func read(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(testdata(t, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(raw)
}

// contains reports whether a layer was among those reported.
func contains(layers []string, want string) bool {
	for _, layer := range layers {
		if layer == want {
			return true
		}
	}
	return false
}

// TestAuditResolvesARecordingRootByTheReportsRunID lets a caller name the
// directory recordings live in rather than the file inside it.
//
// A run writes its recording to `<root>/<run id>/trace.jsonl`, and the run id
// is minted while the run is happening. A caller that has the report has the id
// — it is a field of the document — but a caller writing a command line ahead
// of time does not, so asking for the file by name means asking somebody to
// interpolate a value that does not exist yet. That is the shape of a task
// nobody wires up, which is how this package went un-run: the command its own
// doc comment gave was wrong, and nothing was calling it to find out.
func TestAuditResolvesARecordingRootByTheReportsRunID(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	run := filepath.Join(root, "20260914T002752Z-1131")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatalf("creating the run directory: %v", err)
	}
	stream, err := os.ReadFile(filepath.Join("testdata", "killable.trace.jsonl"))
	if err != nil {
		t.Fatalf("reading the fixture recording: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(run, "trace.jsonl"), stream, 0o600); writeErr != nil {
		t.Fatalf("writing the recording: %v", writeErr)
	}

	byRoot, err := traceaudit.Audit(filepath.Join("testdata", "killable.report.json"), root)
	if err != nil {
		t.Fatalf("Audit against the recording root: %v", err)
	}
	byFile, err := traceaudit.Audit(filepath.Join("testdata", "killable.report.json"), filepath.Join(run, "trace.jsonl"))
	if err != nil {
		t.Fatalf("Audit against the recording file: %v", err)
	}

	if byRoot.Mutants != byFile.Mutants || byRoot.Audited != byFile.Audited ||
		len(byRoot.Violations()) != len(byFile.Violations()) ||
		len(byRoot.Unaudited()) != len(byFile.Unaudited()) {
		t.Errorf("naming the root and naming the file inside it audited differently:\n"+
			"\troot: %d mutants, %d audited, %d violations, %d unaudited\n"+
			"\tfile: %d mutants, %d audited, %d violations, %d unaudited",
			byRoot.Mutants, byRoot.Audited, len(byRoot.Violations()), len(byRoot.Unaudited()),
			byFile.Mutants, byFile.Audited, len(byFile.Violations()), len(byFile.Unaudited()))
	}
}

// TestAuditSaysWhichRecordingItLookedForWhenARootHoldsNone refuses to read a
// root that has no recording for this report as a root that agrees with it.
func TestAuditSaysWhichRecordingItLookedForWhenARootHoldsNone(t *testing.T) {
	t.Parallel()

	_, err := traceaudit.Audit(filepath.Join("testdata", "killable.report.json"), t.TempDir())
	if err == nil {
		t.Fatal("auditing against an empty recording root succeeded; an absent recording is not an agreement")
	}
	if !strings.Contains(err.Error(), "20260914T002752Z-1131") {
		t.Errorf("the error does not name the run it looked for: %v", err)
	}
}
