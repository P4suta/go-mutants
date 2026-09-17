// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/trace"
)

func explainedJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()

	stdout := explained(t, append([]string{"--json"}, args...)...)
	if err := schemas.Validate(schemas.ExplainV1, []byte(stdout)); err != nil {
		t.Fatalf("`go-mutants explain --json %s` wrote a document %s rejects: %v\n%s",
			strings.Join(args, " "), schemas.ExplainV1, err, stdout)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("decoding the document: %v\n%s", err, stdout)
	}
	return document
}

func at(t *testing.T, document any, path ...any) any {
	t.Helper()

	here := document
	for i, step := range path {
		switch key := step.(type) {
		case string:
			object, ok := here.(map[string]any)
			if !ok {
				t.Fatalf("%v: %v is not an object", path[:i+1], here)
			}
			value, held := object[key]
			if !held {
				t.Fatalf("%v: the document holds no %q", path[:i+1], key)
			}
			here = value
		case int:
			list, ok := here.([]any)
			if !ok {
				t.Fatalf("%v: %v is not a list", path[:i+1], here)
			}
			if key >= len(list) {
				t.Fatalf("%v: the list holds %d entries", path[:i+1], len(list))
			}
			here = list[key]
		default:
			t.Fatalf("%v is not a key or an index", step)
		}
	}
	return here
}

func text(t *testing.T, document any, path ...any) string {
	t.Helper()

	value, ok := at(t, document, path...).(string)
	if !ok {
		t.Fatalf("%v is not a string", path)
	}
	if value == "" {
		t.Fatalf("%v is empty", path)
	}
	return value
}

func TestExplainJSONIsADocumentOfItsOwnType(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	recordEvents(t, workspace, killedRecording(), map[int64]string{
		5: "--- FAIL: TestClamp (0.00s)\n    clamp_test.go:41: Clamp(10, 0, 10) = 10, want 9\nFAIL\n",
	})

	document := explainedJSON(t, killedID[:8])
	if got := at(t, document, "document_type"); got != schemas.ExplainV1 {
		t.Errorf("document_type = %v, want %q", got, schemas.ExplainV1)
	}
	if got := at(t, document, "schema_version"); got != float64(explainSchemaVersion) {
		t.Errorf("schema_version = %v, want %d", got, explainSchemaVersion)
	}
	if got := text(t, document, "tool_version"); got != Version {
		t.Errorf("tool_version = %q, want %q", got, Version)
	}
}

func TestExplainJSONCarriesWhatNeitherSourceHolds(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	recordEvents(t, workspace, killedRecording(), map[int64]string{
		5: "--- FAIL: TestClamp (0.00s)\n    clamp_test.go:41: Clamp(10, 0, 10) = 10, want 9\nFAIL\n",
	})

	document := explainedJSON(t, killedID[:8])

	command := text(t, document, "reproduce", "command")
	for _, want := range []string{"cd ", "/tmp/snapshot", activationVariable + "=" + killedID, "killable.test"} {
		if !strings.Contains(command, want) {
			t.Errorf("reproduce.command does not carry %q:\n%s", want, command)
		}
	}
	if got := at(t, document, "reproduce", "activation", "value"); got != killedID {
		t.Errorf("reproduce.activation.value = %v, want the full identity %q", got, killedID)
	}
	if got := at(t, document, "reproduce", "temporaries_kept"); got != true {
		t.Errorf("reproduce.temporaries_kept = %v, and the recording holds a kept-scratch artifact", got)
	}

	if got := at(t, document, "timeline", 0, "share_ms"); got != float64(20520) {
		t.Errorf("timeline[0].share_ms = %v, want the sum of this mutant's own passes (20520)", got)
	}

	tail, ok := at(t, document, "executions", 1, "commands", 0, "output_tail").([]any)
	if !ok || len(tail) == 0 {
		t.Fatalf("executions[1].commands[0].output_tail is empty; the preserved file was not read")
	}
	if !strings.Contains(tail[len(tail)-1].(string), "FAIL") {
		t.Errorf("the quoted output does not end where a Go failure does: %v", tail)
	}
}

func TestExplainJSONNamesTheDocumentsItWasDerivedFrom(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	streamDir := recordEvents(t, workspace, killedRecording(), nil)

	document := explainedJSON(t, killedID[:8])
	if got := text(t, document, "source", "report", "path"); got != "mutation.json" {
		t.Errorf("source.report.path = %q, want the file that was read", got)
	}
	if got := text(t, document, "source", "report", "run_id"); got != explainRunID {
		t.Errorf("source.report.run_id = %q, want %q", got, explainRunID)
	}
	if got := text(t, document, "source", "trace", "directory"); got != streamDir {
		t.Errorf("source.trace.directory = %q, want %q", got, streamDir)
	}
	if got := at(t, document, "source", "trace", "describes_the_report"); got != true {
		t.Errorf("source.trace.describes_the_report = %v for the run's own recording", got)
	}
	if warnings := at(t, document, "source", "warnings").([]any); len(warnings) != 0 {
		t.Errorf("source.warnings = %v, and there is nothing wrong with these sources", warnings)
	}
}

func TestExplainJSONStatesAbsenceRatherThanOmittingIt(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	document := explainedJSON(t, killedID[:8])
	if got := at(t, document, "source", "trace"); got != nil {
		t.Errorf("source.trace = %v for a run that recorded nothing", got)
	}
	if stages := at(t, document, "timeline").([]any); len(stages) != 0 {
		t.Errorf("timeline = %v for a run that recorded nothing", stages)
	}
	if got := at(t, document, "reproduce", "available"); got != false {
		t.Errorf("reproduce.available = %v with no recording to take an argument vector from", got)
	}
	if got := text(t, document, "reproduce", "unavailable_reason"); !strings.Contains(got, "no recording") {
		t.Errorf("reproduce.unavailable_reason = %q, want the reason there is none", got)
	}
	if got := at(t, document, "reproduce", "temporaries_kept"); got != nil {
		t.Errorf("reproduce.temporaries_kept = %v with no recording to say either way", got)
	}
	if got := text(t, document, "reproduce", "suggestion"); !strings.Contains(got, "--trace") {
		t.Errorf("reproduce.suggestion = %q, want the invocation that would record one", got)
	}
}

func TestExplainJSONWritesAnEmptyListWhereAListBelongs(t *testing.T) {
	for _, target := range []struct {
		what     string
		id       string
		recorded bool
	}{
		{"a mutant nothing covered", uncoveredID[:8], false},
		{"a mutant the compiler refused", rejectedID[:8], false},
		{"a mutant adopted from the cache", cachedID[:8], false},
		{"a mutant with passes and no recording", survivorID[:8], false},
		{"a mutant with passes and a recording", killedID[:8], true},
	} {
		t.Run(target.what, func(t *testing.T) {
			workspace := inExplainWorkspace(t, explainReport())
			if target.recorded {
				recordEvents(t, workspace, killedRecording(), map[int64]string{5: "FAIL\n"})
			}
			document := explainedJSON(t, target.id)
			walked := listsIn(document, nil)
			for path, value := range walked {
				if value == nil {
					t.Errorf("%s is null; a list a consumer may not iterate is a list with a branch in front of it", path)
				}
			}
		})
	}
}

func TestTheEmptyListWalkReachesEveryListTheDocumentHas(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	recordEvents(t, workspace, killedRecording(), map[int64]string{5: "FAIL\n"})
	reached := map[string]bool{}
	for path := range listsIn(explainedJSON(t, killedID[:8]), nil) {
		reached[lastKeyOf(path)] = true
	}

	r := explainReport()
	sites := []discover.SkipSite{{Path: "clamp.go", Reason: discover.SkipConstDecl, Line: 41, Column: 9}}
	mutants := []catalogMutant{{
		ID: killedID, DisplayID: displayOf(killedID), Path: "clamp.go", Line: 41, Column: 7,
		Family: "comparison", Rule: "lt-to-le", Original: "v < hi", Replacement: "v <= hi",
	}}
	for path := range listsIn(marshalled(t, gatherPosition(r, "mutation.json",
		position{path: "clamp.go", line: 41}, sites, mutants, outcomesOf(r))), nil) {
		reached[lastKeyOf(path)] = true
	}

	for key := range listFields {
		if !reached[key] {
			t.Errorf("the walk never reached %q, so nothing checks whether it can be null", key)
		}
	}
}

func lastKeyOf(path string) string {
	if at := strings.LastIndex(path, "."); at >= 0 {
		return path[at+1:]
	}
	return path
}

func TestGatheringAnAccountWithNothingInItStillWritesEveryList(t *testing.T) {
	r := explainReport()
	bare := report.Mutant{
		ID: killedID, DisplayID: displayOf(killedID),
		Path: "clamp.go", Package: "example.com/killable",
		Family: "comparison", Rule: "lt-to-le", Line: 41, Column: 7,
		Original: "v < hi", Replacement: "v <= hi",
		Outcome: report.OutcomeSurvived, Attempts: 1,
		Executions: []report.Execution{
			{Attempt: 1, Worker: 0, Outcome: report.OutcomeSurvived, DurationMS: 1},
		},
	}
	document := marshalled(t, gatherAccount(r, "mutation.json", subject{mutant: &bare}, nil))

	reached := 0
	for path, value := range listsIn(document, nil) {
		if value == nil {
			t.Errorf("%s is null for an account with nothing in it", path)
		}
		reached++
	}
	for _, path := range [][]any{
		{"executions", 0, "binaries"},
		{"executions", 0, "commands"},
		{"coverage", "packages"},
		{"coverage", "tests"},
	} {
		if list, ok := at(t, document, path...).([]any); !ok || len(list) != 0 {
			t.Errorf("%v = %v, want the empty list", path, list)
		}
	}
	if reached == 0 {
		t.Fatal("the walk found no lists at all")
	}
}

func listsIn(document any, prefix []string) map[string]any {
	found := map[string]any{}
	object, ok := document.(map[string]any)
	if !ok {
		return found
	}
	for key, value := range object {
		path := append(append([]string{}, prefix...), key)
		switch typed := value.(type) {
		case []any:
			found[strings.Join(path, ".")] = typed
			for index, item := range typed {
				for nested, nestedValue := range listsIn(item, append(path, "["+strconv.Itoa(index)+"]")) {
					found[nested] = nestedValue
				}
			}
		case map[string]any:
			for nested, nestedValue := range listsIn(typed, path) {
				found[nested] = nestedValue
			}
		case nil:
			if listFields[key] {
				found[strings.Join(path, ".")] = nil
			}
		}
	}
	return found
}

var listFields = map[string]bool{
	"warnings": true, "packages": true, "tests": true, "executions": true,
	"binaries": true, "commands": true, "argv": true, "output_tail": true,
	"timeline": true, "test_command": true, "skip_sites": true, "mutants": true,
}

func TestExplainJSONOfARejectionCarriesTheCompilersWords(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	document := explainedJSON(t, rejectedID[:8])
	if got := at(t, document, "subject", "rejected"); got != true {
		t.Errorf("subject.rejected = %v for a mutant in rejected[]", got)
	}
	if got := at(t, document, "verdict", "outcome"); got != "rejected" {
		t.Errorf("verdict.outcome = %v, want %q", got, "rejected")
	}
	if got := text(t, document, "verdict", "diagnostic"); !strings.Contains(got, "cannot") {
		t.Errorf("verdict.diagnostic = %q, want the compiler's own words", got)
	}
	for _, field := range []string{"family", "original", "replacement", "package"} {
		if got := at(t, document, "subject", field); got != nil {
			t.Errorf("subject.%s = %v, and a rejection was never built", field, got)
		}
	}
	if got := text(t, document, "reproduce", "suggestion"); !strings.Contains(got, "--explain") {
		t.Errorf("reproduce.suggestion = %q, want the way to make the compiler say it again", got)
	}
}

func TestExplainJSONOfAPositionIsTheSameDocumentType(t *testing.T) {
	r := explainReport()
	sites := []discover.SkipSite{
		{Path: "clamp.go", Reason: discover.SkipConstDecl, Line: 41, Column: 9},
		{Path: "gen.go", Reason: discover.SkipGenerated},
	}
	mutants := []catalogMutant{{
		ID: killedID, DisplayID: displayOf(killedID), Path: "clamp.go", Line: 41, Column: 7,
		Family: "comparison", Rule: "lt-to-le", Original: "v < hi", Replacement: "v <= hi",
	}}

	document := marshalled(t, gatherPosition(r, "mutation.json",
		position{path: "clamp.go", line: 41}, sites, mutants, outcomesOf(r)))

	if got := at(t, document, "subject", "kind"); got != "position" {
		t.Errorf("subject.kind = %v, want %q", got, "position")
	}
	if got := at(t, document, "subject", "line"); got != float64(41) {
		t.Errorf("subject.line = %v, want 41", got)
	}
	if got := at(t, document, "skip_sites", 0, "reason"); got != string(discover.SkipConstDecl) {
		t.Errorf("skip_sites[0].reason = %v, want %q", got, discover.SkipConstDecl)
	}
	if got := at(t, document, "mutants", 0, "outcome"); got != "killed" {
		t.Errorf("mutants[0].outcome = %v, want what the report says became of it", got)
	}
	if sites := at(t, document, "skip_sites").([]any); len(sites) != 1 {
		t.Errorf("skip_sites = %v, want only the site at the position asked about", sites)
	}
}

func TestExplainJSONOfAPositionWithNoReportSaysEveryOutcomeIsUnknown(t *testing.T) {
	mutants := []catalogMutant{{
		ID: killedID, DisplayID: displayOf(killedID), Path: "clamp.go", Line: 41, Column: 7,
		Family: "comparison", Rule: "lt-to-le", Original: "v < hi", Replacement: "v <= hi",
	}}

	document := marshalled(t, gatherPosition(nil, "",
		position{path: "clamp.go", line: 41}, nil, mutants, nil))

	if got := at(t, document, "source", "report"); got != nil {
		t.Errorf("source.report = %v with no run stored", got)
	}
	if got := at(t, document, "mutants", 0, "outcome"); got != nil {
		t.Errorf("mutants[0].outcome = %v with no report to say", got)
	}
}

func marshalled(t *testing.T, document any) map[string]any {
	t.Helper()

	var out strings.Builder
	if err := writeExplainJSON(&out, document); err != nil {
		t.Fatalf("encoding the document: %v", err)
	}
	if err := schemas.Validate(schemas.ExplainV1, []byte(out.String())); err != nil {
		t.Fatalf("the document does not satisfy %s: %v\n%s", schemas.ExplainV1, err, out.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out.String()), &decoded); err != nil {
		t.Fatalf("decoding the document: %v\n%s", err, out.String())
	}
	return decoded
}

func TestTheExplanationSchemaNamesTheFieldThatIsWrong(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	recordEvents(t, workspace, killedRecording(), nil)
	document := explainedJSON(t, killedID[:8])

	for _, broken := range []struct {
		what  string
		spoil func(map[string]any)
		want  string
	}{
		{"a list that became null", func(d map[string]any) {
			d["executions"].([]any)[0].(map[string]any)["binaries"] = nil
		}, "/executions/0/binaries"},
		{"a string that became a number", func(d map[string]any) {
			d["source"].(map[string]any)["report"].(map[string]any)["run_id"] = 7
		}, "/source/report/run_id"},
		{"an outcome nothing produces", func(d map[string]any) {
			d["verdict"].(map[string]any)["outcome"] = "vanquished"
		}, "/verdict/outcome"},
		{"a subject field of the wrong type", func(d map[string]any) {
			d["subject"].(map[string]any)["column"] = "seven"
		}, "/subject/column"},
		{"a nullable object with a bad field in it", func(d map[string]any) {
			d["verdict"].(map[string]any)["memory"].(map[string]any)["exceeded"] = "yes"
		}, "/verdict/memory/exceeded"},
	} {
		t.Run(broken.what, func(t *testing.T) {
			copied := reparse(t, document)
			broken.spoil(copied)
			encoded, err := json.Marshal(copied)
			if err != nil {
				t.Fatalf("re-encoding: %v", err)
			}
			err = schemas.Validate(schemas.ExplainV1, encoded)
			var coded *schemas.Error
			if !errors.As(err, &coded) {
				t.Fatalf("validating a broken document gave %v, want a schema failure", err)
			}
			if coded.Pointer != broken.want {
				t.Errorf("the failure points at %q; the field that is wrong is %q, and a reader who is "+
					"sent anywhere else has to find it themselves: %v", coded.Pointer, broken.want, coded)
			}
		})
	}
}

func TestTheExplanationSchemaRefusesADocumentOfBothForms(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	recordEvents(t, workspace, killedRecording(), nil)
	mixed := reparse(t, explainedJSON(t, killedID[:8]))
	mixed["skip_sites"] = []any{}
	mixed["mutants"] = []any{}

	encoded, err := json.Marshal(mixed)
	if err != nil {
		t.Fatalf("re-encoding: %v", err)
	}
	if err := schemas.Validate(schemas.ExplainV1, encoded); err == nil {
		t.Error("a document holding a mutant's verdict and a position's listings was accepted")
	}
}

func reparse(t *testing.T, document map[string]any) map[string]any {
	t.Helper()

	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	var copied map[string]any
	if err := json.Unmarshal(encoded, &copied); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return copied
}

func TestExplainJSONAndTheProseAreOneAccount(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	recordEvents(t, workspace, killedRecording(), map[int64]string{
		5: "--- FAIL: TestClamp (0.00s)\nFAIL\n",
	})

	document := explainedJSON(t, killedID[:8])
	prose := explained(t, killedID[:8])

	for _, path := range [][]any{
		{"subject", "id"},
		{"subject", "rule"},
		{"verdict", "summary"},
		{"verdict", "killed_by"},
		{"coverage", "summary"},
		{"reproduce", "command"},
		{"reproduce", "note"},
		{"executions", 0, "binaries", 0},
		{"executions", 1, "commands", 0, "dir"},
	} {
		want := text(t, document, path...)
		if !strings.Contains(prose, want) {
			t.Errorf("the document says %v is %q and the prose does not print it:\n%s", path, want, prose)
		}
	}
}

func TestExplainJSONWarnsAboutAForeignRecording(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	dir := recordEvents(t, workspace, foreignRecording(), nil)

	document := explainedJSON(t, "--trace", dir, killedID[:8])
	if got := at(t, document, "source", "trace", "describes_the_report"); got != false {
		t.Errorf("source.trace.describes_the_report = %v for a recording of another run", got)
	}
	if got := text(t, document, "source", "trace", "run_id"); got != "20260101T000000Z-9999" {
		t.Errorf("source.trace.run_id = %q, want the run the recording says it is of", got)
	}
	warnings, ok := at(t, document, "source", "warnings").([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("source.warnings = %v, want the one sentence the prose prints", warnings)
	}
	if !strings.Contains(warnings[0].(string), "not "+explainRunID) {
		t.Errorf("source.warnings[0] = %q, want it to name both runs", warnings[0])
	}
}

func TestExplainJSONKeepsTheStreamADocumentWhenAPrefixIsAmbiguous(t *testing.T) {
	inExplainWorkspace(t, explainReport())

	code, stdout, stderr := explain(t, "--json", "abcd")
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("standard output is not empty, so the --json stream is not a document:\n%s", stdout)
	}
	for _, want := range []string{displayOf(firstTwinID), displayOf(otherTwinID), string(CodeMutantUnresolved)} {
		if !strings.Contains(stderr, want) {
			t.Errorf("standard error does not carry %q, so the matches are nowhere:\n%s", want, stderr)
		}
	}
}

func TestExplainJSONReadsAPreservedOutputItCannotOpen(t *testing.T) {
	workspace := inExplainWorkspace(t, explainReport())
	dir := recordEvents(t, workspace, killedRecording(), map[int64]string{5: "gone\n"})
	if err := os.Remove(filepath.Join(dir, trace.OutputDirectoryName, "5.txt")); err != nil {
		t.Fatalf("removing the preserved output: %v", err)
	}

	document := explainedJSON(t, killedID[:8])
	if got := text(t, document, "executions", 1, "commands", 0, "output_error"); !strings.Contains(got, "could not be read") {
		t.Errorf("output_error = %q, want the reason the file could not be opened", got)
	}
	if tail := at(t, document, "executions", 1, "commands", 0, "output_tail").([]any); len(tail) != 0 {
		t.Errorf("output_tail = %v for a file that could not be read", tail)
	}
}

func TestExplainJSONSaysWhichBudgetSettledAMemoryKill(t *testing.T) {
	r := explainReport()
	for i := range r.Mutants {
		if r.Mutants[i].ID != killedID {
			continue
		}
		r.Mutants[i].MemoryExceeded = true
		r.Mutants[i].PeakMemoryBytes = 2 << 30
	}
	inExplainWorkspace(t, r)

	document := explainedJSON(t, killedID[:8])
	if got := at(t, document, "verdict", "memory", "exceeded"); got != true {
		t.Errorf("verdict.memory.exceeded = %v for a mutant the bound stopped", got)
	}
	if got := at(t, document, "verdict", "memory", "peak_bytes"); got != float64(2<<30) {
		t.Errorf("verdict.memory.peak_bytes = %v, want what the mutant held", got)
	}
	if got := at(t, document, "verdict", "memory", "bound_bytes"); got != float64(1<<30) {
		t.Errorf("verdict.memory.bound_bytes = %v, want what it was measured against", got)
	}
	if got := at(t, document, "verdict", "outcome"); got != string(report.OutcomeKilled) {
		t.Errorf("verdict.outcome = %v, want the outcome the report carries", got)
	}
}
