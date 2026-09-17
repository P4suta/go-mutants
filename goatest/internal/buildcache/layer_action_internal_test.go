// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func layerActionID(t *testing.T) []byte {
	t.Helper()
	decoded, err := hex.DecodeString("abcdef0123456789")
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func writeLayerAction(t *testing.T, layer Layer, actionID []byte, contents string) {
	t.Helper()
	path := layer.actionPath(actionID)
	if err := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
}

func TestReadingALayerActionRefusesEveryRecordItCannotUse(t *testing.T) {
	t.Parallel()
	actionID := layerActionID(t)
	for _, test := range []struct {
		name     string
		contents string
		absent   bool
		found    bool
	}{
		{name: "a record that names an output and a size", contents: `{"output":"abcd","size":12}`, found: true},
		{name: "a record of no size at all", contents: `{"output":"abcd","size":0}`, found: true},
		{name: "a record nobody wrote", absent: true},
		{name: "a record that is not JSON", contents: "{"},
		{name: "a record that names no output", contents: `{"size":12}`},
		{name: "a record of a size below zero", contents: `{"output":"abcd","size":-1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := Layer{Dir: t.TempDir()}
			if !test.absent {
				writeLayerAction(t, layer, actionID, test.contents)
			}
			record, _, err := layer.readAction(actionID, layerHooks{}.resolved())
			if err != nil {
				t.Fatalf("readAction reported %v, want none", err)
			}
			if found := record != (actionRecord{}); found != test.found {
				t.Fatalf("readAction = (%+v, %t), want %t", record, found, test.found)
			}
		})
	}
}

func TestReadingALayerActionAnswersNothingWithoutADirectoryOrAnIdentity(t *testing.T) {
	t.Parallel()
	actionID := layerActionID(t)
	if record, _, err := (Layer{}).readAction(actionID, layerHooks{}.resolved()); record != (actionRecord{}) || err != nil {
		t.Fatalf("a layer with no directory found an action (%+v, %v)", record, err)
	}
	layer := Layer{Dir: t.TempDir()}
	if record, _, err := layer.readAction(nil, layerHooks{}.resolved()); record != (actionRecord{}) || err != nil {
		t.Fatalf("an action with no identity was found (%+v, %v)", record, err)
	}
}

func TestReadingALayerActionReportsAFailureThatIsNotAbsence(t *testing.T) {
	t.Parallel()
	actionID := layerActionID(t)
	sentinel := errors.New("the layer would not answer")
	for _, stage := range []string{"stat", "read"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			layer := Layer{Dir: t.TempDir()}
			writeLayerAction(t, layer, actionID, `{"output":"abcd","size":12}`)
			hooks := layerHooks{}.resolved()
			if stage == "stat" {
				hooks.stat = func(string) (os.FileInfo, error) { return nil, sentinel }
			} else {
				hooks.readFile = func(string) ([]byte, error) { return nil, sentinel }
			}
			record, _, err := layer.readAction(actionID, hooks)
			if record != (actionRecord{}) || !errors.Is(err, sentinel) {
				t.Fatalf("readAction = (%+v, %v), want the %s failure", record, err, stage)
			}
		})
	}
}
