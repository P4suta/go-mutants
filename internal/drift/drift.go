// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package drift identifies snapshot changes that mutation instrumentation did
// not make. It is shared by CLI runs and reusable public sessions.
package drift

import (
	"strings"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/snapshot"
)

// Unexpected returns sorted, human-readable changes not explained by the
// guarded source files or generated runtime of instrumented.
//
// It is the rendering of [UnexpectedDrifts], and it stays because one caller
// wants exactly this: internal/engine prints the lines and has no use for the
// digests behind them.
func Unexpected(snap *snapshot.Snapshot, instrumented instrument.Result, generated ...string) ([]string, error) {
	drifts, err := UnexpectedDrifts(snap, instrumented, generated...)
	if err != nil {
		return nil, err
	}
	unexpected := make([]string, 0, len(drifts))
	for _, change := range drifts {
		unexpected = append(unexpected, change.Kind.String()+" "+change.RelPath)
	}
	return unexpected, nil
}

// UnexpectedDrifts returns the sorted changes not explained by the guarded
// source files or generated runtime of instrumented.
//
// It carries the drifts themselves rather than a line each, because a public
// caller has to be able to *act* on them — which path, which kind, and the
// digests on both sides — and reparsing a sentence to find out is how a
// consumer ends up depending on the wording of one.
func UnexpectedDrifts(
	snap *snapshot.Snapshot,
	instrumented instrument.Result,
	generated ...string,
) ([]snapshot.Drift, error) {
	drifts, err := snap.Redigest()
	if err != nil {
		return nil, err
	}
	guarded := make(map[string]bool, len(instrumented.FilesInstrumented))
	for _, path := range instrumented.FilesInstrumented {
		guarded[path] = true
	}
	// The generated directories are given as well as read off the pass, because
	// a workspace has more than one: it is instrumented a module at a time,
	// each module's files importing a runtime its own module declares, so a
	// gate that knew only about the pass's own directory would report every
	// other module's runtime as a tree that changed under it. A run over one
	// module names it once either way.
	prefixes := make([]string, 0, len(generated)+1)
	if instrumented.RuntimeDir != "" {
		prefixes = append(prefixes, instrumented.RuntimeDir+"/")
	}
	for _, dir := range generated {
		prefixes = append(prefixes, dir+"/")
	}

	unexpected := make([]snapshot.Drift, 0, len(drifts))
	for _, change := range drifts {
		switch {
		case change.Kind == snapshot.DriftChanged && guarded[change.RelPath]:
		case change.Kind == snapshot.DriftAdded && underAny(change.RelPath, prefixes):
		default:
			unexpected = append(unexpected, change)
		}
	}
	return unexpected, nil
}

// underAny reports whether a path sits under one of the generated runtime
// directories.
func underAny(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
