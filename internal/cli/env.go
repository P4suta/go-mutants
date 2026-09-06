// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"slices"
	"strconv"
	"strings"
)

// traceEnvironmentVariable asks for a recording without a flag.
//
// It exists for the invocation nobody can add a flag to: a `go test` that
// drives go-mutants, a CI step whose command line is generated, a script
// somebody else owns. `1` and `true` ask for the default directory and any
// other non-empty value names one.
const traceEnvironmentVariable = "GO_MUTANTS_TRACE"

// keepTempEnvironmentVariable asks for the run's temporary directories to be
// left behind without a flag, and diagnosticsEnvironmentVariable switches the
// bundle a failed run writes off.
//
// Both exist for the invocation nobody can add a flag to, which is
// [traceEnvironmentVariable]'s reason and the same reason: a CI step whose
// command line is generated, a script somebody else owns, a `go test` that
// drives go-mutants. The pair is deliberately asymmetric, because the defaults
// are: keeping is off and the variable turns it on, the bundle is on and the
// variable turns it off.
const (
	keepTempEnvironmentVariable    = "GO_MUTANTS_KEEP_TEMP"
	diagnosticsEnvironmentVariable = "GO_MUTANTS_DIAGNOSTICS"
)

// tracingCommand is the only command an environment request may add a flag to.
// See [withEnvironmentFlags].
const tracingCommand = "run"

// withEnvironmentFlags turns the environment's requests into the flags the
// command layer already parses, and returns the argument vector to run.
//
// This is the whole of go-mutants' reading of the environment for these
// options, and it happens in [Execute] alone. Nothing below the command line
// asks what is exported: a flag is a value the command tree validates, renders
// in `--help`, and reports a mistake in by name, and a variable read three
// layers down is none of those things. Turning one into the other here means
// there is exactly one description of what each option means and one place a
// precedence question is answered.
//
// Three variables are read — the recording, the keep, and the bundle — and
// three rules hold whatever is exported:
//
//   - Only `run` is given a flag. `--trace` is an unknown flag everywhere else,
//     and a user who exported the variable once would otherwise find every
//     other command refusing to work.
//   - Nothing is inserted after `--`. Everything there is the test command's own
//     argv, passed through verbatim; adding a flag to somebody else's command
//     line is the one thing the separator promises never happens.
//   - An explicit flag wins. A job may export the variable for every step it
//     cannot reach, and a nested invocation has to be able to say something else
//     without unsetting a variable it does not own.
//
// lookup is [os.LookupEnv] in production and a map in the tests, which is what
// keeps the rules above testable without a process-wide variable.
func withEnvironmentFlags(args []string, lookup func(string) (string, bool)) []string {
	// In a fixed order, so that a job exporting all three produces one argument
	// vector rather than one per map iteration.
	requests := []struct {
		variable string
		flag     func(string) (string, bool)
	}{
		{traceEnvironmentVariable, traceFlag},
		{keepTempEnvironmentVariable, keepTempFlag},
		{diagnosticsEnvironmentVariable, diagnosticsFlag},
	}
	for _, request := range requests {
		value, _ := lookup(request.variable)
		flag, requested := request.flag(value)
		args = withEnvironmentFlag(args, flag, requested)
	}
	return args
}

// traceFlag is the flag a value of [traceEnvironmentVariable] asks for.
//
// The empty string means no, and deliberately: `GO_MUTANTS_TRACE=` in a CI
// configuration is how a job switches an inherited request off, and reading it
// as a directory name would make every run record into the workspace root.
//
// This is the one of the three variables whose non-boolean values mean
// something — they name the directory to record into — and that is exactly why
// it has to agree with the other two about which values are boolean at all. A
// user who learns that `TRUE` works for one of them and then finds this one
// recording into a directory called `TRUE` has learned that go-mutants has no
// rule, only three implementations of one.
func traceFlag(value string) (string, bool) {
	switch exported(value) {
	case requestUnset, requestOff:
		return "", false
	case requestOn:
		return "--trace", true
	case requestOther:
	}
	return "--trace=" + value, true
}

// A request is what one environment variable's value asks for, before anything
// knows which option it is about.
//
// Three states rather than a boolean, because the two variables here have
// opposite defaults: an unset `GO_MUTANTS_KEEP_TEMP` keeps nothing and an unset
// `GO_MUTANTS_DIAGNOSTICS` writes the bundle, so "said nothing" cannot be folded
// into either answer.
type request int

const (
	// requestUnset is a variable that is not set, or set to nothing. The second
	// is not a mistake: `GO_MUTANTS_TRACE=` in a CI configuration is how a job
	// switches an inherited request back off, and it means the same thing as
	// never having exported it.
	requestUnset request = iota
	// requestOn and requestOff are the two answers [strconv.ParseBool] gives.
	requestOn
	requestOff
	// requestOther is a value only the option itself can judge — a directory
	// for `--trace`, a mode for `--keep-temp`, a typo for either.
	requestOther
)

// exported reads one variable's value.
//
// The yes and the no are [strconv.ParseBool]'s and nothing of this package's
// own, which is the point. A CI file is written by a person, and a person
// switching a feature on writes `TRUE` as readily as `true`; a reader that took
// one spelling and treated the other as something else entirely would be a trap
// whose failure mode depends on the shift key. ParseBool is the vocabulary every
// Go program on the machine already answers to — `1`, `t`, `T`, `TRUE`, `true`,
// `True` and their negatives — so there is one list of spellings and it is not
// this file's.
func exported(value string) request {
	if value == "" {
		return requestUnset
	}
	on, err := strconv.ParseBool(value)
	switch {
	case err != nil:
		return requestOther
	case on:
		return requestOn
	default:
		return requestOff
	}
}

// keepTempFlag is the flag a value of [keepTempEnvironmentVariable] asks for.
//
// Off — and `never`, which is the word the engine's zero mode prints for the
// same thing — asks for no flag at all. On asks for the mode the bare flag
// carries, which is the only reading of "yes" that means anything here.
//
// Anything else becomes the flag's own value rather than being judged here, and
// a mistake in it is refused by the flag. That is what keeps one description of
// what `--keep-temp` accepts and one message when something else arrives: a
// second table of mode names in this file would be a second thing to keep in
// step with the first.
func keepTempFlag(value string) (string, bool) {
	switch exported(value) {
	case requestUnset, requestOff:
		return "", false
	case requestOn:
		return "--keep-temp=" + keepTempAlways, true
	case requestOther:
	}
	// `never` is a mode `--keep-temp` accepts and the word KeepTempNever prints,
	// so the variable reads it as the same request: off, and therefore no flag
	// rather than a flag asking for nothing.
	if strings.TrimSpace(value) == keepTempNever {
		return "", false
	}
	return "--keep-temp=" + value, true
}

// diagnosticsFlag is the flag a value of [diagnosticsEnvironmentVariable] asks
// for, and is the one variable here whose default is on.
//
// So an unset variable means on rather than off — a bundle is written for a
// failed run unless somebody says otherwise, and a variable nobody set has said
// nothing. An unrecognised value becomes `--no-diagnostics=<value>`, which pflag
// refuses by name: a typo in a CI file should be a message rather than a
// silently different default, and this is the switch where guessing wrong the
// quiet way takes away the diagnosis of a failure nobody can reproduce.
func diagnosticsFlag(value string) (string, bool) {
	switch exported(value) {
	case requestUnset, requestOn:
		return "", false
	case requestOff:
		return "--no-diagnostics", true
	case requestOther:
	}
	return "--no-diagnostics=" + value, true
}

// withEnvironmentFlag inserts one flag into an argument vector, or leaves it
// exactly as it is. The rules are [withEnvironmentFlags]'s.
//
// The vector is never rewritten in place: [Execute] passes os.Args[1:], and a
// helper that appended to it would be editing the process's own arguments.
func withEnvironmentFlag(args []string, flag string, requested bool) []string {
	if !requested || len(args) == 0 {
		return args
	}
	separator := slices.Index(args, "--")
	head := args
	if separator >= 0 {
		head = args[:separator]
	}
	if !runCommand(head) || hasFlag(head, flag) {
		return args
	}
	if separator >= 0 {
		return slices.Insert(slices.Clone(args), separator, flag)
	}
	return append(slices.Clone(args), flag)
}

// runCommand reports whether the arguments before `--` invoke `run` rather than
// another command, help, or the version.
//
// The command is the first argument that is not a flag, because that is where
// cobra finds it too. Help and the version are answers rather than runs, and a
// flag added to one would turn "what does this do" into "unknown flag".
func runCommand(head []string) bool {
	command := ""
	for _, argument := range head {
		switch argument {
		case "--help", "-h", "--version":
			return false
		}
		if command == "" && !strings.HasPrefix(argument, "-") {
			command = argument
		}
	}
	return command == tracingCommand
}

// hasFlag reports whether the flag, in either spelling, is already on the
// command line. It is what makes an explicit flag win over the environment.
func hasFlag(head []string, flag string) bool {
	name, _, _ := strings.Cut(flag, "=")
	for _, argument := range head {
		if argument == name || strings.HasPrefix(argument, name+"=") {
			return true
		}
	}
	return false
}
