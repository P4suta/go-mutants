// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package schemas_test

import (
	"errors"
	"testing"

	"github.com/P4suta/go-mutants/internal/schemas"
)

func FuzzValidate(f *testing.F) {
	for _, documentType := range schemas.DocumentTypes() {
		f.Add(documentType, `{}`)
		f.Add(documentType, `{"document_type":"`+documentType+`","schema_version":1}`)
	}
	f.Add("", "")
	f.Add("go-mutants/run-report", "")
	f.Add("go-mutants/run-report", "null")
	f.Add("go-mutants/run-report", "[]")
	f.Add("go-mutants/run-report", "{")
	f.Add("go-mutants/run-report", `{"document_type":1}`)
	f.Add("not/a/type", `{}`)
	f.Add("go-mutants/trace-event", `{"seq":1}`)
	f.Add("go-mutants/doctor", `{"checks":[]}`)
	f.Add("go-mutants/diagnostics", `{"files":[]}`)

	f.Fuzz(func(t *testing.T, documentType, document string) {
		err := schemas.Validate(documentType, []byte(document))
		if err == nil {
			return
		}
		var refusal *schemas.Error
		if !errors.As(err, &refusal) {
			t.Fatalf("Validate(%q, %q) returned %T, want a *schemas.Error a caller can branch on: %v",
				documentType, document, err, err)
		}
		if refusal.Code == "" {
			t.Fatalf("Validate(%q, %q) refused with no code", documentType, document)
		}
		if refusal.Error() == "" {
			t.Fatalf("Validate(%q, %q) refused with an empty message", documentType, document)
		}
		if second := schemas.Validate(documentType, []byte(document)); (second == nil) != (err == nil) {
			t.Fatalf("Validate(%q, %q) answered differently the second time: %v then %v",
				documentType, document, err, second)
		}
	})
}
