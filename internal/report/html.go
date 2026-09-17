// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"

	vendorassets "github.com/P4suta/go-mutants/vendor-assets"
)

const bootstrap = `(()=>{const d=document.getElementById("report");const a=document.querySelector("mutation-test-report-app");a.report=JSON.parse(d.textContent);})();`

const islandID = "report"

const pageTitle = "Mutation test report"

var verifyViewer = vendorassets.Verify

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

func scriptHash(script string) string {
	sum := sha256.Sum256([]byte(script))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func notice(digest string) string {
	return commentSafe("Inlined third-party software: mutation-testing-elements " + vendorassets.Version +
		", copyright " + vendorassets.Copyright + ", licensed " + vendorassets.License +
		" (the full text is vendored at vendor-assets/mutation-testing-elements/" + vendorassets.Version +
		"/LICENSE). Verified SHA-256 " + digest + ". This page loads nothing: see the Content-Security-Policy above.")
}

func commentSafe(s string) string {
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "- -")
	}
	return s
}

func escapeScriptData(document []byte) []byte {
	text := string(document)
	text = strings.ReplaceAll(text, "&", `\u0026`)
	text = strings.ReplaceAll(text, "<", `\u003c`)
	text = strings.ReplaceAll(text, ">", `\u003e`)
	text = strings.ReplaceAll(text, "\u2028", `\u2028`)
	text = strings.ReplaceAll(text, "\u2029", `\u2029`)
	return []byte(text)
}
