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

// The three sources the discovery below reads. Two hold one `==` each for the
// comparison rule to propose an edit at, and all three differ, so a digest
// computed from one is never accidentally the digest of another.
//
// The third holds nothing mutable at all, which is the case a check built on
// the catalogue could not see: discovery reads it, walks it, and produces no
// candidate, so only what discovery *read* can answer for it.
const (
	frozenASource = "package a\n\nfunc A(v int) bool { return v == 1 }\n"
	frozenBSource = "package b\n\nfunc B(v int) bool { return v == 2 }\n"
	frozenCSource = "package c\n\nvar C = \"nothing here is mutable\"\n"
	// rewrittenASource and rewrittenCSource are those sources as a command left
	// them: one comment more, which is a change to every byte-level identity the
	// engine mints from a file and to nothing a compiler can see.
	rewrittenASource = frozenASource + "\n// a command wrote this\n"
	rewrittenCSource = frozenCSource + "\n// a command wrote this\n"
)

// TestCatalogSourceDigestsMustMatchTheManifest drives the check that closes the
// hole a preparation opens by letting commands run beside it.
//
// Discovery reads the tree while a command may write it, and the integrity gate
// runs afterwards with the tree held exclusively. So a command that changed a
// source file *and put it back* leaves the gate nothing to find, while the
// catalogue it produced identifies its mutants by the digest of bytes that are
// nowhere on disk. This is the check that refuses that catalogue, and it is
// driven synthetically because what it does is compare three digests: no
// toolchain, no snapshot on disk, and no way for the assertions to be about
// anything but the comparison.
//
// The four ways it can fail are four different writes. Discovery read a
// *catalogued* file that has since been put back; it read a file with nothing
// mutable in it, which no catalogue can speak for; the capture that restoration
// will write from read one; and a file was created and removed again, so what
// discovery read names a path the manifest never held.
func TestCatalogSourceDigestsMustMatchTheManifest(t *testing.T) {
	t.Parallel()

	frozen := map[string]string{"a.go": frozenASource, "b.go": frozenBSource, "c.go": frozenCSource}
	cases := []struct {
		name string
		// read is what discovery read, and captured is what the capture for
		// restoration read. A nil captured means "the same bytes discovery
		// saw", which is the ordinary case.
		read     map[string]string
		captured map[string]string
		// manifest is what the snapshot froze; nil means all of frozen.
		manifest map[string]string
		want     []Change
	}{
		{
			name: "a discovery nothing wrote under",
			read: frozen,
		},
		{
			// The case a catalogue cannot answer for at all: c.go holds nothing
			// mutable, so it has no mutants and the only record that discovery
			// ever looked at it is the digest it recorded.
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

	// A preparation that read and catalogued nothing has nothing to compare,
	// which is what every path that fails before discovery hands over.
	if err := checkDiscoveredSources(manifestOf(frozen), nil, nil, nil); err != nil {
		t.Errorf("checkDiscoveredSources with nothing discovered = %v, want nil", err)
	}
	// And a catalogue is still checked where discovery recorded no digest, so
	// that the two answers are belt and braces rather than one replacing the
	// other.
	rewritten := map[string]string{"a.go": rewrittenASource}
	if err := checkDiscoveredSources(
		manifestOf(frozen), nil, catalogueOf(t, rewritten), nil); err == nil {
		t.Error("checkDiscoveredSources with only a catalogue to go on = nil, want the rewritten" +
			" a.go it catalogued")
	}
}

// digestsOf is what [github.com/P4suta/go-mutants/internal/discover.Result]
// records for the files a pass read.
func digestsOf(sources map[string]string) map[string]string {
	digests := make(map[string]string, len(sources))
	for path, source := range sources {
		digests[path] = mutation.DigestString(source)
	}
	return digests
}

// catalogueOf catalogues one comparison mutant per source that has one to
// propose, identified by that source's digest — which is the only thing about
// the catalogue this check reads. A source with nothing mutable in it is
// catalogued as nothing at all, exactly as discovery would leave it.
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

// manifestOf is a snapshot manifest over the given sources, in path order, as
// [snapshot.Create] returns one.
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

// imagesOf is what [captureInstrumentationSources] would have returned for the
// given sources.
func imagesOf(sources map[string]string) map[string]sourceImage {
	images := make(map[string]sourceImage, len(sources))
	for path, source := range sources {
		images[path] = sourceImage{data: []byte(source), mode: privateFileMode}
	}
	return images
}

// TestWorkspaceLifecycleRefusalsAreOneStateMachine drives every reachable
// combination of the four fields that say what has become of a workspace, and
// the two calls that answer to them.
//
// They are one table because they are one decision, and because the fields
// stopped being able to speak for each other. "A preparation that began and
// failed" used to be inferred — prepared, and no session — which made it the
// same shape a `Close` leaves behind and made the *order* of two checks the
// only thing keeping the two apart. Each is now recorded, so the table can ask
// about each state directly, and the order is still asserted for the one
// workspace that really is in two of them at once.
//
// Prepare is driven with a profile no engine accepts, so that a state the
// lifecycle does *not* refuse comes back with the option's own message: it is
// how a row says "not refused" without this unit test having to own a snapshot.
// Exec is driven with an empty command for the same reason.
func TestWorkspaceLifecycleRefusalsAreOneStateMachine(t *testing.T) {
	t.Parallel()

	const (
		noExecutable = "gomutants: exec: command has no executable"
		badProfile   = `gomutants: prepare profile "nope": expected balanced, strong, or all`
		execClosed   = "gomutants: exec: workspace is closed"
		execFailed   = "gomutants: exec: workspace preparation failed; its tree may hold instrumented sources"
		prepClosed   = "gomutants: prepare: workspace is closed"
		prepAgain    = "gomutants: prepare: workspace has already been prepared"
	)
	cases := []struct {
		name string
		// The four fields stateMu guards, which are the whole of the state.
		closed         bool
		prepareStarted bool
		prepareFailed  bool
		session        bool
		// The message each call answers with, refusal or not.
		exec    string
		prepare string
	}{
		{
			name:    "freshly opened",
			exec:    noExecutable,
			prepare: badProfile,
		},
		{
			name:           "a preparation in flight",
			prepareStarted: true,
			exec:           noExecutable,
			prepare:        prepAgain,
		},
		{
			name:           "a preparation that succeeded",
			prepareStarted: true,
			session:        true,
			exec:           noExecutable,
			prepare:        prepAgain,
		},
		{
			name:           "a preparation that began and failed",
			prepareStarted: true,
			prepareFailed:  true,
			exec:           execFailed,
			prepare:        prepAgain,
		},
		{
			name:    "closed",
			closed:  true,
			exec:    execClosed,
			prepare: prepClosed,
		},
		{
			name:           "closed after a preparation that succeeded",
			closed:         true,
			prepareStarted: true,
			exec:           execClosed,
			prepare:        prepClosed,
		},
		{
			// Both true, and closed wins: a consumer whose workspace is gone
			// has to be told that rather than sent to open another one.
			name:           "closed after a preparation that failed",
			closed:         true,
			prepareStarted: true,
			prepareFailed:  true,
			exec:           execClosed,
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
			_, prepareErr := workspace.Prepare(t.Context(), PrepareOptions{Profile: "nope"})
			if got := errorText(prepareErr); got != c.prepare {
				t.Errorf("Prepare = %q, want %q", got, c.prepare)
			}
			assertLifecycleSentinels(t, execErr, prepareErr)
			if c.closed {
				return
			}

			// And whatever the state was, a Close makes both answers the closed
			// one: it is the row every other row ends at.
			if closeErr := workspace.Close(); closeErr != nil {
				t.Fatalf("closing: %v", closeErr)
			}
			if _, err := workspace.Exec(t.Context(), Command{}); !errors.Is(err, ErrWorkspaceClosed) {
				t.Errorf("Exec after Close = %v, want ErrWorkspaceClosed", err)
			}
			if _, err := workspace.Prepare(t.Context(), PrepareOptions{}); !errors.Is(err, ErrWorkspaceClosed) {
				t.Errorf("Prepare after Close = %v, want ErrWorkspaceClosed", err)
			}
		})
	}
}

// assertLifecycleSentinels requires each refusal to carry exactly the sentinel
// its message names, because a consumer acts on the sentinel and reads the
// message.
func assertLifecycleSentinels(t *testing.T, execErr, prepareErr error) {
	t.Helper()
	sentinels := map[string]error{
		"workspace is closed":                 ErrWorkspaceClosed,
		"workspace has already been prepared": ErrWorkspacePrepared,
		"workspace preparation failed":        ErrPrepareFailed,
	}
	for _, err := range []error{execErr, prepareErr} {
		for phrase, sentinel := range sentinels {
			named := strings.Contains(errorText(err), phrase)
			if named != errors.Is(err, sentinel) {
				t.Errorf("%v names %q = %v but errors.Is(%v) = %v", err, phrase, named,
					sentinel, errors.Is(err, sentinel))
			}
		}
	}
}

// TestAPreparationThatPanickedSpendsTheWorkspace is the failure a state machine
// made of explicit flags can have and an inferred one could not.
//
// The old rule read "prepared, and no session" as a preparation that failed, so
// a preparation that died *any* way at all — including by panicking out of the
// middle of instrumentation — left the workspace refusing commands, by
// construction. Recording the failure instead means recording it on every path
// out, and a `panic` unwinds through a deferred function that is looking at the
// named error, which a panic never sets. A workspace that answered "this
// preparation is fine" after one would hand a command a tree with instrumented
// sources in it.
//
// The panic is a [PrepareOptions.Trace] callback's, which is the honest seam
// rather than a contrived one: the callback is a consumer's own code, ordinary
// Go code panics, and the recorder already goes out of its way to file the
// event a consumer died on. It also happens on the first phase event, before
// this synthetic workspace's absent snapshot is touched — so the test is about
// the state machine and nothing else.
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
	// And the workspace is spent, not merely refusing: a second preparation
	// cannot be started to find out what state the first left the tree in.
	if _, err := workspace.Prepare(context.Background(), PrepareOptions{SkipVerify: true}); !errors.Is(
		err, ErrWorkspacePrepared) {
		t.Errorf("Prepare after a preparation that panicked = %v, want ErrWorkspacePrepared", err)
	}
}

// TestWorkspaceCallsAreRaceFreeAcrossItsLifecycle drives every call that
// touches the workspace's state at once, under the race detector.
//
// The three locks this workspace keeps are worth exactly what the detector says
// about them. `mu` is held shared by commands *and* by preparations now, which
// is the change: the fields that say what has become of the workspace are no
// longer protected by anybody holding it exclusively, and are written under
// `stateMu` instead. A field that was left behind would be a data race here and
// a wrong refusal in production, where the two are the same bug.
//
// It is synthetic on purpose. What is under test is the state machine and its
// locks, so the commands have no executable and the preparations have an option
// no engine accepts: every one of them runs the whole lifecycle path and none
// of them starts a process, which is what lets the untagged tier — the tier
// `-race` is the gate for — run it at all.
func TestWorkspaceCallsAreRaceFreeAcrossItsLifecycle(t *testing.T) {
	const commands = 8
	const preparations = 4
	const readers = 2

	workspace := &Workspace{scratch: t.TempDir(), closeDone: make(chan struct{})}
	// Before the storm, and in order, because the storm cannot say this: a
	// preparation refused for an option it never accepted hands the claim back,
	// so the next one is judged on its own request rather than told the
	// workspace is spent. Under the concurrent part below a Close may reach the
	// workspace first, and then every Prepare is legitimately refused for that.
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
	// Closing while the rest are in flight is the point rather than a tidy-up:
	// Close is the one caller that takes the workspace exclusively, and what it
	// must never do is take the scratch directory away from a command that is
	// already inside one.
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
	// Idempotent, and from a second goroutine's point of view too: the first
	// Close published its answer through the done channel this one waits on.
	if err := workspace.Close(); err != nil {
		t.Errorf("closing twice: %v", err)
	}
}
