// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner_test

import (
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/tempowner"
	"github.com/P4suta/go-mutants/internal/testkit"
)

func TestMain(m *testing.M) {
	testkit.OwnedMain(m, func(root string) error {
		owner, err := tempowner.Claim(root, time.Now())
		if err != nil {
			return err
		}
		return owner.Keep()
	})
}
