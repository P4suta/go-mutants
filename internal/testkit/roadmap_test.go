// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	// roadmapDoc is the page that says what is work and what is not.
	roadmapDoc = "docs/roadmap.md"
	// limitationsDoc is where a boundary goes when it turns out to be a fact
	// about Go rather than about go-mutants.
	limitationsDoc = "docs/limitations.md"
	// runReportSchema declares the reasons a skip may carry.
	runReportSchema = "schema/run-report-v1.schema.json"
	// discoverReasons is where the Go constants for those reasons live.
	discoverReasons = "internal/discover/discover.go"
	// reservedHeading opens the roadmap's index of reserved reasons.
	//
	// The section rather than the page, because the page mentions `struct-tag`
	// in its own opening paragraph as the example of a boundary that moved --
	// and a rule satisfied by any mention anywhere is one a passing reference
	// silently satisfies.
	reservedHeading = "## Reserved and unemitted"
)

// TestEveryReservedSkipReasonIsAccountedFor keeps the schema's superset honest.
//
// internal/mutation/outcome.go argues for that superset: the run-report
// enumeration lists reasons no build emits "so that landing them is a code
// change and not a schema change". The argument is good and the consequence is
// a set nothing was watching -- a reason could be reserved for years with no
// page saying whether it is work somebody will do or a thing that cannot be
// done.
//
// The rule is that **the roadmap names every reserved reason**, because it is
// the index of them, and that a reason which is a boundary rather than work
// also appears on the limitations page -- where the roadmap's row for it is the
// one that says so. A name on neither page is unexplained. A name on
// limitations and not on the roadmap is one a reader looking for it in the
// index cannot find.
//
// This test is also how a reserved name is retired: land it, and the name
// leaves the reserved set, and the row that described it fails here until it is
// deleted. A green deletion prompt rather than a page that quietly describes
// something already done.
func TestEveryReservedSkipReasonIsAccountedFor(t *testing.T) {
	t.Parallel()

	root := Root(t)
	reserved := reservedReasons(t, root)
	if len(reserved) == 0 {
		t.Fatalf("nothing is reserved any more;\n"+
			"\tdelete this test and the `Reserved and unemitted` section of %s --\n"+
			"\tthe superset has become the set, which is the outcome it was designed for", roadmapDoc)
	}

	roadmap, ok := sectionOf(readDoc(t, filepath.Join(root, filepath.FromSlash(roadmapDoc))), reservedHeading)
	if !ok {
		t.Fatalf("%s has no `%s` section", roadmapDoc, reservedHeading)
	}
	limitations := readDoc(t, filepath.Join(root, filepath.FromSlash(limitationsDoc)))
	for _, reason := range reserved {
		quoted := "`" + reason + "`"
		onRoadmap := strings.Contains(roadmap, quoted)
		onLimitations := strings.Contains(limitations, quoted)
		switch {
		case !onRoadmap && !onLimitations:
			t.Errorf("%s reserves %q and neither %s nor %s says anything about it;\n"+
				"\ta name reserved and unexplained is one nobody can tell work from impossibility about",
				runReportSchema, reason, roadmapDoc, limitationsDoc)
		case onRoadmap:
			// Named in the index. Whether the row says "work" or "not work" is
			// prose a reader judges; what is checked is that the row is there.
		case onLimitations:
			t.Errorf("%s names %q and %s does not;\n"+
				"\ta boundary that is a fact about Go still belongs on the roadmap, as the row\n"+
				"\tthat says it is not one -- otherwise a reader looking for it there finds nothing",
				limitationsDoc, reason, roadmapDoc)
		}
	}
}

// reservedReasons is the skip reasons the run-report schema allows and no
// package declares, in name order.
func reservedReasons(t *testing.T, root string) []string {
	t.Helper()

	allowed := schemaSkipReasons(t, root)
	if len(allowed) == 0 {
		t.Fatalf("%s enumerates no skip reasons; the reader has stopped seeing them", runReportSchema)
	}
	declared := declaredSkipReasons(t, root)
	if len(declared) == 0 {
		t.Fatalf("%s declares no skip reasons; the scan has stopped seeing them", discoverReasons)
	}

	var reserved []string
	for _, reason := range allowed {
		if !slices.Contains(declared, reason) {
			reserved = append(reserved, reason)
		}
	}
	slices.Sort(reserved)
	return reserved
}

// schemaSkipReasons reads the `reason` enumeration out of the run-report
// schema.
//
// encoding/json rather than a schema library, because this package may import
// nothing from this module and the shape wanted is one enumeration.
func schemaSkipReasons(t *testing.T, root string) []string {
	t.Helper()

	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(runReportSchema)))
	if err != nil {
		t.Fatalf("reading %s: %v", runReportSchema, err)
	}
	var document struct {
		Defs struct {
			Skip struct {
				Properties struct {
					Reason struct {
						Enum []string `json:"enum"`
					} `json:"reason"`
				} `json:"properties"`
			} `json:"skip"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(source, &document); err != nil {
		t.Fatalf("decoding %s: %v", runReportSchema, err)
	}
	return document.Defs.Skip.Properties.Reason.Enum
}

// declaredSkipReasons is the value of every exported SkipReason constant.
func declaredSkipReasons(t *testing.T, root string) []string {
	t.Helper()

	var reasons []string
	for _, constant := range exportedStringConstants(t, filepath.Join(root, filepath.FromSlash(discoverReasons))) {
		if strings.HasPrefix(constant.Name, "Skip") {
			reasons = append(reasons, constant.Value)
		}
	}
	return reasons
}
