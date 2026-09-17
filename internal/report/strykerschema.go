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

var (
	strykerSchemaOnce sync.Once
	strykerSchema     *jsonschema.Schema
	strykerSchemaErr  error
)

var strykerSchemaSource = stryker.Schema

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

func compiledStrykerSchema() (*jsonschema.Schema, error) {
	strykerSchemaOnce.Do(func() {
		c := jsonschema.NewCompiler()
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(strykerSchemaSource()))
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

func unusableStrykerSchema(problem string, cause error) error {
	return &Error{
		Code: CodeProjectionSchemaUnusable,
		Message: fmt.Sprintf("the vendored %s %s schema %s, so no projection can be checked against it",
			stryker.Package, stryker.PackageVersion, problem),
		Err: cause,
	}
}

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
