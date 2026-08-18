<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Mutation operators

**Status: planned.** No operator is implemented yet. This page is the agreed
v1 catalogue that the discovery and instrumentation phases build against; it
is written down first so that rule names, versions, and profile tiers are
decided before any mutant ID is minted. When a family lands, its row gains an
implemented marker and the fixtures that prove it.

Every rule is versioned (`add-to-sub@1`). The version participates in the
stable mutant ID, so changing what a rule emits changes the identity of its
mutants and invalidates cached outcomes instead of silently reusing them.
`--operator NAME` and `mutation.operators` select whole families by the names
below; `--mutant ID_PREFIX` resolves against the complete catalogue regardless
of the selected profile.

## Catalogue

| Family | Rules | Tier |
| --- | --- | --- |
| `boolean-literal` | `true-to-false`, `false-to-true` | balanced |
| `condition-negation` | `negate-condition`, `negate-loop-condition`, `remove-negation` | balanced |
| `boolean-connective` | `and-to-or`, `or-to-and` | balanced |
| `comparison` | `eq-to-neq`, `neq-to-eq`, `lt-to-le`, `le-to-lt`, `gt-to-ge`, `ge-to-gt` | balanced |
| `integer-arithmetic` | `add-to-sub`, `sub-to-add`, `mul-to-div`, `div-to-mul`, `rem-to-mul` | balanced |
| `float-arithmetic` | `fadd-to-fsub`, `fsub-to-fadd`, `fmul-to-fdiv`, `fdiv-to-fmul` | balanced |
| `return-replacement` | `return-zero-numeric`, `return-empty-string`, `return-true`, `return-false`, `return-nil` | balanced |
| `error-swallowing` | `return-err-to-nil`, `nil-error-branch` | balanced |
| `bitwise` | `band-to-bor`, `bor-to-band`, `xor-to-band`, `shl-to-shr`, `shr-to-shl`, `andnot-to-band` | strong |
| `arithmetic-assignment` | `add-assign-to-sub-assign`, `sub-assign-to-add-assign`, `incr-to-decr`, `decr-to-incr` | strong |
| `statement-deletion` | `delete-call-statement`, `delete-assignment`, `delete-incdec` | all |

That is 11 families and 42 enumerated rules. The design plan's headline says
43; the enumeration in the same plan lists 42. The registry phase owns the
reconciliation — either a named 43rd rule is added deliberately or the
headline is corrected — and the catalogue schema will assert the count so the
two can never drift again.

## Type conditions

Go's type system does the work an untyped rewriter would have to guess at, and
discovery uses `go/types` evidence rather than syntax alone:

- `integer-arithmetic` requires integer operands. String concatenation with
  `+` is excluded by the operand type, not by a spelling heuristic.
- `float-arithmetic` requires floating-point operands; complex arithmetic is
  out of scope for v1.
- `bitwise` and `arithmetic-assignment` require integer operands, and the
  shift rules keep the shift count untouched.
- `return-replacement` requires a nameable zero-ish value for the declared
  result type at that return position. Anything else is a recorded skip.
- `error-swallowing` requires the value to be assignable to `error`. This is
  the Go-specific family with the highest expected yield: `return err` becoming
  `return nil`, and an `if err != nil` branch that no longer fires, are the two
  failure modes Go test suites most often miss.

## Profiles

Profiles are monotonically inclusive tiers, matching the sibling projects:

```text
balanced  ⊂  strong  ⊂  all
```

`balanced` is the default and holds the eight families whose survivors almost
always indicate a real testing gap. `strong` adds `bitwise` and
`arithmetic-assignment`, which are valuable but noisier in code that does bit
manipulation for performance rather than for semantics. `all` adds
`statement-deletion`, including `append` removal, which is the classic source
of equivalent mutants in logging and metrics code.

## Deduplication

When two families produce the exact same byte edit at the same span, the
catalogue keeps one deterministically: **the more local rule wins**. A
`true-to-false@1` edit beats a whole-condition negation that happens to
produce the same bytes. Users never pay to run two IDs that mutate identical
source.

## Documented exclusions

These are recorded as skips with a reason and surfaced by `--explain`; they
are never silently dropped:

| Reason | Why |
| --- | --- |
| `const-decl`, `iota-expr` | Constant expressions must stay constant |
| `array-length`, `struct-tag` | Not runtime-evaluated expressions |
| `type-param-list`, `type-arg` | Generic constraints are not value code |
| `switch-case`, `select-case` | v1 limitation; planned for v2 |
| `package-level-var-init` | Initialization order hazards; v1 limitation |
| `go-embed-decl` | `//go:embed` declarations must stay literal |
| `label-or-goto` | Control-flow labels are not expressions |
| `cgo-package` | cgo packages are excluded wholesale |
| `generated-file` | Matches `^// Code generated .* DO NOT EDIT\.$` |
| `test-file` | `_test.go` files are built and run, never mutated |
| `unnameable-decl-type` | Form D cannot name the declared type |

## Planned for v2

`if`-branch replacement, map/slice neutral values, and `switch`/`select` case
mutation. They are deliberately out of v1 because each needs either a new
guard form or a type-directed neutral-value model that the instrumentation
phase does not build yet.
