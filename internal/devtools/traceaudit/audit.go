// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

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

type Finding struct {
	Layer     string
	Subject   string
	Detail    string
	Unaudited bool
}

func (f Finding) String() string {
	kind := "violation"
	if f.Unaudited {
		kind = "unaudited"
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s", kind, f.Layer, f.Subject, f.Detail)
}

type Result struct {
	Findings []Finding
	Audited  int
	Mutants  int
}

func (r Result) Violations() []Finding {
	var out []Finding
	for _, finding := range r.Findings {
		if !finding.Unaudited {
			out = append(out, finding)
		}
	}
	return out
}

func (r Result) Unaudited() []Finding {
	var out []Finding
	for _, finding := range r.Findings {
		if finding.Unaudited {
			out = append(out, finding)
		}
	}
	return out
}

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

func sameOutcome(reported, recorded string) bool {
	return strings.ReplaceAll(reported, "-", "_") == strings.ReplaceAll(recorded, "-", "_")
}

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
