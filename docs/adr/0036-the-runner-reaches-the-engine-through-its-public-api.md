<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0036. The runner reaches the engine through its public API

## Status

Accepted, 2026-09-17.

## Context

[ADR 0035](0035-one-repository-two-modules.md) put the two products in one
repository and recorded the fact that made it cheap: Go's `internal/` rule is a
path-prefix test, so `github.com/P4suta/go-mutants/goatest/...` may import
`github.com/P4suta/go-mutants/internal/...`. A third module is unnecessary and
external consumers stay shut out.

The same fact is the hazard. Before this change the runner *could not* reach the
engine's internals — the module boundary refused it, and
`goatest/internal/mutationbridge` exists because of that refusal: one file, one
door, the whole external contract frozen behind it. After this change the
refusal is gone and only the habit remains.

That matters more than a layering preference. `docs/library.md` is the contract
the engine publishes, `external_contract_integration_test.go` compiles a
synthetic consumer against it under `GOPROXY=off`, and the reason both are worth
their cost is that the runner is the contract's first and largest consumer. A
runner that reached past the contract would leave it proved only by a test
written to prove it.

## Decision

The runner's production code imports the engine's public API and nothing else.

Three rules, all enforced by `goatest/internal/devgates`:

1. No file under `goatest/` outside `_test.go` imports
   `github.com/P4suta/go-mutants/internal/...`.
2. No file in the engine imports `github.com/P4suta/go-mutants/goatest/...`.
   The language already refuses this — the require would be cyclic — and the
   gate exists so that the refusal is a stated rule rather than a build error
   somebody works around with a `replace`.
3. A `_test.go` under `goatest/` may import `internal/testkit` and no other
   `internal/` package. The harness is shared on purpose; the rest is not.

## Consequences

**`mutationbridge` keeps its reason to exist.** It was one door because a module
boundary made it one; it is one door now because this says so, and the gate
means "somebody decided" rather than "nobody tried".

**A shortcut is refused at the moment it is taken**, which is the only moment
anybody knows why they wanted it.

**Rule 3 is a judgement and will be argued with.** Sharing the harness is what
makes `-update` flags, golden policy and scratch ownership one set of rules
rather than two that drift; sharing anything else would make the runner depend
on the engine's private shape. The line is where it is because the harness is
already test-only and already refuses to import anything from either product.

**The engine still cannot see the runner, and now says why.** Rule 2 is the one
a cyclic require already enforces; writing it down is what stops somebody
reaching for `replace` to get around a build error whose message does not
explain itself.
