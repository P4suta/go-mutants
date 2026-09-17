// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

var hooksMoment = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func TestResolvingLayerHooksLeavesNoneOfThemNil(t *testing.T) {
	t.Parallel()
	hooks := layerHooks{}.resolved()
	for name, present := range map[string]bool{
		"mkdirAll":        hooks.mkdirAll != nil,
		"createTemporary": hooks.createTemporary != nil,
		"copyBody":        hooks.copyBody != nil,
		"readFile":        hooks.readFile != nil,
		"stat":            hooks.stat != nil,
		"readDir":         hooks.readDir != nil,
		"chtimes":         hooks.chtimes != nil,
		"remove":          hooks.remove != nil,
		"rename":          hooks.rename != nil,
		"now":             hooks.now != nil,
	} {
		if !present {
			t.Errorf("resolved hooks left %s nil; the first call through it panics", name)
		}
	}
}

func TestResolvingLayerHooksKeepsEveryOneItWasGiven(t *testing.T) {
	t.Parallel()
	answered := errors.New("the hook it was given answered")
	given := layerHooks{
		mkdirAll:        func(string, os.FileMode) error { return answered },
		createTemporary: func(string, string) (layerWritableFile, error) { return nil, answered },
		copyBody:        func(io.Writer, io.Reader) (int64, error) { return 0, answered },
		readFile:        func(string) ([]byte, error) { return nil, answered },
		stat:            func(string) (fs.FileInfo, error) { return nil, answered },
		readDir:         func(string) ([]os.DirEntry, error) { return nil, answered },
		chtimes:         func(string, time.Time, time.Time) error { return answered },
		remove:          func(string) error { return answered },
		rename:          func(string, string) error { return answered },
		now:             func() time.Time { return hooksMoment },
	}
	hooks := given.resolved()
	_, createErr := hooks.createTemporary("", "")
	_, copyErr := hooks.copyBody(nil, nil)
	_, readErr := hooks.readFile("")
	_, statErr := hooks.stat("")
	_, dirErr := hooks.readDir("")
	for name, err := range map[string]error{
		"mkdirAll":        hooks.mkdirAll("", 0),
		"createTemporary": createErr,
		"copyBody":        copyErr,
		"readFile":        readErr,
		"stat":            statErr,
		"readDir":         dirErr,
		"chtimes":         hooks.chtimes("", hooksMoment, hooksMoment),
		"remove":          hooks.remove(""),
		"rename":          hooks.rename("", ""),
	} {
		if !errors.Is(err, answered) {
			t.Errorf("resolving replaced %s with its own: %v", name, err)
		}
	}
	if !hooks.now().Equal(hooksMoment) {
		t.Errorf("resolving replaced now: it reads %s, want %s", hooks.now(), hooksMoment)
	}
}

func TestResolvingServeHooksFillsWhatIsMissingAndKeepsWhatIsGiven(t *testing.T) {
	t.Parallel()
	empty := serveHooks{}.resolved()
	if empty.now == nil || empty.statsName == nil || empty.layer.stat == nil {
		t.Fatalf("resolved serve hooks left a hook nil: now=%t statsName=%t layer=%t",
			empty.now == nil, empty.statsName == nil, empty.layer.stat == nil)
	}
	name := empty.statsName()
	if !strings.HasPrefix(name, strconv.Itoa(os.Getpid())+"-") || !strings.HasSuffix(name, ".json") {
		t.Errorf("a serve that named no stats file uses %q, want this process's own default name", name)
	}
	given := serveHooks{
		now:       func() time.Time { return hooksMoment },
		statsName: func() string { return "stats-of-its-own" },
	}.resolved()
	if !given.now().Equal(hooksMoment) {
		t.Errorf("resolving replaced now: it reads %s, want %s", given.now(), hooksMoment)
	}
	if given.statsName() != "stats-of-its-own" {
		t.Errorf("resolving replaced statsName: it reads %q, want the one it was given", given.statsName())
	}
}
