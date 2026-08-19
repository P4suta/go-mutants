// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"errors"
	"strings"
	"testing"
)

// maxFuzzDocument is the largest input [FuzzParse] examines: far above any
// configuration a person writes, and far below the size at which one execution
// costs more than the information it returns.
const maxFuzzDocument = 64 << 10

// FuzzParse asserts the properties that have to hold for every byte sequence
// somebody might put in a configuration file, valid or not.
//
// The decoder is the one part of go-mutants that reads untrusted-shaped input
// before anything else runs, and its failure mode matters: a panic there is a
// stack trace instead of a message, on a file the user can see and fix. So the
// target checks three things at once — Parse never panics, every refusal
// arrives as a *[Error] carrying a code from this package's block, and a
// document that is accepted can only fail after the merge for a reason no
// single layer could have judged.
//
// The second of those is the property with teeth. It is what makes "the CLI
// can map any configuration failure to an exit status and a code" a checked
// claim rather than an intention, and it is exactly what an untyped error
// escaping from a new validation branch would break.
//
// The third is deliberately not "an accepted document always validates". That
// stronger claim is false, and correctly so: a file setting only `report.high
// = 0` is fine on its own and contradicts the default `report.low` once merged.
// What must hold is that merging invents no *new kind* of problem — every
// per-value rule has already run at the layer that can point at the line, so a
// post-merge failure can only be one of the cross-field rules.
//
// # Running it
//
// The nightly job that drives this target has to pass a small
// `-fuzzminimizetime`; two seconds is plenty. Go's fuzzing engine queues every
// coverage-expanding input for minimization and gives each one up to a minute
// by default, and this target finds new coverage often enough that minimizing
// takes every worker. A run in that state prints `execs: N (0/sec)` for the
// rest of its budget and then passes, having explored nothing — which reads
// exactly like a hang and is not one. Measured here: 45s at the default budget
// reached 116k executions and 38 new inputs, all of them inside the first
// twelve seconds; 30s with `-fuzzminimizetime=1s` reached 1.6M executions and
// 321 new inputs.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"",
		"version = 1\n",
		"version = 1\r\n[report]\r\nhigh = 80\r\n",
		"version = 0\n",
		"version = \"1\"\n",
		"version = 99999999999999999999\n",
		"[mutation]\n",
		"version = 1\n[mutation]\ninclude = []\n",
		"version = 1\n[mutation]\ninclude = [\"\"]\n",
		"version = 1\n[mutation]\ninclude = [\"**/*.go\"]\nexclude = [\"a//b\"]\n",
		"version = 1\n[mutation]\noperators = [\"comparison\"]\nprofile = \"all\"\n",
		"version = 1\n[[mutation.expect]]\nid = \"" + strings.Repeat("a", 64) + "\"\nreason = \"r\"\n",
		"version = 1\n[[mutation.expect]]\n",
		"version = 1\n[mutation]\nexpect = [{ id = \"x\", reason = \"\" }]\n",
		"version = 1\n[test]\ncommand = []\ntimeout = \"\"\nbaseline_runs = -1\n",
		"version = 1\n[test]\ntimeout = \"9223372036854775807h\"\n",
		"version = 1\n[execution]\njobs = 9223372036854775807\n",
		"version = 1\n[cache]\nmode = \"auto\"\ndirectory = \"../../../etc\"\n",
		"version = 1\n[policy]\nminimum_score = nan\n",
		"version = 1\n[policy]\nminimum_score = inf\n",
		"version = 1\n[report]\nformats = [\"json\", \"html\", \"json\"]\nhigh = 0\nlow = 100\n",
		// A layer that is valid on its own and contradicts a default once
		// merged: found by this target, kept as a seed rather than as a
		// testdata corpus file.
		"version=1\n[report]\nhigh=0",
		"version = 1\n[report]\ndirectory = \"\"\n",
		"version = 1\nunknown = 1\n[also.unknown]\nx = 1\n",
		"version = 1\n[mutation\n",
		"version = 1\na.b.c.d.e = 1\n",
		"\x00\x01\x02",
		"version = 1\n[report]\nhigh = 80 # trailing comment\n",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, document string) {
		// A configuration file is a few kilobytes at most, and every branch
		// this target can reach is reachable in a few hundred bytes, so a
		// large input buys no coverage. It can still cost a great deal of
		// time: a document of nothing but unknown keys is one located
		// diagnostic per key, and locating each one costs go-toml a scan from
		// the top of the file, which measures at 0.7s for 48 KiB and 19s for
		// 192 KiB. The cap is what keeps a nightly budget spent on exploring
		// the decoder rather than on re-reading one document the fuzzer grew.
		if len(document) > maxFuzzDocument {
			t.Skip("larger than a configuration file is ever meant to be")
		}

		file, err := Parse(FileName, []byte(document))
		if err != nil {
			for _, problem := range flatten(err) {
				if problem == nil {
					t.Fatalf("Parse(%q) returned an error that is not a *config.Error: %v", document, err)
				}
				if !strings.HasPrefix(string(problem.Code), "GOM30") {
					t.Fatalf("Parse(%q) reported code %q, which is outside this package's block",
						document, problem.Code)
				}
				if problem.Message == "" {
					t.Fatalf("Parse(%q) reported %s with no message", document, problem.Code)
				}
				if problem.Position.Line < 0 || problem.Position.Column < 0 {
					t.Fatalf("Parse(%q) reported a negative position %v", document, problem.Position)
				}
			}
			return
		}

		if !file.Present {
			t.Fatalf("Parse(%q) succeeded but reported the document as absent", document)
		}
		// Merging may only surface a cross-field problem. Anything else means
		// a per-value rule is missing from the layer that could have pointed
		// at the offending line.
		resolved := Merge(Defaults(), file, Overlay{})
		for _, problem := range flatten(resolved.Validate()) {
			if problem == nil {
				t.Fatalf("Config.Validate returned an error that is not a *config.Error for %q", document)
			}
			if !crossFieldCodes[problem.Code] {
				t.Fatalf("Parse(%q) accepted a document that then failed the merge with %s, "+
					"which is a per-value rule the file layer should have caught: %v",
					document, problem.Code, problem)
			}
		}
		// Parsing is pure: the same bytes give the same answer.
		again, againErr := Parse(FileName, []byte(document))
		if againErr != nil {
			t.Fatalf("Parse(%q) succeeded then failed: %v", document, againErr)
		}
		if !again.Overlay.Include.Equal(file.Overlay.Include) || !again.Overlay.Jobs.Equal(file.Overlay.Jobs) {
			t.Fatalf("Parse(%q) is not deterministic", document)
		}
	})
}

// crossFieldCodes is every problem that only exists once the layers are
// merged. It is the exact set [Config.Validate] adds on top of the per-value
// rules, and a rule that moves into or out of that set has to be moved here
// too, on purpose.
var crossFieldCodes = map[Code]bool{
	CodeThresholdsInverted: true,
}

// flatten returns every diagnostic an error carries, with a nil entry standing
// for anything that is not a *[Error] so the caller can fail on it. A nil
// error carries nothing.
func flatten(err error) []*Error {
	if err == nil {
		return nil
	}
	var multi *multiError
	if errors.As(err, &multi) {
		out := make([]*Error, 0, len(multi.Unwrap()))
		for _, one := range multi.Unwrap() {
			var problem *Error
			if !errors.As(one, &problem) {
				out = append(out, nil)
				continue
			}
			out = append(out, problem)
		}
		return out
	}
	var problem *Error
	if !errors.As(err, &problem) {
		return []*Error{nil}
	}
	return []*Error{problem}
}
