// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const eventFieldCount = 3

const minimumEventFields = 2

func events(t *testing.T, rows ...string) string {
	t.Helper()
	var builder strings.Builder
	for _, row := range rows {
		fields := strings.Fields(row)
		if len(fields) < minimumEventFields {
			t.Fatalf("event row %q needs at least an action and a package", row)
		}
		builder.WriteString(`{"Action":"` + fields[0] + `","Package":"` + fields[1] + `"`)
		if len(fields) == eventFieldCount {
			builder.WriteString(`,"Test":"` + fields[2] + `"`)
		}
		builder.WriteString("}\n")
	}
	return builder.String()
}

func TestAuditCountsEveryTerminalVerdict(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t,
		"run example.test/a TestOne",
		"pass example.test/a TestOne",
		"run example.test/a TestTwo",
		"fail example.test/a TestTwo",
		"run example.test/b TestThree",
		"skip example.test/b TestThree",
	)))
	if err != nil {
		t.Fatal(err)
	}
	if summary.passed != 1 || summary.failed != 1 || summary.skipped != 1 {
		t.Fatalf("passed=%d failed=%d skipped=%d, want 1/1/1",
			summary.passed, summary.failed, summary.skipped)
	}
	const wantPackages = 2
	if len(summary.packages) != wantPackages {
		t.Fatalf("packages = %d, want %d", len(summary.packages), wantPackages)
	}
	if summary.packages[0].pkg != "example.test/a" {
		t.Errorf("packages are not sorted: %q first", summary.packages[0].pkg)
	}
}

func TestAuditSeesAPackageThatOnlySkipped(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t,
		"run example.test/quiet TestOne",
		"skip example.test/quiet TestOne",
		"run example.test/quiet TestTwo",
		"skip example.test/quiet TestTwo",
		"run example.test/loud TestThree",
		"pass example.test/loud TestThree",
	)))
	if err != nil {
		t.Fatal(err)
	}
	silent := summary.silentPackages()
	if !slices.Equal(silent, []string{"example.test/quiet"}) {
		t.Fatalf("silentPackages() = %q, want the quiet package alone", silent)
	}
}

func TestAuditDoesNotCallAPackageWithNoTestFilesSilent(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t, "skip example.test/empty")))
	if err != nil {
		t.Fatal(err)
	}
	if silent := summary.silentPackages(); len(silent) != 0 {
		t.Fatalf("silentPackages() = %q, want none", silent)
	}
	if summary.skipped != 0 {
		t.Fatalf("skipped = %d, want 0: a package-level skip is not a skipped test", summary.skipped)
	}
}

func TestAuditReportsOnlyTheSkipsTheLedgerDoesNotAllow(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t,
		"run example.test/a TestRecorded",
		"skip example.test/a TestRecorded",
		"run example.test/a TestUnrecorded",
		"skip example.test/a TestUnrecorded",
		"pass example.test/a TestRan",
	)))
	if err != nil {
		t.Fatal(err)
	}
	unrecorded := summary.unrecordedSkips([]string{"example.test/a TestRecorded"})
	if !slices.Equal(unrecorded, []string{"example.test/a TestUnrecorded"}) {
		t.Fatalf("unrecordedSkips() = %q, want the unrecorded one alone", unrecorded)
	}
}

func TestAuditChargesASkippedSubtestToItsParent(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t,
		"pass example.test/a TestParent",
		"skip example.test/a TestParent/one_case",
		"skip example.test/a TestParent/another_case",
	)))
	if err != nil {
		t.Fatal(err)
	}
	const wantSkipped = 2
	if summary.skipped != wantSkipped {
		t.Fatalf("skipped = %d, want %d: a skipped subtest is a test that did not run",
			summary.skipped, wantSkipped)
	}
	unrecorded := summary.unrecordedSkips([]string{"example.test/a TestParent"})
	if len(unrecorded) != 0 {
		t.Fatalf("unrecordedSkips() = %q, want none once the parent is recorded", unrecorded)
	}
}

func TestAuditRefusesAStreamItCannotRead(t *testing.T) {
	t.Parallel()
	if _, err := audit(strings.NewReader("ok  \texample.test/a\t0.01s\n")); err == nil {
		t.Fatal("a non-JSON line was accepted")
	}
	if _, err := audit(strings.NewReader("{\"Action\":\n")); err == nil {
		t.Fatal("a truncated JSON line was accepted")
	}
}

func TestReadLedgerReadsTheRecordFormat(t *testing.T) {
	t.Parallel()
	path := writeLedger(t, "# a comment\n\nexample.test/a TestOne\nexample.test/b TestTwo\n")
	allowed, err := readLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.test/a TestOne", "example.test/b TestTwo"}
	if !slices.Equal(allowed, want) {
		t.Fatalf("readLedger() = %q, want %q", allowed, want)
	}
}

func TestReadLedgerRejectsAMalformedEntry(t *testing.T) {
	t.Parallel()
	path := writeLedger(t, "# a comment\n\nexample.test/a\n")
	_, err := readLedger(path)
	if err == nil || !strings.Contains(err.Error(), path+":3:") {
		t.Fatalf("readLedger = %v, want the third line named", err)
	}
}

func TestReadLedgerTreatsAMissingRecordAsEmpty(t *testing.T) {
	t.Parallel()
	allowed, err := readLedger(filepath.Join(t.TempDir(), "absent.txt"))
	if err != nil {
		t.Fatalf("a missing ledger was an error: %v", err)
	}
	if len(allowed) != 0 {
		t.Fatalf("readLedger() = %q, want none", allowed)
	}
}

func writeLedger(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ledgerName)
	if err := os.WriteFile(path, []byte(contents), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuditRefusesARunThatWasNarrowedBeforeItStarted(t *testing.T) {
	t.Parallel()
	stream := `{"Action":"output","Package":"example.test/a","Output":"` +
		NarrowedFilterMarker + `: GOATEST_INTERNAL_ASSURE_TEST_RUN=\"^TestOne$\"\n"}` + "\n" +
		events(t, "pass example.test/a TestOne")
	summary, err := audit(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	narrowed := summary.narrowedPackages()
	if len(narrowed) != 1 {
		t.Fatalf("narrowedPackages() = %q, want the one announcement", narrowed)
	}
	if !strings.HasPrefix(narrowed[0], "example.test/a: ") {
		t.Errorf("narrowedPackages() = %q, want it charged to its package", narrowed[0])
	}
	if summary.passed != 1 {
		t.Errorf("passed = %d, want 1: the marker must not disturb the counts", summary.passed)
	}
}

func TestAuditIgnoresOrdinaryPackageOutput(t *testing.T) {
	t.Parallel()
	stream := `{"Action":"output","Package":"example.test/a","Output":"ok  \texample.test/a\t0.01s\n"}` + "\n"
	summary, err := audit(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if narrowed := summary.narrowedPackages(); len(narrowed) != 0 {
		t.Fatalf("narrowedPackages() = %q, want none", narrowed)
	}
}

func TestTheNarrowedFilterMarkerIsSpeltTheSameInBothPlaces(t *testing.T) {
	t.Parallel()
	const printer = "../../assure/main_test.go"
	source, err := os.ReadFile(printer)
	if err != nil {
		t.Fatalf("read %s: %v", printer, err)
	}
	quoted := strconv.Quote(NarrowedFilterMarker)
	if !strings.Contains(string(source), quoted) {
		t.Fatalf("%s does not declare the marker as %s.\n\n"+
			"The two spellings have drifted, so a narrowed run would announce itself\n"+
			"in words this tool no longer recognises.", printer, quoted)
	}
}

func TestAuditNamesTheLineItRefused(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		stream  string
		message string
	}{
		{name: "the first line", stream: "not json\n", message: "line 1 is not JSON"},
		{name: "a later line", stream: "\n{\"Action\":\"pass\",\"Package\":\"p\"}\nnot json\n", message: "line 3 is not JSON"},
		{name: "a line that is not an event", stream: "{\"Action\":1}\n", message: "decode test event line 1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			summary, err := audit(strings.NewReader(test.stream))
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("audit = (%+v, %v), want %q", summary, err, test.message)
			}
		})
	}
}

func TestAuditIgnoresAnEventThatNamesNoPackage(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader("{\"Action\":\"pass\",\"Test\":\"TestOne\"}\n"))
	if err != nil || len(summary.packages) != 0 || summary.passed != 0 {
		t.Fatalf("audit of an event with no package = (%+v, %v)", summary, err)
	}
}

func TestPackagesAreSummarisedInTheOrderOfTheirNames(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t,
		"pass example/z TestOne",
		"pass example/a TestOne",
		"pass example/m TestOne",
	)))
	if err != nil || len(summary.packages) != 3 {
		t.Fatalf("audit = (%+v, %v)", summary, err)
	}
	names := []string{summary.packages[0].pkg, summary.packages[1].pkg, summary.packages[2].pkg}
	if !slices.Equal(names, []string{"example/a", "example/m", "example/z"}) {
		t.Fatalf("package order = %v", names)
	}
}

func TestSilentPackagesAreExactlyThoseThatOnlySkipped(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		stream     func(t *testing.T) string
		wantSilent bool
	}{
		{
			name:       "every test stepped aside",
			stream:     func(t *testing.T) string { return events(t, "skip example/a TestOne") },
			wantSilent: true,
		},
		{
			name:   "one passed beside the skip",
			stream: func(t *testing.T) string { return events(t, "skip example/a TestOne", "pass example/a TestTwo") },
		},
		{
			name:   "one failed beside the skip",
			stream: func(t *testing.T) string { return events(t, "skip example/a TestOne", "fail example/a TestTwo") },
		},
		{
			name:   "the package holds no test files",
			stream: func(t *testing.T) string { return events(t, "skip example/a") },
		},
		{
			name:   "nothing was skipped at all",
			stream: func(t *testing.T) string { return events(t, "pass example/a TestOne") },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			summary, err := audit(strings.NewReader(test.stream(t)))
			if err != nil {
				t.Fatal(err)
			}
			if silent := len(summary.silentPackages()) != 0; silent != test.wantSilent {
				t.Fatalf("silent = %t, want %t (%+v)", silent, test.wantSilent, summary.packages)
			}
		})
	}
}

func TestRenderNamesTheSkipsOfEveryPackageThatHasThem(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t,
		"skip example/a TestOne",
		"pass example/b TestTwo",
	)))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	render(&out, summary)
	if !strings.Contains(out.String(), "example/a: 1 skipped (TestOne)") {
		t.Fatalf("render wrote %q", out.String())
	}
	if strings.Contains(out.String(), "example/b: ") {
		t.Fatalf("render named a package that skipped nothing: %q", out.String())
	}
}

func TestAPackageWithNoTestFilesIsNotSilentHoweverManyTestsSteppedAside(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(events(t, "skip example/a", "skip example/a TestOne")))
	if err != nil {
		t.Fatal(err)
	}
	if silent := summary.silentPackages(); len(silent) != 0 {
		t.Fatalf("silent packages = %v, want a package with no test files left alone", silent)
	}
}

func TestAPackageThatReachedNoVerdictAtAllIsNotCalledSilent(t *testing.T) {
	t.Parallel()
	summary, err := audit(strings.NewReader(`{"Action":"output","Package":"example/a","Output":"building\n"}` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if silent := summary.silentPackages(); len(silent) != 0 {
		t.Fatalf("silent packages = %v, want a package that skipped nothing left alone", silent)
	}
}

func TestAuditRefusesALineLongerThanItWillHold(t *testing.T) {
	t.Parallel()
	long := `{"Action":"output","Package":"example/a","Output":"` + strings.Repeat("x", maximumLineBuffer+1) + `"}` + "\n"
	summary, err := audit(strings.NewReader(long))
	if err == nil || !strings.Contains(err.Error(), "read the test event stream") {
		t.Fatalf("audit of a line it cannot hold = (%+v, %v)", summary, err)
	}
}
