// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// repositoryConfig is go-mutants' own .go-mutants.toml, which the repository
// keeps as the worked example of the whole v1 surface.
const repositoryConfig = "../../" + FileName

// The example everyone reads has to be an example that works. A documented
// surface that the decoder rejects is worse than no example, and this is the
// one test that would catch the file and the decoder drifting apart — a key
// renamed here, a section added there, a value that stops being in range.
func TestRepositoryConfigurationRoundTrips(t *testing.T) {
	path := filepath.FromSlash(repositoryConfig)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the repository's own configuration is missing: %v", err)
	}

	file, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(%s): %v", path, err)
	}
	if !file.Present {
		t.Fatalf("Present = false for a file that is in the repository")
	}

	resolved := Merge(Defaults(), file, Overlay{})
	if err := resolved.Validate(); err != nil {
		t.Fatalf("the repository's own configuration does not validate: %v", err)
	}

	want := Config{
		Version: 1,
		Mutation: Mutation{
			// Two whole packages and one file. The whole-package entries are
			// what scoped test binaries bought: the gate used to be two files
			// because every mutant ran every test binary in the module.
			Include: []string{
				"internal/mutation/shard.go",
				"internal/glob/*.go",
				"internal/interval/*.go",
			},
			Exclude: []string{"**/*_test.go", "**/testdata/**", "fixtures/**", "vendor-assets/**"},
			// `operators` is deliberately omitted from the file, so the
			// profile decides and this stays empty.
			Operators: nil,
			Profile:   mutation.TierBalanced,
			// The one declared equivalent mutant: `return Disjoint` rewritten
			// to `return 0`, where Disjoint is the zero value of Relation.
			// Pinned here as well as in the file so that deleting the ledger
			// entry to make a red gate green shows up as a failing test.
			Expect: []Expectation{{
				ID: "7c3b141c043e632d833e0d9b948690bf3efb9047c502de6cb3cd372dfc7685b9",
				Reason: "Equivalent: Disjoint is the zero value of Relation, " +
					"being first in its iota block, so `return Disjoint` and " +
					"`return 0` are the same constant.",
			}},
		},
		Test: Test{
			// The command is the run's scope as well as its measurement: these
			// three patterns are the only packages a test binary is built for.
			Command: []string{
				"go", "test",
				"./internal/mutation/...", "./internal/glob/...", "./internal/interval/...",
			},
			// `timeout` is deliberately omitted from the file now that the
			// binaries are scoped, so it derives from the baseline rather than
			// clearing internal/discover's toolchain-driving suite, which is no
			// longer built. Zero is what "derive it" looks like here.
			Timeout:      0,
			BaselineRuns: 3,
		},
		// `jobs` is pinned in the file rather than defaulted, so that a local
		// run and a GitHub-hosted CI run are the same run; see the comment there
		// for why it is no longer pinned for correctness.
		Execution: Execution{Jobs: 4},
		Cache:     Cache{Mode: CacheAuto, Directory: ""},
		Policy:    mutation.Policy{Strict: false, MinimumScore: 96, RequireMutants: true},
		Report: Report{
			Directory: "reports/mutation",
			Formats:   []ReportFormat{FormatJSON, FormatHTML},
			High:      80,
			Low:       60,
		},
	}
	if diff := cmp.Diff(want, resolved); diff != "" {
		t.Errorf("the repository's configuration resolves differently than documented (-want +got):\n%s", diff)
	}

	// The file's own patterns have to compile, which is what makes it a
	// working example rather than a plausible-looking one.
	if err := (Overlay{
		Include: Explicit(resolved.Mutation.Include),
		Exclude: Explicit(resolved.Mutation.Exclude),
	}).Validate(); err != nil {
		t.Errorf("the repository's patterns do not compile: %v", err)
	}
}

// Every commented-out key in the worked example has to be a key that would be
// accepted if it were uncommented. A commented example that no longer decodes
// is a trap, and it is exactly the kind of thing that rots unnoticed.
func TestRepositoryConfigurationCommentedKeysDecode(t *testing.T) {
	uncommented := "version = 1\n\n" +
		"[mutation]\n" +
		"profile = \"balanced\"\n" +
		"operators = [\"comparison\", \"error-swallowing\"]\n\n" +
		"[[mutation.expect]]\n" +
		"id = \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n" +
		"reason = \"Equivalent: the branch is unreachable for all valid inputs.\"\n\n" +
		"[cache]\n" +
		"mode = \"auto\"\n" +
		"directory = \"team-cache\"\n"

	if _, err := Parse(FileName, []byte(uncommented)); err != nil {
		t.Fatalf("the commented-out examples do not decode: %v", err)
	}
}
