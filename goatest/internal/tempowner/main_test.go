// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner_test

import (
	"os"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/tempowner"
	"github.com/P4suta/go-mutants/internal/testkit"
)

func TestMain(m *testing.M) {
	testkit.OwnedMain(m, func(root string) error {
		owner, err := tempowner.Claim(root, tempowner.Marker{RunID: "tempowner-tests"}, time.Now())
		if err != nil {
			return err
		}
		return owner.Keep()
	})
}

func TestFixtureScratchRootIsKept(t *testing.T) {
	t.Parallel()
	marker, err := tempowner.ReadMarker(os.TempDir())
	if err != nil || marker.Schema != tempowner.Schema || !marker.Kept {
		t.Fatalf("fixture scratch ownership = %+v, error = %v; want a kept parent", marker, err)
	}
}
