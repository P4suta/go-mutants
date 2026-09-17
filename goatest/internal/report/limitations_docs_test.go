// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	limitationsDocumentation = "../../docs/limitations.md"

	limitationTableHeading = "| Code | What the run is saying |"
)

var limitationTableRow = regexp.MustCompile("^\\| `([a-z0-9-]+)` \\|")

func documentedLimitationCodes(t *testing.T) []string {
	t.Helper()
	page, err := os.ReadFile(limitationsDocumentation)
	if err != nil {
		t.Fatalf("read %s: %v", limitationsDocumentation, err)
	}
	lines := strings.Split(string(page), "\n")
	heading := slices.Index(lines, limitationTableHeading)
	if heading < 0 {
		t.Fatalf("%s holds no table headed %q", limitationsDocumentation, limitationTableHeading)
	}
	var codes []string
	for _, line := range lines[heading+1:] {
		if !strings.HasPrefix(line, "|") {
			break
		}
		match := limitationTableRow.FindStringSubmatch(line)
		if match != nil {
			codes = append(codes, match[1])
		}
	}
	if len(codes) == 0 {
		t.Fatalf("%s holds the table heading and no rows under it", limitationsDocumentation)
	}
	return codes
}

func TestEveryLimitationCodeIsDocumented(t *testing.T) {
	t.Parallel()
	documented := documentedLimitationCodes(t)
	for _, code := range LimitationCodes() {
		if !slices.Contains(documented, code) {
			t.Errorf("limitation %s is raised by this module and absent from %s", code, limitationsDocumentation)
		}
	}
}

func TestEveryDocumentedLimitationCodeExists(t *testing.T) {
	t.Parallel()
	for _, code := range documentedLimitationCodes(t) {
		if !KnownLimitationCode(code) {
			t.Errorf("%s documents limitation %s, which this module never raises", limitationsDocumentation, code)
		}
	}
}

func TestTheLimitationLedgerSeesADocumentedCodeThatDoesNotExist(t *testing.T) {
	t.Parallel()
	forged := "| `a-caveat-nothing-raises` | invented by a test |"
	if limitationTableRow.FindStringSubmatch(forged) == nil {
		t.Fatal("the row pattern does not match a row of the shape the page uses")
	}
	if KnownLimitationCode("a-caveat-nothing-raises") {
		t.Fatal("a code nothing raises is reported as known")
	}
}

func TestTheLimitationCodesAreSorted(t *testing.T) {
	t.Parallel()
	codes := LimitationCodes()
	if !slices.IsSorted(codes) {
		t.Errorf("LimitationCodes() = %v, which is not sorted", codes)
	}
	documented := documentedLimitationCodes(t)
	if !slices.IsSorted(documented) {
		t.Errorf("%s lists %v, which is not sorted", limitationsDocumentation, documented)
	}
}
