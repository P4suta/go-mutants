// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/P4suta/go-mutants/internal/console"
)

func verboseOptions(t *testing.T, args ...string) *runOptions {
	t.Helper()
	o := &runOptions{}
	cmd := newRunCommandWith(o)
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parsing %v: %v", args, err)
	}
	return o
}

func TestVerboseCountsTheTimesItWasTyped(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int
	}{
		{args: nil, want: 0},
		{args: []string{"-v"}, want: 1},
		{args: []string{"--verbose"}, want: 1},
		{args: []string{"-vv"}, want: 2},
		{args: []string{"-v", "-v"}, want: 2},
		{args: []string{"-vvv"}, want: 3},
	} {
		if got := verboseOptions(t, tc.args...).verbose; got != tc.want {
			t.Errorf("run %v: verbose = %d, want %d", tc.args, got, tc.want)
		}
	}
	if got := verboseOptions(t, "-vvv").verbosity(); got != console.VerbosityTrace {
		t.Errorf("-vvv resolved to verbosity %d, want %d", got, console.VerbosityTrace)
	}
}

func TestVerboseAndQuietAreRefused(t *testing.T) {
	err := runWith(t, "-v", "--quiet")
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeConflictingFlags {
		t.Fatalf("run -v --quiet = %v, want %s", err, CodeConflictingFlags)
	}
	if coded.Hint == "" {
		t.Error("the conflict names no remedy")
	}
	if !strings.Contains(coded.Message, "--verbose") || !strings.Contains(coded.Message, "--quiet") {
		t.Errorf("the refusal does not name both flags: %s", coded.Message)
	}
}

func TestVerboseAndJSONAreRefused(t *testing.T) {
	err := runWith(t, "-vv", "--json")
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeConflictingFlags {
		t.Fatalf("run -vv --json = %v, want %s", err, CodeConflictingFlags)
	}
	if coded.Hint == "" {
		t.Error("the conflict names no remedy")
	}
}

func TestVerboseImpliesThePlainRenderer(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "")
	for verbosity := 1; verbosity <= 2; verbosity++ {
		options := runOptions{verbose: verbosity}
		if wantsDashboard(io.Discard, &options, probing(true, colorprofile.TrueColor)) {
			t.Errorf("-%s chose the dashboard on a colour terminal", strings.Repeat("v", verbosity))
		}
	}
	options := runOptions{}
	if !wantsDashboard(io.Discard, &options, probing(true, colorprofile.TrueColor)) {
		t.Error("a run with no -v lost its dashboard")
	}
}

func TestVerboseTwoAsksTheEngineToPublishTrace(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{args: nil, want: false},
		{args: []string{"-v"}, want: true},
		{args: []string{"-vv"}, want: true},
	} {
		if got := verboseOptions(t, tc.args...).publishTrace(); got != tc.want {
			t.Errorf("run %v: publishTrace = %t, want %t", tc.args, got, tc.want)
		}
	}
}

func TestRunHelpMentionsVerbose(t *testing.T) {
	code, stdout, stderr := execute(t, "run", "--help")
	if code != 0 {
		t.Fatalf("`run --help` exited %d\n%s", code, stderr)
	}
	for _, want := range []string{"-v, --verbose", "-vv"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("`run --help` does not mention %q:\n%s", want, stdout)
		}
	}
}
