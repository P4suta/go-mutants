// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package probeable

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	childGoTestEnvironment = "PROBEABLE_CHILD_GO_TEST"
	childGoTestParent      = "parent"
	childGoTestNested      = "nested"
	expectedWidth          = 3
)

// TestWidth pins Width and nothing else, which is what makes it usable as the
// "test A" of a probe pass: it reaches exactly one probed site.
func TestWidth(t *testing.T) {
	if got := Width(); got != expectedWidth {
		t.Errorf("Width() = %d, want %d", got, expectedWidth)
	}
}

// TestLabel pins Label and nothing else, and is the "test B" every assertion
// about TestWidth is stated against.
func TestLabel(t *testing.T) {
	if got := Label(); got != "probe" {
		t.Errorf("Label() = %q, want %q", got, "probe")
	}
}

// TestReady pins the unprobed mutant's function, so that a run has a kill no
// probe pass can account for.
func TestReady(t *testing.T) {
	if !Ready() {
		t.Error("Ready() = false, want true")
	}
}

// TestPrintsALot is the chatty target the output-budget test probes: 2000 lines
// of 64 characters, which is over a hundred kilobytes and thirty times the
// smallest limit that test sets.
//
// It reaches no probed site on purpose. What is under test is the budget a pass
// runs under, and a target that also infected something would make a failure
// ambiguous between the two.
//
// It lives in the fixture rather than being written into the tree by the test
// that needs it, because the probeable session is prepared once and shared: no
// test that uses it can add a file to it.
func TestPrintsALot(t *testing.T) {
	for range 2000 {
		fmt.Println(strings.Repeat("x", 64))
	}
}

func TestFlagged(t *testing.T) {
	if os.Getenv("PROBEABLE_FAIL") == "yes" {
		t.Fatal("PROBEABLE_FAIL asked this test to fail")
	}
}

func TestSourceTreeIsPristine(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "gomutants_rt") {
			t.Fatalf("generated runtime is visible at %s", entry.Name())
		}
	}
	_, caller, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller is unavailable")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(caller), "probeable.go"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("__gm")) {
		t.Fatal("instrumentation is visible in probeable.go")
	}
	if !bytes.Contains(data, []byte("return 3")) {
		t.Fatal("probeable.go is not the pristine source")
	}
}

func TestChildGoTestUsesSessionOverlay(t *testing.T) {
	switch os.Getenv(childGoTestEnvironment) {
	case childGoTestNested:
		if got := Width(); got != expectedWidth {
			t.Fatalf("Width() = %d, want %d", got, expectedWidth)
		}
		return
	case childGoTestParent:
	default:
		return
	}
	command := exec.Command("go", "test", "-run=^TestChildGoTestUsesSessionOverlay$", ".")
	command.Env = append(os.Environ(), childGoTestEnvironment+"="+childGoTestNested)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("child go test failed: %v\n%s", err, output)
	}
}

// TestBlocks sleeps past any budget a probe pass gives it, when the
// environment says to.
//
// The environment gate is what keeps the fixture fast: the sleep would
// otherwise be paid by the verification run of every session prepared over this
// module, and by every whole-package target, for the sake of one test that asks
// for a timeout on purpose.
func TestBlocks(t *testing.T) {
	if os.Getenv("PROBEABLE_BLOCK") != "yes" {
		return
	}
	time.Sleep(30 * time.Second)
}
