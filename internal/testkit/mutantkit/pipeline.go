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
)

// Discover finds every mutation candidate in a snapshot.
//
// The environment is left to discovery's own default rather than composed here.
// Discovery loads packages through go/packages, which runs `go list` itself, and
// the four suites that wrote this call out all passed nothing: the snapshot is
// already a copy, and what the loader needs — a module cache, a toolchain — is
// exactly what the composed environment pins rather than replaces.
func Discover(t testing.TB, tc gocmd.Toolchain, snap *snapshot.Snapshot) discover.Result {
	t.Helper()
	found, err := discover.Discover(t.Context(), discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    tc,
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
	found := Discover(t, tc, snap)
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
