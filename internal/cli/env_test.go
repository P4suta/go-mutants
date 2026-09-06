// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"slices"
	"testing"
)

// environment builds the lookup [withEnvironmentFlags] takes, out of the
// variables a test wants set. A name that is not in the map is unset, which is
// the case that has to be told apart from one set to the empty string.
func environment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

// TestTraceEnvironmentBecomesTheFlagOnlyForRunAndNeverAfterTheSeparator pins
// the two boundaries the variable has to respect.
//
// `run` is the only command that opens a recording, so it is the only command
// the variable may add a flag to: `--trace` on `list` is an unknown flag, and a
// user who exported the variable once would otherwise find every other command
// refusing to work. And everything after `--` is the test command's own argv,
// which go-mutants passes through verbatim — inserting a flag into somebody
// else's command line would be the one thing the separator promises never
// happens.
func TestTraceEnvironmentBecomesTheFlagOnlyForRunAndNeverAfterTheSeparator(t *testing.T) {
	cases := []struct {
		name  string
		value string
		args  []string
		want  []string
	}{
		{
			name:  "bare run",
			value: "1",
			args:  []string{"run"},
			want:  []string{"run", "--trace"},
		},
		{
			name:  "true is the same as one",
			value: "true",
			args:  []string{"run", "--no-tui"},
			want:  []string{"run", "--no-tui", "--trace"},
		},
		{
			name:  "a value names the directory",
			value: "/var/tmp/recordings",
			args:  []string{"run"},
			want:  []string{"run", "--trace=/var/tmp/recordings"},
		},
		{
			name:  "before the separator, never after it",
			value: "1",
			args:  []string{"run", "--", "go", "test", "./..."},
			want:  []string{"run", "--trace", "--", "go", "test", "./..."},
		},
		{
			name:  "a --trace meant for the test binary is not this flag",
			value: "1",
			args:  []string{"run", "--", "go", "test", "--trace"},
			want:  []string{"run", "--trace", "--", "go", "test", "--trace"},
		},
		{
			name:  "list takes no recording",
			value: "1",
			args:  []string{"list", "--json"},
			want:  []string{"list", "--json"},
		},
		{
			name:  "doctor takes no recording",
			value: "1",
			args:  []string{"doctor"},
			want:  []string{"doctor"},
		},
		{
			name:  "report takes no recording",
			value: "1",
			args:  []string{"report", "latest"},
			want:  []string{"report", "latest"},
		},
		{
			name:  "cache takes no recording",
			value: "1",
			args:  []string{"cache", "status"},
			want:  []string{"cache", "status"},
		},
		{
			name:  "init takes no recording",
			value: "1",
			args:  []string{"init", "--check"},
			want:  []string{"init", "--check"},
		},
		{
			name:  "the empty command line is left alone",
			value: "1",
			args:  nil,
			want:  nil,
		},
		{
			name:  "help is an answer, not a run",
			value: "1",
			args:  []string{"run", "--help"},
			want:  []string{"run", "--help"},
		},
		{
			name:  "version is an answer, not a run",
			value: "1",
			args:  []string{"--version"},
			want:  []string{"--version"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			given := slices.Clone(c.args)
			got := withEnvironmentFlags(c.args, environment(map[string]string{traceEnvironmentVariable: c.value}))
			if !slices.Equal(got, c.want) {
				t.Errorf("withEnvironmentFlags(%q) = %q, want %q", c.args, got, c.want)
			}
			if !slices.Equal(c.args, given) {
				t.Errorf("the arguments were rewritten in place: %q, want %q", c.args, given)
			}
		})
	}
}

// TestAnExplicitTraceFlagWinsOverTheEnvironment keeps the command line the
// last word.
//
// A job may export the variable for every step it cannot add a flag to, and a
// nested invocation inside one has to be able to say something else — including
// a different directory — without unsetting a variable it does not own.
func TestAnExplicitTraceFlagWinsOverTheEnvironment(t *testing.T) {
	cases := []struct {
		name  string
		value string
		args  []string
	}{
		{"a bare flag against a directory", "/var/tmp/recordings", []string{"run", "--trace"}},
		{"a directory against a bare request", "1", []string{"run", "--trace=/var/tmp/here"}},
		{"the same directory twice", "/a", []string{"run", "--trace=/a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := withEnvironmentFlags(c.args, environment(map[string]string{traceEnvironmentVariable: c.value}))
			if !slices.Equal(got, c.args) {
				t.Errorf("withEnvironmentFlags(%q) = %q, want it unchanged", c.args, got)
			}
		})
	}
}

// TestTraceEnvironmentZeroFalseAndEmptyAskForNothing covers the spellings that
// mean "no".
//
// The empty string is the one worth writing down: `GO_MUTANTS_TRACE=` in a CI
// configuration is how a job switches an inherited request off, and reading it
// as a directory name would make every run trace into the workspace root.
func TestTraceEnvironmentZeroFalseAndEmptyAskForNothing(t *testing.T) {
	args := []string{"run", "--no-tui"}
	for _, value := range []string{"", "0", "false"} {
		got := withEnvironmentFlags(args, environment(map[string]string{traceEnvironmentVariable: value}))
		if !slices.Equal(got, args) {
			t.Errorf("GO_MUTANTS_TRACE=%q asked for %q, want no flag", value, got)
		}
	}
	if got := withEnvironmentFlags(args, environment(nil)); !slices.Equal(got, args) {
		t.Errorf("an unset variable asked for %q, want no flag", got)
	}
}
