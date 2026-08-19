<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Mutation operators

**Status: discovery only, two families.** `comparison` and `boolean-literal`
are discovered today — `go-mutants list` enumerates their mutants with stable
IDs and coordinates — and no other family is. Discovered is not executed: no
mutant of any family is instrumented or run yet, so a row marked *discovered*
means its IDs exist and are reviewable, not that it kills anything.

The rest of this page is the agreed v1 catalogue that the remaining discovery
and instrumentation work builds against; it is written down first so that rule
names, versions, and profile tiers are decided before any mutant ID is minted.
When a family lands, its row gains a marker and the fixtures that prove it.

Every rule is versioned (`add-to-sub@1`). The version participates in the
stable mutant ID, so changing what a rule emits changes the identity of its
mutants and invalidates cached outcomes instead of silently reusing them.
`--operator NAME` and `mutation.operators` select whole families by the names
below; `--mutant ID_PREFIX` resolves against the complete catalogue regardless
of the selected profile.

## Catalogue

| Family | Rules | Tier | Status |
| --- | --- | --- | --- |
| `boolean-literal` | `true-to-false`, `false-to-true` | balanced | discovered |
| `condition-negation` | `negate-condition`, `negate-loop-condition`, `remove-negation` | balanced | planned |
| `boolean-connective` | `and-to-or`, `or-to-and` | balanced | planned |
| `comparison` | `eq-to-neq`, `neq-to-eq`, `lt-to-le`, `le-to-lt`, `gt-to-ge`, `ge-to-gt` | balanced | discovered |
| `integer-arithmetic` | `add-to-sub`, `sub-to-add`, `mul-to-div`, `div-to-mul`, `rem-to-mul` | balanced | planned |
| `float-arithmetic` | `fadd-to-fsub`, `fsub-to-fadd`, `fmul-to-fdiv`, `fdiv-to-fmul` | balanced | planned |
| `return-replacement` | `return-zero-numeric`, `return-empty-string`, `return-true`, `return-false`, `return-nil` | balanced | planned |
| `error-swallowing` | `return-err-to-nil`, `nil-error-branch` | balanced | planned |
| `bitwise` | `band-to-bor`, `bor-to-band`, `xor-to-band`, `shl-to-shr`, `shr-to-shl`, `andnot-to-band` | strong | planned |
| `arithmetic-assignment` | `add-assign-to-sub-assign`, `sub-assign-to-add-assign`, `incr-to-decr`, `decr-to-incr` | strong | planned |
| `statement-deletion` | `delete-call-statement`, `delete-assignment`, `delete-incdec` | all | planned |

That is 11 families and 42 enumerated rules. The design plan's headline said
43 while its own table listed 42; the registry has settled it in favour of the
table. `mutation.CanonicalRuleCount` is 42 and the canonical registry tests
assert it, so the count cannot drift again without a test failing.

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

These are recorded as skips with a reason and never silently dropped.
`go-mutants list` already prints the breakdown, and `--json` carries every
skip with its reason; `--explain` is the planned per-skip detail view:

The reason strings below are the exact identifiers `internal/discover` emits
(they appear verbatim in `list` output and in catalog/report JSON):

| Reason | Why |
| --- | --- |
| `const-decl` | Constant expressions must stay constant (covers `iota`) |
| `array-length` | `[N]T` lengths are not runtime-evaluated expressions |
| `type-param` | Type parameter lists, constraints, and type arguments are not value code |
| `case-label` | `switch`/`select` label expressions; v1 limitation, planned for v2 |
| `package-var-init` | Initialization order hazards; covers `//go:embed` vars; v1 limitation |
| `cgo` | cgo packages are excluded wholesale |
| `generated` | Matches `^// Code generated .* DO NOT EDIT\.$` |
| `excluded` | The file matched a configured `mutation.exclude` pattern |

Reserved reasons that later phases emit: `struct-tag`, `label-or-goto`, and
`unnameable-decl-type` (Form D cannot name the declared type). `_test.go`
files are built and run but never mutated; that is inherent, not a recorded
skip.

## Planned for v2

`if`-branch replacement, map/slice neutral values, and `switch`/`select` case
mutation. They are deliberately out of v1 because each needs either a new
guard form or a type-directed neutral-value model that the instrumentation
phase does not build yet.
