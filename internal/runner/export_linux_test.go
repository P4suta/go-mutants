// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import "sync"

// resetProcfsProbe forgets the cached answer to "can this machine read /proc".
//
// The probe is a sync.OnceValue in production because the answer cannot change
// under a running process, which is exactly what makes it untestable without a
// seam: a test that moved [procRoot] would be asking a question that had
// already been answered. This is compiled only under `go test`.
func resetProcfsProbe() {
	procfsReadable = sync.OnceValue(procfsProbe)
}
