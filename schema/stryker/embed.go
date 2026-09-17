// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package stryker carries the vendored mutation-testing-report schema, and.
package stryker

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	Package        = "mutation-testing-report-schema"
	PackageVersion = "3.9.0"

	ReportSchemaVersion = "2"

	SchemaID = "http://stryker-mutator.io/report.schema.json"

	License   = "Apache-2.0"
	Copyright = "Stryker Mutator contributors"
)

//go:embed mutation-testing-report-schema-3.9.0.json
var schemaJSON []byte

//go:embed PROVENANCE.json
var provenance []byte

//go:embed LICENSE
var licenseText []byte

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

func Schema() []byte { return schemaJSON }

func LicenseText() []byte { return licenseText }

func ProvenanceJSON() []byte { return provenance }

var ErrTampered = errors.New("the vendored Stryker schema does not match its recorded identity")

func ReadProvenance() (Provenance, error) {
	var p Provenance
	if err := json.Unmarshal(provenance, &p); err != nil {
		return Provenance{}, fmt.Errorf("%w: PROVENANCE.json is not readable: %w", ErrTampered, err)
	}
	return p, nil
}
