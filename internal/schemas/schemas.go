// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package schemas

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/P4suta/go-mutants/schema"
)

const (
	CatalogV1 = "go-mutants/catalog"

	RunReportV1 = "go-mutants/run-report"

	DoctorV1 = "go-mutants/doctor"

	TraceEventV1 = "go-mutants/trace-event"

	ExplainV1 = "go-mutants/explain"

	DiagnosticsV1 = "go-mutants/diagnostics"

	WorkspaceReportV1 = "go-mutants/workspace-report"
)

var registry = map[string]string{
	CatalogV1:     "catalog-v1.schema.json",
	DiagnosticsV1: "diagnostics-v1.schema.json",
	ExplainV1:     "explain-v1.schema.json",
	DoctorV1:      "doctor-v1.schema.json",
	RunReportV1:   "run-report-v1.schema.json",
	TraceEventV1:  "trace-v1.schema.json",

	WorkspaceReportV1: "workspace-report-v1.schema.json",
}

const baseURL = "https://go-mutants.invalid/schema/"

var (
	compileOnce sync.Once
	compiled    map[string]*jsonschema.Schema
	compileErr  error
)

func DocumentTypes() []string {
	types := make([]string, 0, len(registry))
	for documentType := range registry {
		types = append(types, documentType)
	}
	slices.Sort(types)
	return types
}

func Validate(documentType string, doc []byte) error {
	sch, err := schemaFor(documentType)
	if err != nil {
		return err
	}
	instance, decodeErr := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if decodeErr != nil {
		return &Error{
			Code:         CodeMalformedJSON,
			DocumentType: documentType,
			Message:      fmt.Sprintf("%s document is not JSON: %v", documentType, decodeErr),
			Err:          decodeErr,
		}
	}
	if validateErr := sch.Validate(instance); validateErr != nil {
		return invalidDocument(documentType, validateErr)
	}
	return nil
}

func schemaFor(documentType string) (*jsonschema.Schema, error) {
	if _, known := registry[documentType]; !known {
		return nil, &Error{
			Code:         CodeUnknownDocument,
			DocumentType: documentType,
			Message: fmt.Sprintf("unknown document type %q; this build validates %s",
				documentType, strings.Join(DocumentTypes(), ", ")),
		}
	}
	compileOnce.Do(compileAll)
	if compileErr != nil {
		return nil, compileErr
	}
	sch, ok := compiled[documentType]
	if !ok {
		return nil, &Error{
			Code:         CodeSchemaUnusable,
			DocumentType: documentType,
			Message:      fmt.Sprintf("no schema was compiled for %q", documentType),
		}
	}
	return sch, nil
}

func compileAll() {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)

	urls := make(map[string]string, len(registry))
	for _, file := range registeredFiles() {
		data, readErr := schema.FS.ReadFile(file)
		if readErr != nil {
			compileErr = unusable(file, "cannot be read", readErr)
			return
		}
		doc, decodeErr := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if decodeErr != nil {
			compileErr = unusable(file, "is not JSON", decodeErr)
			return
		}
		url := resourceURL(file, doc)
		if addErr := c.AddResource(url, doc); addErr != nil {
			compileErr = unusable(file, "cannot be registered as "+url, addErr)
			return
		}
		urls[file] = url
	}

	out := make(map[string]*jsonschema.Schema, len(registry))
	for _, documentType := range DocumentTypes() {
		file := registry[documentType]
		sch, err := c.Compile(urls[file])
		if err != nil {
			compileErr = unusable(file, "does not compile", err)
			return
		}
		out[documentType] = sch
	}
	compiled = out
}

func registeredFiles() []string {
	files := make([]string, 0, len(registry))
	for _, file := range registry {
		if !slices.Contains(files, file) {
			files = append(files, file)
		}
	}
	slices.Sort(files)
	return files
}

func resourceURL(file string, doc any) string {
	if obj, ok := doc.(map[string]any); ok {
		if id, ok := obj["$id"].(string); ok && id != "" {
			return id
		}
	}
	return baseURL + file
}

func unusable(file, problem string, cause error) error {
	return &Error{
		Code:    CodeSchemaUnusable,
		Message: fmt.Sprintf("embedded schema %s %s: %v", file, problem, cause),
		Err:     cause,
	}
}

func invalidDocument(documentType string, cause error) error {
	var failure *jsonschema.ValidationError
	if !errors.As(cause, &failure) {
		return &Error{
			Code:         CodeInvalidDocument,
			DocumentType: documentType,
			Message:      fmt.Sprintf("%s document is not valid: %v", documentType, cause),
			Err:          cause,
		}
	}
	first := firstViolation(failure)
	return &Error{
		Code:         CodeInvalidDocument,
		DocumentType: documentType,
		Pointer:      first.pointer,
		Message: fmt.Sprintf("%s document is not valid at %s: %s",
			documentType, displayPointer(first.pointer), first.detail),
		Err: cause,
	}
}

type violation struct {
	pointer string
	keyword string
	detail  string
}

func firstViolation(failure *jsonschema.ValidationError) violation {
	var leaves []violation
	collectViolations(failure, &leaves)
	if len(leaves) == 0 {
		return describe(failure)
	}
	slices.SortFunc(leaves, func(x, y violation) int {
		if c := strings.Compare(x.pointer, y.pointer); c != 0 {
			return c
		}
		if c := strings.Compare(x.keyword, y.keyword); c != 0 {
			return c
		}
		return strings.Compare(x.detail, y.detail)
	})
	return leaves[0]
}

func collectViolations(failure *jsonschema.ValidationError, out *[]violation) {
	if len(failure.Causes) == 0 {
		*out = append(*out, describe(failure))
		return
	}
	for _, cause := range failure.Causes {
		collectViolations(cause, out)
	}
}

func describe(failure *jsonschema.ValidationError) violation {
	pointer := jsonPointer(failure.InstanceLocation)
	v := violation{pointer: pointer, detail: detailOf(failure, pointer)}
	if failure.ErrorKind == nil {
		return v
	}
	v.keyword = jsonPointer(failure.ErrorKind.KeywordPath())
	switch k := failure.ErrorKind.(type) {
	case *kind.Required:
		if name, ok := firstName(k.Missing); ok {
			v.pointer += "/" + escapeToken(name)
		}
	case *kind.AdditionalProperties:
		if name, ok := firstName(k.Properties); ok {
			v.pointer += "/" + escapeToken(name)
		}
	}
	return v
}

func firstName(names []string) (string, bool) {
	if len(names) == 0 {
		return "", false
	}
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	return sorted[0], true
}

func detailOf(failure *jsonschema.ValidationError, pointer string) string {
	return strings.TrimPrefix(failure.Error(), "at "+quoteLocation(pointer)+": ")
}

func jsonPointer(tokens []string) string {
	var b strings.Builder
	for _, token := range tokens {
		b.WriteByte('/')
		b.WriteString(escapeToken(token))
	}
	return b.String()
}

func escapeToken(token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	return strings.ReplaceAll(token, "/", "~1")
}

func quoteLocation(s string) string {
	q := fmt.Sprintf("%q", s)
	q = strings.ReplaceAll(q, `\"`, `"`)
	q = strings.ReplaceAll(q, `'`, `\'`)
	return "'" + q[1:len(q)-1] + "'"
}

func displayPointer(pointer string) string {
	if pointer == "" {
		return "the document root"
	}
	return pointer
}
