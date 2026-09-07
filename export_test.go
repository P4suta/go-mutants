// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

// ModuleInterest is how many callers are currently holding an interest in a
// listing this workspace has not settled yet.
//
// It is test-only surface and it exists for one reason: the concurrency
// contract of [Workspace.Module] is about a window nothing outside this package
// can see. Two callers asking the same question share one `go list`, and the
// claim — that the caller which *started* it may go away without taking the
// answer from the one still waiting — can only be set up once the second caller
// has really joined. A test that waited for that with a sleep would pass on a
// fast machine and report a bug on a loaded one, which for a concurrency claim
// is worse than no test.
func (w *Workspace) ModuleInterest() int {
	w.moduleMu.Lock()
	defer w.moduleMu.Unlock()
	interest := 0
	for _, answer := range w.modules {
		if !answer.settled {
			interest += answer.interested
		}
	}
	return interest
}
