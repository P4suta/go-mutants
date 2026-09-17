// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package exhaustive_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/P4suta/go-mutants/internal/analysis/exhaustive"
)

// TestTheCheckReadsEveryShapeASwitchOverAVocabularyTakes runs the analyzer
// against a fixture that holds all of them, and the `// want` comments in that
// fixture are the expectations.
//
// Every case is a claim the check would otherwise make silently: that grouped
// cases count, that a `default` does not excuse a missing word, that a type
// with one constant is a sentinel rather than a set, that a type switch is not
// this check's business, and that an exemption without a sentence is refused.
// A check that reported nothing at all would pass a test that only looked for
// the absence of noise.
func TestTheCheckReadsEveryShapeASwitchOverAVocabularyTakes(t *testing.T) {
	t.Parallel()

	analysistest.Run(t, analysistest.TestData(), exhaustive.Analyzer,
		"github.com/P4suta/go-mutants/vocabulary")
}
