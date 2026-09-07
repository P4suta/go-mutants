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
		ModulePath:   modulePath,
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
