// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"fmt"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// Discover finds every mutation candidate in a snapshot.
//
// It composes a hermetic environment of its own, and that is not a convenience:
// go/packages asks the go command for export data, so a discovery pass
// *compiles* the module and everything below it. That makes discovery the
// heaviest writer of build cache entries in every suite that drives it — heavier
// than the builds those suites are usually about — and each entry is keyed on a
// snapshot path that exists for a single run. Left on the process's own
// environment it filled the developer's cache, which is the failure the harness
// exists to prevent, so there is no form of this helper that does.
//
// A caller that already has an environment — because its later steps have to run
// under the same one, which is every integration suite here — passes it to
// [DiscoverWith] instead. Two composed environments differ only in their scratch
// directory, so the difference is not correctness but a temporary directory
// nobody needed.
func Discover(t testing.TB, tc gocmd.Toolchain, snap *snapshot.Snapshot) discover.Result {
	t.Helper()
	return DiscoverWith(t, tc, snap, testkit.Compose(t, t.TempDir()))
}

// DiscoverWith finds every mutation candidate in a snapshot, with the loader
// under the environment the caller is running its other steps with.
//
// GOWORK=off and the located toolchain's directory on PATH are forced by
// discovery itself either way, so what a composed environment adds is the build
// cache, the temporary directory, the private home and the stripped activation.
// An empty env is not "inherit": it is the go command with no PATH and no HOME,
// so a caller with nothing to share wants [Discover].
func DiscoverWith(t testing.TB, tc gocmd.Toolchain, snap *snapshot.Snapshot, env []string) discover.Result {
	t.Helper()
	found, err := discover.Discover(t.Context(), discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    tc,
		Env:          env,
	})
	if err != nil {
		t.Fatalf("discovering the candidates in %s: %v", snap.Root, err)
		return discover.Result{}
	}
	logInputs(t, fmt.Sprintf("module=%s candidates=%d skips=%d",
		found.ModulePath, len(found.Candidates), len(found.Skips)))
	return found
}

// Catalog identifies and deduplicates what discovery found, and assigns the
// dense runtime indices the generated guards read.
func Catalog(t testing.TB, found discover.Result) *mutation.Catalog {
	t.Helper()
	catalog, err := discover.BuildCatalog(found)
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
		return nil
	}
	logInputs(t, fmt.Sprintf("catalogued=%d duplicates=%d digest=%s",
		catalog.Len(), len(catalog.Duplicates()), catalog.Digest()))
	return catalog
}

// Hints indexes the rewrite sites discovery chose, one per catalogued mutant.
//
// They travel with the catalogue from the pass that had the type checker to the
// one that rewrites bytes, because internal/instrument is a byte rewriter and
// cannot choose a rewrite form for itself — a catalogued mutant with no hint is
// refused there rather than guessed at.
func Hints(t testing.TB, found discover.Result) instrument.Hints {
	t.Helper()
	hints, err := instrument.HintsOf(found.Candidates)
	if err != nil {
		t.Fatalf("indexing the guard hints: %v", err)
		return nil
	}
	logInputs(t, fmt.Sprintf("hints=%d", len(hints)))
	return hints
}

// Instrument runs the whole sequence — discover, catalogue, hint, rewrite — and
// returns the catalogue every later step indexes mutants by.
//
// It is one call because it is one thing: the four steps have no meaningful
// intermediate state a test wants to inspect, they fail for reasons a test can
// do nothing about, and every suite that needs an instrumented tree needs all
// four. A test that does want the discovery result — because its subject is a
// skip, a rejection or a coordinate — calls the steps itself.
func Instrument(t testing.TB, tc gocmd.Toolchain, snap *snapshot.Snapshot) *mutation.Catalog {
	t.Helper()
	return InstrumentWith(t, tc, snap, testkit.Compose(t, t.TempDir()))
}

// InstrumentWith is [Instrument] with the discovery pass under the environment
// the caller's own steps run with, for the reason [DiscoverWith] gives.
func InstrumentWith(t testing.TB, tc gocmd.Toolchain, snap *snapshot.Snapshot, env []string) *mutation.Catalog {
	t.Helper()
	found := DiscoverWith(t, tc, snap, env)
	catalog := Catalog(t, found)
	if _, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: snap.Root,
		ModulePath:   found.ModulePath,
		Catalog:      catalog,
		Hints:        Hints(t, found),
	}); err != nil {
		t.Fatalf("instrumenting the snapshot at %s: %v", snap.Root, err)
		return nil
	}
	logInputs(t, fmt.Sprintf("instrumented=%s mutants=%d", snap.Root, catalog.Len()))
	return catalog
}

// CatalogLines renders a catalogue in the terms a fixture is written in, one
// mutant per line, so that a failure reads as a list of candidates rather than
// of digests.
func CatalogLines(catalog *mutation.Catalog) []string {
	out := make([]string, 0, catalog.Len())
	for _, m := range catalog.Mutants() {
		out = append(out, fmt.Sprintf("%s %s %s -> %s", m.Path, m.Rule.Name, m.Original, m.Replacement))
	}
	return out
}

// Describe renders a list of mutant IDs in those same terms.
//
// The ids in a run's own output — an activation list, a cache decision, an
// expectation row — are digests, and a failure that printed them would be a
// failure nobody can read without a second lookup. An id the catalogue does not
// hold is said so rather than dropped, because that is a real answer: it is what
// a stale expectation looks like.
func Describe(catalog *mutation.Catalog, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		m, ok := catalog.ByID(id)
		if !ok {
			out = append(out, id+" (not in the catalogue)")
			continue
		}
		out = append(out, fmt.Sprintf("[%d] %s %s %s -> %s",
			m.Index, m.Path, m.Rule.Name, m.Original, m.Replacement))
	}
	return out
}
