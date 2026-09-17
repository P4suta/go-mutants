// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/keptledger"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const keptLedgerItemCount = 3

func temporaryMoment() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func TestTemporaryStatusAndSweepSayWhenNobodyNamedADirectory(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		evidence func(Service) report.Evidence
		id       string
	}{
		{
			name:     "the orphan status",
			evidence: func(s Service) report.Evidence { return s.temporaryStatus(temporaryMoment()) },
			id:       "orphans",
		},
		{
			name:     "the sweep",
			evidence: func(s Service) report.Evidence { return s.temporarySweep(temporaryMoment()) },
			id:       "sweep",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			unnamed := test.evidence(Service{})
			if unnamed.ID != test.id || unnamed.Status != "skipped" {
				t.Fatalf("a service that named no temporary directory answered %+v, want %q skipped",
					unnamed, test.id)
			}
			named := test.evidence(Service{TempDirectory: t.TempDir()})
			if named.ID != test.id || named.Status == "skipped" {
				t.Fatalf("a service that named one answered %+v, want %q to have run", named, test.id)
			}
		})
	}
}

func TestTheNativeCacheStatusAndSweepSayWhenNobodyNamedADirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = 1\ncontract = \"standard-v1\"\n"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	unconfigured := Service{}
	for _, test := range []struct {
		name     string
		evidence func(Service) report.Evidence
		id       string
	}{
		{
			name:     "the orphan status",
			evidence: func(s Service) report.Evidence { return s.nativeCacheStatus(root, temporaryMoment()) },
			id:       "native-cache-orphans",
		},
		{
			name:     "the sweep",
			evidence: func(s Service) report.Evidence { return s.nativeCacheSweep(root, temporaryMoment()) },
			id:       "native-cache-sweep",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			unnamed := test.evidence(unconfigured)
			if unnamed.ID != test.id || unnamed.Status != "skipped" {
				t.Fatalf("a service with no build cache directory answered %+v, want %q skipped",
					unnamed, test.id)
			}
			cacheRoot := t.TempDir()
			named := test.evidence(Service{UserCacheDir: func() (string, error) { return cacheRoot, nil }})
			if named.ID != test.id || named.Status == "skipped" {
				t.Fatalf("a service with one answered %+v, want %q to have run", named, test.id)
			}
		})
	}
}

func TestTheNativeCacheParentIsTheDirectoryAboveTheBuildCache(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = 1\ncontract = \"standard-v1\"\n"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if parent := (Service{}).nativeCacheParent(root); parent != "" {
		t.Fatalf("a service with no build cache directory named the parent %q, want none", parent)
	}
	cacheRoot := t.TempDir()
	service := Service{UserCacheDir: func() (string, error) { return cacheRoot, nil }}
	want := filepath.Join(cacheRoot, "goatest")
	if parent := service.nativeCacheParent(root); parent != want {
		t.Fatalf("nativeCacheParent = %q, want %q", parent, want)
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ".goatest.toml"), []byte("version = "), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if parent := service.nativeCacheParent(broken); parent != "" {
		t.Fatalf("a repository whose configuration cannot be read named the parent %q, want none", parent)
	}
}

func TestKeptTemporaryStatusCountsWhatIsThereAndWhatIsNot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	present := t.TempDir()
	absent := filepath.Join(root, "gone")
	if err := keptledger.Update(keptledger.Path(root), func(ledger *keptledger.Ledger) error {
		ledger.Entries = []keptledger.Entry{
			{RunID: "run-present", Path: present, Bytes: 1, KeptAt: temporaryMoment()},
			{RunID: "run-absent", Path: absent, Bytes: 2, KeptAt: temporaryMoment()},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	items := keptTemporaryStatus(root)
	if len(items) != keptLedgerItemCount {
		t.Fatalf("a ledger of two entries stated %d items, want three", len(items))
	}
	byID := make(map[string]report.Evidence, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	if byID["run-absent"].Status != "missing" {
		t.Errorf("an entry whose directory is gone is %q, want missing", byID["run-absent"].Status)
	}
	if byID["run-present"].Status != "unverified" {
		t.Errorf("an entry nothing vouches for is %q, want unverified", byID["run-present"].Status)
	}
	total := byID["kept-temp-status"]
	if total.Status != "ready" || !strings.Contains(total.Detail, "entries=2") ||
		!strings.Contains(total.Detail, "missing=1") || !strings.Contains(total.Detail, "errors=1") {
		t.Fatalf("the total says %q, want two entries, one missing and one it could not verify", total.Detail)
	}
}

func TestKeptTemporaryStatusSaysWhenItCannotReadTheLedger(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(keptledger.Path(root)), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keptledger.Path(root), []byte("{"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}

	items := keptTemporaryStatus(root)
	if len(items) != 1 || items[0].Status != "unavailable" {
		t.Fatalf("a ledger nobody can read stated %+v, want one unavailable item", items)
	}
}
