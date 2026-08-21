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
| `github.com/santhosh-tekuri/jsonschema/v6` | Validate emitted JSON, and `report validate` | Apache-2.0 |

Their transitive dependencies, mostly terminal and text-handling libraries
pulled in by the Charm packages, are recorded in `go.mod` and `go.sum`.

## Test-only dependencies

| Module | Purpose | License |
| --- | --- | --- |
| `github.com/google/go-cmp` | Structural diffs in assertions | BSD-3-Clause |
| `pgregory.net/rapid` | Property-based testing | MPL-2.0 |

## Planned vendored assets

The HTML report will embed the Mutation Testing Elements viewer bundle,
copyright the Stryker Mutator contributors, under the Apache License 2.0. When
it lands it will live under `vendor-assets/<pkg>/<ver>/` with its license, a
`PROVENANCE.json` recording the upstream integrity hash, and a SHA-256 that is
re-verified at render time. **No such asset is vendored at this commit.**
