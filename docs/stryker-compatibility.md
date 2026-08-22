<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Stryker report ecosystem compatibility

**Status: implemented.** Every run writes
`reports/mutation/mutation.json` — a projection into the Mutation Testing
Report Schema — and `reports/mutation/mutation.html`, a single self-contained
page that embeds it. `--report` decides which of the two, or neither.

## Scope

go-mutants is a Go-native mutation engine that participates in the Stryker
**reporting** ecosystem. The compatibility point is the Mutation Testing Report
Schema and the [Mutation Testing Elements][mte] viewer — not a shared mutation
kernel.

This project is independent of the Stryker project. It is not affiliated with,
endorsed by, or maintained by the Stryker team. The name is used only to
identify the report schema and the viewer this project targets.

[mte]: https://github.com/stryker-mutator/mutation-testing-elements

## What is compatible

- A deterministic projection of a completed run into the report schema,
  emitted as JSON.
- A single self-contained HTML file that embeds that JSON and a vendored copy
  of Mutation Testing Elements.

## What is not compatible, by design

Command-line options, configuration files, the plugin API, the mutation kernel,
operator implementations, the execution protocol, and the cache representation
are all Go-native and are not intended to match Stryker's.

## Two documents, two responsibilities

`go-mutants/run-report` v1 is the authoritative ledger: rejected candidates
with compiler diagnostics, confirmed versus inconclusive timeouts, expectation
states, per-shard `not_run` entries, coverage mapping decisions, cache
provenance, and bounded process output. The shared schema has a smaller model
and cannot hold any of that.

The projection is therefore **one-way and lossy**. It is derived from the
native report after that report has been stored, and it is never read back as
cache or resume state. Nothing in go-mutants parses a
mutation-testing-report file, and nothing should: a format designed for a
viewer is a poor place to keep the facts a run established, and a round trip
through it would quietly become the thing everything else trusts. Anything that
diagnoses, resumes, or audits a run reads the native document.

It is also **deterministic**. Two runs that reach the same outcomes over the
same tree produce byte-identical projections: object keys are sorted by the
JSON encoder, and every array is sorted explicitly — by position, then mutator
name, then id — rather than left in the order a worker pool happened to finish
in. `projectRoot` is deliberately omitted although the format allows it: it is
an absolute path on the machine that produced the report, which would both
break that determinism and leak a developer's directory layout into a file that
gets attached to pull requests.

## Outcome mapping

Six go-mutants outcomes and the `rejected[]` ledger become six statuses — the
six of the eight the format defines that this projection ever emits:

| go-mutants | Projected status | `statusReason` |
| --- | --- | --- |
| `killed` | `Killed` | — |
| `survived` | `Survived` | — |
| `survived`, uncovered | `Survived` | — |
| confirmed `timeout` | `Timeout` | — |
| `inconclusive` | `Ignored` | It timed out once and did not time out again when retried alone, so it counts neither as a detection nor as a survivor |
| `not_run` | `Ignored` | Worded per reason: the run was interrupted, the run narrowed itself with `--mutant`/`--changed`, or another shard measured it |
| `error` | `RuntimeError` | — |
| a `rejected[]` row | `CompileError` | The first line of the compiler diagnostic |

Three things about this table are decisions rather than mechanics.

**`NoCoverage` is never emitted.** It looks like the right answer for a mutant
no test binary reaches, and it is not the one this projection gives: the run
report's own vocabulary calls that mutant a survivor, and the two documents
must agree about how many survivors there were. Which survivors were uncovered
is a fact the run report keeps and this one drops — that is what "lossy" means,
stated once, in the place a reader would otherwise assume a bug.

**`not_run` is projected, not omitted.** A mutant left out of a sharded or
`--changed` run is a real row in the catalogue, and dropping it would make the
viewer's totals disagree with the run report's. It arrives as `Ignored` with a
reason a reader of the viewer can act on, because they have no run report in
front of them and no way to guess which of three quite different things
"ignored" meant.

**`Pending` is never emitted.** It describes a run still in progress, and every
document go-mutants writes describes a run that has stopped.

A rejected candidate is thinner than a measured one, and both limits are the
rejection row's rather than choices: the row records the rule but not the
family, so `mutatorName` is the rule alone, and it records the coordinate
discovery printed rather than the span, so the location is the zero-width point
where the edit would have started. Claiming an end the document does not know
would be inventing a range.

The remaining projected fields, for completeness: a measured mutant's
`mutatorName` is `family/rule` (the rejection case above is the thin
exception), its `description` is `original -> replacement`, and every file
entry carries `language: "go"`. Should the projector ever meet an outcome it
does not recognize — which would be a go-mutants bug, not a document state —
it emits `Ignored` with the unknown outcome quoted in `statusReason` rather
than inventing a status the viewer would present as evidence.

## Coordinates: the UTF-16 rule

go-mutants locates a mutant by a half-open **byte** range, because it splices
bytes. The published format locates one by a half-open pair of `(line, column)`
positions, both 1-based, and its column is counted in **UTF-16 code units** —
because the viewer is JavaScript, and a JavaScript string index is a UTF-16
code unit index. The three counts diverge the moment a line is not pure ASCII:

```text
"a¥b"    bytes 1,2,1   runes 1,1,1   UTF-16 units 1,1,1
"a🎉b"   bytes 1,4,1   runes 1,1,1   UTF-16 units 1,2,1
```

Handing over byte columns would place every mutant after a multi-byte rune too
far right; handing over rune columns would place every mutant after an astral
character — an emoji in a test name, a mathematical symbol in a comment — too
far left. Both are silently valid against the schema, which requires only that
the numbers be at least 1, so neither would ever be caught by validation. A
rune outside the Basic Multilingual Plane counts as two units; everything else
counts as one.

CRLF gets no special case, deliberately. A line ends at its `\n`, and the `\r`
before it is an ordinary character on the line it terminates — exactly how a
JavaScript viewer that split the same source on `\n` would count it.

The source embedded per file is the **pristine** text from the user's tree, not
the instrumented snapshot: a projection built from instrumented source would
show a reader the guard scaffolding instead of their own code, with every
location pointing into it. Because the projection reads the tree again after
the run copied it, it re-checks that each span still covers the text the report
says it covers, and refuses with `GOM5202` when it does not — editing a file
while a run is in flight is what a developer does while waiting, and every
coordinate derived from a moved span would be wrong in a document that would
still validate.

## Thresholds are not policy

`thresholds.high` and `thresholds.low` come from `report.high` and
`report.low`, and they are independent of `[policy]`. They colour the viewer's
score and decide nothing else. Making a report prettier must never change
whether CI passes, and the two settings are kept apart so that nobody can do it
by accident; `policy.strict` and `policy.minimum_score` are the only things
that decide an exit status. See [`configuration.md`](configuration.md).

## Validation gate

The schema is vendored at `schema/stryker/`, pinned by version and SHA-256
alongside its Apache-2.0 license and a `PROVENANCE.json`. It is honoured as
upstream published it: the compiler is given no default draft, because the file
declares draft-07 in its own `$schema` and forcing 2020-12 on it would silently
change what `definitions` and `additionalProperties` mean in somebody else's
document.

Every projection is validated against it **before anything is written**, and a
document that fails validation aborts with `GOM5203` having touched nothing. A
projection that appears authoritative and is not would be worse than no
projection at all — and because the check runs on the way out, a file that
exists on disk is one another tool will accept.

One trap is worth naming, because the schema itself catches it: the
`schemaVersion` a document carries is `"2"`, the major version of the report
format. It is *not* the version of the npm package the schema was vendored
from, which is 3.9.0. The schema's own pattern for the field is
`^([1-2])(\.(([1-9]\d*)|0)){0,2}$`, so a document claiming `"3"` is refused by
the very schema whose package name suggested it.

## HTML report

One file, and it works with the network unplugged.

The viewer bundle is vendored under
`vendor-assets/mutation-testing-elements/3.9.0/` with its license, its
provenance, and its digest. Its SHA-256 is re-checked **at render time**, on
every page, against both the constant in `vendor-assets` and the digest
recorded in its `PROVENANCE.json` — checking it once at build time would prove
something about the machine that built the binary, whereas checking it here
proves something about the bytes about to be written into a file somebody will
open and trust. A mismatch aborts with `GOM5210` rather than producing a report
with an unvouched-for quarter-megabyte of JavaScript in it.

Nothing in the page causes a network request. The viewer's JavaScript is
inlined and the data is inlined as a JSON island. The only URLs in the file
are inside the vendored bundle — the SVG namespace (a name, not an address),
documentation hyperlinks a reader may click, and `data:` image URIs — plus
the attribution comment; `default-src 'none'` blocks every fetch regardless.

The page carries a strict `Content-Security-Policy` even though nothing served
it. This is not defence against the file's author, who is go-mutants; it is a
*statement* that the page needs no network, enforced by the browser rather than
asserted in a comment. `default-src 'none'` means a future edit that adds a
font, a tracker, or a "check for updates" fetch does not silently work — it
breaks loudly, in review, instead of turning a report somebody attached to a
pull request into a beacon.

The two executable scripts are allowed by SHA-256 hash and by nothing else: no
`'unsafe-inline'`, and no nonce, because a nonce in a static file is a constant,
which is `'unsafe-inline'` with extra steps. The hashes are computed from the
very strings the renderer concatenates, so a change to either script that
forgot to update the policy produces a page that refuses to run rather than one
that runs something unvouched-for.

The JSON island is not hashed and does not need to be. A `<script>` whose type
is not a JavaScript MIME type is a *data block*: the HTML parser returns from
"prepare the script element" before the CSP check, and nothing in it is ever
executed. It is escaped instead, which is the protection that actually matters
for it. Five characters are respelled as JSON escapes: `<` becomes `\u003c`,
because a `</script>` inside a string in the data would end the element and the
rest of the document would be parsed as markup — and every comparison operator
go-mutants mutates is a `<`, so this is an ordinary case rather than an exotic
one. `&` and `>` follow for the same reason Go's own encoder escapes them:
there is no context in which a raw one is needed, and escaping all three makes
the island safe under any parser rather than under a correct one. `U+2028` and
`U+2029` are the JavaScript trap rather than the HTML one — JSON permits them
raw inside a string, and ECMAScript before ES2019 treated them as line
terminators, so a `JSON.parse` of text containing one used to be a syntax
error. Each replacement is a valid JSON escape for exactly the character it
replaces, and all five occur only inside string literals, so the escaped island
parses to the identical document.

## Publication is atomic, and the pair is one publication

The two files are one publication in two formats. Within one publication, a
`mutation.json` from this run beside a `mutation.html` from last week would be
worse than either file alone, because the two disagree and nothing in either
says which is newer. So if the HTML cannot be written, the JSON written
moments before is put back exactly as it was found — restored, or removed
when there was none — and the run reports the failure rather than a
half-published pair. (A deliberate single-format run — `--report json` over a
directory holding an older pair — is the user's own composition; go-mutants
never deletes a sibling artifact it was not asked to write.)

Both files are staged in the destination directory and renamed into place, so a
crash leaves either the previous pair or the new one, never a mixture. The
project artefacts are written **after** the run's own record is filed in the
history store, never before: the history is what a later run, a `report merge`,
or a `report latest` reads, and publishing the other way round would mean a
crash between the two left a workspace holding a mutation report for a run that
has no record.
