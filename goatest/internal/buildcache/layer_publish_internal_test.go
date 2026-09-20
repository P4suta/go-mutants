// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishingRetriesOnlyOnceTheNameItWantsIsFree(t *testing.T) {
	t.Parallel()
	renameFailure := errors.New("the name was taken")
	removeFailure := errors.New("the name could not be freed")
	for _, test := range []struct {
		name      string
		remove    error
		retried   bool
		published bool
		want      error
	}{
		{name: "a name it freed", retried: true, published: true},
		{name: "a name that was already free", remove: os.ErrNotExist, retried: true, published: true},
		{name: "a name it could not free", remove: removeFailure, want: removeFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			renames := 0
			hooks := layerHooks{
				rename: func(oldPath, newPath string) error {
					renames++
					if renames == 1 {
						return renameFailure
					}
					return os.Rename(oldPath, newPath)
				},
				remove: func(string) error { return test.remove },
			}
			directory := t.TempDir()
			temporary := filepath.Join(directory, "temporary")
			published := filepath.Join(directory, "published")
			if err := os.WriteFile(temporary, []byte("body"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := publish(temporary, published, hooks.resolved())
			if test.want != nil {
				if !errors.Is(err, test.want) || !errors.Is(err, renameFailure) {
					t.Fatalf("publishing over %s reported %v, want both %v and %v",
						test.name, err, renameFailure, test.want)
				}
			} else if err != nil {
				t.Fatalf("publishing over %s reported %v, want nothing", test.name, err)
			}
			if retried := renames > 1; retried != test.retried {
				t.Errorf("publishing over %s retried=%t, want %t", test.name, retried, test.retried)
			}
			if _, statErr := os.Stat(published); (statErr == nil) != test.published {
				t.Errorf("publishing over %s left the file present=%t, want %t",
					test.name, statErr == nil, test.published)
			}
		})
	}
}

func TestPublishingReportsASecondRenameThatAlsoFailed(t *testing.T) {
	t.Parallel()
	failure := errors.New("the name stayed taken")
	hooks := layerHooks{
		rename: func(string, string) error { return failure },
		remove: func(string) error { return nil },
	}
	if err := publish("temporary", "published", hooks.resolved()); !errors.Is(err, failure) {
		t.Fatalf("publishing reported %v, want %v", err, failure)
	}
}

func TestPuttingIntoALayerRefusesWhatItCannotStore(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		directory bool
		actionID  []byte
		outputID  []byte
		size      int64
		want      string
	}{
		{name: "a layer with no directory", actionID: key(1), outputID: key(2), want: "has no directory"},
		{
			name: "a put with no action identity", directory: true, outputID: key(2),
			want: "requires an action and an output identifier",
		},
		{
			name: "a put with no output identity", directory: true, actionID: key(1),
			want: "requires an action and an output identifier",
		},
		{
			name: "a put of a size below zero", directory: true,
			actionID: key(1), outputID: key(2), size: -1, want: "is negative",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var layer Layer
			if test.directory {
				layer.Dir = t.TempDir()
			}
			_, err := layer.putWithHooks(test.actionID, test.outputID, strings.NewReader(""),
				test.size, faultsMoment, layerHooks{})
			if err == nil {
				t.Fatalf("putting %s was accepted", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("putting %s reported %v, want it to say %q", test.name, err, test.want)
			}
		})
	}
}

func TestPuttingIntoALayerStopsAtAnObjectItCouldNotInspect(t *testing.T) {
	t.Parallel()
	failure := errors.New("the object could not be inspected")
	layer := Layer{Dir: t.TempDir()}
	_, err := layer.putWithHooks(key(1), key(2), strings.NewReader(routingBody),
		routingObjectSize, faultsMoment, failingObjectStat(failure))
	if !errors.Is(err, failure) {
		t.Fatalf("putting reported %v, want %v rather than a write over what it could not read", err, failure)
	}
}

func TestPuttingIntoALayerWritesAnObjectItCannotReuse(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		stored  string
		written bool
	}{
		{name: "an object of the size it declared", stored: routingBody},
		{name: "an object of another size", stored: routingBody + "0", written: true},
		{name: "no object at all", written: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := Layer{Dir: t.TempDir()}
			if test.stored != "" {
				writeStoredFile(t, layer.objectPath(key(2)), test.stored)
			}
			body := strings.NewReader(routingBody)
			entry, err := layer.putWithHooks(key(1), key(2), body, routingObjectSize, faultsMoment, layerHooks{})
			if err != nil {
				t.Fatalf("putting over %s reported %v", test.name, err)
			}
			if written := body.Len() == 0; written != test.written {
				t.Fatalf("putting over %s read the body=%t, want %t", test.name, written, test.written)
			}
			if entry.DiskPath != layer.objectPath(key(2)) {
				t.Errorf("the entry points at %q, want the object path", entry.DiskPath)
			}
		})
	}
}
