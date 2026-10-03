// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"os"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/tempowner"
	"github.com/P4suta/go-mutants/internal/testkit"
)

func TestMain(m *testing.M) {
	if _, selected := os.LookupEnv(doctorHelperVariable); selected {
		os.Exit(m.Run())
	}
	testkit.OwnedMain(m, func(root string) error {
		owner, err := tempowner.Claim(root, tempowner.Marker{RunID: "app-tests"}, time.Now())
		if err != nil {
			return err
		}
		return owner.Keep()
	})
}
