<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Mutation operators

**Status: every family is executed.** All fourteen families and all forty-nine
rules are found by `go-mutants list` with stable IDs, coordinates, and the
guard-site hint the instrumentation phase consumes, and `go-mutants run`
instruments, compile-validates, executes, and scores every one of them through
the six guard forms.

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
| `neutral-value` | `return-empty-slice`, `return-empty-map` | strong |
| `branch-replacement` | `condition-to-true`, `condition-to-false`, `loop-condition-to-false` | strong |
| `bitwise` | `band-to-bor`, `bor-to-band`, `xor-to-band`, `shl-to-shr`, `shr-to-shl`, `andnot-to-band` | strong |
| `arithmetic-assignment` | `add-assign-to-sub-assign`, `sub-assign-to-add-assign`, `incr-to-decr`, `decr-to-incr` | strong |
| `labeled-branch` | `drop-break-label`, `drop-continue-label` | all |
| `statement-deletion` | `delete-call-statement`, `delete-assignment`, `delete-incdec` | all |

That is 14 families and 49 enumerated rules. The design plan's headline said
43 while its own table listed 42; the registry has settled it in favour of the
table. `mutation.CanonicalRuleCount` is 49 and the canonical registry tests
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
  would offer `return nil` for a function returning an `int`. A value that
  already *is* its own replacement produces no candidate and no skip — the
  mutation and the source would be the same program — and that is asked of the
  constant go/types folded rather than of the bytes, so `return Disjoint` from
  an `iota` block is refused exactly as `return 0` is.
- `error-swallowing` owns the values whose static type is exactly `error`, and
  `return-replacement` owns every other nillable result. `return err` is
  therefore `return-err-to-nil` and `return &myErr{}` from the same function is
  `return-nil`. `nil-error-branch` replaces a whole `err != nil` comparison
  with `false`, in either operand order, when the compared value implements
  `error`. This is the Go-specific family with the highest expected yield:
  `return err` becoming `return nil`, and an `if err != nil` branch that no
  longer fires, are the two failure modes Go test suites most often miss.
- `neutral-value` requires the declared result type to be a slice or a map,
  and writes the *other* empty value for it: `[]T{}` and `map[K]V{}`. The line
  is drawn at types that have two distinct neutral values the standard library
  treats differently and `len()` cannot separate, which is these two and
  nothing else. A `string` has no nil; an array or a struct has `T{}` as its
  zero value rather than as a second neutral; a channel's non-nil empty blocks
  rather than being empty; a pointer's `new(T)` hides a nil dereference instead
  of exposing one; a function type would need its whole signature rendered. A
  bare type parameter is refused for `return-replacement`'s reason, while `[]T`
  is a slice whatever `T` is and is accepted. The type is *spelled* against the
  file's own imports by the same machinery Form D declarations go through, so a
  type this file cannot name is refused here exactly as it is there and
  recorded as [`unnameable-decl-type`](limitations.md).
- `branch-replacement` requires the condition to be boolean underneath, and
  writes the untyped constant `true` or `false` over the whole of it. The type
  gate is the guard's rather than the rule's: an untyped constant is assignable
  to any boolean type, so the *edit* is fine at a condition of a named boolean
  type, and Form C′ is what carries one: it writes the same selector and
  converts it back to the named type. A `for` with no condition and a `range`
  clause have nothing to settle and are passed over. A condition go/types has already
  folded to a constant is refused in the matching direction only, by the same
  predicate `return-replacement` asks: settling a constantly-true guard *true*
  writes different bytes for the same program, while settling it *false* is a
  branch that stops firing, which is exactly the mutant somebody wants when a
  build tag has quietly made a guard unconditional.
- `statement-deletion` deletes an expression statement that is a call, a plain
  `=` assignment (`x = append(x, e)` included), and an `++`/`--`. It never
  deletes a `:=`, which would make every later use of the name a compile error,
  and it never deletes a call to the builtin `panic`: removing a terminating
  panic leaves a path that reaches the closing brace without returning, which
  manufactures a missing-return error wholesale in exactly the defensive code
  where the mutant would have been interesting.

## Guard site hints

Discovery is the only phase with type information, so it is the phase that
decides which rewrite form the instrumenter has to use, and hands that down with
every candidate. Walking outward from the edit, each form is tried after the
ones before it:

1. the nearest enclosing expression whose static type is **exactly** the
   universe `bool` is a **Form C** site;
2. otherwise the nearest enclosing statement, which is a **Form S** site when it
   declares nothing (`ExprStmt`, `return`, an assignment that is not `:=`,
   `++`/`--`, send, `defer`, `go`, `break`, `continue`, `goto`) and a **Form D**
   site when it does (`:=`, or a `var` declaration with an initialiser). A Form
   D hint carries the source spelling of every type the site declares, rendered
   against the file's own imports;
3. a statement in a slot that holds a *simple* statement rather than any
   statement — an `if`, `switch` or `for` initialiser, or a `for` post
   statement — is a **Form F** site, when it is an expression statement, a send,
   an `++`/`--`, or an assignment that is not `:=`;
4. otherwise the nearest enclosing expression that is boolean *underneath* — a
   named boolean type — is a **Form C′** site;
5. otherwise the nearest enclosing expression of any type the file can spell is
   a **Form E** site.

Every search stops at the enclosing function, so a site is never chosen from
outside the function literal an edit sits in.

**The order is a promise, not an implementation detail.** Each form is tried
after the ones before it, so a site an earlier form covers is covered by exactly
that form — which means a new form adds sites and moves none, and no existing
mutant's bytes or identity changed when one landed. Form C′ is not a loosened
Form C for that reason, and Form E is not a loosened anything.

### Import completion

A guard spells a type with the name its package has **in the file being
rewritten**, and a file can perfectly well hold an expression whose type belongs
to a package it does not import: a helper in a sibling file returns one. That
was a refusal until discovery could supply the name.

The rule is one line, and it is what makes the addition safe: **a completion may
only add a path some file of the same package already imports.** Everything an
import injector normally has to prove follows from it by construction rather
than by analysis — no cycle is possible, because the package compiles today with
that edge in its graph and moving it between files does not change the graph;
visibility is unchanged, because `internal/`, module boundaries and vendoring
all judge the importing *package*, which is the same package; and `go.mod` needs
nothing, because the requirement that resolves the path is already there.

Three details follow:

- **The name is chosen at discovery**, beside the type it appears in, because
  the rendered type string already contains it. A name the file binds — a local
  variable called `time`, a package-level `carrier` — is bumped rather than
  refused, exactly as the generated runtime's own alias is.
- **A blank or dot import of the file's own is completed.** Both import the
  package and bind no name for it, which is the condition a completion exists
  for.
- **Each guard declares its own imports.** Validation bisects, so any subset of
  a file's mutants may be instrumented alone, and an import declared by a guard
  that was left out would be one the file does not have.

What it does not fix is reach rather than spelling: a type whose package *no*
file of this one imports stays a refusal, and so does one naming something
unexported elsewhere. `fixtures/unnameable/` holds one of each, side by side.

### What is refused

Everything else, and a refused candidate is never catalogued. Every refusal is
recorded as `unnameable-decl-type`, which reads as "no guard form can express
this site". After Form E there are three shapes left, and only the first is
about types at all:

- **an expression whose type cannot be spelled with the imports the file, or one
  of its siblings, has.** Form E writes the closure's result type out, and Form
  C′ writes a conversion, so both need a name. A missing *name* is no longer a
  refusal: where the type belongs to a package some file of the same package
  imports, discovery adds that import to the file being rewritten, under a name
  nothing there binds. See **Import completion** below for the rule and why it
  is safe. What is left is a missing *type*: an unexported name from another
  package has no source form anywhere outside it, and no import supplies one.
  The search walks outward past one — an expression *around* it may have a type
  the file can name — and refuses only when nothing on the way out can be named.
  Arithmetic over an unexported numeric type from another package, in a `switch`
  tag, is the smallest shape that reaches it;
- **an expression that is not a value.** `case int:` in a type switch records a
  type, `fmt` in `fmt.Println` records a package, `len` records a builtin. All
  three are expressions to `go/ast`, none is something a closure can return;
- **an expression in a position that needs more than its type.** An assignment
  target and the operand of `++`, `--` or `&` have to be addressable, and a call
  is not; a field name in `x.ok` is not an expression at all.

Three shapes used to be on this list and are not any more, and each one is worth
knowing about because the reason it left is the reason a form exists:

- a `:=` that **redeclares** an existing variable, and a `var` or `:=` whose
  **initialiser names a variable that same statement declares**. Go begins a
  declared name's scope at the *end* of its own specification, so `total :=
  total * 2` reads the enclosing `total`; Form D hoists the declaration out in
  front of the assignment, which would rebind it to a zero value, so Form D
  refuses — and Form E moves the initialiser *expression* instead, which
  declares nothing and moves nothing;
- a `var` whose **declaring tokens cannot be cut without moving a line**: a spec
  with no initialiser, or a spelled-out type, written across more than one line.
  Form D still refuses it, for the same reason; Form E takes the expression,
  which cuts nothing;
- a **`for` post statement or an `if` initialiser**, where a block is not legal
  Go. Form F puts the guard in a closure; Form E takes the initialiser
  expression where the statement itself declares.

## Branch proof

Six edits can only make a condition *less* often true. When one of them lands
in the condition of an `if` or a `for`, discovery records the span of the body
that condition gates, and publishes it as the mutant's `branch` in
[both JSON documents](json-schema.md#branch).

| Rule | Edit | Why it narrows |
| --- | --- | --- |
| `le-to-lt` | `<=` → `<` | Drops the equal case |
| `ge-to-gt` | `>=` → `>` | Drops the equal case |
| `or-to-and` | `\|\|` → `&&` | Requires both operands where one sufficed |
| `nil-error-branch` | `X != nil` → `false` | True of nothing |
| `condition-to-false` | `C` → `false` | True of nothing, whatever C was |
| `loop-condition-to-false` | `C` → `false` | The same, in a `for` |

Write C for the original condition and C′ for the mutant's. In each row
C′ ⟹ C, and that is the whole lemma:

> If no statement of the gated body ran during a test, C was false every time it
> was evaluated. C′ ⟹ C, so C′ was false there too, the branch taken was
> identical on every evaluation, and — the condition having no effects — the two
> programs ran identically. **That test cannot have observed the mutant.**

A consumer holding per-test coverage can therefore discharge every such test
without executing it. Nothing else may be inferred from `branch`: it is a
one-way statement about a body nobody entered, never about one they did.

The increasing edits are deliberately absent. `lt-to-le`, `gt-to-ge`,
`and-to-or` and `condition-to-true` widen a condition, so the mutant may enter a
body the original never did — exactly the case coverage cannot rule out.
`eq-to-neq` and `neq-to-eq` move it in neither direction, because the two
comparisons are true of disjoint sets of inputs rather than nested ones. The
`branch-replacement` family is the clearest case of the asymmetry: two of its
three rules carry the proof and the third, which is the same edit pointed the
other way, cannot.

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
manipulation for performance rather than for semantics, `neutral-value`, which
is noisy for a different reason — every function returning a slice or a map
gains a mutant, and a suite that only ever asserts `len` leaves most of them
alive — and `branch-replacement`, which is noisy for two reasons at once: a
defensive check that cannot actually fail survives `condition-to-false` in every
suite, and a test that kills `condition-to-true` almost always kills
`negate-condition` at the same span, so in `balanced` the family would mostly
inflate the denominator with near duplicates of a rule already there. `all`
adds `labeled-branch`, whose survivors are the ones hardest to argue about — a
label that changes nothing observable is equivalent in a way no analysis can
settle — and `statement-deletion`, including `append` removal, which is the
classic source of equivalent mutants in logging and metrics code.

## Deduplication

When two families produce the exact same byte edit at the same span, the
catalogue keeps one deterministically: **the more local rule wins**. A
`true-to-false@1` edit beats a whole-condition negation that happens to
produce the same bytes. Users never pay to run two IDs that mutate identical
source.

## Termination proof

A mutant that never returns is reported as a timeout, and a timeout counts as a
detection, so the verdict is already honest. What was not honest is how it was
reached: a timeout is ordinarily measured a second time before it is believed,
because one timeout is as much a fact about the machine as about the mutant, and
on a scope whose budget is derived from a slow baseline that is minutes of
worker time spent on one mutant to learn what was already true.

Whether a loop's bound survives an edit is not a fact about the machine. It is a
fact about the syntax and the types, and discovery has both — so it is decided
there, before anything is executed, and published on the mutant.

**It never changes a verdict.** A mutant proved unbounded is catalogued,
instrumented and measured like any other, and a proved mutant that is killed is
killed. What the proof changes is one thing: a timeout it predicted is believed
the first time, because the question the repeat asks has already been answered.

### The shape it reads

A `for` whose condition compares an induction variable against something the
loop does not change, and which moves that variable by a constant step — in the
post slot, or in the body.

```go
for i := 0; i < n; i++ { … }      // counting up towards an upper bound
for i := n; i > 0; i-- { … }      // counting down towards a lower bound
for i := 0; i < n; i += 2 { … }   // and any constant stride
for i > 0 { …; i-- }              // and the same measure, written in the body
```

The body form is the one most Go loops with a measure are written in, and it
costs three conditions a post slot gives for free — because a post statement
runs once per iteration by the grammar and a body statement only does if nothing
can jump past it. The step has to be a *direct* statement of the body, there has
to be exactly one of it, and the loop must hold no `continue` anywhere: a
`continue` jumps to the end of the iteration, which in a body-stepped loop is
jumping past the step.

| Edit | Verdict | Why |
| --- | --- | --- |
| `negate-loop-condition`, `negate-condition` on the condition | `unbounded` | The variable now moves *away* from the bound. `i >= n` with `i++` is true forever once it is true at all |
| `lt-to-le`, `le-to-lt`, `gt-to-ge`, `ge-to-gt` | `bounded` | The boundary moves by one, which changes how many iterations run and not whether they end |
| a reversed or deleted step | `unbounded` | The variable never reaches the bound. Not reachable today: a statement in a `for` post is refused by the guard forms, so no such mutant exists yet |
| anything else in the loop | `bounded` | An edit in the body does not stop a counted loop counting |

`unbounded` means **there is an input for which the mutant does not terminate**,
not that this suite has one. `for i := len(xs) - 1; i >= 0; i--` negated is
entered only when `xs` is empty, so a suite that never passes an empty slice
kills that mutant in the ordinary way. The proof is an upper bound on how many
timeouts a scope can cost, which is what a budget wants.

### What it refuses, and why the refusal is silent

A `range`, a `for` with no condition, a condition over a call rather than a
comparison, a condition over two moving variables, a body that assigns the
variable anywhere but its step or that assigns the bound, a step behind an `if`
or inside a nested loop, two steps in one body, a `continue` under a
body-stepped loop. Each is refused without a proof and without a skip —
**an absent proof is never a claim that a loop is fine**, and recording a skip
for every loop go-mutants declined to reason about would bury the skips that
mean "go-mutants declined to mutate this" under ones that mean "go-mutants
declined to think about this". That is the [branch proof](#branch-proof)'s rule, applied to the
second proof.

`unbounded` is what lets a run believe one timeout. Everything else a proof says
is for a reader: `go-mutants list --json` carries it, and "this mutant is proved
not to return" is the sentence that saves somebody suspecting their machine.

Measured over this repository at profile `all`: 108 of 7069 mutants carry a
proof, 30 of them `unbounded`. One and a half per cent sounds small and is the
right one and a half per cent — most mutants are not in a loop condition at all.
None of this repository's own four spinners is among them, because none of their
conditions compares a variable against a bound; the proof is a statement about a
shape rather than a promise about a tree.

## Documented exclusions

These are recorded as skips with a reason and never silently dropped.
`go-mutants list` prints the breakdown, `--json` carries every skip with its
reason, and `--explain` prints the detail view underneath the summary.
`list --explain` names each suppressed site as `path:line:col` — a whole-file
reason as the bare path, because such a file is never opened — and
`run --explain` names the file and its count, which is what the run report
keeps:

The reason strings below are the exact identifiers `internal/discover` emits
(they appear verbatim in `list` output and in catalog/report JSON):

| Reason | Why |
| --- | --- |
| `const-decl` | Constant expressions must stay constant (covers `iota`) |
| `array-length` | `[N]T` lengths are not runtime-evaluated expressions |
| `type-param` | Type parameter lists, constraints, and type arguments are not value code |
| `package-var-init` | Initialization order hazards; covers `//go:embed` vars; v1 limitation |
| `cgo` | cgo packages are excluded wholesale |
| `generated` | Matches `^// Code generated .* DO NOT EDIT\.$` |
| `excluded` | The file matched a configured `mutation.exclude` pattern |
| `label-or-goto` | A `goto`, whose target cannot be moved without jumping over a declaration or into a block, and whose removal would leave a function reaching its closing brace without returning |
| `unnameable-decl-type` | No guard form can express the rewrite site; see **Guard site hints** above for the three shapes that reach it, and **Import completion** for the one that used to and no longer does |

One reason remains reserved in the run-report schema and emitted by nothing:
`struct-tag`. Nothing will ever emit it — a tag is part of a *type*, so there is
no run-time value for a guard to select between. See
[Limitations](limitations.md) for the argument.

`_test.go` files are built and run but never mutated; that is inherent, not a
recorded skip, and neither is a `panic` call the deletion family declines nor a
site that already holds the value its replacement would write.

### The refusals that are not skips

Five places in the catalogue produce neither a candidate nor a skip, and they
are listed together because each one is an argument rather than a mechanism. A
skip says *go-mutants declined to mutate a site*; these say *there is no mutant
here to decline*.

| Refusal | The argument |
| --- | --- |
| A `panic` call, for `delete-call-statement` | Removing a terminating `panic` leaves a path that reaches the closing brace without returning. The mutant would not compile, and manufacturing a missing-return error wholesale in defensive code is not a measurement |
| A site already spelled as its own replacement — `return 0`, `return nil`, `if false`, and for `neutral-value` also `return []T{}` and `return make([]T, 0)` | The mutation and the source are the same program |
| A site whose value go/types already folded to its replacement — `return Disjoint` where `Disjoint` is the head of an `iota` block, `return Anonymous` where that constant is `""`, `if enabled` where `const enabled = 3 > 2` | The same argument one level down, and the one that earns its keep: a constant has one value however it is spelled, the compiler has already computed it, and two spellings of it are one program. Only the *matching* direction goes — `return-false` at a constantly-true result, and settling that guard `false`, are branches that stop firing, which is exactly the mutant somebody wants when a build tag has quietly made one unconditional |
| A slice or a map returned beside a non-nil `error`, for `neutral-value` | By universal Go convention a caller that sees an error does not look at the other results, so `if err != nil { return nil, err }` mutated to `return []T{}, err` is equivalent. **This is an argument from convention, not a proof** — the same honesty the `panic` refusal above is stated with. It is gated per statement, so `return xs, nil` on the success path, where the rule is worth the most, is not touched. Without the gate most of the family's output would be this one shape |
| `loop-condition-to-true`, everywhere | Not a refusal of a site but of a rule: it is the one edit whose every instance would cost a whole per-mutant timeout — twice, since a timeout is measured again before it is believed — to teach a reader what the source already says. `false` is the safe direction, and it is in the catalogue |

`neutral-value` also carries no [probe hint](#guard-site-hints), deliberately.
The return probe compares the returned value against the replacement, and a
slice is not comparable: `r0 != []string{}` is not legal Go. The `return-nil`
beside it keeps its hint, because `r0 != nil` is legal for both. A sound
predicate can be written — `r == nil || len(r) != 0 || cap(r) != 0` — and is
recorded here so that nobody has to re-derive it; what stops it today is that
the probe form is one shape for every rule and this would be a second.

## What used to be planned for v2

Nothing is. This section used to hold three entries and now holds their
epitaphs, which is worth keeping because each one was waiting on less than it
looked like.

**Map and slice neutral values** were waiting on "a type-directed neutral-value
model the instrumentation phase does not build yet". That model already
existed: `guardResolver.typeString` is the speller Form D's declarations go
through, and the `neutral-value` family calls it. Nothing was built for the
instrumenter at all.

**`if`-branch replacement** was waiting on a new guard form. It needed none:
settling a condition writes a constant at exactly the anchor `negate-condition`
already uses, and Form C takes it. What it needed was a decision about which
three of four candidate rules to write — swapping an `if`'s arms is
`negate-condition` spelled differently, emptying a body is the conjunction of
two `statement-deletion` mutants, and `loop-condition-to-true` is a timeout per
counted loop — so the family has three rules and not four.

A tagless `switch`'s case labels used to be on this list and are not any more:
they are exactly `bool`, so Form C expresses them and nothing had to be built.
What they were waiting on was somebody asking the guard chooser rather than
suppressing them before it was consulted. A **tagged** switch's labels and a
type switch's remain [documented exclusions](#documented-exclusions).
