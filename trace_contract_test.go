// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/trace"
)

// TestOpenOptionsTraceIsPartOfTheContract pins the recording surface by name
// and by type.
//
// A consumer joining its own recording to the engine's writes against exactly
// these five things: the sink it hands over, the ring it reads back when it
// hands over none, the two overlay manifests that let it reproduce a run by
// hand, and the sequence numbers on the results. A rename is a compile error
// for that consumer, which is what this is for;
// [TestExternalModuleCompilesAgainstTheEngineAPI] states the same claim from
// outside the module, where it is checked against a build that cannot see any
// of this package's own test helpers.
func TestOpenOptionsTraceIsPartOfTheContract(t *testing.T) {
	t.Parallel()

	pinType[trace.Sink](gomutants.OpenOptions{}.Trace)
	pinType[func(*gomutants.Workspace) []trace.Event]((*gomutants.Workspace).Recording)
	pinType[func(*gomutants.Session) string]((*gomutants.Session).OverlayManifest)
	pinType[func(*gomutants.Session) string]((*gomutants.Session).ProbeOverlayManifest)

	// A sink of the consumer's own is the point of the field being an interface
	// rather than a directory: a recording goes wherever the embedder already
	// sends its own.
	var sink trace.Sink = trace.NewMemorySink(trace.DefaultRingCapacity)
	options := gomutants.OpenOptions{Trace: sink}
	if options.Trace == nil {
		t.Error("OpenOptions.Trace did not keep the sink it was given")
	}

	// Nil is the ring rather than silence, and that is a documented default a
	// consumer relies on: it is what makes "read the recording of the run that
	// went wrong" possible without having asked for one in advance.
	if (gomutants.OpenOptions{}).Trace != nil {
		t.Error("the zero OpenOptions carries a sink")
	}
}

// pinType states that a value has exactly the type named here, and nothing
// weaker.
//
// It is a call rather than a typed variable declaration because the two are not
// the same claim to a reader. A declaration whose type could be inferred reads
// as noise everywhere else in a Go file, and a tool that offers to remove it is
// right everywhere else — while here the type *is* the assertion, and removing
// it would delete the test while leaving something that still compiles.
func pinType[T any](T) {}

// workspaceArtifactKinds are the artifact kinds a workspace's recording can
// carry, and workspaceNoteKinds the notes.
//
// They are written out rather than derived, because that is the point: the list
// is what this build records, and a kind added to the code without a paragraph
// explaining it is a label a reader of a recording cannot act on.
var (
	workspaceArtifactKinds = []string{
		trace.ArtifactOverlayManifest,
		trace.ArtifactProbeOverlayManifest,
		trace.ArtifactKeptSnapshot,
		trace.ArtifactKeptScratch,
		trace.ArtifactKeptProbeTree,
		trace.ArtifactKeptExecScratch,
	}
	workspaceNoteKinds = []string{trace.NotePrepareFailed}
)

// TestEveryNewArtifactAndNoteKindIsInTheSchemaAndTheDocs keeps the vocabulary,
// the contract and the page that explains them from drifting apart.
//
// An artifact or a note is the one place a recording says what a run wrote or
// could not do, and its `kind` is what a reader branches on. A kind that exists
// in the code and nowhere else is a string somebody has to guess at; one the
// schema constrains and the docs do not is a validation failure with no
// explanation attached. So both directions are checked here, against the
// published files rather than against a copy of them.
func TestEveryNewArtifactAndNoteKindIsInTheSchemaAndTheDocs(t *testing.T) {
	t.Parallel()

	page, err := os.ReadFile("docs/trace-v1.md")
	if err != nil {
		t.Fatal(err)
	}
	document := map[string]any{}
	if err := json.Unmarshal(trace.JSONSchema(), &document); err != nil {
		t.Fatal(err)
	}

	for payload, kinds := range map[string][]string{
		"artifact": workspaceArtifactKinds,
		"note":     workspaceNoteKinds,
	} {
		enumerated := schemaKindEnum(t, document, payload)
		for _, kind := range kinds {
			if !strings.Contains(string(page), "`"+kind+"`") {
				t.Errorf("docs/trace-v1.md never mentions the %s kind %q, so a reader of a"+
					" recording carrying it has nothing to look it up in", payload, kind)
			}
			// The schema deliberately leaves both kinds open — a recording made
			// by a newer build stays readable by an older reader — so the claim
			// is conditional. The day one of them is closed, this is what says
			// so rather than a run failing validation.
			if enumerated != nil && !slices.Contains(enumerated, kind) {
				t.Errorf("the schema enumerates %s kinds and %q is not among them: %v",
					payload, kind, enumerated)
			}
		}
	}
}

// schemaKindEnum is the enumeration the schema constrains one payload's `kind`
// with, or nil when it constrains it only as a non-empty string.
func schemaKindEnum(t *testing.T, document map[string]any, payload string) []string {
	t.Helper()
	defs, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("the trace schema has no $defs")
	}
	definition, ok := defs[payload].(map[string]any)
	if !ok {
		t.Fatalf("the trace schema defines no %q payload", payload)
	}
	properties, ok := definition["properties"].(map[string]any)
	if !ok {
		t.Fatalf("the %q payload has no properties", payload)
	}
	kind, ok := properties["kind"].(map[string]any)
	if !ok {
		t.Fatalf("the %q payload has no kind", payload)
	}
	values, ok := kind["enum"].([]any)
	if !ok {
		return nil
	}
	enumerated := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("the %q payload's kind enumerates %v, which is not a string", payload, value)
		}
		enumerated = append(enumerated, text)
	}
	return enumerated
}

// TestConcurrentExecutionsUnderKeepTempRecordAndPreserveEveryScratch is the
// recording under the load it is actually used at.
//
// Three things write to a workspace's recording at once — the executions, the
// probe passes, and the housekeeping each of them does when a keep is in force
// — and a fourth reads it while they do. Every existing concurrency test in
// this suite runs without KeepTemp, so the two structures a keep introduces
// (the kept-directory list and the artifact recorded beside each execution)
// have never been under a race detector at all.
//
// It lives in the untagged tier deliberately: `go test -race .` is the gate
// that would catch a data race here, and a test behind a build tag that gate
// does not set is a test that never runs under it.
func TestConcurrentExecutionsUnderKeepTempRecordAndPreserveEveryScratch(t *testing.T) {
	const workspaceCommands = 8
	const sessionCalls = 6

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

	// A reader for as long as the writers run. Recording() is documented as
	// safe at any point in a workspace's life, and "any point" is exactly the
	// point at which fourteen goroutines are recording into it.
	reading := make(chan struct{})
	read := make(chan int, 1)
	go func() {
		seen := 0
		for {
			select {
			case <-reading:
				read <- seen
				return
			default:
				seen = max(seen, len(workspace.Recording()))
			}
		}
	}()

	var group sync.WaitGroup
	for range workspaceCommands {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, execErr := workspace.Exec(t.Context(),
				gomutants.Command{Argv: []string{"go", "env", "GOVERSION"}}); execErr != nil {
				t.Errorf("running a workspace command: %v", execErr)
			}
		}()
	}
	group.Wait()

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Probe:              true,
		ProbeCoverPackages: []string{probeableModule + "/..."},
		SkipVerify:         true,
		MutantTimeout:      30 * time.Second,
	})
	if err != nil {
		t.Fatalf("preparing session: %v", err)
	}
	mutant := mutantkit.APIByRule(t, session.Catalog(), widthRule)
	for i := range sessionCalls {
		group.Add(1)
		go func() {
			defer group.Done()
			if i%2 == 0 {
				if _, execErr := session.Exec(t.Context(), gomutants.ExecRequest{
					Mutant:  mutant.ID,
					Package: probeableModule,
					Args:    []string{"-test.run=^TestWidth$"},
				}); execErr != nil {
					t.Errorf("executing %s: %v", mutant.DisplayID, execErr)
				}
				return
			}
			if _, probeErr := session.Probe(t.Context(), gomutants.ProbeRequest{
				Package: probeableModule,
				Args:    []string{"-test.run=^TestWidth$"},
			}); probeErr != nil {
				t.Errorf("probing: %v", probeErr)
			}
		}()
	}
	group.Wait()
	close(reading)
	if seen := <-read; seen == 0 {
		t.Error("the concurrent reader never saw an event, so it was not reading a live recording")
	}

	if err := workspace.Close(); err != nil {
		t.Fatalf("closing workspace: %v", err)
	}

	// Every directory that was kept is named once, recorded once, and there.
	preserved := workspace.Preserved()
	var scratch []string
	for _, event := range workspace.Recording() {
		if event.Type == trace.TypeArtifact && event.Artifact.Kind == trace.ArtifactKeptExecScratch {
			scratch = append(scratch, event.Artifact.Path)
		}
	}
	if want := workspaceCommands + sessionCalls; len(scratch) != want {
		t.Errorf("the recording names %d kept execution scratch directories, want one per call (%d)",
			len(scratch), want)
	}
	if len(slices.Compact(slices.Sorted(slices.Values(scratch)))) != len(scratch) {
		t.Errorf("a directory was recorded twice: %v", scratch)
	}
	for _, directory := range scratch {
		if !slices.Contains(preserved, directory) {
			t.Errorf("the recording kept %q and Preserved() does not name it", directory)
		}
		if _, statErr := os.Stat(directory); statErr != nil {
			t.Errorf("KeepTemp did not keep %s: %v", directory, statErr)
		}
	}
	if !slices.IsSorted(preserved) {
		t.Errorf("Preserved() = %v, want path order however the calls interleaved", preserved)
	}
}
