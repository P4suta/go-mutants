// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/cli"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

type service struct {
	request cli.Request
	command cli.Command
	id      string
	report  report.Report
	err     error
}

func TestHelpListsPublicSurfaceWithoutRunningService(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"--help", "-h"} {
		fake := &service{}
		var stdout, stderr bytes.Buffer
		if exit := cli.Run(t.Context(), []string{flag}, &stdout, &stderr, fake); exit != cli.ExitAssured {
			t.Fatalf("%s exit = %d, stderr = %q", flag, exit, stderr.String())
		}
		for _, expected := range []string{
			"--changed[=REF]", "--contract=standard-v1|deep-v1", "--trace[=DIR]", "GOATEST_TRACE",
			"--keep-temp", "GOATEST_KEEP_TEMP", "init", "explain ID", "replay ID", "accept ID", "report",
			"--ui=auto|plain|jsonl", "--json", "--version", "help [command]",
		} {
			if !strings.Contains(stdout.String(), expected) {
				t.Errorf("%s help omitted %q:\n%s", flag, expected, stdout.String())
			}
		}
		if fake.command != "" {
			t.Errorf("%s ran service command %q", flag, fake.command)
		}
	}
}

func (s *service) Execute(_ context.Context, command cli.Command, request cli.Request, id string) (report.Report, error) {
	s.command, s.request, s.id = command, request, id
	return s.report, s.err
}

func TestDefaultCommandAndGlobalFlags(t *testing.T) {
	t.Parallel()
	fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "deep-v1"}}
	var stdout, stderr bytes.Buffer
	exit := cli.Run(t.Context(), []string{"--changed=origin/main", "--contract=deep-v1", "--json", "--ui=plain"}, &stdout, &stderr, fake)
	if exit != cli.ExitAssured || fake.command != cli.CommandVerify {
		t.Fatalf("exit/command = %d/%s", exit, fake.command)
	}
	if !fake.request.Changed || fake.request.ChangedRef != "origin/main" || fake.request.Contract != "deep-v1" || !fake.request.JSON || fake.request.UI != cli.UIPlain {
		t.Errorf("request = %+v", fake.request)
	}
	var rendered report.Report
	if err := json.Unmarshal(stdout.Bytes(), &rendered); err != nil || rendered.Verdict != report.VerdictAssured || rendered.Contract != "deep-v1" {
		t.Fatalf("JSON output = %+v, %v\n%s", rendered, err, stdout.Bytes())
	}
	if stderr.Len() != 0 {
		t.Errorf("stdout/stderr = %q / %q", stdout.String(), stderr.String())
	}
}

func TestTestBinaryArgumentsAreCanonicalizedAfterSeparator(t *testing.T) {
	t.Parallel()
	fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	exit := cli.Run(t.Context(), []string{"verify", "./...", "--", "-short", "-custom=value"}, &bytes.Buffer{}, &bytes.Buffer{}, fake)
	if exit != cli.ExitAssured {
		t.Fatalf("exit = %d", exit)
	}
	if got, want := fake.request.TestArgs, []string{"-test.short=true", "-custom=value"}; !slices.Equal(got, want) {
		t.Fatalf("test args = %v, want %v", got, want)
	}
}

func TestBareChangedFlagAndCancellationArePreserved(t *testing.T) {
	t.Parallel()
	changed := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	if exit := cli.Run(t.Context(), []string{"--changed"}, &bytes.Buffer{}, &bytes.Buffer{}, changed); exit != cli.ExitAssured || !changed.request.Changed || changed.request.ChangedRef != "" {
		t.Fatalf("bare changed = exit %d request %+v", exit, changed.request)
	}

	cancelled := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictError}, err: context.Canceled}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := cli.Run(t.Context(), []string{"verify"}, &stdout, &stderr, cancelled); exit != cli.ExitInterrupted ||
		stderr.String() != "goatest: interrupted: context canceled\n" || stdout.Len() != 0 {
		t.Fatalf("cancellation = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}

func TestTraceFlagAsksForADefaultOrANamedDirectory(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		args      []string
		command   cli.Command
		id        string
		directory string
	}{
		{name: "default", args: []string{"--trace"}, command: cli.CommandVerify},
		{name: "named", args: []string{"verify", "--trace=/tmp/goatest-trace"}, command: cli.CommandVerify, directory: "/tmp/goatest-trace"},
		{name: "empty-value", args: []string{"verify", "--trace="}, command: cli.CommandVerify},
		{name: "replay", args: []string{"replay", "finding-a", "--trace"}, command: cli.CommandReplay, id: "finding-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
			exit := cli.Run(t.Context(), test.args, &bytes.Buffer{}, &bytes.Buffer{}, fake)
			if exit != cli.ExitAssured || fake.command != test.command || fake.id != test.id {
				t.Fatalf("%v => exit %d command %s id %q", test.args, exit, fake.command, fake.id)
			}
			if !fake.request.Trace || fake.request.TraceDirectory != test.directory {
				t.Fatalf("%v => trace %t directory %q", test.args, fake.request.Trace, fake.request.TraceDirectory)
			}
		})
	}
	unrequested := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	if exit := cli.Run(t.Context(), []string{"verify"}, &bytes.Buffer{}, &bytes.Buffer{}, unrequested); exit != cli.ExitAssured || unrequested.request.Trace {
		t.Fatalf("unrequested verify = exit %d request %+v", exit, unrequested.request)
	}
	separated := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	if exit := cli.Run(t.Context(), []string{"verify", "--", "--trace"}, &bytes.Buffer{}, &bytes.Buffer{}, separated); exit != cli.ExitAssured || separated.request.Trace {
		t.Fatalf("test-binary --trace = exit %d request %+v", exit, separated.request)
	}
}

func TestKeepTempFlagIsAcceptedByTheCommandsThatAccountForWhatTheyKeep(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		args    []string
		command cli.Command
		id      string
	}{
		{name: "default command", args: []string{"--keep-temp"}, command: cli.CommandVerify},
		{name: "verify", args: []string{"verify", "--keep-temp"}, command: cli.CommandVerify},
		{name: "traced verify", args: []string{"verify", "--trace", "--keep-temp"}, command: cli.CommandVerify},
		{name: "replay", args: []string{"replay", "finding-a", "--keep-temp"}, command: cli.CommandReplay, id: "finding-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
			exit := cli.Run(t.Context(), test.args, &bytes.Buffer{}, &bytes.Buffer{}, fake)
			if exit != cli.ExitAssured || fake.command != test.command || fake.id != test.id {
				t.Fatalf("%v => exit %d command %s id %q", test.args, exit, fake.command, fake.id)
			}
			if !fake.request.KeepTemp {
				t.Fatalf("%v => request %+v", test.args, fake.request)
			}
		})
	}
	unrequested := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	if exit := cli.Run(t.Context(), []string{"verify"}, &bytes.Buffer{}, &bytes.Buffer{}, unrequested); exit != cli.ExitAssured || unrequested.request.KeepTemp {
		t.Fatalf("unrequested verify = exit %d request %+v", exit, unrequested.request)
	}
	separated := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	if exit := cli.Run(t.Context(), []string{"verify", "--", "--keep-temp"}, &bytes.Buffer{}, &bytes.Buffer{}, separated); exit != cli.ExitAssured || separated.request.KeepTemp {
		t.Fatalf("test-binary --keep-temp = exit %d request %+v", exit, separated.request)
	}

	for _, args := range [][]string{{"verify", "--keep-temp=1"}, {"verify", "--keep-temp=maybe"}} {
		refusing := &service{}
		var stderr bytes.Buffer
		if exit := cli.Run(t.Context(), args, &bytes.Buffer{}, &stderr, refusing); exit != cli.ExitError ||
			refusing.command != "" || !strings.Contains(stderr.String(), "keep-temp") {
			t.Fatalf("%v => exit %d command %q stderr %q", args, exit, refusing.command, stderr.String())
		}
	}
}

func TestSubcommandsRequireTheirDocumentedArguments(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		args    []string
		command cli.Command
		id      string
	}{
		{[]string{"init"}, cli.CommandInit, ""},
		{[]string{"explain", "finding-a"}, cli.CommandExplain, "finding-a"},
		{[]string{"replay", "finding-b"}, cli.CommandReplay, "finding-b"},
		{[]string{"accept", "finding-c", "--reason=reviewed", "--expires=2026-12-01T00:00:00Z"}, cli.CommandAccept, "finding-c"},
		{[]string{"report"}, cli.CommandReport, ""},
		{[]string{"cache", "flush"}, cli.CommandCache, "flush"},
		{[]string{"cache", "status"}, cli.CommandCache, "status"},
		{[]string{"cache", "gc"}, cli.CommandCache, "gc"},
		{[]string{"report", "--latest-full"}, cli.CommandReport, ""},
		{[]string{"report", "--run=run-a"}, cli.CommandReport, ""},
		{[]string{"trace", "summary"}, cli.CommandTrace, "summary"},
		{[]string{"trace", "diff", "run-a", "run-b"}, cli.CommandTrace, "diff"},
	} {
		fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
		exit := cli.Run(t.Context(), test.args, &bytes.Buffer{}, &bytes.Buffer{}, fake)
		if exit != cli.ExitAssured || fake.command != test.command || fake.id != test.id {
			t.Errorf("%v => %d/%s/%q", test.args, exit, fake.command, fake.id)
		}
	}
	for _, args := range [][]string{
		{"explain"}, {"accept"}, {"unknown"}, {"--contract=bad"}, {"--unknown"},
		{"--no-tui"}, {"--no-apply"},
		{"verify", "--apply"}, {"doctor", "--changed"}, {"init", "--contract=deep-v1"},
		{"verify", "--latest-full"}, {"fix", "--reason=reviewed"}, {"report", "--apply"},
		{"doctor", "--trace"}, {"plan", "--trace=out"}, {"report", "--trace"}, {"fix", "--trace"},
		{"doctor", "--keep-temp"}, {"plan", "--keep-temp"}, {"report", "--keep-temp"}, {"fix", "--keep-temp"},
		{"plan", "--", "-short"}, {"doctor", "--", "-short"}, {"report", "--", "-short"},
		{"init", "extra"}, {"report", "extra"}, {"replay", ""}, {"accept", "finding-c"},
		{"trace"}, {"trace", "summary", "a", "b"}, {"trace", "diff", "a"}, {"trace", "unknown"},
		{"--json", "--ui=jsonl"}, {"--ui=bad"},
		{"report", "--latest-full", "--run=run-a"},
		{"accept", "finding-c", "--reason=reviewed"},
		{"accept", "finding-c", "--expires=2026-12-01T00:00:00Z"},
		{"accept", "finding-c", "--reason=  ", "--expires=2026-12-01T00:00:00Z"},
		{"accept", "finding-c", "--reason=reviewed", "--expires=  "},
		{"cache"}, {"cache", "bad"}, {"cache", "status", "extra"},
		{"verify", "--owner=me"}, {"verify", "--ticket=T-1"}, {"verify", "--expires=2026-12-01T00:00:00Z"},
		{"verify", "--", "-test.run=TestX"},
	} {
		var stderr bytes.Buffer
		if exit := cli.Run(t.Context(), args, &bytes.Buffer{}, &stderr, &service{}); exit != cli.ExitError || stderr.Len() == 0 {
			t.Errorf("%v => exit %d stderr %q", args, exit, stderr.String())
		}
	}
}

func TestTraceReaderArgumentsReachServiceWithoutBecomingVerificationScope(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		args []string
		id   string
		ids  []string
	}{
		{args: []string{"trace", "summary", "run-a"}, id: "summary", ids: []string{"run-a"}},
		{args: []string{"trace", "diff", "run-a", "run-b"}, id: "diff", ids: []string{"run-a", "run-b"}},
	} {
		fake := &service{report: report.Report{Verdict: report.VerdictCompleted}}
		if exit := cli.Run(t.Context(), test.args, &bytes.Buffer{}, &bytes.Buffer{}, fake); exit != cli.ExitAssured || fake.command != cli.CommandTrace || fake.id != test.id || !slices.Equal(fake.request.IDs, test.ids) {
			t.Fatalf("%v => exit=%d command=%s id=%q request=%+v", test.args, exit, fake.command, fake.id, fake.request)
		}
	}
}

func TestVerdictsMapToStableExitCodes(t *testing.T) {
	t.Parallel()
	for verdict, want := range map[report.Verdict]int{
		report.VerdictAssured:       cli.ExitAssured,
		report.VerdictChangeAssured: cli.ExitAssured,
		report.VerdictScopeAssured:  cli.ExitAssured,
		report.VerdictResolved:      cli.ExitAssured,
		report.VerdictCompleted:     cli.ExitAssured,
		report.VerdictDefect:        cli.ExitDefect,
		report.VerdictReproduced:    cli.ExitDefect,
		report.VerdictInsufficient:  cli.ExitInsufficient,
		report.VerdictError:         cli.ExitError,
	} {
		fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: verdict}}
		if got := cli.Run(t.Context(), []string{"verify"}, &bytes.Buffer{}, &bytes.Buffer{}, fake); got != want {
			t.Errorf("%s => %d, want %d", verdict, got, want)
		}
	}
}

func TestErrorsEscapeTerminalControlCharactersOntoOneLine(t *testing.T) {
	t.Parallel()
	fake := &service{err: errors.New("failed\nFINDING forged\x1b[31m")}
	var stderr bytes.Buffer
	if exit := cli.Run(t.Context(), []string{"verify"}, &bytes.Buffer{}, &stderr, fake); exit != cli.ExitError {
		t.Fatalf("exit = %d", exit)
	}
	if got, want := stderr.String(), "goatest: failed\\nFINDING forged\\u001b[31m\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestErrorPrefixIsNeverDoubled(t *testing.T) {
	t.Parallel()
	for _, wrapped := range []string{
		"goatest: read latest report: file is absent",
		"goatest: goatest: read latest report: file is absent",
	} {
		fake := &service{err: errors.New(wrapped)}
		var stderr bytes.Buffer
		if exit := cli.Run(t.Context(), []string{"report"}, &bytes.Buffer{}, &stderr, fake); exit != cli.ExitError {
			t.Fatalf("exit = %d", exit)
		}
		if got, want := stderr.String(), "goatest: read latest report: file is absent\n"; got != want {
			t.Fatalf("stderr = %q, want %q", got, want)
		}
	}
}

func TestBareInvocationShowsHelpWithoutRunningService(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {}} {
		fake := &service{}
		var stdout, stderr bytes.Buffer
		if exit := cli.Run(t.Context(), args, &stdout, &stderr, fake); exit != cli.ExitAssured {
			t.Fatalf("%v exit = %d stderr = %q", args, exit, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "Usage:") || fake.command != "" || stderr.Len() != 0 {
			t.Fatalf("%v stdout = %q command = %q stderr = %q", args, stdout.String(), fake.command, stderr.String())
		}
	}
}

func TestCommandHelpIsAvailablePerSubcommand(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"verify", "--help"}, {"help", "verify"}, {"--help", "verify"}, {"verify", "-h"},
	} {
		fake := &service{}
		var stdout, stderr bytes.Buffer
		if exit := cli.Run(t.Context(), args, &stdout, &stderr, fake); exit != cli.ExitAssured || fake.command != "" {
			t.Fatalf("%v exit = %d command = %q stderr = %q", args, exit, fake.command, stderr.String())
		}
		if !strings.Contains(stdout.String(), "goatest verify [packages...]") || !strings.Contains(stdout.String(), "--changed[=REF]") {
			t.Fatalf("%v stdout = %q", args, stdout.String())
		}
	}
	for _, command := range []string{"plan", "doctor", "init", "explain", "replay", "accept", "fix", "report", "cache", "trace"} {
		fake := &service{}
		var stdout, stderr bytes.Buffer
		if exit := cli.Run(t.Context(), []string{"help", command}, &stdout, &stderr, fake); exit != cli.ExitAssured || fake.command != "" {
			t.Fatalf("help %s exit = %d command = %q stderr = %q", command, exit, fake.command, stderr.String())
		}
		if !strings.Contains(stdout.String(), "goatest "+command) {
			t.Fatalf("help %s stdout = %q", command, stdout.String())
		}
	}
	unknown := &service{}
	var stdout, stderr bytes.Buffer
	if exit := cli.Run(t.Context(), []string{"help", "nope"}, &stdout, &stderr, unknown); exit != cli.ExitError || unknown.command != "" {
		t.Fatalf("help nope exit = %d command = %q", exit, unknown.command)
	}
	if !strings.Contains(stderr.String(), `unknown command "nope"`) || !strings.Contains(stderr.String(), "goatest --help") {
		t.Fatalf("help nope stderr = %q", stderr.String())
	}

	separated := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	if exit := cli.Run(t.Context(), []string{"verify", "--", "--help"}, &bytes.Buffer{}, &bytes.Buffer{}, separated); exit != cli.ExitAssured || separated.command != cli.CommandVerify {
		t.Fatalf("separated --help exit = %d command = %q", exit, separated.command)
	}
}

func TestParseErrorsPointAtHelp(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		args []string
		hint string
	}{
		{args: []string{"--unknown"}, hint: "run 'goatest --help' for usage"},
		{args: []string{"--ui=fancy"}, hint: "run 'goatest --help' for usage"},
		{args: []string{"accept", "finding-a"}, hint: "run 'goatest help accept' for usage"},
		{args: []string{"cache", "prune"}, hint: "run 'goatest help cache' for usage"},
		{args: []string{"doctor", "--changed"}, hint: "run 'goatest help doctor' for usage"},
		{args: []string{"report", "--apply"}, hint: "run 'goatest help report' for usage"},
	} {
		var stderr bytes.Buffer
		if exit := cli.Run(t.Context(), test.args, &bytes.Buffer{}, &stderr, &service{}); exit != cli.ExitError {
			t.Fatalf("%v exit = %d", test.args, exit)
		}
		if !strings.HasPrefix(stderr.String(), "goatest: ") || !strings.Contains(stderr.String(), test.hint) {
			t.Fatalf("%v stderr = %q, want hint %q", test.args, stderr.String(), test.hint)
		}
	}
}

func TestInfrastructureErrorsRenderTheirErrorReportBeforeTheDiagnostic(t *testing.T) {
	t.Parallel()
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "lines", true: "json"}[jsonOutput], func(t *testing.T) {
			t.Parallel()
			result := report.Report{
				Schema: report.SchemaV1, Verdict: report.VerdictError, Contract: "standard-v1",
				Findings: []report.Finding{{ID: "infrastructure-error", Kind: "infrastructure", Summary: "workspace failed"}},
			}
			fake := &service{report: result, err: errors.New("workspace failed")}
			var stdout, stderr bytes.Buffer
			arguments := []string{"verify"}
			if jsonOutput {
				arguments = []string{"verify", "--json"}
			}
			if exit := cli.Run(t.Context(), arguments, &stdout, &stderr, fake); exit != cli.ExitError {
				t.Fatalf("exit = %d", exit)
			}
			if jsonOutput {
				var rendered report.Report
				if err := json.Unmarshal(stdout.Bytes(), &rendered); err != nil || rendered.Verdict != report.VerdictError {
					t.Fatalf("JSON ERROR report = %+v, %v\n%s", rendered, err, stdout.Bytes())
				}
			} else if !strings.HasPrefix(stdout.String(), "ERROR standard-v1") {
				t.Fatalf("line ERROR report = %q", stdout.String())
			}
			if got, want := stderr.String(), "goatest: workspace failed\n"; got != want {
				t.Fatalf("stderr = %q, want %q", got, want)
			}
		})
	}
}

func TestHelpReadsOnlyTheArgumentsBeforeTheSeparator(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	fake := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	if exit := cli.Run(t.Context(), []string{"verify", "--", "--help"}, &stdout, &stderr, fake); exit != cli.ExitAssured {
		t.Fatalf("exit = %d, stderr = %q", exit, stderr.String())
	}
	if fake.command != cli.CommandVerify {
		t.Errorf("command = %q, want a verify: --help after the separator is the test binary's", fake.command)
	}
	if strings.Contains(stdout.String(), "Usage") {
		t.Error("the help text was printed for a --help that belongs to the test binary")
	}

	stdout.Reset()
	stderr.Reset()
	bare := &service{report: report.Report{Schema: report.SchemaV1, Verdict: report.VerdictAssured}}
	if exit := cli.Run(t.Context(), []string{"--", "--help"}, &stdout, &stderr, bare); exit != cli.ExitAssured {
		t.Fatalf("exit = %d, stderr = %q", exit, stderr.String())
	}
	if bare.command != cli.CommandVerify {
		t.Errorf("command = %q, want a verify: a separator at the front leaves nothing before it", bare.command)
	}
}

func TestHelpForAnUnknownCommandSaysSoAndFails(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if exit := cli.Run(t.Context(), []string{"help", "nonesuch"}, &stdout, &stderr, &service{}); exit != cli.ExitError {
		t.Fatalf("exit = %d, want ExitError", exit)
	}
	if !strings.Contains(stderr.String(), `unknown command "nonesuch"`) {
		t.Errorf("stderr = %q, want it to name the command", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want no help text for a command there is none for", stdout.String())
	}
	if lines := strings.Count(strings.TrimRight(stderr.String(), "\n"), "\n") + 1; lines != 2 {
		t.Errorf("stderr holds %d lines:\n%s\nwant the refusal and where to look, said once", lines, stderr.String())
	}
}

func TestAnInterruptedRunRendersOnlyAReportThatHasARunID(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		result   report.Report
		rendered bool
	}{
		{"a run that started", report.Report{Schema: report.SchemaV1, RunID: "run-a", Verdict: report.VerdictError}, true},
		{"a run that never did", report.Report{Schema: report.SchemaV1, Verdict: report.VerdictError}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			fake := &service{report: test.result, err: context.Canceled}
			if exit := cli.Run(t.Context(), []string{"verify"}, &stdout, &stderr, fake); exit != cli.ExitInterrupted {
				t.Fatalf("exit = %d, want ExitInterrupted", exit)
			}
			if rendered := stdout.Len() != 0; rendered != test.rendered {
				t.Errorf("rendered = %t, want %t; stdout = %q", rendered, test.rendered, stdout.String())
			}
		})
	}
}

func TestAFailedRunRendersOnlyAnErrorVerdict(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		verdict  report.Verdict
		rendered bool
	}{
		{report.VerdictError, true},
		{report.VerdictInsufficient, false},
	} {
		var stdout, stderr bytes.Buffer
		fake := &service{report: report.Report{Schema: report.SchemaV1, RunID: "run-a", Verdict: test.verdict}, err: errors.New("stopped")}
		if exit := cli.Run(t.Context(), []string{"verify"}, &stdout, &stderr, fake); exit != cli.ExitError {
			t.Fatalf("%s exit = %d", test.verdict, exit)
		}
		if rendered := stdout.Len() != 0; rendered != test.rendered {
			t.Errorf("%s rendered = %t, want %t", test.verdict, rendered, test.rendered)
		}
	}
}

func TestTheJSONLRenderingIsChosenByTheUIFlag(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		args  []string
		jsonl bool
	}{
		{[]string{"verify", "--ui=jsonl"}, true},
		{[]string{"verify", "--ui=plain"}, false},
	} {
		var stdout, stderr bytes.Buffer
		fake := &service{report: report.Report{Schema: report.SchemaV1, RunID: "run-a", Verdict: report.VerdictAssured}}
		if exit := cli.Run(t.Context(), test.args, &stdout, &stderr, fake); exit != cli.ExitAssured {
			t.Fatalf("%v exit = %d, stderr = %q", test.args, exit, stderr.String())
		}
		if jsonl := strings.Contains(stdout.String(), `"report"`); jsonl != test.jsonl {
			t.Errorf("%v rendered jsonl = %t, want %t; stdout = %q", test.args, jsonl, test.jsonl, stdout.String())
		}
	}
}
