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

func Discover(t testing.TB, tc gocmd.Toolchain, snap *snapshot.Snapshot) discover.Result {
	t.Helper()
	return DiscoverWith(t, tc, snap, testkit.Compose(t, testkit.Scratch(t)))
}

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

func Instrument(t testing.TB, tc gocmd.Toolchain, snap *snapshot.Snapshot) *mutation.Catalog {
	t.Helper()
	return InstrumentWith(t, tc, snap, testkit.Compose(t, testkit.Scratch(t)))
}

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

func CatalogLines(catalog *mutation.Catalog) []string {
	out := make([]string, 0, catalog.Len())
	for _, m := range catalog.Mutants() {
		out = append(out, fmt.Sprintf("%s %s %s -> %s", m.Path, m.Rule.Name, m.Original, m.Replacement))
	}
	return out
}

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
