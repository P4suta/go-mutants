<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Mutation operators

**Status: every family is executed.** All eleven families and all forty-two
rules are found by `go-mutants list` with stable IDs, coordinates, and the
guard-site hint the instrumentation phase consumes, and `go-mutants run`
instruments, compile-validates, executes, and scores every one of them through
the three guard forms.

The **Status** column that used to sit in the table below is gone rather than
filled in with one repeated word: it recorded the gap between "the rule mints
an ID" and "`run` can score it", and there is no longer a rule on the wrong side
of it. `fixtures/families` is where that claim is held — one module carrying at
least one live candidate per rule, whose integration test fails if any family
stops reaching execution.

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

That is 11 families and 42 enumerated rules. The design plan's headline said
43 while its own table listed 42; the registry has settled it in favour of the
table. `mutation.CanonicalRuleCount` is 42 and the canonical registry tests
assert it, so the count cannot drift again without a test failing.

## Type conditions

Go's type system does the work an untyped rewriter would have to guess at, and
discovery uses `go/types` evidence rather than syntax alone. Every gate below
reads through a named type to its underlying one — `type Celsius float64` adds
and subtracts exactly like a `float64` — and none of them reads the spelling of
the source:

- `condition-negation` requires the condition, or the `!` operand, to be
  boolean underneath. A named boolean type qualifies: `!` applies to any
  boolean type.
- `boolean-connective` needs no gate. Both operands of `&&` and `||` are
  boolean by construction.
- `integer-arithmetic` requires both operands to be integers. String
  concatenation with `+` is excluded by the operand type, not by a spelling
  heuristic.
- `float-arithmetic` requires both operands to be floating-point; complex
  arithmetic is out of scope for v1, which is why the gate asks for a
  floating-point type rather than a numeric one.
- `bitwise` requires integer operands, and a shift is gated on its left operand
  alone: the count is an operand of a different kind and is never rewritten.
- `arithmetic-assignment` requires the assigned variable to be an integer or a
  float. `s += "!"` is concatenation and is excluded like `+` between strings.
- `return-replacement` requires the declared result type at that return
  position to have a zero-ish literal: numeric becomes `0`, string becomes
  `""`, boolean becomes both `true` and `false`, and a pointer, slice, map,
  channel, function, or non-`error` interface becomes `nil`. A type parameter
  is refused — its underlying type is its constraint, so an unwary reading
  would offer `return nil` for a function returning an `int`. A value that is
  already spelled as its own replacement produces no candidate and no skip:
  the mutation and the source would be the same program.
- `error-swallowing` owns the values whose static type is exactly `error`, and
  `return-replacement` owns every other nillable result. `return err` is
  therefore `return-err-to-nil` and `return &myErr{}` from the same function is
  `return-nil`. `nil-error-branch` replaces a whole `err != nil` comparison
  with `false`, in either operand order, when the compared value implements
  `error`. This is the Go-specific family with the highest expected yield:
  `return err` becoming `return nil`, and an `if err != nil` branch that no
  longer fires, are the two failure modes Go test suites most often miss.
- `statement-deletion` deletes an expression statement that is a call, a plain
  `=` assignment (`x = append(x, e)` included), and an `++`/`--`. It never
  deletes a `:=`, which would make every later use of the name a compile error,
  and it never deletes a call to the builtin `panic`: removing a terminating
  panic leaves a path that reaches the closing brace without returning, which
  manufactures a missing-return error wholesale in exactly the defensive code
  where the mutant would have been interesting.

## Guard site hints

Discovery is the only phase with type information, so it is the phase that
decides which of the three rewrite forms the instrumenter has to use, and hands
that down with every candidate. Walking outward from the edit:

- the nearest enclosing expression whose static type is **exactly** the
  universe `bool` — not a named boolean type, whose values a `bool`-valued
  selector cannot be assigned to — is a **Form C** site;
- otherwise the nearest enclosing statement, which is a **Form S** site when it
  declares nothing (`ExprStmt`, `return`, an assignment that is not `:=`,
  `++`/`--`, send, `defer`, `go`) and a **Form D** site when it does (`:=`, or
  a `var` declaration with an initialiser). A Form D hint carries the source
  spelling of every type the site declares, rendered against the file's own
  imports.

Both searches stop at the enclosing function, so a site is never chosen from
outside the function literal an edit sits in.

Everything else is refused, and a refused candidate is never catalogued. All
six refusals are recorded as `unnameable-decl-type`, which reads as "v1's
guard forms cannot express this site":

- a statement no form covers (a `switch` tag, a `range` clause, an `if` whose
  condition is a named boolean type);
- a statement in a position where a block is not legal Go (an `if`, `switch`,
  or `for` initialiser, a `for` post statement, a type switch guard);
- a Form D type that cannot be spelled with the imports the file already has;
- a `:=` that redeclares an existing variable rather than declaring every name
  afresh, which is a v1 restriction rather than a fact about Go;
- a Form D site whose initialiser mentions a name that same site declares. Go
  begins a declared name's scope at the **end** of its own specification, so
  `total := total * 2` and `err := fmt.Errorf("…: %w", err)` read the enclosing
  declaration; hoisting the new one out in front of the assignment would rebind
  them to a zero value. The rewritten program usually still compiles, which is
  why this is refused rather than left to the compiler. The whole of a `var`
  block's names are weighed against the whole of its initialisers, because the
  block is one site and one spec may name another's;
- a Form D `var` whose declaring tokens cannot be cut out without moving a
  line: a spec with no initialiser, or a spelled-out type, written across more
  than one line. The rewrite removes the whole of the first and the type of the
  second, and removing a line break moves every line after it.

## Branch proof

Four edits can only make a condition *less* often true. When one of them lands
in the condition of an `if` or a `for`, discovery records the span of the body
that condition gates, and publishes it as the mutant's `branch` in
[both JSON documents](json-schema.md#branch).

| Rule | Edit | Why it narrows |
| --- | --- | --- |
| `le-to-lt` | `<=` → `<` | Drops the equal case |
| `ge-to-gt` | `>=` → `>` | Drops the equal case |
| `or-to-and` | `\|\|` → `&&` | Requires both operands where one sufficed |
| `nil-error-branch` | `X != nil` → `false` | True of nothing |

Write C for the original condition and C′ for the mutant's. In each row
C′ ⟹ C, and that is the whole lemma:

> If no statement of the gated body ran during a test, C was false every time it
> was evaluated. C′ ⟹ C, so C′ was false there too, the branch taken was
> identical on every evaluation, and — the condition having no effects — the two
> programs ran identically. **That test cannot have observed the mutant.**

A consumer holding per-test coverage can therefore discharge every such test
without executing it. Nothing else may be inferred from `branch`: it is a
one-way statement about a body nobody entered, never about one they did.

The increasing edits are deliberately absent. `lt-to-le`, `gt-to-ge` and
`and-to-or` widen a condition, so the mutant may enter a body the original never
did — exactly the case coverage cannot rule out. `eq-to-neq` and `neq-to-eq`
move it in neither direction, because the two comparisons are true of disjoint
sets of inputs rather than nested ones.

### The conditions a proof has to satisfy

A proof is stated only when **all** of these hold; otherwise there is simply no
`branch`. The refusal is silent and is not a skip: nothing was declined to be
mutated, only reasoned about.

- **Monotone path.** Walking outward from the edit, only parentheses and the two
  short-circuit connectives may be crossed, and the walk has to end at the `if`
  or `for` whose condition it arrived at. `A && B` and `A || B` are monotone in
  both operands, so narrowing an operand narrows the whole; a `!` inverts the
  implication, and a boolean `==` or `!=` destroys it. An `else if` works
  naturally, because the inner `if` is where the walk ends. An `if`'s
  initialiser is not part of its condition and is never inspected: it runs
  before the condition is evaluated, and the mutant runs it too.
- **The whole condition is inert** — no effects, no possible panic, guaranteed
  to terminate — and not merely the part the edit touches. The mutant may
  evaluate *fewer* sub-expressions than the original: `X != nil` becomes
  `false`, which evaluates nothing; `A || B` becomes `A && B`, which stops
  evaluating `B` when `A` is false; and once an operand short-circuits, every
  operand after it stops being evaluated too. An effect or a panic in something
  the mutant skips is an observable difference even when both take the same
  branch. Inertness is an allowlist over the syntax, so the honest default for a
  construct nobody has thought about is "no proof": names, literals, field
  selections that dereference no pointer, `!` `-` `+` `^`, the arithmetic,
  bitwise and ordering operators, `/` `%` `<<` `>>` with a *constant* right
  operand, `==`/`!=` against `nil` or between types comparable without a panic,
  conversions that are not to an array, and `len` `cap` `min` `max` `real`
  `imag` `complex`. Every other call, and every index, slice, type assertion,
  receive, dereference and composite literal, is refused.
- **The body holds at least one statement.** `cmd/cover` records an empty body
  as a block of zero statements whose coordinates differ between releases, so
  "did it run" has no single answer across profiles — and a branch that does
  nothing is one no test can observe either way, so refusing costs nothing.
- **No `//line` directive over either brace.** `cmd/cover` attributes a block to
  the file name the directive gives, so the span would be measured in one file's
  numbering and compared against blocks recorded under another's.

### Why the braces, and not the first statement

The span runs from the body's opening brace to its closing brace, inclusive,
because `cmd/cover` does not record a body block the same way in every release.
For

```go
if a <= b {
	return 1
}
```

on lines 4 to 6, Go 1.26.6 records the body block as `4.12,6.3` — starting at
the `{` and ending one past the `}` — while Go 1.27.0 records `5.3,6.1`,
starting at the first statement.

`[Lbrace, Rbrace]` is the one span that works under both. It contains the body's
first recorded block start either way, and no block belonging to code outside
the body starts inside it under either convention: the `if` or `for` header
block *ends* at the `{`, an `else` block's recorded start is after the `else`
keyword, and the block following the whole statement starts after the `}`. That
is exactly what the consumer's check needs — an instrumented block starts inside
the span, and no covered block does, therefore the body never ran.

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
`go-mutants list` prints the breakdown, `--json` carries every skip with its
reason, and `run --explain` prints the per-skip detail view underneath the
summary:

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
| `unnameable-decl-type` | No guard form can express the rewrite site; see **Guard site hints** above for the six cases |

Reserved reasons that later phases emit: `struct-tag` and `label-or-goto`.
`_test.go` files are built and run but never mutated; that is inherent, not a
recorded skip, and neither is a `panic` call the deletion family declines nor a
return value already spelled as its own replacement.

## Planned for v2

`if`-branch replacement, map/slice neutral values, and `switch`/`select` case
mutation. They are deliberately out of v1 because each needs either a new
guard form or a type-directed neutral-value model that the instrumentation
phase does not build yet.
