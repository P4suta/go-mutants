<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0006 — Selection is advisory and takes no part in the prepared digest

## Status

Accepted, 2026-09-07. Implemented by `PrepareOptions.Selection`,
`Catalog.Selection` and `Mutant.Selected` (#51).

## Context

A tool that runs mutation over "what changed" wants to measure the mutants on the
lines somebody edited. Before this existed, the library could only be asked for
the whole catalogue, leaving the consumer to drop the mutants whose `Path` the
diff did not name — which keeps *every* mutant in an edited file. Two hundred of
them, for one edited line.

Narrowing by line is therefore worth having. The question is where in the
pipeline it applies, and there are two answers with very different consequences.

Applying it **before discovery** is the cheap one: discover less, validate less,
instrument less. It also changes what the catalogue *is*. `Catalog.Digest`, the
mutant ids, the accepted set and the rejection diagnostics would all become
functions of the caller's diff — so two consumers asking about one commit, with
diffs computed against different bases, would get two catalogues that cannot be
compared, and a stored id could not be looked up again.

Applying it **after** costs the preparation nothing is saved on, and keeps the
catalogue a fact about the code.

There is a second, sharper question underneath, and it is the one worth deciding
in writing: whether a selection belongs in `Catalog.PreparedDigest`. That digest
is what a consumer keys per-mutant evidence on — *this mutant survived against
this prepared tree*. If a selection moved the key, the first narrowed run would
miss on every row anybody had ever stored, and would then re-measure a module's
worth of mutants to write down answers it already had.

## Decision

A selection describes the caller's plan, not the session. It applies after
discovery and validation, and it is invisible to every identity the engine
publishes.

1. **Applied after discovery and validation.** `Catalog.Digest`, every mutant id,
   `Accepted`, `Probed` and `Rejections` are identical to those of an unnarrowed
   run of the same workspace. A narrowed preparation and a full one differ in
   exactly one field per mutant.

2. **`Mutant.Selected` is the only thing a selection changes.** It is true for
   every mutant when no selection was given, so a consumer that never narrows
   reads it as "yes" and never has to ask whether it was narrowing.

3. **The narrowing is advisory.** `Session.Exec` runs an unselected mutant
   exactly as it runs a selected one, without complaint. A plan for a run is not
   a rule about what may be measured, and a consumer that finds a survivor and
   wants its neighbours executed must not have to prepare the module a second
   time.

4. **`Selected` is not hashed into `Catalog.PreparedDigest`**, and the exclusion
   is exhaustive and documented as part of the recipe. Evidence keyed on that
   digest is a fact about the tree, the toolchain and the mutant — none of which
   a selection touches.

5. **The rule that makes point 4 safe belongs to the caller, and it is one
   line: never store "not run, out of selection" as evidence.** An unselected
   mutant was not measured, so there is nothing about it to record. Recording an
   absence as a result is the only way two sessions under one key could come to
   disagree.

6. **A selection is validated before discovery.** A path or a range the engine
   cannot read is `ErrInvalidSelection`, raised before any work is done, rather
   than a silently empty result. `Catalog.Selection` returns the normalised copy,
   so a consumer can see what its request became.

## Consequences

- Preparation costs the same whether a caller narrows or not. That is the price
  of the catalogue staying a fact about the code, and it is the right way round:
  the expensive part of a narrowed run is executing mutants, and that is the part
  the selection actually removes.
- A consumer must not read "not selected" as a statement about the mutant. An
  unselected mutant is not uninteresting, not equivalent and not out of scope —
  it is one the caller did not set out to measure this time.
- A consumer scoring a narrowed run has to say which lines the score covers.
  `Catalog.Selection` is that answer; reporting the number as the module's score
  would be reporting a different measurement under the same name.
- Two prepared sessions of one workspace with different selections share a
  digest, and therefore a cache. That is the whole benefit, and it is only sound
  because of point 5.
- Should a future engine want to *save* work on a narrowed preparation — skipping
  validation for mutants nothing will execute — it cannot do it under this
  decision without changing what `Accepted` and `Rejections` mean. That would be
  a new ADR superseding this one, not a tuning of it.
