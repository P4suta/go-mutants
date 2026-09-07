// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The public API's first refusal, without a toolchain.
//
// [gomutants.Open] probes a `go` before it copies a byte, and what it does when
// that probe fails is the first thing a consumer of this package will meet on a
// machine that is not set up: a typed failure with the toolchain's own words,
// nothing frozen, nothing left behind, and — for a caller that supplied a sink —
// a recording that ends rather than stops.
//
// Every other test of Open needs a real toolchain and lives behind the
// integration tag. This one needs a `go` that fails, which no released toolchain
// can be asked to be, so it is scripted; and because it is scripted it is in the
// tier a developer runs on every save.

package gomutants_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/trace"
)

// TestOpenSurfacesAFailingVersionProbe is what a consumer sees first on a
// machine where go-mutants cannot run.
//
// Three things have to be true at once and each of them used to need a real
// toolchain to observe. The failure is typed, so a caller can branch on the code
// rather than on a message. It carries what the probe printed, because a binary
// that is not a Go toolchain explains itself in its own output. And it costs
// nothing: no snapshot is taken, so the temporary directory the caller named is
// as empty afterwards as it was before.
func TestOpenSurfacesAFailingVersionProbe(t *testing.T) {
	t.Parallel()

	const complaint = "go: unsupported GOOS/GOARCH pair js/wasi"
	f := mutantkit.FakeGo(t)
	f.On("version").Stderr(complaint + "\n").Exit(1)

	module := testkit.NewModule(t).Module("fixture.example/opened")
	module.Source("only/only.go", "package only\n\nfunc Only() int { return 1 }\n")

	temp := testkit.Scratch(t)
	sink := trace.NewMemorySink(0)
	workspace, err := gomutants.Open(t.Context(), module.Root(), gomutants.OpenOptions{
		GoBinary:      f.Bin(),
		Env:           f.Env(testkit.Compose(t, testkit.Scratch(t))),
		TempDirectory: temp,
		Trace:         sink,
	})
	if err == nil {
		_ = workspace.Close()
		t.Fatal("Open against a `go` that fails its version probe = a workspace, want an error")
	}
	if workspace != nil {
		t.Errorf("Open returned %+v as well as an error, want nil", workspace)
	}
	if code := gomutants.DiagnosticCode(err); code != gocmd.CodeVersionProbeFailed {
		t.Fatalf("DiagnosticCode(err) = %q (err %v), want %q", code, err, gocmd.CodeVersionProbeFailed)
	}
	if !strings.Contains(err.Error(), "open toolchain") {
		t.Errorf("Error() = %q, want it to say which step of Open refused", err)
	}
	if !strings.Contains(err.Error(), "exited with status 1") {
		t.Errorf("Error() = %q, want it to say what the probe did", err)
	}
	var failure *gocmd.Error
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want the toolchain failure to survive unwrapping", err)
	}
	if !strings.Contains(failure.RetainedOutput(), complaint) {
		t.Errorf("RetainedOutput() = %q, want what the probe printed, %q",
			failure.RetainedOutput(), complaint)
	}

	// Nothing was frozen. The probe comes before the copy precisely so that a
	// machine without a usable toolchain does not pay for a module-sized copy
	// to be told so.
	if entries := testkit.Entries(t, temp); len(entries) != 0 {
		t.Errorf("the temporary directory holds %q, want nothing: Open probes before it copies",
			entries)
	}

	// The account of a failed Open is complete: the probe is recorded, and the
	// stream ends with a run-end rather than stopping mid-sentence. A caller
	// who supplied a sink to find out why a workspace could not be opened is
	// the reader this is for.
	var kinds []string
	for _, event := range sink.Events() {
		kinds = append(kinds, event.Type)
	}
	for _, want := range []string{trace.TypeRunStart, trace.TypeExec, trace.TypeRunEnd} {
		if !slices.Contains(kinds, want) {
			t.Errorf("the recording holds %q, want a %q event", kinds, want)
		}
	}

	// And the toolchain that was probed is the one the caller named, argv and
	// all, read off the process rather than off the spec that described it.
	calls := f.Calls()
	if len(calls) != 1 || !slices.Equal(calls[0].Argv, []string{"version"}) {
		t.Fatalf("the scripted toolchain was called %v, want one `go version`", calls)
	}
	if base := filepath.Base(f.Bin()); !strings.HasPrefix(base, "go") {
		t.Errorf("the scripted toolchain is installed as %q, want a `go`", base)
	}
}
