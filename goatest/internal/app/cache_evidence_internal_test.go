// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/buildcache"
	"github.com/P4suta/go-mutants/goatest/internal/cache"
	"github.com/P4suta/go-mutants/goatest/internal/evidence"
	"github.com/P4suta/go-mutants/goatest/internal/retention"
)

var (
	evidenceOldest = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	evidenceNewest = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
)

func stampOf(moment time.Time) string { return moment.UTC().Format(time.RFC3339Nano) }

func TestAStatusDetailNamesAMomentOnlyWhereThereIsOne(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		detail string
		want   []string
		absent []string
	}{
		{
			name:   "a build cache that has never been written to",
			detail: buildCacheStatusEvidence("build", buildcache.Status{}).Detail,
			want:   []string{"entries=0", "bytes=0"}, absent: []string{"oldest="},
		},
		{
			name:   "a build cache with an oldest entry",
			detail: buildCacheStatusEvidence("build", buildcache.Status{Oldest: evidenceOldest}).Detail,
			want:   []string{"oldest=" + stampOf(evidenceOldest)},
		},
		{
			name:   "a store that holds nothing",
			detail: retentionDetail(retention.Status{}),
			want:   []string{"entries=0"}, absent: []string{"oldest=", "newest="},
		},
		{
			name:   "a store with an oldest entry alone",
			detail: retentionDetail(retention.Status{Oldest: evidenceOldest}),
			want:   []string{"oldest=" + stampOf(evidenceOldest)}, absent: []string{"newest="},
		},
		{
			name:   "a store with a newest entry alone",
			detail: retentionDetail(retention.Status{Newest: evidenceNewest}),
			want:   []string{"newest=" + stampOf(evidenceNewest)}, absent: []string{"oldest="},
		},
		{
			name:   "a cache that holds nothing",
			detail: cacheStatusEvidence("status", cache.Status{}).Detail,
			want:   []string{"entries=0"}, absent: []string{"oldest=", "newest="},
		},
		{
			name:   "a cache with an oldest entry alone",
			detail: cacheStatusEvidence("status", cache.Status{Oldest: evidenceOldest}).Detail,
			want:   []string{"oldest=" + stampOf(evidenceOldest)}, absent: []string{"newest="},
		},
		{
			name:   "a cache with a newest entry alone",
			detail: cacheStatusEvidence("status", cache.Status{Newest: evidenceNewest}).Detail,
			want:   []string{"newest=" + stampOf(evidenceNewest)}, absent: []string{"oldest="},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, want := range test.want {
				if !strings.Contains(test.detail, want) {
					t.Errorf("%s reads %q, want it to say %q", test.name, test.detail, want)
				}
			}
			for _, absent := range test.absent {
				if strings.Contains(test.detail, absent) {
					t.Errorf("%s reads %q, want it to say nothing of %q", test.name, test.detail, absent)
				}
			}
		})
	}
}

func TestMutationEvidenceSaysWhatItHoldsAndWhatItCannot(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status evidence.MutationStatus
		want   string
		says   []string
		absent []string
	}{
		{
			name: "evidence nobody wrote", status: evidence.MutationStatus{},
			want: "missing", absent: []string{"module=", "modified=", "problem="},
		},
		{
			name: "evidence it cannot read", want: "invalid",
			status: evidence.MutationStatus{Present: true, Problem: "read failed"},
			says:   []string{`problem="read failed"`},
		},
		{
			name: "evidence of a module at a moment", want: "ready",
			status: evidence.MutationStatus{
				Present: true, Valid: true, ModulePath: "example.com/app", Modified: evidenceNewest,
			},
			says:   []string{"module=example.com/app", "modified=" + stampOf(evidenceNewest)},
			absent: []string{"problem="},
		},
		{
			name: "evidence of a module at no moment at all", want: "ready",
			status: evidence.MutationStatus{Present: true, Valid: true, ModulePath: "example.com/app"},
			says:   []string{"module=example.com/app"}, absent: []string{"modified="},
		},
		{
			name: "evidence at a moment of no module at all", want: "ready",
			status: evidence.MutationStatus{Present: true, Valid: true, Modified: evidenceNewest},
			says:   []string{"modified=" + stampOf(evidenceNewest)}, absent: []string{"module="},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			item := mutationEvidenceStatusEvidence("status", "ready", test.status)
			if item.Status != test.want {
				t.Fatalf("%s reads status %q, want %q", test.name, item.Status, test.want)
			}
			for _, says := range test.says {
				if !strings.Contains(item.Detail, says) {
					t.Errorf("%s reads %q, want it to say %q", test.name, item.Detail, says)
				}
			}
			for _, absent := range test.absent {
				if strings.Contains(item.Detail, absent) {
					t.Errorf("%s reads %q, want it to say nothing of %q", test.name, item.Detail, absent)
				}
			}
		})
	}
}

func TestABuildCacheCollectionSaysWhetherItRan(t *testing.T) {
	t.Parallel()
	collected := buildcache.Collected{RemovedActions: 1, RemovedObjects: 2, RemovedBytes: 3}
	ran := buildCacheGCEvidence(collected, true)
	if ran.Status != "completed" || !strings.Contains(ran.Detail, "removed-actions=1") {
		t.Fatalf("a collection that ran reads %+v, want what it removed", ran)
	}
	skipped := buildCacheGCEvidence(collected, false)
	if skipped.Status != "skipped" || strings.Contains(skipped.Detail, "removed-actions=") {
		t.Fatalf("a collection that stood down reads %+v, want no count at all", skipped)
	}
}
