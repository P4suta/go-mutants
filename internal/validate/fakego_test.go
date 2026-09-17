// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The validation phase against a real process, without a real toolchain.
//
// Every other statement this package makes about a build is made by an
// integration test, because a build is a process and the phase's whole subject
// is what one said. The scripted `go` from internal/testkit/mutantkit is a
// process that answers from a table, so the one verdict a real toolchain cannot
// be made to produce on demand — a snapshot that does not build with *no*
// mutants in it — becomes a unit-tier test.

package validate_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/internal/validate"
)

// TestMain turns this binary into the scripted `go` when it is started as one.
// The test below runs this very binary as its toolchain, so without the
// dispatch the build would run the whole package again.
func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

// TestPristineBuildFailureIsNotMutantInducedAndKeepsTheCompilersWords is the
// gate that stops a validation from blaming the user's code on go-mutants'
// behalf, and the other way round.
//
// The catalogue is empty, so the tree the phase builds is the user's own source
// plus a generated runtime that mutates nothing. A build that fails there cannot
// have been caused by a mutant, and saying so is the difference between a
// diagnosis and a wild goose chase: [validate.CodeNotMutantInduced] tells the
// reader to look at the snapshot, and a rejection would have told them to look
// at a mutant that does not exist.
//
// It needs a real process and no toolchain at all, which is exactly the gap the
// scripted `go` fills: a released `go build` cannot be asked to fail over a tree
// that compiles.
func TestPristineBuildFailureIsNotMutantInducedAndKeepsTheCompilersWords(t *testing.T) {
	t.Parallel()

	const modulePath = "fixture.example/validate"
	// A `go build` header, which is the bare import path: the bracketed
	// `# path [path.test]` form is `go test -c`'s, and this phase never issues
	// one. The wording is the go command's own.
	const diagnostics = "# " + modulePath + "\n" +
		"only.go:5:2: \"fmt\" imported and not used\n"

	module := testkit.NewModule(t).Module(modulePath)
	module.Source("only.go", "package only\n\nfunc Only() int { return 1 }\n")

	snap, err := snapshot.Create(module.Root(), snapshot.Options{DestParent: testkit.Scratch(t)})
	if err != nil {
		t.Fatalf("snapshotting the synthesized module: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("removing the snapshot: %v", cleanupErr)
		}
	})

	f := mutantkit.FakeGo(t)
	f.On("build").Stderr(diagnostics).Exit(1)

	_, err = validate.Validate(t.Context(), validate.Options{
		Snap:         snap,
		Catalog:      emptyCatalog(t),
		Modules:      []validate.Module{{Dir: ".", Path: modulePath}},
		Toolchain:    gocmd.Toolchain{GoBin: f.Bin()},
		Env:          f.Env(testkit.Compose(t, testkit.Scratch(t))),
		BuildTimeout: time.Minute,
	})
	if err == nil {
		t.Fatal("Validate over a snapshot that does not build = nil, want an error")
	}
	if code := validate.CodeOf(err); code != validate.CodeNotMutantInduced {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, validate.CodeNotMutantInduced)
	}
	var failure *validate.Error
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want a *validate.Error", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(diagnostics), "\n") {
		if !strings.Contains(failure.Output, line) {
			t.Errorf("the retained output %q does not quote %q", failure.Output, line)
		}
	}

	// Two builds, which is the gate itself: the instrumented tree, and then the
	// tree with every mutant taken out again. The second is what turns "it did
	// not build" into "it did not build for a reason no mutant can explain",
	// and with an empty catalogue the two trees are the same one.
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("the toolchain was called %d times, want the instrumented build and the pristine "+
			"gate's: %v", len(calls), calls)
	}
	for _, call := range calls {
		want := []string{"build", "-o", os.DevNull, "./..."}
		if !slices.Equal(call.Argv, want) {
			t.Errorf("the toolchain received %q, want %q: without `-o %s` the go command links an "+
				"executable into the tree the drift gate is about to re-digest",
				call.Argv, want, os.DevNull)
		}
		// In the snapshot rather than in the workspace: a validation that built
		// the user's own tree would be reporting on bytes it had not
		// instrumented.
		if !testkit.SamePath(call.Dir, snap.Root) {
			t.Errorf("the build ran in %q, want the snapshot root %q", call.Dir, snap.Root)
		}
	}
}

// TestABuildThatRunsOutOfTimeIsNotABuildThatSaidNo is the second of the four
// answers one build can give, and the one no released toolchain can be asked
// for.
//
// A build that did not finish has said nothing about any mutant. Reading it as
// a red build would send the search bisecting a file over a verdict the
// compiler never reached, and reading it as green would accept a catalogue
// nobody compiled -- so it is an error with a code of its own, and the code
// says what a user can do about it.
func TestABuildThatRunsOutOfTimeIsNotABuildThatSaidNo(t *testing.T) {
	t.Parallel()

	const modulePath = "fixture.example/slow"
	module := testkit.NewModule(t).Module(modulePath)
	module.Source("only.go", "package only\n\nfunc Only() int { return 1 }\n")

	snap, err := snapshot.Create(module.Root(), snapshot.Options{DestParent: testkit.Scratch(t)})
	if err != nil {
		t.Fatalf("snapshotting the synthesized module: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("removing the snapshot: %v", cleanupErr)
		}
	})

	f := mutantkit.FakeGo(t)
	// Ten times the bound below, which is enough for the supervisor to be what
	// ends it and short enough that a build nobody bounded still ends: an edit
	// that dropped the bound would otherwise leave this test waiting out a
	// whole per-mutant timeout in this repository's own gate, twice.
	f.On("build").Sleep(2 * time.Second)

	_, err = validate.Validate(t.Context(), validate.Options{
		Snap:         snap,
		Catalog:      emptyCatalog(t),
		Modules:      []validate.Module{{Dir: ".", Path: modulePath}},
		Toolchain:    gocmd.Toolchain{GoBin: f.Bin()},
		Env:          f.Env(testkit.Compose(t, testkit.Scratch(t))),
		BuildTimeout: 200 * time.Millisecond,
	})
	if code := validate.CodeOf(err); code != validate.CodeBuildTimedOut {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, validate.CodeBuildTimedOut)
	}
	var failure *validate.Error
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want a *validate.Error", err)
	}
	if !failure.TimedOut {
		t.Error("the failure does not report that it was the clock that ended the build")
	}
	if !strings.Contains(failure.Message, "200ms") {
		t.Errorf("the failure %q does not name the bound it exceeded", failure.Message)
	}
	// And the command is named, because a user asked to look at a build needs
	// to be able to run it.
	if failure.Invocation == nil {
		t.Error("the failure names no command to reproduce")
	}
}

// TestACancelledRunIsNotABrokenBuild is the third answer, and the one that is
// indistinguishable from the others unless the context is asked.
//
// A cancelled run comes back from the runner as an unavailable exit status with
// no error and no timeout, which reads exactly like a compiler that refused. The
// context is what tells them apart, and a run somebody stopped has to say so
// rather than blame the tree.
func TestACancelledRunIsNotABrokenBuild(t *testing.T) {
	t.Parallel()

	const modulePath = "fixture.example/cancelled"
	module := testkit.NewModule(t).Module(modulePath)
	module.Source("only.go", "package only\n\nfunc Only() int { return 1 }\n")

	snap, err := snapshot.Create(module.Root(), snapshot.Options{DestParent: testkit.Scratch(t)})
	if err != nil {
		t.Fatalf("snapshotting the synthesized module: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("removing the snapshot: %v", cleanupErr)
		}
	})

	f := mutantkit.FakeGo(t)
	// Long enough that the cancellation below is what ends it, and short enough
	// that a build nobody cancelled still ends; see the test above.
	f.On("build").Sleep(2 * time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err = validate.Validate(ctx, validate.Options{
		Snap:         snap,
		Catalog:      emptyCatalog(t),
		Modules:      []validate.Module{{Dir: ".", Path: modulePath}},
		Toolchain:    gocmd.Toolchain{GoBin: f.Bin()},
		Env:          f.Env(testkit.Compose(t, testkit.Scratch(t))),
		BuildTimeout: time.Minute,
	})
	if code := validate.CodeOf(err); code != validate.CodeInterrupted {
		t.Fatalf("CodeOf(err) = %q (err %v), want %q", code, err, validate.CodeInterrupted)
	}
	var failure *validate.Error
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want a *validate.Error", err)
	}
	if failure.TimedOut {
		t.Error("an interrupted run was reported as one the clock ended")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("context.Canceled is not reachable through the failure: %v", err)
	}
}
