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
		ID: "565a13327c3aeca8d6b512fc779934904a161a98911a06d6580a48d96a8f27d8",
		Reason: "Equivalent: `>= 2` and `> 2` differ only for two-byte paths; " +
			"a letter plus colon is rejected by the identical post-clean volume " +
			"guard immediately below, and every other two-byte path fails the " +
			"colon-or-letter predicates.",
	},
	{
		ID: "5ab87ae91e8e36e223776f77bc7073cf78024a5c0ee5c2f8c68ca40e54b0ce2c",
		Reason: "Equivalent: the comparison sits inside " +
			"`if x.position != y.position`, where the two registry positions are " +
			"already known to differ, so `<` and `<=` are the same test.",
	},
	{
		ID: "900c4913fd6dc386d37c0cbd9d5753f2330504920ae559e3c76a45b806db0a21",
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
		ID: "8b92325aaa44b1850003c67eba45d5cc8f5bf919ed7c78dfeadabb6c1ccda7a6",
		Reason: "Unreachable: Build only ever sees candidates Add accepted, and " +
			"Add calls Registry.Verify, which refuses exactly the names " +
			"Registry.Position cannot find -- both read one immutable map -- so " +
			"nothing a caller can build reaches this return.",
	},
	{
		ID: "9bc7cd149597bf890755c4774c0f74a728d91e0367f0deed2aaa6e029963843a",
		Reason: "Unkillable: `>` and `>=` pick different catalogues only at " +
			"exactly math.MaxUint32 queued candidates, which is 4,294,967,295 " +
			"Candidate values in one builder.",
	},
	{
		ID: "5787a1dbed1334bd7ae10267e4a44fa05c0b26f6eba202194db08bdf2f17958f",
		Reason: "Unkillable: this return is reached only past math.MaxUint32 " +
			"queued candidates, so killing it means holding more than " +
			"4,294,967,295 Candidate values in memory.",
	},
	{
		ID: "cb8fd2b2364bab9b19cf007c167a5f1ba7d7c2efba55e35d1c80e54a7aea36d7",
		Reason: "Unkillable: Build only sees candidates Add validated and " +
			"Candidate.ID re-runs that same validation, so the only error left " +
			"for this branch to catch is WriteLengthPrefixed's 4 GiB field guard " +
			"-- see the id.go rows below.",
	},
	{
		ID: "17ee3178f2ecd2292dd028daced5dd0ce3550ba32f604880f417ae71927eb122",
		Reason: "Unkillable: the same branch as the row above -- the error this " +
			"forwards can only come from WriteLengthPrefixed's 4 GiB field guard.",
	},
	{
		ID: "b0d2a8afec954577ebc1513a7524fa17426961c52e6efabaa029d99f7923e861",
		Reason: "Unkillable: WriteLengthPrefixed fails only on a field longer " +
			"than math.MaxUint32 bytes, so entering this branch means hashing an " +
			"identity whose path is four gigabytes long.",
	},
	{
		ID: "941885a4446b5d0051d8aff50241e741d6bf8ea0e7cc04b26e2fb904189b4818",
		Reason: "Unkillable: the same branch as the row above -- the error this " +
			"forwards exists only for a field longer than math.MaxUint32 bytes.",
	},
	{
		ID: "9843e3803263d340889f58b002b6e66622b2f25dcf4163a8d4fe48ab62fb038c",
		Reason: "Unkillable: `>` and `>=` disagree only on a string of exactly " +
			"math.MaxUint32 bytes, so telling them apart means allocating four " +
			"gigabytes in a unit test.",
	},
	{
		ID: "778f61b378f9a44f3cc836eccc8bcac2f79295189a0514b28e838b9340dab6b2",
		Reason: "Unkillable: this return is reached only for a string longer " +
			"than math.MaxUint32 bytes, so killing it means allocating more than " +
			"four gigabytes in a unit test.",
	},
	{
		ID: "d42fb63591d5db90e42e36081a7f65630aae5c925098abab5ed536f7dd5f57bb",
		Reason: "Equivalent: this is the tie-break of a sort whose primary key " +
			"is the start line, so it only orders intervals that share one; " +
			"merge joins any such run into [start, max end] whatever their " +
			"order, and nothing downstream reads the order itself.",
	},
	{
		ID: "96e3e2eaaff49e3c662188a99235ddcd673af4d3e5c9189bd9b42461d3a5faa6",
		Reason: "Equivalent: the same tie-break as the row above -- intervals " +
			"sharing a start line all overlap, so merge folds them into " +
			"[start, max end] however the sort arranges them, and the " +
			"primary key still decides every pair whose start lines differ.",
	},
	{
		ID: "a8237c43e03fd9e633b7dd558bad7ba180328124c3de5ff86e5998ee19124a4c",
		Reason: "Unreachable: compileErr is set only when an embedded schema " +
			"cannot be read, parsed, registered or compiled, and " +
			"TestEveryRegisteredSchemaCompiles asserts that none of that " +
			"happens in this build, so this branch is never taken.",
	},
	{
		ID: "5dfdfc895fcd322e018ef0cafb3bfc69511729b08a2ce86b8db6e4d837c07702",
		Reason: "Unreachable: the return the row above guards, reported " +
			"`survived (uncovered)` because no suite reaches a line that " +
			"needs compileErr to be non-nil.",
	},
	{
		ID: "f15a9180a3334747013cda29e7839d0f3074d6eddf6ca6bd59083b2dbefa179f",
		Reason: "Unreachable: compileAll compiles every type in the registry " +
			"and schemaFor looks up that same registry, so the lookup " +
			"cannot miss; TestEveryRegisteredSchemaCompiles asserts it for " +
			"every registered type.",
	},
	{
		ID: "983f4838a49d37538553dd88db49af35189dd436620d65a5585875d241839a21",
		Reason: "Unreachable: the file is read out of an embed.FS fixed at " +
			"build time, and TestEverySchemaIsRegistered plus " +
			"TestEveryRegisteredSchemaCompiles assert that every registered " +
			"name is a file in it, so ReadFile has no failure left to " +
			"return.",
	},
	{
		ID: "240b83cf253c8a0be78af6a577fa1e5aac790e91f17aa0b3fad28c8ea9592f56",
		Reason: "Unreachable: the bytes are an embedded schema this " +
			"repository's own tests parse and compile, so they are JSON in " +
			"every build TestEveryRegisteredSchemaCompiles passes on.",
	},
	{
		ID: "4e5e675648a454570b0e5ebdc67c29add60e07e077bfc20bb0d2130559b2fc99",
		Reason: "Unreachable: AddResource fails on a resource identity it " +
			"cannot parse, and TestSchemaIDsMatchTheirFilenames pins every " +
			"embedded schema's `$id` to `baseURL + <file>`, which is a URL " +
			"by construction.",
	},
	{
		ID: "1563c960a9b93088c64e78f07be618e1354cf4027bc2a51a967fa2cfa5af673b",
		Reason: "Unreachable: every registered schema compiles, which is " +
			"exactly what TestEveryRegisteredSchemaCompiles asserts by " +
			"requiring an invalid document to come back GOM5003 rather than " +
			"GOM5004.",
	},
	// The four rows this package brought with it, every one of them toInt in
	// load.go: two comparisons that are equivalent on any word size, and the
	// two saturating returns behind them, which are dead code on a 64-bit
	// build and are the reason the rows say "on a 64-bit build" rather than
	// "unkillable".
	{
		ID: "5bb4a8040f463792d43781d51bd9b6995e38f6a12f6a8580317a3e79cd71a179",
		Reason: "Equivalent on every platform: `v > int64(maxInt)` and " +
			"`v >= int64(maxInt)` select different branches only at exactly " +
			"int64(maxInt), where the guard returns maxInt and falling " +
			"through returns int(v) -- and int(int64(maxInt)) is maxInt, so " +
			"both spellings narrow every int64 to the same int.",
	},
	{
		ID: "7af7a156c75b48f732fb14d52058b69c48c7fec5ffb4e3a438c4e865035db75a",
		Reason: "Unreachable on a 64-bit build, which is every platform this " +
			"gate runs on: maxInt is int(^uint(0) >> 1), so int64(maxInt) is " +
			"math.MaxInt64 and no int64 is greater than it. Killing it means " +
			"running this package's suite on a 32-bit GOARCH.",
	},
	{
		ID: "985fd8191cfaf201339168e161400c586913136dd6b7ec0d719a1611736b3131",
		Reason: "Equivalent on every platform: the same argument as the `>` " +
			"row above, at the other end -- `<` and `<=` disagree only at " +
			"exactly int64(minInt), where the guard returns minInt and " +
			"falling through returns int(v), which is minInt.",
	},
	{
		ID: "559f0ed09e2f0cbabc410e7beccf4c79b0e365f0daf3e13e2ecfb3e7b0e9d04d",
		Reason: "Unreachable on a 64-bit build: minInt is -maxInt - 1, so " +
			"int64(minInt) is math.MinInt64 and no int64 is less than it -- " +
			"the mirror of the maxInt row above, and reachable on the same " +
			"32-bit GOARCH.",
	},
	// The one row internal/gocmd brought with it: a narrowing that cannot
	// fail, so the forwarding return underneath it cannot be reached.
	{
		ID: "79dfe6684299febc066964f5ab3c6752751da3f855030841483911d3c133387a",
		Reason: "Unreachable: parseVersion has four failure returns and every " +
			"one of them is a *Error carrying CodeVersionUnparsable, so " +
			"errors.As above always matches and this forwarding return is " +
			"reached only by an error kind parseVersion does not produce.",
	},
	// internal/report's thirty-four, in the file's five groups: an expression
	// the rewrite leaves computing the same answer, an error the caller has
	// already ruled out, a value encoding/json cannot refuse, the vendored
	// schema's registration and its diagnostic's fallback, and the path
	// resolution the store's containment check is built on. The arguments are
	// in .go-mutants.toml beside them.
	{
		ID: "b961736e4139ecbe97e50f2ded94f653b8e1fe72b7f624db5a725bcf733d540e",
		Reason: "Equivalent: strings.Builder.Grow is a capacity hint and the " +
			"page is the same bytes for every non-negative size, and the " +
			"vendored viewer is 232 KiB, so len(bundle) + len(document) - " +
			"4096 is positive for any document at all.",
	},
	{
		ID: "ed7481e8cd1f94434f5337692cf82a700e764f04ad21539a96dc7ea1d095ed0a",
		Reason: "Equivalent: the second argument of make is a capacity hint, " +
			"the runtime clamps a negative one to zero, and a map holds the " +
			"same entries whatever it was sized for.",
	},
	{
		ID: "eebd2dc5a8fd6ecb2591f3a05d97ab23dddaa1ee3810fadeca068fb460c8452e",
		Reason: "Equivalent: OutcomeNotRun is the zero value of " +
			"mutation.Outcome, being first in its iota block, so `return " +
			"mutation.OutcomeNotRun` and `return 0` are the same constant " +
			"-- the same argument as the Disjoint and OutcomeNotRun rows " +
			"above.",
	},
	{
		ID: "9525f578dd1e2ba9416b5840f04aa29eac46731dfc166d0a1d7bb4d29f9fa7e7",
		Reason: "Equivalent: the guard reports 0 for a negative duration and " +
			"d.Milliseconds() otherwise, and a zero duration is 0 " +
			"milliseconds through either branch, so `<` and `<=` render " +
			"every duration the same.",
	},
	{
		ID: "73674bbd5d0ff2893df5f890a642badf7cb4414ab97ea227641f07e2c7d7520b",
		Reason: "Equivalent: `<=` and `<` disagree only for an id of exactly " +
			"DisplayIDLength characters, where returning `id` and returning " +
			"`id[:DisplayIDLength]` return the same string.",
	},
	{
		ID: "6e8a1b109029e1c7a06bc6da1d2bf5ae4de4d519823e2e8d419dbec4b90a42f7",
		Reason: "Equivalent: the walk keeps the lexicographically first " +
			"instance location, and `at <= best` differs from `at < best` " +
			"only in assigning best the value it already holds.",
	},
	{
		ID: "9e6ce60f8296cab93089d0865d5fe1d022f1f91e032bb78dc54e536914bfc908",
		Reason: "Equivalent: the guard clamps a negative offset to 0, and `<=` " +
			"also assigns 0 to an offset that is already 0.",
	},
	{
		ID: "e30afbd6a3a390a9c9795e794ca19ea1df445dd9668ae2464978dcb56157c280",
		Reason: "Equivalent: the guard clamps an offset past the end of the " +
			"file to the end, and `>=` also assigns len(src) to an offset " +
			"that is already len(src).",
	},
	{
		ID: "19e108a41d09d62ce7821d0580c02b5f226540c1679af432d2f7e1b3d1726050",
		Reason: "Equivalent: sort.SearchInts is asked for the first line start " +
			"greater than the offset, lineStarts[0] is 0, and an offset is " +
			"never negative -- so the search never answers 0 and neither " +
			"`<= 0` nor `< 0` is ever true.",
	},
	{
		ID: "8c9d90d7b4df5f3b07adaf9331b6bebb233480743888afceaccd3e9327cfa21a",
		Reason: "Equivalent: the guard clamps a line index below the first line " +
			"to 0, and `<=` also assigns 0 to an index that is already 0.",
	},
	{
		ID: "1449b2fcd7b848d1841d473598acb3db148bcf85d40386adffca970929cfc582",
		Reason: "Equivalent: the guard clamps an offset past the end of the " +
			"file to the end, and `>=` also assigns len(src) to an offset " +
			"that is already len(src) -- the same argument as the position " +
			"row above.",
	},
	{
		ID: "bb5d7070cdfcd8f9a300d14e4ac613ef4b5c4696ce6941d6933e76e381862c20",
		Reason: "Equivalent: a rejected mutant's disposition carries no " +
			"outcome, and StateOf answers `unfulfilled` through the " +
			"Rejected case and through the default alike, because the zero " +
			"Outcome is not OutcomeSurvived. Nothing else reads the field.",
	},
	{
		ID: "926d93171820882528895f954fa11206a0277bf20ede3b5beed639d7d333437b",
		Reason: "Equivalent: the `existed` flag is read only by the rollback " +
			"that puts a document back, and a caller handed this error " +
			"returns before there is a rollback to run.",
	},
	{
		ID: "e8db06e466a9891f3385d91579a4e8b3e3a0d0371fd517ac84d82c44c93edee7",
		Reason: "Equivalent: both callers impose a total order of their own on " +
			"what this returns -- readWorkspace sorts the runs by " +
			"NewestFirst and the damaged rows by path, and RemoveRuns only " +
			"counts them and their bytes -- so the order the files come " +
			"back in is not observable.",
	},
	{
		ID: "94ebea36dab33b2c9cd25cad0af4cdefce2091feb7f815b069bc5af30558f938",
		Reason: "Unreachable: partition has already translated every result's " +
			"outcome through OutcomeOf, which refuses anything outside the " +
			"six, and mutation.Tally records all six -- so the count this " +
			"forwards cannot fail.",
	},
	{
		ID: "fcbf85329076ce6bddc0f6c222cff55456e4b3449b18881a88bad9ce01b8e52d",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "d5aca19227c0b4c0221af1a67665df1f4004d869618127b1aa9ba326ada9d996",
		Reason: "Unreachable: the same argument one level in -- tallyOf reads " +
			"the outcomes partition has already accepted, so " +
			"mutation.TallyOf cannot refuse one.",
	},
	{
		ID: "6aa0c9767b21b5d04e22f95cdd4e68d034edd744bbe5508cf5436cfb2ee312e4",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "04c726e648dd175f048be9ffe1a6c450accb49564bd2e66d2f08cbd508dafb56",
		Reason: "Unreachable: Outcome.Mutation answers with one of the six core " +
			"outcomes or with an error the line above returns, and " +
			"mutation.Tally.Record has a case for all six.",
	},
	{
		ID: "4ed85de71aa3f58bfc544352dbc6670620d2a5eb35d71eb7dd90a7a218ab1f0a",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "7d277185c89cfbde1548bd9ef18ad72e2ab7ef007bc4c3353c5029e6fafbc67e",
		Reason: "Unreachable: the counts disagree only when a row was not " +
			"consumed by the catalogue walk, and a row is consumed exactly " +
			"when its id is catalogued -- so the loop above always finds " +
			"the row this line exists to report the absence of.",
	},
	{
		ID: "7dd25029ee5d776af3373a66167256898409217c7d655bf0fbbad65a6a8aab1b",
		Reason: "Unkillable: encoding/json fails only on a value it cannot " +
			"represent -- a cycle, a channel or function field, a " +
			"non-finite float -- and a Projection is strings, ints and a " +
			"map of them, so no value of the type can make Encode fail.",
	},
	{
		ID: "779e89c5aa67aa6ee881c10fc35d27ec70f693c193005ece49b215d40c95321a",
		Reason: "Unkillable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "fb9d1e077634dc9befaec38521c6026643a2bb81dfb66404cbe2eb3ee232ee1e",
		Reason: "Unkillable: the same encoding, reached through WriteArtifacts " +
			"-- the document it publishes is the Projection the row above " +
			"is about.",
	},
	{
		ID: "4dad6256bc511e6795e5e344201ef55546b6d1bc41df6c38ff5f02ad9406c2db",
		Reason: "Unkillable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "1dedbb5f405697914b0966b973fb1f7c68b59541fea2158480b9aef3e4ddadcf",
		Reason: "Unreachable: jsonschema/v6's AddResource stores the document " +
			"and defers every check to Compile, so it fails only for a url " +
			"it cannot parse or one already registered -- and this compiler " +
			"is used once, for one constant url.",
	},
	{
		ID: "588ad59e967ace2e5e9ba2e66cb2359e2cf36eaecc67065c9505397f64584739",
		Reason: "Unreachable: pointerOf answers `the document root` for an " +
			"empty instance location and a pointer beginning with a slash " +
			"otherwise, so every leaf the walk reaches sets best to " +
			"something -- and the walk always reaches at least the error it " +
			"was given.",
	},
	{
		ID: "3190c37f7f6fc15fd4d516a8beb3f098a6a4aaaff0930c9dc3026d81b859d7f8",
		Reason: "Unkillable: filepath.Abs returns an error only when os.Getwd " +
			"does, which needs this process's own working directory to have " +
			"been deleted -- a state a test would be arranging for every " +
			"other test in the same binary.",
	},
	{
		ID: "ace7fb73f25d06b183db8d38fe3fdafb0cea0462c99c02b82b6ca9863d399164",
		Reason: "Unkillable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "700cbc056f3fb82558d41158da5f717ce1041698df05826787554562ba3c2a3b",
		Reason: "Unreachable: the answer for a volume root that does not " +
			"resolve, which needs a path none of whose ancestors exist -- " +
			"and the root itself always does.",
	},
	{
		ID: "2380340d24fe192076857b8148ca285ed7c8a60f2ef97ca79ae09c272d76d540",
		Reason: "Unreachable: filepath.EvalSymlinks walks a path from the left " +
			"and reports the first name it cannot resolve, so a path it " +
			"calls `not there` and a parent that fails for some other " +
			"reason cannot both happen -- the parent shares the prefix and " +
			"answers with the same failure.",
	},
	{
		ID: "e136ee1f2165220ec767905f8c86ed8a73d44f043657f2ac293afc970793a275",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would forward it.",
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
			// Ten whole packages. Scoped test binaries bought the first two
			// — the gate used to be two files, because every mutant ran every
			// test binary in the module — internal/mutation's own tests
			// bought the third, by killing the survivors that kept it out,
			// the next three were measured before they were included and
			// had no survivor to kill, and the three after those were bought
			// the same way the third was. The ninth is this package: the file
			// this test reads is inside the scope that reads it. The tenth is
			// the toolchain wrapper, and it is the first one in this list
			// that starts processes rather than deciding over values.
			// had no survivor to kill, and the four after that were bought
			// the same way the third was. The ninth is this package: the
			// file this test reads is inside the scope that reads it. The
			// tenth is internal/gocmd, the toolchain wrapper, and the
			// eleventh is internal/report, which is what a run writes down.
			//
			// The order is the file's order, and it is asserted rather than
			// sorted for the same reason the expectation ids are: a list
			// somebody reorders is a list somebody edited.
			Include: []string{
				"internal/mutation/*.go",
				"internal/glob/*.go",
				"internal/interval/*.go",
				"internal/testflag/*.go",
				"internal/operatorselect/*.go",
				"internal/drift/*.go",
				"internal/coverage/*.go",
				"internal/schemas/*.go",
				"internal/config/*.go",
				"internal/gocmd/*.go",
				"internal/report/*.go",
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
			// ten patterns are the only packages a test binary is built for,
			// and they have to be the ten Include names above. A package that
			// is mutated but not named here gets no binary, so every mutant in
			// it is reported `survived (uncovered)` — which is why both lists
			// are pinned here rather than one of them.
			Command: []string{
				"go", "test",
				"./internal/mutation/...", "./internal/glob/...", "./internal/interval/...",
				"./internal/testflag/...", "./internal/operatorselect/...", "./internal/drift/...",
				"./internal/coverage/...", "./internal/schemas/...", "./internal/config/...",
				"./internal/gocmd/...",
				"./internal/report/...",
			},
			// `timeout` is deliberately omitted from the file now that the
			// binaries are scoped, so it derives from the baseline rather than
			// clearing internal/discover's toolchain-driving suite, which is no
			// longer built. Zero is what "derive it" looks like here.
			Timeout: 0,
			// `memory` is omitted for the same reason and pinned here for a
			// sharper one: this scope is why the setting exists, so a number
			// written into the file would be somebody's guess standing in for
			// a bound derived from this repository's own baseline.
			Memory:       0,
			BaselineRuns: 3,
			Narrowing:    NarrowingTest,
			Probing:      ProbingOff,
		},
		// `jobs` is pinned in the file rather than defaulted, so that a local
		// run and a GitHub-hosted CI run are the same run; see the comment there
		// for why it is no longer pinned for correctness.
		Execution: Execution{Jobs: 4},
		Cache:     Cache{Mode: CacheAuto, Directory: ""},
		// The floor moved with the eleventh package, for the first time since
		// it went to 99: one percent of 2432 scored mutants was twenty-four
		// survivors of slack, which is more than the twenty-one that was
		// judged too much at 544. It has not moved since, and that is the
		// same arithmetic rather than inertia: at 2673 scored mutants half a
		// percent buys thirteen survivors (2660/2673 = 99.51% clears,
		// 2659/2673 = 99.48% does not), where it bought twelve when it was
		// set -- still far short of the twenty-one that moves this number.
		// The arithmetic is written out in the file.
		Policy: mutation.Policy{Strict: false, MinimumScore: 99.5, RequireMutants: true},
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
