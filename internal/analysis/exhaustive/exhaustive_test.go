// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package exhaustive_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/P4suta/go-mutants/internal/analysis/exhaustive"
)

func TestTheCheckReadsEveryShapeASwitchOverAVocabularyTakes(t *testing.T) {
	t.Parallel()

	analysistest.Run(t, analysistest.TestData(), exhaustive.Analyzer,
		"github.com/P4suta/go-mutants/vocabulary")
}
