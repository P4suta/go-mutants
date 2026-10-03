// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner_test

import (
	"os"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/tempowner"
	"github.com/P4suta/go-mutants/goatest/internal/testscratch"
)

func TestMain(m *testing.M) {
	testscratch.Main(m)
}

func TestFixtureScratchRootIsKept(t *testing.T) {
	t.Parallel()
	marker, err := tempowner.ReadMarker(os.TempDir())
	if err != nil || marker.Schema != tempowner.Schema || !marker.Kept {
		t.Fatalf("fixture scratch ownership = %+v, error = %v; want a kept parent", marker, err)
	}
}
