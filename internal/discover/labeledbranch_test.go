// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// A labelled branch says "leave *that* construct". Dropping the label says
// "leave the nearest one", which is a different program wherever the two
// differ -- and the whole difficulty of the family is that wherever they do not
// differ, the mutant is equivalent token for token and no test could ever kill
// it. The gate is structural, and the two rules need it for different shapes,
// which is why they are two rules.

// TestABreakThatLeavesAnOuterLoopLosesItsLabel is the shape the family exists
// for: two nested loops, and a `break` that means the outer one.
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

// TestAContinueThatSkipsAnOuterLoopLosesItsLabel is the same for the other
// rule.
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

// TestALabelOnTheNearestConstructProducesNothing is the refusal, and it is
// structural rather than statistical: the label names the construct the bare
// form would bind to anyway, so the two statements are the same program.
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

// TestASwitchInALabelledLoopSeparatesTheTwoRules is the one case that makes
// this a family of two rules rather than one.
//
// A `switch` is breakable and not continuable. So inside a `switch` inside a
// labelled `for`, `break L` is a real mutant -- the bare form leaves the switch
// and the labelled one leaves the loop -- while `continue L` at the same
// position is equivalent, because `continue` was never going to bind to the
// switch. One gate reading "does the label name the nearest enclosing
// construct" would have to know which constructs count, and this is the test
// that says it does.
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

// TestASelectInALabelledLoopIsTheSameShape pins the other breakable-only
// construct, because "breakable" is three constructs and a gate that knew two
// of them would pass every test above.
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

// TestATypeSwitchInALabelledLoopIsTheSameShape is the third and last of them.
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

// TestAGotoIsRecordedRatherThanMutated is the reserved reason becoming a fact.
//
// `label-or-goto` has been in the run report's enumeration since v1 with
// nothing emitting it, which is a string a user could meet in a document and
// find nothing about. It now names the one statement this family reaches and
// declines.
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

// TestAFallthroughIsNeitherMutatedNorRecorded is the deliberate silence beside
// it, and the two are different for a reason worth keeping straight.
//
// A `goto` is mutable in principle and declined with an argument. A
// `fallthrough` is not a site at all: it has to be the final statement of a
// case clause, so no guard form can wrap it, and there is no edit to decline.
// Recording a skip for it would put a row in `list --explain` that means
// "go-mutants declined to think about this" among rows that mean "go-mutants
// declined to mutate this".
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

// TestABareBranchOffersNothing is the absence at the other end: there is no
// label to drop.
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

// TestTheLabeledBranchFamilyIsOnlyInTheAllProfile pins the tier.
//
// `all` rather than `strong` for the same reason `statement-deletion` is there:
// it is an edit that removes something rather than changing it, and the
// survivors it produces are the ones hardest to argue about. The structural
// gate removes the equivalent ones it can prove, and what is left is still a
// family a default run should not carry.
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

// TestDroppingALabelKeepsTheLabelUsed is the trap that would otherwise make
// this family impossible, written down as a test rather than as a comment.
//
// An unused label is a compile error in Go. If the guard replaced the statement
// outright, a mutant that dropped a label would remove its only reference and
// the whole tree would stop building -- not for the mutant, for everybody, since
// the instrumented tree holds every mutant at once. Form S keeps the original
// bytes in its `else` arm, so the reference survives regardless.
//
// What is checked here is the premise: the rewrite site is the whole statement
// and the form is S. A form that replaced only the label token would put the
// label in the mutated copy and nowhere else.
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
