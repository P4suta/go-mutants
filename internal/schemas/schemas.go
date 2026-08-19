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

// The document types this package validates. They are the values of the
// `document_type` field, and a consumer branches on them before decoding
// anything else.
const (
	// CatalogV1 is the `list --json` catalogue: mutants and static skips, with
	// no outcomes and no run.
	CatalogV1 = "go-mutants/catalog"
)

// registry maps a document type onto the schema file in [schema.FS] that
// defines it.
//
// This is the whole extension point. Adding run-report-v1 or doctor-v1 is one
// file in schema/ and one line here; nothing else in this package knows how
// many schemas there are or what they contain.
var registry = map[string]string{
	CatalogV1: "catalog-v1.schema.json",
}

// baseURL is the identity a schema gets when its file declares no "$id".
//
// The host is deliberately unresolvable. These identifiers are names, not
// addresses: nothing ever dereferences one, and a name that cannot be fetched
// even by accident is a name that can never turn a validation into a network
// request. A vendored third-party schema keeps its own "$id" instead, which is
// why this is a fallback rather than a rule.
const baseURL = "https://go-mutants.invalid/schema/"

// compiled holds the compiled schemas by document type, or compileErr holds
// the reason there are none. Exactly one of them is set once compileOnce has
// run.
var (
	compileOnce sync.Once
	compiled    map[string]*jsonschema.Schema
	compileErr  error
)

// DocumentTypes returns every document type this build can validate, sorted.
func DocumentTypes() []string {
	types := make([]string, 0, len(registry))
	for documentType := range registry {
		types = append(types, documentType)
	}
	slices.Sort(types)
	return types
}

// Validate reports whether doc satisfies the schema for documentType.
//
// It returns nil when the document is valid, and otherwise an [*Error] whose
// [Code] says what kind of failure it was and whose [Error.Pointer] locates the
// first violation. An unknown documentType is a failure, never a silent pass;
// see the package documentation.
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

// schemaFor returns the compiled schema for a document type, compiling every
// registered schema on the first call.
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
		// Unreachable while compileAll compiles every registered type, which
		// the package tests assert. Reported rather than dereferenced, because
		// a nil schema would panic inside the validator with no clue as to why.
		return nil, &Error{
			Code:         CodeSchemaUnusable,
			DocumentType: documentType,
			Message:      fmt.Sprintf("no schema was compiled for %q", documentType),
		}
	}
	return sch, nil
}

// compileAll reads and compiles every registered schema, setting either
// compiled or compileErr. It runs at most once per process.
//
// Every schema is registered with the compiler before any of them is compiled,
// so that a $ref from one document type to another resolves against the
// embedded copy instead of being looked up. No URL loader is installed, so
// there is nothing to look it up with.
func compileAll() {
	c := jsonschema.NewCompiler()
	// Every schema in this repository declares "$schema", so the default only
	// matters for a vendored schema that does not. 2020-12 is the dialect the
	// project writes in, and guessing an older draft for a schema that omits
	// the keyword would silently change what "additionalProperties" means.
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

// registeredFiles returns the distinct schema files the registry names, sorted
// so that compilation order does not depend on map iteration.
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

// resourceURL is the identity a schema is registered and compiled under: its
// own "$id" when it declares one, and otherwise a name derived from the file.
func resourceURL(file string, doc any) string {
	if obj, ok := doc.(map[string]any); ok {
		if id, ok := obj["$id"].(string); ok && id != "" {
			return id
		}
	}
	return baseURL + file
}

// unusable builds the error for an embedded schema that cannot be used.
func unusable(file, problem string, cause error) error {
	return &Error{
		Code:    CodeSchemaUnusable,
		Message: fmt.Sprintf("embedded schema %s %s: %v", file, problem, cause),
		Err:     cause,
	}
}

// invalidDocument turns the validator's error tree into a single located
// failure.
func invalidDocument(documentType string, cause error) error {
	var failure *jsonschema.ValidationError
	if !errors.As(cause, &failure) {
		// The validator only ever returns *ValidationError, but an error is a
		// poor place to assume anything: report it whole rather than lose it.
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

// A violation is one leaf of the validator's error tree, reduced to the three
// strings that identify it.
type violation struct {
	// pointer locates the offending value in the instance.
	pointer string
	// keyword locates the schema keyword that rejected it, as a JSON pointer
	// fragment such as "/required". It is a tiebreak, not output.
	keyword string
	// detail is the validator's own one-line complaint, with its "at '...':"
	// prefix removed because the pointer is reported separately.
	detail string
}

// firstViolation picks the violation to report.
//
// "First" is a choice this package makes, not one the validator offers. Its
// error tree branches in map iteration order, so the leaves arrive shuffled;
// sorting them by (pointer, keyword, detail) imposes a total order on a set
// that is itself deterministic, which makes the reported location a function of
// the document alone.
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

// collectViolations appends every leaf of the error tree to out. Interior
// nodes are the structural keywords — $ref, properties, items — that merely
// say where the failure happened; the leaves are the failures.
func collectViolations(failure *jsonschema.ValidationError, out *[]violation) {
	if len(failure.Causes) == 0 {
		*out = append(*out, describe(failure))
		return
	}
	for _, cause := range failure.Causes {
		collectViolations(cause, out)
	}
}

// describe reduces one node of the error tree to a [violation].
//
// A missing or unexpected property is located at the property itself rather
// than at the object that lacks or carries it. The validator reports both
// against the parent, which is correct but useless: the answer to "what is
// wrong with this catalogue" should be "/mutants/0/line", not "/mutants/0".
// Where several properties are missing or unexpected at once, the
// lexicographically first is named, so that the answer does not depend on the
// order a map happened to be iterated in.
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

// firstName returns the lexicographically first of a set of property names.
func firstName(names []string) (string, bool) {
	if len(names) == 0 {
		return "", false
	}
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	return sorted[0], true
}

// detailOf returns the validator's complaint without the location it prints in
// front of it, since the location is reported as a pointer instead.
//
// The prefix is reconstructed rather than searched for: an instance location
// can contain any character a JSON property name can, quote marks and colons
// included, so scanning for the separator would truncate the wrong message on
// exactly the documents that are hardest to debug. A prefix that does not match
// — because the validator changed how it prints — leaves the text untouched.
func detailOf(failure *jsonschema.ValidationError, pointer string) string {
	return strings.TrimPrefix(failure.Error(), "at "+quoteLocation(pointer)+": ")
}

// jsonPointer renders RFC 6901 tokens as a pointer. The empty token list is
// the empty pointer, which names the whole document.
func jsonPointer(tokens []string) string {
	var b strings.Builder
	for _, token := range tokens {
		b.WriteByte('/')
		b.WriteString(escapeToken(token))
	}
	return b.String()
}

// escapeToken applies the RFC 6901 escapes, in the order the RFC requires:
// '~' first, so that a '/' escaped to "~1" is not escaped again.
func escapeToken(token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	return strings.ReplaceAll(token, "/", "~1")
}

// quoteLocation reproduces how the validator quotes an instance location, so
// that [detailOf] can strip exactly the prefix the validator wrote.
func quoteLocation(s string) string {
	q := fmt.Sprintf("%q", s)
	q = strings.ReplaceAll(q, `\"`, `"`)
	q = strings.ReplaceAll(q, `'`, `\'`)
	return "'" + q[1:len(q)-1] + "'"
}

// displayPointer renders a pointer for a human. The empty pointer is a real
// location — the document itself — and printing nothing for it would produce
// an error message with a hole in it.
func displayPointer(pointer string) string {
	if pointer == "" {
		return "the document root"
	}
	return pointer
}
