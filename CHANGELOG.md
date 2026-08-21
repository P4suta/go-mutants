<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Changelog

All notable changes are documented here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project follows
Semantic Versioning for its CLI, its TOML configuration, and its JSON schemas.
Entries say *why* a change was made, not only what changed.

## [Unreleased]

### Added

- The repository scaffold: module `github.com/P4suta/go-mutants` targeting
  Go 1.26, a stub `cmd/go-mutants` that prints its version, and the complete v1
  dependency set recorded in `go.mod` ahead of the packages that will import
  it. Cobra, `pelletier/go-toml/v2`, bubbletea/bubbles/lipgloss,
  `golang.org/x/tools`, and `golang.org/x/sync` are the runtime set;
  `santhosh-tekuri/jsonschema/v6`, `google/go-cmp`, and `pgregory.net/rapid`
  are test-only. Recording them now means the dependency decision is reviewable
  as one commit instead of arriving piecemeal.
- One pinned toolchain in `mise.toml` — the Go compiler included — with tasks
  for `bootstrap`, `build`, `test`, `test-integration`, `fmt`, `lint`, `check`,
  `dogfood`, `package`, and `hooks`. Every gate is defined exactly once there,
  so `just`, the git hooks, and CI cannot drift apart; `justfile` and
  `lefthook.yml` are deliberately thin shims over it.
- CI as three workflows with every action pinned by commit SHA,
  `permissions: contents: read`, per-ref concurrency, and a timeout on every
  job: `ci.yml` (quality, a three-OS test matrix, artifacts, dogfood),
  `nightly.yml` (fuzz and property placeholders), and `release.yml` (verify,
  then a draft-only release job that is the sole holder of `contents: write`).
  Dependabot watches actions and Go modules weekly.
- `.go-mutants.toml`, the configuration this project will dogfood itself with,
  written out in full with comments so the v1 surface is reviewable before the
  strict decoder exists.
- `docs/` covering the instrument-once schemata design and the S/C/D guard
  forms, the typestate pipeline, coverage-guided selection, the 11-family
  operator catalogue with its profile tiers, the full configuration surface,
  the JSON contracts, the Stryker projection boundary, and the release
  checklist. Every page states plainly that it describes planned behaviour, so
  the documentation can be reviewed now without ever claiming working software.
- `.gitattributes` pinning `* -text`. Mutant identities hash exact source
  bytes, so a CRLF checkout would silently change every ID; byte-exact
  checkouts are a correctness requirement here, not a preference.
- The strict configuration decoder, `internal/config`: `.go-mutants.toml`
  decoded with `pelletier/go-toml/v2` and `DisallowUnknownFields()`, every
  problem carrying a `GOM30xx` code, the file name, and a one-based
  line/column. A misspelled key is the single most common way a mutation run
  quietly does the wrong thing, so it is a positioned error rather than a
  default; BurntSushi/toml cannot report that position, which is why it is not
  the dependency. Problems are aggregated instead of returned one per
  invocation, because fixing a configuration file one error per run is
  unpleasant enough that people stop reading the errors.
- The baseline execution layer behind `go-mutants run`: snapshot the workspace
  into a disposable copy, build it, then run the project's test command three
  times (`test.baseline_runs`) before anything is mutated. A suite that is
  already red, flaky, or unbuildable makes every later "survived" meaningless,
  so the run proves the baseline first and stops there rather than reporting a
  score it has not measured. Every observation is retained, not just the
  slowest, and the per-mutant timeout derives as
  `max(10s, slowest baseline x 5)`; an explicit `--timeout` at or below the
  slowest baseline is refused outright, because a timeout a passing suite
  cannot meet turns the whole run into confirmed timeouts.
- Process-tree supervision in `internal/runner`. A test command that spawns
  children is the normal case in Go, and killing only the direct child leaves
  them holding ports and temporary directories for the rest of the run.
  Windows uses a Job Object with `KILL_ON_JOB_CLOSE` and fails closed when
  ownership of the tree cannot be established; POSIX uses a process group with
  `TERM` before `KILL`, so a well-behaved suite gets to clean up after itself.
- Discovery and the `list` command: `internal/discover` loads the module
  through `packages.Load`, walks it with `go/types` evidence, and mints stable
  ids for the `comparison` and `boolean-literal` families, recording every
  suppressed candidate as a skip with a reason instead of dropping it.
  `go-mutants list` prints that catalogue, and `--json` writes the
  `go-mutants/catalog` v1 document validated by
  `schema/catalog-v1.schema.json`. Enumerating mutants before any of them can
  be executed is deliberate: it makes ids, coordinates, and skip reasons
  reviewable — and diffable across changes — while the instrumentation phase is
  still being built.
- Compile validation, `internal/validate`: the instrumented snapshot is built
  once with every catalogued mutant spliced in, and a green build accepts the
  whole catalogue — which is the entire point of the schemata design. A red one
  starts a bisection that first restores every catalogued file to its pristine
  bytes and rebuilds, so a tree that was already broken stops the run with
  `GOM7420` instead of being blamed on whichever candidate was tested first,
  then searches the files the compiler named one at a time: halving while
  halving is cheaper than scanning, verifying every join, and falling back to a
  scan when a join fails, so a pair of candidates that only fail together is an
  ordinary case rather than a wrong answer. Instrumentation is a byte rewrite
  that leaves typing to the compiler, so some guarded sites genuinely cannot
  compile; the alternative to asking the compiler is type-checking every file
  to answer a question it answers for free, or answering it conservatively and
  dropping candidates that were fine. Every refusal is published as a
  `rejected[]` entry carrying the identity, the coordinates, and the compiler's
  own words, captured at the moment of rejection because by the time the phase
  finishes the tree compiles and that message exists nowhere else. Silently
  dropping them is what makes a catalogue shrink between runs with nobody able
  to say what left it, and a mutant that cannot exist must never reach a score's
  denominator.
- The execution engine, `internal/execute`: `go test -c` builds each package
  that has tests once, and every mutant afterwards starts those binaries
  directly with `GO_MUTANTS_ACTIVE` set. Going through `go test` per mutant
  would pay for a build-graph load and a staleness check in the inner loop, and
  would consult a result cache that keys on inputs the mutant is invisible to.
  A non-zero exit is a kill and the remaining binaries are skipped, because they
  cannot change the answer; the generated runtime's exit 97 is not a kill but a
  stale catalogue, reported as errored, since treating it as one would inflate
  a score.
- Timeouts are retried before they are believed. A first timeout is not
  evidence: N test binaries on a loaded machine produce timeouts that say
  nothing about the mutant, and counting one as a detection would flatter a
  suite exactly when the run is least able to notice. Every timed-out mutant is
  held back and retried serially after the queue drains — one at a time,
  nothing else running, the same timeout. A second timeout is a confirmed
  detection; a retry that finishes, pass or fail, is `inconclusive` and counts
  in neither direction. Both attempts stay in the report, so the document shows
  what happened rather than only the verdict.
- `RunReport v1` and its history store, `internal/report`, with
  `schema/run-report-v1.schema.json` and the `GOM51xx` codes. It is lossless by
  construction — every catalogued mutant appears exactly once, in `mutants[]`
  with an outcome or in `rejected[]` with a diagnostic — because the console
  summary, the exit code, and every projection still to come are views of it,
  and that is only safe if it holds everything they need. The exit decision is
  made from the written document rather than beside it, so the number a user
  reads and the gate that failed cannot disagree. `summary.score_percent` is
  `null` rather than a number when the denominator is zero: 0 reads as "your
  tests caught nothing" and 100 as "your tests caught everything", when the
  truth is that nothing was measured. History is kept under the OS cache
  directory at `<cache>/go-mutants/workspaces/<key>/`, not in the project, so a
  mutation run adds no files to the tree it is measuring; every write is
  temp-file plus atomic rename, and `ReportPublished` is emitted only after the
  rename succeeds.
- The live dashboard, `internal/tui`: a second renderer over the same
  `chan engine.Event` the plain lines come from, drawn with bubbletea on the
  alternate screen. It shows the phase and what the baseline established, a
  score gauge over what has settled, the outcome counters, a worker-slot table
  fixed at `RunPlanned.Workers` rows so that nothing reorders itself while it
  is being read, a scrolling survivor feed carrying the same
  `- original` / `+ replacement` diff the plain output prints, an EWMA estimate
  of what is left, and an elapsed clock. The engine is unchanged and unaware:
  both renderers consume one stream, and neither computes a number the engine
  did not publish.
- `internal/cli` picks the dashboard only when standard output is a terminal
  that can do better than ASCII and nothing has asked for something else.
  `--json`, `--quiet`, `--no-color`, `NO_COLOR`, `CI`, and the new `--no-tui`
  each fall back to the plain renderer, and the terminal detection is
  charmbracelet's own — `x/term` and `colorprofile`, the libraries bubbletea
  itself decides with — rather than a hand-rolled escape-sequence probe. Only a
  terminal is handed over as input: a redirected standard input is somebody's
  data, not a keyboard.
- Ctrl-C in the dashboard cancels the run's context and does nothing else. The
  engine then unwinds exactly as it does for a signal in plain mode — marking
  what it never reached as not-run, publishing the partial report, emitting
  `RunCompleted` — and only then does the screen come down, so the alternate
  screen can never be torn away before the report exists. A second Ctrl-C is
  the documented escape hatch and quits at once; the run keeps unwinding and
  the renderer keeps draining it, because abandoning the stream would deadlock
  the cleanup that removes the snapshot. Once the screen is restored, the
  warnings and the closing summary are printed underneath it by the plain
  renderer itself — the same code, fed the events it would have rendered
  anyway — so the block left in the scrollback is byte for byte the block a
  plain run prints, and a warning the alternate screen erased is not lost.
- Coverage-guided selection, `internal/coverage`. Each test binary is built
  with `-cover -coverpkg=<module>/...`, run once with nothing activated, and
  rendered through `go tool covdata textfmt`; a mutant is then measured only
  against the binaries whose profile reaches its lines, and one no binary
  reaches is not executed at all. Most of a mutation run's wall-clock time goes
  on proving that mutants no test touches survive, and that is a fact the
  profiles already know. The mapping is by **line interval only**, never by
  column: the profile is collected from the instrumented snapshot, where the
  guard rewrite preserves line numbers by design and moves columns by
  construction, so lines are the coordinates the two documents agree on.
  Over-approximating costs a wasted execution; under-approximating would cost a
  kill, so the boundaries are inclusive and a mutant a covered block merely
  touches is treated as covered.
- A mutant nothing covers is reported as `survived` with `uncovered: true` and
  zero attempts, rather than as `not-run`. It really did survive — no test runs
  the line, so no test could have caught the edit — and taking it out of the
  score's denominator would let a workspace raise its mutation score by
  deleting tests. What `uncovered` adds is the more actionable half of the
  finding: write a test for this line, rather than sharpen the test you have.
  The console prints `SURVIVED (uncovered)`, lists those survivors after the
  covered ones, and adds an `uncovered N` column to the counts line.
- Two rules decide whether any of that happens, and both fail safe.
  Coverage-guided selection is **auto-on only for the built-in
  `go test ./...`** and off with a `GOM7601` warning for any other
  `test.command`: the mapping is from a *test binary* to the lines it reached,
  and there is no honest way to attribute an opaque command's coverage to
  go-mutants' own per-package binaries. And every failure of the pass —
  the instrumented build not compiling, a profiling run failing, `covdata`
  missing, a profile that will not parse, a profile set with no blocks, a
  module path the profiles do not line up with — publishes a `GOM7602` warning
  and runs every mutant against every binary. None of them can fail a run:
  without the optimisation the run does strictly more work and reaches exactly
  the same verdicts.
- `run-report-v1` gains `coverage.binaries` and `coverage.mutants_uncovered`
  in the new `package` mode, and `covering_test_packages` and `uncovered` on
  every entry of `mutants[]`. The two summary numbers are absent rather than
  zero outside `package` mode, and the schema refuses them there: a run that
  narrowed nothing must not state a measurement it never made.
- The remaining nine operator families in `internal/discover`, which completes
  the 42-rule catalogue: condition negation, boolean connectives, integer and
  float arithmetic, bitwise operators, arithmetic assignment, return
  replacement, error swallowing, and statement deletion. Every gate is a
  `go/types` question rather than a syntactic one, and reads through a named
  type to its underlying one: `+` between strings is not integer arithmetic
  because its operands are strings, `type Celsius float64` is mutated like the
  float it is, and complex arithmetic is out of scope because the float gate
  asks for a floating-point type. `error` is where the two nil rules divide —
  `return err` is error swallowing, `return &myErr{}` from the same function is
  the ordinary nillable replacement — and `panic` is the one call statement
  deletion refuses, because removing a terminating panic manufactures a missing
  return rather than a mutant.
- Every candidate now carries a **guard site hint**: which of the Form S / Form
  C / Form D rewrites the instrumenter has to use, the bytes it replaces, and —
  for Form D — the source spelling of each type the site declares, rendered
  through a qualifier built from the file's own imports. Discovery is the only
  phase with type information and instrumentation deliberately has none, so
  the choice is made once, here, and handed down as data; that is what keeps
  the instrumenter a byte rewriter testable without a toolchain. A site none of
  the three forms can express is a recorded `unnameable-decl-type` skip rather
  than a catalogued mutant the next phase would have to hand back — a `switch`
  tag, an `if` whose condition is a named boolean type, a statement in an
  initialiser or `for` post where a block is not legal Go, a declared type the
  file cannot spell, or a `:=` that redeclares instead of declaring.
- **Form S and Form D in `internal/instrument`**, which makes every one of
  those families instrumentable and retires the last hint-less path: the site
  of a rewrite is now the hint discovery emitted, for all three forms, and the
  instrumenter no longer works out where a comparison or a boolean literal
  lives by looking for one. Form S wraps a statement in
  `if __gm.M[i] { <flattened copy> } else { <original bytes> }`, with a
  statement-deletion mutant rendering as the empty branch `if __gm.M[i] { }`
  because that is the whole of what "this statement does not run" means; Form D
  hoists the names a `:=` or a `var` declares out in front of the guard —
  `var x T; if __gm.M[i] { x = … } else { x = … }` — so that the code after the
  declaration still sees them. The declaring tokens are cut out in place rather
  than the statement being re-rendered, so every other byte of it, line breaks
  included, is still the user's own. Alternatives at one site chain regardless
  of family, which is how an arithmetic swap and a deletion of the statement
  around it end up as two branches of one guard; sites still nest, so an
  expression guard inside a statement guard's original branch keeps working.
- The named boolean type is no longer a candidate the run instruments and the
  compiler throws out. A selector evaluates to `bool`, which is not assignable
  to `type Flag bool` — so discovery hints those sites as Form S and a
  statement guard around a `return` is well typed whatever the function
  returns. `fixtures/rejectable`'s two traps are ordinary mutants now, and what
  is left for compile validation is the mutant that is not a program at all,
  such as `v * 0` swapped into `v / 0`.
- `fixtures/families`, a corpus module carrying at least one live candidate for
  each of the 42 rules, and the integration suite that drives it. Nothing else
  in the corpus could have caught a family that quietly stopped reaching
  execution: every other fixture proves one *mechanism* — the baseline gate,
  compile validation, coverage narrowing — against whichever handful of
  operators its code happens to contain, so a rule that disappeared would have
  shown up, if at all, as a count that got smaller for no stated reason. The new
  test holds the run against a per-family table of kills and survivors, names
  every survivor by file and line, and fails when any of the 42 rules produces
  no mutant at all.
  The fixture's tests are part of the specimen rather than scaffolding around
  it. Four of its functions are deliberately under-tested and one is never
  called, because a fixture in which everything died would be
  indistinguishable from a suite that is merely strong, and one in which
  everything survived would be activation that never happened; `fixtures/README.md`
  names each gap and says what its test leaves out. Its other invariant is that
  every loop terminates under every mutant of it — `negate-loop-condition` and
  `gt-to-ge` both turn an ordinary counter into one that never stops — because a
  hung mutant is a ten-second timeout where a kill belongs, and reads as a flaky
  suite rather than as the design bug it is.
- A second integration test runs the same fixture once per profile and asserts
  the tier contract end to end. `balanced ⊂ strong ⊂ all` is already unit tested
  against the rule table; what this adds is that the property survives every
  phase between the table and the report. The counts differing (59, 72, 76) is
  the readable half — the load-bearing half is that the mutant *identities*
  nest, and that the families each tier adds are exactly `bitwise` and
  `arithmetic-assignment`, then `statement-deletion`. A count can move for any
  reason; those three names are what a profile actually means.
- Licensing and policy files: dual `MIT OR Apache-2.0` with `LICENSES/` and
  `REUSE.toml` annotations for the files that cannot carry an inline SPDX
  header, plus `SECURITY.md`, `CONTRIBUTING.md`, `THIRD_PARTY_NOTICES.md`, and
  `RELEASE_NOTES.md`.

### Fixed

- A `var` inside a function body no longer ends a run with an internal error.
  Form D rewrites a declaration by cutting its declaring tokens out in place,
  and two of those cuts are as long as the source says: a spec with no
  initialiser goes whole, and a spelled-out type goes with it. Written across
  more than one line — which gofmt itself produces for a `func(` type — the cut
  removed a line break, the rewrite stopped preserving line numbers, and the
  instrumenter answered the only way it can, with `GOM7326` out of the whole
  pass. One legal declaration anywhere in the tree took every mutant in it down.
  The refusal now belongs to discovery, where a candidate can simply not be
  emitted: the site is recorded as an `unnameable-decl-type` skip and the rest
  of the file still runs. Padding the cut back to its own height is not an
  alternative and the fixtures say why — `f func(\n…\n) int = mk(n)` padded
  reads `f \n\n = mk(n)`, and the scanner ends the statement after `f`.
- Form D no longer rebinds a declaration's own initialiser, which was producing
  wrong verdicts in silence. Go begins a declared name's scope at the *end* of
  its specification, so `total := total * 2` reads the `total` declared outside
  the block and `err := fmt.Errorf("…: %w", err)` wraps the error that was
  already there. Hoisting `var total int;` in front of the assignment put the
  new name in scope first and read a zero out of it. The rewritten program
  compiles — that was the danger — so the instrumented baseline passed, the run
  scored, and mutants in the rewritten function were measured against a program
  the user did not write; the `%w` shape reported a kill for a mutant that
  really survives. Such a site is now refused at discovery. The test has to be
  lexical rather than type-directed: go/types resolves the initialiser
  correctly, to the outer object, so the object the hoist would create appears
  in no `Uses` entry and comparing against it silently answers no. The names of
  a whole `var` block are collected before any of its initialisers is weighed,
  because the block is one site and one spec may name another's.
- `fixtures/rejectable`'s traps are traps again. Its first ones were a
  comparison and a boolean literal returned as a named boolean type, and they
  were facts about a *rewrite form* — Form C's selector is a plain `bool` — so
  routing named booleans to the statement form disarmed them: the module went on
  compiling, its tests went on passing, and compile validation was left with
  nothing to isolate while its expectations still said three. The replacements
  are facts about the *mutated program*, which no rewrite can rescue: `v*0`
  swapped to `v/0` is a constant division by zero, and `200 - 100` returned as a
  `uint8` overflows when `sub-to-add` makes it 300. Both shapes are kept, in two
  files, each still outnumbered by healthy candidates, so the bisection still
  has to halve and every accepted mutant still has to come back intact.
  The named boolean did not leave with the trap; it moved to `named.go` and
  changed sides. Its four candidates are the fixture's control now — accepted,
  instrumented through the statement guard, executed, and killed — because an
  improvement is only an improvement if something fails when it is undone, and
  every other fixture in the corpus returns a plain `bool`. The engine's suite
  requires all four to be killed rather than merely accepted, and
  `internal/validate`'s activates each one and requires the suite to go red:
  accepted proves the guard compiled, killed proves it selected anything.
  Synthesising an uncompilable candidate through the `validate` API was
  considered for the rejection path and rejected as unnecessary — two natural Go
  constructs fail reliably and are already in the tree, and a hand-built
  candidate would have proved the phase against an input no discovery pass can
  produce.
- `internal/execute`'s integration harness instrumented without guard hints, so
  all four of its tests died with `GOM7329` before doing any work. It now runs
  discovery and passes `instrument.HintsOf(found.Candidates)`, the way
  `internal/validate` and `internal/engine` already did.
- The catalogue expectations across `internal/cli`, `internal/engine`,
  `internal/instrument`, and `internal/validate` absorbed the operator
  expansion. They had been written when discovery implemented two families of
  the eleven, and every count in them was short; `list --operator bitwise` also
  stopped being an "this build cannot discover it" case, because every rule the
  registry names is discovered now.

### Notes

- The dashboard draws with ASCII glyphs only, its score gauge included.
  bubbletea enables virtual-terminal processing on Windows but does not touch
  the console output code page, so a ConHost on a legacy OEM code page renders
  multi-byte UTF-8 as mojibake — and a progress bar is the one element that is
  read by its shape. The gauge is drawn here rather than with `bubbles/progress`
  for a second reason: that component's value is a spring animation, which
  needs `charmbracelet/harmonica`, a module this project does not depend on, to
  interpolate through values the score never actually had.
- The dashboard's counters are `killed`, `survived`, `timeout`, `inconclusive`,
  `errored`, and `not-run`. There is deliberately no `uncovered` counter:
  `mutation.Outcome` has no such outcome, and a mutant that coverage showed no
  test reaches is a *survivor*, which is where it is counted. The plain
  renderer's closing block states the split separately, as `uncovered N`
  alongside the six, precisely because it is a subset of `survived` and not a
  seventh bucket.
- `dogfood` and `package` are honest placeholders that echo and exit 0. They
  exist as named tasks and as CI jobs so that self-mutation and packaging can
  never be bolted on without a gate; they become real in the later phases.
- The operator catalogue enumerates 42 rules while the design plan's headline
  says 43. The registry has now resolved this in favour of the enumeration:
  `mutation.CanonicalRuleCount` is 42, asserted by the canonical registry
  tests, so the headline was the loose count and no phantom 43rd rule was
  invented to match it.
- `run` now performs real mutation testing end to end, across all eleven
  operator families: `discover.SupportedRules` covers every one of the 42 rules
  the canonical registry names, so `GOM1006` — "this pre-release build does not
  discover that operator" — can no longer be reached through any selection of a
  registered operator. The message is kept, and kept under test at the unit
  level, because it must not depend on that staying true. The outcome cache,
  `--changed`, `--shard`, the HTML report, the Stryker projection, and the
  `init`, `doctor`, `report`, and `cache` commands do not exist yet, and no page
  in `docs/` claims otherwise.
- The **Status** column is gone from `docs/operators.md` rather than filled in
  with one repeated word. It recorded the gap between "the rule mints an ID" and
  "`run` can score it", and with no rule left on the wrong side of it the column
  said the same thing eleven times. The status is a sentence at the top of the
  page instead, and `README.md`'s honest-limits list has been rewritten around
  what is actually missing — v2's `switch`/`select` and `if`-branch mutation, the
  documented exclusions, and the rewrite sites no guard form can express — in
  place of the "two of the eleven families" bullet that expansion retired.

[Unreleased]: https://github.com/P4suta/go-mutants/commits/main
