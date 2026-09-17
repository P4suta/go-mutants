// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"flag"
	"testing"
)

func TestRegisteringTheUpdateFlagTwiceIsANoOp(t *testing.T) {
	t.Parallel()
	if flag.Lookup(UpdateFlagName) == nil {
		t.Fatalf("%s is not registered, so this test cannot show a second registration being refused", UpdateFlagName)
	}
	if registerUpdateFlag() {
		t.Fatalf("registerUpdateFlag re-registered %s instead of leaving the existing flag alone", UpdateFlagName)
	}
}

func TestUpdateReadsTheFlagSetRatherThanAPointer(t *testing.T) {
	registered := flag.Lookup(UpdateFlagName)
	if registered == nil {
		t.Fatalf("%s is not registered", UpdateFlagName)
	}
	previous := registered.Value.String()
	t.Cleanup(func() {
		if err := registered.Value.Set(previous); err != nil {
			t.Fatalf("restore %s: %v", UpdateFlagName, err)
		}
	})
	for _, want := range []bool{true, false} {
		value := "false"
		if want {
			value = "true"
		}
		if err := registered.Value.Set(value); err != nil {
			t.Fatalf("set %s=%s: %v", UpdateFlagName, value, err)
		}
		if got := Update(); got != want {
			t.Errorf("Update() = %t with the flag set to %s, want %t", got, value, want)
		}
	}
}

func TestTheUpdateFlagIsOffUntilSomebodyAsksForIt(t *testing.T) {
	t.Parallel()
	registered := flag.Lookup(UpdateFlagName)
	if registered == nil {
		t.Fatalf("%s is not registered", UpdateFlagName)
	}
	if registered.DefValue != "false" {
		t.Fatalf("%s defaults to %q, want the golden files left alone", UpdateFlagName, registered.DefValue)
	}
}
