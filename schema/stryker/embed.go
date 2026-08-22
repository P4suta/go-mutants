// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package stryker carries the vendored mutation-testing-report schema, and
// nothing else.
//
// # Why it is vendored at all
//
// go-mutants publishes a one-way projection of its own run report into the
// mutation-testing-report format, so that the Stryker ecosystem's viewer — and
// the dashboards that consume the same format — can read it. A projection into
// somebody else's format is a promise about that format, and the only way to
// keep such a promise is to hold the format's own definition and check the
// document against it before writing it. internal/report does exactly that, and
// refuses to write a projection that does not validate.
//
// # Why it is not in the schema/ registry
//
// The parent package carries the schemas go-mutants *publishes*, and
// internal/schemas maps a `document_type` onto each of them. This schema is
// neither: it is a third-party definition go-mutants writes *against*, the
// documents it describes carry no `document_type` at all, and it is Apache-2.0
// rather than the project's own dual licence. Putting it in the same directory
// would have mixed a fetched artefact into a set of hand-maintained ones and
// given `report validate` a document type nobody can ask for. A directory of
// its own, with the licence and the provenance beside it, keeps the difference
// visible in a directory listing.
//
// The schema declares draft-07 and its own "$id"; both are honoured as written
// rather than reinterpreted, because a vendored document that has been adjusted
// to fit is no longer the document upstream published.
package stryker

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

// The identity of the vendored schema.
const (
	// Package is the npm package the schema was published in.
	Package = "mutation-testing-report-schema"
	// PackageVersion is the release vendored here. It names the file, and it is
	// *not* the schema version a document carries; see [ReportSchemaVersion].
	PackageVersion = "3.9.0"

	// ReportSchemaVersion is the value a projected document puts in
	// `schemaVersion`: the major version of the report format, which is 2 and
	// has been since long before the 3.x packaging.
	//
	// It is deliberately not derived from [PackageVersion], and nobody should
	// "fix" it to match: the schema's own pattern for the field is
	// `^([1-2])(\.(([1-9]\d*)|0)){0,2}$`, so a document claiming "3" is refused
	// by the very schema the package name comes from. The validation
	// internal/report performs before writing catches the mistake either way,
	// which is the point of doing it.
	ReportSchemaVersion = "2"

	// SchemaID is the "$id" the schema declares. It is a name and not an
	// address: nothing dereferences it, and no URL loader is installed anywhere
	// this schema is compiled.
	SchemaID = "http://stryker-mutator.io/report.schema.json"

	// License is the SPDX identifier the schema is distributed under, with the
	// full text vendored beside it as `LICENSE`.
	License = "Apache-2.0"
	// Copyright is who holds it.
	Copyright = "Stryker Mutator contributors"
)

// schemaJSON is the schema exactly as upstream published it.
//
//go:embed mutation-testing-report-schema-3.9.0.json
var schemaJSON []byte

// provenance records where [schemaJSON] came from.
//
//go:embed PROVENANCE.json
var provenance []byte

// licenseText is the Apache-2.0 text the upstream project ships.
//
//go:embed LICENSE
var licenseText []byte

// A Provenance is `PROVENANCE.json`, decoded. See the same type in
// vendor-assets for why a digest alone is not provenance.
type Provenance struct {
	Name                string `json:"name"`
	Version             string `json:"version"`
	ReportSchemaVersion string `json:"report_schema_version"`
	UpstreamURL         string `json:"upstream_url"`
	File                string `json:"file"`
	SHA256              string `json:"sha256"`
	RetrievedAt         string `json:"retrieved_at"`
	License             string `json:"license"`
	LicenseFile         string `json:"license_file"`
	LicenseURL          string `json:"license_url"`
	NPMIntegrity        string `json:"npm_integrity"`
}

// Schema returns the vendored schema document. The slice is the embedded one
// and must not be modified.
func Schema() []byte { return schemaJSON }

// LicenseText returns the vendored Apache-2.0 text.
func LicenseText() []byte { return licenseText }

// ProvenanceJSON returns `PROVENANCE.json` as it is on disk.
func ProvenanceJSON() []byte { return provenance }

// ErrTampered reports a vendored file that does not match what is written down
// about it.
var ErrTampered = errors.New("the vendored Stryker schema does not match its recorded identity")

// ReadProvenance decodes `PROVENANCE.json`.
func ReadProvenance() (Provenance, error) {
	var p Provenance
	if err := json.Unmarshal(provenance, &p); err != nil {
		return Provenance{}, fmt.Errorf("%w: PROVENANCE.json is not readable: %w", ErrTampered, err)
	}
	return p, nil
}
