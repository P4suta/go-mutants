// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package vendorassets

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const Version = "3.9.0"

const BundleSHA256 = "751fb010242b0b44e32d84fe7fe0b9ff1da182823b94f59f5c52b001fcfc163b"

const License = "Apache-2.0"

const Copyright = "Stryker Mutator contributors"

//go:embed mutation-testing-elements/3.9.0/mutation-test-elements.js
var bundle []byte

//go:embed mutation-testing-elements/3.9.0/PROVENANCE.json
var provenance []byte

//go:embed mutation-testing-elements/3.9.0/LICENSE
var licenseText []byte

type Provenance struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	UpstreamURL  string `json:"upstream_url"`
	File         string `json:"file"`
	SHA256       string `json:"sha256"`
	RetrievedAt  string `json:"retrieved_at"`
	License      string `json:"license"`
	LicenseFile  string `json:"license_file"`
	LicenseURL   string `json:"license_url"`
	NPMIntegrity string `json:"npm_integrity"`
}

func Bundle() []byte { return bundle }

func LicenseText() []byte { return licenseText }

func ProvenanceJSON() []byte { return provenance }

func Digest() string {
	sum := sha256.Sum256(bundle)
	return hex.EncodeToString(sum[:])
}

var ErrTampered = errors.New("a vendored asset does not match its recorded identity")

func ReadProvenance() (Provenance, error) {
	var p Provenance
	if err := json.Unmarshal(provenance, &p); err != nil {
		return Provenance{}, fmt.Errorf("%w: PROVENANCE.json is not readable: %w", ErrTampered, err)
	}
	return p, nil
}

func Verify() (string, error) {
	digest := Digest()
	if digest != BundleSHA256 {
		return "", fmt.Errorf("%w: the embedded %s bundle hashes to %s, but the source constant says %s",
			ErrTampered, Version, digest, BundleSHA256)
	}
	p, err := ReadProvenance()
	if err != nil {
		return "", err
	}
	switch {
	case p.Version != Version:
		return "", fmt.Errorf("%w: PROVENANCE.json records version %q, but the source constant says %q",
			ErrTampered, p.Version, Version)
	case p.SHA256 != BundleSHA256:
		return "", fmt.Errorf("%w: PROVENANCE.json records SHA-256 %s, but the source constant says %s",
			ErrTampered, p.SHA256, BundleSHA256)
	case p.License != License:
		return "", fmt.Errorf("%w: PROVENANCE.json records licence %q, but the source constant says %q",
			ErrTampered, p.License, License)
	case len(licenseText) == 0:
		return "", fmt.Errorf("%w: the vendored licence text is empty", ErrTampered)
	}
	return digest, nil
}
