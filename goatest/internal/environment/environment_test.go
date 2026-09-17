// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package environment_test

import (
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/environment"
)

func TestProviderSelectsLaunchAndExplicitNamesWithoutSecrets(t *testing.T) {
	input := []string{"Path=C:/tools", "TEMP=C:/temp", "TOKEN=allowed", "SECRET=hidden", "token=last"}
	got := environment.Provider(input, []string{"TOKEN"})
	want := []string{"Path=C:/tools", "TEMP=C:/temp", "token=last"}
	if !slices.Equal(got, want) {
		t.Fatalf("Provider = %v, want %v", got, want)
	}
	got[0] = "MUTATED=yes"
	if input[0] != "Path=C:/tools" {
		t.Fatal("Provider aliases caller input")
	}
}

func TestSelectDistinguishesNilAndExplicitEmptyInputs(t *testing.T) {
	t.Setenv("GOATEST_ENVIRONMENT_SELECTION", "ready")
	if got := environment.Select(nil, []string{"GOATEST_ENVIRONMENT_SELECTION"}); !slices.Equal(got, []string{"GOATEST_ENVIRONMENT_SELECTION=ready"}) {
		t.Fatalf("nil selection = %v", got)
	}
	if got := environment.Select([]string{}, []string{"GOATEST_ENVIRONMENT_SELECTION"}); got == nil || len(got) != 0 {
		t.Fatalf("explicit empty selection = %#v", got)
	}
}

// TestSelectSkipsAnEntryThatIsNotAnAssignment pins the two shapes the loop
// steps over, which nothing else here names.
//
// An environment slice is `KEY=VALUE` by convention and not by construction: it
// arrives from `os.Environ`, from a configuration file, or from a caller that
// built it by hand, and any of those can hand over a bare word or an entry
// whose name is empty. Both are skipped, and both were skipped without a test —
// so the condition that skips them could be inverted, or either half of it
// dropped, and every test here still passed.
//
// The assertion is that the *selected* names come through untouched beside the
// malformed ones, because "skip" has to mean the entry is ignored rather than
// that the loop stops.
func TestSelectSkipsAnEntryThatIsNotAnAssignment(t *testing.T) {
	t.Parallel()

	got := environment.Select([]string{"BARE", "=orphaned", "KEEP=value"}, []string{"KEEP", "BARE", ""})
	if !slices.Equal(got, []string{"KEEP=value"}) {
		t.Errorf("Select = %v, want only the entry that is an assignment with a name", got)
	}
}
