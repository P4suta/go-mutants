// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// `doctor` and `init` against the real thing: a real Go toolchain on PATH, a
// real module, and a configuration file that is loaded rather than parsed.
//
// The unit tests drive the checks one at a time with fabricated findings, which
// is how the table and the document are pinned. What they cannot say is whether
// the machine a developer is sitting at passes — and a diagnosis that is wrong
// about a working machine is worse than no diagnosis, because the first thing
// anybody does with `doctor` is believe it.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/cli/...
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/schemas"
)

// TestDoctorIsGreenOnThisRepository. go-mutants develops itself, so its own
// repository is a module with a real toolchain, a real configuration file, and
// git — everything `doctor` looks for. If it cannot pass here it cannot pass
// anywhere.
func TestDoctorIsGreenOnThisRepository(t *testing.T) {
	repository := repositoryRoot(t)
	// The cache check writes a probe file, so it is pointed at a temporary
	// directory: a test suite has no business creating anything in the
	// developer's own cache.
	base := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", base)
	t.Setenv("LocalAppData", base)
	t.Chdir(repository)

	code, stdout, stderr := execute(t, "doctor")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants doctor` exited %d in its own repository\n%s%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "FAIL") {
		t.Errorf("a check failed on a machine that can build go-mutants:\n%s", stdout)
	}
	for _, needle := range []string{"go toolchain", "github.com/P4suta/go-mutants", config.FileName} {
		if !strings.Contains(stdout, needle) {
			t.Errorf("the table does not report %q:\n%s", needle, stdout)
		}
	}
}

// TestDoctorJSONOnThisRepositorySatisfiesTheSchema is the same run through
// --json. The document is validated before it is printed, so this proves the
// findings a real machine produces are ones the published schema accepts —
// including the details, which the unit tests can only fabricate.
func TestDoctorJSONOnThisRepositorySatisfiesTheSchema(t *testing.T) {
	repository := repositoryRoot(t)
	base := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", base)
	t.Setenv("LocalAppData", base)
	t.Chdir(repository)

	code, stdout, stderr := execute(t, "doctor", "--json")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants doctor --json` exited %d\n%s", code, stderr)
	}
	if err := schemas.Validate(schemas.DoctorV1, []byte(stdout)); err != nil {
		t.Fatalf("the document does not satisfy %s: %v\n%s", schemas.DoctorV1, err, stdout)
	}
	var doc doctorDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decoding the document: %v", err)
	}
	for _, c := range doc.Checks {
		if c.Status == statusFail {
			t.Errorf("check %q failed: %s", c.Name, c.Detail)
		}
		if c.Detail == "" {
			t.Errorf("check %q reported no detail", c.Name)
		}
	}
}

// TestInitWritesAConfigurationThatLoads is the round trip through the
// filesystem: the command writes a real file, and [config.Load] — the function
// every run starts with — reads it back to exactly the defaults.
//
// The unit test proves the generated text resolves to [config.Defaults]. This
// proves the bytes that reach the disk do, which is a different claim on a
// platform that could rewrite a line ending on the way.
func TestInitWritesAConfigurationThatLoads(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	code, stdout, stderr := execute(t, "init")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants init` exited %d\n%s%s", code, stdout, stderr)
	}
	path := filepath.Join(dir, config.FileName)

	loaded, err := config.Load(path, config.Overlay{})
	if err != nil {
		t.Fatalf("the file `init` wrote does not load: %v", err)
	}
	if diff := cmp.Diff(config.Defaults(), loaded); diff != "" {
		t.Errorf("the written configuration is not the defaults (-want +got):\n%s", diff)
	}

	// Written with the line endings it was generated with, whatever the
	// platform: a configuration file is hashed into nothing, but a file that
	// grew carriage returns on Windows would fail `init --check` on Linux and
	// nowhere else.
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the written file: %v", err)
	}
	if strings.Contains(string(written), "\r\n") {
		t.Error("the written file carries CRLF line endings")
	}
	if code, _, _ = execute(t, "init", "--check"); code != int(mutation.ExitOK) {
		t.Errorf("`init --check` exited %d against the file `init` had just written", code)
	}
}

// repositoryRoot resolves go-mutants' own checkout from this package's
// directory.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("the repository root is not a module: %v", err)
	}
	return root
}
