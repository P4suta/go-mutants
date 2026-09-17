<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0035. One repository, two modules

## Status

Accepted, 2026-09-17.

## Context

go-mutants is a mutation engine. goatest is an assurance runner that consumes
it, through one file — `goatest/internal/mutationbridge` — and pins it by
pseudo-version. They were developed as two repositories, and the same author's
Rust pair names what that costs by pointing at exactly this one:

> a new proof is a change to two repositories — the engine gains a claim, the
> runner gains a rule, a trace vocabulary, documentation, and an audit layer —
> and a proof without all four is not finished. Two repositories make that a
> sequence of pull requests with a version pin between them.

The cost is not the ceremony. It is that the four halves of a proof land at
different times, so between them the repository is in a state nobody designed:
an engine with a claim nothing consumes, or a runner with a rule about a claim
that does not exist yet. Every check is green throughout.

Three things were measured before this was decided, and each of them decided
part of it.

**`internal/` is a path prefix, not a module boundary.** `cmd/go`'s loader asks
`str.HasPathPrefix(importerPath, parentOfInternal)` and nothing else, so
`github.com/P4suta/go-mutants/goatest/...` may import
`github.com/P4suta/go-mutants/internal/...`. `golang.org/x/tools/gopls` is the
precedent. A third module to hold shared code is therefore unnecessary, and
external consumers stay shut out exactly as before — which is a stronger
guarantee than Cargo's `publish = false`, where anything in the workspace may
depend on anything else.

**The dependency direction is enforced by the language in one direction only.**
The engine cannot import the runner: the runner requires the engine, and a
cycle in `require` does not build. The runner importing the engine's `internal/`
is open, by the paragraph above, and needs a gate rather than a hope.

**`./...` does not cross a module boundary.** `cmd/go`'s package walker returns
`SkipDir` at a nested `go.mod`, silently. Measured here: `go list ./...` names
no package under `goatest/` and `go list ./goatest/...` names thirty-two. A
build task that says "every package" and means one module is a task that can be
green over a runner that does not compile.

## Decision

One repository, two modules, joined by `go.work`.

The engine keeps the import path it has. `docs/library.md` is eighteen hundred
lines about `github.com/P4suta/go-mutants`, `external_contract_integration_test.go`
compiles a consumer against that path, and the family of ports — rust-mutants,
ocaml-mutants, the rest — is named in it. Moving the engine to make the layout
symmetrical would cost all of that to buy nothing.

The runner becomes `github.com/P4suta/go-mutants/goatest`, under `goatest/`,
with its history merged rather than squashed or subtree-added: `git log --
goatest/x.go`, `git blame` and `git log -S` all have to reach, and the first two
do not survive a squash. Every imported commit carries a `Goatest-origin`
trailer naming the commit it came from in the archived repository.

Documentation goes the other way from the Rust pair's. There the engine's pages
moved into `docs/engine/`; here the runner's stay under `goatest/docs/`, because
the engine's are referenced from godoc, from schema descriptions and from the
README, and the runner's eighty-five references were all Markdown in its own
tree.

Every whole-tree pattern names both modules on the same line, and
`internal/devgates/modules_integration_test.go` refuses one that does not — and
takes the `go list` measurement itself, so the rule retires when the toolchain
stops behaving that way.

## Consequences

**A proof is one pull request.** That is the whole of what this buys, and it is
worth the rest.

**Tests become possible that could not be written before.** The runner's
`switch` over the engine's `Outcome` had a `default` that failed at runtime; a
seventh outcome would have shipped green and broken on somebody's machine. A
test in the runner that reads `gomutants.KnownOutcomes()` fails the moment the
engine adds one. Every ledger of that shape was unwritable across two
repositories.

**The runner may import the engine's `internal/`, and must not.** Nothing in Go
stops it. The gate is ADR 0036's.

**`go.work` changes what a development build reports about itself.** The runner
reads the engine's version from `debug.ReadBuildInfo`, and a workspace build has
no version to read, so it falls back to `executable-sha256:<hash>` — which is a
field the assurance contract counts as part of a run's identity. Nothing was
wrong before and nothing is wrong now; what changed is the value's domain, and
the contract page says so.

**The release becomes two tags of one version.** Product-specific versions would
put a pin back between the products — the same defect in a smaller space — so
`v0.2.0` and `goatest/v0.2.0` are cut together from one CHANGELOG.

**`GOWORK=off` is now load-bearing in two places.** The runner refuses a
workspace on purpose: `internal/golang` rejects more than one main module, and
its limitations page calls workspace aggregation deliberately deferred. So every
task that runs the runner sets `GOWORK=off` *and* starts inside `goatest/`,
because `DetectWorkspace` reads `go.work` as a file and walks up to find it.
The engine's own `external_contract_integration_test.go` already set the
variable for its own reasons, which is why the public-contract proof survived
this change untouched.
