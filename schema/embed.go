// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package schema carries the JSON Schema documents go-mutants publishes, and
// nothing else.
//
// The schemas are the compatibility surface: `list --json`, `run --json`, and
// the report files on disk are all documents somebody scripts against, and a
// schema is how go-mutants states — precisely, and in a form other tools can
// read — what those documents contain. They are validated in-process against
// the very output the CLI is about to print, so a document that would not
// validate never reaches a user's pipeline.
//
// # Why this package exists at all
//
// The schemas belong in schema/ at the repository root: they are published
// artefacts, referenced by docs/json-schema.md, and diffed by hand when a
// version is cut. Embedding them into the binary is what makes validation
// possible with no files on disk and no network — and //go:embed patterns may
// not contain "..", so the embed directive has to live in the same directory
// as the files it embeds.
//
// That is the whole reason for this file. The logic — the document-type
// registry, compilation, and validation — lives in internal/schemas, which
// imports [FS]. Nothing here decides anything, so nothing here can go stale
// when a schema is added: the pattern below picks up every .json file in this
// directory, including the vendored third-party schemas that later phases add.
package schema

import "embed"

// FS holds every schema in this directory, named exactly as the file is
// named — for example "catalog-v1.schema.json".
//
// The pattern deliberately does not recurse and does not match dotfiles: this
// directory is flat by convention, and a schema that a reader cannot find by
// listing the directory is a schema nobody will remember to version.
//
//go:embed *.json
var FS embed.FS
