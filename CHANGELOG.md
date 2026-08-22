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

- **Scoped test binaries.** A `test.command` that go-mutants can read as a set
  of package patterns now decides which test binaries a run builds, not just
  which suites the baseline measures. A module whose tests live in three of
  forty packages compiles three binaries and starts three processes per mutant
  instead of forty. This repository's own dogfood gate went from 16 mutants in
  7m08s to 121 mutants in 48-50 seconds on the same machine — seven times the
  scope in a ninth of the time — and could grow from two files to two whole
  packages because of it. Widening it further is now bounded by
  missing tests rather than by the clock: the whole-package scope over
  `internal/mutation`, `internal/glob` and `internal/interval` catalogues 560
  mutants and runs in 1m20s, and it reports 41 real survivors in
  `internal/mutation` that need tests written rather than a scope adjusted.
  `.go-mutants.toml` records that measurement where somebody widening the scope
  will read it.

  Scoping also decides which test binaries get a *vote*, which is a correctness
  property and not only a speed one: a suite the baseline never measured can
  fail for reasons that have nothing to do with any mutant, and every mutant it
  fails is a false kill — which is exactly what happened on Linux, where
  `internal/tui`'s dashboard test failed unconditionally because the kernel
  refuses to poll a standard input that is not a terminal, so the unscoped
  dogfood job recorded all 16 mutants as killed, the one declared equivalent
  included, and exited 2 on the expectation that was then unfulfilled; the
  input handling behind that failure is fixed, and the scope keeps a suite in
  that state out of the run rather than relying on it having been.

  Until now this was the one setting that promised more than it delivered.
  `internal/execute` listed `./...` unconditionally, so naming
  `go test ./internal/mutation/...` bought a fast baseline and then measured
  every mutant against every binary in the module anyway — including the suites
  the command had just excluded. Worse, it *cost* something: coverage-guided
  selection switched off for any command that was not the built-in default, so
  the setting most likely to make a large repository tractable was also the one
  that turned off the optimisation it needed most.

  Recognition is spelling-strict, and that is the design rather than a first
  cut. A command is read when it is `go`, then `test`, then one or more package
  patterns (`.` or anything under `./` with no `..` in it) and nothing else. Any
  flag, any bare import path, a `..` anywhere in a pattern — including one that
  climbs out of the tree and back into it, because patterns are resolved against
  the disposable `go-mutants-snap-…` copy, whose directory name and siblings are
  not the workspace's — a Windows `.\internal\...`, or another program is
  unrecognised and behaves exactly as it always has: every
  binary built, every mutant measured against all of them, and a `GOM7601`
  warning saying so. There is no shortlist of harmless flags because there is no
  flag that stays harmless as the go command grows — `-run` makes the command a
  fraction of the suite, `-tags` compiles different files, `-race` changes which
  paths are taken — and the failure is silent in the direction that matters: a
  mutant skipped as uncovered that a test does cover is a kill lost and a score
  inflated. Anything go-mutants has not been taught falls back to the slow,
  correct behaviour, which is the only fallback that cannot flatter a suite.

  Recognising a command also turns coverage-guided selection **on**, where it
  used to be reserved for the built-in `go test ./...`. That is not a loosening
  of the rule but the same rule stated properly: what made the mapping sound was
  never the exact spelling of the default, it was go-mutants knowing in full
  what the command does — and it knows exactly that for `go test` over patterns,
  because it compiled the binaries those patterns name. A mutant in an included
  file that no scoped binary covers is a `survived (uncovered)` result, which is
  the honest reading of a scope that leaves the line out and the same answer the
  run would reach by executing every scoped binary against it and watching them
  all pass.

  A scope that resolves to nothing is loud: `GOM4022`. It fires when a pattern
  places no package directory at all — the go command answers that with a
  warning and an exit status of zero, so nothing downstream would have noticed —
  which is checked before the baseline is measured, and again when the scope as
  a whole turns out to hold no package with a test file, which cannot be known
  until the binaries are built. A pattern naming a directory that exists and
  holds no Go files (`./docs` without the wildcard) is left to the baseline
  instead, which runs the command verbatim and reports the go command's own "no
  Go files in ...", for the same reason `-e` keeps a package that does not
  compile from being blamed on the scope. This is the one diagnostic in the whole
  coverage-and-scoping story that does not fail open, and the asymmetry is the
  point. Everywhere else the fallback is "do more work and reach the same
  verdict", which is free to take silently. Here there is no such direction:
  widening back to `./...` would build and run the very suites the user's
  command excluded, and running no binaries at all would report every mutant as
  having survived a suite that never started — a score of zero from a run that
  never looked. Both are fictions, and a typo in a package pattern takes a
  second to fix once somebody is told which pattern it was. Each pattern is
  resolved with its own `go list -e`, which is what lets the message name the
  one that is wrong rather than reporting that the total came up short; `-e` is
  what keeps a package that does not compile from being mistaken for a pattern
  that names nothing, so `GOM4010` still diagnoses a broken snapshot with the
  compiler's own words.

  The report gains no field for any of this. `test.command` is already recorded
  verbatim, and the command *is* the scope.

  One thing a scoped command does not get yet is the outcome cache: `cache.mode
  = "auto"` still stands down for anything but the built-in `go test ./...`,
  with its `GOM7901` warning, because that rule is about whether go-mutants can
  reason about a command's reproducibility rather than about whether it can read
  its scope. Setting `mode = "on"` still promises it. Aligning the two is worth
  doing and is deliberately not smuggled in here.
- **The outcome cache.** A run reuses an outcome it has already proven, so a
  second run over unchanged code measures only what has moved. Entries live
  beside the run history — `<os cache>/go-mutants/workspaces/<key>/outcomes/` —
  under the same ownership marker, claimed through the same code, because a
  second implementation of "prove this directory is ours" would be a second
  place for the one property that makes deleting files in a shared cache
  defensible to be wrong.

  The key is a SHA-256 over length-prefixed fields: the tool version, the
  running executable's own digest, the Go toolchain's own release, the
  workspace digest, the catalogue digest, the test command, the timeout as
  configured, and `CGO_ENABLED`, `GOARCH`, `GODEBUG`, `GOEXPERIMENT`, `GOFLAGS`
  and `GOOS` — with an unset variable hashing differently from one set to
  nothing, because they are different to the go command. Entries are *filed*
  under that key rather than validated against it, which is what makes stale
  data cost nothing: editing a file moves the key, so yesterday's entries are
  unreachable rather than wrong, there is no invalidation pass to get wrong,
  and no window in which a stale answer is still reachable. The executable
  digest is what separates two development builds calling themselves the same
  version, which is every build between two releases — exactly when the guard
  forms and the rule set are changing.

  The toolchain's release is in the key because nothing else carries it: the
  test command is hashed as the user wrote it, so the default command hashes
  the word `go` and never the compiler that word resolves to, and `go.mod` pins
  a language version rather than a patch release. Without it a 1.26.5→1.26.6
  upgrade would keep every outcome the old compiler measured reachable. The
  environment list is the same standard applied to the same question: each of
  those six names changes what the tests compile to or how they are run, and
  `CGO_ENABLED` earns its place twice over because its *default* depends on
  whether a C toolchain is installed, so two CI images that differ in nothing
  else can compile different programs.

  Each entry records the full 64-character key it was written under, not just
  the 16 characters that name its directory. A truncated directory name is
  short enough for a Windows path and long enough that a collision needs about
  2³² contexts on one machine — but if one ever happened, two runs would share
  a directory *and* agree about the truncation, so only the untruncated key can
  tell them apart. A read that does not match it is a miss, never an adoption.

  The timeout is the one field that is deliberately *not* keyed on as it is
  applied. A derived timeout is `max(10s, slowest baseline × 5)`, a wall-clock
  measurement that is a slightly different number on every run, so hashing it
  would have given every run of any non-trivial project its own empty directory
  — silently switching the cache off for exactly the projects worth caching
  for. The configured value is hashed instead, and each entry records the bound
  its measurement was made under, so a lookup can be more precise than a key:
  a kill or a survival is reusable when the measurement fits inside this run's
  bound, and a confirmed timeout when this run's bound is no larger than the one
  it already blew.

  Only killed, survived, and *confirmed* timed-out are stored. Inconclusive is
  the one worth spelling out: it means two attempts disagreed, and a cache that
  froze a disagreement would make a flake permanent — the run after the fix
  would still report it. Harness errors and interruptions are not measurements;
  a mutant no test covers is settled by coverage before the cache is consulted,
  which matters because the coverage pass fails open and a cached
  "survived (uncovered)" could otherwise be adopted by a run that would have
  executed and killed it; and every mutant named in `[[mutation.expect]]` is
  measured on every invocation, because an expectation is evidence to check and
  evidence copied from yesterday's answer has not been checked.

  `cache.mode = "auto"` — the default — reuses outcomes only when
  `test.command` is the built-in `go test ./...`, and does nothing at all
  otherwise, with a `GOM7901` warning naming the command. It is the coverage
  rule, for the coverage reason: go-mutants knows what `go test ./...` does and
  nothing about a command somebody wrote, which may consult a clock, a
  database, or a network, none of which can be in the key. Standing down rather
  than degrading to read-only is the same argument once more — a read-only
  cache over such a command would still be adopting outcomes it cannot justify,
  it would merely stop accumulating new ones. `--cache on` is how a project
  promises its command is reproducible; `--cache off` is neither.

  Nothing the cache does can fail a run. A cache that cannot be opened, an
  entry that cannot be read, an outcome that cannot be written: each is a
  `GOM79xx` warning and a run that measures more than it had to, which is the
  same judgement `internal/coverage` makes and the opposite of the one
  `--changed` gets. A corrupt entry is reported once per run rather than once
  per entry, because a half-restored CI archive produces one for every mutant
  and hundreds of copies of a sentence would bury the survivors.

  The report gains `cache{mode, hits, misses, writes}` and `mutants[].cached`.
  `mode` is what the run *did* rather than what was configured — `auto`
  resolves to on or off before any mutant is executed, and recording `auto`
  would put the one value a reader cannot act on into a document whose job is
  to say what happened. `hits` is counted from the rows when the document is
  built, so the summary and the rows underneath it cannot disagree, and a
  document that claims the cache was off and reports a reused outcome is
  refused with `GOM5109` rather than published.
- `go-mutants cache status|gc|clean`. `status` prints the root, one line per
  workspace, and what is stored; `gc --days N` (default 30) removes outcomes
  written more than N days ago, since an entry is only ever readable by a run
  whose whole context still matches and one a month old has almost certainly
  outlived it — age is the modification time and reading an entry does not
  refresh it, so this removes what is old and not what is unpopular; `clean`
  removes them all. All three walk
  only `<root>/workspaces/*/outcomes`, refuse any directory without go-mutants'
  own ownership marker and say how many they skipped, and never touch the run
  history filed beside the outcomes — that is `report clean`'s. A deletion that
  fails is `GOM7911` and a non-zero exit: deleting is the whole of what these
  commands do, so one that could not delete has not done its job.
- `run --cache MODE`, overriding `cache.mode` for one invocation.
- `run --changed [=GIT_REF]`, which executes only the mutants sitting on lines
  that have changed since a ref — the merge base of it and `HEAD`, so a branch
  is measured against the commit it left rather than against everything that
  has landed on the target since. Bare `--changed` follows the upstream of
  `HEAD` and reports it by name, and `--changed=@{upstream}` is the same
  request written out: both record `origin/main` rather than the notation that
  found it, because a report should say what was compared and not how it was
  looked up. A branch that tracks nothing is told so — `GOM7712`, which names
  the `git branch --set-upstream-to` remedy — rather than being sent after a
  merge base it was never going to find. The changed set is the
  working tree — uncommitted edits and files git has never been told about
  alike. A file with no index entry produces no diff hunks however new it is,
  so reading the diff alone would have selected every mutant on an edited line
  and none at all in a file written from scratch; every line of such a file is
  new, which is exactly what `git add` would make the diff say a moment later.
  Ignored files stay out, since a repository that ignores a tree has said it is
  not source. The new `internal/gitdiff` reads the *original* workspace,
  because a snapshot deliberately excludes `.git`, and every failure is an
  error rather than a fallback: a narrowing that quietly measured everything
  would take twenty minutes where one was expected, and one that quietly
  measured nothing would exit 0 having proved nothing. Rename detection is off
  for v1 — a renamed file selects every mutant in it, which is the safe
  direction to be wrong in. Only execution is narrowed: discovery and
  validation still cover the whole module, so a `--changed` run mints the same
  ids and the same `rejected[]` as a full one, and the two documents can be
  compared mutant for mutant — which is what makes a pull request's report
  readable next to the branch point's rather than a fragment nobody can line
  up.
- `run --shard K/N` and `go-mutants report merge`, for fanning one run out
  across a CI matrix. A mutant's shard is `sha256(id)[:8] % N + 1`, published in
  the document as `shard.assignment: "id-hash-v1"` so that a consumer can
  recompute the partition. Assigning from the id alone is what makes sharding
  worth having: editing one file never reshuffles the rest, so a shard's work
  does not change shape on every commit — which a positional "every nth mutant"
  split would. Every shard discovers, validates and reports the *whole*
  catalogue and executes only its share, so the N documents are directly
  comparable, and `report merge` proves they describe one run — one tool
  version, one workspace digest, one catalogue, one changed ref, every index
  exactly once, every row owned by the shard that reported it — before
  combining them. Any mismatch is a refusal naming the first discrepancy,
  because the whole point of a merged document is that somebody will trust it.
- `go-mutants report validate FILE`, which checks a report against the schema
  this build embeds. It is why the JSON Schema validator is now linked into the
  shipped binary: nothing on the writing path validates, but two commands read
  documents somebody else wrote.
- `--explain` on `run` and `list`. `run --explain` prints every rejected mutant
  with the compiler's own words — whole and indented, since the second line of a
  diagnostic is usually the one that says whether the rewrite could ever have
  worked — and both print the suppressed sites by reason, with a sentence
  saying what each reason means. It refuses to combine with `--json`:
  everything it prints is already in the document, and mixing prose into one
  would make the output neither readable nor parsable.
- `mutants[].not_run_reason` in run-report v1: `out-of-selection`,
  `other-shard`, or `interrupted`, and `null` for every mutant that was
  measured. "Not run" on its own is the one outcome a reader cannot act on, and
  only one of the three reasons is a reason to run anything again. The pairing
  is a biconditional the builder refuses to violate in either direction.
- `selection.changed_ref` and the `shard` and `merge` blocks in run-report v1.
  `changed_ref` is nullable and always present rather than keyed off
  `selection.mode`, because the two narrowings compose: a shard of a pull
  request's diff reports `mode: "shard"` and a `changed_ref` both.
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
  `nightly.yml` (one leg per fuzz target the repository actually has, plus a
  property job at a deepened budget), and `release.yml` (verify,
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
- **The project artefacts: `reports/mutation/mutation.json` and
  `mutation.html`.** These are the only two files go-mutants writes into a
  workspace, and they are one publication in two formats. A `mutation.json`
  from this run beside a `mutation.html` from last week is worse than either
  file alone, because the two disagree and nothing in either says which is
  newer — so a failure to write the HTML puts the JSON back exactly as it was
  found, restored or removed, and the run reports the failure rather than a
  half-published pair. Both are staged in the destination directory and renamed
  into place, so a crash leaves the previous pair or the new one and never a
  mixture. They are written *after* the run's own record is filed in the
  history store, never before: the history is what a later run, a `report
  merge`, or a `report latest` reads, and the other order would leave a
  workspace holding a mutation report for a run with no record. `--report`
  chooses `none`, `json`, `html`, or `json,html`, and `none` is honoured before
  anything is read, so turning the artefacts off also turns off the work of
  building them.
- **A one-way, lossy, deterministic projection into the Mutation Testing Report
  Schema.** `mutation.json` is what the Stryker ecosystem's viewers and
  dashboards read. It is derived from the run report after that report has been
  stored, and it is never read back: nothing in go-mutants parses a
  mutation-testing-report file, because a format designed for a viewer is a poor
  place to keep the facts a run established, and a round trip through it would
  quietly become the thing everything else trusts.

  Six outcomes become five statuses, and two of the format's own statuses are
  deliberately never written. `Pending` describes a run still in progress, and
  every document go-mutants writes describes a run that has stopped.
  `NoCoverage` is the harder one: it looks like the right answer for a mutant no
  test binary reaches, and it is not the one given, because the run report's own
  vocabulary calls that mutant a survivor and the two documents must agree about
  how many survivors there were. Which survivors were uncovered is a fact the
  run report keeps and this one drops — which is what "lossy" means, and it is
  written down rather than left for somebody to rediscover as a bug. A `not_run`
  mutant is *not* omitted either: it projects as `Ignored` carrying a reason
  worded for a reader who has no run report in front of them, because dropping
  it would make the viewer's totals disagree with the run report's.

  Determinism is enforced rather than hoped for: every array is sorted
  explicitly before encoding, and `projectRoot` is omitted although the format
  allows it, because it is an absolute path on the machine that produced the
  report — it would make two identical runs produce different documents and
  would leak a developer's directory layout into a file that gets attached to
  pull requests.

  Coordinates are the part most likely to be silently wrong. go-mutants locates
  a mutant by a byte range because it splices bytes; the format locates one by
  1-based `(line, column)` whose column is counted in **UTF-16 code units**,
  because the viewer is JavaScript and a JavaScript string index is a UTF-16
  index. Byte columns would place every mutant after a multi-byte rune too far
  right, and rune columns would place every mutant after an emoji or a
  mathematical symbol too far left — and the schema, which asks only that the
  numbers be at least 1, would accept either. The projection also re-reads the
  *pristine* tree, never the instrumented snapshot, and refuses (`GOM5202`)
  when a span no longer covers the text the report says it covers: editing a
  file while a run is in flight is what a developer does while waiting, and
  every coordinate derived from a moved span would be wrong in a document that
  would still validate.
- **Validation before writing, against a vendored copy of somebody else's
  schema.** A projection into another project's format is a promise about that
  format, and the only way to keep such a promise is to hold the format's own
  definition and check the document against it. `schema/stryker/` carries
  mutation-testing-report-schema 3.9.0 with its Apache-2.0 licence, a
  `PROVENANCE.json` recording the URL, the npm integrity hash and the SHA-256,
  and a test that the bytes still match. It is compiled with no default draft,
  because it declares draft-07 itself and forcing 2020-12 on it would silently
  change what `definitions` and `additionalProperties` mean in a document that
  is not ours to reinterpret.

  Every projection is validated before anything is written, and one that fails
  aborts with `GOM5203` having touched nothing. A document that appears
  authoritative and is not would be worse than no document at all — and because
  the check runs on the way out, a file that exists on disk is one another tool
  will accept. It also catches the trap the version numbers set: the
  `schemaVersion` a document carries is `"2"`, the major version of the report
  *format*, not the 3.9.0 of the npm package the schema came from, and the
  schema's own pattern refuses a document claiming `"3"`.
- **A self-contained HTML report, and why zero network is non-negotiable.** A
  mutation report is opened from a CI artefact, from a shared drive, from a
  `file://` URL on a laptop on a train. Every one of those is a context where a
  page that fetches anything shows an empty frame — so nothing in the page
  causes a network request. The viewer's JavaScript is inlined from the
  vendored bundle and the data is inlined as a JSON island; the only URLs in
  the file are inside that bundle (the SVG namespace, documentation
  hyperlinks a reader may click, `data:` image URIs) plus the attribution
  comment, and `default-src 'none'` blocks every fetch regardless. An
  integration test cuts the vendored bytes out and fails on any URL in what
  go-mutants itself emits.

  The page carries a strict `Content-Security-Policy` even though nothing served
  it. That is not defence against the file's author, who is go-mutants; it is a
  *statement* that the page needs no network, enforced by the browser rather
  than asserted in a comment. `default-src 'none'` means a future edit that adds
  a font, a tracker, or a "check for updates" fetch does not silently work — it
  breaks loudly, in review, instead of turning a report somebody attached to a
  pull request into a beacon. The two executable scripts are allowed by SHA-256
  hash and by nothing else: no `'unsafe-inline'`, and no nonce, because a nonce
  in a static file is a constant, which is `'unsafe-inline'` with extra steps.
  The hashes are computed from the very strings the renderer concatenates, so a
  script edited without updating the policy yields a page that refuses to run
  rather than one that runs something unvouched-for.

  The JSON island is not hashed and does not need to be: a `<script>` whose type
  is not a JavaScript MIME type is a data block, and the HTML parser returns
  from "prepare the script element" before the CSP check ever applies. It is
  escaped instead, which is the protection that actually matters for it — `<`
  becomes `\u003c`, and *every comparison operator go-mutants mutates is a `<`*,
  so a `</script>` reaching the parser as markup is an ordinary case rather than
  an exotic one. `&`, `>`, `U+2028` and `U+2029` go with it.

  The vendored viewer's SHA-256 is re-checked **at render time**, on every page,
  against both the constant in `vendor-assets` and the digest in its
  `PROVENANCE.json`. Checking it at build time would prove something about the
  machine that built the binary; checking it here proves something about the
  bytes about to be written into a file somebody will open and trust. A mismatch
  aborts with `GOM5210` rather than shipping an unvouched-for quarter-megabyte
  of JavaScript.
- **`doctor`.** An aligned table over the six things a run needs before it can
  measure anything: the Go toolchain and where it was found, the module this
  directory is the root of, git, whether go-mutants' own directory under the
  operating system's cache root can be written, the platform, and whether
  `.go-mutants.toml` parses and resolves. Every check runs whatever the ones
  before it found, because a machine with two problems should learn about both
  at once rather than one CI round at a time. A `warn` is a check that failed on
  something only an opt-in feature needs — git, which only `run --changed` asks
  for — and never fails the command; any `FAIL` exits 2. `--json` emits a
  `go-mutants/doctor` v1 document, validated against the new
  `schema/doctor-v1.schema.json` *before* it is printed, on the same argument
  `report merge` makes: a document that fails the schema go-mutants itself
  publishes is a poor thing to hand a script that is deciding whether to trust
  this machine. The cache check proves writability by writing, so where it
  writes is a safety property: a probe file created and removed inside
  `<os cache>/go-mutants` and nowhere else.
- **`init`.** A fully commented `.go-mutants.toml` whose every value is
  interpolated from `config.Defaults()`, so adopting the file changes nothing
  and a changed default cannot leave a stale number behind in the text. A test
  parses what it generates and asserts the result is *exactly* `Defaults()`.
  Three settings are commented out rather than written, and each for a reason:
  `execution.jobs` is `min(CPU count, 8)`, which would make the generated file
  machine-dependent and `init --check` a gate that fails on the wrong hardware;
  `test.timeout` is zero meaning "derive it", which no duration spells; and the
  empty lists (`mutation.exclude`, `mutation.operators`, `[[mutation.expect]]`)
  are decisions that read as oversights when written out. There is no `--force`,
  deliberately: a configuration file is hand-edited and is usually the only
  record of decisions nobody wrote down twice, so deleting it first is the
  deliberate act such a flag would only have pretended to be. `--dry-run` prints
  and touches nothing; `--check` compares byte for byte and exits 1 — the one
  place in the CLI where 1 is not a policy gate, and still an opt-in gate
  somebody asked for.
- **`report list`, `report latest` and `report clean`.** The run history is
  filed under `<os cache>/go-mutants/workspaces/<key>/`, where the key is a
  digest of the workspace's *contents* — so two runs with an edit between them
  are stored apart, by design, since that digest is what makes a mutant id mean
  something. These three gather one module's runs back together by the
  `workspace.module_path` in each document, which is why they are run from a
  module root: without a go.mod there is nothing to say whose history is being
  asked about, and for `clean`, which deletes, guessing would be the worst
  possible answer. `list` prints an aligned table newest first and exits 0 on an
  empty history, because that is a true answer; `latest` summarises the newest
  run and names its file, and `--json` prints the stored bytes verbatim rather
  than a re-encoding, since an archive reshaped on its way out is not an
  archive; `clean` removes `runs/` and `latest.json` and nothing else, leaving
  the ownership marker so the directory keeps an identity a concurrent run may
  be relying on, and leaving `outcomes/` to `cache clean`.

  A document that cannot be read is a row in the listing rather than an error:
  one truncated file must not cost a user the forty-nine runs beside it, and
  "this file is not a run report" is exactly what they need told. A directory
  with no marker, or one this build did not write, is reported and left alone —
  it is neither listed as this module's history nor deleted, whatever the
  documents inside it claim. So is one whose *name* is not the key its own
  marker names: a workspace directory copied or restored under another name — a
  CI cache unpacked into a fresh key, a backup taken by hand — carries a
  perfectly genuine marker naming the original, and `clean` deletes by digest
  and rebuilds the original's path from it. Listing such a copy as history would
  report it swept while it sat on the disk, so it is reported as skipped
  instead. And nothing is deleted on the strength of how a path is spelled:
  containment in the store is proved against what the filesystem resolves, so a
  directory somebody replaced with a link is one go-mutants refuses rather than
  one it follows out of the cache.
- Licensing and policy files: dual `MIT OR Apache-2.0` with `LICENSES/` and
  `REUSE.toml` annotations for the files that cannot carry an inline SPDX
  header, plus `SECURITY.md`, `CONTRIBUTING.md`, `THIRD_PARTY_NOTICES.md`, and
  `RELEASE_NOTES.md`.

### Fixed

- A run no longer stops because `go vet` disapproves of go-mutants' own
  generated code. A Form C guard renders each alternative from the pristine
  bytes with one edit applied and splices it in beside the original, so the
  `or-to-and` mutant of `s == "." || s == ".."` writes `s == "." && s == ".."`
  into the snapshot verbatim — legal Go, always false, and exactly what vet's
  `bools` analyzer reports as a suspect and; `s != "." || s != ".."` is the same
  trap from the other side. `go test` and `go test -c` both run a default vet
  subset that includes `bools`, so any project with ordinary path handling in it
  failed at `GOM4013` when the instrumented baseline ran the test command, or at
  `GOM7505` when a per-package test binary was built, with a diagnostic naming
  code its author never wrote and cannot fix.

  `-vet=off` is now merged into `GOFLAGS` for exactly the two commands issued
  against the *instrumented* tree, and for nothing else. The scope is the whole
  point: the pristine baseline runs the project's real test command with vet at
  its default, so a genuine `bools` finding in the user's source still stops the
  run — before anything is instrumented — and their own CI still sees everything
  it saw before. What is suppressed is an analyzer's opinion of a rewrite, not
  an analyzer's opinion of them. `go build` and `go list` are untouched by
  definition, since neither defines the flag; compile validation is a
  `go build` and keeps rejecting a mutant the compiler really refuses.

  Merged rather than set, through a new `gocmd.AppendGoflags`, because `GOFLAGS`
  is also how a developer, a CI image or a toolchain manager says `-mod=readonly`
  or `-tags=…`, and both packages inherit that on purpose: overwriting the
  variable would compile a different program from the one the project builds,
  which is a quieter failure than the one being fixed. `mise run dogfood` had
  been working around this with a run-wide `GOFLAGS=-vet=off`, which is a
  workaround no user of the tool could have been expected to find; that export
  is gone. `fixtures/vetsuspect` holds both suspect shapes and the engine's
  integration suite requires all ten of its mutants to be *executed*, which is
  the assertion the old behaviour cannot pass — it never got as far as building
  them.
- `internal/runner`'s process-tree tests no longer fail under whole-suite load.
  Each of them kills a helper child that has been given a fixed 1500ms to boot
  and fork its grandchild, and each says so and fails rather than passing
  quietly when the kill lands before the fork — which is right, and which is
  exactly what made them flaky. The child is a whole coverage-instrumented Go
  test binary starting while twenty sibling packages start, and on a machine
  running `go test ./...` 1500ms is not always enough.

  Raising the constant was considered and the code says why it is not the fix.
  That one number is doing two jobs at once — it is the moment the kill lands
  *and* the tolerance for how long the child takes to boot — and the
  grandchild's own delay has to sit past the first of them, so a load-proof 15s
  would have inverted that inequality and turned four tests deterministically
  red; and because the helper sleeps for a minute and so never exits early, the
  run always takes the whole deadline, which makes a generous constant a cost
  every run pays rather than a ceiling only a slow one reaches.

  The deadline now starts at 1500ms and doubles up to a 15s ceiling, and only on
  the attempts that caught a child which had not yet forked, so an unloaded
  machine never reaches a second attempt and the suite's wall clock is
  unchanged. The grandchild's delay is *derived* from the deadline in use rather
  than asserted against it in a comment two constants away, which is the part
  that keeps the next edit from reintroducing this. The proves-nothing guard is
  kept and sharpened: a spawn that is genuinely broken reports nothing at any
  deadline and fails by name rather than being retried into silence. The
  concurrent case retries the whole burst instead of the runs that lost the
  race, because concurrency is its subject and a lone retry would not reproduce
  the adoption gap it was written for, and every attempt's sentinel is checked
  at the end rather than only the last one's — a grandchild that escaped on the
  first attempt is the bug the test exists for. The positive control watches for
  its sentinel instead of stat-ing once, since the grandchild is another Go
  binary that can still be booting when its parent's own 600ms are up.
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
- `cache status` and `cache gc` no longer pluralise "directory" by adding an
  "s". The counted noun takes the one irregular plural these messages need,
  which is the kind of wart that makes a careful tool look careless.
- The exit code table injected into every command's help said that 1 meant
  `--strict` or `policy.minimum_score`. `init --check` exits 1 as well — it is
  an opt-in freshness gate somebody asked for, which is the same category — and
  the table is part of the command line contract that CI configurations branch
  on, so a table naming only two of the three gates is a table that will be
  believed and be wrong.
- The project-artefact integration tests copied the fixture module *including*
  any `reports/mutation/` already in it. That directory is gitignored precisely
  because a manual run against one of the corpus modules leaves one behind, so
  a developer's tree could carry a stale pair that a clean checkout does not —
  and `--report html` then looked as though it had written last week's
  `mutation.json`, while `--report none` looked as though it had created a
  directory it never touched. The copy is now cleared before the run: asserting
  that a file is absent afterwards means nothing unless its absence beforehand
  is a fact rather than an assumption.
- `internal/mutation` no longer declares a second skip-reason vocabulary.
  It carried fifteen `SkipReason` constants spelled differently from the nine
  `internal/discover` emits — `generated-file` against `generated`,
  `cgo-package` against `cgo`, `switch-case` and `select-case` against the one
  `case-label` — and nothing in the tree ever read one of them: not the CLI, not
  the report builder, not the schema. Only `KnownSkipReasons` did, and its test
  pinned that function against the same fifteen strings retyped, so the list
  agreed with a copy of itself and with nothing a user could ever see. That is
  the same shape of guard already removed from discovery and reporting, and it
  is worse than no guard: it reads as a checked contract, so a contributor
  adding a reason would reasonably have added it here and watched a green test
  tell them the schema knew about it.

  The vocabulary is deleted rather than realigned. Skip reasons belong with the
  decision that produces them, and discovery's `AllSkipReasons` is already the
  single list they come from: every reason it declares is checked against the
  run report schema's `reason` enumeration, which is a superset holding two
  names — `struct-tag` and `label-or-goto` — that no constant in the tree
  declares yet, reserved so that landing them is a code change and not a schema
  change. A test parses discovery's own sources, so a tenth constant fails in
  the commit that adds it. A second list could only ever be the copy that
  drifts. The published enumeration is unchanged, `docs/operators.md` and
  `docs/json-schema.md` already documented discovery's spelling, and a note
  where the constants were says where reasons live and why nothing should start
  a second list here.
- The outcome cache's maintenance walk no longer looks inside a workspace
  directory that is a copy of another one. `cache status`, `cache gc` and
  `cache clean` checked the ownership marker and stopped there, and a marker is
  a genuine claim about the *original* directory: a CI cache restored under a
  different key, a `cp -r` backup, an `xcopy` of somebody's cache folder all
  carry it verbatim under a name this build would never have chosen. So the
  copy's entries were counted as the marked workspace's by `status` and then
  deleted as the marked workspace's by `gc` and `clean` — one workspace's
  stored outcomes swept under a key that was never theirs, which is not an
  arithmetic error anybody can undo once it is noticed.

  The walk now requires a directory's name to equal `WorkspaceKey` of the digest
  its own marker states, and reports the mismatch as a skipped row with a reason
  naming the directory the marker really belongs to — the same shape the run
  history's `List` already used, in the same words, because these commands walk
  the very same directories and two walks disagreeing about what a workspace is
  would make `cache status` and `report list` describe different stores. The
  hazard on this side is the plainer one: the cache keys its deletions by the
  directory entry it is standing in, so unlike the history store it never
  reported a sweep it could not carry out — it carried out a sweep it should
  never have been asked for.

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
- `dogfood` and `package` were honest placeholders that echoed a sentence and
  exited 0 when this note was written, listed as named tasks and as CI jobs so
  that self-mutation and packaging could never be bolted on without a gate. Both
  do real work now: `package` runs `goreleaser release --snapshot --clean` and
  then asserts that the built binary's `--version` is the stamped one rather
  than `internal/cli`'s compiled-in fallback, and `dogfood` runs go-mutants
  against this repository under `--strict`. Running is not gating, and the two
  are tracked apart: `docs/release-checklist.md` states what a dogfood run has
  to show — `inconclusive 0` among it — before its box can be ticked.
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
  `init`, `doctor`, `report`, and `cache` commands all landed in the phases
  after this note was written, and no page in `docs/` claims otherwise in either
  direction.
- The **Status** column is gone from `docs/operators.md` rather than filled in
  with one repeated word. It recorded the gap between "the rule mints an ID" and
  "`run` can score it", and with no rule left on the wrong side of it the column
  said the same thing eleven times. The status is a sentence at the top of the
  page instead, and `README.md`'s honest-limits list has been rewritten around
  what is actually missing — v2's `switch`/`select` and `if`-branch mutation, the
  documented exclusions, and the rewrite sites no guard form can express — in
  place of the "two of the eleven families" bullet that expansion retired.

[Unreleased]: https://github.com/P4suta/go-mutants/commits/main
