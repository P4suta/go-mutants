// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

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

func TestMain(m *testing.M) {
	os.Exit(mutantkit.Main(m))
}

func TestPristineBuildFailureIsNotMutantInducedAndKeepsTheCompilersWords(t *testing.T) {
	t.Parallel()

	const modulePath = "fixture.example/validate"
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
		if !testkit.SamePath(call.Dir, snap.Root) {
			t.Errorf("the build ran in %q, want the snapshot root %q", call.Dir, snap.Root)
		}
	}
}

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
	if failure.Invocation == nil {
		t.Error("the failure names no command to reproduce")
	}
}

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
