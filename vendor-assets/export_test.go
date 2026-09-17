// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package vendorassets

func SwapBundle(data []byte) func() {
	previous := bundle
	bundle = data
	return func() { bundle = previous }
}

func SwapProvenance(data []byte) func() {
	previous := provenance
	provenance = data
	return func() { provenance = previous }
}

func SwapLicense(data []byte) func() {
	previous := licenseText
	licenseText = data
	return func() { licenseText = previous }
}
