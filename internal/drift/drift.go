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
func Unexpected(snap *snapshot.Snapshot, instrumented instrument.Result) ([]string, error) {
	drifts, err := snap.Redigest()
	if err != nil {
		return nil, err
	}
	guarded := make(map[string]bool, len(instrumented.FilesInstrumented))
	for _, path := range instrumented.FilesInstrumented {
		guarded[path] = true
	}
	runtimePrefix := instrumented.RuntimeDir + "/"

	unexpected := make([]string, 0)
	for _, change := range drifts {
		switch {
		case change.Kind == snapshot.DriftChanged && guarded[change.RelPath]:
		case change.Kind == snapshot.DriftAdded && strings.HasPrefix(change.RelPath, runtimePrefix):
		default:
			unexpected = append(unexpected, change.Kind.String()+" "+change.RelPath)
		}
	}
	return unexpected, nil
}
