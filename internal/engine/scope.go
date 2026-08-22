// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/runner"
)

// wholeModule is the package pattern that means every package in the snapshot.
//
// It is the pattern the built-in test command carries, and the one value of
// [testScope] that is not a narrowing: a scope containing it is the whole
// module however many other patterns sit beside it.
const wholeModule = "./..."

// The `go list` marker [resolveTestScope] counts resolved packages by.
//
// The format prints a fixed prefix and then the package's directory, and both
// halves are load bearing. The prefix is what separates a package row from
// anything else on the stream — internal/runner merges stdout and stderr, so a
// `go: warning: "./docs/..." matched no packages` arrives in the same capture —
// and reading the row structurally rather than matching the toolchain's wording
// is what keeps this from breaking on a release that rephrases a diagnostic.
//
// The directory is what separates a pattern the go command could place from one
// it invented. `go list -e` answers a pattern that names no directory at all
// with a synthetic record whose ImportPath is the pattern itself and whose Dir
// is empty, so a marker alone would count `./nope/...` as a package. A row
// without a directory is the go command saying it found nothing to place.
//
// A row *with* one claims less than it looks like it does, deliberately: it says
// a directory is there, not that a package is. `go list -e ./docs` over a
// directory holding one Markdown file prints its Dir and exits zero, so this
// check passes it and the run goes on to fail at the baseline, where the go
// command refuses the user's own command in its own words — "no Go files in
// ...". That is the division of labour [resolveTestScope] passes `-e` for: a
// pattern that named a real directory is not a pattern that named nothing, and
// which of the go command's file lists add up to a package is a judgement better
// left to the command being measured than made from a template here. Wildcards,
// which are how these patterns are almost always written, do not reach the
// question at all: `./docs/...` matches no package and prints no row.
const (
	scopeMarker = "go-mutants-package\t"
	scopeFormat = scopeMarker + "{{.Dir}}"
)

// testScope reads a test command as a package scope: the patterns it runs, and
// whether go-mutants recognises the command at all.
//
// A command is recognised exactly when it is `go`, then `test`, then one or more
// Go package patterns and nothing else — where a package pattern is `.` or
// anything beginning with `./` and holding no `..`, which is the go command's
// own spelling for "a directory in this module" and the only spelling `go list`
// resolves the same way from the snapshot as from the workspace. The built-in
// `go test ./...` is the trivial recognised case, and everything else — a flag,
// a bare import path, a `..` in any position, a Windows `.\internal\...`,
// another program entirely — is unrecognised.
//
// The strictness is the point, and it is worth saying why a looser reader would
// be worse than no reader. Recognition buys two things the run cannot take back:
// the test binaries are built for these packages *only*, and coverage-guided
// selection is switched on. Both are sound only because a recognised command is
// one whose semantics go-mutants can state in full — these packages' test
// binaries, run once each, nothing else — so that "these lines were reached by
// this binary" attributes to a name the execution phase can act on. A flag can
// change what a `go test` run means: `-run` can select a fraction of it, `-tags`
// can compile different files, `-count` can defeat caching, a `-race` build can
// change which paths are taken. There is no shortlist of harmless flags that
// stays harmless as the go command grows, and the failure is silent in the
// direction that matters — a mutant skipped as uncovered that a test does cover
// is a kill lost and a score inflated. So anything this function has not been
// taught is not recognised, and an unrecognised command loses nothing it has
// today: every binary is built and every mutant is measured against all of them.
//
// It is deliberately not asked where the command was written. A `--` passthrough
// that spells out `go test ./internal/...` is the same scope as a `test.command`
// that does: what makes the reading sound is what the command does.
func testScope(command []string) (patterns []string, ok bool) {
	if len(command) < 3 || command[0] != "go" || command[1] != "test" {
		return nil, false
	}
	for _, arg := range command[2:] {
		if !isPackagePattern(arg) {
			return nil, false
		}
	}
	// Cloned rather than sliced. The result is handed to internal/execute as a
	// build option and outlives the call, and a window onto the caller's argv
	// would make the scope a run is built with something a later edit to the
	// command could change after it was proven.
	return slices.Clone(command[2:]), true
}

// isPackagePattern reports whether arg is a relative Go package pattern.
//
// `.` and anything under `./` with no `..` in it, and nothing else. `/...`
// inside such a pattern is the go command's wildcard and needs no special
// reading here; what is refused is everything that is not rooted in the module
// being measured — a bare import path, which `go list` would resolve from the
// module cache rather than from the snapshot, an absolute path, and a flag,
// which cannot begin with `./` and so is refused by the same rule rather than by
// a second one.
//
// A `..` is refused wherever it appears, including one that climbs out of the
// tree and straight back into it, and the reason is the promise the whole
// reading rests on: a pattern has to mean the same thing resolved from the
// snapshot as from the workspace. A `..` is the one spelling that cannot make
// it. Every pattern here is resolved against the snapshot — a
// `go-mutants-snap-…` copy os.MkdirTemp made in the temporary area — which
// carries neither the workspace's directory name nor any of its siblings, so
// `./../myproject/core` names a real package where the user wrote it and
// nothing at all where the run resolves it, and `./..` is the temporary area.
// Sorting the escaping `..` from the harmless one would be a second rule to get
// wrong, for a spelling nobody writes on purpose and that loses nothing when it
// is refused: an unrecognised command still measures every mutant against every
// binary.
func isPackagePattern(arg string) bool {
	if arg != "." && !strings.HasPrefix(arg, "./") {
		return false
	}
	// Backslashes are folded to slashes before the walk so that `./..\sibling`
	// is refused by the same rule as `./../sibling`. The go command reads the
	// Windows separator, and a reader that knew only about `/` would let the
	// climb through in exactly the spelling this package refuses everywhere
	// else — the same way the missing-`./` rule refuses `..\sibling` outright.
	for element := range strings.SplitSeq(strings.ReplaceAll(arg, `\`, "/"), "/") {
		if element == ".." {
			return false
		}
	}
	return true
}

// narrowed reports whether a recognised scope is smaller than the whole module.
//
// A scope that names `./...` covers every package there is, whatever else is
// written beside it, and is therefore not a claim about which suites matter. The
// distinction exists for exactly one decision — whether a scope that produced no
// test binary at all is a mistake or a fact — and it is documented at the place
// that makes it, [scopedBinaries].
func narrowed(patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == wholeModule {
			return false
		}
	}
	return len(patterns) > 0
}

// resolveTestScope proves that every pattern in a recognised test command names
// at least one package in the snapshot, before anything is built or measured.
//
// One `go list` per pattern, which is what makes the failure name the pattern
// the user got wrong rather than the set it was in. The go command answers a
// pattern that matches nothing with a warning and an exit status of zero — the
// same status it gives a pattern that matched everything — so a single listing
// over the whole set could only report that the total came up short, and "one of
// these four is wrong" is not a diagnosis. Against a run measured in minutes,
// the extra listings cost a second.
//
// The environment is the run's own composed one — the same every other command
// this package issues against the snapshot gets, and deliberately not
// internal/execute's, which sets GOWORK=off so that a `go.work` above the
// snapshot cannot change the package set discovery type-checked. The choice
// follows the question. This asks whether the *user's* command names anything,
// and their command is measured a moment later in exactly this environment; a
// check that answered under a different view of the module would be checking a
// command nobody runs. If a `go.work` ever made the two views disagree, the
// build's listing is the narrower one, and a scope that resolved here and then
// built nothing is what [scopedBinaries] is for.
//
// `-e` is passed, and it is the difference between this check and a worse one. A
// package that does not compile is not a pattern that names nothing: it is the
// snapshot being broken, which [CodeBaselineBuildFailed] exists to say with the
// compiler's own words a moment later. Without `-e` the two would arrive here as
// the same non-zero exit and this function would blame the user's test command
// for it. With `-e` the listing tolerates a package it cannot load and still
// prints where it is, so a non-zero exit from here really is the go command
// refusing to work in this snapshot at all — and its output is carried along.
func resolveTestScope(
	ctx context.Context,
	toolchain gocmd.Toolchain,
	root string,
	env []string,
	patterns []string,
) error {
	for _, pattern := range patterns {
		spec := toolchain.Command("list", "-e", "-f", scopeFormat, pattern)
		spec.Dir = root
		spec.Env = env
		spec.Timeout = BaselineCap

		result := runner.Run(ctx, spec)
		if err := check(ctx, result, CodeTestScope,
			"the package pattern "+strconv.Quote(pattern)+
				" in the test command could not be resolved"); err != nil {
			return err
		}
		if resolvedPackages(result.Output) == 0 {
			return &Error{
				Code: CodeTestScope,
				Message: "the test command names the package pattern " + strconv.Quote(pattern) +
					", which matches no package in the workspace; " +
					"go-mutants builds a test binary for each package the command names, " +
					"and widening the scope back to " + strconv.Quote(wholeModule) +
					" would run the tests the command excludes",
			}
		}
	}
	return nil
}

// resolvedPackages counts the package rows in one `go list` capture.
//
// A row counts when it carries the marker *and* a directory; see [scopeFormat]
// for what each half rules out.
func resolvedPackages(output []byte) int {
	count := 0
	for line := range strings.SplitSeq(string(output), "\n") {
		dir, marked := strings.CutPrefix(strings.TrimRight(line, "\r"), scopeMarker)
		if marked && strings.TrimSpace(dir) != "" {
			count++
		}
	}
	return count
}

// scopedBinaries refuses a narrowed test scope that produced no test binary.
//
// It is the second half of [resolveTestScope] and catches what patterns alone
// cannot: every pattern named a real package, and not one of those packages has
// a test file in it. Nothing downstream would notice. The coverage pass skips a
// run with no binaries without a word, the scheduler walks an empty list, every
// mutant comes back survived, and the run publishes a score of zero as though it
// had looked — which is the same fiction [CodeTestScope] refuses at the pattern.
//
// It is a mistake only for a *narrowed* scope, and that asymmetry is deliberate.
// A whole-module run of a module with no test files anywhere is a fact about the
// project, and the score, `policy.require_mutants` and the survivor list all say
// so already; a command that picked out three packages and got no tests out of
// them is a command that picked the wrong three.
func scopedBinaries(patterns []string, built int) error {
	if built > 0 || !narrowed(patterns) {
		return nil
	}
	return &Error{
		Code: CodeTestScope,
		Message: "the test command's scope " + strconv.Quote(strings.Join(patterns, " ")) +
			" holds no package with a test file, so every mutant would be reported as having " +
			"survived a suite that was never run",
	}
}

// customTestCommand is what [coverage.CodeCustomTestCommand] says: which command
// was configured, which shape would have been understood, and what the run is
// doing instead.
//
// All three, because a user who has just set `test.command` and noticed the run
// got slower cannot act on any one of them alone. The recognised shape is named
// by example rather than by rule — the built-in command, and the observation
// that any `go test` over patterns is read the same way — because the mistake is
// almost always one flag, and "yours has a flag in it" is the sentence that
// fixes it.
func customTestCommand(command []string) string {
	return "coverage-guided selection is off because test.command " +
		strconv.Quote(strings.Join(command, " ")) +
		" is not `go test` over package patterns: go-mutants understands " +
		strconv.Quote(strings.Join(config.DefaultTestCommand(), " ")) +
		" and any other `go test` followed only by patterns such as `./internal/...`, " +
		"and it cannot tell which of its per-package test binaries any other command's coverage " +
		"belongs to, so every mutant will be measured against every one of them"
}
