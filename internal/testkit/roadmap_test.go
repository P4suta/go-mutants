// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	roadmapDoc      = "docs/roadmap.md"
	limitationsDoc  = "docs/limitations.md"
	runReportSchema = "schema/run-report-v1.schema.json"
	discoverReasons = "internal/discover/discover.go"
	reservedHeading = "## Reserved and unemitted"
)

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
		case onLimitations:
			t.Errorf("%s names %q and %s does not;\n"+
				"\ta boundary that is a fact about Go still belongs on the roadmap, as the row\n"+
				"\tthat says it is not one -- otherwise a reader looking for it there finds nothing",
				limitationsDoc, reason, roadmapDoc)
		}
	}
}

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

var roadmapCodeCount = regexp.MustCompile(`(\d+) constants across (\d+) packages`)

func TestTheRoadmapCountsTheDiagnosticCodesThisModuleDeclares(t *testing.T) {
	t.Parallel()

	root := Root(t)
	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(roadmapDoc)))
	if err != nil {
		t.Fatalf("reading %s: %v", roadmapDoc, err)
	}
	stated := roadmapCodeCount.FindStringSubmatch(string(source))
	if stated == nil {
		t.Fatalf("%s no longer says how many constants the job covers, in the words %q;\n"+
			"\tthe row is what this test is about, so a rewrite that drops the count\n"+
			"\tneeds this test rewritten with it", roadmapDoc, roadmapCodeCount)
	}

	found := declaredCodes(t, root)
	declaring := map[string]bool{}
	for _, code := range found {
		declaring[code.Package] = true
	}

	if got, want := stated[1], strconv.Itoa(len(found)); got != want {
		t.Errorf("%s says %s constants and this module declares %s", roadmapDoc, got, want)
	}
	if got, want := stated[2], strconv.Itoa(len(declaring)); got != want {
		t.Errorf("%s says %s packages declare them and %s do", roadmapDoc, got, want)
	}
}
