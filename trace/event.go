// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import "slices"

type Event struct {
	Seq int64 `json:"seq"`

	Type string `json:"type"`

	Schema string `json:"schema,omitempty"`

	Timestamp string `json:"timestamp"`

	ElapsedMS int64 `json:"elapsed_ms"`

	Start    *StartRecord    `json:"start,omitempty"`
	Phase    *PhaseRecord    `json:"phase,omitempty"`
	Stage    *StageRecord    `json:"stage,omitempty"`
	Prepare  *PrepareRecord  `json:"prepare,omitempty"`
	Exec     *ExecRecord     `json:"exec,omitempty"`
	Mutant   *MutantRecord   `json:"mutant,omitempty"`
	Probe    *ProbeRecord    `json:"probe,omitempty"`
	Validate *ValidateRecord `json:"validate,omitempty"`
	Coverage *CoverageRecord `json:"coverage,omitempty"`
	Cache    *CacheRecord    `json:"cache,omitempty"`
	Snapshot *SnapshotRecord `json:"snapshot,omitempty"`
	Sweep    *SweepRecord    `json:"sweep,omitempty"`
	Artifact *ArtifactRecord `json:"artifact,omitempty"`
	Note     *NoteRecord     `json:"note,omitempty"`
	Run      *RunRecord      `json:"run,omitempty"`
}

type StartRecord struct {
	Kind string `json:"kind"`

	RunID string `json:"run_id,omitempty"`

	ToolVersion string `json:"tool_version"`

	PID int `json:"pid"`

	Root string `json:"root"`

	Args []string `json:"args,omitempty"`
}

type PhaseRecord struct {
	Name string `json:"name"`

	DurationMS *int64 `json:"duration_ms,omitempty"`
}

type StageRecord struct {
	Phase string `json:"phase,omitempty"`
	Name  string `json:"name"`

	State string `json:"state"`

	Result string `json:"result,omitempty"`

	DurationMS *int64 `json:"duration_ms,omitempty"`

	Detail string `json:"detail,omitempty"`
}

type PrepareRecord struct {
	Phase      string `json:"phase"`
	State      string `json:"state"`
	Result     string `json:"result,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
}

type ExecRecord struct {
	Kind string `json:"kind"`

	Subject string `json:"subject,omitempty"`

	Argv []string `json:"argv"`

	Dir string `json:"dir,omitempty"`

	EnvNames []string `json:"env_names,omitempty"`

	TimeoutMS int64 `json:"timeout_ms,omitempty"`
	ExitCode  int   `json:"exit_code"`
	TimedOut  bool  `json:"timed_out,omitempty"`

	DurationMS int64 `json:"duration_ms,omitempty"`

	PeakMemoryBytes int64 `json:"peak_memory_bytes,omitempty"`

	OutputBytes  int    `json:"output_bytes,omitempty"`
	OutputSHA256 string `json:"output_sha256,omitempty"`

	OutputTruncated bool   `json:"output_truncated,omitempty"`
	OutputPath      string `json:"output_path,omitempty"`

	Error string `json:"error,omitempty"`

	Output []byte `json:"-"`
}

type MutantRecord struct {
	ID        string `json:"id"`
	DisplayID string `json:"display_id,omitempty"`

	Attempt int `json:"attempt"`

	Worker int `json:"worker"`

	Package string `json:"package,omitempty"`

	Binaries []string `json:"binaries,omitempty"`

	Tests []string `json:"tests,omitempty"`

	Args      []string `json:"args,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`

	Outcome string `json:"outcome,omitempty"`

	KilledBy string `json:"killed_by,omitempty"`

	DurationMS int64 `json:"duration_ms,omitempty"`

	MemoryExceeded  bool  `json:"memory_exceeded,omitempty"`
	PeakMemoryBytes int64 `json:"peak_memory_bytes,omitempty"`

	ExecSeqs []int64 `json:"exec_seqs,omitempty"`

	OutputTail string `json:"output_tail,omitempty"`

	Error string `json:"error,omitempty"`
}

type ProbeRecord struct {
	Package string `json:"package,omitempty"`

	Binaries []string `json:"binaries,omitempty"`

	Args      []string `json:"args,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`

	Outcome    string `json:"outcome,omitempty"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms,omitempty"`

	Infected []string `json:"infected,omitzero"`

	ExecSeqs []int64 `json:"exec_seqs,omitempty"`
	Error    string  `json:"error,omitempty"`
}

type ValidateRecord struct {
	Tree string `json:"tree"`

	Op string `json:"op"`

	Build int `json:"build,omitempty"`

	Failed bool `json:"failed,omitempty"`

	Blamed []string `json:"blamed,omitempty"`

	Pending int `json:"pending,omitempty"`

	Path string `json:"path,omitempty"`

	Candidates int `json:"candidates,omitempty"`
	Accepted   int `json:"accepted,omitempty"`

	MutantID   string `json:"mutant_id,omitempty"`
	Diagnostic string `json:"diagnostic,omitempty"`

	Builds   int `json:"builds,omitempty"`
	Rejected int `json:"rejected,omitempty"`

	ExecSeq int64 `json:"exec_seq,omitempty"`
}

type CoverageRecord struct {
	MutantID string `json:"mutant_id"`
	Path     string `json:"path"`

	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`

	Covering []string `json:"covering,omitempty"`

	CoveringTests []string `json:"covering_tests,omitempty"`

	Uncovered bool `json:"uncovered,omitempty"`
}

type CacheRecord struct {
	Op       string `json:"op"`
	MutantID string `json:"mutant_id,omitempty"`
	Result   string `json:"result"`

	Outcome string `json:"outcome,omitempty"`

	Directory string `json:"directory,omitempty"`

	ContextKey string `json:"context_key,omitempty"`

	Error string `json:"error,omitempty"`
}

type SnapshotRecord struct {
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
	Dir    string `json:"dir,omitempty"`

	Stable bool `json:"stable,omitempty"`

	Files      int    `json:"files,omitempty"`
	Digest     string `json:"digest,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

type SweepRecord struct {
	Parent       string   `json:"parent"`
	Removed      []string `json:"removed,omitempty"`
	RemovedBytes int64    `json:"removed_bytes,omitempty"`

	Live  int    `json:"live,omitempty"`
	Kept  int    `json:"kept,omitempty"`
	Error string `json:"error,omitempty"`
}

type ArtifactRecord struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type NoteRecord struct {
	Kind   string `json:"kind"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type RunRecord struct {
	Verdict string `json:"verdict,omitempty"`

	ExitCode int `json:"exit_code"`

	Error string `json:"error,omitempty"`

	EventsEmitted int64 `json:"events_emitted"`
	EventsDropped int64 `json:"events_dropped"`
}

func (event Event) Clone() Event {
	if event.Start != nil {
		record := *event.Start
		record.Args = slices.Clone(record.Args)
		event.Start = &record
	}
	if event.Phase != nil {
		record := *event.Phase
		record.DurationMS = cloneDuration(record.DurationMS)
		event.Phase = &record
	}
	if event.Stage != nil {
		record := *event.Stage
		record.DurationMS = cloneDuration(record.DurationMS)
		event.Stage = &record
	}
	if event.Prepare != nil {
		record := *event.Prepare
		record.DurationMS = cloneDuration(record.DurationMS)
		event.Prepare = &record
	}
	if event.Exec != nil {
		record := *event.Exec
		record.Argv = slices.Clone(record.Argv)
		record.EnvNames = slices.Clone(record.EnvNames)
		record.Output = slices.Clone(record.Output)
		event.Exec = &record
	}
	if event.Mutant != nil {
		record := *event.Mutant
		record.Binaries = slices.Clone(record.Binaries)
		record.Args = slices.Clone(record.Args)
		record.ExecSeqs = slices.Clone(record.ExecSeqs)
		event.Mutant = &record
	}
	if event.Probe != nil {
		record := *event.Probe
		record.Binaries = slices.Clone(record.Binaries)
		record.Args = slices.Clone(record.Args)
		record.Infected = slices.Clone(record.Infected)
		record.ExecSeqs = slices.Clone(record.ExecSeqs)
		event.Probe = &record
	}
	if event.Validate != nil {
		record := *event.Validate
		record.Blamed = slices.Clone(record.Blamed)
		event.Validate = &record
	}
	if event.Coverage != nil {
		record := *event.Coverage
		record.Covering = slices.Clone(record.Covering)
		record.CoveringTests = slices.Clone(record.CoveringTests)
		event.Coverage = &record
	}
	if event.Cache != nil {
		record := *event.Cache
		event.Cache = &record
	}
	if event.Snapshot != nil {
		record := *event.Snapshot
		event.Snapshot = &record
	}
	if event.Sweep != nil {
		record := *event.Sweep
		record.Removed = slices.Clone(record.Removed)
		event.Sweep = &record
	}
	if event.Artifact != nil {
		record := *event.Artifact
		event.Artifact = &record
	}
	if event.Note != nil {
		record := *event.Note
		event.Note = &record
	}
	if event.Run != nil {
		record := *event.Run
		event.Run = &record
	}
	return event
}

func cloneDuration(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
