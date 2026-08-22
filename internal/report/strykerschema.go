// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/P4suta/go-mutants/schema/stryker"
)

// This file is the gate the projection has to pass before it is written.
//
// # Why validate a document this package composed itself
//
// The run report is validated by internal/schemas, which this package
// deliberately does not import: the dependency would run from a document to its
// own validator, and the point of a published schema is that somebody else
// checks the document against it. The projection is the other case entirely.
//
// It is a translation into a format go-mutants does not own. The schema is the
// only written statement of what that format requires, the requirements are not
// obvious — `schemaVersion` is a *string* matching a pattern that refuses "3",
// a location's columns are counted in UTF-16 code units, `mutants` must be an
// array and never null — and every one of those mistakes produces a file the
// viewer renders as an error page or, worse, renders wrongly. So the vendored
// schema is compiled in-process and the document is checked against it before
// the bytes reach the disk, and a failure aborts the write.
//
// The house rule behind that is worth stating plainly: **never emit an
// authoritative-looking document that does not validate**. A `mutation.json`
// somebody's dashboard rejects is a worse outcome than no `mutation.json` at
// all, because the second is a missing file and the first is a lie about a
// format.
//
// The schema is compiled from the embedded copy with no URL loader installed,
// so validation reads no files and makes no network requests — including for
// the draft-07 meta-schema it declares, which the validator carries.

// strykerSchema holds the compiled vendored schema, or strykerSchemaErr holds
// the reason there is none. Exactly one is set once strykerSchemaOnce has run.
var (
	strykerSchemaOnce sync.Once
	strykerSchema     *jsonschema.Schema
	strykerSchemaErr  error
)

// ValidateProjection reports whether doc satisfies the vendored
// mutation-testing-report schema.
//
// It takes the encoded bytes rather than a [*Projection] because the bytes are
// what gets written and what a consumer will read: validating the struct would
// check this package's idea of the document, and any disagreement between the
// struct and its JSON encoding — an omitempty that should not have been there,
// a nil slice encoding as null — would be exactly the disagreement that slipped
// through.
func ValidateProjection(doc []byte) error {
	sch, err := compiledStrykerSchema()
	if err != nil {
		return err
	}
	instance, decodeErr := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if decodeErr != nil {
		return &Error{
			Code:    CodeProjectionInvalid,
			Message: "the mutation-testing-report projection is not JSON",
			Err:     decodeErr,
		}
	}
	if validateErr := sch.Validate(instance); validateErr != nil {
		return &Error{
			Code: CodeProjectionInvalid,
			Message: "the mutation-testing-report projection does not satisfy the vendored " +
				stryker.Package + " " + stryker.PackageVersion + " schema at " + firstFailure(validateErr) +
				"; nothing was written, because a document that does not validate is worse than no document",
			Err: validateErr,
		}
	}
	return nil
}

// compiledStrykerSchema compiles the vendored schema once per process.
func compiledStrykerSchema() (*jsonschema.Schema, error) {
	strykerSchemaOnce.Do(func() {
		c := jsonschema.NewCompiler()
		// No DefaultDraft is set, on purpose. The vendored file declares
		// draft-07 in its own "$schema", and forcing 2020-12 on it would
		// silently change what "definitions" and "additionalProperties" mean
		// in somebody else's document. A vendored schema is honoured as
		// written or it is not vendored at all.
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(stryker.Schema()))
		if err != nil {
			strykerSchemaErr = unusableStrykerSchema("is not JSON", err)
			return
		}
		if err = c.AddResource(stryker.SchemaID, doc); err != nil {
			strykerSchemaErr = unusableStrykerSchema("cannot be registered as "+stryker.SchemaID, err)
			return
		}
		sch, err := c.Compile(stryker.SchemaID)
		if err != nil {
			strykerSchemaErr = unusableStrykerSchema("does not compile", err)
			return
		}
		strykerSchema = sch
	})
	if strykerSchemaErr != nil {
		return nil, strykerSchemaErr
	}
	return strykerSchema, nil
}

// unusableStrykerSchema builds the error for a vendored schema that cannot be
// used at all, which is a broken build rather than a bad document.
func unusableStrykerSchema(problem string, cause error) error {
	return &Error{
		Code: CodeProjectionSchemaUnusable,
		Message: fmt.Sprintf("the vendored %s %s schema %s, so no projection can be checked against it",
			stryker.Package, stryker.PackageVersion, problem),
		Err: cause,
	}
}

// firstFailure locates the violation to name in a one-line diagnostic.
//
// The validator's error tree branches in map iteration order, so its leaves
// arrive shuffled; the lexicographically first instance location is picked so
// that the same bad document always produces the same message. The detail is
// left to the wrapped error, which a `-v` renderer prints in full.
func firstFailure(err error) string {
	var failure *jsonschema.ValidationError
	if !errors.As(err, &failure) {
		return "an unlocatable position"
	}
	best := ""
	var walk func(*jsonschema.ValidationError)
	walk = func(node *jsonschema.ValidationError) {
		if len(node.Causes) == 0 {
			at := pointerOf(node.InstanceLocation)
			if best == "" || at < best {
				best = at
			}
			return
		}
		for _, cause := range node.Causes {
			walk(cause)
		}
	}
	walk(failure)
	if best == "" {
		return "the document root"
	}
	return best
}

// pointerOf renders RFC 6901 tokens as a pointer, escaping in the order the
// RFC requires so that a '/' escaped to "~1" is not escaped again.
func pointerOf(tokens []string) string {
	if len(tokens) == 0 {
		return "the document root"
	}
	var b strings.Builder
	for _, token := range tokens {
		token = strings.ReplaceAll(token, "~", "~0")
		b.WriteByte('/')
		b.WriteString(strings.ReplaceAll(token, "/", "~1"))
	}
	return b.String()
}
