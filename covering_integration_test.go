// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package gomutants_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// TestSessionCoveringTestsNamesTheTestsBehindEachMutant is CoveringTests against
// the killable fixture: clamp.go is exercised only by TestClamp, and untested.go
// by nothing. So the mutant in clamp.go is reported covered by exactly TestClamp,
// and the mutant in untested.go is absent — no test reaches it, which is the
// honest answer rather than an empty list of covering tests.
func TestSessionCoveringTestsNamesTheTestsBehindEachMutant(t *testing.T) {
	t.Parallel()

	root := copyFixture(t, "killable")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := workspace.Close(); closeErr != nil {
			t.Errorf("closing workspace: %v", closeErr)
		}
	})

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Operators:     []string{"comparison"},
		MutantTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("preparing session: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("closing session: %v", closeErr)
		}
	})

	const module = "fixture.example/killable"
	clamp := mutantkit.APIMutantAt(t, session.Catalog(), "clamp.go", "lt-to-le")
	untested := mutantkit.APIMutantAt(t, session.Catalog(), "untested.go", "neq-to-eq")

	covering, err := session.CoveringTests(t.Context())
	if err != nil {
		t.Fatalf("CoveringTests: %v", err)
	}

	if got, want := covering[clamp.ID], []gomutants.TestRef{{Package: module, Name: "TestClamp"}}; !slices.Equal(got, want) {
		t.Errorf("clamp mutant is covered by %v, want %v", got, want)
	}
	if got, ok := covering[untested.ID]; ok {
		t.Errorf("untested mutant is covered by %v, want it absent: no test reaches its line", got)
	}

	// Every covering entry names a real, accepted mutant and a test of the one
	// module: the mapping does not invent ids or tests.
	accepted := make(map[string]bool)
	for _, m := range session.Catalog().Mutants {
		if m.Accepted {
			accepted[m.ID] = true
		}
	}
	for id, refs := range covering {
		if !accepted[id] {
			t.Errorf("covering names %s, which is not an accepted mutant", id)
		}
		if len(refs) == 0 {
			t.Errorf("mutant %s maps to an empty covering list, want it absent instead", id)
		}
		for _, ref := range refs {
			if ref.Package != module || ref.Name == "" {
				t.Errorf("mutant %s names test %+v, want a named test of %s", id, ref, module)
			}
		}
	}
}

// TestSessionCoveringTestsAfterCloseIsRefused: a closed session measures
// nothing, and says so with the sentinel every other closed-session call uses.
func TestSessionCoveringTestsAfterCloseIsRefused(t *testing.T) {
	t.Parallel()

	root := copyFixture(t, "killable")
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: t.TempDir(),
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Operators:     []string{"comparison"},
		MutantTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("preparing session: %v", err)
	}
	if closeErr := session.Close(); closeErr != nil {
		t.Fatalf("closing session: %v", closeErr)
	}
	if _, err := session.CoveringTests(t.Context()); !errors.Is(err, gomutants.ErrSessionClosed) {
		t.Fatalf("CoveringTests on a closed session = %v, want ErrSessionClosed", err)
	}
}
