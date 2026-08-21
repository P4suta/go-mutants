// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
)

// TestCodesAreUniqueAndInBlock holds this package inside the ranges it owns.
//
// GOM5001 to GOM5009 belong to internal/schemas, which checks the documents
// this package writes; overlapping the two would make a code ambiguous in
// exactly the situation a user is trying to tell them apart — a report that was
// written and a report that does not validate.
//
// GOM78xx is the second block, and it is sharding: the `--shard` specification,
// the document's `shard` block, and every refusal `report merge` can make. It
// is checked here rather than merely allowed, so that a shard code cannot drift
// into the GOM51xx range or a reporting code into the shard one.
func TestCodesAreUniqueAndInBlock(t *testing.T) {
	t.Parallel()

	codes := report.Codes()
	if len(codes) == 0 {
		t.Fatal("this package reports no codes at all")
	}
	shardCodes := map[report.Code]bool{
		report.CodeInvalidShardSpec:       true,
		report.CodeInvalidShard:           true,
		report.CodeNoShardReports:         true,
		report.CodeNotAShardReport:        true,
		report.CodeIncongruentShards:      true,
		report.CodeIncompleteShardSet:     true,
		report.CodeShardOwnershipMismatch: true,
	}
	seen := make(map[report.Code]bool, len(codes))
	for _, code := range codes {
		if seen[code] {
			t.Errorf("code %s is defined twice", code)
		}
		seen[code] = true
		want := "GOM51"
		if shardCodes[code] {
			want = "GOM78"
		}
		if !strings.HasPrefix(string(code), want) || len(code) != len("GOM5101") {
			t.Errorf("code %s is outside the %sxx block", code, want)
		}
	}
	for code := range shardCodes {
		if !seen[code] {
			t.Errorf("the sharding code %s is not listed by Codes()", code)
		}
	}
	if !slices.IsSortedFunc(codes, func(x, y report.Code) int { return strings.Compare(string(x), string(y)) }) {
		t.Errorf("Codes() is not in numeric order: %v", codes)
	}
	for _, schemaCode := range schemas.Codes() {
		if seen[report.Code(schemaCode)] {
			t.Errorf("code %s is claimed by both internal/report and internal/schemas", schemaCode)
		}
	}
}

// TestErrorRendersOneLine checks the shape every renderer in the tool expects.
func TestErrorRendersOneLine(t *testing.T) {
	t.Parallel()

	err := &report.Error{Code: report.CodeHistoryWrite, Message: "the report could not be moved into place"}
	want := "GOM5132: the report could not be moved into place"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	cause := errors.New("access is denied")
	wrapped := &report.Error{Code: report.CodeHistoryWrite, Message: "the report could not be written", Err: cause}
	if got := wrapped.Error(); got != "GOM5132: the report could not be written: access is denied" {
		t.Errorf("Error() with a cause = %q", got)
	}
	if !errors.Is(wrapped, cause) {
		t.Error("the cause is not reachable through errors.Is")
	}
	if strings.Contains(wrapped.Error(), "\n") {
		t.Error("the rendered error spans more than one line")
	}
}

// TestCodeOfForeignError proves the accessor says nothing about an error that
// did not come from here, rather than guessing.
func TestCodeOfForeignError(t *testing.T) {
	t.Parallel()

	if got := report.CodeOf(errors.New("something else")); got != "" {
		t.Errorf("CodeOf(a foreign error) = %q, want the empty code", got)
	}
	if got := report.CodeOf(nil); got != "" {
		t.Errorf("CodeOf(nil) = %q, want the empty code", got)
	}
	wrapped := fmt.Errorf("while writing: %w", &report.Error{Code: report.CodeCacheUnavailable})
	if got := report.CodeOf(wrapped); got != report.CodeCacheUnavailable {
		t.Errorf("CodeOf(a wrapped error) = %q, want %q", got, report.CodeCacheUnavailable)
	}
}
