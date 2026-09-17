<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Documentation

**Status: this index names every page under `docs/`, and a test refuses one it
does not name.** `internal/testkit/docsindex_test.go` keeps the set here, the
set on disk, and the list in the repository's own README equal — and refuses a
page that does not say, under its title, how much of what it describes is
built.

Every page opens with that **Status** line for one reason: a reader should
never have to guess whether they are reading a description of something that
exists or a plan for something that does not.

## The contracts

What go-mutants is, what it produces, and what it will refuse to do.

- [Command line](command-line.md) — every command and the flags that change a
  verdict rather than a rendering
- [Architecture](architecture.md) — the phases a run passes through, and the
  decisions that shaped each one
- [Mutation operators](operators.md) — every family and rule, the type
  conditions each one is gated on, and every reason a site is skipped
- [Configuration](configuration.md) — every key of `.go-mutants.toml`, what it
  decides, and what it defaults to
- [Diagnostic codes](errors.md) — every `GOM` code a build can print, what it
  means, and what to do about it
- [Limitations](limitations.md) — everything go-mutants will not do, and what
  it does instead
- [Roadmap](roadmap.md) — the work nobody has done yet, and what finishing each
  piece looks like
- [JSON contracts](json-schema.md) — the documents go-mutants writes, and the
  rule for when one of them may change
- [Run trace v1](trace-v1.md) — the diagnostic account a run keeps of itself,
  which is never evidence
- [Stryker report ecosystem compatibility](stryker-compatibility.md) — what the
  one-way projection into the published report format does and does not carry
- [The engine API](library.md) — the module root as a reusable engine, for a
  tool that wants mutation as a measurement rather than as a score

## Working on go-mutants

- [Development](development.md) — the test harness, the two tiers, where a test
  writes, what a failure leaves behind, and how to read it
- [Continuous integration](ci.md) — what runs on every push, what runs nightly,
  and how to run any of it yourself
- [Architecture decision records](adr/README.md) — the decisions that constrain
  code nobody has written yet
- [Release checklist](release-checklist.md) — what has to be true, and proven by
  a runner rather than a laptop, before anything is tagged

## Elsewhere

[CONTRIBUTING.md](../CONTRIBUTING.md) is the shorter road into the page above
it: setup, the gates to run before submitting, and the rules a change has to
keep. [SECURITY.md](../SECURITY.md) is how to report something that should not
be an issue in public.

## The runner

`goatest/` is the second module of this repository, and its documentation has
its own index at [`goatest/docs/README.md`](../goatest/docs/README.md), pinned
to its own directory by `goatest/internal/devgates` in both directions.

It is linked rather than merged. The two products publish different contracts —
`goatest-trace-v1` and `gomutants-trace-v1` reject each other by design, and so
do their reports — so a single index would have to say which product each page
was about on every line, which is what a directory already says. What one
repository buys is that a change to both lands together, not that the two
describe themselves in one voice.
