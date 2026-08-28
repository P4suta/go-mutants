// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testflag_test

import (
	"testing"

	"github.com/P4suta/go-mutants/internal/testflag"
)

func TestMatchUsesTheStandardFlagSpellingsOnly(t *testing.T) {
	for _, argument := range []string{
		"-test.timeout",
		"-test.timeout=1s",
		"--test.timeout",
		"--test.timeout=1s",
	} {
		if !testflag.Match(argument, "test.timeout") {
			t.Errorf("Match(%q) = false, want true", argument)
		}
	}
	for _, argument := range []string{
		"test.timeout=1s",
		"---test.timeout=1s",
		"-test.timeout-extra=1s",
		"-test.timeouts=1s",
	} {
		if testflag.Match(argument, "test.timeout") {
			t.Errorf("Match(%q) = true, want false", argument)
		}
	}
}
