// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"

	vendorassets "github.com/P4suta/go-mutants/vendor-assets"
)

// The HTML report is one file that works with the network unplugged.
//
// # What "self-contained" has to mean
//
// A mutation report is opened from a CI artefact, from a shared drive, from a
// `file://` URL on a laptop on a train. Every one of those is a context where a
// page that fetches anything shows an empty frame, so nothing in the page
// causes a network request: the viewer's JavaScript is inlined from
// [vendorassets] and the data is inlined as a JSON island. The only URLs in
// the file live inside the vendored bundle — the SVG namespace (a name, not
// an address), documentation hyperlinks a reader may click, and `data:`
// image URIs — plus the notice comment; `default-src 'none'` blocks every
// fetch regardless of what a future bundle version might try.
//
// # Why a Content-Security-Policy on a local file
//
// The page carries a strict CSP even though nothing served it. It is not
// defence against the file's author, who is go-mutants; it is a *statement*
// that the page needs no network, enforced by the browser rather than asserted
// in a comment. `default-src 'none'` means a future edit that adds a font, a
// tracker, or a "check for updates" fetch does not silently work — it breaks
// loudly, in review, instead of turning a report somebody attached to a pull
// request into a beacon.
//
// The two executable scripts are allowed by SHA-256 hash and by nothing else:
// no 'unsafe-inline', no nonce (a nonce in a static file is a constant, which
// is the same as 'unsafe-inline' with extra steps). A hash is a promise about
// exactly which bytes may run, and it is computed from the very strings this
// file concatenates, so a change to either script that forgot to update the
// policy produces a page that refuses to run rather than one that runs
// something unvouched-for.
//
// The JSON island is not hashed and does not need to be. A <script> whose type
// is not a JavaScript MIME type is a *data block*: the HTML parser returns from
// "prepare the script element" before the CSP check, and nothing in it is ever
// executed. It is escaped instead, which is the protection that actually
// matters for it; see [escapeScriptData].

// bootstrap is the whole of the page's own JavaScript.
//
// It hands the island to the custom element and stops. Everything else — the
// rendering, the routing, the theme — is the vendored viewer's, and the smaller
// this string is the less there is to review against its hash.
//
// It is a single line with no trailing newline, because its bytes are hashed
// into the Content-Security-Policy: reformatting it is a change to the policy,
// and that is exactly the coupling the hash is for.
const bootstrap = `(()=>{const d=document.getElementById("report");const a=document.querySelector("mutation-test-report-app");a.report=JSON.parse(d.textContent);})();`

// islandID is the id the JSON island carries and the bootstrap looks up.
const islandID = "report"

// pageTitle is what a browser tab and a bookmark show.
const pageTitle = "Mutation test report"

// verifyViewer is the vendored-asset check [RenderHTML] performs.
//
// It is a variable for one reason, and nothing but a test ever assigns to it:
// the gate has to be proved to be a *gate*. The check itself is tested against
// really altered bytes in vendor-assets' own tests, but that cannot show what
// this package does when it fails, and "aborts the page" and "logs a warning
// and inlines it anyway" are indistinguishable from outside until somebody
// tries it. See internal/report's html_test.go.
var verifyViewer = vendorassets.Verify

// RenderHTML returns the complete self-contained report page for an encoded
// projection.
//
// document must be the bytes [Projection.Marshal] produced and
// [ValidateProjection] accepted. It is embedded verbatim apart from the four
// characters [escapeScriptData] respells, so a page that renders is a page
// whose data is the same document `mutation.json` holds.
//
// The vendored bundle's SHA-256 is checked here, against both the constant in
// [vendorassets] and the digest recorded in its `PROVENANCE.json`, every time a
// page is rendered. Checking it once at build time would prove something about
// the machine that built the binary; checking it here proves something about
// the bytes that are about to be written into a file somebody will open and
// trust. A mismatch aborts with [CodeVendoredAssetTampered] rather than
// producing a report with an unvouched-for quarter-megabyte of JavaScript in it.
func RenderHTML(document []byte) ([]byte, error) {
	digest, err := verifyViewer()
	if err != nil {
		return nil, &Error{
			Code: CodeVendoredAssetTampered,
			Message: "the vendored mutation-testing-elements " + vendorassets.Version +
				" viewer is not the one this build recorded, so no HTML report was written",
			Err: err,
		}
	}
	bundle := string(vendorassets.Bundle())

	var b strings.Builder
	b.Grow(len(bundle) + len(document) + 4096)
	b.WriteString("<!doctype html>\n")
	b.WriteString(`<html lang="en">` + "\n")
	b.WriteString("<head>\n")
	b.WriteString(`<meta charset="utf-8">` + "\n")
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">` + "\n")
	b.WriteString(`<meta http-equiv="Content-Security-Policy" content="` + contentSecurityPolicy(bundle) + `">` + "\n")
	b.WriteString("<title>" + pageTitle + "</title>\n")
	b.WriteString("</head>\n<body>\n")
	b.WriteString("<!-- " + notice(digest) + " -->\n")
	b.WriteString("<mutation-test-report-app></mutation-test-report-app>\n")
	// No whitespace inside any of the three script elements: the two
	// executable ones are allowed by a hash of their exact text content, and a
	// stray newline would be a byte the policy does not cover.
	b.WriteString(`<script id="` + islandID + `" type="application/json">`)
	b.Write(escapeScriptData(document))
	b.WriteString("</script>\n")
	b.WriteString("<script>")
	b.WriteString(bundle)
	b.WriteString("</script>\n")
	b.WriteString("<script>")
	b.WriteString(bootstrap)
	b.WriteString("</script>\n")
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String()), nil
}

// contentSecurityPolicy is the policy the page carries.
//
// `default-src 'none'` already forbids every fetch directive, so the ones named
// after it are redundant — and they are named anyway. A policy that spells out
// `connect-src 'none'` and `frame-src 'none'` is one a reviewer can read
// without having to remember which directives fall back to the default, and the
// two that are *not* 'none' — the inline styles the viewer's components need,
// and the data: URIs its icons are — stand out as the only two exceptions in a
// list where everything else is refused.
func contentSecurityPolicy(bundle string) string {
	return "default-src 'none'" +
		"; script-src 'sha256-" + scriptHash(bundle) + "' 'sha256-" + scriptHash(bootstrap) + "'" +
		"; style-src 'unsafe-inline'" +
		"; img-src data:" +
		"; font-src data:" +
		"; connect-src 'none'" +
		"; object-src 'none'" +
		"; frame-src 'none'" +
		"; media-src 'none'" +
		"; worker-src 'none'" +
		"; manifest-src 'none'" +
		"; base-uri 'none'" +
		"; form-action 'none'"
}

// scriptHash is the base64 SHA-256 of one inline script's text content, as a
// CSP hash source spells it.
func scriptHash(script string) string {
	sum := sha256.Sum256([]byte(script))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// notice is the third-party attribution the page carries, as an HTML comment.
//
// It names what is inlined, who wrote it, the licence it is under, and the
// digest that was checked before it was inlined — so that a reader who opens
// the file in an editor and finds a quarter of a megabyte of minified
// JavaScript can find out what it is without leaving the file.
func notice(digest string) string {
	return commentSafe("Inlined third-party software: mutation-testing-elements " + vendorassets.Version +
		", copyright " + vendorassets.Copyright + ", licensed " + vendorassets.License +
		" (the full text is vendored at vendor-assets/mutation-testing-elements/" + vendorassets.Version +
		"/LICENSE). Verified SHA-256 " + digest + ". This page loads nothing: see the Content-Security-Policy above.")
}

// commentSafe removes the one sequence that cannot appear inside an HTML
// comment.
//
// Nothing composed above contains "--" today. It is neutralised anyway, because
// every part of that sentence is a constant somebody may edit — a version, a
// copyright line, a path — and a double hyphen slipped into one of them would
// close the comment early and put prose into the document body, in a file
// nobody reads by hand.
func commentSafe(s string) string {
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "- -")
	}
	return s
}

// escapeScriptData respells the characters a JSON document embedded in an HTML
// page must not contain literally.
//
// `<` is the one that matters: a `</script>` inside a string in the data would
// end the element, and the rest of the document would be parsed as markup. `&`
// and `>` follow it for the same reason Go's own encoder escapes them —
// there is no context in which a raw one is needed, and escaping all three
// makes the island safe under any parser rather than under a correct one.
//
// U+2028 and U+2029 are the JavaScript trap rather than the HTML one: JSON
// permits them raw inside a string, ECMAScript before ES2019 treated them as
// line terminators, and a JSON.parse of text containing one used to be a syntax
// error. They cost two escapes to rule out for good.
//
// Every replacement is a valid JSON escape for exactly the character it
// replaces, and all five characters occur only inside JSON string literals, so
// the escaped island parses to the identical document.
func escapeScriptData(document []byte) []byte {
	text := string(document)
	text = strings.ReplaceAll(text, "&", `\u0026`)
	text = strings.ReplaceAll(text, "<", `\u003c`)
	text = strings.ReplaceAll(text, ">", `\u003e`)
	text = strings.ReplaceAll(text, "\u2028", `\u2028`)
	text = strings.ReplaceAll(text, "\u2029", `\u2029`)
	return []byte(text)
}
