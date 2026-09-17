// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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
	"github.com/P4suta/go-mutants/internal/testsupport"
)

func TestDoctorIsGreenOnThisRepository(t *testing.T) {
	repository := repositoryRoot(t)
	testsupport.CacheDir(t)
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

func TestDoctorJSONOnThisRepositorySatisfiesTheSchema(t *testing.T) {
	repository := repositoryRoot(t)
	testsupport.CacheDir(t)
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
