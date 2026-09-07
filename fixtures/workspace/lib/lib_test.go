// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package lib

import "testing"

// TestDiffers exercises [Differs] and checks nothing about it, which is what
// leaves its mutants alive.
//
// The test passes: a red suite here would stop a run at the workspace root in
// the baseline, and the refusal that run is meant to prove happens after it.
func TestDiffers(t *testing.T) {
	_ = Differs(1, 2)
	_ = Differs(2, 2)
}
