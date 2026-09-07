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

// The verbosity levels [PlainRenderer.Verbosity] takes.
//
// They are levels rather than a set of switches because that is what a user
// types: `-v` is "tell me more" and `-vv` is "tell me everything", and a person
// deciding between them is not choosing which of six categories to enable. Each
// level adds to the one below it. The one thing that changes shape rather than
// being repeated is the recording: `-v` reads two events out of it in prose, and
// `-vv` prints every one of them, one to a line, in the recording's own words.
const (
	// VerbosityNormal is the default, and is the output every run printed
	// before the flag existed. It is byte-identical to it: the accounting
	// events are on the stream whether or not anybody is drawing them.
	VerbosityNormal = 0

	// VerbosityDetail is `-v`: where the time went, what caught each mutant,
	// which suites cover a survivor, and the handful of things the run's own
	// recording knows that a one-line warning had to fold away.
	VerbosityDetail = 1

	// VerbosityTrace is `-vv`: one line per recorded event, which is the
	// recording printed rather than a second account of the run.
	VerbosityTrace = 2

	// MaxVerbosity is the deepest level there is. A deeper request is this
	// level: `-vvv` is a typo with one obvious meaning, and refusing it would be
	// pedantry in front of somebody who is already trying to see more.
	MaxVerbosity = VerbosityTrace
)

// tracePrefix is what a line drawn from the recording is indented by.
//
// Two spaces, and every such line has them, so that the recording is a block a
// reader's eye can skip over and `grep -v '^  '` is the whole of "show me the
// run without its account of itself". It is indentation rather than a `trace:`
// label on each line because there are thousands of them, and a label repeated
// a thousand times is a column of noise.
const tracePrefix = "  "

// coarse is what a phase duration is rounded to.
//
// A phase is seconds to minutes long, and the milliseconds under it are the
// machine's mood rather than a measurement: two runs of the same workspace
// differ in them every time, and a `-v` log exists to be diffed against the run
// before it. Ten milliseconds is fine enough that a phase which really did get
// slower still says so.
const coarse = 10 * time.Millisecond

// FormatCoarseDuration renders a duration that is about a phase rather than
// about a command, rounded to [coarse].
func FormatCoarseDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return d.Round(coarse).String()
}

// byteUnits are the suffixes [FormatBytes] steps through, in powers of 1024.
var byteUnits = []string{"KiB", "MiB", "GiB", "TiB"}

// FormatBytes renders a size for a human.
//
// Powers of 1024 with the unambiguous suffixes, because the number beside them
// is going to be compared against what a file manager says. Bytes are printed
// exactly and everything above them to one decimal place: the difference
// between 4.0 and 4.1 MiB is worth seeing and the digits after it are not.
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

// phaseCompleted renders how long a phase took.
//
// It is the other half of the `phase <name>: <what is about to happen>` banner
// the run prints on entry, and it is worded to line up under it: same prefix,
// same colour, and the duration where the description was. A phase's duration is
// the coarsest useful thing a run can say about where its time went, which is
// why this is the first thing `-v` adds.
//
// Quiet wins over verbosity here and in [PlainRenderer.traced], which is what
// makes this type total rather than dependent on a rule two packages away:
// internal/cli refuses `-v` with `--quiet`, and a renderer handed both would
// otherwise print a phase's duration under a banner --quiet had dropped.
func (r *PlainRenderer) phaseCompleted(e engine.PhaseCompleted) (string, bool) {
	if r.Quiet || r.Verbosity < VerbosityDetail {
		return "", false
	}
	return r.paint(stylePhase, "phase "+e.Phase.String()+":") +
		" done (" + FormatCoarseDuration(e.Duration) + ")", true
}

// attribution is what `-v` adds to the end of a result line: the suite the
// outcome came from, and how many passes it took.
//
// Both answer the first question their outcome raises. "Killed" on a module
// with forty test binaries leaves a reader with forty places to go and look;
// and a second pass means the first one timed out, which is the difference
// between a slow test and a mutant that hangs.
//
// The two outcomes that name a binary name it differently, because they are
// different findings. A kill was detected — a test failed, and that test is
// where the mutant is caught. A timeout was not detected by anything: the
// binary is the one the mutant hung, and "killed by" would tell a reader to go
// and look for an assertion that does not exist. [engine.MutantResult.KilledBy]
// documents the field as carrying both.
//
// The attempt count is stated only for the outcomes something was attempted on.
// An uncovered survivor was never executed and an abandoned mutant never
// settled, so a count beside either would be a number about work that did not
// happen.
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
			b.WriteString(" hung in " + m.KilledBy)
		}
	}
	if m.MemoryExceeded {
		// Why a kill names a suite that reported no failure. Both numbers are
		// there because either alone is unactionable: the peak says what the
		// mutant did, and the bound says what it was measured against, and the
		// person deciding whether the bound is too tight needs to see them
		// beside each other.
		b.WriteString(" (memory: " + FormatBytes(m.PeakMemory) + " > " + FormatBytes(m.MemoryLimit) + " bound)")
	}
	if m.Attempts > 1 && attempted(m.Outcome) {
		b.WriteString(" (" + strconv.Itoa(m.Attempts) + " attempts)")
	}
	return b.String()
}

// memoryDerivedLine is the run's memory budget in one sentence.
//
// The unbounded case is a sentence rather than a number because there is no
// number to print, and "memory bound: 0 B" would read as a bound of nothing
// rather than as the absence of one. What it does not say is *why* — that is
// the warning's job, and saying it twice would be two places to reword it.
func memoryDerivedLine(e engine.MemoryDerived) string {
	if e.Limit <= 0 {
		// The one place the absence of a bound is stated, now that a derived
		// bound nobody can enforce publishes no warning: warning about a
		// platform's own limits on every clean run is how a warning stops being
		// read, and the run's own account is where a fact like this belongs.
		// The peak is still printed when there is one, because it is the number
		// that says *why* there is no bound — a platform that measured a peak
		// and enforces nothing is a different situation from one that measured
		// nothing at all.
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

// attempted reports whether an outcome is one a pass over the test binaries
// produced. See [engine.MutantResult.Attempts], which counts those passes.
func attempted(o mutation.Outcome) bool {
	switch o {
	case mutation.OutcomeKilled, mutation.OutcomeSurvived, mutation.OutcomeTimedOut:
		return true
	default:
		return false
	}
}

// warningDetail is the rest of what a warning has to say, laid out under it.
//
// It is indented like a survivor's diff and for the same reason: a block of
// compiler output arriving flush against the left margin would read as further
// findings rather than as the explanation of the line above it. A warning with
// nothing more to say prints nothing at all, rather than a label with an empty
// line after it.
func (r *PlainRenderer) warningDetail(e engine.Warning) string {
	if r.Verbosity < VerbosityDetail || strings.TrimSpace(e.Detail) == "" {
		return ""
	}
	return "\n" + diffIndent + r.paint(styleDetail, indented(e.Detail))
}

// covering is the third line `-v` puts under a survivor: the suites that ran
// the line and did not notice, or the statement that nothing runs it at all.
//
// The two are different pieces of work — sharpen a test you have, or write one
// — and they are told apart by [engine.MutantResult.Uncovered] rather than by
// the list being empty. A run with coverage off has an empty list for every
// mutant because nothing was measured, and printing "no test binary" there
// would be a claim the run never made.
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

// traced renders one event of the run's own recording, at whichever verbosity
// asked for it.
//
// The two levels are not two formats. `-v` reads the one thing out of the
// recording that a console has no other way to say — what a sweep reclaimed,
// which is where a run's unexplained pause went — and is silent about the rest,
// because a run of any size records thousands of events and burying four
// survivors under them is the opposite of what a verbose flag is for. `-vv`
// prints all of them, one to a line, indented, *and* keeps the prose line: more
// `v` must never show less, and the prose line is unindented, so the promise
// that indented lines count recorded events is untouched.
//
// The finished recorded line is flattened here, at the one place every one of
// them passes through. A recorded argument vector, directory or path is
// whatever the operating system allowed, and a newline in any of them would
// otherwise become a second physical line — one that carries no prefix, so it
// would survive a `grep -v` meant to remove the recording, and would make the
// count of lines exceed the count of events. The spacing a shell-quoted
// argument put there on purpose is left alone.
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
			// An event with no type at all: the recording lost its envelope,
			// which is worth a line saying so rather than a blank one.
			line = "event"
		}
		lines = append(lines, tracePrefix+r.paint(styleDetail, flattened(line)))
	}
	if len(lines) == 0 {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

// digest is the `-v` reading of the recording: what a user would otherwise have
// to open the stream to find.
func (r *PlainRenderer) digest(e trace.Event) (string, bool) {
	if e.Type != trace.TypeSweep || e.Sweep == nil || len(e.Sweep.Removed) == 0 {
		return "", false
	}
	// A run that paused to delete four gigabytes has an explanation for the
	// pause, and this is it. A sweep that reclaimed nothing — which is most of
	// them — says nothing rather than a zero.
	return r.paint(styleDetail, "sweep: removed "+
		strconv.Itoa(len(e.Sweep.Removed))+" "+plural(len(e.Sweep.Removed), "directory", "directories")+
		" ("+FormatBytes(e.Sweep.RemovedBytes)+")"), true
}

// lineBreaks is what [flattened] spends on a space: the two bytes that would
// otherwise end a line somebody is counting.
var lineBreaks = strings.NewReplacer("\n", " ", "\r", " ")

// flattened spends every line break in a finished line on a space.
//
// It is deliberately not [oneLine], which collapses runs of whitespace: by the
// time a line reaches here it holds shell-quoted arguments whose spacing is the
// command as it was run, and a formatter that squeezed `\'a  b\'` would make the
// line unpasteable in order to tidy it.
func flattened(line string) string { return lineBreaks.Replace(line) }

// indented lays a multi-line detail out under the line that introduced it, so
// that a block of compiler output cannot be mistaken for further findings.
func indented(detail string) string {
	return strings.ReplaceAll(strings.TrimRight(detail, "\n"), "\n", "\n"+diffIndent)
}

// traceLine renders one recorded event as one line, and reports whether this
// package knew what to do with it.
//
// The rendering rules are the same for every type, and they are what make two
// runs comparable:
//
//   - No timestamps. A recording is stamped with the wall clock, and a console
//     that printed the stamps would make every line of two runs differ. What a
//     reader needs is how long the thing took, which is what is printed.
//   - One line per event, always. Any field that can hold a newline — a
//     compiler diagnostic, a note's detail — is flattened, so that counting
//     lines counts events.
//   - A field the recording did not set is left out rather than printed empty,
//     which keeps a line about a cache open from carrying a mutant id it has
//     none of.
//
// A false second return says the type is not one this package knows, or that
// its payload was missing: the type itself is returned, so that even an event
// nothing can read still costs exactly one line and names itself. It is what
// TestEveryTraceEventTypeHasAVerboseRendering asserts against.
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
		// The colon is what separates the note's identity from its free text,
		// which is prose and would otherwise run straight on from the code.
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

// validateLine renders one validation step from the fields it set.
//
// Validation is the one payload whose shape changes with its `op` — a compile
// carries a build number and the files the compiler blamed, a rejection carries
// a candidate and a diagnostic, and the closing step carries what the whole
// search spent — so the line is assembled from what is there rather than laid
// out in fixed columns. The order is fixed even so, which is what keeps two runs
// diffable line for line.
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

// detail appends a line's free text behind a colon, flattened, or nothing when
// there is none.
func detail(s string) string {
	if flat := oneLine(s); flat != "" {
		return ": " + flat
	}
	return ""
}

// join puts the fields that are set on one line, separated by single spaces.
// An empty field is dropped rather than printed, which is what lets every
// rendering above list its optional fields inline instead of building a slice.
//
// It copies rather than filtering in place, because a caller may pass a slice
// it built — [validateLine] does — and a formatter that rearranged its
// argument would be a surprise nobody would look for.
func join(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " ")
}

// qualified writes "phase/name", or the name alone when there is no phase.
func qualified(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

// settled prefers what became of a step to what state it is in: a finished
// stage says "succeeded", and one that is still open says "started".
func settled(state, result string) string {
	if result != "" {
		return result
	}
	return state
}

// duration renders an optional recorded duration, and nothing at all when the
// recording did not carry one.
func duration(ms *int64) string {
	if ms == nil {
		return ""
	}
	return FormatDuration(milliseconds(*ms))
}

// milliseconds turns the recording's own unit back into a duration.
func milliseconds(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

// timedOut names the one exit status a number cannot describe: a child that was
// killed for taking too long exits however the signal left it.
func timedOut(t bool) string {
	if !t {
		return ""
	}
	return "timed-out"
}

// failure appends what went wrong, flattened onto the line it belongs to.
func failure(err string) string {
	if err == "" {
		return ""
	}
	return "error: " + oneLine(err)
}

// subject shortens a mutant id and leaves every other subject alone.
//
// A recorded subject is a mutant id, an import path, or a scope pattern. The id
// is sixty-four characters and the console names mutants by their first eight
// everywhere else, so printing it in full would put the same identity on the
// screen in two widths and make every mutant line wrap.
func subject(s string) string {
	if mutation.IsID(s) {
		return shortID(s)
	}
	return s
}

// bracketed writes a list as "[a b]", or nothing when it is empty.
//
// Brackets rather than commas because the elements are import paths and file
// names, which contain no spaces, and a reader scanning a wall of verbose lines
// finds the edges of a list faster than the separators inside it.
func bracketed(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return "[" + strings.Join(items, " ") + "]"
}

// TraceLine renders one recorded event as the single line `run -vv` prints for
// it, without the indentation that marks a recorded line on a console.
//
// It is exported for `go-mutants explain`, which reads a recording back and
// quotes the commands one mutant's passes started. Two renderings of one event
// would be two things to keep in step, and the difference would surface in the
// worst possible place: a user comparing what `-vv` printed during the run with
// what `explain` says about it afterwards.
//
// An event this package cannot read still costs exactly one line and names
// itself, which is [traceLine]'s own rule; an event that lost its envelope
// altogether is rendered as "event", so that no caller has to invent a spelling
// for a line with no type on it.
func TraceLine(e trace.Event) string {
	line, _ := traceLine(e)
	if line == "" {
		return "event"
	}
	return flattened(line)
}

// QuoteArgv renders an argument vector as a POSIX shell would have to be given
// it: single quotes, with an embedded quote closed, escaped and reopened.
//
// It is exported for the reason [TraceLine] is: `go-mutants explain` prints a
// command to paste, and a second quoter would eventually disagree with this one
// about a path with a space in it — which is exactly the command nobody would
// notice was wrong until they ran it.
//
// POSIX is the whole of what it promises, and a caller printing a line for a
// user to paste should say so. PowerShell and cmd.exe quote differently and
// spell an environment assignment differently again, so on Windows the result
// is a line to read — the program, its arguments, and where one ends and the
// next begins — rather than one to paste. Quoting for every shell there is
// would mean printing the same command three times, and choosing at run time
// would mean guessing which shell the terminal on the other end of a pipe is.
func QuoteArgv(argv []string) string { return quoteArgv(argv) }

// UnquoteArgv reads back a line [QuoteArgv] wrote.
//
// It exists for the platform whose shell cannot run that line. A reproduction
// printed for a POSIX shell still has to be *checked* on Windows, and the only
// way to check a line is to decode it — so the quoter and the reader are kept
// beside each other and held to each other by a round trip, rather than a test
// growing a second, subtly different parser of its own.
//
// It is deliberately not a shell. What it accepts is exactly what [QuoteArgv]
// produces: bare words, single-quoted runs, and a backslash escaping the byte
// after it — which is the three pieces the `'\”` idiom is made of. Words are
// separated by spaces and tabs, and adjacent pieces belong to one word, so
// `'a'\”b'` decodes to the single argument `a'b`. Double quotes, `$`, and
// every other metacharacter are ordinary bytes here, because a quoted line
// never asks a shell to interpret them and this is not the place to start.
//
// An unterminated quote and a trailing backslash are errors rather than
// guesses: a line that did not come from [QuoteArgv] should be reported as one,
// not turned into a plausible argument vector.
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

// quoteArgv renders an argument vector the way a shell would have to be given
// it: joined with spaces, and quoted only where a bare word would not survive.
//
// The point is that a line can be selected with a mouse and pasted into a
// terminal to run the command again, which is the single most useful thing a
// recorded subprocess offers somebody diagnosing a run. Quoting everything
// would make every line unreadable to buy that; quoting nothing would make the
// paste silently run a different command.
func quoteArgv(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

// shellSafe is every byte a POSIX shell passes through untouched.
const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" +
	"@%+=:,./-_"

// shellQuote wraps one argument in single quotes when it needs them.
func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if strings.IndexFunc(arg, func(r rune) bool { return !strings.ContainsRune(shellSafe, r) }) < 0 {
		return arg
	}
	// A single quote cannot appear inside single quotes, so it is closed,
	// escaped, and reopened — which is what every shell quoter does and what a
	// shell reads back as the original byte.
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// oneLine flattens whatever a recording holds into a line.
//
// A compiler diagnostic and a note's detail are both free text that may be a
// paragraph. `-vv` promises one line per event — it is what makes counting
// lines the same as counting events — so the newlines are collapsed here rather
// than being allowed to break the promise wherever such a field happens to be
// set.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// coveringTestLabels renders test references as `<package> <name>`, the form
// the covering line names a narrowed run's tests by.
func coveringTestLabels(refs []report.TestRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Package+" "+ref.Name)
	}
	return out
}
