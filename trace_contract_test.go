// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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

func TestOpenOptionsTraceIsPartOfTheContract(t *testing.T) {
	t.Parallel()

	pinType[trace.Sink](gomutants.OpenOptions{}.Trace)
	pinType[func(*gomutants.Workspace) []trace.Event]((*gomutants.Workspace).Recording)
	pinType[func(*gomutants.Session) string]((*gomutants.Session).OverlayManifest)
	pinType[func(*gomutants.Session) string]((*gomutants.Session).ProbeOverlayManifest)

	var sink trace.Sink = trace.NewMemorySink(trace.DefaultRingCapacity)
	options := gomutants.OpenOptions{Trace: sink}
	if options.Trace == nil {
		t.Error("OpenOptions.Trace did not keep the sink it was given")
	}

	if (gomutants.OpenOptions{}).Trace != nil {
		t.Error("the zero OpenOptions carries a sink")
	}
}

func pinType[T any](T) {}

var (
	workspaceArtifactKinds = []string{
		trace.ArtifactOverlayManifest,
		trace.ArtifactProbeOverlayManifest,
		trace.ArtifactKeptSnapshot,
		trace.ArtifactKeptScratch,
		trace.ArtifactKeptProbeTree,
		trace.ArtifactKeptExecScratch,
	}
	workspaceNoteKinds = []string{trace.NotePrepareFailed, trace.NoteControl}
)

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
			if enumerated != nil && !slices.Contains(enumerated, kind) {
				t.Errorf("the schema enumerates %s kinds and %q is not among them: %v",
					payload, kind, enumerated)
			}
		}
	}
}

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
