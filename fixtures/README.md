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
| `killable/` | `fixture.example/killable` | The end-to-end kill. Thirteen mutants with predetermined fates: nine in `Clamp` and one boolean literal in `IsReady` that the tests kill, and three in `Untested` that survive because nothing calls it. One function per file and no repeated operator, so a mutant can be named by path, line and rule alone. |
| `rejectable/` | `fixture.example/rejectable` | Compile validation's oracle. Nineteen candidates over three files, three of which are not programs once the mutation is applied: two constant divisions by zero (`v*0` swapped to `v/0`) and an untyped constant that stops fitting its context (`200 - 100` returned as a `uint8`, swapped to `200 + 100`). The other sixteen are healthy, share their files with the traps, and are all killed by the fixture's tests, so a phase that rejected a file rather than a candidate would be caught. Four of the sixteen are the control in `named.go`: the named boolean type that used to be this module's trap and is now an ordinary mutant. |
| `coverage/` | `fixture.example/coverage` | Coverage-guided selection. Two packages, two test binaries, and three functions in one file with three different coverage fates, eleven mutants between them: `AboveZero` is reached only by its own package's tests, `Differs` only by the caller package's, and `Orphan` by nothing at all. It is the one fixture where the *right* answer and the *fast* answer differ, so a mutant measured against the wrong binary would survive rather than merely cost time. |
| `vetsuspect/` | `fixture.example/vetsuspect` | The toolchain's opinion of the rewrite. Two functions, ten mutants, all killed — and two of the ten are the point: a Form C guard renders each alternative from the pristine bytes with one edit applied, so `or-to-and` writes `s == "." && s == ".."` into the snapshot and `and-to-or` writes `s != "." || s != ".."`. Both are legal Go and both are what vet's `bools` analyzer reports, and `go test` and `go test -c` run it by default. It is the only fixture whose subject is a command line rather than a program. |
| `probeable/` | `fixture.example/probeable` | The probe session. Three mutants and no other mutable expression: two return-value ones a probe tree has a form for and one boolean literal it has none for, so both directions of the layer can be stated — a probed mutant whose absence from a measurement is a fact, and an unprobed one whose absence means nothing at all and which a consumer has to treat as infected by every test. Every probed function returns a value differing from its mutant's constant on every call, so a test that does not name it is a test that never reached it. Its `isolated/` package holds nothing to mutate and imports nothing that does, so its binary links no runtime and writes no log — the one absence a probe pass must read as the empty set rather than as a failure. |
| `families/` | `fixture.example/families` | The whole operator catalogue. Twenty small functions in one package holding at least one live candidate for each of the 42 rules the frozen registry names — 76 mutants at profile `all`, 72 at `strong`, 59 at `balanced`. Every other fixture proves one mechanism against a handful of operators; this one proves the operators, and a family that stopped being discovered, instrumentable, or compilable shows up as a missing row rather than as a smaller number. |
| `workspace/` | `fixture.example/workspace/app`, `fixture.example/workspace/lib` | A `go.work` joining two modules. Pointed at `app/` it is a scope test: the snapshot is that module alone, six mutants, all killed. Pointed at its own root it is a refusal — a workspace has no single module path, no single set of identities and no single baseline, so the run stops with GOM4102 before anything is copied. `app` deliberately imports nothing from `lib`; see below. |
| `tagged/` | `fixture.example/tagged` | Build constraints as an input to the catalogue. Two boolean literals, one of them in a file under `//go:build special`, so a run under `GOFLAGS=-tags=special` catalogues two mutants where a run without it catalogues one — and the two runs key their cached outcomes differently, because GOFLAGS is in the cache context. |
| `untested/` | `fixture.example/untested` | A package with tests beside one without. `lib/`'s two mutants are killed; `orphan/`'s two are settled as uncovered survivors without being executed, because no test binary reaches the line. It is also the specimen for the one test command that names real packages and still builds nothing: `go test ./orphan/...` is refused with GOM4022 rather than reported as a score of zero. |
| `selfwriting/` | `fixture.example/selfwriting` | A passing test suite that writes a file into the package directory it runs in. Every mutant is measured against the snapshot the baseline was measured against, so a suite that edits that tree makes the score a mixture of two programs; the run stops at the drift gate with GOM4014, and `Prepare` refuses the same tree at its verification stage with a `*DriftError`. |
| `runaway/` | `fixture.example/runaway` | The memory bound. One countdown whose loop condition, negated, is true for exactly the input the original loop ran zero times for — so the mutant appends to a slice until something stops it, which is what a per-mutant timeout derived from a millisecond suite never does in time. Two more mutants on the same lines are killed by ordinary assertions, so a run in which the runaway is reported killed can be shown to have been killed by the bound and not by the suite going red for another reason. |
| `unnameable/` | `fixture.example/unnameable` | A declaration whose type cannot be written down: `hidden.New` returns a `*counter` that is not exported, so a guard at the addition inside the call would have to declare a temporary of a type this file has no source form for. Discovery records it as an `unnameable-decl-type` skip with its coordinates and carries on, and the ordinary candidate on the next line is catalogued and killed. |

The discovery fixture is the one module in the corpus with no test files, which
is deliberate: `list` builds nothing and runs nothing, so a test here would add
time to the suite without being able to fail for a reason `list` could cause.

The rejectable fixture's traps are load-bearing in the same way the killable
fixture's bounds are, and its history is the reason they are the shape they are.
Its first traps were a comparison and a boolean literal returned as a named
boolean type `Flag`: Form C's selector evaluates to plain `bool`, which is not
assignable to `Flag`, so guarding them was a compile error. That was a fact
about a *rewrite form*, and it stopped being true the day discovery started
routing an edit with no exactly-`bool` expression around it to the statement
form — the module went on compiling, its tests went on passing, and the phase
that exists to isolate traps was left with nothing to isolate.

Both traps are now facts about the *mutated program*, which no change of rewrite
shape can rescue: `v/0` is not Go anywhere, and 300 does not fit a `uint8`
anywhere. Replacing `v*0 + 1` with something whose multiplication is not by a
constant zero, or widening `Level`'s result past a byte, would disarm the
fixture in exactly the way `type Flag = bool` once would have.

The named boolean itself did not leave; it moved from `flag.go` to `named.go`
and changed sides. Its four candidates are now the fixture's *control*: they are
accepted, instrumented through the statement guard, executed, and killed by
`TestReady` and `TestAlways`. Keeping them is what makes the improvement a test
rather than a changelog claim — every other fixture in the corpus returns a
plain `bool`, so nothing else would notice if the statement form ever stopped
carrying a named boolean result. `internal/validate`'s integration suite
activates each of the four and requires the suite to go red, because "accepted"
only proves the guard compiled while "killed" proves it selected anything at
all.

The killable fixture is the one whose *behaviour* is load-bearing rather than
only its shape. `Clamp` takes an open range — a value at or below `lo` becomes
`lo+1`, one at or above `hi` becomes `hi-1` — because a clamp with inclusive
bounds returns the bound itself at the bound, which makes `<` and `<=` agree on
every input and turns the boundary mutants into equivalent ones that no test can
kill. Its doc comment says so; changing the bounds back to the inclusive ones a
reader expects would leave the fixture compiling, its own tests passing, and the
integration test waiting for a failure that can no longer happen.

## What the families fixture deliberately misses

`families/` is the one fixture whose *tests* are part of the specimen. Most of
its functions are pinned by a test that fails for every mutant of them; four are
under-tested on purpose, and one is not called at all. Without both fates the
fixture would prove very little — a run in which everything died is
indistinguishable from a suite that is simply strong, and one in which
everything survived is activation that never happened.

The gaps, and what each leaves out:

| Function | Test | What the test leaves out | Survivors |
| --- | --- | --- | --- |
| `Toggle` | `TestToggle` | Calls it with both inputs and asserts nothing at all about either answer. The commonest gap there is: a test that exercises rather than checks, invisible to `go test` and to coverage alike. | 3 |
| `Weigh` | `TestWeigh` | Only the zero row. At zero the multiplication, the addition, and the whole returned expression all agree with the `0` that `return-zero-numeric` puts there; one non-zero row would kill all three. | 3 |
| `Salt` | `TestSalt` | Calls it and throws the result away, the same shape as `TestToggle` in a different family. | 3 |
| `Drift` | `TestDrift` | Accumulates a slice of *zeros*, so the loop body really runs — these are survivors the run measured, not ones coverage inferred — but adding zero and subtracting zero come to the same thing. | 2 |
| `Orphan` | none | Nothing calls it, from a test or from anywhere else. No test binary reaches the line, so coverage settles both of its mutants without executing either. | 2 |

Four under-tested functions rather than one is deliberate: a table with a
single survivor row could not tell "the run reports survivors" from "the run
reports this one". `Orphan` is the fixture's only *uncovered* pair and is the
reason the run's coverage narrowing is observable here at all; calling it from a
test would leave the module compiling, the suite green, and the narrowing
unproven.

The fixture's other invariant is about loops, and it is the one an edit is
likeliest to break: **every loop in `families/` terminates under every mutant of
it.** `negate-loop-condition` turns `for i := 0; i < limit; i++` into a loop that
never ends when `limit` is not positive, and `gt-to-ge` does the same to a
counter running down to zero. So the loops there either run over a length no rule
can rewrite — `for range` has no condition — or take a bound the tests never hand
a degenerate value to. Adding a zero row to `TestSteps` would not fail the suite;
it would hang one mutant until the run's timeout and turn a kill into a
`timed-out`.

## Who drives them

`internal/engine`'s integration suite runs the whole pipeline — snapshot,
baseline, discovery, compile validation, the instrumented baseline, the drift
gate, the coverage pass, execution, and the report — against `simple/`,
`killable/`, `rejectable/`, `coverage/`, `families/`, `vetsuspect/`, and
`failing-baseline/`. It asserts the exact tally each of them produces, so the
numbers in those tests are the fixtures' documented claims about themselves
stated as data: `killable/` is 10 killed and 3 survived, `rejectable/` is 16
accepted and 3 rejected, `coverage/` is 8 killed and 3 uncovered survivors
across 2 test binaries, `families/` is 63 killed and 13 survived over all
eleven families, `vetsuspect/` is 10 killed and nothing left unexecuted, and
`simple/` is a green run whose event sequence is pinned whole. A fixture
edited without its test is a fixture whose claim quietly stopped being true.

`probeable/` is driven by the engine API's own integration suite instead, which
prepares two sessions over it — one with a probe tree and one without — and
holds every claim in its package documentation as data: two mutants probed and
one not, a test that reaches a site named and one that does not absent, a
failing and a timed-out target carrying no facts, and, over every (mutant, test)
pair it has, that a kill was always preceded by an infection. That last one is
the soundness statement of the whole infection layer, and the fixture's three
returns exist to make it checkable by reading the file. Adding a fourth
mutable expression, or letting `Width` or `Label` ever return the constant its
mutant returns, would leave the module compiling, the suite green, and a probe
recorded as silent about a site it did reach. Its `TestPrintsALot` is the one
target in the fixture that is not about probing at all: it prints two thousand
short lines so that the same suite can watch a pass run into
`ProbeRequest.OutputLimit`. It lives in the fixture rather than being written
into the tree by the test that needs it, because that session is prepared once
and shared, so no test using it can add a file — and it reaches no probed site,
which is what keeps it out of every claim above.

`vetsuspect/` is the one whose tally is the least interesting part of it. What
that test asserts is that the mutants *executed* at all: `bools` is one of the
analyzers `go test` and `go test -c` run before compiling, so a run without the
engine's `-vet=off` on the instrumented tree dies at GOM4013 or GOM7505 with a
diagnostic about generated code, and every mutant in the fixture is settled by
never being built. Its own package documentation states the invariant that
keeps it a trap — both comparisons against *different* constants, one pair
joined by `||` and the other by `&&` — because every simplification of those
two functions leaves the module compiling, the suite green, and the fixture
proving nothing.

`families/` is driven twice. `TestFamiliesRunReachesEveryOperatorFamily` runs it
at profile `all` and holds it against a per-family table of kills and survivors,
the exact list of survivors by file and line, and the requirement that every one
of the 42 catalogued rules produced a mutant.
`TestProfileTiersSelectMonotonicallyOverTheWholeCatalogue` runs it three more
times, once per tier, and asserts not only that the counts differ but that the
mutant *identities* nest — `balanced ⊂ strong ⊂ all` — and that the families
each tier adds are exactly `bitwise` and `arithmetic-assignment`, then
`statement-deletion`.

The coverage fixture's *absences* are load-bearing in the way the killable
fixture's bounds are. Nothing may call `Orphan`, and nothing in package `core`
may call `Differs`: adding either call leaves the module compiling, every test
passing, and the fixture silently proving something weaker — that coverage
narrowing did not break anything, rather than that it narrowed to the one binary
that could kill the mutant. `TestAboveZero`'s zero row is the same kind of
detail: it is the only input at which `>` and `>=` disagree, so deleting it
turns the fixture's first claim from "killed by the binary that covers it" into
"survived".

The five modules below are the cases the instrumentation and the run have to get
right around the edges of an ordinary module, and each is driven by tests named
here so that a fixture and its claim can be read together. The sixth case, CRLF
sources, has no directory and has a section of its own:

| Fixture | Driving tests |
| --- | --- |
| `workspace/` | `internal/engine`: `TestRunInsideAGoWorkspaceSeesOnlyTheModuleItWasPointedAt`, `TestRunAtTheWorkspaceRootIsRefused` |
| `tagged/` | `internal/engine`: `TestBuildTagsNarrowTheCatalogueThroughGOFLAGS`, `TestFixtureReportsMatchTheirGoldens` |
| `untested/` | `internal/engine`: `TestAPackageWithoutTestsReportsItsMutantsAsUncoveredSurvivors`, `TestScopingTheTestCommandToTheUntestedPackageIsRefused`, `TestFixtureReportsMatchTheirGoldens` |
| `selfwriting/` | `internal/engine`: `TestATestThatWritesIntoItsOwnDirectoryStopsTheRunAtTheDriftGate`; root package: `TestPrepareRefusesDriftFromTheBaselineItself` |
| `unnameable/` | `internal/engine`: `TestAnUnnameableDeclarationIsSkippedWithItsReasonAndTheRunStaysGreen` |
| `simple/`, `killable/`, `untested/`, `tagged/` | `internal/engine`: `TestFixtureReportsMatchTheirGoldens`, against `internal/engine/testdata/<fixture>.report.golden.json` |

The workspace fixture's one constraint is the one a reader will want to relax:
**`app` imports nothing from `lib`.** A run pointed at `app/` snapshots `app/`
alone — the workspace file is one directory above the root it was given and is
not copied — so the snapshot is resolved as the single module it contains. An
import of the sibling would therefore not be a scope test at all: it would be a
`go build` failure inside the snapshot, and the fixture would stop being able to
say anything about what a run inside a workspace measures. What makes the
absence of `lib` observable instead is its *fate*: all three of its mutants
survive on purpose — `neq-to-eq` on the comparison, `return-true` and
`return-false` on the expression it returns — so a run that reached across the
workspace would report survivors and a lower score where the test requires
every mutant killed.

`untested/orphan` and `selfwriting/`'s witness file are load-bearing in the same
way. Adding an `orphan_test.go` would leave the module compiling and the suite
green while turning two uncovered survivors into two executed ones and making
`go test ./orphan/...` an ordinary command; deleting the `os.WriteFile` from
`selfwriting`'s test would leave a run that passes the gate it exists to fail.

## Modules the corpus cannot hold

Some of the trees the engine has to be proven against are trees this repository
refuses to check in. They are synthesized by `internal/testkit`'s module builder
instead, and they are listed here because they are corpus members in every sense
but the filesystem's:

| Module | Built by | Driving tests |
| --- | --- | --- |
| A CRLF copy of `simple/` | `testkit.NewModule(t).From("simple").CRLF()` | `internal/engine`: `TestCRLFSourcesAreInstrumentedByteForByteAndKilled`; `internal/instrument`: `TestInstrumentPreservesCRLF` |

The CRLF tree cannot be a directory here because `.gitattributes` pins
`* -text`: a CRLF file in the corpus would be CRLF on every platform, and line
endings are part of a file's bytes, so it would change every mutant identity
that covers it and make the corpus a worse test of the instrumenter rather than
a better one. Synthesizing it keeps the claim — the rewriter preserves every
byte it did not mutate — checkable without weakening the corpus.

## The conformance gate

`internal/testkit`'s `TestCorpusConformance` reads this document and the
directories beside it and requires them to agree. Every fixture is a module or a
workspace whose `go.mod` names it under `fixture.example/`, declares a `go`
directive no newer than the toolchain in use, and carries no `require`,
`replace` or `go.sum`; every file is LF-only and every Go file and `go.mod`
opens with the SPDX pair; every fixture is a row of *The corpus* table above and
every row of it has a directory under it; every fixture is named by a test
somewhere in the repository; and nothing under `fixtures/` is a report, a
`.go-mutants*` state file or a compiled binary. The conventions at the top of
this page are the prose, and that test is the copy of them a new fixture cannot
get past without reading.
