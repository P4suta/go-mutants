// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"bytes"
	"fmt"

	"github.com/P4suta/go-mutants/schema"
)

// schemaFileName is the published contract in [schema.FS].
//
// The file lives in schema/ with every other published schema rather than in
// this directory, so that there is one copy of it: the one embedded in the
// binary, the one internal/schemas validates against, and the one a reader of
// the repository finds by listing schema/ are the same bytes.
const schemaFileName = "trace-v1.schema.json"

// schemaDocument is that file, read once.
var schemaDocument = mustEmbed(schemaFileName)

// mustEmbed reads a schema out of the embedded directory at initialisation.
//
// A failure here is unreachable: [schema.FS] embeds every .json file in that
// directory at build time, so the only way to get one is to delete the file
// without changing the constant above — which the package tests catch. It
// panics rather than yielding nothing on purpose, because a validator handed an
// empty schema accepts every document, and a contract that silently stops being
// enforced is worse than a build that stops.
func mustEmbed(name string) []byte {
	data, err := schema.FS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("go-mutants: embedded schema %s is unreadable: %v", name, err))
	}
	return data
}

// JSONSchema returns the JSON Schema every line of a recording is one instance
// of, as a copy: a caller that writes into what it was given cannot change what
// the next one reads.
func JSONSchema() []byte { return bytes.Clone(schemaDocument) }
