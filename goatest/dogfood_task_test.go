// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDogfoodTaskRunsBuiltCLIWithoutGoRunWrapper(t *testing.T) {
	t.Parallel()
	data, err := readFromAncestor(t, "mise.toml")
	if err != nil {
		if inSnapshot(t) {
			return
		}
		t.Fatal(err)
	}
	const (
		marker           = "[tasks.dogfood-runner]"
		dogfoodTaskParts = 2
	)
	parts := strings.SplitN(string(data), marker, dogfoodTaskParts)
	if len(parts) != dogfoodTaskParts {
		t.Fatal("mise.toml has no dogfood task")
	}
	task := parts[1]
	if next := strings.Index(task, "\n["); next >= 0 {
		task = task[:next]
	}
	if strings.Contains(task, "go run") {
		t.Fatal("dogfood uses a go run wrapper that can retain the CLI after Ctrl-C")
	}

	for _, required := range []string{
		"go build -o ./dist/dogfood/goatest ./cmd/goatest",
		"./dist/dogfood/goatest verify --ui=plain",
		"go build -o ./dist/dogfood/goatest.exe ./cmd/goatest",
		`.\dist\dogfood\goatest.exe verify --ui=plain`,
	} {
		if !strings.Contains(task, required) {
			t.Errorf("dogfood task omitted %q", required)
		}
	}

	changed := taskBody(t, string(data), "[tasks.dogfood-changed]")
	if strings.Contains(changed, "go run") {
		t.Error("dogfood-changed uses a go run wrapper that can retain the CLI after Ctrl-C")
	}
	for _, required := range []string{
		"./dist/dogfood/goatest verify --changed=origin/main --ui=plain",
		`.\dist\dogfood\goatest.exe verify --changed=origin/main --ui=plain`,
	} {
		if !strings.Contains(changed, required) {
			t.Errorf("dogfood-changed task omitted %q", required)
		}
	}
}

func taskBody(t *testing.T, document, heading string) string {
	t.Helper()
	const bodyParts = 2
	parts := strings.SplitN(document, heading, bodyParts)
	if len(parts) != bodyParts {
		t.Fatalf("mise.toml has no %s task", heading)
	}
	body := parts[1]
	if next := strings.Index(body, "\n["); next >= 0 {
		body = body[:next]
	}
	return body
}

func readFromAncestor(t *testing.T, name string) ([]byte, error) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	var looked []string
	for {
		candidate := filepath.Join(dir, name)
		looked = append(looked, candidate)
		data, readErr := os.ReadFile(candidate)
		if readErr == nil {
			return data, nil
		}
		if !errors.Is(readErr, fs.ErrNotExist) {
			return nil, readErr
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("no %s in %s or any directory above it", name, strings.Join(looked, ", "))
		}
		dir = parent
	}
}

func inSnapshot(t *testing.T) bool {
	t.Helper()
	_, err := readFromAncestor(t, "go.work")
	return err != nil
}
