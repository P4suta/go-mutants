// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package drift identifies snapshot changes instrumentation must not be blamed for.
package drift

import (
	"strings"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/snapshot"
)

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

func underAny(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
