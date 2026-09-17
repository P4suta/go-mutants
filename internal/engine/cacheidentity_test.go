// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
)

var cacheIdentityInputs = map[string]string{
	"Mutation.Include": "decides which files discovery walks, so which mutants are catalogued; " +
		"cache.Context.CatalogDigest is mutation.Catalog.Digest, over the ordered id list",
	"Mutation.Exclude":   "the same, from the other side",
	"Mutation.Operators": "internal/engine/rules.go passes it to operatorselect.Select, and the rule set decides which mutants exist at all",
	"Mutation.Profile":   "the same call, and a profile that drops a tier drops its mutants out of the catalogue",
	"Test.Command":       "cache.Context.TestCommand, the argv as the user wrote it",
	"Test.Timeout":       "cache.Context.ConfiguredTimeout, the configured number and deliberately not the derived one",
}

var cacheIdentityExclusions = map[string]string{
	"Version": "the schema version of the file, which says how to read it rather than what the run does",
	"Mutation.Expect": "decides which survivors the report calls expected and what the process exits with; " +
		"every mutant reaches the same verdict either way",
	"Test.BaselineRuns": "feeds the derived timeout, which is out of the key on purpose -- a wall-clock number " +
		"would give every run its own empty context -- and whose soundness cache.Entry.UsableUnder buys at the point of use",
	"Execution.Jobs":        "how many mutants run at once, which decides nothing about what any one of them measures",
	"Cache.Mode":            "whether this cache is consulted at all; a setting about the cache cannot be part of what the cache identifies",
	"Cache.Directory":       "where the store lives, which is not a property of the run the entries describe",
	"Policy.Strict":         "a gate applied to a finished report",
	"Policy.MinimumScore":   "the same gate, by number",
	"Policy.RequireMutants": "the same gate, by count",
	"Report.Directory":      "where the run is written out",
	"Report.Formats":        "which shapes it is written in",
	"Report.High":           "a threshold for how a score is coloured",
	"Report.Low":            "the same threshold, at the other end",
}

var cacheIdentityUnsettled = map[string]string{
	"Test.Memory": "the timeout's twin by internal/cache/store.go's own words, filtered at the point of use by " +
		"cache.Entry.UsableWithin exactly as the timeout is by UsableUnder -- and absent from the key while the " +
		"timeout is in it. The timeout is in the key as a partition and not for soundness, which UsableUnder " +
		"already gives; whether memory should partition too is nowhere written",
	"Test.Narrowing": "test-level narrowing decides which tests are run against a mutant, and coverage attribution " +
		"is an approximation this repository documents as one. A mutant a narrowed run records as survived, because " +
		"the test that would have killed it was attributed elsewhere, is a different verdict from the same tree",
	"Test.Probing": "probing licenses not executing a mutant at all, which is the strongest thing a setting can do " +
		"to an outcome short of deciding it",
	"Execution.Isolate": "isolation decides what else is running in the process tree when a mutant is measured",
}

func TestEverySettingIsEitherInTheCacheIdentityOrDeliberatelyOutOfIt(t *testing.T) {
	t.Parallel()

	var settings []string
	collectSettings(reflect.TypeOf(config.Config{}), "", &settings)
	if len(settings) == 0 {
		t.Fatal("the walk found no setting, and config.Config has several")
	}

	lists := map[string]map[string]string{
		"cacheIdentityInputs":     cacheIdentityInputs,
		"cacheIdentityExclusions": cacheIdentityExclusions,
		"cacheIdentityUnsettled":  cacheIdentityUnsettled,
	}
	for _, name := range settings {
		var in []string
		for list, members := range lists {
			if reason, ok := members[name]; ok {
				in = append(in, list)
				if reason == "" {
					t.Errorf("%s names %s with no reason, which is a row that decided nothing", list, name)
				}
			}
		}
		slices.Sort(in)
		switch len(in) {
		case 0:
			t.Errorf("%s is in none of the three lists;\n"+
				"\tsay whether it changes a cached outcome -- a setting that does and is not in\n"+
				"\tcache.Context makes a warm run adopt entries measured under another value", name)
		case 1:
		default:
			t.Errorf("%s is in %v, and a setting is one of the three", name, in)
		}
	}

	for list, members := range lists {
		for name := range members {
			if !slices.Contains(settings, name) {
				t.Errorf("%s names %s, which config.Config no longer has;\n"+
					"\tdelete the row -- a decision about a setting that is gone reads as one about a setting that is not",
					list, name)
			}
		}
	}
}

func TestTheSettingWalkReachesANestedLeaf(t *testing.T) {
	t.Parallel()

	var settings []string
	collectSettings(reflect.TypeOf(config.Config{}), "", &settings)
	for _, want := range []string{"Test.Timeout", "Policy.MinimumScore", "Report.Formats"} {
		if !slices.Contains(settings, want) {
			t.Errorf("the walk did not reach %s, so the lists above are checked against a shorter tree than the one that exists", want)
		}
	}
	for _, unwanted := range []string{"Test", "Policy", "Report", "Mutation"} {
		if slices.Contains(settings, unwanted) {
			t.Errorf("the walk returned the section %s as a leaf, which would let one row excuse everything under it", unwanted)
		}
	}
}

func collectSettings(t reflect.Type, prefix string, out *[]string) {
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name := f.Name
		if prefix != "" {
			name = prefix + "." + name
		}
		if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(time.Duration(0)) {
			collectSettings(f.Type, name, out)
			continue
		}
		*out = append(*out, name)
	}
}
