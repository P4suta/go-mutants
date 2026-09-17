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
		ID: "955ef7519b8fd9e564bf20a0e8c11f7b899298708405716e59b20b7fd50c680f",
		Reason: "Equivalent: Len returns 0 through the guard for a reversed span " +
			"and EndByte - StartByte otherwise, and that difference is 0 exactly " +
			"when the bounds are equal, so `<` and `<=` report the same length " +
			"for every span.",
	},
	{
		ID: "a36a082afb5fca17a6bb2cf7c01fe2e8b453981e3b3510ae210a1ea45e7e43b4",
		Reason: "Equivalent: the comparison sits inside " +
			"`case s.StartByte != other.StartByte`, where the two start bytes are " +
			"already known to differ, so `<` and `<=` are the same test.",
	},
	{
		ID: "b63d62e41a5d70323435d0afca6657cf9944e117196607b3120ddf74dbf399c4",
		Reason: "Equivalent: the comparison sits inside " +
			"`case s.EndByte != other.EndByte`, where the two end bytes are " +
			"already known to differ, so `<` and `<=` are the same test.",
	},
	{
		ID: "d5ba43b8ceeec3f48f3b5262604413739e76abeb5feecb5bc7b1a8c5791758a2",
		Reason: "Equivalent: `>= 2` and `> 2` differ only for two-byte paths; " +
			"a letter plus colon is rejected by the identical post-clean volume " +
			"guard immediately below, and every other two-byte path fails the " +
			"colon-or-letter predicates.",
	},
	{
		ID: "35ce0eb062fee7958fdaa4bcb7260df0ba8a8d343da3ec1a6950418f8e5ff778",
		Reason: "Equivalent: the comparison sits inside " +
			"`if x.position != y.position`, where the two registry positions are " +
			"already known to differ, so `<` and `<=` are the same test.",
	},
	{
		ID: "bf3c329fd370fabe17a97258e57d7b44e5f90618221ed4e6e801d328f49b5b63",
		Reason: "Equivalent for every coherent tally: `p.MinimumScore > 0` and " +
			"`>= 0` select different runs only when the floor is exactly zero and " +
			"the score is negative, and a negative percentage needs a negative " +
			"Detected count, which Score.Validate reports as incoherent.",
	},
	{
		ID: "b4dcab268c877bd71d01b2be2d2cc005e6868eec52e27d23a68c87d0ea369739",
		Reason: "Unreachable: Build only ever sees candidates Add accepted, and " +
			"Add calls Registry.Verify, which refuses exactly the names " +
			"Registry.Position cannot find -- both read one immutable map -- so " +
			"nothing a caller can build reaches this return.",
	},
	{
		ID: "8f73cb5106525601c9898a33c0a33bd0aaf67690f5e37a3b95a5ca50a46b16ab",
		Reason: "Unkillable: `>` and `>=` pick different catalogues only at " +
			"exactly math.MaxUint32 queued candidates, which is 4,294,967,295 " +
			"Candidate values in one builder.",
	},
	{
		ID: "51376013649354a5072791c005192247af1cd6964f3459e3c264e7e8f088a3d2",
		Reason: "Unkillable: this return is reached only past math.MaxUint32 " +
			"queued candidates, so killing it means holding more than " +
			"4,294,967,295 Candidate values in memory.",
	},
	{
		ID: "9ee39a1c98481eec850ea7e78d8de028cf8dbcd09a2d41d46b677502fc34cd15",
		Reason: "Unkillable: Build only sees candidates Add validated and " +
			"Candidate.ID re-runs that same validation, so the only error left " +
			"for this branch to catch is WriteLengthPrefixed's 4 GiB field guard " +
			"-- see the id.go rows below.",
	},
	{
		ID: "80e96a4140688f46c6f781c5fec24b7d623e97739683e6d037743c62b739f628",
		Reason: "Unkillable: the same branch as the row above -- the error this " +
			"forwards can only come from WriteLengthPrefixed's 4 GiB field guard.",
	},
	{
		ID: "4048137c5cda94871d25333cbbd0fef42c6bf9e6c6b00722076033927bc7cd99",
		Reason: "Unkillable: WriteLengthPrefixed fails only on a field longer " +
			"than math.MaxUint32 bytes, so entering this branch means hashing an " +
			"identity whose path is four gigabytes long.",
	},
	{
		ID: "b711b52d46960264ad9591954ac72680f090f69b50c5c7aceb9078ef7041afbf",
		Reason: "Unkillable: the same branch as the row above -- the error this " +
			"forwards exists only for a field longer than math.MaxUint32 bytes.",
	},
	{
		ID: "50409bd12cbfea335c1dd45447c5b4f601c6505f98c27d9fd31493b675d08c04",
		Reason: "Unkillable: `>` and `>=` disagree only on a string of exactly " +
			"math.MaxUint32 bytes, so telling them apart means allocating four " +
			"gigabytes in a unit test.",
	},
	{
		ID: "92e9574107376c1afa97d9ff5df887c728bab442f93e44bdfc67d72bea7b4491",
		Reason: "Unkillable: this return is reached only for a string longer " +
			"than math.MaxUint32 bytes, so killing it means allocating more than " +
			"four gigabytes in a unit test.",
	},
	{
		ID: "c62feb25adb23b4853bae49beff0089f2c8c22abf5df8efe102b46d24bd431fa",
		Reason: "Equivalent: the loop ends at the first unterminated line, and " +
			"an empty body is that case -- bytes.Cut finds no separator and " +
			"reports none -- so the extra pass `>=` admits breaks before it " +
			"reads anything. The condition is kept over `for {}` because " +
			"dropping it makes two mutants of this package never return.",
	},
	{
		ID: "a7e26cc710d6d039d7a07f34372091e88609a3048b28989f1984e562e49132c4",
		Reason: "Equivalent: a pair where exactly one path carries a drive letter " +
			"differs at the colon, which has no case, so EqualFold and == " +
			"answer alike on it -- the third disjunct therefore never decides " +
			"anything the first two did not.",
	},
	{
		ID: "33219d74e940b3e1acb3284a933da1efa0aa3fb4a88bb557ba646d8647006d37",
		Reason: "Equivalent: blame over an empty pending set answers an empty list, " +
			"so `> 0` and `>= 0` set the same blame on every build; the guard " +
			"is there to skip parsing the compiler's whole output on a trial " +
			"build, which is a cost and not an answer.",
	},
	{
		ID: "de23a2cf021b43d2badd29756cbf1dcfe884a26bdf15b94efdce60203c407939",
		Reason: "Unreachable: the loop is bounded at one pass per catalogued file " +
			"and every pass decides at least one of them, because blame never " +
			"answers an empty list while anything is pending -- so the search " +
			"always returns from inside it.",
	},
	{
		ID: "470a4164cf833f4ac1857da0d7507a33fda8102d2281aa212767a98fda5d1913",
		Reason: "Unreachable: the other half of the same backstop, which exists so " +
			"that a search wrong about its own bound fails closed rather than " +
			"falling out of the loop.",
	},
	{
		ID: "178b97acbd70a6e3d8d66a4203907e5e6c453942fd9724969e9508224bdb9cde",
		Reason: "Unkillable: WriteLengthPrefixed fails only on a field longer than " +
			"math.MaxUint32 bytes, so entering this branch means hashing a " +
			"cache context with a four-gigabyte field in it.",
	},
	{
		ID: "78e42a1d3dceb4e17822dcfc650bf6b87d4abb615f0eae88c4d01a19aa054892",
		Reason: "Unkillable: the same branch as the row above -- the error this " +
			"forwards exists only for a field longer than math.MaxUint32 bytes.",
	},
	{
		ID: "a3af81438206ae8739b803f35805b20afda8af63df16b7a3d99162c42c4f9642",
		Reason: "Equivalent: the listing this guards is of the same directory the " +
			"emptiness check lists a few lines below, with the same call and " +
			"the same message, and a context whose files could not be listed " +
			"always reaches it -- so removing the guard reports the identical " +
			"failure one step later.",
	},
	{
		ID: "95faf75f2c64af748f7536c8c41640c425ba79013e1c45a5ff89291b0633b2cc",
		Reason: "Equivalent: an entry with no recorded bound falls through to the " +
			"default case at the same answer, because `limit <= 0 || limit >= 0` " +
			"is true for every limit -- so `<=` and `<` choose different " +
			"branches and the same value.",
	},
	{
		ID: "6b25c77ddaea960c98f7f47a7542d81a090ba36e857b188dba00ce366d56937a",
		Reason: "Unreachable: an Entry is strings, integers and booleans, and " +
			"encoding/json has no failure for any of them.",
	},
	{
		ID: "3d5b658b0f2a748f2dff487866aa7a00c135148e00e1b8af21859a1a06fa10ff",
		Reason: "Unreachable: the other half of the same branch -- the diagnostic " +
			"returned for an encoding failure an Entry cannot produce.",
	},
	{
		ID: "ae57268dbc443d0849142ad360c336d9ee6e64bf3f5b537f5a161ec486ae2772",
		Reason: "Unreachable where this gate runs: filepath.Rel refuses a pair only " +
			"when the two carry different volume names, which no POSIX path " +
			"does, so both paths here are always relatable.",
	},
	{
		ID: "cc90d65f7ef4b7eade866a06159727d6c18cbcc33b1c03d625611be0865f9e1c",
		Reason: "Unreachable where this gate runs: the same branch as the row above, " +
			"and the answer it gives -- not inside -- for a pair of paths POSIX " +
			"cannot produce.",
	},
	{
		ID: "2c78e03a0b19dc9c4f3d9eb22bb93db43537fdbfdb131caa5bc0463ba7ec1514",
		Reason: "Unreachable: the pattern compiled here is path.Clean's output " +
			"over a path NormalizePath already refused as empty, absolute or " +
			"escaping, which is exactly the set glob.Compile refuses, so the " +
			"error is never non-nil.",
	},
	{
		ID: "6a4676e98aaa8526c1284169f03fb9f2107c75459398ac05554b4a8ad9be9e4d",
		Reason: "Unreachable: the other half of the same branch -- the " +
			"diagnostic returned for a glob.Compile failure that no configured " +
			"report directory can produce.",
	},
	{
		ID: "295ac61dc1070bb46e7ab92c1e86bd2ee36fca2f62fc6c9eb38ed4321f8abdb6",
		Reason: "Unreachable: compileErr is set only when an embedded schema " +
			"cannot be read, parsed, registered or compiled, and " +
			"TestEveryRegisteredSchemaCompiles asserts that none of that " +
			"happens in this build, so this branch is never taken.",
	},
	{
		ID: "336a552a99637e3c828315c82c1aeb4b7799408d2487e8cbbc141a197f37b96f",
		Reason: "Unreachable: the return the row above guards, reported " +
			"`survived (uncovered)` because no suite reaches a line that " +
			"needs compileErr to be non-nil.",
	},
	{
		ID: "4708535aab7682d2dc1d18e8eaff87bd421320b4f8dc37ec779d2e9396caed68",
		Reason: "Unreachable: compileAll compiles every type in the registry " +
			"and schemaFor looks up that same registry, so the lookup " +
			"cannot miss; TestEveryRegisteredSchemaCompiles asserts it for " +
			"every registered type.",
	},
	{
		ID: "22317ee75356784b2d3f6f6020c61fb057c293c9efcadf1661d2334102a46f4f",
		Reason: "Unreachable: the file is read out of an embed.FS fixed at " +
			"build time, and TestEverySchemaIsRegistered plus " +
			"TestEveryRegisteredSchemaCompiles assert that every registered " +
			"name is a file in it, so ReadFile has no failure left to " +
			"return.",
	},
	{
		ID: "983e4a7be022f5a81f7a2424e633df8f9dd3342bf39267fe402aea8a652ff946",
		Reason: "Unreachable: the bytes are an embedded schema this " +
			"repository's own tests parse and compile, so they are JSON in " +
			"every build TestEveryRegisteredSchemaCompiles passes on.",
	},
	{
		ID: "7b561b5eb571a361eb8ea3d21349b46a8e990671a626f18c1e4dfd42c56d7efa",
		Reason: "Unreachable: AddResource fails on a resource identity it " +
			"cannot parse, and TestSchemaIDsMatchTheirFilenames pins every " +
			"embedded schema's `$id` to `baseURL + <file>`, which is a URL " +
			"by construction.",
	},
	{
		ID: "93496397d7258d8d0a08b1cf9aa3052f278d227f69742ad593c9288bf4fb2271",
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
		ID: "72a676ff09f4a94e5850bc2855a77eed08498a3a9951bf6aafba3d34f8eae199",
		Reason: "Equivalent on every platform: `v > int64(maxInt)` and " +
			"`v >= int64(maxInt)` select different branches only at exactly " +
			"int64(maxInt), where the guard returns maxInt and falling " +
			"through returns int(v) -- and int(int64(maxInt)) is maxInt, so " +
			"both spellings narrow every int64 to the same int.",
	},
	{
		ID: "f1375e0c836da7e46f14c5788834f2f0a7b57483ed7440702b35ff47ce62ae48",
		Reason: "Unreachable on a 64-bit build, which is every platform this " +
			"gate runs on: maxInt is int(^uint(0) >> 1), so int64(maxInt) is " +
			"math.MaxInt64 and no int64 is greater than it. Killing it means " +
			"running this package's suite on a 32-bit GOARCH.",
	},
	{
		ID: "b98799291e0028980030250a093df8ccd7d72b035a62413617fc68daf7a23dcc",
		Reason: "Equivalent on every platform: the same argument as the `>` " +
			"row above, at the other end -- `<` and `<=` disagree only at " +
			"exactly int64(minInt), where the guard returns minInt and " +
			"falling through returns int(v), which is minInt.",
	},
	{
		ID: "1304b960e5bd3013784f5fe65e01eb37216b0a393b03bdec3e91452239452879",
		Reason: "Unreachable on a 64-bit build: minInt is -maxInt - 1, so " +
			"int64(minInt) is math.MinInt64 and no int64 is less than it -- " +
			"the mirror of the maxInt row above, and reachable on the same " +
			"32-bit GOARCH.",
	},
	// The one row internal/gocmd brought with it: a narrowing that cannot
	// fail, so the forwarding return underneath it cannot be reached.
	{
		ID: "46ef76f3f811345c6c702668727f2cedc4c61b27e6fa2bf22bc62a3cf6bcafb3",
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
		ID: "fd35bbae14be8dba2102f162903a0fa5a584eb2053e6912dfe699fe7b932d82a",
		Reason: "Equivalent: strings.Builder.Grow is a capacity hint and the " +
			"page is the same bytes for every non-negative size, and the " +
			"vendored viewer is 232 KiB, so len(bundle) + len(document) - " +
			"4096 is positive for any document at all.",
	},
	{
		ID: "bbfcab281ea349926b937be3a29e330e4f9c8bf514578783b28925c909be902f",
		Reason: "Equivalent: the second argument of make is a capacity hint, " +
			"the runtime clamps a negative one to zero, and a map holds the " +
			"same entries whatever it was sized for.",
	},
	{
		ID: "b8a044ec89ef836a0c34a8fc4a47627bbd86413532e2f0e4563556a47ca73c36",
		Reason: "Equivalent: the guard reports 0 for a negative duration and " +
			"d.Milliseconds() otherwise, and a zero duration is 0 " +
			"milliseconds through either branch, so `<` and `<=` render " +
			"every duration the same.",
	},
	{
		ID: "6c38067b0f768ec5b894b66b4ea2465739b49ef09ffca79748c52b2413d05291",
		Reason: "Equivalent: `<=` and `<` disagree only for an id of exactly " +
			"DisplayIDLength characters, where returning `id` and returning " +
			"`id[:DisplayIDLength]` return the same string.",
	},
	{
		ID: "55b4489eeea46a29597c06a15f0ab46ee06988b3755c5fce9b076fb2c91f26e0",
		Reason: "Equivalent: the walk keeps the lexicographically first " +
			"instance location, and `at <= best` differs from `at < best` " +
			"only in assigning best the value it already holds.",
	},
	{
		ID: "bbbb8eb333d7f546978aea2dadaf1a933363f67bb006a25fc671cda4eb741113",
		Reason: "Equivalent: the guard clamps a negative offset to 0, and `<=` " +
			"also assigns 0 to an offset that is already 0.",
	},
	{
		ID: "64e37450b6a2dee0065f78a72587c80f187a874aadfe8a856ef04297039eb3b0",
		Reason: "Equivalent: the guard clamps an offset past the end of the " +
			"file to the end, and `>=` also assigns len(src) to an offset " +
			"that is already len(src).",
	},
	{
		ID: "1a926fd09a5fc77db4d9e9b56d16cb92e296ecb00a21ffaa195c2177256c654f",
		Reason: "Equivalent: sort.SearchInts is asked for the first line start " +
			"greater than the offset, lineStarts[0] is 0, and an offset is " +
			"never negative -- so the search never answers 0 and neither " +
			"`<= 0` nor `< 0` is ever true.",
	},
	{
		ID: "a6a6ea9f7db60d246a0176b526b6cf74844161502dedcb114b9262b99fda5361",
		Reason: "Equivalent: the guard clamps a line index below the first line " +
			"to 0, and `<=` also assigns 0 to an index that is already 0.",
	},
	{
		ID: "0a204388d50d6c0cc0a29d3090e47ec2f92586ad32ee54441891f98046e8a9f9",
		Reason: "Equivalent: the guard clamps an offset past the end of the " +
			"file to the end, and `>=` also assigns len(src) to an offset " +
			"that is already len(src) -- the same argument as the position " +
			"row above.",
	},
	{
		ID: "ae0161ad8ee89960b3662744c45c54d29be0ca4c1cdabebae32bcb68a5059903",
		Reason: "Equivalent: a rejected mutant's disposition carries no " +
			"outcome, and StateOf answers `unfulfilled` through the " +
			"Rejected case and through the default alike, because the zero " +
			"Outcome is not OutcomeSurvived. Nothing else reads the field.",
	},
	{
		ID: "002afe29b7c3fb9e845e6f63116ad42c7a504b57af71887cdf80b58a2599b833",
		Reason: "Equivalent: the `existed` flag is read only by the rollback " +
			"that puts a document back, and a caller handed this error " +
			"returns before there is a rollback to run.",
	},
	{
		ID: "2eae8639701f83ff91784fa2b1ebdedf86cd7740e63f5e659be7a7a75706f2b5",
		Reason: "Equivalent: both callers impose a total order of their own on " +
			"what this returns -- readWorkspace sorts the runs by " +
			"NewestFirst and the damaged rows by path, and RemoveRuns only " +
			"counts them and their bytes -- so the order the files come " +
			"back in is not observable.",
	},
	{
		ID: "f7f2f309e71e2e75ca768e62892ff91ec8da0f25d51e3c6c3e87d54a2197a877",
		Reason: "Unreachable: partition has already translated every result's " +
			"outcome through OutcomeOf, which refuses anything outside the " +
			"six, and mutation.Tally records all six -- so the count this " +
			"forwards cannot fail.",
	},
	{
		ID: "3e485fa52d4a347e50d37e6f796886037c4de50c93e61d33a87e2a1fa8b047fb",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "729594b43b59263117abc577b9e89a7f9e5df929da05ad4b388155941f1cb899",
		Reason: "Unreachable: the same argument one level in -- tallyOf reads " +
			"the outcomes partition has already accepted, so " +
			"mutation.TallyOf cannot refuse one.",
	},
	{
		ID: "843cfbe940c050890bfcd26512ab928636286c69d45c7a5643fb37047cf5f0e7",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "b1b722fecea45bc80b1ea88df100a93153fd6cae7caafe08b02a61e38185c40b",
		Reason: "Unreachable: Outcome.Mutation answers with one of the six core " +
			"outcomes or with an error the line above returns, and " +
			"mutation.Tally.Record has a case for all six.",
	},
	{
		ID: "ffb56e7c15223a5bb71d3606b2bf7e9b5dae52da8bdb7d57f3e3a4f9502db35a",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "3f796fb632e674c7310a4d6cc295e5bd577c4e7ee5d1aec5030a526b901432cc",
		Reason: "Unreachable: the counts disagree only when a row was not " +
			"consumed by the catalogue walk, and a row is consumed exactly " +
			"when its id is catalogued -- so the loop above always finds " +
			"the row this line exists to report the absence of.",
	},
	{
		ID: "a4cb8ec614f9f03fbba05e82032b94b78bedba750da641ef8ad546b52bce183b",
		Reason: "Unreachable: every module's report has been built or merged " +
			"by the time this runs, and a report that was is one whose every " +
			"outcome mutation.TallyOf accepted -- so the aggregate over those " +
			"same outcomes cannot fail. Both constructors return through this " +
			"one line, which is why it is declared once rather than at each of them.",
	},
	{
		ID: "10a21695dcf7b185c5de1f3d8c5c9acdbbdb8edb74aec727750664822db3ad1b",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "93e0d4258830b5b7507f36a5d38b06e1e8d110710014ef8826edef672017c2ea",
		Reason: "Unkillable: encoding/json fails only on a value it cannot " +
			"represent -- a cycle, a channel or function field, a " +
			"non-finite float -- and a Projection is strings, ints and a " +
			"map of them, so no value of the type can make Encode fail.",
	},
	{
		ID: "4ac08a57be3c66c3ecd05963d556699612d85247f1b4325ad7045c4a175edaea",
		Reason: "Unkillable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "7e1351f637e0f91ef7d233ac61eea4ba38c787afe4f04775983ce63c69d8c189",
		Reason: "Unkillable: the same encoding, reached through WriteArtifacts " +
			"-- the document it publishes is the Projection the row above " +
			"is about.",
	},
	{
		ID: "8a62628e06decefaa8b489e7167f586359387785bbc39bcc776837dbbd59c025",
		Reason: "Unkillable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "7d8a53f6e9079d87975e64b2161da7327ea2c74747055dab137bd95c1150d5db",
		Reason: "Unreachable: jsonschema/v6's AddResource stores the document " +
			"and defers every check to Compile, so it fails only for a url " +
			"it cannot parse or one already registered -- and this compiler " +
			"is used once, for one constant url.",
	},
	{
		ID: "d31d673eaf91673796968ae1a4492d157375e203e27e786a34327ba77831b781",
		Reason: "Unreachable: pointerOf answers `the document root` for an " +
			"empty instance location and a pointer beginning with a slash " +
			"otherwise, so every leaf the walk reaches sets best to " +
			"something -- and the walk always reaches at least the error it " +
			"was given.",
	},
	{
		ID: "5614b7ba3e9f4ddddfd7fa4a27ea762743d27e9db32efa0d9f2558ced6413003",
		Reason: "Unkillable: filepath.Abs returns an error only when os.Getwd " +
			"does, which needs this process's own working directory to have " +
			"been deleted -- a state a test would be arranging for every " +
			"other test in the same binary.",
	},
	{
		ID: "68cc73613b4ee13c84c15455c41d3e3c2a03257150376b849c00f7eb7bf07018",
		Reason: "Unkillable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "d996a9ae543987b8d064d76f98195232bba6ff816d8f9d0226611f7c6809ced1",
		Reason: "Unreachable: the answer for a volume root that does not " +
			"resolve, which needs a path none of whose ancestors exist -- " +
			"and the root itself always does.",
	},
	{
		ID: "908af008c3e76944b0813c02c7705c85c9554b4e28f9b0e0a96f3da08c0b6d1e",
		Reason: "Unreachable: filepath.EvalSymlinks walks a path from the left " +
			"and reports the first name it cannot resolve, so a path it " +
			"calls `not there` and a parent that fails for some other " +
			"reason cannot both happen -- the parent shares the prefix and " +
			"answers with the same failure.",
	},
	{
		ID: "03040a2de8a23b4cfb720924dbe074447aa9ce6ee9d7cf07ffbeb81bc05a681b",
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
