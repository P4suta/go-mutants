// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package console

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/trace"
)

const (
	VerbosityNormal = 0

	VerbosityDetail = 1

	VerbosityTrace = 2

	MaxVerbosity = VerbosityTrace
)

const tracePrefix = "  "

const coarse = 10 * time.Millisecond

func FormatCoarseDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return d.Round(coarse).String()
}

var byteUnits = []string{"KiB", "MiB", "GiB", "TiB"}

func FormatBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	value := float64(n) / 1024
	unit := byteUnits[0]
	for _, next := range byteUnits[1:] {
		if value < 1024 {
			break
		}
		value /= 1024
		unit = next
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + unit
}

func (r *PlainRenderer) phaseCompleted(e engine.PhaseCompleted) (string, bool) {
	if r.Quiet || r.Verbosity < VerbosityDetail {
		return "", false
	}
	return r.paint(stylePhase, "phase "+e.Phase.String()+":") +
		" done (" + FormatCoarseDuration(e.Duration) + ")", true
}

func (r *PlainRenderer) attribution(m engine.MutantResult) string {
	if r.Verbosity < VerbosityDetail {
		return ""
	}
	var b strings.Builder
	if m.KilledBy != "" {
		switch m.Outcome {
		case mutation.OutcomeKilled:
			b.WriteString(" killed by " + m.KilledBy)
		case mutation.OutcomeTimedOut:
			if m.Diverged {
				b.WriteString(" does not return; a loop in " + m.KilledBy + " ran away")
			} else {
				b.WriteString(" hung in " + m.KilledBy)
			}
		case mutation.OutcomeSurvived, mutation.OutcomeErrored, mutation.OutcomeInconclusive, mutation.OutcomeNotRun:
		}
	}
	if m.MemoryExceeded {
		b.WriteString(" (memory: " + FormatBytes(m.PeakMemory) + " > " + FormatBytes(m.MemoryLimit) + " bound)")
	}
	if m.Attempts > 1 && attempted(m.Outcome) {
		b.WriteString(" (" + strconv.Itoa(m.Attempts) + " attempts)")
	}
	return b.String()
}

func memoryDerivedLine(e engine.MemoryDerived) string {
	if e.Limit <= 0 {
		if e.Peak > 0 {
			return "memory: baseline peak " + FormatBytes(e.Peak) +
				", no per-mutant bound (not enforced on this platform)"
		}
		return "memory: no per-mutant bound (nothing measured what the baseline runs cost)"
	}
	if e.Peak <= 0 {
		return "memory: bound " + FormatBytes(e.Limit) + " (" + e.Source.String() + ")"
	}
	return "memory: baseline peak " + FormatBytes(e.Peak) +
		", bound " + FormatBytes(e.Limit) + " (" + e.Source.String() + ")"
}

func attempted(o mutation.Outcome) bool {
	switch o {
	case mutation.OutcomeKilled, mutation.OutcomeSurvived, mutation.OutcomeTimedOut:
		return true
	case mutation.OutcomeErrored, mutation.OutcomeInconclusive, mutation.OutcomeNotRun:
		return false
	}
	return false
}

func (r *PlainRenderer) warningDetail(e engine.Warning) string {
	if r.Verbosity < VerbosityDetail || strings.TrimSpace(e.Detail) == "" {
		return ""
	}
	return "\n" + diffIndent + r.paint(styleDetail, indented(e.Detail))
}

func (r *PlainRenderer) covering(m engine.MutantResult) string {
	if r.Verbosity < VerbosityDetail {
		return ""
	}
	switch {
	case len(m.CoveringTests) > 0:
		return "\n" + diffIndent + r.paint(styleDetail, "covered by: "+strings.Join(coveringTestLabels(m.CoveringTests), ", "))
	case len(m.CoveringTestPackages) > 0:
		return "\n" + diffIndent + r.paint(styleDetail, "covered by: "+strings.Join(m.CoveringTestPackages, ", "))
	case m.Uncovered:
		return "\n" + diffIndent + r.paint(styleDetail, "no test binary")
	default:
		return ""
	}
}

func (r *PlainRenderer) traced(e trace.Event) (string, bool) {
	if r.Quiet || r.Verbosity < VerbosityDetail {
		return "", false
	}
	var lines []string
	if line, ok := r.digest(e); ok {
		lines = append(lines, line)
	}
	if r.Verbosity >= VerbosityTrace {
		line, _ := traceLine(e)
		if line == "" {
			line = "event"
		}
		lines = append(lines, tracePrefix+r.paint(styleDetail, flattened(line)))
	}
	if len(lines) == 0 {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

func (r *PlainRenderer) digest(e trace.Event) (string, bool) {
	if e.Type != trace.TypeSweep || e.Sweep == nil || len(e.Sweep.Removed) == 0 {
		return "", false
	}
	return r.paint(styleDetail, "sweep: removed "+
		strconv.Itoa(len(e.Sweep.Removed))+" "+plural(len(e.Sweep.Removed), "directory", "directories")+
		" ("+FormatBytes(e.Sweep.RemovedBytes)+")"), true
}

var lineBreaks = strings.NewReplacer("\n", " ", "\r", " ")

func flattened(line string) string { return lineBreaks.Replace(line) }

func indented(detail string) string {
	return strings.ReplaceAll(strings.TrimRight(detail, "\n"), "\n", "\n"+diffIndent)
}

func traceLine(e trace.Event) (string, bool) {
	switch e.Type {
	case trace.TypeRunStart:
		if e.Start == nil {
			break
		}
		return join("run-start", e.Start.Kind, e.Start.RunID, e.Start.ToolVersion, e.Start.Root), true

	case trace.TypePhaseStart, trace.TypePhaseEnd:
		if e.Phase == nil {
			break
		}
		return join(e.Type, e.Phase.Name, duration(e.Phase.DurationMS)), true

	case trace.TypeStage:
		if e.Stage == nil {
			break
		}
		return join("stage", qualified(e.Stage.Phase, e.Stage.Name),
			settled(e.Stage.State, e.Stage.Result), duration(e.Stage.DurationMS),
			oneLine(e.Stage.Detail)), true

	case trace.TypePrepare:
		if e.Prepare == nil {
			break
		}
		return join("prepare", e.Prepare.Phase,
			settled(e.Prepare.State, e.Prepare.Result), duration(e.Prepare.DurationMS)), true

	case trace.TypeExec:
		if e.Exec == nil {
			break
		}
		return join("exec", e.Exec.Kind, subject(e.Exec.Subject),
			"exit", strconv.Itoa(e.Exec.ExitCode), timedOut(e.Exec.TimedOut),
			FormatDuration(milliseconds(e.Exec.DurationMS)),
			quoteArgv(e.Exec.Argv), failure(e.Exec.Error)), true

	case trace.TypeMutantExec:
		if e.Mutant == nil {
			break
		}
		return join("attempt", strconv.Itoa(e.Mutant.Attempt), shortID(e.Mutant.ID),
			"worker", strconv.Itoa(e.Mutant.Worker), e.Mutant.Outcome,
			FormatDuration(milliseconds(e.Mutant.DurationMS)),
			bracketed(e.Mutant.Binaries), failure(e.Mutant.Error)), true

	case trace.TypeProbeExec:
		if e.Probe == nil {
			break
		}
		return join("probe", e.Probe.Outcome, "exit", strconv.Itoa(e.Probe.ExitCode),
			FormatDuration(milliseconds(e.Probe.DurationMS)),
			"infected", strconv.Itoa(len(e.Probe.Infected)),
			bracketed(e.Probe.Binaries), failure(e.Probe.Error)), true

	case trace.TypeValidate:
		if e.Validate == nil {
			break
		}
		return validateLine(*e.Validate), true

	case trace.TypeCoverageMap:
		if e.Coverage == nil {
			break
		}
		if e.Coverage.Uncovered || len(e.Coverage.Covering) == 0 {
			return join("coverage-map", shortID(e.Coverage.MutantID), "uncovered"), true
		}
		return join("coverage-map", shortID(e.Coverage.MutantID),
			"covered by", bracketed(e.Coverage.Covering)), true

	case trace.TypeCache:
		if e.Cache == nil {
			break
		}
		return join("cache", e.Cache.Op, shortID(e.Cache.MutantID), e.Cache.Result,
			failure(e.Cache.Error)), true

	case trace.TypeSnapshot:
		if e.Snapshot == nil {
			break
		}
		return join("snapshot", e.Snapshot.Kind, e.Snapshot.Dir,
			"stable="+strconv.FormatBool(e.Snapshot.Stable),
			"files="+strconv.Itoa(e.Snapshot.Files), failure(e.Snapshot.Error)), true

	case trace.TypeSweep:
		if e.Sweep == nil {
			break
		}
		return join("sweep", e.Sweep.Parent,
			"removed", strconv.Itoa(len(e.Sweep.Removed)),
			"("+FormatBytes(e.Sweep.RemovedBytes)+")",
			"live", strconv.Itoa(e.Sweep.Live), "kept", strconv.Itoa(e.Sweep.Kept),
			failure(e.Sweep.Error)), true

	case trace.TypeArtifact:
		if e.Artifact == nil {
			break
		}
		return join("artifact", e.Artifact.Kind, e.Artifact.Path), true

	case trace.TypeNote:
		if e.Note == nil {
			break
		}
		return join("note", e.Note.Kind, e.Note.Code) + detail(e.Note.Detail), true

	case trace.TypeRunEnd:
		if e.Run == nil {
			break
		}
		return join("run-end", e.Run.Verdict, "exit", strconv.Itoa(e.Run.ExitCode),
			"events", strconv.FormatInt(e.Run.EventsEmitted, 10),
			"dropped", strconv.FormatInt(e.Run.EventsDropped, 10),
			failure(e.Run.Error)), true
	}
	return e.Type, false
}

func validateLine(v trace.ValidateRecord) string {
	parts := []string{"validate", qualified(v.Tree, v.Op)}
	if v.Build > 0 {
		parts = append(parts, "build", strconv.Itoa(v.Build))
	}
	if v.Failed {
		parts = append(parts, "failed")
	}
	parts = append(parts, v.Path)
	if v.Candidates > 0 {
		parts = append(parts, "candidates", strconv.Itoa(v.Candidates))
	}
	if v.Accepted > 0 {
		parts = append(parts, "accepted", strconv.Itoa(v.Accepted))
	}
	if len(v.Blamed) > 0 {
		parts = append(parts, "blamed", bracketed(v.Blamed))
	}
	if v.Pending > 0 {
		parts = append(parts, "pending", strconv.Itoa(v.Pending))
	}
	if v.MutantID != "" {
		parts = append(parts, shortID(v.MutantID))
	}
	if v.Builds > 0 {
		parts = append(parts, "builds", strconv.Itoa(v.Builds))
	}
	if v.Rejected > 0 {
		parts = append(parts, "rejected", strconv.Itoa(v.Rejected))
	}
	return join(parts...) + detail(v.Diagnostic)
}

func detail(s string) string {
	if flat := oneLine(s); flat != "" {
		return ": " + flat
	}
	return ""
}

func join(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " ")
}

func qualified(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

func settled(state, result string) string {
	if result != "" {
		return result
	}
	return state
}

func duration(ms *int64) string {
	if ms == nil {
		return ""
	}
	return FormatDuration(milliseconds(*ms))
}

func milliseconds(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

func timedOut(t bool) string {
	if !t {
		return ""
	}
	return "timed-out"
}

func failure(err string) string {
	if err == "" {
		return ""
	}
	return "error: " + oneLine(err)
}

func subject(s string) string {
	if mutation.IsID(s) {
		return shortID(s)
	}
	return s
}

func bracketed(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return "[" + strings.Join(items, " ") + "]"
}

func TraceLine(e trace.Event) string {
	line, _ := traceLine(e)
	if line == "" {
		return "event"
	}
	return flattened(line)
}

func QuoteArgv(argv []string) string { return quoteArgv(argv) }

func UnquoteArgv(line string) ([]string, error) {
	var argv []string
	var word strings.Builder
	started, quoted := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quoted && c == '\'':
			quoted = false
		case quoted:
			word.WriteByte(c)
		case c == '\'':
			quoted, started = true, true
		case c == '\\':
			i++
			if i == len(line) {
				return nil, fmt.Errorf("go-mutants: %q ends in a backslash", line)
			}
			word.WriteByte(line[i])
			started = true
		case c == ' ' || c == '\t':
			if started {
				argv = append(argv, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteByte(c)
			started = true
		}
	}
	if quoted {
		return nil, fmt.Errorf("go-mutants: %q has an unterminated quote", line)
	}
	if started {
		argv = append(argv, word.String())
	}
	return argv, nil
}

func quoteArgv(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" +
	"@%+=:,./-_"

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if strings.IndexFunc(arg, func(r rune) bool { return !strings.ContainsRune(shellSafe, r) }) < 0 {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func coveringTestLabels(refs []report.TestRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Package+" "+ref.Name)
	}
	return out
}
