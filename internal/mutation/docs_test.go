// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
)

const (
	// operatorsDoc is the page this file holds to the registry.
	operatorsDoc = "docs/operators.md"
	// catalogueHeading opens the table of families and their rules.
	catalogueHeading = "## Catalogue"
)

// TestTheOperatorsPageNamesEveryRuleAndEveryFamily keeps the catalogue table
// equal to the registry, in both directions.
//
// The registry has pinned its own counts since it was written -- rule_test.go
// compares them with a golden list -- but nothing read the page. So the table a
// user reads to find out what go-mutants does could name a rule that does not
// exist, or omit one that does, and every test would pass. The page itself says
// at the foot of the table that "the registry has settled it in favour of the
// table", which is a claim about two things nothing compared.
func TestTheOperatorsPageNamesEveryRuleAndEveryFamily(t *testing.T) {
	t.Parallel()

	registry := mutation.CanonicalRegistry()
	rows := catalogueRows(t)
	if len(rows) == 0 {
		t.Fatalf("%s has no catalogue rows; the parser has stopped seeing the table", operatorsDoc)
	}

	var families, rules []string
	for _, row := range rows {
		families = append(families, row.family)
		rules = append(rules, row.rules...)
	}

	var wantFamilies []string
	for _, family := range registry.Families() {
		wantFamilies = append(wantFamilies, string(family))
	}
	var wantRules []string
	for _, rule := range registry.Rules() {
		wantRules = append(wantRules, rule.Name)
	}

	slices.Sort(families)
	slices.Sort(rules)
	slices.Sort(wantFamilies)
	slices.Sort(wantRules)
	if !slices.Equal(families, wantFamilies) {
		t.Errorf("%s names families %v and the registry holds %v", operatorsDoc, families, wantFamilies)
	}
	if !slices.Equal(rules, wantRules) {
		t.Errorf("%s names rules %v and the registry holds %v", operatorsDoc, rules, wantRules)
	}
}

// TestTheOperatorsPageCountsWhatItLists keeps the sentence under the table
// equal to the table above it and to the registry beside it.
//
// The page spells the two numbers out in prose -- "That is 11 families and 42
// enumerated rules" -- and a spelled number is the first thing to go stale when
// a row is added, because adding the row feels like the whole change.
func TestTheOperatorsPageCountsWhatItLists(t *testing.T) {
	t.Parallel()

	registry := mutation.CanonicalRegistry()
	body := operatorsPage(t)
	for _, count := range []struct {
		noun string
		want int
	}{
		{"families", len(registry.Families())},
		{"enumerated rules", registry.Len()},
	} {
		if !strings.Contains(body, strconv.Itoa(count.want)+" "+count.noun) {
			t.Errorf("%s does not say %q, which is what the registry holds", operatorsDoc, strconv.Itoa(count.want)+" "+count.noun)
		}
	}
	if mutation.CanonicalFamilyCount != len(registry.Families()) {
		t.Errorf("CanonicalFamilyCount is %d and the registry holds %d families",
			mutation.CanonicalFamilyCount, len(registry.Families()))
	}
	if mutation.CanonicalRuleCount != registry.Len() {
		t.Errorf("CanonicalRuleCount is %d and the registry holds %d rules",
			mutation.CanonicalRuleCount, registry.Len())
	}
}

// TestEveryFamilyOnThePageIsInTheTierItSays keeps the third column honest.
func TestEveryFamilyOnThePageIsInTheTierItSays(t *testing.T) {
	t.Parallel()

	registry := mutation.CanonicalRegistry()
	for _, row := range catalogueRows(t) {
		rules := registry.FamilyRules(mutation.Family(row.family))
		if len(rules) == 0 {
			continue
		}
		if got := rules[0].Tier.String(); got != row.tier {
			t.Errorf("%s puts %s in the %s tier and the registry has it in %s",
				operatorsDoc, row.family, row.tier, got)
		}
	}
}

// A catalogueRow is one line of the family table.
type catalogueRow struct {
	family string
	rules  []string
	tier   string
}

// catalogueRows reads the family table: a family, its rules, and its tier, each
// taken from the backticked tokens of a cell.
func catalogueRows(t *testing.T) []catalogueRow {
	t.Helper()

	var rows []catalogueRow
	inTable := false
	for _, line := range strings.Split(operatorsPage(t), "\n") {
		if strings.HasPrefix(line, "## ") {
			inTable = strings.TrimRight(line, " ") == catalogueHeading
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !inTable || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		if len(cells) < 3 {
			continue
		}
		family := backtickedIn(cells[0])
		if len(family) != 1 {
			continue
		}
		rows = append(rows, catalogueRow{
			family: family[0],
			rules:  backtickedIn(cells[1]),
			tier:   strings.TrimSpace(cells[2]),
		})
	}
	return rows
}

// backtickedIn is every `quoted` token of one cell, in order.
func backtickedIn(cell string) []string {
	var out []string
	for {
		open := strings.Index(cell, "`")
		if open < 0 {
			return out
		}
		rest := cell[open+1:]
		end := strings.Index(rest, "`")
		if end < 0 {
			return out
		}
		if token := rest[:end]; token != "" {
			out = append(out, token)
		}
		cell = rest[end+1:]
	}
}

// operatorsPage reads the page once per call, which is cheap enough that a
// cache would be a second thing to be wrong about.
func operatorsPage(t *testing.T) string {
	t.Helper()

	source, err := os.ReadFile(filepath.Join(testkit.Root(t), filepath.FromSlash(operatorsDoc)))
	if err != nil {
		t.Fatalf("reading %s: %v", operatorsDoc, err)
	}
	return string(source)
}
