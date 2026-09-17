// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package goatest_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest"
)

func TestRunPreservesTestingTAndScope(t *testing.T) {
	t.Parallel()
	called := false
	goatest.Run(t, goatest.Unit(), func(gt *goatest.T) {
		called = true
		if gt.T != t {
			t.Fatal("goatest.T did not embed the original testing.T")
		}
		if got := gt.Scope(); got.Kind != goatest.ScopeUnit || got.Capabilities() != nil {
			t.Errorf("scope = %+v", got)
		}
	})
	if !called {
		t.Fatal("Run did not execute the test body")
	}
}

func TestIntegrationCarriesUniqueCapabilitiesWithoutAliasing(t *testing.T) {
	t.Parallel()
	got := goatest.Integration("postgres", "redis", "postgres")
	if got.Kind != goatest.ScopeIntegration || !slices.Equal(got.Capabilities(), []string{"postgres", "redis"}) {
		t.Fatalf("scope = %+v", got)
	}
	capabilities := got.Capabilities()
	capabilities[0] = "mutated"
	if !slices.Equal(got.Capabilities(), []string{"postgres", "redis"}) {
		t.Fatal("Capabilities aliases internal metadata")
	}
}

func TestIntegrationRejectsBlankCapability(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("Integration accepted a blank capability")
		}
	}()
	_ = goatest.Integration(" \t")
}

func TestIntegrationRejectsNoCapabilityAtAll(t *testing.T) {
	t.Parallel()
	defer func() {
		recovered := recover()
		message, ok := recovered.(string)
		if !ok || !strings.Contains(message, "requires at least one capability") {
			t.Fatalf("Integration() recovered %v, want the missing capability named", recovered)
		}
	}()
	goatest.Integration()
	t.Fatal("Integration with no capability returned a scope")
}

func TestNoTestAnswersForItsOwnScope(t *testing.T) {
	t.Parallel()
	var absent *goatest.T
	if got := absent.Scope(); got.Kind != "" || len(got.Capabilities()) != 0 {
		t.Fatalf("Scope of no test = %+v", got)
	}
}
