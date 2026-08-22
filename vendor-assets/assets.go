// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package vendorassets carries the third-party browser assets the HTML report
// is built from, and nothing else.
//
// # Why the bytes are in the repository
//
// The HTML report is a single file that works from `file://` with the network
// unplugged. That is only true if the viewer's code is *inside* it, so the
// viewer has to be somewhere go-mutants can read at build time — and a build
// that downloads something is a build whose output depends on what a CDN served
// that morning. The bundle is therefore committed, exactly as it was published,
// and //go:embed puts it in the binary.
//
// # The digest discipline
//
// A vendored blob is only trustworthy if its identity is checked rather than
// assumed, so the same SHA-256 is written down three times and all three are
// compared:
//
//   - [BundleSHA256], in Go source, is what the code believes it embedded.
//   - `PROVENANCE.json`, beside the file, is what the person who fetched it
//     recorded, together with where it came from and when.
//   - The embedded bytes themselves, hashed at render time.
//
// [Verify] compares all three and is called before every HTML report is
// written; a mismatch aborts the write instead of producing a document whose
// contents nobody can vouch for. The package tests make the same comparison, so
// a tampered or half-updated vendor directory fails `go test` rather than
// waiting for a user to run a report.
//
// # Updating the vendored version
//
// Four things move together, and the tests fail until they agree: the directory
// name, the //go:embed paths, [Version] and [BundleSHA256], and the fields in
// `PROVENANCE.json`. The upstream file is the one npm publishes — the browser
// bundle, not the ES module entry point — fetched from unpkg and verified
// against the registry's own integrity hash.
package vendorassets

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Version is the mutation-testing-elements release vendored here. It names the
// directory the assets live in; see the package documentation for what else has
// to move with it.
const Version = "3.9.0"

// BundleSHA256 is the lowercase hex SHA-256 of [Bundle], recorded in source so
// that the embedded bytes can be checked against something a reviewer read
// rather than against themselves.
const BundleSHA256 = "751fb010242b0b44e32d84fe7fe0b9ff1da182823b94f59f5c52b001fcfc163b"

// License is the SPDX identifier the bundle is distributed under. The full text
// is vendored beside it, as `LICENSE`.
const License = "Apache-2.0"

// Copyright is who holds it, for the notice the HTML report carries.
const Copyright = "Stryker Mutator contributors"

// bundle is the browser build of the viewer: one self-contained script that
// defines the <mutation-test-report-app> custom element.
//
//go:embed mutation-testing-elements/3.9.0/mutation-test-elements.js
var bundle []byte

// provenance is the record of where [bundle] came from.
//
//go:embed mutation-testing-elements/3.9.0/PROVENANCE.json
var provenance []byte

// licenseText is the Apache-2.0 text the upstream project ships.
//
//go:embed mutation-testing-elements/3.9.0/LICENSE
var licenseText []byte

// A Provenance is `PROVENANCE.json`, decoded.
//
// It is the third witness to the bundle's identity, and it is the only one that
// says where the bytes came from: a digest proves two files are the same file
// and says nothing at all about which file that ought to be.
type Provenance struct {
	// Name is the upstream package.
	Name string `json:"name"`
	// Version is the release, which must equal [Version].
	Version string `json:"version"`
	// UpstreamURL is the exact URL the file was fetched from.
	UpstreamURL string `json:"upstream_url"`
	// File is the name of the vendored file this record describes.
	File string `json:"file"`
	// SHA256 is its lowercase hex SHA-256, which must equal [BundleSHA256].
	SHA256 string `json:"sha256"`
	// RetrievedAt is when it was downloaded, RFC 3339 in UTC.
	RetrievedAt string `json:"retrieved_at"`
	// License is the SPDX identifier, which must equal [License].
	License string `json:"license"`
	// LicenseFile names the vendored copy of the license text.
	LicenseFile string `json:"license_file"`
	// LicenseURL is where that text was fetched from.
	LicenseURL string `json:"license_url"`
	// NPMIntegrity is the registry's own subresource integrity string for the
	// package tarball the file was published in. It is recorded rather than
	// checked here: verifying it would mean re-fetching the tarball, which is
	// the network access this package exists to avoid.
	NPMIntegrity string `json:"npm_integrity"`
}

// Bundle returns the viewer's JavaScript.
//
// The returned slice is the embedded one and must not be modified; nothing in
// this repository writes to it, and copying a quarter of a megabyte on every
// report render to defend against a caller that would be broken anyway is not a
// trade worth making.
func Bundle() []byte { return bundle }

// LicenseText returns the vendored Apache-2.0 text.
func LicenseText() []byte { return licenseText }

// ProvenanceJSON returns `PROVENANCE.json` as it is on disk.
func ProvenanceJSON() []byte { return provenance }

// Digest returns the SHA-256 of the embedded bundle, as lowercase hex. It is
// computed from the bytes every time rather than cached: the whole point is to
// answer "what is actually here", and a memoised answer is one more thing that
// could be stale.
func Digest() string {
	sum := sha256.Sum256(bundle)
	return hex.EncodeToString(sum[:])
}

// ErrTampered reports a vendored asset that does not match what is written down
// about it. It is a sentinel so that callers in other packages can recognise the
// condition without importing a second error type, and so that the diagnostic
// they wrap it in can carry their own code.
var ErrTampered = errors.New("a vendored asset does not match its recorded identity")

// ReadProvenance decodes `PROVENANCE.json`.
func ReadProvenance() (Provenance, error) {
	var p Provenance
	if err := json.Unmarshal(provenance, &p); err != nil {
		return Provenance{}, fmt.Errorf("%w: PROVENANCE.json is not readable: %w", ErrTampered, err)
	}
	return p, nil
}

// Verify checks the embedded bundle against both records of what it should be,
// and returns its digest.
//
// The digest is returned rather than merely checked because the caller needs it
// anyway — it goes into the report's notice — and returning the value that was
// just proved correct is one fewer opportunity to hash the bytes twice and use
// the wrong answer.
//
// Every mismatch wraps [ErrTampered]. There is no partial success: a bundle
// whose digest is right and whose provenance names another version is exactly
// as unusable as one whose bytes have been edited, because in both cases the
// repository no longer says what is in the binary.
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
