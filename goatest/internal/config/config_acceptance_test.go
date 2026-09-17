// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/config"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

// TestAddAcceptanceKeepsWhatSomebodyWroteInTheFile is the claim an editing
// command has to make about a file a person maintains.
//
// `goatest accept` loaded the configuration, appended one acceptance, and wrote
// the whole model back. Every comment went with it: the SPDX header this
// repository's licence gate requires, and the argument beside each setting for
// why it is not at its default. A configuration file is not a serialisation of
// a struct -- it is a document, and most of what a reader needs from it is the
// part the struct does not hold.
//
// Found by using the product on itself: accepting one equivalent mutant in
// goatest's own .goatest.toml deleted fourteen lines of reasoning and the
// licence header, and the licence gate is what would have caught it next.
func TestAddAcceptanceKeepsWhatSomebodyWroteInTheFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	original := `# SPDX-FileCopyrightText: 2026 goatest contributors
# SPDX-License-Identifier: MIT OR Apache-2.0

# Why this file exists, which the struct does not hold.
version = 1
contract = "standard-v1"

[execution]
# Written out although it is the default, because the absence is a decision.
build_tags = []
`
	path := filepath.Join(root, config.FileName)
	if err := os.WriteFile(path, []byte(original), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}

	err := config.AddAcceptance(root, config.Acceptance{
		ID: "239afd2b864d0119", Reason: "an equivalent mutant no honest test can reach",
		Expires: time.Date(2027, 9, 17, 0, 0, 0, 0, time.UTC), Owner: "goatest",
	})
	if err != nil {
		t.Fatalf("AddAcceptance: %v", err)
	}

	after := string(readFile(t, path))
	for _, kept := range []string{
		"SPDX-License-Identifier: MIT OR Apache-2.0",
		"# Why this file exists, which the struct does not hold.",
		"# Written out although it is the default, because the absence is a decision.",
	} {
		if !strings.Contains(after, kept) {
			t.Errorf("the rewritten file lost %q:\n%s", kept, after)
		}
	}
	if !strings.Contains(after, "239afd2b864d0119") {
		t.Errorf("the acceptance is not in the file:\n%s", after)
	}

	loaded, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load after AddAcceptance: %v", err)
	}
	if len(loaded.Acceptance) != 1 || loaded.Acceptance[0].ID != "239afd2b864d0119" {
		t.Errorf("Load read %+v, want the one acceptance just added", loaded.Acceptance)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
