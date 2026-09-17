// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

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

	if entries := testkit.Entries(t, temp); len(entries) != 0 {
		t.Errorf("the temporary directory holds %q, want nothing: Open probes before it copies",
			entries)
	}

	var kinds []string
	for _, event := range sink.Events() {
		kinds = append(kinds, event.Type)
	}
	for _, want := range []string{trace.TypeRunStart, trace.TypeExec, trace.TypeRunEnd} {
		if !slices.Contains(kinds, want) {
			t.Errorf("the recording holds %q, want a %q event", kinds, want)
		}
	}

	calls := f.Calls()
	if len(calls) != 1 || !slices.Equal(calls[0].Argv, []string{"version"}) {
		t.Fatalf("the scripted toolchain was called %v, want one `go version`", calls)
	}
	if base := filepath.Base(f.Bin()); !strings.HasPrefix(base, "go") {
		t.Errorf("the scripted toolchain is installed as %q, want a `go`", base)
	}
}
