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

func FuzzParseShardSpec(f *testing.F) {
	for _, seed := range []string{
		"1/4", " 1 / 4 ", "1/1", "4/4", "0/4", "5/4", "-1/4", "1/0", "1/-4",
		"", "/", "1/", "/4", "1", "1/4/9", "a/b", "1/4x", "+1/+4",
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
