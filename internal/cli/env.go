// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"slices"
	"strings"
)

// traceEnvironmentVariable asks for a recording without a flag.
//
// It exists for the invocation nobody can add a flag to: a `go test` that
// drives go-mutants, a CI step whose command line is generated, a script
// somebody else owns. `1` and `true` ask for the default directory and any
// other non-empty value names one.
const traceEnvironmentVariable = "GO_MUTANTS_TRACE"

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
// there is exactly one description of what `--trace` means and one place a
// precedence question is answered.
//
// Three rules hold whatever is exported:
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
	value, _ := lookup(traceEnvironmentVariable)
	flag, requested := traceFlag(value)
	return withEnvironmentFlag(args, flag, requested)
}

// traceFlag is the flag a value of [traceEnvironmentVariable] asks for.
//
// The empty string means no, and deliberately: `GO_MUTANTS_TRACE=` in a CI
// configuration is how a job switches an inherited request off, and reading it
// as a directory name would make every run record into the workspace root.
func traceFlag(value string) (string, bool) {
	switch value {
	case "", "0", "false":
		return "", false
	case "1", "true":
		return "--trace", true
	default:
		return "--trace=" + value, true
	}
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
