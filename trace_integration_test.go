// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package gomutants_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/tempowner"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/trace"
)

// This file is the recording half of the engine API: what a workspace records
// about itself, and what a consumer joining its own recording to it may rely
// on.
//
// Every test here is toolchain-backed, because the claim is about what the
// engine *did* rather than about the shape of an event: a `go-test-c` that is
// recorded only when a test builds one is the only kind of evidence worth
// having that the label is on the command it says it is. The claims about the
// shape alone are in trace_contract_test.go, where they cost nothing.

// recordingOf is what a workspace recorded, with the two failures that make
// every later assertion meaningless reported once, here.
func recordingOf(t *testing.T, workspace *gomutants.Workspace) []trace.Event {
	t.Helper()
	events := workspace.Recording()
	if events == nil {
		t.Fatal("Recording() is nil for a workspace opened with no sink, so nothing below" +
			" is a claim about a recording")
	}
	if len(events) == 0 {
		t.Fatal("Recording() is empty; not even the run-start was kept")
	}
	return events
}

// validateRecording checks every line against the published contract, through
// the very validator a consumer would use, and reports the stream-level rules
// the schema cannot state because it validates one line at a time.
func validateRecording(t *testing.T, events []trace.Event) {
	t.Helper()
	previous := int64(0)
	ended := false
	for i, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("event %d cannot be encoded: %v", i, err)
		}
		if err := schemas.Validate(schemas.TraceEventV1, encoded); err != nil {
			t.Errorf("event %d does not validate against %s: %v\n%s", i, schemas.TraceEventV1, err, encoded)
		}
		if event.Seq <= previous {
			t.Errorf("event %d carries seq %d after %d; seq order is the order of the recording",
				i, event.Seq, previous)
		}
		previous = event.Seq
		if ended {
			t.Errorf("event %d is a %s after the run-end; a recording has one last line", i, event.Type)
		}
		ended = ended || event.Type == trace.TypeRunEnd
		if event.Type == trace.TypeRunStart && i != 0 {
			t.Errorf("event %d is a run-start, which may only be the first line", i)
		}
	}
}

// execKinds is every command label the recording holds, deduplicated.
func execKinds(events []trace.Event) []string {
	var kinds []string
	for _, event := range events {
		if event.Type == trace.TypeExec && !slices.Contains(kinds, event.Exec.Kind) {
			kinds = append(kinds, event.Exec.Kind)
		}
	}
	slices.Sort(kinds)
	return kinds
}

// eventAt is the event recorded at one sequence number, which is how a result's
// TraceSeq is followed into the recording.
func eventAt(t *testing.T, events []trace.Event, seq int64) trace.Event {
	t.Helper()
	if seq == 0 {
		t.Fatal("the result carries TraceSeq 0, so it points at no event; a workspace always" +
			" has a recorder, and a call that ran always has a sequence")
	}
	for _, event := range events {
		if event.Seq == seq {
			return event
		}
	}
	t.Fatalf("nothing in the recording was recorded at seq %d", seq)
	return trace.Event{}
}

// artifactsOfKind is every path the recording says was written or kept under
// one kind.
func artifactsOfKind(events []trace.Event, kind string) []string {
	var paths []string
	for _, event := range events {
		if event.Type == trace.TypeArtifact && event.Artifact.Kind == kind {
			paths = append(paths, event.Artifact.Path)
		}
	}
	return paths
}

// TestOpenWithoutATraceSinkRecordsIntoARingReadableAfterClose is the default
// every consumer gets without asking.
//
// A workspace that was handed no sink still records, into a bounded ring, and
// the recording stays readable after Close — which is the moment it is for: the
// last event is the run-end Close wrote, so a caller reading it then has the
// whole account of the workspace. That is the same bargain an untraced
// `go-mutants run` makes, and for the same reason: the failure nobody expected
// is exactly the failure nobody thought to ask for a recording of.
func TestOpenWithoutATraceSinkRecordsIntoARingReadableAfterClose(t *testing.T) {
	root := copyFixture(t, "simple")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: t.TempDir()})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}

	listed, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "env", "GOVERSION"}})
	if err != nil {
		t.Fatalf("running a workspace command: %v", err)
	}
	if listed.ExitCode != 0 {
		t.Fatalf("go env GOVERSION = exit %d:\n%s", listed.ExitCode, listed.Output)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("closing workspace: %v", err)
	}

	events := recordingOf(t, workspace)
	validateRecording(t, events)

	first := events[0]
	if first.Type != trace.TypeRunStart {
		t.Fatalf("the first event is a %s, want a %s", first.Type, trace.TypeRunStart)
	}
	if first.Start.Kind != trace.StartKindWorkspace {
		t.Errorf("run-start kind = %q, want %q", first.Start.Kind, trace.StartKindWorkspace)
	}
	if first.Start.Root != root {
		t.Errorf("run-start root = %q, want the tree the workspace is about, %q", first.Start.Root, root)
	}
	if first.Schema != trace.SchemaV1 {
		t.Errorf("run-start schema = %q, want %q", first.Schema, trace.SchemaV1)
	}

	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd {
		t.Fatalf("the last event after Close is a %s, want a %s: a reader who found the"+
			" run-end has read the whole workspace", last.Type, trace.TypeRunEnd)
	}
	if last.Run.Verdict != "closed" {
		t.Errorf("run-end verdict = %q, want %q", last.Run.Verdict, "closed")
	}
	if last.Run.EventsDropped != 0 {
		t.Errorf("run-end reports %d dropped events; this workspace recorded far fewer than"+
			" the ring holds", last.Run.EventsDropped)
	}

	// Everything Open did is in there, labelled: the toolchain probe, the sweep
	// it ran before copying anything, the tree it froze.
	if kinds := execKinds(events); !slices.Contains(kinds, trace.ExecKindGoVersion) ||
		!slices.Contains(kinds, trace.ExecKindWorkspaceExec) {
		t.Errorf("the recording holds the command kinds %v, want %q and %q among them",
			kinds, trace.ExecKindGoVersion, trace.ExecKindWorkspaceExec)
	}
	frozen := false
	swept := false
	for _, event := range events {
		frozen = frozen || event.Type == trace.TypeSnapshot && event.Snapshot.Kind == trace.SnapshotKindWorkspace
		swept = swept || event.Type == trace.TypeSweep
	}
	if !frozen {
		t.Error("nothing in the recording says the workspace was frozen")
	}
	if !swept {
		t.Error("nothing in the recording says what Open's sweep reclaimed")
	}

	// The join a consumer writes: a CommandResult in hand, the whole command in
	// the recording. argv[0] is the located toolchain rather than the "go" the
	// caller wrote, which is exactly what a reader reproducing the command
	// needs and cannot reconstruct.
	command := eventAt(t, events, listed.TraceSeq)
	if command.Type != trace.TypeExec {
		t.Fatalf("CommandResult.TraceSeq points at a %s, want an %s", command.Type, trace.TypeExec)
	}
	if command.Exec.Kind != trace.ExecKindWorkspaceExec {
		t.Errorf("the command a workspace was asked to run is recorded as %q, want %q",
			command.Exec.Kind, trace.ExecKindWorkspaceExec)
	}
	if got := command.Exec.Argv[1:]; !slices.Equal(got, []string{"env", "GOVERSION"}) {
		t.Errorf("the recorded argv is %v, want the vector the child received", command.Exec.Argv)
	}
	if !filepath.IsAbs(command.Exec.Argv[0]) {
		t.Errorf("argv[0] = %q, want the absolute path of the located toolchain", command.Exec.Argv[0])
	}
	if command.Exec.ExitCode != listed.ExitCode || command.Exec.OutputBytes != int(listed.TotalBytes) {
		t.Errorf("the recording says exit %d over %d bytes and the result says exit %d over %d",
			command.Exec.ExitCode, command.Exec.OutputBytes, listed.ExitCode, listed.TotalBytes)
	}
	// Names, never values: a recording is meant to be attachable to a bug
	// report from a machine holding real credentials.
	for _, name := range command.Exec.EnvNames {
		if strings.Contains(name, "=") {
			t.Errorf("env_names holds %q, which is a value and not a name", name)
		}
	}
}

// TestOpenWithATraceSinkRecordsNothingIntoTheRing is the other half of the
// choice.
//
// A caller that supplies a sink owns the recording, and there is no second copy
// of it: a caller holding both would have to decide which is the account of the
// run, and the ring is the one that silently drops its oldest events.
func TestOpenWithATraceSinkRecordsNothingIntoTheRing(t *testing.T) {
	root := copyFixture(t, "simple")
	// Unbounded, because what is under test is that the sink saw everything.
	sink := trace.NewMemorySink(0)
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Trace:         sink,
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	listed, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "env", "GOVERSION"}})
	if err != nil {
		t.Fatalf("running a workspace command: %v", err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("closing workspace: %v", err)
	}

	if events := workspace.Recording(); events != nil {
		t.Errorf("Recording() returned %d events for a workspace whose caller supplied a sink,"+
			" want nil", len(events))
	}

	events := sink.Events()
	if len(events) == 0 {
		t.Fatal("the supplied sink received nothing")
	}
	validateRecording(t, events)
	if events[0].Type != trace.TypeRunStart || events[len(events)-1].Type != trace.TypeRunEnd {
		t.Fatalf("the sink received a stream from %s to %s, want run-start to run-end",
			events[0].Type, events[len(events)-1].Type)
	}
	command := eventAt(t, events, listed.TraceSeq)
	if command.Type != trace.TypeExec || command.Exec.Kind != trace.ExecKindWorkspaceExec {
		t.Errorf("CommandResult.TraceSeq points at %+v, want a workspace-exec", command)
	}
	// The sink belongs to the caller, so nothing closed it: a caller that tees
	// one recording into two workspaces would otherwise lose the second.
	if err := sink.Emit(trace.Event{Type: trace.TypeNote}); err != nil {
		t.Errorf("the workspace closed the caller's sink: %v", err)
	}
}

// TestAPreparedSessionRecordsSnapshotPrepareBuildsExecAttemptsAndProbePasses is
// the whole timeline of a prepared session, checked against the answers the
// same session gave its caller.
//
// Every claim here is a join a consumer writes: a mutant result and the
// `mutant-exec` that explains it, a probe result and the `probe-exec` that
// explains it, an overlay manifest and the file it names. What makes them worth
// stating is that each one has two sources — the value returned and the event
// recorded — and a recording that disagreed with the result it came from would
// be worse than no recording at all.
func TestAPreparedSessionRecordsSnapshotPrepareBuildsExecAttemptsAndProbePasses(t *testing.T) {
	prepared := probeable(t)
	width := mutantkit.APIByRule(t, prepared.catalog, widthRule)

	killed, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  width.ID,
		Package: probeableModule,
		Args:    []string{"-test.run=^TestWidth$"},
	})
	if err != nil {
		t.Fatalf("executing %s: %v", width.DisplayID, err)
	}
	if killed.Outcome != gomutants.OutcomeKilled {
		t.Fatalf("%s = %s, want a kill:\n%s", width.DisplayID, killed.Outcome, killed.OutputTail)
	}
	probe := probeOf(t, prepared.session, gomutants.ProbeRequest{Package: probeableModule})

	events := recordingOf(t, prepared.workspace)
	validateRecording(t, events)

	// Two trees were frozen and the recording says which was which. A session
	// prepared with a probe tree copies the module twice, and a reader looking
	// at a run that took twice as long as expected is looking for exactly this.
	var snapshots []string
	for _, event := range events {
		if event.Type == trace.TypeSnapshot {
			snapshots = append(snapshots, event.Snapshot.Kind)
		}
	}
	if want := []string{trace.SnapshotKindWorkspace, trace.SnapshotKindProbe}; !slices.Equal(snapshots, want) {
		t.Errorf("the recording holds the snapshots %v, want %v", snapshots, want)
	}

	// The preparation timeline, event for event with the one the callback saw.
	// Two audiences, one sequence: a consumer that watched the preparation live
	// and one that reads the recording afterwards must not be able to tell two
	// different stories about it.
	var recorded []*trace.PrepareRecord
	for _, event := range events {
		if event.Type == trace.TypePrepare {
			recorded = append(recorded, event.Prepare)
		}
	}
	if len(recorded) != len(prepared.events) {
		t.Fatalf("the recording holds %d prepare events and the callback saw %d",
			len(recorded), len(prepared.events))
	}
	if len(recorded) != 2*len(gomutants.KnownPreparePhases()) {
		t.Errorf("the recording holds %d prepare events, want a start and a finish for each of"+
			" the %d phases", len(recorded), len(gomutants.KnownPreparePhases()))
	}
	for i, event := range prepared.events {
		if recorded[i].Phase != string(event.Phase) || recorded[i].State != string(event.State) ||
			recorded[i].Result != string(event.Result) {
			t.Errorf("prepare event %d: the recording says %+v and the callback %+v", i, *recorded[i], event)
		}
	}

	// Every subprocess the session started, labelled. An unlabelled command is
	// a recording that does not validate, so what this adds is that the labels
	// are on the commands they name.
	kinds := execKinds(events)
	for _, kind := range []string{
		trace.ExecKindGoVersion,
		trace.ExecKindValidateBuild,
		trace.ExecKindGoList,
		trace.ExecKindGoTestC,
		trace.ExecKindVerify,
		trace.ExecKindMutantRun,
		trace.ExecKindProbeRun,
	} {
		if !slices.Contains(kinds, kind) {
			t.Errorf("no command is recorded as %q; the recording holds %v", kind, kinds)
		}
	}

	attempt := eventAt(t, events, killed.TraceSeq)
	if attempt.Type != trace.TypeMutantExec {
		t.Fatalf("MutantResult.TraceSeq points at a %s, want a %s", attempt.Type, trace.TypeMutantExec)
	}
	mutant := attempt.Mutant
	switch {
	case mutant.ID != killed.ID:
		t.Errorf("the attempt names %s and the result %s", mutant.ID, killed.ID)
	case mutant.DisplayID != killed.DisplayID:
		t.Errorf("the attempt's display id is %s and the result's %s", mutant.DisplayID, killed.DisplayID)
	case mutant.Outcome != string(killed.Outcome):
		t.Errorf("the attempt says %q and the result %q", mutant.Outcome, killed.Outcome)
	case mutant.KilledBy != killed.KilledBy:
		t.Errorf("the attempt was killed by %q and the result by %q", mutant.KilledBy, killed.KilledBy)
	case !slices.Equal(mutant.Binaries, killed.Binaries):
		t.Errorf("the attempt ran %v and the result reports %v", mutant.Binaries, killed.Binaries)
	}
	if mutant.Attempt != 1 || mutant.Worker != 0 {
		t.Errorf("a session execution is recorded as attempt %d on worker %d, want 1 and 0:"+
			" one attempt, on the caller's goroutine", mutant.Attempt, mutant.Worker)
	}
	if len(killed.Binaries) == 0 {
		t.Error("the result names no test binaries, so it says nothing about what it was measured against")
	}
	// The join goes one level further down: an attempt names the executions
	// underneath it, and through them their preserved output.
	for _, seq := range mutant.ExecSeqs {
		if under := eventAt(t, events, seq); under.Type != trace.TypeExec ||
			under.Exec.Kind != trace.ExecKindMutantRun {
			t.Errorf("the attempt's exec_seqs point at %+v, want a mutant-run", under)
		}
	}

	pass := eventAt(t, events, probe.TraceSeq)
	if pass.Type != trace.TypeProbeExec {
		t.Fatalf("ProbeResult.TraceSeq points at a %s, want a %s", pass.Type, trace.TypeProbeExec)
	}
	if pass.Probe.Outcome != string(probe.Outcome) {
		t.Errorf("the pass says %q and the result %q", pass.Probe.Outcome, probe.Outcome)
	}
	if !slices.Equal(pass.Probe.Binaries, probe.Binaries) {
		t.Errorf("the pass ran %v and the result reports %v", pass.Probe.Binaries, probe.Binaries)
	}
	// By identity and never by index: an index means nothing outside this
	// session, and the identity is what the report, the cache and a consumer's
	// own recording all key on.
	//
	// A subset rather than an equality, and in one direction only. The event is
	// the *raw* set the probe runtime recorded; the result is that set minus the
	// mutants validation rejected, because an infection fact about a mutant
	// nothing will execute licenses no skipping. So everything the caller was
	// given must be in the recording — a recording that lost one of them would
	// be an account of a pass that did not happen — and everything the
	// recording holds and the caller was not given must be a rejected mutant.
	infected := infectedIDs(t, prepared, probe)
	rejected := make(map[string]bool, len(prepared.catalog.Rejections))
	for _, rejection := range prepared.catalog.Rejections {
		rejected[rejection.ID] = true
	}
	for _, id := range infected {
		if !slices.Contains(pass.Probe.Infected, id) {
			t.Errorf("the result names %s infected and the recording does not: %v", id, pass.Probe.Infected)
		}
	}
	for _, id := range pass.Probe.Infected {
		if slices.Contains(infected, id) || rejected[id] {
			continue
		}
		t.Errorf("the recording names %s infected, the result does not, and validation did not"+
			" reject it; the only difference between the two sets is a rejection", id)
	}
	if pass.Probe.Package != probeableModule {
		t.Errorf("the pass is recorded as measuring %q, want %q", pass.Probe.Package, probeableModule)
	}

	// The two paths a consumer needs to reproduce any of this by hand.
	for kind, manifest := range map[string]string{
		trace.ArtifactOverlayManifest:      prepared.session.OverlayManifest(),
		trace.ArtifactProbeOverlayManifest: prepared.session.ProbeOverlayManifest(),
	} {
		if manifest == "" {
			t.Errorf("the session names no %s", kind)
			continue
		}
		if paths := artifactsOfKind(events, kind); !slices.Equal(paths, []string{manifest}) {
			t.Errorf("the recording names the %s %v and the session %q", kind, paths, manifest)
		}
		if _, err := os.Stat(manifest); err != nil {
			t.Errorf("the %s the session names is not there: %v", kind, err)
		}
	}
}

// hostileSink is a caller's sink that cannot keep an event: half of them it
// refuses, and the other half it panics on.
//
// Both, because they are two different failures with one required outcome. A
// Sink is an interface and an embedder's implementation of it is ordinary Go
// code, which panics; without the recorder's recover that panic unwinds through
// whichever goroutine was recording and takes the process with it. A diagnostic
// that can kill the run it is a diagnostic of inverts the point of having one.
type hostileSink struct{ mu sync.Mutex }

func (sink *hostileSink) Emit(event trace.Event) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if event.Seq%2 == 0 {
		panic("the consumer's sink panicked on event " + event.Type)
	}
	return errors.New("the consumer's sink refused event " + event.Type)
}

func (sink *hostileSink) Close() error { return errors.New("the consumer's sink refused to close") }

// hostileFixture is one probeable session prepared exactly as the shared one is
// and recording into a sink that fails every event.
//
// It is prepared once, like the sessions api_integration_test.go shares, and
// registered with them so that TestMain releases it: preparing a session is the
// expensive thing this file does, and two tests ask the same question of it.
var hostileFixture = sync.OnceValue(func() *preparedFixture {
	return prepareFixtureWith("probeable",
		gomutants.OpenOptions{Trace: &hostileSink{}},
		gomutants.PrepareOptions{
			Probe:              true,
			ProbeCoverPackages: []string{probeableModule + "/..."},
			MutantTimeout:      30 * time.Second,
		})
})

// hostile returns that session, failing the calling test if preparing it did
// not work — which is itself the first half of the claim, since a preparation
// that a broken sink stopped would not get this far.
func hostile(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := hostileFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the probeable fixture against a sink that fails every event: %v", prepared.err)
	}
	return prepared
}

// TestATraceSinkThatFailsChangesNoCatalogDigestOrResult is the fail-open
// promise, stated over two preparations of one tree.
//
// A trace is never evidence. A sink that refuses every event and panics on the
// rest is the worst case an embedder can hand over, and what it may cost is the
// recording and nothing else: the same mutants are catalogued, the same
// preparation is identified, the same mutant is killed by the same package, and
// the same target infects the same sites. A diagnostic that made the tool less
// reliable than it was without it would invert the point of the feature.
func TestATraceSinkThatFailsChangesNoCatalogDigestOrResult(t *testing.T) {
	sound := probeable(t)
	broken := hostile(t)

	if got, want := broken.catalog.Digest, sound.catalog.Digest; got != want {
		t.Errorf("the session recording into a broken sink catalogued %s and the ring-recorded one %s",
			got, want)
	}
	if got, want := broken.catalog.PreparedDigest, sound.catalog.PreparedDigest; got != want {
		t.Errorf("PreparedDigest = %s against a broken sink and %s against the ring; a consumer"+
			" keyed on it would re-measure everything the day it handed over a sink", got, want)
	}

	// A known kill, run on both, compared field for field rather than on the
	// two fields somebody remembered to check: what is under test is that
	// *nothing* moved, and a comparison that named the fields would go on
	// passing the day a new one started depending on the sink.
	killed := [2]gomutants.MutantResult{}
	for i, session := range [2]*preparedFixture{sound, broken} {
		mutant := mutantkit.APIByRule(t, session.catalog, widthRule)
		result, err := session.session.Exec(t.Context(), gomutants.ExecRequest{
			Mutant:  mutant.ID,
			Package: probeableModule,
			Args:    []string{"-test.run=^TestWidth$"},
		})
		if err != nil {
			t.Fatalf("executing %s: %v", mutant.DisplayID, err)
		}
		if result.Outcome != gomutants.OutcomeKilled {
			t.Fatalf("%s = %s, want a kill:\n%s", mutant.DisplayID, result.Outcome, result.OutputTail)
		}
		killed[i] = steadyMutantResult(result)
	}
	if diff := cmp.Diff(killed[0], killed[1],
		cmpopts.IgnoreFields(gomutants.MutantResult{}, "Duration", "TotalBytes", "TraceSeq")); diff != "" {
		t.Errorf("the kill differs between a ring and a broken sink (-ring +sink):\n%s", diff)
	}

	// And a probe pass, the same way. The infection set is compared by identity
	// rather than by index, because that is the comparison that would still
	// hold if the two catalogues were merely equivalent rather than equal.
	passes := [2]gomutants.ProbeResult{}
	infected := [2][]string{}
	for i, session := range [2]*preparedFixture{sound, broken} {
		result := probeOf(t, session.session, gomutants.ProbeRequest{Package: probeableModule})
		infected[i] = infectedIDs(t, session, result)
		passes[i] = steadyProbeResult(result)
	}
	if diff := cmp.Diff(passes[0], passes[1],
		cmpopts.IgnoreFields(gomutants.ProbeResult{}, "Duration", "TotalBytes", "TraceSeq")); diff != "" {
		t.Errorf("the probe pass differs between a ring and a broken sink (-ring +sink):\n%s", diff)
	}
	if !slices.Equal(infected[0], infected[1]) {
		t.Errorf("the pass infected %v against the ring and %v against a broken sink",
			infected[0], infected[1])
	}
}

// elapsed matches the `(1.23s)` a Go test binary stamps on every result line.
var elapsed = regexp.MustCompile(`\(\d+\.\d+s\)`)

// steadyMutantResult is one result with the two things that cannot be equal
// between two runs of one test flattened: the wall-clock time the process took,
// and the wall-clock time the test binary printed into its own output.
//
// Only those. Everything else — the identities, the outcome, the deciding
// package, the binaries, the truncation flag, the captured bytes themselves —
// is compared as it came back, because a trace option that moved any of them
// would be the bug this test exists to find. `Duration`, `TotalBytes` and
// `TraceSeq` are ignored by the comparison rather than flattened here:
// TotalBytes counts the unflattened bytes, and a sequence number is the one
// field that is *meant* to differ.
func steadyMutantResult(result gomutants.MutantResult) gomutants.MutantResult {
	result.Output = elapsed.ReplaceAll(result.Output, []byte("(0.00s)"))
	result.OutputTail = elapsed.ReplaceAllString(result.OutputTail, "(0.00s)")
	return result
}

// steadyProbeResult is [steadyMutantResult] for a probe pass.
func steadyProbeResult(result gomutants.ProbeResult) gomutants.ProbeResult {
	result.Output = elapsed.ReplaceAll(result.Output, []byte("(0.00s)"))
	return result
}

// infectedIDs is one measured pass's infection set by mutant identity.
func infectedIDs(t *testing.T, prepared *preparedFixture, result gomutants.ProbeResult) []string {
	t.Helper()
	ids := make([]string, 0, len(result.Infected))
	for _, index := range result.Infected {
		ids = append(ids, prepared.catalog.Mutants[index].ID)
	}
	return ids
}

// TestTraceOptionsTakeNoPartInThePreparedDigest is the narrow half of the same
// claim, stated on its own because it is the one a consumer keys a store on.
//
// Two sessions prepared over one tree are interchangeable, and handing one of
// them a sink does not make them anything else: a recording is an account of
// how a preparation happened and not a fact about what it produced. A digest
// that moved with a trace option would be a cache that never hits for anybody
// who turned tracing on.
func TestTraceOptionsTakeNoPartInThePreparedDigest(t *testing.T) {
	sound := probeable(t)
	broken := hostile(t)

	if got, want := broken.catalog.WorkspaceDigest, sound.catalog.WorkspaceDigest; got != want {
		t.Fatalf("the two preparations froze different trees (%s and %s); the claim below is"+
			" about one tree prepared twice", got, want)
	}
	if got, want := broken.catalog.PreparedDigest, sound.catalog.PreparedDigest; got != want {
		t.Errorf("PreparedDigest = %s with a sink and %s without one", got, want)
	}
	for i, mutant := range sound.catalog.Mutants {
		other := broken.catalog.Mutants[i]
		if mutant.ID != other.ID || mutant.Accepted != other.Accepted || mutant.Probed != other.Probed {
			t.Errorf("mutant %d is %+v without a sink and %+v with one", i, mutant, other)
		}
	}
}

// TestKeepTempKeepsTheExecScratchAndNamesItInPreserved is the escape hatch,
// extended to the one directory it used to miss.
//
// KeepTemp exists to answer the question a removed directory cannot — what did
// the tree this mutant ran in actually look like — and the per-execution
// scratch is half of that tree: it is where the target's TMPDIR pointed, where
// a fuzz cache lived, and where anything the test wrote went. A keep that left
// the snapshot and removed that was answering half the question.
func TestKeepTempKeepsTheExecScratchAndNamesItInPreserved(t *testing.T) {
	kept := keepTempWorkspace(t, 3)

	preserved := kept.workspace.Preserved()
	if len(preserved) == 0 {
		t.Fatal("Preserved() names nothing after a KeepTemp close")
	}
	if !slices.IsSorted(preserved) {
		t.Errorf("Preserved() = %v, want path order", preserved)
	}
	events := recordingOf(t, kept.workspace)
	validateRecording(t, events)

	execScratch := artifactsOfKind(events, trace.ArtifactKeptExecScratch)
	// One per workspace command and one per session execution.
	if want := 1 + len(kept.executions); len(execScratch) != want {
		t.Fatalf("the recording names %d directories as kept execution scratch, want %d",
			len(execScratch), want)
	}
	for _, directory := range execScratch {
		if !strings.HasPrefix(filepath.Base(directory), "exec-") {
			t.Errorf("%q is recorded as kept execution scratch and is not one", directory)
		}
		if !slices.Contains(preserved, directory) {
			t.Errorf("the recording kept %q and Preserved() does not name it: %v", directory, preserved)
		}
		if _, err := os.Stat(directory); err != nil {
			t.Errorf("KeepTemp did not keep %s: %v", directory, err)
		}
	}

	// The durable directories are named too, under kinds of their own, so a
	// reader of the recording can tell a snapshot from the scratch beside it.
	for _, kind := range []string{trace.ArtifactKeptSnapshot, trace.ArtifactKeptScratch} {
		paths := artifactsOfKind(events, kind)
		if len(paths) != 1 {
			t.Errorf("the recording names %v as %s, want exactly one directory", paths, kind)
			continue
		}
		if !slices.Contains(preserved, paths[0]) {
			t.Errorf("the recording kept %q as %s and Preserved() does not name it", paths[0], kind)
		}
		if marker, err := tempowner.ReadMarker(paths[0]); err != nil || !marker.Kept {
			t.Errorf("the durable directory %s was kept without being marked kept (%v): the next"+
				" run's sweep would collect it as an orphan", paths[0], err)
		}
	}
	// A session prepared without a probe tree kept none, and says nothing about
	// one: an artifact for a directory that was never made would be a path a
	// reader would go looking for.
	if paths := artifactsOfKind(events, trace.ArtifactKeptProbeTree); len(paths) != 0 {
		t.Errorf("a session prepared without a probe tree kept %v", paths)
	}
}

// TestAKeptExecScratchHoldsOnlyWhatTheTargetWrote is why a per-call directory
// carries no lock and no marker of go-mutants' own.
//
// That directory *is* the child's TMPDIR, and it is kept so that somebody can
// look at what the target left in it. Two files of the engine's in there would
// be two files in the very tree the keep exists to show them — and they would
// buy nothing, because a sweep looks at the direct children of the temporary
// parent and a per-call scratch is nested inside the workspace scratch, which
// does carry a marker. It survives because its parent does.
func TestAKeptExecScratchHoldsOnlyWhatTheTargetWrote(t *testing.T) {
	kept := keepTempWorkspace(t, 1)
	events := recordingOf(t, kept.workspace)

	scratch := artifactsOfKind(events, trace.ArtifactKeptExecScratch)
	if len(scratch) == 0 {
		t.Fatal("no execution scratch was kept, so there is nothing to look inside")
	}
	for _, directory := range scratch {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Errorf("reading the kept scratch %s: %v", directory, err)
			continue
		}
		for _, entry := range entries {
			switch entry.Name() {
			case tempowner.LockName, tempowner.MarkerName:
				t.Errorf("the kept scratch %s holds %s, which go-mutants wrote into the directory"+
					" a developer kept in order to read", directory, entry.Name())
			}
		}
	}
	// And the parent it survives by is marked, which is the whole of why the
	// nested one does not have to be.
	workspaceScratch := artifactsOfKind(events, trace.ArtifactKeptScratch)
	if len(workspaceScratch) != 1 {
		t.Fatalf("the recording names %v as the kept workspace scratch, want one", workspaceScratch)
	}
	for _, directory := range scratch {
		if !strings.HasPrefix(directory, workspaceScratch[0]+string(filepath.Separator)) {
			t.Errorf("the kept scratch %s is not inside the marked workspace scratch %s, so nothing"+
				" protects it from a sweep", directory, workspaceScratch[0])
		}
	}
}

// TestAKeptExecScratchIsRecordedBesideItsExecution is where a kept directory
// goes in the stream, and why it matters that it is not at the end.
//
// A recording is bounded. A Close that reported ten thousand kept directories
// in one burst would push the run-start, the whole preparation timeline and
// every mutant attempt out of the ring in the last moment of the workspace's
// life — so every TraceSeq a caller was handed would name an event that is no
// longer there. Recording each one where it is kept makes eviction cost
// housekeeping instead, and puts the directory beside the execution it explains.
func TestAKeptExecScratchIsRecordedBesideItsExecution(t *testing.T) {
	kept := keepTempWorkspace(t, 3)
	events := recordingOf(t, kept.workspace)

	// Exactly one per execution, and each between its own attempt and the next.
	for i, result := range kept.executions {
		attempt := eventAt(t, events, result.TraceSeq)
		next := int64(0)
		if i+1 < len(kept.executions) {
			next = kept.executions[i+1].TraceSeq
		}
		found := 0
		for _, event := range events {
			if event.Type != trace.TypeArtifact || event.Artifact.Kind != trace.ArtifactKeptExecScratch {
				continue
			}
			if event.Seq > attempt.Seq && (next == 0 || event.Seq < next) {
				found++
			}
		}
		if found != 1 {
			t.Errorf("execution %d recorded %d kept-exec-scratch artifacts between its own"+
				" mutant-exec at seq %d and the next at %d, want exactly one",
				i, found, attempt.Seq, next)
		}
	}

	// And Close added the durable ones alone. The last execution's own scratch
	// is recorded after its attempt, so the boundary is that artifact rather
	// than the attempt: everything past it belongs to Close.
	last := int64(0)
	for _, event := range events {
		if event.Type == trace.TypeArtifact && event.Artifact.Kind == trace.ArtifactKeptExecScratch {
			last = max(last, event.Seq)
		}
	}
	var afterwards []string
	for _, event := range events {
		if event.Seq > last && event.Type == trace.TypeArtifact {
			afterwards = append(afterwards, event.Artifact.Kind)
		}
	}
	slices.Sort(afterwards)
	want := []string{trace.ArtifactKeptScratch, trace.ArtifactKeptSnapshot}
	slices.Sort(want)
	if !slices.Equal(afterwards, want) {
		t.Errorf("Close recorded the artifacts %v, want only the durable %v: a per-call"+
			" directory recorded again here would be in the stream twice", afterwards, want)
	}
}

// A keptWorkspace is one closed KeepTemp workspace and the executions it made,
// which three tests ask three different questions of.
type keptWorkspace struct {
	workspace  *gomutants.Workspace
	executions []gomutants.MutantResult
}

// keepTempWorkspace prepares a KeepTemp workspace over fixtures/probeable, runs
// one workspace command and count executions against it, and closes it.
func keepTempWorkspace(t *testing.T, count int) keptWorkspace {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "probeable")
	if err := copyFixtureTree("probeable", root); err != nil {
		t.Fatal(err)
	}
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		KeepTemp:      true,
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	if _, execErr := workspace.Exec(t.Context(),
		gomutants.Command{Argv: []string{"go", "env", "GOVERSION"}}); execErr != nil {
		t.Fatalf("running a workspace command: %v", execErr)
	}
	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		SkipVerify:    true,
		MutantTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("preparing session: %v", err)
	}
	mutant := mutantkit.APIByRule(t, session.Catalog(), widthRule)
	kept := keptWorkspace{workspace: workspace}
	for range count {
		result, execErr := session.Exec(t.Context(), gomutants.ExecRequest{
			Mutant:  mutant.ID,
			Package: probeableModule,
			Args:    []string{"-test.run=^TestWidth$"},
		})
		if execErr != nil {
			t.Fatalf("executing %s: %v", mutant.DisplayID, execErr)
		}
		kept.executions = append(kept.executions, result)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("closing workspace: %v", err)
	}
	return kept
}

// TestAFailedOpenEndsTheRecordingItWasGiven is the one recording a caller can
// still read when nothing was returned.
//
// Open hands back a workspace or an error, and there is nowhere on an error to
// hang a ring — so a failed Open's account exists only for a caller that
// supplied a sink. It is a complete account: the recording ends with a run-end
// of verdict "failed" carrying the error, rather than stopping mid-sentence and
// leaving a reader to wonder whether the process died.
func TestAFailedOpenEndsTheRecordingItWasGiven(t *testing.T) {
	sink := trace.NewMemorySink(0)
	workspace, err := gomutants.Open(t.Context(), filepath.Join(t.TempDir(), "not-a-tree"),
		gomutants.OpenOptions{TempDirectory: t.TempDir(), Trace: sink})
	if err == nil {
		t.Fatalf("opening a tree that is not there succeeded: %v", workspace.Close())
	}
	if workspace != nil {
		t.Fatal("a failed Open returned a workspace")
	}

	events := sink.Events()
	validateRecording(t, events)
	if len(events) == 0 || events[0].Type != trace.TypeRunStart {
		t.Fatalf("the sink received %+v, want a recording that opens with a run-start", events)
	}
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd {
		t.Fatalf("the recording of a failed Open ends with a %s, want a run-end", last.Type)
	}
	if last.Run.Verdict != "failed" {
		t.Errorf("run-end verdict = %q, want %q", last.Run.Verdict, "failed")
	}
	if last.Run.Error == "" || !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("the run-end carries %q and Open returned %v; the recording is meant to say"+
			" what stopped it", last.Run.Error, err)
	}
	// The tree it was about, and the toolchain probe it got as far as, are
	// there too: a failed Open is exactly the one somebody reads a recording of.
	if events[0].Start.Kind != trace.StartKindWorkspace {
		t.Errorf("run-start kind = %q, want %q", events[0].Start.Kind, trace.StartKindWorkspace)
	}
}

// TestPrepareFailedNamesThePhaseAndNotARefusal is what a `prepare-failed` note
// is for, and what it is not for.
//
// A note is what the run could not do. A workspace that is closed, one that was
// already prepared, an option this engine does not accept: none of those
// started a preparation, so a note about one would name no phase and describe
// nothing that happened — it would be an event a reader has to learn to ignore.
// A preparation that got as far as running something and failed is the opposite
// case, and the phase is the first thing anybody wants from it.
func TestPrepareFailedNamesThePhaseAndNotARefusal(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "probeable")
	if err := copyFixtureTree("probeable", root); err != nil {
		t.Fatal(err)
	}
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{TempDirectory: parent})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := workspace.Close(); closeErr != nil {
			t.Errorf("closing workspace: %v", closeErr)
		}
	})

	// A verification that cannot pass, which is a preparation failure in a
	// named phase rather than a refusal of the request.
	_, err = workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Verify:        gomutants.Command{Argv: []string{"go", "list", "./definitely-not-a-package"}},
		MutantTimeout: 30 * time.Second,
	})
	if err == nil {
		t.Fatal("a verification that lists a package that is not there succeeded")
	}
	notes := notesOfKind(recordingOf(t, workspace), trace.NotePrepareFailed)
	if len(notes) != 1 {
		t.Fatalf("a failed preparation recorded %d prepare-failed notes, want one: %v", len(notes), notes)
	}
	if want := string(gomutants.PreparePhaseVerification) + ": "; !strings.HasPrefix(notes[0], want) {
		t.Errorf("the note says %q, want it to begin with the phase %q", notes[0], want)
	}
	if !strings.Contains(notes[0], "verification") {
		t.Errorf("the note says %q and names neither the phase nor the failure", notes[0])
	}

	// A second Prepare is a refusal. Nothing was prepared, so nothing failed.
	if _, again := workspace.Prepare(t.Context(), gomutants.PrepareOptions{}); again == nil {
		t.Fatal("a workspace accepted a second Prepare")
	}
	if after := notesOfKind(recordingOf(t, workspace), trace.NotePrepareFailed); len(after) != 1 {
		t.Errorf("a refused Prepare recorded a note: %v", after)
	}
}

// notesOfKind is the detail of every note of one kind in a recording.
func notesOfKind(events []trace.Event, kind string) []string {
	var details []string
	for _, event := range events {
		if event.Type == trace.TypeNote && event.Note.Kind == kind {
			details = append(details, event.Note.Detail)
		}
	}
	return details
}

// TestASuppliedSinkReceivesExactlyWhatTheRingWould is the claim that makes the
// two branches one feature.
//
// A sink and the default ring are meant to be one recording written to two
// places, and the failure that would be invisible without this is a call site
// that records into one and not the other: every existing test reads whichever
// destination it configured, so a sweep event recorded only into the ring would
// pass both tiers. Two workspaces over one fixture, doing the same things, must
// produce the same recording — the same event types, the same command kinds,
// the same artifact kinds — and the results they return must agree field for
// field.
func TestASuppliedSinkReceivesExactlyWhatTheRingWould(t *testing.T) {
	sink := trace.NewMemorySink(0)
	supplied, suppliedResult := recordedWorkspace(t, sink)
	ringed, ringedResult := recordedWorkspace(t, nil)

	if diff := cmp.Diff(ringedResult, suppliedResult,
		cmpopts.IgnoreFields(gomutants.CommandResult{}, "Duration", "TraceSeq")); diff != "" {
		t.Errorf("the two workspaces returned different command results (-ring +sink):\n%s", diff)
	}
	if got := ringed.Recording(); got == nil {
		t.Fatal("the ring-recorded workspace has no recording")
	}
	if got := supplied.Recording(); got != nil {
		t.Fatalf("the sink-recorded workspace also filled a ring: %d events", len(got))
	}

	got := recordingShape(sink.Events())
	want := recordingShape(ringed.Recording())
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("a supplied sink received a different recording from the ring (-ring +sink):\n%s", diff)
	}
}

// recordingShape is what a recording holds, counted: one entry per event type,
// per command kind and per artifact kind.
//
// The shape rather than the events themselves, because two workspaces over one
// fixture differ in every path, every duration and every sequence number — and
// none of that is what "the sink gets what the ring gets" is about.
func recordingShape(events []trace.Event) map[string]int {
	shape := map[string]int{}
	for _, event := range events {
		shape[event.Type]++
		switch event.Type {
		case trace.TypeExec:
			shape["exec/"+event.Exec.Kind]++
		case trace.TypeArtifact:
			shape["artifact/"+event.Artifact.Kind]++
		case trace.TypeSnapshot:
			shape["snapshot/"+event.Snapshot.Kind]++
		}
	}
	return shape
}

// recordedWorkspace opens one workspace over fixtures/simple, runs one command
// against it and closes it.
func recordedWorkspace(t *testing.T, sink trace.Sink) (*gomutants.Workspace, gomutants.CommandResult) {
	t.Helper()
	workspace, err := gomutants.Open(t.Context(), copyFixture(t, "simple"), gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Trace:         sink,
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	result, err := workspace.Exec(t.Context(), gomutants.Command{Argv: []string{"go", "env", "GOVERSION"}})
	if err != nil {
		t.Fatalf("running a workspace command: %v", err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("closing workspace: %v", err)
	}
	return workspace, result
}

// TestEveryLineOfAWorkspaceRecordingValidates is the fail-closed half of a
// fail-open feature.
//
// Recording is best effort about *loss*, and about nothing else. A line that
// went into a recording is a line the published contract describes, because a
// consumer decoding one strictly — which trace.Read does, and which the schema
// exists to let anybody else do — must not be handed a document outside it. The
// richest recording this suite makes is the shared prepared session's, which is
// why the claim is stated over that one: it holds every payload the API can
// produce, from the sweep before the snapshot to the probe pass at the end.
func TestEveryLineOfAWorkspaceRecordingValidates(t *testing.T) {
	prepared := probeable(t)
	events := recordingOf(t, prepared.workspace)
	validateRecording(t, events)

	// Otherwise the walk above could hold vacuously over a recording that lost
	// everything interesting, and go on passing the day it did.
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.Type] = true
	}
	for _, want := range []string{
		trace.TypeRunStart,
		trace.TypePrepare,
		trace.TypeExec,
		trace.TypeSnapshot,
		trace.TypeSweep,
		trace.TypeArtifact,
		trace.TypeValidate,
	} {
		if !seen[want] {
			t.Errorf("the recording holds no %s event, so nothing validated one", want)
		}
	}
}
