// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"errors"
	"strings"
	"testing"
)

const maxFuzzDocument = 64 << 10

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
		again, againErr := Parse(FileName, []byte(document))
		if againErr != nil {
			t.Fatalf("Parse(%q) succeeded then failed: %v", document, againErr)
		}
		if !again.Overlay.Include.Equal(file.Overlay.Include) || !again.Overlay.Jobs.Equal(file.Overlay.Jobs) {
			t.Fatalf("Parse(%q) is not deterministic", document)
		}
	})
}

var crossFieldCodes = map[Code]bool{
	CodeThresholdsInverted: true,
}

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
