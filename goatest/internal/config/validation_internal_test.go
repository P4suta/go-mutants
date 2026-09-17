// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func loadConfigText(t *testing.T, document string) (Config, error) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(document), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	return Load(root)
}

func TestLoadRefusesEveryFieldItCannotAccept(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		document string
		message  string
	}{
		{name: "an execution timeout that is not a duration", document: "version = 1\n[execution]\ntimeout = \"soon\"\n", message: "execution timeout"},
		{name: "an execution timeout of zero", document: "version = 1\n[execution]\ntimeout = \"0s\"\n", message: "execution timeout"},
		{name: "a negative execution timeout", document: "version = 1\n[execution]\ntimeout = \"-1s\"\n", message: "execution timeout"},
		{name: "negative jobs", document: "version = 1\n[execution]\njobs = -1\n", message: "execution jobs must not be negative"},
		{name: "a negative cache budget", document: "version = 1\n[cache]\nmax_bytes = -1\n", message: "cache max_bytes"},
		{name: "a negative build cache budget", document: "version = 1\n[cache]\nbuild_max_bytes = -1\n", message: "cache build_max_bytes"},
		{name: "a negative report count", document: "version = 1\n[reports]\nkeep = -1\n", message: "reports keep"},
		{name: "a cache ttl that is not a duration", document: "version = 1\n[cache]\nttl = \"soon\"\n", message: "cache ttl"},
		{name: "a cache ttl of zero", document: "version = 1\n[cache]\nttl = \"0s\"\n", message: "cache ttl"},
		{name: "a negative cache ttl", document: "version = 1\n[cache]\nttl = \"-1h\"\n", message: "cache ttl"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loaded, err := loadConfigText(t, test.document)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Load = (%+v, %v), want %q", loaded, err, test.message)
			}
		})
	}
}

func TestLoadKeepsEveryBudgetItIsGivenAndFillsTheRestWithDefaults(t *testing.T) {
	t.Parallel()
	loaded, err := loadConfigText(t, "version = 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Cache.MaxBytes != defaultCacheMaxBytes || loaded.Cache.BuildMaxBytes != defaultBuildMaxBytes ||
		loaded.Reports.Keep != DefaultReportsKeep || loaded.Cache.TTL != defaultCacheTTL ||
		loaded.Execution.Timeout != defaultExecutionTimeout {
		t.Fatalf("defaults = %+v", loaded)
	}
	if len(loaded.Project.Packages) != 1 || loaded.Project.Packages[0] != "./..." {
		t.Fatalf("default packages = %v", loaded.Project.Packages)
	}
	stated, err := loadConfigText(t, "version = 1\n[project]\npackages = [\"./cmd/...\"]\n"+
		"[execution]\ntimeout = \"30s\"\n[cache]\nmax_bytes = 1\nbuild_max_bytes = 2\nttl = \"1h\"\n[reports]\nkeep = 3\n")
	if err != nil {
		t.Fatal(err)
	}
	if stated.Cache.MaxBytes != 1 || stated.Cache.BuildMaxBytes != 2 || stated.Reports.Keep != 3 ||
		stated.Cache.TTL.String() != "1h0m0s" || stated.Execution.Timeout.String() != "30s" ||
		len(stated.Project.Packages) != 1 || stated.Project.Packages[0] != "./cmd/..." {
		t.Fatalf("stated configuration = %+v", stated)
	}
}

func TestLoadRefusesAnAcceptanceThatIsNotTrimmed(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"id", "reason", "owner", "ticket"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			values := map[string]string{"id": "a", "reason": "because", "owner": "o", "ticket": "T-1"}
			values[field] = " " + values[field]
			document := "version = 1\n[[acceptance]]\nexpires = \"2030-01-01T00:00:00Z\"\n"
			for name, value := range values {
				document += name + " = \"" + value + "\"\n"
			}
			loaded, err := loadConfigText(t, document)
			if err == nil || !strings.Contains(err.Error(), "whitespace") {
				t.Fatalf("Load = (%+v, %v), want the untrimmed %s refused", loaded, err, field)
			}
		})
	}
}

func trimmedAcceptance() Acceptance {
	return Acceptance{
		ID: "abcd", Reason: "because", Owner: "owner", Ticket: "T-1",
		Expires: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestAddAcceptanceRefusesAFieldThatIsNotTrimmed(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*Acceptance)
	}{
		{name: "id", change: func(a *Acceptance) { a.ID = " " + a.ID }},
		{name: "reason", change: func(a *Acceptance) { a.Reason += " " }},
		{name: "owner", change: func(a *Acceptance) { a.Owner = " owner" }},
		{name: "ticket", change: func(a *Acceptance) { a.Ticket = "T-1 " }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			acceptance := trimmedAcceptance()
			test.change(&acceptance)
			err := AddAcceptance(t.TempDir(), acceptance)
			if err == nil || !strings.Contains(err.Error(), "leading or trailing whitespace") {
				t.Fatalf("AddAcceptance with an untrimmed %s = %v", test.name, err)
			}
		})
	}
}

func TestEnvironmentNamesAreRefusedOutsideTheirAlphabet(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		valid bool
	}{
		{name: "_", valid: true},
		{name: "A", valid: true},
		{name: "Z", valid: true},
		{name: "a", valid: true},
		{name: "z", valid: true},
		{name: "A0", valid: true},
		{name: "A9", valid: true},
		{name: "Az", valid: true},
		{name: ""},
		{name: "@"},
		{name: "["},
		{name: "`"},
		{name: "{"},
		{name: "0"},
		{name: "A/"},
		{name: "A:"},
	} {
		t.Run("name "+test.name, func(t *testing.T) {
			t.Parallel()
			if got := validEnvironmentName(test.name); got != test.valid {
				t.Fatalf("validEnvironmentName(%q) = %t, want %t", test.name, got, test.valid)
			}
		})
	}
}

func TestProjectExcludePatternsAreRefusedForWhatEachRuleNames(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		patterns []string
		message  string
	}{
		{name: "one good pattern", patterns: []string{"vendor/**"}},
		{name: "empty", patterns: []string{""}, message: "is invalid"},
		{name: "untrimmed", patterns: []string{" vendor"}, message: "is invalid"},
		{name: "absolute", patterns: []string{"/vendor"}, message: "is invalid"},
		{name: "a backslash", patterns: []string{`vendor\x`}, message: "is invalid"},
		{name: "a colon", patterns: []string{"vendor:x"}, message: "is invalid"},
		{name: "a zero byte", patterns: []string{"vendor\x00x"}, message: "is invalid"},
		{name: "a parent", patterns: []string{"../vendor"}, message: "escapes the project"},
		{name: "a parent in the middle", patterns: []string{"vendor/../x"}, message: "escapes the project"},
		{name: "a pattern nothing can match", patterns: []string{"vendor/["}, message: "is invalid"},
		{name: "the same pattern twice", patterns: []string{"vendor", "vendor"}, message: "is duplicated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateProjectExcludes(test.patterns)
			if test.message == "" {
				if err != nil {
					t.Fatalf("validateProjectExcludes(%q) = %v", test.patterns, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("validateProjectExcludes(%q) = %v, want %q", test.patterns, err, test.message)
			}
		})
	}
}

func TestLoadRefusesATestBinaryArgumentTheRunOwns(t *testing.T) {
	t.Parallel()
	loaded, err := loadConfigText(t, "version = 1\n[execution]\ntest_binary_args = [\"-test.run=X\"]\n")
	if err == nil || !strings.Contains(err.Error(), "goatest: execution: ") ||
		!strings.Contains(err.Error(), "assurance-owned flag") {
		t.Fatalf("Load = (%+v, %v), want the argument refused under its section", loaded, err)
	}
}

func TestAddAcceptanceWritesTheFirstOneWhereThereIsNoConfigurationYet(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := AddAcceptance(root, trimmedAcceptance()); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil || len(loaded.Acceptance) != 1 || loaded.Acceptance[0].ID != "abcd" {
		t.Fatalf("Load after the first acceptance = (%+v, %v)", loaded, err)
	}
}

func TestAddAcceptanceReportsAConfigurationItCannotWrite(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "absent", "deeper")
	err := AddAcceptance(root, trimmedAcceptance())
	if err == nil {
		t.Fatal("AddAcceptance under a directory that does not exist reported nothing")
	}
}

func TestAppendAcceptanceReportsAConfigurationItCannotRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, FileName), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	err := appendAcceptance(root, trimmedAcceptance())
	if err == nil || !strings.Contains(err.Error(), "goatest: read "+FileName) {
		t.Fatalf("appendAcceptance over a directory = %v", err)
	}
}

func TestAppendedAcceptanceIsSeparatedFromWhateverPrecededIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		existing string
		want     string
	}{
		{name: "a file that ends with a newline", existing: "version = 1\n", want: "version = 1\n\n[[acceptance]]"},
		{name: "a file that does not", existing: "version = 1", want: "version = 1\n\n[[acceptance]]"},
		{name: "a file with nothing in it", existing: "", want: "\n[[acceptance]]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, FileName)
			if err := os.WriteFile(path, []byte(test.existing), filemode.ReadableFile); err != nil {
				t.Fatal(err)
			}
			if err := appendAcceptance(root, trimmedAcceptance()); err != nil {
				t.Fatal(err)
			}
			stored, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(stored), test.want) {
				t.Fatalf("appended configuration = %q, want it to start %q", stored, test.want)
			}
		})
	}
}

func TestSaveWithNoHooksReachesTheRealFilesystem(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := saveWithHooks(root, defaults(), writeHooks{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, FileName)); err != nil {
		t.Fatalf("saveWithHooks left no configuration behind: %v", err)
	}
}
