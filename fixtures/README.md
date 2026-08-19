<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Test corpus

Small Go modules that go-mutants runs against in its own tests. Each one is a
whole workspace, not a package: the engine snapshots a directory, builds it, and
runs its tests, so a fixture has to be something a `go` command can be pointed
at.

## Conventions

- **One module per fixture directory**, each with its own `go.mod`. That is what
  keeps the repository's own `./...` from ever picking a fixture up — a fixture
  that failed on purpose would otherwise fail this repository's test run — and
  it is why `mise run test` and `mise run build` never see them. The integration
  tests reach fixtures by path.
- **Module paths under `fixture.example/`.** The domain is reserved for
  documentation by RFC 2606, so no fixture path can ever collide with a module
  somebody might publish, and no `go get` of one can reach the network.
- **No dependencies, ever.** A fixture with a `require` line needs `go.sum`
  entries and a module cache inside the snapshot, which would make the
  integration suite depend on the network. Everything a fixture needs is in the
  standard library.
- **Fast.** The baseline is measured several times before anything else happens,
  and the derived timeout is five times the slowest run, so a slow fixture makes
  the whole suite slow twice over.
- **SPDX headers everywhere**, `go.mod` included: `gofmt -l .` and the licensing
  check both walk the filesystem rather than the module graph, so fixture files
  are held to the same standard as the rest of the tree.

## The corpus

| Fixture | Module | What it is for |
| --- | --- | --- |
| `simple/` | `fixture.example/simple` | The happy path: one package, three small functions, a fast passing test. Baseline succeeds, the timeout is derived, the run completes. |
| `failing-baseline/` | `fixture.example/failingbaseline` | A workspace that compiles but whose test fails. Proves the baseline gate: the run must stop with a typed baseline error carrying the tail of the test output, not proceed to mutate a red suite. |
| `discovery/` | `fixture.example/discovery` | Five packages holding one live candidate next to every context discovery refuses to mutate: all six comparisons and both boolean literals, a package that shadows `true`, const blocks, an array length, switch and type-switch and select labels against their bodies, package-level initialisers, a `//go:embed` variable, a generated file, and generic type parameters against a generic body. `list` asserts the exact catalogue and the exact skip counts against it. |

The discovery fixture is the one module in the corpus with no test files, which
is deliberate: `list` builds nothing and runs nothing, so a test here would add
time to the suite without being able to fail for a reason `list` could cause.

Later phases add fixtures for the cases the instrumentation has to get right:
`go.work`, build tags, CRLF sources, a package with no tests, a test that
writes into its own directory, and a declaration whose type cannot be named.
