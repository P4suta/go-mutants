// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"bytes"
	"fmt"

	"github.com/P4suta/go-mutants/schema"
)

const schemaFileName = "trace-v1.schema.json"

var schemaDocument = mustEmbed(schemaFileName)

func mustEmbed(name string) []byte {
	data, err := schema.FS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("go-mutants: embedded schema %s is unreadable: %v", name, err))
	}
	return data
}

func JSONSchema() []byte { return bytes.Clone(schemaDocument) }
