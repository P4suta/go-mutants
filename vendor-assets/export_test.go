// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package vendorassets

// This file is compiled only under `go test`.
//
// The digest check exists to catch bytes that are not the bytes anybody
// recorded, and the only honest way to test it is to make the bytes not be
// those bytes. Editing the vendored file would fail the build for every other
// test at once, so the embedded variable is swapped for the length of one
// assertion instead.

// SwapBundle replaces the embedded bundle and returns the function that puts it
// back. A test that uses it cannot be parallel.
func SwapBundle(data []byte) func() {
	previous := bundle
	bundle = data
	return func() { bundle = previous }
}

// SwapProvenance replaces the embedded `PROVENANCE.json`, so that the half of
// the check that compares the record with the constants can be exercised
// without touching the bundle at all.
func SwapProvenance(data []byte) func() {
	previous := provenance
	provenance = data
	return func() { provenance = previous }
}

// SwapLicense replaces the embedded licence text, for the one part of the check
// that is about a file being there rather than about a digest.
func SwapLicense(data []byte) func() {
	previous := licenseText
	licenseText = data
	return func() { licenseText = previous }
}
