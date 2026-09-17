// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/trace"
)

func compiledArgv(binary, packagePath string) []string {
	return []string{"/usr/local/bin/go", "test", "-c", "-o", binary, packagePath}
}

func TestCompiledTestBinaryReadsOnlyACommandThatCompilesOne(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name            string
		argv            []string
		wantBinary      string
		wantPackagePath string
		wantCompiled    bool
	}{
		{
			name: "a command that compiles one", argv: compiledArgv("/tmp/pkg.test", "example.com/app/pkg"),
			wantBinary: "/tmp/pkg.test", wantPackagePath: "example.com/app/pkg", wantCompiled: true,
		},
		{
			name:       "the output joined to its flag",
			argv:       []string{"go", "test", "-c", "-o=/tmp/pkg.test", "example.com/app/pkg"},
			wantBinary: "/tmp/pkg.test", wantPackagePath: "example.com/app/pkg", wantCompiled: true,
		},
		{
			name:       "the output ahead of another flag",
			argv:       []string{"go", "test", "-c", "-o", "/tmp/pkg.test", "-count=1", "example.com/app/pkg"},
			wantBinary: "/tmp/pkg.test", wantPackagePath: "example.com/app/pkg", wantCompiled: true,
		},
		{name: "too few arguments", argv: []string{"go", "test", "-c", "-o"}},
		{name: "another tool", argv: []string{"gofmt", "test", "-c", "-o", "/tmp/pkg.test", "example.com/app/pkg"}},
		{name: "another subcommand", argv: []string{"go", "build", "-c", "-o", "/tmp/pkg.test", "example.com/app/pkg"}},
		{name: "no compile flag", argv: []string{"go", "test", "-x", "-o", "/tmp/pkg.test", "example.com/app/pkg"}},
		{name: "no output at all", argv: []string{"go", "test", "-c", "-x", "-v", "example.com/app/pkg"}},
		{name: "a flag where the package belongs", argv: []string{"go", "test", "-c", "-o", "/tmp/pkg.test", "-v"}},
		{
			name: "an output flag with nothing after it",
			argv: []string{"go", "test", "-c", "-v", "-o", "example.com/app/pkg"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			binary, packagePath, compiled := compiledTestBinary(test.argv)
			if compiled != test.wantCompiled || binary != test.wantBinary || packagePath != test.wantPackagePath {
				t.Fatalf("compiledTestBinary(%q) = (%q, %q, %t), want (%q, %q, %t)",
					test.argv, binary, packagePath, compiled,
					test.wantBinary, test.wantPackagePath, test.wantCompiled)
			}
		})
	}
}

func TestAnAuditorKeepsABinaryUntilTwoPackagesClaimIt(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.measurement(compiledArgv("/tmp/pkg.test", "example.com/app/pkg"))
	if got := audit.testBinaries["/tmp/pkg.test"]; got != "example.com/app/pkg" {
		t.Fatalf("the first claim recorded %q", got)
	}
	audit.measurement(compiledArgv("/tmp/pkg.test", "example.com/app/pkg"))
	if got := audit.testBinaries["/tmp/pkg.test"]; got != "example.com/app/pkg" {
		t.Fatalf("the same claim twice recorded %q", got)
	}
	audit.measurement(compiledArgv("/tmp/pkg.test", "example.com/app/other"))
	if got := audit.testBinaries["/tmp/pkg.test"]; got != "" {
		t.Fatalf("two packages claiming one binary recorded %q", got)
	}
	audit.measurement(compiledArgv("/tmp/pkg.test", "example.com/app/pkg"))
	if got := audit.testBinaries["/tmp/pkg.test"]; got != "" {
		t.Fatalf("a later claim reopened a conflicted binary as %q", got)
	}
}

func TestAnAuditorNamesTheProfileOfTheBinaryThatWroteIt(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.measurement(compiledArgv("/tmp/pkg.test", "example.com/app/pkg"))
	audit.measurement([]string{
		"/tmp/pkg.test", coverageArgument + "TestOne" + profileSuffix, runArgument + "^TestOne$",
	})
	identity := audit.targets["TestOne"]
	if identity.packagePath != "example.com/app/pkg" || identity.test != "TestOne" {
		t.Fatalf("the profile was attributed to %+v", identity)
	}
	if got, measured := audit.measuredTarget("example.com/app/pkg", "TestOne"); !measured || got != "TestOne" {
		t.Fatalf("measuredTarget = (%q, %t)", got, measured)
	}
}

func TestAnAuditorDropsATargetTwoProfilesClaim(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.measurement(compiledArgv("/tmp/pkg.test", "example.com/app/pkg"))
	audit.measurement([]string{
		"/tmp/pkg.test", coverageArgument + "first" + profileSuffix, runArgument + "^TestOne$",
	})
	audit.measurement([]string{
		"/tmp/pkg.test", coverageArgument + "second" + profileSuffix, runArgument + "^TestOne$",
	})
	if got, measured := audit.measuredTarget("example.com/app/pkg", "TestOne"); measured || got != "" {
		t.Fatalf("measuredTarget after two profiles = (%q, %t), want it dropped", got, measured)
	}
}

func TestAnAuditorKeepsASuiteProfileUntilTwoPackagesClaimIt(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.measurement(compiledArgv("/tmp/pkg.test", "example.com/app/pkg"))
	audit.measurement([]string{"/tmp/pkg.test", coverageArgument + "pkg.suite" + profileSuffix})
	if got := audit.suiteProfiles["example.com/app/pkg"]; got != "pkg.suite" {
		t.Fatalf("the first suite profile recorded %q", got)
	}
	audit.measurement([]string{"/tmp/pkg.test", coverageArgument + "other.suite" + profileSuffix})
	if got := audit.suiteProfiles["example.com/app/pkg"]; got != "" ||
		!audit.suiteProfileConflicts["example.com/app/pkg"] {
		t.Fatalf("two suite profiles recorded %q, conflict %t",
			got, audit.suiteProfileConflicts["example.com/app/pkg"])
	}
	audit.measurement([]string{"/tmp/pkg.test", coverageArgument + "pkg.suite" + profileSuffix})
	if got := audit.suiteProfiles["example.com/app/pkg"]; got != "" {
		t.Fatalf("a later suite profile reopened a conflicted package as %q", got)
	}
}

func TestAnAuditorIgnoresAMeasurementThatNamesNoProfile(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.measurement([]string{"/tmp/pkg.test", runArgument + "^TestOne$"})
	if len(audit.targets) != 0 {
		t.Fatalf("a measurement with no profile recorded %+v", audit.targets)
	}
}

func TestAnAuditorIgnoresAnEventWhosePayloadIsNotItsType(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	for _, event := range []trace.Event{
		{Type: trace.TypeExec, Route: &trace.RouteRecord{MutantID: firstMutant}},
		{Type: trace.TypeRoute, Exec: &trace.ExecRecord{Argv: compiledArgv("/tmp/pkg.test", "example.com/app/pkg")}},
		{Type: trace.TypeProbeExec, Mutant: &trace.MutantRecord{ID: firstMutant, Outcome: outcomeKilled}},
		{Type: trace.TypeMutantExec, Probe: &trace.ProbeRecord{Target: killerTarget}},
		{Type: trace.TypeExec},
		{Type: trace.TypeRoute},
		{Type: trace.TypeProbeExec},
		{Type: trace.TypeMutantExec},
	} {
		audit.read(event)
	}
	if audit.result.routes != 0 || len(audit.routes) != 0 {
		t.Fatalf("a payload that is not its type was read as a route: %+v", audit.routes)
	}
	if len(audit.testBinaries) != 0 || len(audit.targets) != 0 {
		t.Fatalf("a payload that is not its type was read as a measurement: %+v", audit.testBinaries)
	}
	if len(audit.probes) != 0 {
		t.Fatalf("a payload that is not its type was read as a probe: %+v", audit.probes)
	}
	if audit.result.killedExecutions != 0 {
		t.Fatalf("a payload that is not its type was read as an execution")
	}
}

func TestAnAuditorReadsEachEventOfItsOwnKind(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.read(trace.Event{Type: trace.TypeRoute, Route: &trace.RouteRecord{MutantID: firstMutant}})
	audit.read(trace.Event{
		Type: trace.TypeExec, Exec: &trace.ExecRecord{Argv: compiledArgv("/tmp/pkg.test", "example.com/app/pkg")},
	})
	audit.read(trace.Event{
		Type:  trace.TypeProbeExec,
		Probe: &trace.ProbeRecord{Target: killerTarget, Outcome: trace.ProbeOutcomeMeasured},
	})
	audit.read(trace.Event{
		Type:   trace.TypeMutantExec,
		Mutant: &trace.MutantRecord{ID: firstMutant, Outcome: outcomeKilled, Args: []string{runArgument + "^TestOne$"}},
	})
	if audit.result.routes != 1 || len(audit.testBinaries) != 1 || len(audit.probes) != 1 ||
		audit.result.killedExecutions != 1 {
		t.Fatalf("an auditor read %d routes, %d binaries, %d probes, %d kills",
			audit.result.routes, len(audit.testBinaries), len(audit.probes), audit.result.killedExecutions)
	}
}

func TestAProbeThatMeasuredNothingCarriesNoInfectionSet(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.read(trace.Event{Type: trace.TypeProbeExec, Probe: &trace.ProbeRecord{
		Target: killerTarget, Outcome: trace.ProbeOutcomeUnavailable, Infected: []string{firstMutant},
	}})
	facts := audit.probes[killerTarget]
	if facts == nil || facts.measured() || facts.infected != nil {
		t.Fatalf("an unmeasured probe recorded %+v, want no infection set at all", facts)
	}
}

func TestAProbeThatMeasuredKeepsWhatItSawInfect(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.read(trace.Event{Type: trace.TypeProbeExec, Probe: &trace.ProbeRecord{
		Target: killerTarget, Outcome: trace.ProbeOutcomeMeasured, Infected: []string{firstMutant},
	}})
	facts := audit.probes[killerTarget]
	if facts == nil || !facts.measured() {
		t.Fatalf("a measured probe recorded %+v", facts)
	}
	if _, infected := facts.infected[firstMutant]; !infected {
		t.Fatalf("a measured probe lost the infection it saw: %+v", facts.infected)
	}
}

func TestAnAuditorAttributesAProfileToTheBinaryOnlyWhileItIsUnambiguous(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.measurement(compiledArgv("/tmp/pkg.test", "example.com/app/pkg"))
	audit.measurement(compiledArgv("/tmp/pkg.test", "example.com/app/other"))
	audit.measurement([]string{
		"/tmp/pkg.test", coverageArgument + "TestOne" + profileSuffix, runArgument + "^TestOne$",
	})
	if identity := audit.targets["TestOne"]; identity.packagePath != "" {
		t.Fatalf("a profile of a conflicted binary was attributed to %q", identity.packagePath)
	}
}

func TestAnAuditorAttributesASuiteProfileOnlyWhenItKnowsThePackage(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.measurement([]string{"/tmp/unknown.test", coverageArgument + "pkg.suite" + profileSuffix})
	if len(audit.suiteProfiles) != 0 {
		t.Fatalf("a suite profile of an unknown package recorded %+v", audit.suiteProfiles)
	}
}

func TestAnAuditorNamesNoTestWhenTheSelectorNamesMoreThanOne(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.measurement(compiledArgv("/tmp/pkg.test", "example.com/app/pkg"))
	audit.measurement([]string{
		"/tmp/pkg.test", coverageArgument + "pair" + profileSuffix, runArgument + "^(TestOne|TestTwo)$",
	})
	if identity := audit.targets["pair"]; identity.test != "" {
		t.Fatalf("a selector of two tests named %q", identity.test)
	}
}

func TestAnAuditorDoesNotTreatAnOrdinaryProfileAsASuiteOne(t *testing.T) {
	t.Parallel()
	audit := newAuditor(evidence{}, nil, nil)
	audit.measurement(compiledArgv("/tmp/pkg.test", "example.com/app/pkg"))
	audit.measurement([]string{"/tmp/pkg.test", coverageArgument + "plain" + profileSuffix})
	if len(audit.suiteProfiles) != 0 {
		t.Fatalf("a profile named without .suite recorded %+v", audit.suiteProfiles)
	}
}

func TestANewAuditorRemembersWhichLayersItWasGiven(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		layers    []layer
		branch    bool
		infection bool
	}{
		{name: "no layer at all"},
		{name: "the reach layer alone", layers: []layer{reachLayer()}},
		{name: "the branch layer", layers: []layer{reachLayer(), branchLayer(nil)}, branch: true},
		{name: "the infection layer", layers: []layer{reachLayer(), infectionLayer()}, infection: true},
		{
			name:   "every layer there is",
			layers: []layer{reachLayer(), branchLayer(nil), infectionLayer()},
			branch: true, infection: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			audit := newAuditor(evidence{}, nil, test.layers)
			if audit.result.branchAudited != test.branch || audit.result.infectionAudited != test.infection {
				t.Fatalf("an auditor of %v audits branch %t and infection %t, want %t and %t",
					layerNames(test.layers), audit.result.branchAudited, audit.result.infectionAudited,
					test.branch, test.infection)
			}
		})
	}
}

func TestASuiteProbeCountsAsMeasuredOnlyWhenItMeasured(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		outcome string
		want    int
	}{
		{outcome: trace.ProbeOutcomeMeasured, want: 1},
		{outcome: trace.ProbeOutcomeUnavailable},
		{outcome: trace.ProbeOutcomeTestFailed},
		{outcome: trace.ProbeOutcomeTimedOut},
	} {
		t.Run("outcome "+test.outcome, func(t *testing.T) {
			t.Parallel()
			audit := newAuditor(evidence{}, nil, nil)
			audit.probe(trace.ProbeRecord{
				Suite: true, Target: killerTarget, Outcome: test.outcome,
			})
			if got := audit.result.suiteProbeMeasured; got != test.want {
				t.Fatalf("a suite probe that %s counted %d measured, want %d", test.outcome, got, test.want)
			}
			if audit.result.suiteProbeExecutions != 1 {
				t.Fatalf("a suite probe counted %d executions, want 1", audit.result.suiteProbeExecutions)
			}
		})
	}
}
