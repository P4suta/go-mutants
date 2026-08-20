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
| `killable/` | `fixture.example/killable` | The end-to-end kill. Four mutants with predetermined fates: two boundary mutants in `Clamp` and one boolean literal in `IsReady` that the tests kill, and one in `Untested` that survives because nothing calls it. One function per file and no repeated operator, so a mutant can be named by path and rule alone. |
| `rejectable/` | `fixture.example/rejectable` | Compile validation's oracle. Nine candidates over two files, three of which cannot compile once guarded: a comparison and a boolean literal returned as the named boolean type `Flag`, whose guard evaluates to plain `bool`. The other six are healthy and share both files with the traps, so a phase that rejected a file rather than a candidate would be caught. |

The discovery fixture is the one module in the corpus with no test files, which
is deliberate: `list` builds nothing and runs nothing, so a test here would add
time to the suite without being able to fail for a reason `list` could cause.

The rejectable fixture's `type Flag bool` is load-bearing in the same way its
namesake's bounds are. Making it a type alias — `type Flag = bool` — would leave
every function in the module compiling, every test passing, and every trap in it
silently accepted, so the phase that exists to isolate them would be proved
against a module with nothing to isolate.

The killable fixture is the one whose *behaviour* is load-bearing rather than
only its shape. `Clamp` takes an open range — a value at or below `lo` becomes
`lo+1`, one at or above `hi` becomes `hi-1` — because a clamp with inclusive
bounds returns the bound itself at the bound, which makes `<` and `<=` agree on
every input and turns the boundary mutants into equivalent ones that no test can
kill. Its doc comment says so; changing the bounds back to the inclusive ones a
reader expects would leave the fixture compiling, its own tests passing, and the
integration test waiting for a failure that can no longer happen.

## Who drives them

`internal/engine`'s integration suite runs the whole pipeline — snapshot,
baseline, discovery, compile validation, the instrumented baseline, the drift
gate, execution, and the report — against `simple/`, `killable/`,
`rejectable/`, and `failing-baseline/`. It asserts the exact tally each of them
produces, so the numbers in those tests are the fixtures' documented claims
about themselves stated as data: `killable/` is 3 killed and 1 survived,
`rejectable/` is 6 accepted and 3 rejected, and `simple/` is a green run whose
event sequence is pinned whole. A fixture edited without its test is a fixture
whose claim quietly stopped being true.

Later phases add fixtures for the cases the instrumentation has to get right:
`go.work`, build tags, CRLF sources, a package with no tests, a test that
writes into its own directory, and a declaration whose type cannot be named.
The drift gate that would catch the writing test already exists and is unit
tested against a hand-built snapshot; the fixture is what will prove it end to
end.
