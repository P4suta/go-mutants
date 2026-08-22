<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Third-party notices

`go-mutants` links the Go modules below. Each remains under its own license;
the authoritative versions and digests are `go.mod` and `go.sum`.

## Runtime dependencies

| Module | Purpose | License |
| --- | --- | --- |
| `github.com/spf13/cobra`, `github.com/spf13/pflag` | Command tree and flags | Apache-2.0, BSD-3-Clause |
| `github.com/pelletier/go-toml/v2` | Strict TOML decoding with positions | MIT |
| `github.com/charmbracelet/bubbletea` | TUI runtime | MIT |
| `github.com/charmbracelet/bubbles` | TUI components | MIT |
| `github.com/charmbracelet/lipgloss` | TUI styling | MIT |
| `golang.org/x/tools` | `go/packages` analysis | BSD-3-Clause |
| `golang.org/x/sync` | `errgroup` worker coordination | BSD-3-Clause |
| `golang.org/x/mod` | Reading the `module` line of a `go.mod` | BSD-3-Clause |
| `github.com/santhosh-tekuri/jsonschema/v6` | Validate emitted JSON, and `report validate` | Apache-2.0 |

Their transitive dependencies, mostly terminal and text-handling libraries
pulled in by the Charm packages, are recorded in `go.mod` and `go.sum`.

## Test-only dependencies

| Module | Purpose | License |
| --- | --- | --- |
| `github.com/google/go-cmp` | Structural diffs in assertions | BSD-3-Clause |
| `pgregory.net/rapid` | Property-based testing | MPL-2.0 |

## Vendored assets

These are not Go modules. They are files copied into this repository verbatim
and compiled into the binary with `go:embed`, so they are not covered by
`go.mod` and `go.sum` and are recorded here instead.

| Asset | Version | Used for | License |
| --- | --- | --- | --- |
| [Mutation Testing Elements][mte] (`mutation-test-elements.js`) | 3.9.0 | The viewer the HTML report embeds | Apache-2.0 |
| [Mutation Testing Report Schema][mtrs] (`mutation-testing-report-schema-3.9.0.json`) | 3.9.0 | Validating the projection before it is written | Apache-2.0 |

Both are copyright the Stryker Mutator contributors. The viewer lives under
`vendor-assets/mutation-testing-elements/3.9.0/` and the schema under
`schema/stryker/`; each sits beside the upstream `LICENSE` text and a
`PROVENANCE.json` recording the URL it was fetched from, the npm integrity
hash, when it was retrieved, and its SHA-256.

Neither file carries an SPDX header, deliberately: a vendored file is verified
by digest, and a header added to it would be a modification that breaks the
check proving the file is the one upstream published. `REUSE.toml` annotates
them instead. The viewer's digest is re-verified in-process every time a page
is rendered, against both the constant in `vendor-assets` and the digest in its
`PROVENANCE.json`, and a mismatch aborts the report rather than writing a
quarter-megabyte of unvouched-for JavaScript into a file somebody will open.

This project is not affiliated with, endorsed by, or maintained by the Stryker
team; see [`docs/stryker-compatibility.md`](docs/stryker-compatibility.md).

[mte]: https://github.com/stryker-mutator/mutation-testing-elements
[mtrs]: https://github.com/stryker-mutator/mutation-testing-elements/tree/master/packages/report-schema
