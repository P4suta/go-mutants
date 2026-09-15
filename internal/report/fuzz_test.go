// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
)

// FuzzParseDocument is the promise this package makes about a document it may
// not have written.
//
// A run report is read back in three places and none of them is a round trip
// within one process: `report merge` reads the shards a CI matrix published,
// `report render` reads a document somebody kept, and the history store reads
// what an earlier version of this tool filed. So the bytes may be truncated by
// a killed job, edited by somebody, or written by a build that declared a field
// this one does not -- and the decoder is strict on purpose, because a field
// quietly dropped on the way in is a field missing from the merged document
// with nothing to say it was ever there.
//
// Three properties:
//
//   - it never panics, whatever the file holds;
//   - a refusal is a typed error carrying a code, never a bare JSON error;
//   - a document it accepts survives being written back out and read again,
//     which is what `report merge` does to every shard it is given.
func FuzzParseDocument(f *testing.F) {
	f.Add(`{"schema_version":1,"document_type":"go-mutants/run-report"}`)
	f.Add(`{}`)
	f.Add(``)
	f.Add(`null`)
	f.Add(`[]`)
	f.Add(`{"schema_version":1`)
	f.Add(`{"schema_version":"one"}`)
	f.Add(`{"unknown_field":1}`)
	f.Add(`{"schema_version":1,"mutants":[]}`)
	f.Add(`{"schema_version":1,"mutants":[{"id":"abc"}]}`)
	f.Add(`{"schema_version":1,"shard":{"index":1,"total":2,"assignment":"x"}}`)
	f.Add("{\"schema_version\":1}\n{\"schema_version\":1}")
	f.Add(`{"schema_version":99999999999999999999}`)
	f.Add("\x00\x00\x00")

	f.Fuzz(func(t *testing.T, document string) {
		parsed, err := report.Parse([]byte(document))
		if err != nil {
			var refusal *report.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("Parse returned %T, want an *Error a caller can branch on: %v", err, err)
			}
			if refusal.Code == "" {
				t.Fatalf("Parse refused with no code")
			}
			if parsed != nil {
				t.Fatalf("Parse refused and returned a document anyway")
			}
			return
		}
		if parsed == nil {
			t.Fatal("Parse accepted the document and returned nothing")
		}

		// What `report merge` does to every shard: encode the value it decoded
		// and decode it again. A field the decoder accepted and the encoder
		// cannot write is a field that would vanish from a merged document.
		encoded, err := json.Marshal(parsed)
		if err != nil {
			t.Fatalf("a document Parse accepted will not encode: %v", err)
		}
		again, err := report.Parse(encoded)
		if err != nil {
			t.Fatalf("a document this package encoded will not parse: %v\n%s", err, encoded)
		}
		second, err := json.Marshal(again)
		if err != nil {
			t.Fatalf("the re-parsed document will not encode: %v", err)
		}
		if string(encoded) != string(second) {
			t.Fatalf("encoding is not stable:\n%s\n%s", encoded, second)
		}
	})
}

// FuzzParseShardSpec is the promise this package makes about a value a user
// types on a command line.
//
// `--shard 1/4` is written by hand in a CI configuration, and the failure it
// guards against is silent: a specification misread as a different shard would
// run one part of the catalogue twice and another not at all, and the merged
// document would say it covered everything. So the parser refuses rather than
// interprets, and the two properties below are what "refuses" has to mean.
func FuzzParseShardSpec(f *testing.F) {
	for _, seed := range []string{
		"1/4", " 1 / 4 ", "1/1", "4/4", "0/4", "5/4", "-1/4", "1/0", "1/-4",
		"", "/", "1/", "/4", "1", "1/4/9", "a/b", "1/4x", "+1/+4",
		// Arabic-Indic digits, written as escapes: a digit `strconv.Atoi`
		// refuses is a spec this parser has to refuse rather than round.
		"\u0661/\u0664",
		"01/04", "1/99999999999999999999", "\n1/4\n",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, spec string) {
		shard, err := report.ParseShard(spec)
		if err != nil {
			var refusal *report.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("ParseShard returned %T, want an *Error a caller can branch on: %v", err, err)
			}
			if refusal.Code == "" {
				t.Fatalf("ParseShard refused with no code")
			}
			if shard != (report.Shard{}) {
				t.Fatalf("ParseShard refused and returned %+v anyway", shard)
			}
			return
		}
		if shard.Index < 1 || shard.Total < 1 || shard.Index > shard.Total {
			t.Fatalf("ParseShard(%q) = shard %d of %d, which names no part of a run",
				spec, shard.Index, shard.Total)
		}
		if shard.Assignment == "" {
			t.Fatalf("ParseShard(%q) named no assignment function, so nobody can recompute the partition", spec)
		}
		// And the spec is what it says it is: a value that parsed has both
		// numbers in it, in that order.
		index, total, ok := strings.Cut(strings.TrimSpace(spec), "/")
		if !ok {
			t.Fatalf("ParseShard accepted %q, which holds no slash", spec)
		}
		if got, err := strconv.Atoi(strings.TrimSpace(index)); err != nil || got != shard.Index {
			t.Fatalf("ParseShard(%q) read the index as %d", spec, shard.Index)
		}
		if got, err := strconv.Atoi(strings.TrimSpace(total)); err != nil || got != shard.Total {
			t.Fatalf("ParseShard(%q) read the total as %d", spec, shard.Total)
		}
	})
}
