// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestExternalModuleCompilesAgainstTheEngineAPI is the consumer-side contract:
// the bridge must be usable from a different module without importing an
// internal package or relying on an in-repository test-only symbol. GOPROXY is
// disabled so this test also proves that compiling the bridge does not perform
// an implicit network operation once the module's declared dependencies exist.
func TestExternalModuleCompilesAgainstTheEngineAPI(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("Go toolchain is unavailable: %v", err)
	}
	repository, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	consumer := t.TempDir()
	module := `module consumer.example/engineapi

go 1.26.0

require github.com/P4suta/go-mutants v0.0.0

replace github.com/P4suta/go-mutants => ` + filepath.ToSlash(repository) + "\n"
	source := `package engineapi_test

import (
	"context"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
)

var (
	_ func(context.Context, string, ...gomutants.OpenOptions) (*gomutants.Workspace, error) = gomutants.Open
	_ func(*gomutants.Workspace, context.Context, gomutants.Command) (gomutants.CommandResult, error) = (*gomutants.Workspace).Exec
	_ func(*gomutants.Workspace, context.Context, gomutants.PrepareOptions) (*gomutants.Session, error) = (*gomutants.Workspace).Prepare
	_ func(*gomutants.Workspace) error = (*gomutants.Workspace).Close
	_ func(*gomutants.Session) gomutants.Catalog = (*gomutants.Session).Catalog
	_ func(*gomutants.Session, context.Context, gomutants.ExecRequest) (gomutants.MutantResult, error) = (*gomutants.Session).Exec
	_ func(*gomutants.Session, context.Context, gomutants.ProbeRequest) (gomutants.ProbeResult, error) = (*gomutants.Session).Probe
	_ func(*gomutants.Session) ([]gomutants.Change, error) = (*gomutants.Session).Changes
	_ func(*gomutants.Session) error = (*gomutants.Session).Close
)

func TestPublicDataTypes(t *testing.T) {
	_ = gomutants.OpenOptions{}
	_ = gomutants.Command{}
	_ = gomutants.CommandResult{}
	_ = gomutants.PrepareOptions{}
	_ = gomutants.Catalog{}
	_ = gomutants.Mutant{}
	_ = gomutants.BranchProof{}
	_ = gomutants.BranchDecreasing
	_ = gomutants.Rejection{}
	_ = gomutants.ExecRequest{}
	_ = gomutants.MutantResult{}
	_ = gomutants.ProbeRequest{}
	_ = gomutants.ProbeResult{}
	_ = gomutants.Artifact{}
	_ = gomutants.Change{}
	_ = gomutants.OutcomeKilled
	_ = gomutants.ProbeMeasured
	_ = gomutants.ProbeTestFailed
	_ = gomutants.ProbeTimedOut
	_ = gomutants.ProbeUnavailable
	_ = gomutants.ErrProbeNotPrepared
	_ = gomutants.ChangeAdded
}
`
	for name, contents := range map[string]string{
		"go.mod":             module,
		"engine_api_test.go": source,
	} {
		if writeErr := os.WriteFile(filepath.Join(consumer, name), []byte(contents), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}

	command := exec.CommandContext(t.Context(), goBinary, "test", "-mod=mod", "./...")
	command.Dir = consumer
	command.Env = append(os.Environ(),
		"GOWORK=off",
		"GOTOOLCHAIN=local",
		"GOPROXY=off",
		"GOSUMDB=off",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("external module did not compile with %s/%s: %v\n%s", runtime.GOOS, runtime.GOARCH, err, strings.TrimSpace(string(output)))
	}
}
