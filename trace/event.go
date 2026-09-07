// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import "slices"

// An Event is one line of a recording: five envelope fields and exactly one
// payload, named after the concept it carries.
//
// The field order below is the order the JSON is written in, and the JSON names
// are the contract — `schema/trace-v1.schema.json` states it, `docs/trace-v1.md`
// explains it, and a golden test pins every byte of it. The `exec`, `prepare`,
// `mutant`, `artifact`, `note` and `run` payloads deliberately share their field
// names with goatest's own trace, so a consumer that records both streams can
// join them on `(argv, dir, output_sha256)` rather than on guesswork.
type Event struct {
	// Seq is the position of the event in the recording, counting from one. It
	// is assigned under the same lock that hands the event to the sink, so the
	// order of the file and the order of Seq are one order however many
	// goroutines record at once.
	Seq int64 `json:"seq"`

	// Type says which event this is, and therefore which payload it carries.
	Type string `json:"type"`

	// Schema is the format identity, carried by the run-start event alone.
	Schema string `json:"schema,omitempty"`

	// Timestamp is when the event was recorded, RFC 3339 in UTC with
	// nanosecond precision.
	Timestamp string `json:"timestamp"`

	// ElapsedMS is the milliseconds between the start of the recording and the
	// event.
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

// StartRecord opens a recording: who is recording, and what they are recording.
type StartRecord struct {
	// Kind is [StartKindRun] for a CLI run or [StartKindWorkspace] for a
	// library workspace.
	Kind string `json:"kind"`

	// RunID is the run identity the report is also named by, so a recording
	// and a report can be paired. A workspace has none.
	RunID string `json:"run_id,omitempty"`

	// ToolVersion is the build that recorded.
	ToolVersion string `json:"tool_version"`

	// PID is the process that recorded. Two go-mutants processes tracing one
	// workspace are told apart by this and by their run directories.
	PID int `json:"pid"`

	// Root is the workspace the recording is about.
	Root string `json:"root"`

	// Args is the command line, verbatim, when a command line is what opened
	// the recording.
	Args []string `json:"args,omitempty"`
}

// PhaseRecord is one phase of a run. A phase is only timed when it ends.
type PhaseRecord struct {
	Name string `json:"name"`

	// DurationMS is present on a phase-end and absent from a phase-start,
	// including when it is zero — which is why it is a pointer, for the reason
	// [StageRecord.DurationMS] is one. The schema pairs the two in both
	// directions.
	DurationMS *int64 `json:"duration_ms,omitempty"`
}

// StageRecord is one step inside a phase.
//
// Stages are where a phase becomes readable: "the mutate phase took ninety
// seconds" says much less than "it spent eighty of them building test
// binaries". Phase is stamped by the recorder from the phase that was open when
// the stage started, so no call site can mislabel one.
type StageRecord struct {
	Phase string `json:"phase,omitempty"`
	Name  string `json:"name"`

	// State is [StateStarted] or [StateFinished].
	State string `json:"state"`

	// Result is what became of the stage. Present only on a finished stage,
	// and only when the caller had one to report.
	Result string `json:"result,omitempty"`

	// DurationMS is present only on a finished stage, including when it is
	// zero — which is why it is a pointer. A stage that took under a
	// millisecond and a stage whose duration was lost are not the same
	// statement.
	DurationMS *int64 `json:"duration_ms,omitempty"`

	// Detail is what the stage was working on, recorded on the started event
	// where it is known. It is free text for a reader, never a field to
	// branch on.
	Detail string `json:"detail,omitempty"`
}

// PrepareRecord is one timed stage of a library mutation session's
// preparation. Its fields are goatest's, exactly.
type PrepareRecord struct {
	Phase      string `json:"phase"`
	State      string `json:"state"`
	Result     string `json:"result,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
}

// ExecRecord is one executed command, recorded after the child was reaped so
// that the execution and its result are one line.
//
// It records the environment's variable *names* and never their values, and it
// never carries the captured output: the bytes are digested into the event and
// preserved beside the stream. Both are properties of the recorder rather than
// of its callers, so no future call site can leak a value or grow an event.
type ExecRecord struct {
	// Kind is what the command was, from [ExecKinds]. The schema enumerates
	// it, so an unlabelled command is a recording that does not validate.
	Kind string `json:"kind"`

	// Subject is what the command was about: a mutant id, an import path, a
	// scope pattern — or empty when the kind says everything there is to say.
	Subject string `json:"subject,omitempty"`

	// Argv is the complete argument vector, exactly as the child received it:
	// Argv[0] is the executable, and the rest are the arguments it was given.
	// It is never null — a command that could not be run is recorded with the
	// vector as it was given, which is the empty one when it had none — so a
	// reader may iterate it without checking it first.
	Argv []string `json:"argv"`

	Dir string `json:"dir,omitempty"`

	// EnvNames is the environment the command could see, as names alone,
	// sorted and deduplicated. A caller may hand [Recorder.Exec] the whole
	// `NAME=VALUE` environment: the recorder reduces it.
	EnvNames []string `json:"env_names,omitempty"`

	TimeoutMS int64 `json:"timeout_ms,omitempty"`
	ExitCode  int   `json:"exit_code"`
	TimedOut  bool  `json:"timed_out,omitempty"`

	DurationMS int64 `json:"duration_ms,omitempty"`

	// PeakMemoryBytes is the highest memory the command's whole process
	// tree was observed to hold, and is absent when the platform could not say.
	//
	// It is what a command cost the machine beside what DurationMS says it cost
	// the clock, and it is recorded for every command rather than only for the
	// ones somebody bounded. The question a reader brings to a recording — which
	// of these thousands of processes was the expensive one — is asked after the
	// run, and a recording that had measured only what it bounded could not
	// answer it.
	//
	// It is not the same quantity on every platform and is not converted into
	// one: the resident set on Unix, the job's committed charge on Windows. What
	// "the tree" covers is the platform's too, and internal/runner documents the
	// difference: exact on Windows, and on POSIX the larger of the kernel's
	// accounting for the child and the largest sum a bounded run's sampler saw
	// across the process group.
	PeakMemoryBytes int64 `json:"peak_memory_bytes,omitempty"`

	// OutputBytes and OutputSHA256 cover the whole of [ExecRecord.Output] and
	// are set by the recorder, so two runs are compared on what their commands
	// produced.
	//
	// "The whole" is the *retained* capture rather than everything the child
	// wrote. A caller that caps what it keeps — internal/runner keeps the tail
	// and pays for a truncation notice out of the same budget — hands over the
	// bytes it kept, notice included, and these two fields describe exactly
	// those: the same bytes a [DirSink] preserves beside the stream, which is
	// what makes the digest the join between an event and its
	// `output/<seq>.txt`. How much the command produced in total is a different
	// fact and gets a field of its own when something needs it, rather than
	// being folded into these two and making them mean neither thing.
	OutputBytes  int    `json:"output_bytes,omitempty"`
	OutputSHA256 string `json:"output_sha256,omitempty"`

	// OutputTruncated and OutputPath describe the file the bytes were
	// preserved in, and they are set by the sink that wrote it — never by a
	// caller, which has no file to describe. A sink that keeps no file leaves
	// both alone, and [Digested] clears them along with the bytes so that no
	// event claims a truncation of a file nothing wrote.
	//
	// OutputPath is relative to the run directory. It is absent when the
	// command produced no output, and also when the file could not be written:
	// preserving output is best effort, and a failure costs the path rather
	// than the event.
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	OutputPath      string `json:"output_path,omitempty"`

	Error string `json:"error,omitempty"`

	// Output is the retained capture, carried to whichever sink preserves it
	// and never serialised into the event. It is the bytes
	// [ExecRecord.OutputBytes] and [ExecRecord.OutputSHA256] describe.
	Output []byte `json:"-"`
}

// MutantRecord is one attempt at one mutant.
//
// It shares `id`, `display_id`, `package`, `args`, `timeout_ms`, `outcome`,
// `killed_by`, `duration_ms` and `error` with goatest's own mutant record.
type MutantRecord struct {
	ID        string `json:"id"`
	DisplayID string `json:"display_id,omitempty"`

	// Attempt counts from one: 1 is the concurrent pass, 2 the serial retry a
	// survivor gets. It is recorded rather than collapsed because "survived"
	// and "survived twice" are different facts about a flaky test.
	Attempt int `json:"attempt"`

	// Worker is which execution slot ran it, from zero.
	Worker int `json:"worker"`

	Package string `json:"package,omitempty"`

	// Binaries are the test binaries this attempt ran, in order, by the import
	// path of the package each was built from — never the file that was
	// executed, which is what the `argv` of the [ExecRecord] underneath it
	// carries. The import path is the name the run report uses and the one that
	// outlives the temporary directory the file lived in.
	Binaries []string `json:"binaries,omitempty"`

	// Tests are the tests the attempt was narrowed to, as `<import path>
	// <name>` labels in one order — the import path holds no space, so the
	// two halves are recoverable — and absent when every binary ran whole.
	Tests []string `json:"tests,omitempty"`

	Args      []string `json:"args,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`

	Outcome string `json:"outcome,omitempty"`

	// KilledBy is the test binary that detected the mutant, by the same import
	// path Binaries uses and the same one the run report's `killed_by` carries.
	// It is one of Binaries, so a reader can join the two.
	KilledBy string `json:"killed_by,omitempty"`

	DurationMS int64 `json:"duration_ms,omitempty"`

	// MemoryExceeded reports that the attempt was stopped by its memory bound
	// rather than by its deadline or by a test failing, and PeakMemoryBytes is the
	// highest the deciding binary's process tree was observed to hold.
	//
	// MemoryExceeded is why the outcome above says `killed` for a mutant that
	// never failed a test: the vocabulary is frozen and a bound is not a new
	// kind of verdict, so the fact that distinguishes this kill from an
	// assertion's travels beside it rather than inside it.
	//
	// PeakMemoryBytes is recorded for every attempt, bounded or not, as the
	// `exec` record's is — and it is the **maximum over every binary the
	// attempt started**, not the deciding binary's. An attempt's cost is the
	// worst moment it put the machine through, and the binary that settled it
	// need not be the one that cost the most.
	MemoryExceeded  bool  `json:"memory_exceeded,omitempty"`
	PeakMemoryBytes int64 `json:"peak_memory_bytes,omitempty"`

	// ExecSeqs are the `exec` events of the binaries this attempt ran, in
	// order. They are how an attempt is joined to the commands underneath it,
	// and therefore to their preserved output.
	ExecSeqs []int64 `json:"exec_seqs,omitempty"`

	// OutputTail is the tail of the killing binary's output. It is stripped by
	// [Digested] before a bounded ring, and kept by a sink that writes to
	// disk.
	OutputTail string `json:"output_tail,omitempty"`

	Error string `json:"error,omitempty"`
}

// ProbeRecord is one pass through the prepared probe tree, where no mutant is
// active and what is measured is which mutants' sites ever differed.
type ProbeRecord struct {
	Package string `json:"package,omitempty"`

	// Binaries are the test binaries the pass ran, by import path, exactly as
	// [MutantRecord.Binaries] names them.
	Binaries []string `json:"binaries,omitempty"`

	Args      []string `json:"args,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`

	Outcome    string `json:"outcome,omitempty"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms,omitempty"`

	// Infected are the mutants the pass made differ, by full identity.
	//
	// Facts come from a measured pass alone, so the schema requires this field
	// beside [ProbeOutcomeMeasured] and forbids it everywhere else. An empty
	// list is therefore meaningful and is written as `[]`: a measured pass that
	// infected nothing is the strongest thing the probe phase says about a
	// binary — nothing it runs can observe any of them — and omitting the list
	// would make that claim indistinguishable from a pass nothing measured.
	// `omitzero` rather than `omitempty` is what keeps the two apart: a nil
	// slice is absent, an empty one is `[]`.
	Infected []string `json:"infected,omitzero"`

	ExecSeqs []int64 `json:"exec_seqs,omitempty"`
	Error    string  `json:"error,omitempty"`
}

// ValidateRecord is one step of establishing which catalogued mutants can
// exist: the instrumentation, each compile, and each step of the bisection that
// a red compile starts.
type ValidateRecord struct {
	// Tree is [ValidateTreeMutant] or [ValidateTreeProbe].
	Tree string `json:"tree"`

	// Op is which step this is.
	Op string `json:"op"`

	// Build numbers the compiles of one validation, from one.
	Build int `json:"build,omitempty"`

	// Failed is whether that compile was red.
	Failed bool `json:"failed,omitempty"`

	// Blamed are the files the compiler named, in the order the search will
	// take them.
	Blamed []string `json:"blamed,omitempty"`

	// Pending is how many files were still undecided after it.
	Pending int `json:"pending,omitempty"`

	// Path is the file an isolation or a rejection is about.
	Path string `json:"path,omitempty"`

	// Candidates and Accepted are how many mutants that file offered and how
	// many of them the search kept.
	Candidates int `json:"candidates,omitempty"`
	Accepted   int `json:"accepted,omitempty"`

	// MutantID and Diagnostic are the rejected candidate and the compiler's
	// first line about it.
	MutantID   string `json:"mutant_id,omitempty"`
	Diagnostic string `json:"diagnostic,omitempty"`

	// Builds, Rejected and Accepted close the validation with what it spent
	// and what it decided.
	Builds   int `json:"builds,omitempty"`
	Rejected int `json:"rejected,omitempty"`

	// ExecSeq is the `exec` event of the compile this step ran, when it ran
	// one.
	ExecSeq int64 `json:"exec_seq,omitempty"`
}

// CoverageRecord is how coverage placed one mutant: which test binaries reach
// the block the mutation sits in, or that none does.
type CoverageRecord struct {
	MutantID string `json:"mutant_id"`
	Path     string `json:"path"`

	// StartLine and EndLine are the coverage block the mutation was mapped
	// into, not the mutation's own span. Zero for a mutant whose position no
	// block contained.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`

	// Covering are the test binaries whose coverage profile reaches that block,
	// by import path — the same identity [MutantRecord.Binaries] uses, and the
	// same one the run report's `covering_test_packages` carries.
	Covering []string `json:"covering,omitempty"`

	// CoveringTests are the tests whose own profile reaches the block, as
	// `<import path> <name>` labels in one order, in a run narrowed to tests.
	// Absent in a run narrowed to binaries, and for a mutant no test reaches.
	CoveringTests []string `json:"covering_tests,omitempty"`

	// Uncovered is the same statement as an empty Covering, recorded
	// explicitly so that a reader is not asked to infer a decision from an
	// omitted array.
	Uncovered bool `json:"uncovered,omitempty"`
}

// CacheRecord is one cache decision. It is recorded so that a warm run's
// recording says which results it did not compute — a cache hit is the one
// event that explains an absence of work.
type CacheRecord struct {
	Op       string `json:"op"`
	MutantID string `json:"mutant_id,omitempty"`
	Result   string `json:"result"`

	// Outcome is the cached verdict a hit produced.
	Outcome string `json:"outcome,omitempty"`

	// Directory is the store the run opened.
	Directory string `json:"directory,omitempty"`

	// ContextKey is the identity the entry is keyed under. No trace option
	// enters it; see docs/adr/0001-trace-is-not-evidence.md.
	ContextKey string `json:"context_key,omitempty"`

	Error string `json:"error,omitempty"`
}

// SnapshotRecord is one frozen tree.
type SnapshotRecord struct {
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
	Dir    string `json:"dir,omitempty"`

	// Stable reports whether the copy carried the source tree's modification
	// times with it, which is what lets the Go build cache be reused across
	// snapshots.
	Stable bool `json:"stable,omitempty"`

	Files      int    `json:"files,omitempty"`
	Digest     string `json:"digest,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

// SweepRecord is what collection reclaimed from a temporary directory before
// the run wrote anything into it. It is silent about a machine with nothing to
// reclaim, and it can never change a verdict.
type SweepRecord struct {
	Parent       string   `json:"parent"`
	Removed      []string `json:"removed,omitempty"`
	RemovedBytes int64    `json:"removed_bytes,omitempty"`

	// Live and Kept are the directories collection left alone: one still
	// locked by a running process, one a `--keep-temp` asked it to keep.
	Live  int    `json:"live,omitempty"`
	Kept  int    `json:"kept,omitempty"`
	Error string `json:"error,omitempty"`
}

// ArtifactRecord is a file or directory the run wrote or kept. Its fields are
// goatest's, exactly.
type ArtifactRecord struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// NoteRecord is something the run could not do, said once.
//
// Its `kind` and `detail` fields are goatest's, which spells this payload
// `progress` rather than `note`: a consumer joining the two streams reads
// go-mutants' `note` and goatest's `progress` as one kind of line. `code` is
// go-mutants' own `GOMnnnn` warning code and has no counterpart there.
type NoteRecord struct {
	Kind   string `json:"kind"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// RunRecord closes a recording with the accounting that tells a complete trace
// from a lossy one.
type RunRecord struct {
	Verdict string `json:"verdict,omitempty"`

	// ExitCode is the status the process is about to exit with.
	ExitCode int `json:"exit_code"`

	Error string `json:"error,omitempty"`

	// EventsEmitted and EventsDropped are never optional, because they are
	// what tells a complete recording from a lossy one.
	//
	// The accounting is taken before this event is written, so EventsEmitted is
	// the events the sink kept *before* this one and excludes it — which is
	// goatest's rule too, so one number means one thing in both recordings. For
	// an intact recording, `events_emitted + events_dropped + 1` is therefore
	// the number of lines in the stream, with `events_dropped` zero.
	EventsEmitted int64 `json:"events_emitted"`
	EventsDropped int64 `json:"events_dropped"`
}

// Clone returns a deep copy of the event, so that a sink which keeps an event
// and a caller which reuses a record cannot see each other's writes.
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
