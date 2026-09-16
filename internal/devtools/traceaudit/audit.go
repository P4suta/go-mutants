// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package traceaudit re-derives a run's conclusions from the recording beside
// it, with code that never calls the engine's.
//
// # Why this is not a second implementation
//
// A run publishes two documents about itself. The run report is the claim: what
// every mutant is, what happened to it, and what the policy made of the whole.
// The recording is the account: every phase, every child process, every
// execution of every mutant, in order. They are written by the same run and
// they are meant to describe the same thing.
//
// Nothing checked that they did. A bug in the reporting layer -- a tally
// computed from the wrong slice, a cached outcome filed under the wrong id, an
// uncovered mutant that was in fact executed -- would produce a report that is
// internally consistent, validates against its schema, and is wrong. The
// account beside it would say so, and no reader would ever compare them.
//
// So this package reads both and asks whether they agree, and it does it
// **without importing anything the engine uses to produce either**. The shapes
// below are declared here rather than reused from internal/report, which is the
// whole point: a re-derivation that shared the code would agree for the same
// reasons rather than for independent ones.
//
// # A trace is still not evidence
//
// ADR 0001 says a recording takes no part in a verdict, in an identity, or in a
// cache key, and this does not change that. The question asked here is not
// "which of the two is right" -- it is "do they agree", and a disagreement is a
// bug in go-mutants rather than a verdict about anybody's code.
//
// # Fail-closed means three answers and not two
//
// A check has three outcomes, not two. It can find the two documents agreeing,
// find them disagreeing, or find that the recording cannot settle the question
// -- a mutant it holds no events for, a stream that dropped events, a run that
// was interrupted before it finished. The third is reported as `unaudited` and
// counted separately, because turning "I cannot check this" into "this is fine"
// and turning it into "this is broken" are both wrong, and the second is the
// one that makes a gate get switched off.
package traceaudit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/P4suta/go-mutants/trace"
)

// A Finding is one thing the audit noticed.
type Finding struct {
	// Layer names the question that was being asked.
	Layer string
	// Subject is what it was asked about: a mutant id, or "the run".
	Subject string
	// Detail says what did not add up.
	Detail string
	// Unaudited is true when the recording could not settle the question,
	// rather than settling it the wrong way.
	Unaudited bool
}

// String renders one finding as a line.
func (f Finding) String() string {
	kind := "violation"
	if f.Unaudited {
		kind = "unaudited"
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s", kind, f.Layer, f.Subject, f.Detail)
}

// A Result is what one audit found.
type Result struct {
	Findings []Finding
	// Audited is how many mutants the recording could settle.
	Audited int
	// Mutants is how many the report holds.
	Mutants int
}

// Violations are the findings that are disagreements.
func (r Result) Violations() []Finding {
	var out []Finding
	for _, finding := range r.Findings {
		if !finding.Unaudited {
			out = append(out, finding)
		}
	}
	return out
}

// Unaudited are the findings that are questions the recording could not settle.
func (r Result) Unaudited() []Finding {
	var out []Finding
	for _, finding := range r.Findings {
		if finding.Unaudited {
			out = append(out, finding)
		}
	}
	return out
}

// report is the half of a run report this audit reads.
//
// Declared here rather than imported. A re-derivation that decoded the report
// with the engine's own types would inherit the engine's understanding of them,
// which is the thing being checked.
type report struct {
	DocumentType string `json:"document_type"`
	RunID        string `json:"run_id"`
	Status       string `json:"status"`
	Summary      struct {
		Total        int `json:"total"`
		Killed       int `json:"killed"`
		Survived     int `json:"survived"`
		TimedOut     int `json:"timed_out"`
		Inconclusive int `json:"inconclusive"`
		Errored      int `json:"errored"`
		NotRun       int `json:"not_run"`
	} `json:"summary"`
	Mutants []struct {
		ID         string `json:"id"`
		Outcome    string `json:"outcome"`
		Cached     bool   `json:"cached"`
		Uncovered  bool   `json:"uncovered"`
		Attempts   int    `json:"attempts"`
		Executions []struct {
			Attempt int    `json:"attempt"`
			Outcome string `json:"outcome"`
		} `json:"executions"`
	} `json:"mutants"`
}

// recording is the half of a stream this audit reads.
type event struct {
	Seq    int64  `json:"seq"`
	Type   string `json:"type"`
	Mutant *struct {
		ID      string `json:"id"`
		Attempt int    `json:"attempt"`
		Outcome string `json:"outcome"`
	} `json:"mutant,omitempty"`
	Cache *struct {
		Op       string `json:"op"`
		MutantID string `json:"mutant_id"`
		Result   string `json:"result"`
	} `json:"cache,omitempty"`
	Run *struct {
		Verdict string `json:"verdict"`
	} `json:"run,omitempty"`
}

// recordingFor is the recording to read, given either the file itself or the
// directory recordings are collected in.
//
// A directory holding no run of this name is an error and not an empty
// recording. The two are the same number of findings and opposite statements:
// one says the run made no events worth auditing, and the other says nobody
// looked at the run at all.
func recordingFor(tracePath, runID string) (string, error) {
	info, err := os.Stat(tracePath)
	if err != nil {
		return "", fmt.Errorf("reading the recording: %w", err)
	}
	if !info.IsDir() {
		return tracePath, nil
	}

	recording := filepath.Join(tracePath, runID, trace.FileName)
	if _, statErr := os.Stat(recording); statErr != nil {
		return "", fmt.Errorf("%s holds no recording for run %s: %w", tracePath, runID, statErr)
	}
	return recording, nil
}

// sameOutcome reports whether a report's outcome and a recording's are the same
// outcome, across the spelling the two documents use.
//
// They differ on purpose and both spellings are frozen: docs/library.md says so
// under "The outcome vocabulary is not the report's" -- the live API is
// snake_case (`timed_out`, `not_run`) and the published run report is kebab-case
// (`timed-out`, `not-run`). A recording carries the API's spelling because it is
// written by the engine; a report carries the published one.
//
// Comparing them as strings made every timed-out mutant a disagreement, which is
// what this audit found the first time anything ran it: three violations on a
// run whose report and recording agreed about everything. The rule the two
// documents share is that a hyphen and an underscore separate the same words, so
// that is the comparison, rather than a table of pairs that would have to be
// maintained beside the vocabularies it joins.
func sameOutcome(reported, recorded string) bool {
	return strings.ReplaceAll(reported, "-", "_") == strings.ReplaceAll(recorded, "-", "_")
}

// Audit reads a report and a recording and says whether they agree.
//
// tracePath is either the recording itself or the directory recordings are
// collected in, in which case the one this report describes is the run
// directory named after its run id. Naming the directory is what a task or a
// workflow step can write down: the run id is minted while the run happens, so
// a command line spelled ahead of time cannot contain it.
func Audit(reportPath, tracePath string) (Result, error) {
	var claim report
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return Result{}, fmt.Errorf("reading the report: %w", err)
	}
	if decodeErr := json.Unmarshal(raw, &claim); decodeErr != nil {
		return Result{}, fmt.Errorf("decoding the report: %w", decodeErr)
	}
	if claim.DocumentType != "go-mutants/run-report" {
		return Result{}, fmt.Errorf("%s is a %q, not a run report", reportPath, claim.DocumentType)
	}

	recording, err := recordingFor(tracePath, claim.RunID)
	if err != nil {
		return Result{}, err
	}
	stream, err := os.ReadFile(recording)
	if err != nil {
		return Result{}, fmt.Errorf("reading the recording: %w", err)
	}
	events, lossy, err := decodeStream(string(stream))
	if err != nil {
		return Result{}, err
	}

	result := Result{Mutants: len(claim.Mutants)}
	if lossy != "" {
		result.Findings = append(result.Findings, Finding{
			Layer: "stream", Subject: "the run", Unaudited: true,
			Detail: lossy + ", so nothing below it can be settled",
		})
		return result, nil
	}
	result.Findings = append(result.Findings, auditExecutions(claim, events, &result)...)
	result.Findings = append(result.Findings, auditTally(claim, events)...)
	result.Findings = append(result.Findings, auditNothingUnknown(claim, events)...)
	sort.Slice(result.Findings, func(i, j int) bool {
		if result.Findings[i].Layer != result.Findings[j].Layer {
			return result.Findings[i].Layer < result.Findings[j].Layer
		}
		return result.Findings[i].Subject < result.Findings[j].Subject
	})
	return result, nil
}

// decodeStream reads the JSON Lines and says whether the recording is one a
// question can be asked of at all.
func decodeStream(stream string) (events []event, lossy string, err error) {
	var last int64
	var sawEnd bool
	for number, line := range strings.Split(stream, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e event
		if decodeErr := json.Unmarshal([]byte(line), &e); decodeErr != nil {
			return nil, "", fmt.Errorf("line %d of the recording is not an event: %w", number+1, decodeErr)
		}
		if last != 0 && e.Seq != last+1 {
			return nil, fmt.Sprintf("the recording jumps from sequence %d to %d", last, e.Seq), nil
		}
		last = e.Seq
		if e.Type == "run-end" {
			sawEnd = true
		}
		events = append(events, e)
	}
	if len(events) == 0 {
		return nil, "the recording holds no events", nil
	}
	if !sawEnd {
		return events, "the recording has no run-end, so the run it describes did not finish", nil
	}
	return events, "", nil
}

// auditExecutions asks, per mutant, whether the report's account of how it was
// measured matches the events.
//
// Three claims, and each is one the report could get wrong on its own:
// a mutant said to be uncovered was never executed; a mutant said to be
// answered from the cache was never executed and has a cache hit; and a mutant
// said to have been executed N times has N executions in the stream.
func auditExecutions(claim report, events []event, result *Result) []Finding {
	execs := map[string]int{}
	hits := map[string]bool{}
	for _, e := range events {
		switch {
		case e.Type == "mutant-exec" && e.Mutant != nil:
			execs[e.Mutant.ID]++
		case e.Type == "cache" && e.Cache != nil && e.Cache.MutantID != "":
			if e.Cache.Result == "hit" {
				hits[e.Cache.MutantID] = true
			}
		}
	}

	var findings []Finding
	for _, mutant := range claim.Mutants {
		ran := execs[mutant.ID]
		switch {
		case mutant.Uncovered:
			result.Audited++
			if ran != 0 {
				findings = append(findings, Finding{
					Layer: "uncovered", Subject: mutant.ID,
					Detail: fmt.Sprintf("the report says no test binary reaches it and the recording ran it %d time(s)", ran),
				})
			}
		case mutant.Cached:
			result.Audited++
			if ran != 0 {
				findings = append(findings, Finding{
					Layer: "cached", Subject: mutant.ID,
					Detail: fmt.Sprintf("the report says it was answered from the cache and the recording ran it %d time(s)", ran),
				})
			}
			if !hits[mutant.ID] {
				findings = append(findings, Finding{
					Layer: "cached", Subject: mutant.ID, Unaudited: true,
					Detail: "the report says it was answered from the cache and the recording holds no hit for it",
				})
			}
		case len(mutant.Executions) == 0:
			// Neither executed nor explained by a flag the report carries.
			// That is a `not-run` mutant, whose reason is the report's own and
			// is nothing the recording restates.
			findings = append(findings, Finding{
				Layer: "executions", Subject: mutant.ID, Unaudited: true,
				Detail: "the report records no execution and no reason the recording could confirm",
			})
		default:
			result.Audited++
			if ran != len(mutant.Executions) {
				findings = append(findings, Finding{
					Layer: "executions", Subject: mutant.ID,
					Detail: fmt.Sprintf("the report records %d execution(s) and the recording holds %d",
						len(mutant.Executions), ran),
				})
			}
		}
	}
	return findings
}

// auditTally recomputes the summary from the events.
//
// The last attempt of a mutant is its verdict, which is the rule the engine
// applies and is restated here rather than borrowed. A mutant the recording
// never ran contributes nothing, which is why the counts are compared only for
// the outcomes the stream can see.
func auditTally(claim report, events []event) []Finding {
	last := map[string]string{}
	attempt := map[string]int{}
	for _, e := range events {
		if e.Type != "mutant-exec" || e.Mutant == nil || e.Mutant.Outcome == "" {
			continue
		}
		if e.Mutant.Attempt >= attempt[e.Mutant.ID] {
			attempt[e.Mutant.ID] = e.Mutant.Attempt
			last[e.Mutant.ID] = e.Mutant.Outcome
		}
	}

	var findings []Finding
	for _, mutant := range claim.Mutants {
		if mutant.Uncovered || mutant.Cached || len(mutant.Executions) == 0 {
			continue
		}
		seen, ok := last[mutant.ID]
		if !ok {
			findings = append(findings, Finding{
				Layer: "verdict", Subject: mutant.ID, Unaudited: true,
				Detail: "the report records executions and the recording holds no outcome for it",
			})
			continue
		}
		if sameOutcome(mutant.Outcome, seen) {
			continue
		}
		findings = append(findings, Finding{
			Layer: "verdict", Subject: mutant.ID,
			Detail: fmt.Sprintf("the report says %q and the recording's last attempt says %q", mutant.Outcome, seen),
		})
	}
	if got, want := len(claim.Mutants), claim.Summary.Total; got != want {
		findings = append(findings, Finding{
			Layer: "summary", Subject: "the run",
			Detail: fmt.Sprintf("the summary says %d mutants and the document holds %d", want, got),
		})
	}
	return findings
}

// auditNothingUnknown asks the question from the other end: does the recording
// name a mutant the report does not catalogue?
//
// A report that dropped a row would pass every check above, because every check
// above starts from the report.
func auditNothingUnknown(claim report, events []event) []Finding {
	catalogued := make(map[string]bool, len(claim.Mutants))
	for _, mutant := range claim.Mutants {
		catalogued[mutant.ID] = true
	}
	var strays []string
	for _, e := range events {
		if e.Type == "mutant-exec" && e.Mutant != nil && !catalogued[e.Mutant.ID] {
			strays = append(strays, e.Mutant.ID)
		}
	}
	slices.Sort(strays)
	strays = slices.Compact(strays)

	findings := make([]Finding, 0, len(strays))
	for _, id := range strays {
		findings = append(findings, Finding{
			Layer: "catalogue", Subject: id,
			Detail: "the recording ran it and the report does not catalogue it",
		})
	}
	return findings
}
