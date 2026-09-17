// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

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
