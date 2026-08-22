// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
	vendorassets "github.com/P4suta/go-mutants/vendor-assets"
)

// The HTML report makes three promises that are invisible from the outside
// until they are broken in somebody's browser: it runs exactly two scripts and
// says so by hash, it holds the projection as data rather than as markup, and
// it fetches nothing. Each of the three is pinned here, and each is read *out
// of the rendered page* rather than recomputed from the constants that produced
// it — a test that hashed the same constants the renderer hashed would agree
// with itself while the assembly between them inserted a newline and broke the
// page.

// cspPattern extracts the policy from the rendered meta element.
var cspPattern = regexp.MustCompile(`<meta http-equiv="Content-Security-Policy" content="([^"]*)">`)

// hashPattern finds every sha256 source expression in a policy.
var hashPattern = regexp.MustCompile(`'sha256-([A-Za-z0-9+/=]+)'`)

// renderFixture renders the page for the fixture projection.
func renderFixture(t *testing.T) (page string, document []byte) {
	t.Helper()
	document = marshalProjection(t, projectionFixture(t), projectionWorkspace(t))
	rendered, err := report.RenderHTML(document)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	return string(rendered), document
}

// executableScripts returns the text content of every <script> the browser
// would run, in document order.
//
// The JSON island is deliberately not among them: its opening tag carries a
// type, so it does not match, which is the same reason the browser treats it as
// a data block and never asks the policy about it.
func executableScripts(t *testing.T, page string) []string {
	t.Helper()
	var scripts []string
	rest := page
	for {
		open := strings.Index(rest, "<script>")
		if open < 0 {
			return scripts
		}
		rest = rest[open+len("<script>"):]
		close := strings.Index(rest, "</script>")
		if close < 0 {
			t.Fatal("a <script> element in the page is never closed")
		}
		scripts = append(scripts, rest[:close])
		rest = rest[close:]
	}
}

// TestHTMLPolicyHashesTheScriptsThePageActuallyRuns is the assertion that
// catches an assembly mistake.
//
// The hashes are computed from the text the parser would execute — everything
// between `<script>` and `</script>` in the rendered output — and compared with
// the policy in the rendered `<meta>`. A stray newline after the opening tag, a
// bundle that picked up a trailing byte, a bootstrap that was reformatted and
// not rehashed: all of them produce a page a browser refuses to run, and all of
// them fail here.
func TestHTMLPolicyHashesTheScriptsThePageActuallyRuns(t *testing.T) {
	t.Parallel()

	page, _ := renderFixture(t)
	policy := extractPolicy(t, page)

	scripts := executableScripts(t, page)
	if len(scripts) != 2 {
		t.Fatalf("the page runs %d scripts, want exactly 2 (the vendored viewer and the bootstrap)", len(scripts))
	}
	declared := hashPattern.FindAllStringSubmatch(policy, -1)
	if len(declared) != 2 {
		t.Fatalf("the policy declares %d script hashes, want 2:\n%s", len(declared), policy)
	}
	allowed := map[string]bool{declared[0][1]: true, declared[1][1]: true}
	for i, script := range scripts {
		sum := sha256.Sum256([]byte(script))
		hash := base64.StdEncoding.EncodeToString(sum[:])
		if !allowed[hash] {
			t.Errorf("script %d (%d bytes, starting %q) hashes to %s, which the policy does not allow:\n%s",
				i, len(script), head(script), hash, policy)
		}
	}
	// The second script is the bootstrap, byte for byte: it is the one this
	// package wrote, and the one a reviewer checks against the hash by hand.
	if scripts[1] != report.Bootstrap {
		t.Errorf("the page's own script is not the bootstrap constant:\n%q", scripts[1])
	}
	// The first is the vendored viewer, byte for byte.
	if scripts[0] != string(vendorassets.Bundle()) {
		t.Error("the inlined viewer is not the vendored bundle byte for byte")
	}
}

// TestHTMLPolicyRefusesEverythingElse reads the rest of the policy, which is
// what makes "this page fetches nothing" a browser-enforced statement rather
// than a comment.
func TestHTMLPolicyRefusesEverythingElse(t *testing.T) {
	t.Parallel()

	page, _ := renderFixture(t)
	policy := extractPolicy(t, page)
	for _, want := range []string{
		"default-src 'none'",
		"style-src 'unsafe-inline'",
		"img-src data:",
		"connect-src 'none'",
		"base-uri 'none'",
		"form-action 'none'",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the policy does not contain %q:\n%s", want, policy)
		}
	}
	if strings.Contains(policy, "unsafe-eval") {
		t.Error("the policy allows eval")
	}
	if strings.Contains(policy, "script-src 'unsafe-inline'") {
		t.Error("the policy allows any inline script, which makes the hashes decoration")
	}
}

// TestHTMLFetchesNothing is the same promise stated against the markup rather
// than against the policy, because the two fail independently: a page can carry
// a strict policy and still be full of `src` attributes that a browser with the
// policy stripped would happily fetch.
//
// The vendored bundle is cut out before the search. It contains URLs — the SVG
// namespace, which is a name rather than an address, and documentation links a
// reader may click — and it is third-party content whose identity is
// established by digest rather than by grepping it. What is checked here is
// everything this package wrote.
func TestHTMLFetchesNothing(t *testing.T) {
	t.Parallel()

	page, _ := renderFixture(t)
	ours := withoutVendoredBundle(t, page)
	for _, forbidden := range []string{"src=", "href=", "<link", "@import", "url("} {
		if strings.Contains(ours, forbidden) {
			t.Errorf("the page contains %q outside the vendored bundle, so it may fetch something", forbidden)
		}
	}
	for _, line := range strings.Split(ours, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "<!--") {
			// The attribution comment names where the bundle came from, which
			// is the one place a URL belongs in this file.
			continue
		}
		if strings.Contains(line, "http://") || strings.Contains(line, "https://") {
			t.Errorf("a URL outside the vendored bundle and outside a comment: %s", head(line))
		}
	}
}

// TestHTMLIslandIsDataAndNotMarkup pins the escaping.
//
// The document is full of `<` — every comparison operator go-mutants mutates is
// one — and a raw `</script>` inside a string would end the element and turn
// the rest of the report into markup.
func TestHTMLIslandIsDataAndNotMarkup(t *testing.T) {
	t.Parallel()

	page, document := renderFixture(t)
	island := extractIsland(t, page)

	if strings.Contains(island, "<") {
		t.Error("the island contains a literal '<'")
	}
	if !strings.Contains(island, `\u003c`) {
		t.Error("the island contains no escaped '<' at all, so the escaping was not exercised")
	}
	if strings.Contains(island, "</script") {
		t.Fatal("the island can close its own element")
	}

	// The escaped island is still the same document: escaping that changed the
	// data would be a worse bug than escaping that did nothing.
	var got, want any
	if err := json.Unmarshal([]byte(island), &got); err != nil {
		t.Fatalf("the island is not JSON: %v", err)
	}
	if err := json.Unmarshal(document, &want); err != nil {
		t.Fatalf("the projection is not JSON: %v", err)
	}
	if !jsonEqual(got, want) {
		t.Error("the island does not decode to the projection it was built from")
	}
}

// TestEscapeScriptDataTable states the five characters and nothing else.
func TestEscapeScriptDataTable(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		in   string
		want string
	}{
		"less than":       {in: `"a < b"`, want: `"a \u003c b"`},
		"greater than":    {in: `"a > b"`, want: `"a \u003e b"`},
		"ampersand":       {in: `"a & b"`, want: `"a \u0026 b"`},
		"closing tag":     {in: `"</script>"`, want: `"\u003c/script\u003e"`},
		"line separator":  {in: "\"a\u2028b\"", want: `"a\u2028b"`},
		"para separator":  {in: "\"a\u2029b\"", want: `"a\u2029b"`},
		"nothing to do":   {in: `{"a": 1}`, want: `{"a": 1}`},
		"already escaped": {in: `"\\"`, want: `"\\"`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := string(report.EscapeScriptData([]byte(tc.in))); got != tc.want {
				t.Errorf("EscapeScriptData(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestHTMLShape checks the handful of elements the page has to have for a
// browser to make anything of it at all.
func TestHTMLShape(t *testing.T) {
	t.Parallel()

	page, _ := renderFixture(t)
	for _, want := range []string{
		"<!doctype html>\n",
		`<meta charset="utf-8">`,
		"<title>",
		"<mutation-test-report-app></mutation-test-report-app>",
		`<script id="report" type="application/json">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not contain %q", want)
		}
	}
	// The element has to be in the document before the bootstrap looks it up,
	// and the bundle has to have defined it before the bootstrap assigns to it.
	app := strings.Index(page, "<mutation-test-report-app>")
	island := strings.Index(page, `id="report"`)
	viewer := strings.Index(page, "<script>")
	boot := strings.LastIndex(page, "<script>")
	if app >= island || island >= viewer || viewer >= boot {
		t.Errorf("the page is out of order: app %d, island %d, viewer %d, bootstrap %d", app, island, viewer, boot)
	}
	// The notice names what was inlined and the digest that was checked.
	if !strings.Contains(page, vendorassets.BundleSHA256) {
		t.Error("the page does not record the digest of the viewer it inlined")
	}
	if !strings.Contains(page, "Apache-2.0") {
		t.Error("the page does not name the licence of the code it inlines")
	}
}

// TestHTMLIsDeterministic proves the page is a function of the document, so
// that two runs over unchanged code produce a file with no diff.
func TestHTMLIsDeterministic(t *testing.T) {
	t.Parallel()

	document := marshalProjection(t, projectionFixture(t), projectionWorkspace(t))
	first, err := report.RenderHTML(document)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	second, err := report.RenderHTML(document)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if string(first) != string(second) {
		t.Error("two renders of one document produced different pages")
	}
}

// TestHTMLRefusesATamperedViewer proves the digest check is a gate rather than
// a warning: when the vendored asset does not match what this build recorded,
// no page comes back at all.
//
// It cannot be parallel; see [report.BreakVendoredViewer].
func TestHTMLRefusesATamperedViewer(t *testing.T) {
	tampered := errors.New("the embedded bundle hashes to something else")
	restore := report.BreakVendoredViewer(tampered)
	defer restore()

	page, err := report.RenderHTML([]byte(`{"schemaVersion":"2"}`))
	if page != nil {
		t.Errorf("a page was rendered from an unvouched-for viewer: %d bytes", len(page))
	}
	if got := report.CodeOf(err); got != report.CodeVendoredAssetTampered {
		t.Fatalf("RenderHTML with a tampered viewer = %v (code %q), want %s",
			err, got, report.CodeVendoredAssetTampered)
	}
	if !errors.Is(err, tampered) {
		t.Error("the cause is not reachable through errors.Is")
	}
}

// extractPolicy returns the policy out of the rendered meta element.
func extractPolicy(t *testing.T, page string) string {
	t.Helper()
	match := cspPattern.FindStringSubmatch(page)
	if match == nil {
		t.Fatal("the page carries no Content-Security-Policy meta element")
	}
	return match[1]
}

// extractIsland returns the text content of the JSON island.
func extractIsland(t *testing.T, page string) string {
	t.Helper()
	const open = `<script id="report" type="application/json">`
	start := strings.Index(page, open)
	if start < 0 {
		t.Fatal("the page carries no JSON island")
	}
	rest := page[start+len(open):]
	end := strings.Index(rest, "</script>")
	if end < 0 {
		t.Fatal("the JSON island is never closed")
	}
	return rest[:end]
}

// withoutVendoredBundle returns the page with the inlined third-party script
// cut out, so that a search finds only what this package wrote.
func withoutVendoredBundle(t *testing.T, page string) string {
	t.Helper()
	bundle := string(vendorassets.Bundle())
	if !strings.Contains(page, bundle) {
		t.Fatal("the page does not contain the vendored bundle, so cutting it out proves nothing")
	}
	return strings.Replace(page, bundle, "", 1)
}

// head is the first 120 characters of a string, for a failure message that has
// to mention a quarter of a megabyte of minified JavaScript.
func head(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= 120 {
		return s
	}
	return s[:120] + "..."
}

// jsonEqual compares two decoded documents.
func jsonEqual(x, y any) bool {
	left, errLeft := json.Marshal(x)
	right, errRight := json.Marshal(y)
	return errLeft == nil && errRight == nil && string(left) == string(right)
}
