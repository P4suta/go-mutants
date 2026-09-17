// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package gomutants

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
)

func TestInstrumentationEnvironmentSupportsOverlayPathWithWhitespace(t *testing.T) {
	t.Parallel()
	goBinary := testkit.GoBinary(t)
	root := filepath.Join(t.TempDir(), "module root")
	if mkdirErr := os.MkdirAll(root, privateDirectoryMode); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	for name, contents := range map[string]string{
		"go.mod":                "module fixture.example/space\n\ngo 1.26.0\n",
		"value.go":              "package space\n",
		"overlay manifest.json": `{"Replace":{}}`,
	} {
		if writeErr := os.WriteFile(filepath.Join(root, name), []byte(contents), privateFileMode); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	overlay := filepath.Join(root, "overlay manifest.json")
	environment, err := instrumentationEnvironment(os.Environ(), overlay)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), goBinary, "list", "./...")
	command.Dir = root
	command.Env = append(environment, "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off")
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "fixture.example/space" {
		t.Fatalf("go list through spaced overlay = (%q, %v)", output, err)
	}
}
