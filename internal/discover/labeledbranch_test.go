// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

func TestABreakThatLeavesAnOuterLoopLosesItsLabel(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Find(rows [][]int, want int) bool {
outer:
	for _, row := range rows {
		for _, v := range row {
			if v == want {
				break outer
			}
		}
	}
	return false
}
`)
	if !got.has("drop-break-label", "break outer", "break") {
		t.Errorf("scan found %v, want a drop-break-label candidate", got.rules())
	}
}

func TestAContinueThatSkipsAnOuterLoopLosesItsLabel(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Count(rows [][]int, skip int) int {
	total := 0
outer:
	for _, row := range rows {
		for _, v := range row {
			if v == skip {
				continue outer
			}
			total += v
		}
	}
	return total
}
`)
	if !got.has("drop-continue-label", "continue outer", "continue") {
		t.Errorf("scan found %v, want a drop-continue-label candidate", got.rules())
	}
}

func TestALabelOnTheNearestConstructProducesNothing(t *testing.T) {
	t.Parallel()

	for name, source := range map[string]string{
		"a break labelling its own loop": `package pkg

func Find(values []int, want int) bool {
only:
	for _, v := range values {
		if v == want {
			break only
		}
	}
	return false
}
`,
		"a continue labelling its own loop": `package pkg

func Count(values []int, skip int) int {
	total := 0
only:
	for _, v := range values {
		if v == skip {
			continue only
		}
		total += v
	}
	return total
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := scanSource(t, source)
			for _, rule := range got.rules() {
				if strings.HasPrefix(rule, "drop-") {
					t.Errorf("scan produced %v for a label on the nearest construct", got.rules())
				}
			}
		})
	}
}

func TestASwitchInALabelledLoopSeparatesTheTwoRules(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Scan(values []int) int {
	total := 0
loop:
	for _, v := range values {
		switch {
		case v < 0:
			break loop
		case v == 0:
			continue loop
		default:
			total += v
		}
	}
	return total
}
`)
	if !got.has("drop-break-label", "break loop", "break") {
		t.Errorf("scan found %v, want drop-break-label: a bare break leaves the switch", got.rules())
	}
	if got.has("drop-continue-label", "continue loop", "continue") {
		t.Errorf("scan produced %v: a switch is not continuable, so the label is redundant", got.rules())
	}
}

func TestASelectInALabelledLoopIsTheSameShape(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Drain(ch chan int, done chan struct{}) int {
	total := 0
loop:
	for {
		select {
		case v := <-ch:
			total += v
		case <-done:
			break loop
		}
	}
	return total
}
`)
	if !got.has("drop-break-label", "break loop", "break") {
		t.Errorf("scan found %v, want drop-break-label: a bare break leaves the select", got.rules())
	}
}

func TestATypeSwitchInALabelledLoopIsTheSameShape(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Count(values []any) int {
	total := 0
loop:
	for _, value := range values {
		switch v := value.(type) {
		case int:
			total += v
		case string:
			break loop
		}
	}
	return total
}
`)
	if !got.has("drop-break-label", "break loop", "break") {
		t.Errorf("scan found %v, want drop-break-label: a bare break leaves the type switch", got.rules())
	}
}

func TestAGotoIsRecordedRatherThanMutated(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Retry(attempts int) int {
	n := 0
again:
	n++
	if n < attempts {
		goto again
	}
	return n
}
`)
	if !got.hasSkip(SkipLabelOrGoto) {
		t.Errorf("scan recorded %v, want a %s site", got.skips(), SkipLabelOrGoto)
	}
	for _, rule := range got.rules() {
		if strings.HasPrefix(rule, "drop-") {
			t.Errorf("scan produced %v for a goto", got.rules())
		}
	}
}

func TestAFallthroughIsNeitherMutatedNorRecorded(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Level(n int) string {
	out := ""
	switch n {
	case 2:
		out += "high"
		fallthrough
	case 1:
		out += "low"
	}
	return out
}
`)
	if got.hasSkip(SkipLabelOrGoto) {
		t.Errorf("scan recorded %v for a fallthrough, which is not a site", got.skips())
	}
	for _, rule := range got.rules() {
		if strings.HasPrefix(rule, "drop-") {
			t.Errorf("scan produced %v for a fallthrough", got.rules())
		}
	}
}

func TestABareBranchOffersNothing(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func First(values []int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
		break
	}
	return 0
}
`)
	for _, rule := range got.rules() {
		if strings.HasPrefix(rule, "drop-") {
			t.Errorf("scan produced %v for a branch with no label", got.rules())
		}
	}
}

func TestTheLabeledBranchFamilyIsOnlyInTheAllProfile(t *testing.T) {
	t.Parallel()

	found := map[string]mutation.Rule{}
	for _, rule := range SupportedRules() {
		if rule.Family == mutation.FamilyLabeledBranch {
			found[rule.Name] = rule
		}
	}
	for _, name := range []string{"drop-break-label", "drop-continue-label"} {
		rule, ok := found[name]
		if !ok {
			t.Fatalf("SupportedRules() does not name %s", name)
		}
		if rule.Tier != mutation.TierAll {
			t.Errorf("%s is tier %q, want %q", name, rule.Tier, mutation.TierAll)
		}
	}
	if len(found) != 2 {
		t.Errorf("the family holds %d implemented rules, want exactly 2: %v", len(found), found)
	}
}

func TestDroppingALabelKeepsTheLabelUsed(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Find(rows [][]int, want int) bool {
outer:
	for _, row := range rows {
		for _, v := range row {
			if v == want {
				break outer
			}
		}
	}
	return false
}
`)
	found := false
	for _, candidate := range got.candidates {
		if candidate.Rule.Name != "drop-break-label" {
			continue
		}
		found = true
		if candidate.Guard.Form != GuardFormS {
			t.Errorf("drop-break-label uses %q, want %q: only the statement guard keeps the "+
				"original bytes, and the original bytes are what keep the label used",
				candidate.Guard.Form, GuardFormS)
		}
		if candidate.Guard.SiteSpan != candidate.Span {
			t.Errorf("the site %v is not the candidate's own span %v, so the label would be "+
				"replaced rather than the statement", candidate.Guard.SiteSpan, candidate.Span)
		}
	}
	if !found {
		t.Fatalf("scan found %v, want a drop-break-label candidate", got.rules())
	}
}
