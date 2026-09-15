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
		ID: "a7353f2073cb5e21aa44235cf31014b9efb6f153afbfba57b110741e38c34ca7",
		Reason: "Equivalent: the loop ends at the first unterminated line, and " +
			"an empty body is that case -- bytes.Cut finds no separator and " +
			"reports none -- so the extra pass `>=` admits breaks before it " +
			"reads anything. The condition is kept over `for {}` because " +
			"dropping it makes two mutants of this package never return.",
	},
	{
		ID: "db02130598a10e890c61ed90883ab299048c2c39d35a78eff6fc34f7d17a8961",
		Reason: "Equivalent: a pair where exactly one path carries a drive letter " +
			"differs at the colon, which has no case, so EqualFold and == " +
			"answer alike on it -- the third disjunct therefore never decides " +
			"anything the first two did not.",
	},
	{
		ID: "35946b8e442ed10a434d3078182aea181ac5aa24979997eab3a3411fa4436c25",
		Reason: "Equivalent: blame over an empty pending set answers an empty list, " +
			"so `> 0` and `>= 0` set the same blame on every build; the guard " +
			"is there to skip parsing the compiler's whole output on a trial " +
			"build, which is a cost and not an answer.",
	},
	{
		ID: "d1903657159d42413034be2bef3bb000812b503fc6a0800a9b7faf61fcb9ee04",
		Reason: "Unreachable: the loop is bounded at one pass per catalogued file " +
			"and every pass decides at least one of them, because blame never " +
			"answers an empty list while anything is pending -- so the search " +
			"always returns from inside it.",
	},
	{
		ID: "8b20fcac8336c3eca564c9b065c8867b4addb71d0953f0346f2933954411384c",
		Reason: "Unreachable: the other half of the same backstop, which exists so " +
			"that a search wrong about its own bound fails closed rather than " +
			"falling out of the loop.",
	},
	{
		ID: "7793f84f81acc0da784e91c4a1320e9c156ca3d0475b3b002a5ef98874c8201e",
		Reason: "Unkillable: WriteLengthPrefixed fails only on a field longer than " +
			"math.MaxUint32 bytes, so entering this branch means hashing a " +
			"cache context with a four-gigabyte field in it.",
	},
	{
		ID: "3d14887a7d333b18f16fb83768515c14163e526a294eb9dc957c1f040d6d15b2",
		Reason: "Unkillable: the same branch as the row above -- the error this " +
			"forwards exists only for a field longer than math.MaxUint32 bytes.",
	},
	{
		ID: "bcd244f179ba95adfcd0020401cc298e8d31d2d720f4fa45d243e18c55858d36",
		Reason: "Equivalent: the listing this guards is of the same directory the " +
			"emptiness check lists a few lines below, with the same call and " +
			"the same message, and a context whose files could not be listed " +
			"always reaches it -- so removing the guard reports the identical " +
			"failure one step later.",
	},
	{
		ID: "16dc5ab574628847287a82197bd016dc237abcdb4835d77a3fe121818ed1e8f1",
		Reason: "Equivalent: an entry with no recorded bound falls through to the " +
			"default case at the same answer, because `limit <= 0 || limit >= 0` " +
			"is true for every limit -- so `<=` and `<` choose different " +
			"branches and the same value.",
	},
	{
		ID: "be34a1e2f35ba435b9f31477de1f0f71b484ded67020177e14b028779da76f5f",
		Reason: "Unreachable: an Entry is strings, integers and booleans, and " +
			"encoding/json has no failure for any of them.",
	},
	{
		ID: "5a955e26a94e6611e9675d169d6276d5ccb5c72acca52494bfe829a033e7a83d",
		Reason: "Unreachable: the other half of the same branch -- the diagnostic " +
			"returned for an encoding failure an Entry cannot produce.",
	},
	{
		ID: "0c61708323241091dc71b704454a4644278eaab1c340cb77ffb76f35539109b7",
		Reason: "Unreachable where this gate runs: filepath.Rel refuses a pair only " +
			"when the two carry different volume names, which no POSIX path " +
			"does, so both paths here are always relatable.",
	},
	{
		ID: "a87698a37045703dda49276056c91ac240a017f41e1c57783c1694ab13d38814",
		Reason: "Unreachable where this gate runs: the same branch as the row above, " +
			"and the answer it gives -- not inside -- for a pair of paths POSIX " +
			"cannot produce.",
	},
	{
		ID: "06cabbdf7a94e889df9b5cc94b72e9d07805cf567329afb27da1e0a674dfeae2",
		Reason: "Unreachable: the pattern compiled here is path.Clean's output " +
			"over a path NormalizePath already refused as empty, absolute or " +
			"escaping, which is exactly the set glob.Compile refuses, so the " +
			"error is never non-nil.",
	},
	{
		ID: "ae90054cf1b9d69d588a9e2e112cbe697fd0ddc72d994fbce9dea744e302f896",
		Reason: "Unreachable: the other half of the same branch -- the " +
			"diagnostic returned for a glob.Compile failure that no configured " +
			"report directory can produce.",
	},
	{
		ID: "00110ba284fd3da4b8408c57a7bfa66a2187047ede44d2adb78d948757c8ac4d",
		Reason: "Unreachable: compileErr is set only when an embedded schema " +
			"cannot be read, parsed, registered or compiled, and " +
			"TestEveryRegisteredSchemaCompiles asserts that none of that " +
			"happens in this build, so this branch is never taken.",
	},
	{
		ID: "22db5c4a94a19f10e8d159ff0b1b1fa7bc069f9188d1fea90cd5c77fce6ec2bc",
		Reason: "Unreachable: the return the row above guards, reported " +
			"`survived (uncovered)` because no suite reaches a line that " +
			"needs compileErr to be non-nil.",
	},
	{
		ID: "20f61827e337d7d8df88ce7c1b1dd7194ae27b45046037dbb40f6c2397ca5587",
		Reason: "Unreachable: compileAll compiles every type in the registry " +
			"and schemaFor looks up that same registry, so the lookup " +
			"cannot miss; TestEveryRegisteredSchemaCompiles asserts it for " +
			"every registered type.",
	},
	{
		ID: "fa9198cae68dd0d7dd80f6472facc13d667dbce0a9058daa0c4966df57a03d74",
		Reason: "Unreachable: the file is read out of an embed.FS fixed at " +
			"build time, and TestEverySchemaIsRegistered plus " +
			"TestEveryRegisteredSchemaCompiles assert that every registered " +
			"name is a file in it, so ReadFile has no failure left to " +
			"return.",
	},
	{
		ID: "af2611a10aede2a5c1a3d950fb443d87a838d4f43b38b9ae7add828ab038e0de",
		Reason: "Unreachable: the bytes are an embedded schema this " +
			"repository's own tests parse and compile, so they are JSON in " +
			"every build TestEveryRegisteredSchemaCompiles passes on.",
	},
	{
		ID: "2293c23d9c88aecfa2ff1653021a8bc7ac0829a88aa3d692cf4834be61a25c73",
		Reason: "Unreachable: AddResource fails on a resource identity it " +
			"cannot parse, and TestSchemaIDsMatchTheirFilenames pins every " +
			"embedded schema's `$id` to `baseURL + <file>`, which is a URL " +
			"by construction.",
	},
	{
		ID: "a20ef5c99cbd0a4949615e0ef6de29ae6a29e43d177520ca24701dc206aebb06",
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
		ID: "3b525cda27c79847b5043b1ed0c5924088f415ecd7d8bdeb256eb642b30c6fa9",
		Reason: "Equivalent: the second argument of make is a capacity hint, " +
			"the runtime clamps a negative one to zero, and a map holds the " +
			"same entries whatever it was sized for.",
	},
	{
		ID: "e46a140ecb4932fb9454fe6cf44688246bbd11546052e38baf97f199db1330e4",
		Reason: "Equivalent: the guard reports 0 for a negative duration and " +
			"d.Milliseconds() otherwise, and a zero duration is 0 " +
			"milliseconds through either branch, so `<` and `<=` render " +
			"every duration the same.",
	},
	{
		ID: "b95cf5f7760ea8444aa2e1512c60daf7060f533a673783cfbb5f736d767e49ed",
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
		ID: "c6b70e0c3efef28723cff7df0ff96d2d60b06d0f458f8838eb2a7f1a7e6cce0a",
		Reason: "Equivalent: a rejected mutant's disposition carries no " +
			"outcome, and StateOf answers `unfulfilled` through the " +
			"Rejected case and through the default alike, because the zero " +
			"Outcome is not OutcomeSurvived. Nothing else reads the field.",
	},
	{
		ID: "6ff6656d50385f425cd37c8ae327942d870a06a596016e8e8f1c159c3c4cee14",
		Reason: "Equivalent: the `existed` flag is read only by the rollback " +
			"that puts a document back, and a caller handed this error " +
			"returns before there is a rollback to run.",
	},
	{
		ID: "3df5f41b6b99375178004b38f82f50defccf85ee3f1ac56865cb1e201d4bede8",
		Reason: "Equivalent: both callers impose a total order of their own on " +
			"what this returns -- readWorkspace sorts the runs by " +
			"NewestFirst and the damaged rows by path, and RemoveRuns only " +
			"counts them and their bytes -- so the order the files come " +
			"back in is not observable.",
	},
	{
		ID: "8ed9bc39d59d913d6c2b7ee942947280bbd2113bfad19981a9672d469b340e3b",
		Reason: "Unreachable: partition has already translated every result's " +
			"outcome through OutcomeOf, which refuses anything outside the " +
			"six, and mutation.Tally records all six -- so the count this " +
			"forwards cannot fail.",
	},
	{
		ID: "62ec204b2822dd114fab13b2c91171f5217226e7d85c9419777927f35dedefa7",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "d87840061d0c60bc574e6225141be9691d131a2005be37c1206535d8ca8c43ba",
		Reason: "Unreachable: the same argument one level in -- tallyOf reads " +
			"the outcomes partition has already accepted, so " +
			"mutation.TallyOf cannot refuse one.",
	},
	{
		ID: "a0e7af3d26a014fd2ca311f3b27858203d6984bdc23f5fba682e8dcd95505182",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "9ba94e487e2da6ec210920658f492727cf96ac59096361a35bb85e9bffe1efc0",
		Reason: "Unreachable: Outcome.Mutation answers with one of the six core " +
			"outcomes or with an error the line above returns, and " +
			"mutation.Tally.Record has a case for all six.",
	},
	{
		ID: "8faf6ec6bea019578b545f1a72ee6577fa18c924fe61e734b2f2bc16e6851cf3",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "776653c1ef0eef817020ea6bec2d59d21e11f63a4c9127ea7ac5b176faacd64d",
		Reason: "Unreachable: the counts disagree only when a row was not " +
			"consumed by the catalogue walk, and a row is consumed exactly " +
			"when its id is catalogued -- so the loop above always finds " +
			"the row this line exists to report the absence of.",
	},
	{
		ID: "843a24ebe25ecb3c9b062e5df5776e2f82236eea1c85b70c904342083ece1abc",
		Reason: "Unreachable: every module's report has been built or merged " +
			"by the time this runs, and a report that was is one whose every " +
			"outcome mutation.TallyOf accepted -- so the aggregate over those " +
			"same outcomes cannot fail. Both constructors return through this " +
			"one line, which is why it is declared once rather than at each of them.",
	},
	{
		ID: "7647d940eade086845f48dc51b2515eada4bd201d25632874d9eadf7b852530e",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "95b0ddca198266155d3b025dfb8fd94e4b6db7b96486b194b2bad78bbce211b8",
		Reason: "Unkillable: encoding/json fails only on a value it cannot " +
			"represent -- a cycle, a channel or function field, a " +
			"non-finite float -- and a Projection is strings, ints and a " +
			"map of them, so no value of the type can make Encode fail.",
	},
	{
		ID: "58b9a70ee05269bf17d5a0fcc2d3c9ee39d5a821d385abc02665e9050494d5af",
		Reason: "Unkillable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "cdaeae3a0171381e52574b95f907baec6d0bc2fb39d12cc32c7745c973f49c05",
		Reason: "Unkillable: the same encoding, reached through WriteArtifacts " +
			"-- the document it publishes is the Projection the row above " +
			"is about.",
	},
	{
		ID: "0653f507f92b55eeca7b45c60b1c54f4e6823d6b2fb84c383f9d31b95be55303",
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
		ID: "0b273dc1d64bac1ceef63affc3d880d1f737c81c962e1e73e364fe7b9a6d4e5f",
		Reason: "Unkillable: filepath.Abs returns an error only when os.Getwd " +
			"does, which needs this process's own working directory to have " +
			"been deleted -- a state a test would be arranging for every " +
			"other test in the same binary.",
	},
	{
		ID: "170340b291e6416303a6270f2a4395876b8639ef12ab690c95169525511abe07",
		Reason: "Unkillable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "9fbb23c0f5a691d282c91d6f4e76e10cdeef49d64cd129e6e11c8b375c46531c",
		Reason: "Unreachable: the answer for a volume root that does not " +
			"resolve, which needs a path none of whose ancestors exist -- " +
			"and the root itself always does.",
	},
	{
		ID: "d0c7b47105ddeaca591bf4e46a18272d93b0b4e053f4896013277cc950d2abd6",
		Reason: "Unreachable: filepath.EvalSymlinks walks a path from the left " +
			"and reports the first name it cannot resolve, so a path it " +
			"calls `not there` and a parent that fails for some other " +
			"reason cannot both happen -- the parent shares the prefix and " +
			"answers with the same failure.",
	},
	{
		ID: "6234ee76e3bbd193c4afb81a22fcef3027a038ac604c4dda243fff2d30133f7a",
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
			// Thirteen whole packages, in the order the file lists them,
			// which is the order they were added. The gate used to be two
			// files, because every mutant ran every test binary in the
			// module, and scoping the test binaries is what made whole
			// packages affordable. Most arrived by having their survivors
			// killed first, which is the rule; a few were measured before
			// the line was added and had no survivor to kill, which is the
			// only way a package is allowed in without a test being written
			// for it. The ninth is this package: the file this test reads is
			// inside the scope that reads it. The tenth, internal/gocmd, is
			// the first here that starts processes rather than deciding over
			// values; the eleventh, internal/report, is the first that writes
			// files; and the thirteenth, internal/tempowner, is the first
			// that takes a lock.
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
				"internal/testlog/*.go",
				"internal/tempowner/*.go",
				"internal/gitdiff/*.go",
				"internal/snapshot/*.go",
				"internal/cache/*.go",
				"internal/validate/*.go",
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
				"./internal/testlog/...",
				"./internal/tempowner/...",
				"./internal/gitdiff/...",
				"./internal/snapshot/...",
				"./internal/cache/...",
				"./internal/validate/...",
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
		// The floor has moved twice, by the same rule both times: it goes up
		// when half a percent -- one percent, before the first move -- buys
		// more slack than the twenty-one survivors judged too much at 544.
		// One percent of 2432 was twenty-four, which moved it to 99.5; half a
		// percent of 4306 is 21.53, which moves it to 99.75. Between those it
		// stayed put through five widenings, and that was arithmetic rather
		// than inertia. At 4306 scored mutants a quarter of a percent buys ten
		// survivors (4296/4306 = 99.77% clears, 4295/4306 = 99.74% does not),
		// where 99.5 bought twelve when it was set: a floor written as a
		// survivor count rather than as a percentage of a growing catalogue.
		// The arithmetic is written out in the file.
		Policy: mutation.Policy{Strict: false, MinimumScore: 99.75, RequireMutants: true},
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
