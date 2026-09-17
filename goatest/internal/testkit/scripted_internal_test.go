// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"path/filepath"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
)

func TestEveryRuleBuilderReturnsTheRuleItConfigured(t *testing.T) {
	t.Parallel()
	session := NewSession(gomutants.Catalog{})
	mutant := session.On("mutant-a")
	if got := mutant.Do(func(gomutants.ExecRequest) (gomutants.MutantResult, error) {
		return gomutants.MutantResult{Outcome: gomutants.OutcomeKilled}, nil
	}); got != mutant {
		t.Fatalf("MutantRule.Do returned %v, want the rule it configured", got)
	}
	probe := session.OnProbe("example/a")
	if got := probe.Do(func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured}, nil
	}); got != probe {
		t.Fatalf("ProbeRule.Do returned %v, want the rule it configured", got)
	}
	control := session.OnControl("example/a")
	if got := control.Do(func(gomutants.ControlRequest) (gomutants.ControlResult, error) {
		return gomutants.ControlResult{}, nil
	}); got != control {
		t.Fatalf("ControlRule.Do returned %v, want the rule it configured", got)
	}
	workspace := NewWorkspace()
	command := workspace.On("go")
	if got := command.Do(func(gomutants.Command) (gomutants.CommandResult, error) {
		return gomutants.CommandResult{}, nil
	}); got != command {
		t.Fatalf("Rule.Do returned %v, want the rule it configured", got)
	}
}

func TestAProbeIsRoutedToTheRuleThatNamesMostOfItsArguments(t *testing.T) {
	t.Parallel()
	session := NewSession(gomutants.Catalog{})
	session.OnProbe("example/a", "-test.count=1", "-test.short=true").
		Do(func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
			return gomutants.ProbeResult{Outcome: gomutants.ProbeMeasured}, nil
		})
	session.OnProbe("example/a", "-test.count=1").Do(func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{Outcome: gomutants.ProbeTimedOut}, nil
	})
	session.OnProbe("example/a").Do(func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{Outcome: gomutants.ProbeUnavailable}, nil
	})
	session.OnProbe("example/a").Do(func(gomutants.ProbeRequest) (gomutants.ProbeResult, error) {
		return gomutants.ProbeResult{Outcome: gomutants.ProbeTestFailed}, nil
	})
	for _, test := range []struct {
		name string
		args []string
		want gomutants.ProbeOutcome
	}{
		{name: "no arguments, answered by the first rule that named none", want: gomutants.ProbeUnavailable},
		{name: "one argument", args: []string{"-test.count=1"}, want: gomutants.ProbeTimedOut},
		{
			name: "both arguments",
			args: []string{"-test.count=1", "-test.short=true"}, want: gomutants.ProbeMeasured,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := session.Probe(t.Context(), gomutants.ProbeRequest{Package: "example/a", Args: test.args})
			if err != nil || result.Outcome != test.want {
				t.Fatalf("Probe(%q) = (%+v, %v), want %q", test.args, result, err, test.want)
			}
		})
	}
}

func TestARepositoryWritesEveryFileItIsGivenUnderItsOwnRoot(t *testing.T) {
	t.Parallel()
	repository := NewRepo(t).File(filepath.Join("nested", "value.go"), "package nested\n")
	stored, err := os.ReadFile(repository.Path(filepath.Join("nested", "value.go")))
	if err != nil || string(stored) != "package nested\n" {
		t.Fatalf("stored file = (%q, %v)", stored, err)
	}
}
