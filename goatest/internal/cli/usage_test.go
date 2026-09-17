// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"errors"
	"testing"
)

func TestAUsageErrorReachesItsCause(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("a cause worth recognising")
	err := error(usageError{command: CommandTrace, cause: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is did not reach the cause of a usage error")
	}
	if err.Error() != sentinel.Error() {
		t.Fatalf("Error() = %q, want the cause's own text", err.Error())
	}
}

func TestAUsageErrorNamesWhereToLook(t *testing.T) {
	t.Parallel()
	if got, want := (usageError{command: CommandTrace}).Hint(), "run 'goatest help trace' for usage"; got != want {
		t.Errorf("Hint() = %q, want %q", got, want)
	}
	if got, want := (usageError{}).Hint(), "run 'goatest --help' for usage"; got != want {
		t.Errorf("Hint() with no command = %q, want %q", got, want)
	}
}
