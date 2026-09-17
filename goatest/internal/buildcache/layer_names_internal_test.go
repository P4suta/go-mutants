// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"testing"
	"time"
)

const (
	touchIntervalOverride = 5 * time.Second
	layerEntryName        = "abcdef"
)

func TestAMarkerTemporaryIsNamedAtBothEnds(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: markerTemporaryPrefix + "1234" + markerTemporarySuffix, want: true},
		{name: markerTemporaryPrefix + markerTemporarySuffix, want: true},
		{name: markerTemporaryPrefix + "1234"},
		{name: "1234" + markerTemporarySuffix},
		{name: MarkerName},
		{name: ""},
	} {
		t.Run("name "+test.name, func(t *testing.T) {
			t.Parallel()
			if got := markerTemporaryName(test.name); got != test.want {
				t.Fatalf("markerTemporaryName(%q) = %t, want %t", test.name, got, test.want)
			}
		})
	}
}

func TestAHexadecimalEntryNameIsLongEnoughAndHoldsNothingElse(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: layerEntryName, want: true},
		{name: "0123456789abcdef", want: true},
		{name: "ab", want: true},
		{name: "a"},
		{name: ""},
		{name: "abcdeg"},
		{name: "ABCDEF"},
		{name: "abcde/"},
		{name: "abcde:"},
		{name: "abcde`"},
	} {
		t.Run("name "+test.name, func(t *testing.T) {
			t.Parallel()
			if got := hexadecimal(test.name); got != test.want {
				t.Fatalf("hexadecimal(%q) = %t, want %t", test.name, got, test.want)
			}
		})
	}
}

func TestALayerTouchesAtTheIntervalItWasGivenOrTheBaseOne(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		touch time.Duration
		want  time.Duration
	}{
		{name: "an interval of its own", touch: touchIntervalOverride, want: touchIntervalOverride},
		{name: "no interval at all", want: BaseTouchInterval},
		{name: "an interval of no time", touch: 0, want: BaseTouchInterval},
		{name: "an interval below zero", touch: -time.Second, want: BaseTouchInterval},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := Layer{Touch: test.touch}
			if got := layer.touchInterval(); got != test.want {
				t.Fatalf("touchInterval = %s, want %s", got, test.want)
			}
			if got := layer.MinIdle(); got != MinIdleTouchIntervals*test.want {
				t.Fatalf("MinIdle = %s, want %d intervals of %s", got, MinIdleTouchIntervals, test.want)
			}
		})
	}
}

func TestALayerWithNoDirectoryRefusesToPrepareOrEnsure(t *testing.T) {
	t.Parallel()
	if err := (Layer{}).prepareWithHooks(layerHooks{}.resolved()); err == nil {
		t.Fatal("a layer with no directory was prepared")
	}
	if err := (Layer{}).ensureWithHooks(layerHooks{}.resolved()); err == nil {
		t.Fatal("a layer with no directory was ensured")
	}
	files, err := (Layer{}).walk(actionsDirectory, layerHooks{}.resolved())
	if err != nil || files != nil {
		t.Fatalf("a layer with no directory walked %+v (%v), want nothing", files, err)
	}
}
