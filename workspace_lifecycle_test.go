// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/snapshot"
)

const (
	frozenASource    = "package a\n\nfunc A(v int) bool { return v == 1 }\n"
	frozenBSource    = "package b\n\nfunc B(v int) bool { return v == 2 }\n"
	frozenCSource    = "package c\n\nvar C = \"nothing here is mutable\"\n"
	rewrittenASource = frozenASource + "\n// a command wrote this\n"
	rewrittenCSource = frozenCSource + "\n// a command wrote this\n"
)

func TestCatalogSourceDigestsMustMatchTheManifest(t *testing.T) {
	t.Parallel()

	frozen := map[string]string{"a.go": frozenASource, "b.go": frozenBSource, "c.go": frozenCSource}
	cases := []struct {
		name     string
		read     map[string]string
		captured map[string]string
		manifest map[string]string
		want     []Change
	}{
		{
			name: "a discovery nothing wrote under",
			read: frozen,
		},
		{
			name: "a file with no mutants a command rewrote while discovery read it",
			read: map[string]string{"a.go": frozenASource, "b.go": frozenBSource, "c.go": rewrittenCSource},
			want: []Change{{
				Kind:         ChangeModified,
				Path:         "c.go",
				BeforeSHA256: mutation.DigestString(frozenCSource),
				AfterSHA256:  mutation.DigestString(rewrittenCSource),
			}},
		},
		{
			name: "a catalogued file a command rewrote while discovery read it",
			read: map[string]string{"a.go": rewrittenASource, "b.go": frozenBSource, "c.go": frozenCSource},
			want: []Change{{
				Kind:         ChangeModified,
				Path:         "a.go",
				BeforeSHA256: mutation.DigestString(frozenASource),
				AfterSHA256:  mutation.DigestString(rewrittenASource),
			}},
		},
		{
			name:     "sources captured while a command had the file rewritten",
			read:     frozen,
			captured: map[string]string{"a.go": rewrittenASource, "b.go": frozenBSource},
			want: []Change{{
				Kind:         ChangeModified,
				Path:         "a.go",
				BeforeSHA256: mutation.DigestString(frozenASource),
				AfterSHA256:  mutation.DigestString(rewrittenASource),
			}},
		},
		{
			name:     "a file the manifest never held",
			read:     frozen,
			manifest: map[string]string{"a.go": frozenASource, "c.go": frozenCSource},
			want: []Change{{
				Kind:        ChangeAdded,
				Path:        "b.go",
				AfterSHA256: mutation.DigestString(frozenBSource),
			}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			manifest := c.manifest
			if manifest == nil {
				manifest = frozen
			}
			captured := c.captured
			if captured == nil {
				captured = c.read
			}
			err := checkDiscoveredSources(
				manifestOf(manifest), digestsOf(c.read), catalogueOf(t, c.read), imagesOf(captured))
			if len(c.want) == 0 {
				if err != nil {
					t.Fatalf("checkDiscoveredSources = %v, want nil", err)
				}
				return
			}
			var drift *DriftError
			if !errors.As(err, &drift) {
				t.Fatalf("checkDiscoveredSources = %v, want a *DriftError", err)
			}
			if drift.Stage != driftStageDiscovery {
				t.Errorf("Stage = %q, want %q", drift.Stage, driftStageDiscovery)
			}
			if !slices.Equal(drift.Changes, c.want) {
				t.Errorf("Changes = %+v, want %+v", drift.Changes, c.want)
			}
			if !strings.HasPrefix(err.Error(), "gomutants: prepare commands changed the snapshot during discovery:") {
				t.Errorf("message = %q, want the sentence for a write discovery read and the gate"+
					" could no longer see", err.Error())
			}
		})
	}

	if err := checkDiscoveredSources(manifestOf(frozen), nil, nil, nil); err != nil {
		t.Errorf("checkDiscoveredSources with nothing discovered = %v, want nil", err)
	}
	rewritten := map[string]string{"a.go": rewrittenASource}
	if err := checkDiscoveredSources(
		manifestOf(frozen), nil, catalogueOf(t, rewritten), nil); err == nil {
		t.Error("checkDiscoveredSources with only a catalogue to go on = nil, want the rewritten" +
			" a.go it catalogued")
	}
}

func digestsOf(sources map[string]string) map[string]string {
	digests := make(map[string]string, len(sources))
	for path, source := range sources {
		digests[path] = mutation.DigestString(source)
	}
	return digests
}

func catalogueOf(t *testing.T, sources map[string]string) *mutation.Catalog {
	t.Helper()
	builder := mutation.NewBuilder()
	for _, path := range slices.Sorted(maps.Keys(sources)) {
		source := sources[path]
		at := strings.Index(source, "==")
		if at < 0 {
			continue
		}
		span, err := mutation.NewSpan(uint32(at), uint32(at+2))
		if err != nil {
			t.Fatalf("building the span for %s: %v", path, err)
		}
		if addErr := builder.Add(mutation.Candidate{
			Path: path,
			Rule: mutation.Rule{
				Family:  mutation.FamilyComparison,
				Name:    "eq-to-neq",
				Version: 1,
				Tier:    mutation.TierBalanced,
			},
			Span:         span,
			Original:     "==",
			Replacement:  "!=",
			SourceDigest: mutation.DigestString(source),
		}); addErr != nil {
			t.Fatalf("cataloguing %s: %v", path, addErr)
		}
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	return catalog
}

func manifestOf(sources map[string]string) []snapshot.Entry {
	entries := make([]snapshot.Entry, 0, len(sources))
	for _, path := range slices.Sorted(maps.Keys(sources)) {
		entries = append(entries, snapshot.Entry{
			RelPath: path,
			Size:    int64(len(sources[path])),
			SHA256:  mutation.DigestString(sources[path]),
		})
	}
	return entries
}

func imagesOf(sources map[string]string) map[string]sourceImage {
	images := make(map[string]sourceImage, len(sources))
	for path, source := range sources {
		images[path] = sourceImage{data: []byte(source), mode: privateFileMode}
	}
	return images
}

func TestWorkspaceLifecycleRefusalsAreOneStateMachine(t *testing.T) {
	t.Parallel()

	const (
		noExecutable = "gomutants: exec: command has no executable"
		badProfile   = `gomutants: prepare profile "nope": expected balanced, strong, or all`
		badQuery     = `gomutants: module: invalid query: package pattern "/etc" is absolute; patterns are module-relative`
		execClosed   = "gomutants: exec: workspace is closed"
		execFailed   = "gomutants: exec: workspace preparation failed; its tree may hold instrumented sources"
		moduleClosed = "gomutants: module: workspace is closed"
		moduleFailed = "gomutants: module: workspace preparation failed; its tree may hold instrumented sources"
		prepClosed   = "gomutants: prepare: workspace is closed"
		prepAgain    = "gomutants: prepare: workspace has already been prepared"
	)
	cases := []struct {
		name           string
		closed         bool
		prepareStarted bool
		prepareFailed  bool
		session        bool
		exec           string
		module         string
		prepare        string
	}{
		{
			name:    "freshly opened",
			exec:    noExecutable,
			module:  badQuery,
			prepare: badProfile,
		},
		{
			name:           "a preparation in flight",
			prepareStarted: true,
			exec:           noExecutable,
			module:         badQuery,
			prepare:        prepAgain,
		},
		{
			name:           "a preparation that succeeded",
			prepareStarted: true,
			session:        true,
			exec:           noExecutable,
			module:         badQuery,
			prepare:        prepAgain,
		},
		{
			name:           "a preparation that began and failed",
			prepareStarted: true,
			prepareFailed:  true,
			exec:           execFailed,
			module:         moduleFailed,
			prepare:        prepAgain,
		},
		{
			name:    "closed",
			closed:  true,
			exec:    execClosed,
			module:  moduleClosed,
			prepare: prepClosed,
		},
		{
			name:           "closed after a preparation that succeeded",
			closed:         true,
			prepareStarted: true,
			exec:           execClosed,
			module:         moduleClosed,
			prepare:        prepClosed,
		},
		{
			name:           "closed after a preparation that failed",
			closed:         true,
			prepareStarted: true,
			prepareFailed:  true,
			exec:           execClosed,
			module:         moduleClosed,
			prepare:        prepClosed,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			workspace := &Workspace{
				closed:         c.closed,
				prepareStarted: c.prepareStarted,
				prepareFailed:  c.prepareFailed,
				scratch:        t.TempDir(),
				closeDone:      make(chan struct{}),
			}
			if c.session {
				workspace.session = &Session{}
			}

			_, execErr := workspace.Exec(t.Context(), Command{})
			if got := errorText(execErr); got != c.exec {
				t.Errorf("Exec = %q, want %q", got, c.exec)
			}
			_, moduleErr := workspace.Module(t.Context(), ModuleQuery{Packages: []string{"/etc"}})
			if got := errorText(moduleErr); got != c.module {
				t.Errorf("Module = %q, want %q", got, c.module)
			}
			_, prepareErr := workspace.Prepare(t.Context(), PrepareOptions{Profile: "nope"})
			if got := errorText(prepareErr); got != c.prepare {
				t.Errorf("Prepare = %q, want %q", got, c.prepare)
			}
			assertLifecycleSentinels(t, execErr, moduleErr, prepareErr)
			if c.closed {
				return
			}

			if closeErr := workspace.Close(); closeErr != nil {
				t.Fatalf("closing: %v", closeErr)
			}
			if _, err := workspace.Exec(t.Context(), Command{}); !errors.Is(err, ErrWorkspaceClosed) {
				t.Errorf("Exec after Close = %v, want ErrWorkspaceClosed", err)
			}
			if _, err := workspace.Module(t.Context(), ModuleQuery{}); !errors.Is(err, ErrWorkspaceClosed) {
				t.Errorf("Module after Close = %v, want ErrWorkspaceClosed", err)
			}
			if _, err := workspace.Prepare(t.Context(), PrepareOptions{}); !errors.Is(err, ErrWorkspaceClosed) {
				t.Errorf("Prepare after Close = %v, want ErrWorkspaceClosed", err)
			}
		})
	}
}

func assertLifecycleSentinels(t *testing.T, errs ...error) {
	t.Helper()
	sentinels := map[string]error{
		"workspace is closed":                 ErrWorkspaceClosed,
		"workspace has already been prepared": ErrWorkspacePrepared,
		"workspace preparation failed":        ErrPrepareFailed,
	}
	for _, err := range errs {
		for phrase, sentinel := range sentinels {
			named := strings.Contains(errorText(err), phrase)
			if named != errors.Is(err, sentinel) {
				t.Errorf("%v names %q = %v but errors.Is(%v) = %v", err, phrase, named,
					sentinel, errors.Is(err, sentinel))
			}
		}
	}
}

func TestAPreparationThatPanickedSpendsTheWorkspace(t *testing.T) {
	t.Parallel()

	workspace := &Workspace{scratch: t.TempDir(), closeDone: make(chan struct{})}
	recovered := func() (recovered any) {
		defer func() { recovered = recover() }()
		_, _ = workspace.Prepare(context.Background(), PrepareOptions{
			SkipVerify: true,
			Trace:      func(PrepareEvent) { panic("a consumer's callback panicked") },
		})
		return nil
	}()
	if recovered == nil {
		t.Fatal("the callback's panic did not reach the caller, so this test never produced the" +
			" state it is about")
	}

	if _, err := workspace.Exec(context.Background(), Command{}); !errors.Is(err, ErrPrepareFailed) {
		t.Errorf("Exec after a preparation that panicked = %v, want ErrPrepareFailed: the tree may"+
			" still hold instrumented sources", err)
	}
	if _, err := workspace.Prepare(context.Background(), PrepareOptions{SkipVerify: true}); !errors.Is(
		err, ErrWorkspacePrepared) {
		t.Errorf("Prepare after a preparation that panicked = %v, want ErrWorkspacePrepared", err)
	}
}

func TestWorkspaceCallsAreRaceFreeAcrossItsLifecycle(t *testing.T) {
	const commands = 8
	const preparations = 4
	const readers = 2

	workspace := &Workspace{scratch: t.TempDir(), closeDone: make(chan struct{})}
	const badProfile = `gomutants: prepare profile "nope": expected balanced, strong, or all`
	for attempt := range 2 {
		_, err := workspace.Prepare(context.Background(), PrepareOptions{Profile: "nope"})
		if got := errorText(err); got != badProfile {
			t.Fatalf("Prepare %d of 2 with a refused option = %q, want %q: a caller that mistyped"+
				" one must not lose the workspace", attempt+1, got, badProfile)
		}
	}

	execErrs := make(chan error, commands)
	prepareErrs := make(chan error, preparations)
	var group sync.WaitGroup
	for range commands {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := workspace.Exec(context.Background(), Command{})
			execErrs <- err
		}()
	}
	for range preparations {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := workspace.Prepare(context.Background(), PrepareOptions{Profile: "nope"})
			prepareErrs <- err
		}()
	}
	for range readers {
		group.Add(1)
		go func() {
			defer group.Done()
			_ = workspace.Swept()
			_ = workspace.Preserved()
			_ = workspace.ToolchainVersion()
			_ = workspace.Recording()
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		if err := workspace.Close(); err != nil {
			t.Errorf("closing: %v", err)
		}
	}()
	group.Wait()
	close(execErrs)
	close(prepareErrs)

	for err := range execErrs {
		if errors.Is(err, ErrWorkspaceClosed) {
			continue
		}
		if got := errorText(err); got != "gomutants: exec: command has no executable" {
			t.Errorf("a concurrent Exec = %q, want the command's own refusal or a closed workspace", got)
		}
	}
	for err := range prepareErrs {
		if errors.Is(err, ErrWorkspaceClosed) || errors.Is(err, ErrWorkspacePrepared) {
			continue
		}
		if got := errorText(err); got != badProfile {
			t.Errorf("a concurrent Prepare = %q, want the option's own refusal or a lifecycle one", got)
		}
	}
	if err := workspace.Close(); err != nil {
		t.Errorf("closing twice: %v", err)
	}
}
