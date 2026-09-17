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

const repositoryConfig = "../../" + FileName

var repositoryExpectations = []Expectation{
	{
		ID: "63a1e906c55ca93c330fe7b05f5bfa23092bf95241be4c7e7f954a2cb6a7e2a3",
		Reason: "Equivalent: Len returns 0 through the guard for a reversed span " +
			"and EndByte - StartByte otherwise, and that difference is 0 exactly " +
			"when the bounds are equal, so `<` and `<=` report the same length " +
			"for every span.",
	},
	{
		ID: "7dcef76e4f6c796661772109a65a128b66f3834ead660b1d254dd6b075830c1d",
		Reason: "Equivalent: the comparison sits inside " +
			"`case s.StartByte != other.StartByte`, where the two start bytes are " +
			"already known to differ, so `<` and `<=` are the same test.",
	},
	{
		ID: "23f02e67610ceead08de5fd218a016e31dea7cc57b7750e6e32627657b67aa0a",
		Reason: "Equivalent: the comparison sits inside " +
			"`case s.EndByte != other.EndByte`, where the two end bytes are " +
			"already known to differ, so `<` and `<=` are the same test.",
	},
	{
		ID: "c5d9892fd86b594f80fb513f5fb23a2b9641db022bc0fa5e6eb21095a5df1af8",
		Reason: "Equivalent: `>= 2` and `> 2` differ only for two-byte paths; " +
			"a letter plus colon is rejected by the identical post-clean volume " +
			"guard immediately below, and every other two-byte path fails the " +
			"colon-or-letter predicates.",
	},
	{
		ID: "8bda417a0eca8af4770505097675d140aecae223c86a85b0f15f9aeaa22dae9f",
		Reason: "Equivalent: the comparison sits inside " +
			"`if x.position != y.position`, where the two registry positions are " +
			"already known to differ, so `<` and `<=` are the same test.",
	},
	{
		ID: "fdd9258e38596caab607adb387e8e3b442aac76be8405a4505818e79201e7fb7",
		Reason: "Equivalent for every coherent tally: `p.MinimumScore > 0` and " +
			"`>= 0` select different runs only when the floor is exactly zero and " +
			"the score is negative, and a negative percentage needs a negative " +
			"Detected count, which Score.Validate reports as incoherent.",
	},
	{
		ID: "736038ee1234930ef01739c62400e9df1d4121ca8c86946acb47062347206829",
		Reason: "Unreachable: Build only ever sees candidates Add accepted, and " +
			"Add calls Registry.Verify, which refuses exactly the names " +
			"Registry.Position cannot find -- both read one immutable map -- so " +
			"nothing a caller can build reaches this return.",
	},
	{
		ID: "937a379c59ead6d9ca282c7df556b023e9b0bd1ed7fd90fb4478870fa4b5b72e",
		Reason: "Unkillable: `>` and `>=` pick different catalogues only at " +
			"exactly math.MaxUint32 queued candidates, which is 4,294,967,295 " +
			"Candidate values in one builder.",
	},
	{
		ID: "8bba1e45a52462a8f8b30600d2d945b3c9cd8a21d6a01ecfdc3c4910273f23b0",
		Reason: "Unkillable: this return is reached only past math.MaxUint32 " +
			"queued candidates, so killing it means holding more than " +
			"4,294,967,295 Candidate values in memory.",
	},
	{
		ID: "bf29041b7fceaaccc219e0b33d3cf187256b053b7698c9b550a6d3e1c154f964",
		Reason: "Unkillable: Build only sees candidates Add validated and " +
			"Candidate.ID re-runs that same validation, so the only error left " +
			"for this branch to catch is WriteLengthPrefixed's 4 GiB field guard " +
			"-- see the id.go rows below.",
	},
	{
		ID: "ce7a4344bcd0f7307ca93b419987e0b71b65c6e902d4f35fdac93b73edee0f96",
		Reason: "Unkillable: the same branch as the row above -- the error this " +
			"forwards can only come from WriteLengthPrefixed's 4 GiB field guard.",
	},
	{
		ID: "c25d16b9ed17f150c6ae5b0aa3aab455c3994bd0024749668a990d6ef6bf0d0d",
		Reason: "Unkillable: WriteLengthPrefixed fails only on a field longer " +
			"than math.MaxUint32 bytes, so entering this branch means hashing an " +
			"identity whose path is four gigabytes long.",
	},
	{
		ID: "28e810b9251e0abdb5cfba3ef5161458cd940ad6c4155f2999b07bd3c1ada3f5",
		Reason: "Unkillable: the same branch as the row above -- the error this " +
			"forwards exists only for a field longer than math.MaxUint32 bytes.",
	},
	{
		ID: "d0e80f82fc7edffddc96d2ade9ad7f5427c3246e442558e3cf99857806b0d17b",
		Reason: "Unkillable: `>` and `>=` disagree only on a string of exactly " +
			"math.MaxUint32 bytes, so telling them apart means allocating four " +
			"gigabytes in a unit test.",
	},
	{
		ID: "5702365bce3a40457d569258bb64ee318cfcaa69186d1fb5719586b473af5f61",
		Reason: "Unkillable: this return is reached only for a string longer " +
			"than math.MaxUint32 bytes, so killing it means allocating more than " +
			"four gigabytes in a unit test.",
	},
	{
		ID: "bae100d6ea8bb1ef5dceadec8026d2d077aaa21d0a49580d3a248e2843ed25b9",
		Reason: "Equivalent: the loop ends at the first unterminated line, and " +
			"an empty body is that case -- bytes.Cut finds no separator and " +
			"reports none -- so the extra pass `>=` admits breaks before it " +
			"reads anything. The condition is kept over `for {}` because " +
			"dropping it makes two mutants of this package never return.",
	},
	{
		ID: "1f966be03c40e24842479967bb29897c83776ce47c38ff6d99dadcdc4eb28e85",
		Reason: "Equivalent: a pair where exactly one path carries a drive letter " +
			"differs at the colon, which has no case, so EqualFold and == " +
			"answer alike on it -- the third disjunct therefore never decides " +
			"anything the first two did not.",
	},
	{
		ID: "fa492c2a6743cff85732bef6967a4f69b80bed30a6e69f1530ced2ae8c852548",
		Reason: "Equivalent: blame over an empty pending set answers an empty list, " +
			"so `> 0` and `>= 0` set the same blame on every build; the guard " +
			"is there to skip parsing the compiler's whole output on a trial " +
			"build, which is a cost and not an answer.",
	},
	{
		ID: "f21d190813faef5b2eaae1c26e59c12240920a506cd3335fbc331cba9a431ea6",
		Reason: "Unreachable: the loop is bounded at one pass per catalogued file " +
			"and every pass decides at least one of them, because blame never " +
			"answers an empty list while anything is pending -- so the search " +
			"always returns from inside it.",
	},
	{
		ID: "2788ee073fdb997aa1ce09bc5e9d2693d67e2c14d34d4f260958c68c05572297",
		Reason: "Unreachable: the other half of the same backstop, which exists so " +
			"that a search wrong about its own bound fails closed rather than " +
			"falling out of the loop.",
	},
	{
		ID: "87f3943de1449a1bd835a568d55a2b9d30cbc4b7705b47298eebf82e9c844f29",
		Reason: "Unkillable: WriteLengthPrefixed fails only on a field longer than " +
			"math.MaxUint32 bytes, so entering this branch means hashing a " +
			"cache context with a four-gigabyte field in it.",
	},
	{
		ID: "2dc4c66415f81f6cb864ca00bd999e1c422e9d4fd80016765a75e558287aa12d",
		Reason: "Unkillable: the same branch as the row above -- the error this " +
			"forwards exists only for a field longer than math.MaxUint32 bytes.",
	},
	{
		ID: "8ba942b76f47b912633ee5289a96460bd158a92f1b72c82703557e50e596d43c",
		Reason: "Equivalent: the listing this guards is of the same directory the " +
			"emptiness check lists a few lines below, with the same call and " +
			"the same message, and a context whose files could not be listed " +
			"always reaches it -- so removing the guard reports the identical " +
			"failure one step later.",
	},
	{
		ID: "b6e6c41f69f9919cab15ce9fab23a9a951ae4d985ebcedcd9deee987f2cff50d",
		Reason: "Equivalent: an entry with no recorded bound falls through to the " +
			"default case at the same answer, because `limit <= 0 || limit >= 0` " +
			"is true for every limit -- so `<=` and `<` choose different " +
			"branches and the same value.",
	},
	{
		ID: "308cdd4c44d8b2fbdb10f6d9aceb26953e05dc4d74d2ef23d76605d9c38ba68f",
		Reason: "Unreachable: an Entry is strings, integers and booleans, and " +
			"encoding/json has no failure for any of them.",
	},
	{
		ID: "d18ef4d5d206178071c8a43676d4d524678d7ece23a5e4c9e458f053051065ae",
		Reason: "Unreachable: the other half of the same branch -- the diagnostic " +
			"returned for an encoding failure an Entry cannot produce.",
	},
	{
		ID: "e34e7bb4d13fcfda70f9f62a03e36828b9bb6d914fec5d17a6a92598f23cbbcb",
		Reason: "Unreachable where this gate runs: filepath.Rel refuses a pair only " +
			"when the two carry different volume names, which no POSIX path " +
			"does, so both paths here are always relatable.",
	},
	{
		ID: "2113fc62cde4f5b998ff113870a78ada0471a46cd7e1fd90f416037b7b33dd4b",
		Reason: "Unreachable where this gate runs: the same branch as the row above, " +
			"and the answer it gives -- not inside -- for a pair of paths POSIX " +
			"cannot produce.",
	},
	{
		ID: "a8b8857970b7bcc218c62c335699534b5f163bbd158bc0c0755837eec974daa4",
		Reason: "Unreachable: the pattern compiled here is path.Clean's output " +
			"over a path NormalizePath already refused as empty, absolute or " +
			"escaping, which is exactly the set glob.Compile refuses, so the " +
			"error is never non-nil.",
	},
	{
		ID: "bcb51e8c7a8cf07131f095a6b668303815866c4a2c0363ce5bc86291b8b5a6a8",
		Reason: "Unreachable: the other half of the same branch -- the " +
			"diagnostic returned for a glob.Compile failure that no configured " +
			"report directory can produce.",
	},
	{
		ID: "66d1e8ba07846d8e6e62a0c346ec758e7fb4130126a8a63fee05d03a92452a15",
		Reason: "Unreachable: compileErr is set only when an embedded schema " +
			"cannot be read, parsed, registered or compiled, and " +
			"TestEveryRegisteredSchemaCompiles asserts that none of that " +
			"happens in this build, so this branch is never taken.",
	},
	{
		ID: "bc6cb579c2249ec905a3f95105a492e0322818badc06cf72b5857943adf82bcd",
		Reason: "Unreachable: the return the row above guards, reported " +
			"`survived (uncovered)` because no suite reaches a line that " +
			"needs compileErr to be non-nil.",
	},
	{
		ID: "64b8043bb941e0bd53425424d71ab64a2ab6096937106ca59f91dd15cfe0ddac",
		Reason: "Unreachable: compileAll compiles every type in the registry " +
			"and schemaFor looks up that same registry, so the lookup " +
			"cannot miss; TestEveryRegisteredSchemaCompiles asserts it for " +
			"every registered type.",
	},
	{
		ID: "2ca1d6d5543ed090af2ccb808b0a2071ae804de13dacf15896661260ab1b155d",
		Reason: "Unreachable: the file is read out of an embed.FS fixed at " +
			"build time, and TestEverySchemaIsRegistered plus " +
			"TestEveryRegisteredSchemaCompiles assert that every registered " +
			"name is a file in it, so ReadFile has no failure left to " +
			"return.",
	},
	{
		ID: "c1972268c628e136e4ccd23c15d3b54c652cb353714c9fbe026585365d8c4d4f",
		Reason: "Unreachable: the bytes are an embedded schema this " +
			"repository's own tests parse and compile, so they are JSON in " +
			"every build TestEveryRegisteredSchemaCompiles passes on.",
	},
	{
		ID: "4eb53988b253b11373222ceb761f2240c3bd64faf924b2f7bab5a104c3db5c45",
		Reason: "Unreachable: AddResource fails on a resource identity it " +
			"cannot parse, and TestSchemaIDsMatchTheirFilenames pins every " +
			"embedded schema's `$id` to `baseURL + <file>`, which is a URL " +
			"by construction.",
	},
	{
		ID: "ec21b71d83ed1f1c3255af508576641a71ce50054896e139d7a22a8dab39093e",
		Reason: "Unreachable: every registered schema compiles, which is " +
			"exactly what TestEveryRegisteredSchemaCompiles asserts by " +
			"requiring an invalid document to come back GOM5003 rather than " +
			"GOM5004.",
	},
	{
		ID: "83bab2ecab55e5d8105d9b8267ebba5284e1dbba67b13da72dba48f8cf121986",
		Reason: "Equivalent on every platform: `v > int64(maxInt)` and " +
			"`v >= int64(maxInt)` select different branches only at exactly " +
			"int64(maxInt), where the guard returns maxInt and falling " +
			"through returns int(v) -- and int(int64(maxInt)) is maxInt, so " +
			"both spellings narrow every int64 to the same int.",
	},
	{
		ID: "bb4881aeb76c75dc7a33f524184d516f6002e69606a33dcc0211e4726186e463",
		Reason: "Unreachable on a 64-bit build, which is every platform this " +
			"gate runs on: maxInt is int(^uint(0) >> 1), so int64(maxInt) is " +
			"math.MaxInt64 and no int64 is greater than it. Killing it means " +
			"running this package's suite on a 32-bit GOARCH.",
	},
	{
		ID: "59f68100b6fcbf8d0c15d1f6bc36307e4ff710a070cfdbcd4be1a3f33d347b1a",
		Reason: "Equivalent on every platform: the same argument as the `>` " +
			"row above, at the other end -- `<` and `<=` disagree only at " +
			"exactly int64(minInt), where the guard returns minInt and " +
			"falling through returns int(v), which is minInt.",
	},
	{
		ID: "548d95eaf64c8c79c720c2c170b65f84b2ce811b50d849ad0b962cc9340bff57",
		Reason: "Unreachable on a 64-bit build: minInt is -maxInt - 1, so " +
			"int64(minInt) is math.MinInt64 and no int64 is less than it -- " +
			"the mirror of the maxInt row above, and reachable on the same " +
			"32-bit GOARCH.",
	},
	{
		ID: "0c862985d074d905f6c8377a8b8bfe213322c20cb8bd38643cb7fdeca2ec23a2",
		Reason: "Unreachable: parseVersion has four failure returns and every " +
			"one of them is a *Error carrying CodeVersionUnparsable, so " +
			"errors.As above always matches and this forwarding return is " +
			"reached only by an error kind parseVersion does not produce.",
	},
	{
		ID: "03c9300fb2b8bffe6a9165115e33ac08527c9c1085542f220c070001aa084560",
		Reason: "Equivalent: strings.Builder.Grow is a capacity hint and the " +
			"page is the same bytes for every non-negative size, and the " +
			"vendored viewer is 232 KiB, so len(bundle) + len(document) - " +
			"4096 is positive for any document at all.",
	},
	{
		ID: "537360e0ca9e43f59ac1b74e160e9df40152c9d1803922c174c1bfb8eaff3cba",
		Reason: "Equivalent: the second argument of make is a capacity hint, " +
			"the runtime clamps a negative one to zero, and a map holds the " +
			"same entries whatever it was sized for.",
	},
	{
		ID: "3d10c13b02b7590eaa90c386d6df6bb599dcade5784ac7f161c48803d27b5a58",
		Reason: "Equivalent: the guard reports 0 for a negative duration and " +
			"d.Milliseconds() otherwise, and a zero duration is 0 " +
			"milliseconds through either branch, so `<` and `<=` render " +
			"every duration the same.",
	},
	{
		ID: "0561dd5caa6ea6b62ed57014d125936b62bb32bb529211abf2183968a55ff84b",
		Reason: "Equivalent: `<=` and `<` disagree only for an id of exactly " +
			"DisplayIDLength characters, where returning `id` and returning " +
			"`id[:DisplayIDLength]` return the same string.",
	},
	{
		ID: "08b789f806c05e2f1a2b2d633caf030645ea860fd724c9a62e10670aeb49fb82",
		Reason: "Equivalent: the walk keeps the lexicographically first " +
			"instance location, and `at <= best` differs from `at < best` " +
			"only in assigning best the value it already holds.",
	},
	{
		ID: "cd39e4233937817547b07ab4f5b9d8562df8ed18ee6b9f23ae80109bf66121d6",
		Reason: "Equivalent: the guard clamps a negative offset to 0, and `<=` " +
			"also assigns 0 to an offset that is already 0.",
	},
	{
		ID: "16359325f65504f03b5a3827c9bf1c5edadc323790ddf9f5f651311f2420b3ef",
		Reason: "Equivalent: the guard clamps an offset past the end of the " +
			"file to the end, and `>=` also assigns len(src) to an offset " +
			"that is already len(src).",
	},
	{
		ID: "6b9212715cf6fa58919b35a1b7c1e1e262757bda7af76a00060eaccc24c2f975",
		Reason: "Equivalent: sort.SearchInts is asked for the first line start " +
			"greater than the offset, lineStarts[0] is 0, and an offset is " +
			"never negative -- so the search never answers 0 and neither " +
			"`<= 0` nor `< 0` is ever true.",
	},
	{
		ID: "30e374c24b09485dfa9e84cc9ecdbc8f24c0c7f9ced991f2cac06afab1e14f65",
		Reason: "Equivalent: the guard clamps a line index below the first line " +
			"to 0, and `<=` also assigns 0 to an index that is already 0.",
	},
	{
		ID: "193067418c252a5d83ad85cac65d028c295d1fffb86e7c65617240176125ee77",
		Reason: "Equivalent: the guard clamps an offset past the end of the " +
			"file to the end, and `>=` also assigns len(src) to an offset " +
			"that is already len(src) -- the same argument as the position " +
			"row above.",
	},
	{
		ID: "bdae57232f4e20a6646b765768f713c1daf80ef03a49543c85fe1ff71bcc4305",
		Reason: "Equivalent: a rejected mutant's disposition carries no " +
			"outcome, and StateOf answers `unfulfilled` through the " +
			"Rejected case and through the default alike, because the zero " +
			"Outcome is not OutcomeSurvived. Nothing else reads the field.",
	},
	{
		ID: "a0bfcfed8548a3e763634afb0403fef00a9bf4e9c2096d209d93a3624834ac6e",
		Reason: "Equivalent: the `existed` flag is read only by the rollback " +
			"that puts a document back, and a caller handed this error " +
			"returns before there is a rollback to run.",
	},
	{
		ID: "20b2bab24afa3e98c5dd94f465f67e69a21f9d42c8bb7b834eb6e0c310542866",
		Reason: "Equivalent: both callers impose a total order of their own on " +
			"what this returns -- readWorkspace sorts the runs by " +
			"NewestFirst and the damaged rows by path, and RemoveRuns only " +
			"counts them and their bytes -- so the order the files come " +
			"back in is not observable.",
	},
	{
		ID: "62aaecaf36390de08849eea95d98e84cb596ef5de4278cfe02f39d7116020f62",
		Reason: "Unreachable: partition has already translated every result's " +
			"outcome through OutcomeOf, which refuses anything outside the " +
			"six, and mutation.Tally records all six -- so the count this " +
			"forwards cannot fail.",
	},
	{
		ID: "fd4d5fffe8432a3f7e5199f397fa1648d9b6c7f79f9e6df7bb1e8c5e2a15aab4",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "f7fbf9906bdf5748aed49004b7d8c4ceaa4428ace02763ca76ce3f0b5fd346ab",
		Reason: "Unreachable: the same argument one level in -- tallyOf reads " +
			"the outcomes partition has already accepted, so " +
			"mutation.TallyOf cannot refuse one.",
	},
	{
		ID: "e4bfb4acf577ec3f511dc721f4cde7cee31492f1d53ceda5e923aedb9db112ee",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "66af75bbdd630c07f7cb8b3d1ef90ce132aa7d4e722189a6ddf8fa42d7a708d0",
		Reason: "Unreachable: Outcome.Mutation answers with one of the six core " +
			"outcomes or with an error the line above returns, and " +
			"mutation.Tally.Record has a case for all six.",
	},
	{
		ID: "4279395c04cf49390dea30df4bde29505dc9215aebb96c7746056af2c89ea7f6",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "405c26a62bfa4c1bace6ce37e920197c5eca156c86d27b21df86541cf4e709e2",
		Reason: "Unreachable: the counts disagree only when a row was not " +
			"consumed by the catalogue walk, and a row is consumed exactly " +
			"when its id is catalogued -- so the loop above always finds " +
			"the row this line exists to report the absence of.",
	},
	{
		ID: "f3cc94e26231d98ddc19672b181add116d54b3ec9ea1b581d853c34e8265dab3",
		Reason: "Unreachable: every module's report has been built or merged " +
			"by the time this runs, and a report that was is one whose every " +
			"outcome mutation.TallyOf accepted -- so the aggregate over those " +
			"same outcomes cannot fail. Both constructors return through this " +
			"one line, which is why it is declared once rather than at each of them.",
	},
	{
		ID: "72380a881c7538f1bff6c85fc6215f54dcd24bb222f8fa85d3305b848d6c7d75",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "032fcf80ffddb8f876135bcc03fd78691de4a9a0ba918dbfe4639a6eee404e92",
		Reason: "Unkillable: encoding/json fails only on a value it cannot " +
			"represent -- a cycle, a channel or function field, a " +
			"non-finite float -- and a Projection is strings, ints and a " +
			"map of them, so no value of the type can make Encode fail.",
	},
	{
		ID: "90f02992edecc1d22ddb551bfcf7c48b083141520f21650e82398bf76482c862",
		Reason: "Unkillable: the same failure as the row above, on the line " +
			"that would report it.",
	},
	{
		ID: "e16cce66c286994d6e96485f17652b3c742a40a772094b5c3888a8a07d994205",
		Reason: "Unkillable: the same encoding, reached through WriteArtifacts " +
			"-- the document it publishes is the Projection the row above " +
			"is about.",
	},
	{
		ID: "15b009ead2253197b7e45c517c4e98986dbceff798f2b97c811e51714680a19e",
		Reason: "Unkillable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "3fb39ffdc4043f2724e478fd04bbae647f02ac31d7ba8d808da6e888453bb5fb",
		Reason: "Unreachable: jsonschema/v6's AddResource stores the document " +
			"and defers every check to Compile, so it fails only for a url " +
			"it cannot parse or one already registered -- and this compiler " +
			"is used once, for one constant url.",
	},
	{
		ID: "b29b5a8acc2d9728368fde1157dad2361e0e902f128b4b9917ec5721a7253a2f",
		Reason: "Unreachable: pointerOf answers `the document root` for an " +
			"empty instance location and a pointer beginning with a slash " +
			"otherwise, so every leaf the walk reaches sets best to " +
			"something -- and the walk always reaches at least the error it " +
			"was given.",
	},
	{
		ID: "19ab23139784bde136642d7ec7afcf34bfe78fa04d70ff77c29edaf80fcb53c1",
		Reason: "Unkillable: filepath.Abs returns an error only when os.Getwd " +
			"does, which needs this process's own working directory to have " +
			"been deleted -- a state a test would be arranging for every " +
			"other test in the same binary.",
	},
	{
		ID: "c4c26dabd3bf10212a37d05972e7b012e65c397cbdecde2ba1de9113527dd581",
		Reason: "Unkillable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
	{
		ID: "c0d6ebf39b3703cd20a36005fc3d82840edba08fe8bf14c5f80d871fcb3bf301",
		Reason: "Unreachable: the answer for a volume root that does not " +
			"resolve, which needs a path none of whose ancestors exist -- " +
			"and the root itself always does.",
	},
	{
		ID: "e7198f838f3abfe96711c8dbf1d252cc6d7c701d19dbc4f81410586f19a269d1",
		Reason: "Unreachable: filepath.EvalSymlinks walks a path from the left " +
			"and reports the first name it cannot resolve, so a path it " +
			"calls `not there` and a parent that fails for some other " +
			"reason cannot both happen -- the parent shares the prefix and " +
			"answers with the same failure.",
	},
	{
		ID: "12ca89f9f9dd1e0375da6e769606ca34c3199be67893f279dc3ca3abc30039c9",
		Reason: "Unreachable: the same failure as the row above, on the line " +
			"that would forward it.",
	},
}

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
			Exclude:   []string{"**/*_test.go", "**/testdata/**", "fixtures/**", "vendor-assets/**"},
			Operators: nil,
			Profile:   mutation.TierBalanced,
			Expect:    repositoryExpectations,
		},
		Test: Test{
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
			Timeout:      0,
			Memory:       0,
			BaselineRuns: 3,
			Narrowing:    NarrowingTest,
			Probing:      ProbingOff,
		},
		Execution: Execution{Jobs: 4},
		Cache:     Cache{Mode: CacheAuto, Directory: ""},
		Policy:    mutation.Policy{Strict: false, MinimumScore: 99.75, RequireMutants: true},
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

	if err := (Overlay{
		Include: Explicit(resolved.Mutation.Include),
		Exclude: Explicit(resolved.Mutation.Exclude),
	}).Validate(); err != nil {
		t.Errorf("the repository's patterns do not compile: %v", err)
	}
}

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
