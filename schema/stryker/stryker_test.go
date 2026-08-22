// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package stryker_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/schema/stryker"
)

// The vendored schema is the only written statement of what the projection has
// to satisfy, so what is tested here is that it is *unaltered* and that the
// constants beside it describe the file that is actually present.
//
// The one that matters most is [stryker.ReportSchemaVersion]. It is "2" while
// the package it came from is 3.9.0, which looks like a mistake every time
// somebody reads it, and the schema's own pattern refuses "3" — so the pattern
// is read out of the file and applied to the constant here, which turns the
// argument from a comment into a failing test.

// TestSchemaIsUnaltered pins the vendored bytes against the digest recorded
// beside them.
func TestSchemaIsUnaltered(t *testing.T) {
	t.Parallel()

	p, err := stryker.ReadProvenance()
	if err != nil {
		t.Fatalf("ReadProvenance: %v", err)
	}
	sum := sha256.Sum256(stryker.Schema())
	if digest := hex.EncodeToString(sum[:]); digest != p.SHA256 {
		t.Errorf("the embedded schema hashes to %s, and PROVENANCE.json records %s", digest, p.SHA256)
	}
	if p.Version != stryker.PackageVersion {
		t.Errorf("PROVENANCE.json records version %q, the source constant says %q", p.Version, stryker.PackageVersion)
	}
	if p.Name != stryker.Package {
		t.Errorf("PROVENANCE.json records package %q, the source constant says %q", p.Name, stryker.Package)
	}
	if p.License != stryker.License {
		t.Errorf("PROVENANCE.json records licence %q, the source constant says %q", p.License, stryker.License)
	}
	if p.RetrievedAt == "" || p.UpstreamURL == "" {
		t.Error("PROVENANCE.json does not say where or when the schema was fetched")
	}
}

// TestSchemaDeclaresWhatTheCompilerIsToldToExpect reads the two things
// internal/report relies on when it compiles the schema with no URL loader:
// the identity it registers the document under, and the draft it is written in.
func TestSchemaDeclaresWhatTheCompilerIsToldToExpect(t *testing.T) {
	t.Parallel()

	var doc struct {
		Schema string `json:"$schema"`
		ID     string `json:"$id"`
	}
	if err := json.Unmarshal(stryker.Schema(), &doc); err != nil {
		t.Fatalf("the vendored schema is not JSON: %v", err)
	}
	if doc.ID != stryker.SchemaID {
		t.Errorf("the schema declares $id %q, the source constant says %q", doc.ID, stryker.SchemaID)
	}
	if !strings.Contains(doc.Schema, "draft-07") {
		t.Errorf("the schema declares $schema %q, and internal/report compiles it expecting draft-07", doc.Schema)
	}
}

// TestReportSchemaVersionIsTheOneTheSchemaAccepts is the trap, written down.
//
// The pattern is read out of the vendored file rather than copied, so that a
// future version of the schema that widens or narrows it is checked against the
// constant automatically.
func TestReportSchemaVersionIsTheOneTheSchemaAccepts(t *testing.T) {
	t.Parallel()

	var doc struct {
		Properties struct {
			SchemaVersion struct {
				Pattern string `json:"pattern"`
			} `json:"schemaVersion"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(stryker.Schema(), &doc); err != nil {
		t.Fatalf("the vendored schema is not JSON: %v", err)
	}
	pattern := doc.Properties.SchemaVersion.Pattern
	if pattern == "" {
		t.Fatal("the vendored schema states no pattern for schemaVersion, so this test proves nothing")
	}
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("the schema's pattern %q does not compile: %v", pattern, err)
	}
	if !matcher.MatchString(stryker.ReportSchemaVersion) {
		t.Errorf("ReportSchemaVersion %q does not match the schema's own pattern %q",
			stryker.ReportSchemaVersion, pattern)
	}
	// The package version is 3.9.0 and would be refused. This is the assertion
	// that stops somebody "fixing" the constant to match the package name.
	if major, _, _ := strings.Cut(stryker.PackageVersion, "."); matcher.MatchString(major) {
		t.Errorf("the schema accepts %q, so the note about the package version no longer holds", major)
	}
}

// TestLicenseIsVendored checks the obligation the licence imposes.
func TestLicenseIsVendored(t *testing.T) {
	t.Parallel()

	text := string(stryker.LicenseText())
	if !strings.Contains(text, "Apache License") || !strings.Contains(text, "Version 2.0") {
		t.Errorf("the vendored licence is not the Apache License 2.0:\n%.120s", text)
	}
}

// TestSchemaIsNotInThePublishedRegistry is the boundary this package exists to
// draw, checked from the other side: nothing here declares a `document_type`,
// so `report validate` can never be asked for it and a third-party definition
// cannot be mistaken for one go-mutants publishes.
func TestSchemaIsNotInThePublishedRegistry(t *testing.T) {
	t.Parallel()

	if strings.Contains(string(stryker.Schema()), "go-mutants") {
		t.Error("the vendored schema mentions go-mutants, so it is not the file upstream published")
	}
	if strings.Contains(string(stryker.ProvenanceJSON()), "document_type") {
		t.Error("the provenance claims a document type; this schema describes somebody else's format")
	}
}
