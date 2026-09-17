// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/P4suta/go-mutants/internal/report"
)

const (
	sameAsTheWholeRun  = "the value the unsharded run would have written"
	identityOfTheMerge = "the merged document's own identity"
)

var mergedFields = map[string]string{
	"DocumentType":  sameAsTheWholeRun,
	"SchemaVersion": sameAsTheWholeRun,
	"ToolVersion":   sameAsTheWholeRun,
	"RunID":         identityOfTheMerge,
	"Merge":         identityOfTheMerge,
	"Status":        sameAsTheWholeRun,
	"StartedAt":     sameAsTheWholeRun,
	"FinishedAt":    sameAsTheWholeRun,
	"DurationMS":    sameAsTheWholeRun,

	"Workspace.ModulePath":             sameAsTheWholeRun,
	"Workspace.GoVersion":              sameAsTheWholeRun,
	"Workspace.WorkspaceDigest":        sameAsTheWholeRun,
	"Workspace.Platform.OS":            sameAsTheWholeRun,
	"Workspace.Platform.Arch":          sameAsTheWholeRun,
	"Selection.Mode":                   sameAsTheWholeRun,
	"Selection.ChangedRef":             sameAsTheWholeRun,
	"Selection.Profile":                sameAsTheWholeRun,
	"Selection.Operators":              sameAsTheWholeRun,
	"Selection.Include":                sameAsTheWholeRun,
	"Selection.Exclude":                sameAsTheWholeRun,
	"Selection.Candidates":             sameAsTheWholeRun,
	"Selection.Rejected":               sameAsTheWholeRun,
	"Selection.Selected":               sameAsTheWholeRun,
	"Test.Command":                     sameAsTheWholeRun,
	"Test.Baseline.Runs":               sameAsTheWholeRun,
	"Test.Baseline.DurationsMS":        sameAsTheWholeRun,
	"Test.Baseline.SlowestMS":          sameAsTheWholeRun,
	"Test.TimeoutMS":                   sameAsTheWholeRun,
	"Test.TimeoutSource":               sameAsTheWholeRun,
	"Test.MemoryBytes":                 sameAsTheWholeRun,
	"Test.MemorySource":                sameAsTheWholeRun,
	"Coverage.Mode":                    sameAsTheWholeRun,
	"Coverage.Binaries":                sameAsTheWholeRun,
	"Coverage.Tests":                   sameAsTheWholeRun,
	"Coverage.MutantsUncovered":        sameAsTheWholeRun,
	"Cache.Mode":                       sameAsTheWholeRun,
	"Cache.Hits":                       sameAsTheWholeRun,
	"Cache.Misses":                     sameAsTheWholeRun,
	"Cache.Writes":                     sameAsTheWholeRun,
	"Summary.Total":                    sameAsTheWholeRun,
	"Summary.Killed":                   sameAsTheWholeRun,
	"Summary.Survived":                 sameAsTheWholeRun,
	"Summary.TimedOut":                 sameAsTheWholeRun,
	"Summary.Inconclusive":             sameAsTheWholeRun,
	"Summary.Errored":                  sameAsTheWholeRun,
	"Summary.NotRun":                   sameAsTheWholeRun,
	"Summary.ScorePercent":             sameAsTheWholeRun,
	"Summary.Policy.Strict":            sameAsTheWholeRun,
	"Summary.Policy.MinimumScore":      sameAsTheWholeRun,
	"Summary.Policy.RequireMutants":    sameAsTheWholeRun,
	"Summary.Policy.Failure":           sameAsTheWholeRun,
	"Mutants[].ID":                     sameAsTheWholeRun,
	"Mutants[].DisplayID":              sameAsTheWholeRun,
	"Mutants[].Path":                   sameAsTheWholeRun,
	"Mutants[].Package":                sameAsTheWholeRun,
	"Mutants[].Family":                 sameAsTheWholeRun,
	"Mutants[].Rule":                   sameAsTheWholeRun,
	"Mutants[].RuleVersion":            sameAsTheWholeRun,
	"Mutants[].Line":                   sameAsTheWholeRun,
	"Mutants[].Column":                 sameAsTheWholeRun,
	"Mutants[].StartByte":              sameAsTheWholeRun,
	"Mutants[].EndByte":                sameAsTheWholeRun,
	"Mutants[].Original":               sameAsTheWholeRun,
	"Mutants[].Replacement":            sameAsTheWholeRun,
	"Mutants[].Branch.Direction":       sameAsTheWholeRun,
	"Mutants[].Branch.BodyStartLine":   sameAsTheWholeRun,
	"Mutants[].Branch.BodyStartColumn": sameAsTheWholeRun,
	"Mutants[].Branch.BodyEndLine":     sameAsTheWholeRun,
	"Mutants[].Branch.BodyEndColumn":   sameAsTheWholeRun,
	"Mutants[].Outcome":                sameAsTheWholeRun,
	"Mutants[].Diverged": "a counted loop went past what the original program does in this tree " +
		"under this suite, which is the same two counts in every shard",
	"Mutants[].NotRunReason":            sameAsTheWholeRun,
	"Mutants[].DurationMS":              sameAsTheWholeRun,
	"Mutants[].KilledBy":                sameAsTheWholeRun,
	"Mutants[].Attempts":                sameAsTheWholeRun,
	"Mutants[].OutputTail":              sameAsTheWholeRun,
	"Mutants[].CoveringTestPackages":    sameAsTheWholeRun,
	"Mutants[].CoveringTests[].Package": sameAsTheWholeRun,
	"Mutants[].CoveringTests[].Name":    sameAsTheWholeRun,
	"Mutants[].Uncovered":               sameAsTheWholeRun,
	"Mutants[].Unobserved":              sameAsTheWholeRun,
	"Mutants[].Cached":                  sameAsTheWholeRun,
	"Rejected[].ID":                     sameAsTheWholeRun,
	"Rejected[].DisplayID":              sameAsTheWholeRun,
	"Rejected[].Path":                   sameAsTheWholeRun,
	"Rejected[].Line":                   sameAsTheWholeRun,
	"Rejected[].Column":                 sameAsTheWholeRun,
	"Rejected[].Rule":                   sameAsTheWholeRun,
	"Rejected[].Diagnostic":             sameAsTheWholeRun,
	"Skips[].Path":                      sameAsTheWholeRun,
	"Skips[].Reason":                    sameAsTheWholeRun,
	"Skips[].Count":                     sameAsTheWholeRun,
	"Expectations[].ID":                 sameAsTheWholeRun,
	"Expectations[].Reason":             sameAsTheWholeRun,
	"Expectations[].State":              sameAsTheWholeRun,
	"Warnings[].Code":                   sameAsTheWholeRun,
	"Warnings[].Message":                sameAsTheWholeRun,
}

var droppedFields = map[string]string{
	"Shard":                      "a merged document is the whole run and no shard of it",
	"Timing":                     "four shards are four timelines on four machines",
	"Validation":                 "each shard compile-validated the catalogue itself, and paid its own bisection for it",
	"Workspace.Snapshot":         "each shard copied the tree into its own temporary directory",
	"Test.Toolchain":             "each shard ran the tests with the `go` its own machine located",
	"Test.ResolvedCommand":       "the same, one argv per shard: the command is merged, the executable it resolved to is not",
	"Coverage.UnavailableReason": "whether the instrumented binaries compiled is one machine's answer",
	"Coverage.BuildFallback":     "the same event as the reason above, and the same one machine",
	"Mutants[].Executions":       "a worker number and a duration describe the machine the mutant ran on",
	"Mutants[].PeakMemoryBytes":  "what a mutant cost is what it cost on the machine that ran it, and a merge describes no machine",
	"Mutants[].MemoryExceeded":   "the bound that settled it was that shard's, and a merged document reports no bound",
}

func TestEveryFieldOfAReportIsMergedOrDropped(t *testing.T) {
	t.Parallel()

	for path := range droppedFields {
		if _, both := mergedFields[path]; both {
			t.Errorf("%s is listed as both merged and dropped", path)
		}
	}
	sites := map[string]fieldSite{}
	walkFields(t, reflect.TypeFor[report.Report](), "", sites)
	for path := range mergedFields {
		if _, walked := sites[path]; !walked {
			t.Errorf("mergedFields names %s, which is not a field of a run report any more", path)
		}
	}
	for path := range droppedFields {
		if _, walked := sites[path]; !walked {
			t.Errorf("droppedFields names %s, which is not a field of a run report any more", path)
		}
	}
}

type fieldSite struct {
	owner reflect.Type
	name  string
}

func walkFields(t *testing.T, typ reflect.Type, prefix string, sites map[string]fieldSite) {
	t.Helper()
	for i := range typ.NumField() {
		field := typ.Field(i)
		path := prefix + field.Name
		_, merged := mergedFields[path]
		_, dropped := droppedFields[path]
		if merged || dropped {
			sites[path] = fieldSite{owner: typ, name: field.Name}
			continue
		}
		inner := field.Type
		separator := "."
		for inner.Kind() == reflect.Pointer || inner.Kind() == reflect.Slice {
			if inner.Kind() == reflect.Slice {
				separator = "[]."
			}
			inner = inner.Elem()
		}
		if inner.Kind() == reflect.Struct {
			walkFields(t, inner, path+separator, sites)
			continue
		}
		t.Errorf("%s is in neither mergedFields nor droppedFields: decide what a merge of several runs should say about it",
			path)
	}
}

func TestAMergedDocumentSaysNothingAboutEveryDroppedField(t *testing.T) {
	t.Parallel()

	merged := mergeShards(t, shards(t, 3))
	for path := range droppedFields {
		values := valuesAt(t, reflect.ValueOf(*merged), path)
		if len(values) == 0 {
			t.Errorf("%s could not be resolved in a merged document", path)
			continue
		}
		for _, value := range values {
			if !value.IsZero() {
				t.Errorf("the merged document reports %s = %v, which describes one shard's run rather than the merge",
					path, value.Interface())
			}
		}
	}
}

func valuesAt(t *testing.T, value reflect.Value, path string) []reflect.Value {
	t.Helper()
	values := []reflect.Value{value}
	for _, segment := range strings.Split(path, ".") {
		name, slice := strings.CutSuffix(segment, "[]")
		next := make([]reflect.Value, 0, len(values))
		for _, current := range values {
			for current.Kind() == reflect.Pointer {
				if current.IsNil() {
					return nil
				}
				current = current.Elem()
			}
			field := current.FieldByName(name)
			if !field.IsValid() {
				t.Errorf("%s names %s, which no struct in a run report has", path, name)
				return nil
			}
			if !slice {
				next = append(next, field)
				continue
			}
			for i := range field.Len() {
				next = append(next, field.Index(i))
			}
		}
		values = next
	}
	return values
}

func mergeExemptOptions(t *testing.T) []cmp.Option {
	t.Helper()
	sites := map[string]fieldSite{}
	walkFields(t, reflect.TypeFor[report.Report](), "", sites)

	options := make([]cmp.Option, 0, len(sites))
	for path, site := range sites {
		if mergedFields[path] == sameAsTheWholeRun {
			continue
		}
		options = append(options, cmpopts.IgnoreFields(reflect.New(site.owner).Elem().Interface(), site.name))
	}
	return options
}
