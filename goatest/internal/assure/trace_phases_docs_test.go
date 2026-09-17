// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	phaseDocumentation = "../../docs/trace-v1.md"

	phaseSentenceOpening = "The phases are "
)

var phaseName = regexp.MustCompile("`([^`]+)`")

func TestEveryRunPhaseIsDocumented(t *testing.T) {
	t.Parallel()
	documented := documentedPhases(t)
	for _, phase := range runPhaseNames() {
		if !slices.Contains(documented, phase) {
			t.Errorf("phase %q is entered by a run and absent from %s", phase, phaseDocumentation)
		}
	}
	for _, phase := range documented {
		if !slices.Contains(runPhaseNames(), phase) {
			t.Errorf("%s names phase %q, which no run enters", phaseDocumentation, phase)
		}
	}
	if !slices.Equal(documented, runPhaseNames()) {
		t.Errorf("%s lists the phases as %q, and a run enters them as %q.\n\n"+
			"The order is part of what the sentence says: a run uses the phases as a\n"+
			"sequence, so a page that lists them in another order describes another run.",
			phaseDocumentation, documented, runPhaseNames())
	}
}

func TestThePhaseLedgerSeesANameTheSentenceDoesNotHold(t *testing.T) {
	t.Parallel()
	documented := documentedPhases(t)
	if len(documented) == 0 {
		t.Fatal("the sentence was read as empty, so the ledger would agree with anything")
	}
	if slices.Contains(documented, "a-phase-no-run-enters") {
		t.Fatal("the sentence holds the fixture name, so this test proves nothing")
	}
}

func documentedPhases(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(phaseDocumentation)
	if err != nil {
		t.Fatalf("read %s: %v", phaseDocumentation, err)
	}
	text := string(data)
	start := strings.Index(text, phaseSentenceOpening)
	if start < 0 {
		t.Fatalf("%s no longer holds %q", phaseDocumentation, phaseSentenceOpening)
	}
	rest := text[start+len(phaseSentenceOpening):]
	end := strings.Index(rest, ".")
	if end < 0 {
		t.Fatalf("%s: the phase sentence has no end", phaseDocumentation)
	}
	var documented []string
	for _, match := range phaseName.FindAllStringSubmatch(rest[:end], -1) {
		documented = append(documented, match[1])
	}
	return documented
}
