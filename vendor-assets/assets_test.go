// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package vendorassets_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	vendorassets "github.com/P4suta/go-mutants/vendor-assets"
)

// A vendored blob is worth exactly as much as the discipline around it, so the
// discipline is what is tested: the three records of the bundle's identity
// agree, the licence is really here, and a bundle that is not the recorded one
// is refused rather than reported.
//
// These tests are what make a half-finished version bump fail `go test` — a new
// file with the old constant, a new constant with the old `PROVENANCE.json` —
// instead of failing in somebody's browser weeks later.

// TestEmbeddedBundleMatchesItsRecordedDigest is the whole point of the package,
// stated once.
func TestEmbeddedBundleMatchesItsRecordedDigest(t *testing.T) {
	t.Parallel()

	sum := sha256.Sum256(vendorassets.Bundle())
	digest := hex.EncodeToString(sum[:])
	if digest != vendorassets.BundleSHA256 {
		t.Fatalf("the embedded bundle hashes to %s, and the source constant says %s",
			digest, vendorassets.BundleSHA256)
	}
	if got := vendorassets.Digest(); got != digest {
		t.Errorf("Digest() = %s, want %s", got, digest)
	}
}

// TestProvenanceAgreesWithTheConstants pins the second and third records
// against each other.
func TestProvenanceAgreesWithTheConstants(t *testing.T) {
	t.Parallel()

	p, err := vendorassets.ReadProvenance()
	if err != nil {
		t.Fatalf("ReadProvenance: %v", err)
	}
	if p.Version != vendorassets.Version {
		t.Errorf("PROVENANCE.json records version %q, the source constant says %q", p.Version, vendorassets.Version)
	}
	if p.SHA256 != vendorassets.BundleSHA256 {
		t.Errorf("PROVENANCE.json records %s, the source constant says %s", p.SHA256, vendorassets.BundleSHA256)
	}
	if p.License != vendorassets.License {
		t.Errorf("PROVENANCE.json records licence %q, the source constant says %q", p.License, vendorassets.License)
	}
	// Provenance is where the bytes came from, not merely what they hash to: a
	// digest proves two files are the same file and says nothing about which
	// file that ought to be.
	if !strings.Contains(p.UpstreamURL, vendorassets.Version) {
		t.Errorf("the upstream URL %q does not name the vendored version", p.UpstreamURL)
	}
	if p.RetrievedAt == "" {
		t.Error("PROVENANCE.json does not say when the file was fetched")
	}
	if p.Name == "" || p.File == "" {
		t.Error("PROVENANCE.json does not name the package or the file it describes")
	}
}

// TestLicenseIsVendored checks the obligation the licence itself imposes: the
// text travels with the code.
func TestLicenseIsVendored(t *testing.T) {
	t.Parallel()

	text := string(vendorassets.LicenseText())
	if !strings.Contains(text, "Apache License") || !strings.Contains(text, "Version 2.0") {
		t.Errorf("the vendored licence is not the Apache License 2.0:\n%.120s", text)
	}
}

// TestVerifyAcceptsWhatIsHere is the state the repository is meant to be in.
func TestVerifyAcceptsWhatIsHere(t *testing.T) {
	t.Parallel()

	digest, err := vendorassets.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if digest != vendorassets.BundleSHA256 {
		t.Errorf("Verify returned %s, want %s", digest, vendorassets.BundleSHA256)
	}
}

// TestVerifyRefusesATamperedBundle is the check doing its job.
//
// The bundle is really replaced rather than a hash being faked, because the
// failure this guards against is real bytes that nobody vouched for — a proxy
// that rewrote a download, a merge that took the wrong side, an editor that
// reformatted a file it should not have opened.
func TestVerifyRefusesATamperedBundle(t *testing.T) {
	for name, tc := range map[string]func() func(){
		"one byte appended": func() func() {
			return vendorassets.SwapBundle(append(append([]byte{}, vendorassets.Bundle()...), ' '))
		},
		"emptied": func() func() {
			return vendorassets.SwapBundle(nil)
		},
		"replaced wholesale": func() func() {
			return vendorassets.SwapBundle([]byte("alert('hello')"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			restore := tc()
			defer restore()

			digest, err := vendorassets.Verify()
			if err == nil {
				t.Fatalf("Verify accepted a tampered bundle and returned %s", digest)
			}
			if !errors.Is(err, vendorassets.ErrTampered) {
				t.Errorf("Verify reported %v, which is not an ErrTampered", err)
			}
			if digest != "" {
				t.Errorf("Verify returned a digest (%s) alongside a refusal", digest)
			}
		})
	}
}

// TestVerifyRefusesADisagreeingProvenance covers the half-finished version
// bump: correct bytes, and a record that describes something else.
func TestVerifyRefusesADisagreeingProvenance(t *testing.T) {
	for name, record := range map[string]string{
		"another version": `{"version":"4.0.0","sha256":"` + vendorassets.BundleSHA256 + `","license":"Apache-2.0"}`,
		"another digest":  `{"version":"` + vendorassets.Version + `","sha256":"` + strings.Repeat("0", 64) + `","license":"Apache-2.0"}`,
		"another licence": `{"version":"` + vendorassets.Version + `","sha256":"` + vendorassets.BundleSHA256 + `","license":"MIT"}`,
		"not json":        `{`,
	} {
		t.Run(name, func(t *testing.T) {
			restore := vendorassets.SwapProvenance([]byte(record))
			defer restore()

			if _, err := vendorassets.Verify(); !errors.Is(err, vendorassets.ErrTampered) {
				t.Errorf("Verify with %s provenance = %v, want an ErrTampered", name, err)
			}
		})
	}
}

// TestVerifyRefusesAMissingLicense is the one part of the check that is about a
// file being present rather than about a digest: shipping somebody's Apache-2.0
// code without the text is a licence violation, and the HTML report inlines a
// quarter of a megabyte of it.
func TestVerifyRefusesAMissingLicense(t *testing.T) {
	restore := vendorassets.SwapLicense(nil)
	defer restore()

	if _, err := vendorassets.Verify(); !errors.Is(err, vendorassets.ErrTampered) {
		t.Errorf("Verify with no licence text = %v, want an ErrTampered", err)
	}
}

// TestBundleIsTheBrowserBuild is a cheap sanity check that the *right file* was
// vendored: the npm package also ships an ES module entry point, which is a
// perfectly good file that does nothing at all when inlined into a script
// element.
func TestBundleIsTheBrowserBuild(t *testing.T) {
	t.Parallel()

	bundle := string(vendorassets.Bundle())
	if !strings.Contains(bundle, "mutation-test-report-app") {
		t.Error("the vendored bundle does not define the report element")
	}
	// The browser build is one immediately-invoked function that assigns a
	// global; the module build starts with import statements and ends with an
	// export list, and inlining it into a plain <script> does nothing at all.
	// The prefix is checked rather than the absence of the word "import",
	// which appears inside the syntax highlighter's own regular expressions.
	if !strings.HasPrefix(bundle, "var MutationTestElements=(function(") {
		t.Errorf("the vendored bundle is not the browser build; it starts %q", bundle[:min(60, len(bundle))])
	}
	for _, forbidden := range []string{"export{", "export default", "export const"} {
		if strings.Contains(bundle, forbidden) {
			t.Errorf("the vendored bundle contains %q, so it is a module rather than a browser build", forbidden)
		}
	}
	// The page inlines this between <script> and </script>, so a closing tag
	// anywhere in it would end the element early and turn the rest of the
	// report into markup. The escaping that protects the JSON island cannot
	// protect this: escaping it would change the bytes the policy hashes.
	if strings.Contains(bundle, "</script") || strings.Contains(bundle, "<script") {
		t.Error("the vendored bundle contains a script tag, so it cannot be inlined verbatim")
	}
}
