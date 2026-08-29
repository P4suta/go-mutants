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

// repositoryExpectations is the ledger the repository's own configuration
// declares, transcribed row for row.
//
// It is spelled out here, id and argument alike, for the reason the ledger
// exists at all: an expectation is a claim that a mutant cannot be killed, and
// the way such a claim rots is that somebody quietly deletes or rewords a row
// to turn a red gate green. Pinning the ids catches a deletion; pinning the
// reasons catches a row whose argument was replaced by a shrug. The order is
// the file's order, which internal/report relies on when it tells an author
// which row has gone stale.
var repositoryExpectations = []Expectation{
	{
		ID: "7c3b141c043e632d833e0d9b948690bf3efb9047c502de6cb3cd372dfc7685b9",
		Reason: "Equivalent: Disjoint is the zero value of Relation, " +
			"being first in its iota block, so `return Disjoint` and " +
			"`return 0` are the same constant.",
	},
	{
		ID: "326eb774d53498b186d7a5990a0ee7f0c7dd2cf4934f6ba8adb79a0217b6025b",
		Reason: "Equivalent: Len returns 0 through the guard for a reversed span " +
			"and EndByte - StartByte otherwise, and that difference is 0 exactly " +
			"when the bounds are equal, so `<` and `<=` report the same length " +
			"for every span.",
	},
	{
		ID: "0d13d53c6b4f43b0e2fb26f7da1096974e1caaf4b17cf09e527ddb014cf85abf",
		Reason: "Equivalent: the comparison sits inside " +
			"`case s.StartByte != other.StartByte`, where the two start bytes are " +
			"already known to differ, so `<` and `<=` are the same test.",
	},
	{
		ID: "b3bc0192a26baa4e0c2080c6abe78d188189d55ed7544392937d5023b498a485",
		Reason: "Equivalent: the comparison sits inside " +
			"`case s.EndByte != other.EndByte`, where the two end bytes are " +
			"already known to differ, so `<` and `<=` are the same test.",
	},
	{
		ID: "9fd083af0792f6b5155cb1cff8883f7a3dce112be8c53e3c92c2ccc0e813044b",
		Reason: "Equivalent: `>= 2` and `> 2` differ only for two-byte paths; " +
			"a letter plus colon is rejected by the identical post-clean volume " +
			"guard immediately below, and every other two-byte path fails the " +
			"colon-or-letter predicates.",
	},
	{
		ID: "ab63a5a3e327b7db2bbe97d3267ca840cebf9cf54a899f10cdc52a111a398548",
		Reason: "Equivalent: the comparison sits inside " +
			"`if x.position != y.position`, where the two registry positions are " +
			"already known to differ, so `<` and `<=` are the same test.",
	},
	{
		ID: "90dcb6c9a5eef3328c6287e649f96f5d71187376e2fdb906a262947c815a0c75",
		Reason: "Equivalent: OutcomeNotRun is the zero value of Outcome, being " +
			"first in its iota block, so `return OutcomeNotRun` and `return 0` " +
			"are the same constant -- the same argument as the Disjoint row above.",
	},
	{
		ID: "cf02d1c0b8855d56c21d1216e47fec02bc0674638c7f3e8ab02ce647af24b391",
		Reason: "Equivalent for every coherent tally: `p.MinimumScore > 0` and " +
			"`>= 0` select different runs only when the floor is exactly zero and " +
			"the score is negative, and a negative percentage needs a negative " +
			"Detected count, which Score.Validate reports as incoherent.",
	},
	{
		ID: "71a2e9ed6670de5c01f1c29c061ab45b9a5b6000d754250758601e204f997e68",
		Reason: "Unreachable: Build only ever sees candidates Add accepted, and " +
			"Add calls Registry.Verify, which refuses exactly the names " +
			"Registry.Position cannot find -- both read one immutable map -- so " +
			"nothing a caller can build reaches this return.",
	},
	{
		ID: "e997446d6c157c03f5403f1ea5ae0a63a7b20fab96a4ccafa604197d2c44e929",
		Reason: "Unkillable: `>` and `>=` pick different catalogues only at " +
			"exactly math.MaxUint32 queued candidates, which is 4,294,967,295 " +
			"Candidate values in one builder.",
	},
	{
		ID: "a94a2c50fc8b33c7f5ea10f9f5cea1e05eb85138b54119cf80383a8eac4b5c01",
		Reason: "Unkillable: this return is reached only past math.MaxUint32 " +
			"queued candidates, so killing it means holding more than " +
			"4,294,967,295 Candidate values in memory.",
	},
	{
		ID: "0879ed736b35300ea72bf867721591ee218f02a21a7d1fdc5d1a8a89df20a718",
		Reason: "Unkillable: Build only sees candidates Add validated and " +
			"Candidate.ID re-runs that same validation, so the only error left " +
			"for this branch to catch is WriteLengthPrefixed's 4 GiB field guard " +
			"-- see the id.go rows below.",
	},
	{
		ID: "85dc0334ed347973ea6af195207e0cd262ca6a2bfc9c7a710bda312262b5aad7",
		Reason: "Unkillable: the same branch as the row above -- the error this " +
			"forwards can only come from WriteLengthPrefixed's 4 GiB field guard.",
	},
	{
		ID: "ce40ad71426f18bdb1c7af2f1ab665321d3b2e150d071e52c39b9c6e38ada90c",
		Reason: "Unkillable: WriteLengthPrefixed fails only on a field longer " +
			"than math.MaxUint32 bytes, so entering this branch means hashing an " +
			"identity whose path is four gigabytes long.",
	},
	{
		ID: "feba9b0fd4945263451deb51c5fbe281d6fd97a4c98c181f6aec9eebcab4c2c1",
		Reason: "Unkillable: the same branch as the row above -- the error this " +
			"forwards exists only for a field longer than math.MaxUint32 bytes.",
	},
	{
		ID: "e8eb2f6092486a0554da44e0796371ab157e4bf07c574ef16d9ff8ece74f4474",
		Reason: "Unkillable: `>` and `>=` disagree only on a string of exactly " +
			"math.MaxUint32 bytes, so telling them apart means allocating four " +
			"gigabytes in a unit test.",
	},
	{
		ID: "ad9321c58f07017b27f8a9dd8f1e4acad7a38d4a83a9e100589634d63512117c",
		Reason: "Unkillable: this return is reached only for a string longer " +
			"than math.MaxUint32 bytes, so killing it means allocating more than " +
			"four gigabytes in a unit test.",
	},
}

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
			// Three whole packages. Scoped test binaries bought the first two
			// — the gate used to be two files, because every mutant ran every
			// test binary in the module — and internal/mutation's own tests
			// bought the third, by killing the survivors that kept it out.
			Include: []string{
				"internal/mutation/*.go",
				"internal/glob/*.go",
				"internal/interval/*.go",
			},
			Exclude: []string{"**/*_test.go", "**/testdata/**", "fixtures/**", "vendor-assets/**"},
			// `operators` is deliberately omitted from the file, so the
			// profile decides and this stays empty.
			Operators: nil,
			Profile:   mutation.TierBalanced,
			// The declared mutants, pinned here as well as in the file so that
			// deleting a ledger entry to make a red gate green shows up as a
			// failing test. See repositoryExpectations above.
			Expect: repositoryExpectations,
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
		Policy:    mutation.Policy{Strict: false, MinimumScore: 99, RequireMutants: true},
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
