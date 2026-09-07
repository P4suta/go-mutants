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

- **`Workspace.Module` answers what the frozen module holds, so a consumer does
  not have to run `go list -json` itself and parse it.** The library had already
  frozen the tree, located the toolchain and settled the report and snapshot
  exclusions, and then handed the question back: docs/library.md carried a
  recipe, and every consumer re-decided the same four things from it — which
  package patterns are legal, how `-tags` is spelled, how much output to keep,
  and where the module's Go version comes from. Four answers per consumer, each
  able to be wrong on its own, to a question the workspace was already holding
  every input to.

  So it is answered directly and typed. `Module(ctx, ModuleQuery)` returns
  `{Path, GoVersion, Toolchain, Packages, TraceSeq}`, each `Package` carrying
  `{ImportPath, Dir, Name, HasTests, GoFiles, TestGoFiles, XTestGoFiles,
  Imports, Deps, EmbedFiles}`, sorted by import path so two listings can be
  diffed. `Path` and `GoVersion` are go.mod's own, read in this process with
  `golang.org/x/mod/modfile` rather than asked of a child; `Toolchain` is what
  `ToolchainVersion` reports, so the three identities a consumer keys evidence
  on come from one call. `HasTests` is carried rather than left to be derived,
  because two consumers deriving it from two different file lists is two answers
  to one question.

  It is the same call as `Workspace.Exec` underneath and keeps the whole of its
  lifecycle: it holds the workspace shared and the tree shared around one
  `go list -e=false -json=<fields>` child, so it runs before a preparation,
  beside one — waiting only for the instrumentation window — and after one that
  succeeded, and it is refused after a failed preparation with
  `ErrPrepareFailed` and after `Close` with `ErrWorkspaceClosed`. The child is
  in the recording like every other, as an `exec` event of kind `go-list` with
  the subject `module`, and `Module.TraceSeq` is the join. None of it is
  evidence: no new `PreparePhase`, and nothing in an ID, a digest or a cache
  key.

  Only the *document* is decoded. `go list` writes its JSON to stdout and writes
  `go: warning: "./x/..." matched no packages`, `go: downloading …` and toolchain
  switches to stderr, every one of them on a command that exits **zero** — so a
  decoder reading the combined capture would refuse a perfectly good listing on
  the first byte of a line the go command wrote as a courtesy, and the advertised
  "call it before `Prepare`, on a machine that has not built this module yet"
  case would never have worked. `internal/runner` grew `Spec.SeparateStdout` and
  `Result.Stdout` for it. The combined capture is unchanged, is what a failure
  quotes and is still what the recording digests; a pattern that matches nothing
  is an empty `Packages` and no error.

  The answer is **memoised** per distinct query, and dropped whenever a
  `Workspace.Exec` call returns. The tree does not change on its own, so a
  consumer that wants the package list in three places pays one `go list` for it
  — but a command *can* change it, and nothing refuses one that does: a
  `go generate`, a test that rewrites a golden file, a fuzz target the go
  command files a crasher for. A package set that outlived one would describe a
  tree nobody has, and "did this command write" cannot be answered without
  re-freezing the tree, so every listing is dropped whatever the command did.
  `Prepare` needs no such rule: its integrity gate refuses a tree a command has
  changed, and a successful preparation leaves the tree byte for byte the one
  `Open` froze. The key is the normalised query — patterns in the order given,
  tags sorted and deduplicated — so `ModuleQuery{}` and
  `ModuleQuery{Packages: []string{"./..."}}` are one question, while a different
  tag set is a different one. That last half is what a memo must not get wrong:
  a listing under `-tags special` is a different file set and not another
  spelling of the same one. A query that *failed* is not remembered, so a
  toolchain that could not answer once is asked again.

  Two callers asking the same question **share one `go list`**, and the listing
  belongs to neither: it runs under a context detached from every caller's,
  cancelled only when the last interested caller has left. So the caller that
  started it may go away and the one still waiting is handed the whole answer
  rather than somebody else's `context canceled`; when every caller leaves
  before the listing finishes, the child is cut off and nothing is remembered,
  so the next caller lists again rather than being handed what a torn-down
  listing came back with.

  A query the engine will not resolve is refused **before any child starts**,
  with `ErrInvalidQuery` quoting the element that was wrong: an absolute
  pattern, one that escapes the module, one that is not module-relative, one
  beginning with a dash the go command would read as a flag, and a `Tags`
  element holding a comma or whitespace. It is a refusal rather than a listing
  of nothing, on the rule `ErrInvalidSelection` already states — a pattern
  nothing can satisfy names no package, and a consumer told that the module
  holds nothing would believe it. Absolute is judged the same way on every
  operating system, Windows shapes included (`C:\x`, `\\?\C:\x`,
  `\\server\share`), because a query is composed from a consumer's own
  configuration and a path typed on Windows reaches a Linux runner unchanged.

  Every *other* failure — `go list` exiting non-zero, a child that would not
  start, a cancelled context, a go.mod that could not be read — is an
  `*ExecutionError` with `Call: "module"` and a message beginning
  `gomutants: module:` and a space, wrapping its cause so `errors.Is` still
  reaches it and
  carrying the toolchain's own output. That type is borrowed rather than a new
  one invented, because it already names this fact one call up; the sentinels
  are the only refusals that keep their own shape, because they are the ones a
  consumer branches on rather than reports.
- **The test binaries are compiled from the frozen manifest, so nothing a
  command writes during the build reaches them.** A preparation's
  instrumentation window ends at `main_restoration` and the test binaries — the
  longest phase of a real preparation — were compiled after it, *from the tree*:
  the overlay replaced the instrumented sources and the compiler read every
  other file where it lay. A `Workspace.Exec` command running beside the
  preparation, which is exactly what the overlap above exists to allow, could
  therefore have its bytes compiled into the binaries. The re-digest that stood
  under that, `DriftError{Stage: "test binaries"}`, caught every write a command
  *left* behind and could not catch one made and undone while the compiler was
  between one file and the next — a transient edit was compiled in and gone
  before the digest looked.

  So the build's inputs are now the manifest rather than the tree. At the top of
  the window — under the exclusive lock, from a tree the integrity gate has just
  proved is byte-identical to the manifest — every file the snapshot froze is
  copied into a directory the preparation owns, each copy's digest checked
  against the manifest entry it came from as it is written, and the overlay
  names all of them. The instrumented sources keep their own mapping and win
  where the two meet, so the session still compiles the mutated program.

  The file set is the manifest **whole**, not a list of the extensions a build
  reads. `go` reads Go sources, `go.mod`, `go.sum`, assembly, the cgo inputs and
  `//go:embed` targets through `-overlay` — all of them verified against the
  toolchain in use rather than assumed — and a `//go:embed` can name any path in
  the module, a `testdata/` fixture or a `README.md` included, so a list of
  extensions would have to parse every source in the module to be sure and every
  miss in it would be a file read off the disk with nothing saying so.

  **What it costs.** One whole-tree copy per preparation, of exactly the
  snapshot's bytes, kept for as long as the session — so a prepared workspace
  holds the module twice over, three times with a probe tree. The time is
  proportional to the tree and is paid inside the instrumentation window, where
  a command waits for it: 679 files and 7.7 MiB of this repository in 50-90 ms,
  and under a millisecond for each `fixtures/` module. Identity mappings cost no
  build-cache hits. It is recorded in a trace as the `freeze-build-inputs` stage
  with the file count and byte total, so a slow preparation says how much of
  itself went there, and it is deliberately **not** a new `PreparePhase`: that
  vocabulary is shared with goatest and closed. A preparation that gives up
  removes the copy, on the rule the probe tree already follows, unless
  `OpenOptions.KeepTemp` asked for it — in which case it stays inside the
  workspace scratch that `Close` already preserves as `kept-scratch`.

  Two things follow. `DriftError{Stage: "test binaries"}` is **gone** — with no
  frozen file reaching the compiler off the disk there is nothing left for a
  re-digest between the last build and the published session to protect. And
  `Session.Changes`'s baseline is the manifest rather than a scan taken after
  the build, which is what the binaries were built from: a write a command
  leaves during the build is now reported by `Session.Changes` and refused by
  nothing, exactly as a write after a successful `Prepare` is. No message
  changed — the retired stage used the generic sentence every
  instrumentation-adjacent check uses — and nothing exported moved.

  One residual is stated rather than hidden, in `docs/library.md` and in
  [ADR 0007](docs/adr/0007-commands-overlap-preparation.md): `-overlay` replaces
  the paths it names and the `go` command still lists the real directory, so a
  **new** file a command creates in a package directory while the binaries
  compile is still seen by the compiler. `Session.Changes` reports it as an
  addition. The gap that used to be one transient write wide is now one added
  file wide.

  None of this is about **run** time, and `Session.Changes` is where the
  difference shows. A prepared binary still starts in the directory of the
  package it was built from, so a test that opens `testdata/` reads the tree and
  one that writes — a golden file, a fuzz crasher the runtime files under
  `testdata/fuzz/` — writes into it. That is a change to the tree like a
  command's, reported by the same call and refused by nothing.
- **`Workspace.Exec` runs beside `Workspace.Prepare`, waiting only for the
  stretch of a preparation that actually rewrites the tree.** A preparation used
  to hold the workspace exclusively for its whole duration — minutes of
  discovery, compile validation, verification and test binaries — so a consumer
  that wanted to run its own `go vet`, `go build` or baseline while that
  happened had to open a *second workspace* over the same root: a second
  snapshot of the module, a second toolchain probe, a second discovery pass and
  a second compile of everything, in order to run work about the tree the first
  workspace had already frozen. goatest did exactly that, and does not need to
  any more.

  The lock was that wide for a reason that is true of only part of the call.
  `main_validation` instruments the sources in place and `main_restoration` puts
  them back; in between, the files on disk are a program nobody wrote. That
  stretch — from the integrity gate to the end of restoration — is now the
  *instrumentation window*, and it is the only thing a command waits for.
  Everywhere else a preparation reads the same frozen bytes a command reads, so
  the two overlap.

  The rule is symmetric and stated as a lock rather than as a policy. A command
  issued while the window is open waits for it and then runs against the
  restored tree; a command already running when a preparation reaches its gate
  makes the *preparation* wait, so a long baseline delays instrumentation and
  can never corrupt it. `Workspace.Close` still waits for a preparation, because
  a workspace that removed its snapshot underneath one would be a use-after-free
  with a friendlier name. The one place a command may not be started from is a
  `PrepareOptions.Trace` callback, which runs on the preparation's own
  goroutine.

  A window that **fails** is the case worth stating outright, because the
  obvious implementation gets it wrong. Validation can break, a context can be
  cancelled, a restoration can fail to write — and the unlock that gives up on
  the window is the same unlock that wakes every command queued behind it, into
  a tree that still holds the instrumented sources. So the failure is published
  before the window is unlocked and every command re-asks whether it may still
  run once it holds the tree: a command that waited out a failed window is
  refused with `ErrPrepareFailed` and never runs at all.

  `docs/library.md` has the three locks, what a command sees at each moment, and
  which drift check names which write;
  [ADR 0007](docs/adr/0007-commands-overlap-preparation.md) has the decision and
  the residual — the window is exclusive only because validation compiles a
  mutated program by writing it into the tree, and a validation that built
  through the overlay instead would need no window at all.
- **The dogfood gate reads its own coverage and validates its own documents.**
  This repository's own `.go-mutants.toml` now includes `internal/coverage/*.go`
  and `internal/schemas/*.go` as well, so the gate is eight whole packages rather
  than six: the six below plus the coverage reader and mapping that decide which
  suites a mutant is measured against, and the JSON schema validation every
  published document passes through. 835 mutants — 450 in `internal/mutation`,
  146 in `internal/coverage`, 89 in `internal/schemas`, 68 in `internal/glob`,
  48 in `internal/interval`, 16 in `internal/operatorselect`, 11 in
  `internal/drift`, 7 in `internal/testflag` — 809 detected, 808 killed and one
  caught by the timeout, 26 declared, **100.00%**, 24–30 seconds at `--jobs 4`
  against a warm build cache where six packages took ten. CI's `dogfood` job
  keeps its 25-minute budget: cold runs of the widened scope measured 1m36s and
  2m14s on a machine that was doing other things at the time.

  The two packages are there because the tests that kill their survivors are
  there, which is the only way this list is allowed to grow. The first
  measurement over `internal/coverage` reported 28 unexpected survivors and 7
  mutants no binary reached at all; the first over `internal/schemas` reported
  21 and 11. Forty of those 49 are now dead, killed by sixteen named tests and
  ten new cases in tables that already existed, and every uncovered mutant in
  `internal/coverage` is now executed. Nothing was excluded and no budget was
  cut: the survivors that are left are argued, one row each.

  What the tests found is worth more than the score. `ParseTextfmt` had never
  been asked about a *reader* that fails — only about documents that are wrong —
  so a profile truncated halfway through a pipe was returned as a short profile
  and no error, which is a coverage map quietly missing the blocks it never read
  and mutants reported as uncovered survivors with nothing saying why. It had
  never been given a coordinate too large for an `int` either, where
  `strconv.Atoi` returns `math.MaxInt64` *and* an error and the 1-based check
  alone waves it through. Its closing position and its statement count were
  never malformed in any test, only its opening position and its execution
  count, so the second of each pair was unchecked by construction. `Map` had
  never been given a file's blocks out of the order the toolchain writes them,
  which is the case the index sorts for, nor two blocks that nest, nor a span
  whose end precedes its start. `resourceURL` had never been given a schema
  without an `$id`, because every schema in this repository has one that is
  exactly the fallback — so both branches returned the same string and nothing
  told them apart. And `firstViolation`'s second and third sort keys had never
  run at all, because no test document had ever produced two violations at one
  location; a trace event carrying a payload that belongs to another event type
  produces one per foreign payload, all of them at `/type`, and now two of them
  pin which complaint a reader is shown.

  Four of those tests are white-box, in a new `map_internal_test.go`, and the
  file's header says why: the index records a file it never reached and nothing
  downstream reads that record back, `merge` joins two adjacent ranges into one
  and `covers` answers a query about the join exactly as it answers one about
  the pair, and `relativeTo` refuses a name that is not a path. Each is a
  documented promise and each is what makes the structure above it cheap or
  honest, and none of them changes an answer `Map` gives — so a test written
  through `Map` could not reach them, and the mutation run said so by leaving
  the lines alive.

  The nine new `[[mutation.expect]]` rows are two claims. Two of them are one
  comparator's secondary key in `internal/coverage`, mutated two ways: the key
  orders only intervals that already share a start line, and `merge` folds any
  run of those into one interval reaching the furthest of their ends whatever
  order the sort leaves them in, so no permutation of a tie changes the merged
  list and `covers` reads nothing else. Both are executed rather than skipped —
  a case in `map_test.go` gives one file two blocks opening on one line — so the
  rows are fulfilled by a test binary that ran. The other seven are the
  "embedded schema that cannot be used" branches in `internal/schemas`:
  `schema.FS` is an `embed.FS` fixed at build time, so no input, flag or file on
  disk can make it fail to read, stop being JSON, refuse its `$id` or fail to
  compile. `TestEveryRegisteredSchemaCompiles` is the negation of all seven,
  asserted on every run of the suite, so the day one becomes reachable is a day
  that test is already red and these rows are stale — which is exit 2, not a
  quiet pass. The guards stay, because they are what makes a broken schema take
  down one report format rather than the whole run.

  `policy.minimum_score` stays at 99, and the arithmetic is in the file. Growing
  from 583 scored mutants to 809 moves the slack that floor buys from five
  survivors to eight — 801/809 = 99.01% clears it and 800/809 = 98.89% does not
  — where the last time it moved, 96 was buying four survivors at 120 mutants
  and would have bought twenty-one at 544, which is a fivefold loosening wearing
  an unchanged number. Three is not that. The floor has never been the gate that
  guards CI — `--strict` fails on the first unexpected survivor, and that is the
  flag `mise run dogfood` passes.

  `docs/development.md` §11 moves with it, because a scope table that names
  three packages and quotes 566 mutants is a document describing a gate this
  repository no longer runs.
- **`docs/development.md`, six architecture decision records, and the tests
  that keep them true.** The developer infrastructure of this repository grew a
  great deal in a short time — a shared hermetic harness, a test-owned build
  cache, two tiers with an allowlist ratchet, keep-on-failure scratch, a
  per-package cost table, an execution trace, a diagnostics bundle, `-v`/`-vv`,
  `explain` — and every piece of it was documented where it was implemented: in
  a package doc, in a `mise.toml` comment, in a workflow step. Each of those is
  the right place for *why that thing is the way it is*, and none of them
  answers "I have a failing test on a runner I cannot log into, what do I do".
  There was no page a contributor could be pointed at, so the answer was a
  conversation every time. `docs/development.md` is that page: the hermetic
  policy table, the tiers and what they cost, where tests write and who collects
  it, a walkthrough for a failing test and one for a failing run, the goldens,
  the helper processes and the scripted `go`, the corpus, and the dogfood
  gate's floor and how to widen it — always by writing the tests that kill the
  survivors first, never by excluding what survives.

  The records are for the decisions that constrain what may be built on top:
  every subprocess is recorded at the runner and every `runner.Spec` names a
  `Kind` (0002); diagnostics live in the report directory, which is the one
  in-tree place the snapshot never reads, and never in a temporary one (0003);
  verbosity is a rendering of the events a run already records rather than a
  second set of print statements (0004); the test harness owns its temporaries,
  says so with a marker file, and one tool collects them (0005); a selection
  is advisory and takes no part in `Catalog.PreparedDigest` (0006); and the unit
  tier scripts the `go` command rather than needing one installed, which is why
  a file that constructs a fake is not driving a toolchain (0008). 0007 is left
  free for a decision still in flight.

  Prose drifts silently, so the derivable parts are derived and pinned.
  `internal/testkit/devdocs_test.go` fails when the page stops naming a
  `GO_MUTANTS_TEST_*` or `TESTKIT_*` variable the harness declares, stops naming
  a `mise` task, documents a `mise run` that is not a task, or when an ADR is
  written and never linked from the index. `trace/docs_test.go` fails when an
  exec kind exists in the code and nowhere in `docs/trace-v1.md`, in both
  directions. `run --help` now names `GO_MUTANTS_TRACE` beside its flag, as
  `--keep-temp` and `--no-diagnostics` already named theirs, and a test in
  `internal/cli` says so.
- **The dogfood gate covers `internal/testflag`, `internal/operatorselect` and
  `internal/drift`.** The floor is six packages where it was three: 600 mutants
  against 566, all 34 new ones killed, still 100.00%, still seventeen declared
  equivalents — not one of them new. The three packages were measured before
  they were included, one run each, and every mutant in them was already dead:
  `testflag` 7, `operatorselect` 16, `drift` 11. So this change is three
  `include` globs and the three `test.command` patterns that give them a binary,
  with no test written and no `[[mutation.expect]]` row added, because there was
  no survivor to kill and therefore nothing to argue equivalent. A row for a
  mutant a test could kill is the skip list `.go-mutants.toml` refuses to keep,
  and so is a row for a mutant nothing survived.

  They are the small deciders the pure core is trusted through, which is why
  they were the next three: which argument names a test-binary flag, which
  rules a profile or an `--operator` name selects, and which change to an
  instrumented snapshot the instrumentation did not make. Each is a pure
  function of its inputs, so a mutant either changes an answer or it does not —
  the same property that let the first three packages in.

  It cost about a second. The run before this widening and the run after it,
  back to back at `--jobs 4`, three runs each: 8.8–9.0s for 566 mutants and
  9.9–10.5s for 600 against a warm test-owned build cache, and 38 seconds
  either way against a cold one. Cold is the case CI measures, because the
  dogfood job points `GO_MUTANTS_TEST_GOCACHE` at the runner's temporary
  directory and never restores it, and a cold run is dominated by compiling the
  module rather than by 34 more test processes over 125 more lines. The job's
  25-minute budget was left alone on that evidence rather than on the hope that
  it still fitted. `policy.minimum_score` stayed at 99 for the same kind of
  reason: one percent of 549 scored mutants is 5.49 and one percent of 583 is
  5.83, so the backstop buys five survivors of slack at both sizes and moving
  it would have changed a gate nobody measured a need to change.
- **A scripted `go` command, so that "what happens when the toolchain
  misbehaves" is a unit test.** `mutantkit.FakeGo` hands a test an executable
  named `go` that re-executes the test binary and answers from a rule table the
  test writes: `f.Version("1.99.0")`, `f.On("test", "-c").Stderr(diags).Exit(2)`,
  `f.On("version").Sleep(2 * time.Minute)`. It reaches the code under test the
  same way a real toolchain does — by path through `gocmd.Options.Explicit`,
  `execute.Options.Toolchain` and `OpenOptions.GoBinary`, or on `PATH` through
  `Fake.Export` for `doctor` and `internal/engine`, which locate one themselves.

  Every failure this project exists to report clearly used to be untestable
  cheaply or at all. A version probe that hangs cannot be installed; a `go list`
  that refuses a pattern needs a broken module; a baseline suite that is red
  needs a fixture every other test has to route around. Those tests either lived
  in the integration tier at a cost of minutes and a toolchain, or did not
  exist. Twelve of them now run in the unit tier on a machine with no Go on it:
  `internal/gocmd` covers a probe that exits non-zero, one that hangs, one that
  answers garbage and a listing whose failure has to carry the toolchain's own
  words; `internal/execute` covers a compile that fails with diagnostics;
  `internal/validate` covers a snapshot that does not build with *no* mutants in
  it; `internal/engine` covers an unresolvable scope pattern and a red baseline;
  `internal/cli` covers `doctor`'s toolchain row; and the root package covers
  what `Open` does when the probe fails.

  It also gives the unit tier an assertion it never had. Every call is written
  to a log, so `f.Calls()` is the argv a phase issued, the directory it issued
  it from and the environment a child process really received — which is how
  "the compile carries `-vet=off` and the listing does not" stopped being a
  claim about a `runner.Spec` and became a claim about a process, how `doctor`
  probing the toolchain exactly once and sharing the answer with its platform
  row became checkable at all, and how the rule that puts the located
  toolchain's own directory in front of a child's `PATH` — so that a `go`
  cannot hand work to a different `go` it finds ahead of itself — got its first
  test of any kind. Only `GOFLAGS`, `GOWORK`, `GOCACHE`, `GOTOOLCHAIN`,
  `GOENV`, `GOMODCACHE`, `PATH`, `GO_MUTANTS_ACTIVE` and `GO_MUTANTS_PROBE` are
  kept by value — the ones go-mutants itself composes, every one of them a flag
  list or a filesystem path; every other variable is logged by name alone,
  because a call log is written into a scratch directory CI uploads.

  A scripted compile produces the fake again rather than an inert file, on
  every platform, and that is what carries the fake past the build. `RunOne`
  starts a compiled test binary directly, so the output of a scripted
  `go test -c -o X` being the fake means the scheduler's own child answers the
  same rule table: a mutant can be scripted killed or survived by exit status,
  and the `-test.timeout` the supervisor owns and the single
  `GO_MUTANTS_ACTIVE` it sets are read back off the log. A run past the
  baseline still needs real source, because discovery type-checks the module
  with go/packages.

  The binary is installed once per *test binary*, not once per fake. It is a
  link to the test binary — six megabytes — and a unit-tier run builds
  twenty-eight fakes; on a Windows runner, where `RUNNER_TEMP` and the build
  cache sit on different volumes and `os.Link` cannot answer, that would be a
  hundred and seventy megabytes of copying per run. It also keeps the evidence
  small: a failing fake test's kept scratch holds the rule table and the call
  log, two text files, rather than a copy of the program. `mutantkit.Main`
  removes the shared directory after the suite, retrying because an exiting
  child can still hold its own image for a moment.

  On Windows nothing is hard-linked at all. A hard link is a second name for one
  file, so a link to the test binary that is running names an image the
  operating system has mapped, and Windows refuses to unlink a mapped image —
  which made both the shared install, removed while the process is still alive,
  and every `-o` output a scripted compile creates undeletable. Each arrived as
  an `Access is denied` from a cleanup rather than from any assertion. The
  choice is expressed as "has this platform a hard link that can be removed
  again", so the copy the cross-volume case already needed is the one fallback
  both take, and the removal waits five times two hundred milliseconds there
  against three times a hundred elsewhere.

  `Fake.Export`, which is the `PATH` form, forces
  `testkit.ResolveToolchainDirectories` before it changes anything. Putting the
  fake in front of `PATH` hijacks every `go` this process starts afterwards,
  and one of them is the harness's own once-per-process `go env GOENV GOPATH
  GOMODCACHE` probe. That probe is lazy, so whether it reached the machine's
  toolchain or the fake depended on which tests had run first — and when it
  reached the fake it recorded a call nobody asked for, aimed squarely at the
  exact-count assertions above, and fell back to `build.Default` for the go
  command's directories.

  Three rules make it trustworthy. A call is recorded *before* it is answered,
  so a command nobody scripted is in the log rather than only on stderr. A call
  no rule matches is refused with exit 97 and a message naming the argv, never
  answered with a silent success — a fake that guessed would let a test pass on
  a command its author never considered. And the control variables are
  `TESTKIT_FAKE_GO_RULES` and `TESTKIT_FAKE_GO_CALLS` rather than anything under
  `GO_MUTANTS_`, which `internal/execute` and `internal/engine` strip from every
  child: a switch wearing that prefix would be removed from the very
  environments the fake exists to observe, and the test binary would then run
  its own suite in place of the go command. `mutantkit.Main` is the `TestMain`
  hook that dispatches, `mutantkit.IsFakeGo` composes it with a `TestMain` that
  has something of its own to do — the root package's, which releases its
  prepared sessions — and a binary started as `go` with the switch unset is
  refused outright rather than left to recurse.

  The ledger shrank, which is the point of having one.
  `internal/gocmd/gocmd_test.go` is out of
  `internal/testkit/testdata/unit-toolchain-allowlist.txt`: its unit tier now
  scripts every misbehaviour and compiles nothing, where before it built three
  stand-in programs with a real `go build`. On a cold build cache that package's
  unit tier went from 16.2 s and 34 MB of cache entries to 0.42 s and 8 KB, and
  on a warm one from about 0.50 s to 0.55 s; the whole suite's added unit-tier
  time is about two seconds, a fifth of it the two deliberate 200 ms hangs.
  The four claims that are about a *real* `go` rather than about go-mutants'
  code — that the parser agrees with what a released toolchain prints, that an
  explicit path beats `PATH`, that the fragment `Command` returns is runnable,
  and that the probe is recorded as the run's first `exec` — moved intact to
  `internal/gocmd/toolchain_integration_test.go`. `TestEveryToolchainDrivingTestIsIntegrationTagged`
  learned one exemption to make that possible: a file that *constructs* a fake
  is supplying the toolchain rather than reaching for one, so it is not an
  offender. It is per file, which is why the split above is a second file, and
  `TestAFileThatScriptsTheToolchainIsNotDrivingOne` pins both halves — the
  scripted file exempt, the same `gocmd.Locate` call without a fake reported.
- **`PrepareOptions.Selection` narrows a prepared session by line range, so a
  consumer that used to narrow by *file* stops over-selecting.** A tool that
  runs mutation over "what changed" could only ask the library for the whole
  catalogue and then drop the mutants whose `Path` the diff did not name — which
  keeps every mutant in an edited file, two hundred of them for one edited line.
  The line-level rule was already here and was not reachable: `go-mutants run
  --changed` intersects a diff's ranges with each mutant's `[Line, EndLine]`
  span, and `Mutant.EndLine` was exported for exactly this and left for the
  caller to apply. It is now the engine's to apply, through the same function
  `--changed` calls, so the two cannot come to disagree about a condition
  spanning three lines.

  A `Selection` is module-relative paths onto 1-based inclusive `LineRange`s.
  It is applied **after** discovery and validation, so `Catalog.Digest`, every
  mutant id, `Accepted`, `Probed` and `Rejections` are identical to the same
  preparation without it — a narrowed run stays comparable with the full run
  before it, and an id out of either can be handed straight back. Narrowing
  discovery instead would be faster and wrong: identities are minted from a
  file's own bytes and the catalogue is deduplicated across the module, so
  skipping unselected files would move the identity of a mutant nobody touched.

  The narrowing is **advisory**. `Session.Exec` runs an unselected mutant like
  any other, because a selection is the plan for a run and not a rule about what
  may be measured: a consumer that finds an interesting survivor and wants the
  mutants beside it executed must not have to prepare the module again to do it.
  `Catalog.Selection` hands back the normalised copy the engine applied — paths
  cleaned, ranges sorted and merged — so two runs can be asked whether they
  selected the same lines, and a score can say which lines it covers.

  `Catalog.PreparedDigest` does **not** move, and the reasoning is worth having
  in writing because the opposite looks right. The recipe had reserved a third
  flag byte per mutant against the day a selection could narrow a session, and
  the obvious thing to do with it was to hash `Selected`. That would have been
  the most expensive kind of correct-looking: what a consumer keys on this
  digest is *per-mutant evidence* — this mutant survived against this prepared
  tree — which is a fact about the tree, the toolchain and the mutant, and about
  none of the caller's intentions. Hashing an advisory flag would mean the very
  first narrowed run of the consumer this feature was built for missing on every
  row it had ever stored, then re-measuring a module to write down answers it
  already had. So the byte stays the constant it was — dropping it would move
  every stored key too — and both `Mutant.Selected` and `Catalog.Selection` are
  named in the not-hashed ledger with this argument beside them. What a caller
  owes in exchange is one line: never store "not run, out of selection" as
  evidence, because an unselected mutant was not measured and there is nothing
  about it to record. A test pins the digest as a literal and pins it as
  unchanged by any narrowing.

  A path or a range the engine will not narrow by is refused with the new
  `ErrInvalidSelection`, before discovery starts, in a sentence naming the
  entry: an empty, absolute, backslash-separated or escaping path, a `First`
  below 1, a `Last` below its `First`. Each of those looks like a narrowing and
  would select nothing, and a run that measured no mutants and reported a
  perfect score is the failure this must not produce. A path the module simply
  does not hold is not one of them — it selects nothing and is documented to,
  because a selection built from a diff names deleted files, documents and
  testdata beside source, and filtering them is not the caller's job.
- **`ExecRequest.RecordTestLog`, `ProbeRequest.RecordTestLog` and
  `ControlRequest.RecordTestLog` record which environment variables and files a
  target consulted, and the three results carry the answers in `TestLogs`.** A
  Go test binary can be told to write that down — it is how the go command
  decides whether a cached test result is still valid — and a consumer keeping
  evidence about a (mutant, target) pair needs the same answer for the same
  reason: the inputs a target read are what say whether yesterday's verdict is
  still about today's repository. goatest reaches it today by smuggling its own
  `-test.testlogfile=<path>` through `Args` and stripping the flag out again on
  both sides of its trace, which means composing a flag the engine also owns,
  choosing a path in a directory the engine manages, and rewriting the recording
  to hide it. This is that answer as a request option.

  **The engine resolves nothing and interprets nothing.** `TestLogEntry.Name` is
  the bytes the testing package wrote: a relative path stays relative, a name
  that no longer exists on disk is reported as written, and `TestLog.Dir` is the
  directory the binary ran in so a caller can resolve one itself. `TestLogOp` is
  `getenv`, `open`, `stat` or `chdir` — the four package `os` reports — and the
  vocabulary is open, so an operation a later Go release writes is carried
  through verbatim rather than dropped. A dropped operation reads as an input
  nothing consulted, and that is the one answer a consumer must never be handed
  by accident.

  `TestLog.Complete` is the field to read before acting on `Entries`, and it is
  two claims rather than one: the log ends at a line boundary **and** the binary
  exited on its own. The testing package writes through a 4096-byte buffer that
  flushes whenever it fills, as well as from the deferred call at the end of
  `M.Run`, so a chatty target the supervisor killed leaves a log ending in a
  newline that is a fraction of what it touched — the bytes alone cannot say so,
  and a binary the engine timed out or cancelled therefore reports `false`
  whatever the last byte is. A quiet one leaves the empty file it created and
  carries `Err` instead. `Err` is a string beside the facts rather than an
  error, because a run that could not record what a target touched is not a run
  that failed. The go command's further rule — trust a log only from a test that
  exited 0 — is left to the caller, which has the exit status; and a `TestMain`
  calling `m.Run` twice flushes only the first run's entries, while a test
  calling `os.Exit` skips the flush altogether. The go command's companion
  `-test.paniconexit0` is deliberately not passed, because it would change what
  the binary does.

  One failure is a sentinel, and it is one no standard Go test binary produces:
  a binary that does not define the flag is refused by the standard flag package
  with `flag provided but not defined: -test.testlogfile` and exit **2**. Exit 2
  is a non-zero status, which is how the engine recognises a detection, so the
  branch exists to keep a status of 2 from ever being scored as a kill: it comes
  back as `ErrTestLogUnsupported` inside an `*ExecutionError` (`GOM7521`) with
  the outcome `errored`. It fires only for a binary the engine really did hand
  the flag to, so a target that exits 2 having printed that line for its own
  reasons is still a kill.

  A `-test.fuzz` target is given no flag and says so in `Err`: `internal/fuzz`
  starts every worker with the coordinator's own arguments, so a worker inherits
  the flag and truncates the file the coordinator is writing. The go command
  never combines the two either, `-test.fuzz` not being a cacheable test
  argument.

  A caller-supplied `-test.testlogfile` is refused with a `*ReservedError` while
  `RecordTestLog` is set — two of them are not two logs, since the standard flag
  package keeps the last value it sees — and passes through verbatim when it is
  not, so the method this replaces goes on working unchanged. The logs live in a
  `testlogs/` directory inside the call's own scratch, one file per binary,
  removed with it unless `OpenOptions.KeepTemp` asked otherwise; a directory of
  their own because that scratch is the target's `TMPDIR`. In a recording the
  flag is simply part of the `exec` event's `argv`, ahead of the caller's
  arguments where the go command puts it: no new event, no new field, and
  `env_names` identical to the same target run without it.
- **`go-mutants explain` answers "why did *this* mutant get that verdict, and
  how do I run it again".** Every fact it prints was already written down and
  nobody had joined it up. The run report said a mutant survived, which packages
  cover it, how many passes it took and what the tests were; the trace said which
  binaries ran, with which arguments, in which directory, for how long, and where
  their output was preserved. Answering one question about one mutant meant
  opening two documents and matching a sixty-four character identity across them
  by eye — and the last step, "run it yourself", meant reconstructing an argument
  vector from prose.

  `explain <ID_PREFIX>` prints six titled blocks in one order, so that two
  accounts of two mutants can be diffed: what the mutant is, what became of it
  (killed by which suite after how many passes, survived, timed out and hung in
  which binary, refused by the compiler with its own words), which binaries cover
  it or which line none of them reaches, every pass the run made with the
  commands underneath and the tail of each one's preserved output, the stages
  those passes happened inside, and a command to paste. The reproduce line is the
  recording's own `argv` in the recording's own `dir` with `GO_MUTANTS_ACTIVE`
  set — never composed — because a reproduction that does not reproduce is worse
  than none: somebody will paste it and believe what comes back. An integration
  test runs the printed command and asserts that the killed mutant is caught by
  it.

  The source is the latest run of this module, or `--report FILE`, or `--run
  RUN-ID`. The recording is the one filed beside the report, or the one inside a
  failed run's diagnostics bundle, or `--trace DIR` for one you were sent. A run
  that recorded nothing is not a failure: every section that would have come out
  of a recording says there is none and the account says how to get one, rather
  than guessing. `explain <path>:<line>` asks from the other end — a discovery
  pass over the workspace, then every mutant at that place with what the report
  says became of it and every site discovery declined with the reason — which is
  "there should be a mutant here, where is it" made answerable.

  Nothing is claimed that a document does not support. The line under the
  reproduction says whether the run kept its temporaries — the recording records
  an `artifact` for each one it kept — so a command whose `cd` is about to fail
  says so instead of hedging. A `--trace DIR` whose `run-start` names a different
  run is warned about above everything derived from it, because a run id is
  content-derived and two runs really can collide. The timeline prints the
  mutant's own share beside each step's total, since every mutant of a run sits
  inside the same `mutate/execute`, and names a step the recording stops in the
  middle of rather than dropping it. And the overlay manifest a library session
  compiled through is printed on a `to rebuild the binary` line rather than on
  the run line, where `GOFLAGS` would have been inert: a prebuilt test binary
  never reads it.

  `--run` resolves a prefix, listing the matches when one names two runs, the
  way the mutant target does. A position may be spelled `path`, `path:line` or
  `path:line:col` — the last is what the account itself prints — is cleaned and
  relativised against the module root, and is refused, naming the path, when the
  workspace has no such file; the discovery pass behind it is configured from
  the report's own `selection`, so "not in this run" means the run excluded the
  mutant rather than the reader's defaults did. The command it prints is quoted
  for a POSIX shell, which on Windows makes it a line to read rather than one to
  paste.

  `--json` is refused rather than implemented, and the refusal says a v2 may add
  one. The report and the recording are the machine-readable forms; a third
  encoding of the same facts would be a third document to keep in step with them.
  Nothing here measures anything, and no flag of it changes a verdict, a mutant
  id, a score, or a cache key.
- **`Workspace.Exec` may run beside a prepared session, so a consumer no longer
  opens a second workspace for `go vet`, `go build` or a baseline of its own.**
  The rule used to be one line — every command is refused once `Prepare` has
  been called — and goatest paid for it with a whole second workspace over the
  same root: a second snapshot of the module, a second toolchain probe and a
  second frozen environment, kept alive for the length of a session, to run
  commands against a tree byte-identical to one it already had.

  It is byte-identical, and that is the fact that makes this safe. Preparation
  instruments the sources *in place*, but `main_restoration` writes the pristine
  ones back and re-digests the tree before the first test binary is compiled —
  so a `Prepare` that returned a session has already proved the tree is the
  snapshot `Open` froze. The instrumented sources survive only in the overlay
  manifest the session owns, and nothing but `Session.Exec` and `Session.Probe`
  puts that manifest in a child's environment. A command run afterwards
  therefore compiles the program the user wrote, which is exactly what a vet or
  a baseline is asking for.

  The other two states are unchanged and now stated outright. A command *waits*
  while a preparation is in flight — `Workspace.Exec` takes the shared half of
  the lock `Prepare` holds exclusively, so it cannot observe the tree mid
  instrumentation — and a command after a preparation that *failed* is refused
  with the new `ErrPrepareFailed`, because a preparation that stopped part-way
  promises nothing about the tree and may have left instrumented sources in it.
  Every failed preparation is refused, including one that stopped before
  anything was instrumented: which failures left the tree alone is not a
  question a caller could answer, and the engine does not answer it either. That
  workspace is spent — a second `Prepare` still refuses with
  `ErrWorkspacePrepared` — and the answer is to open another one. A closed
  workspace is still `ErrWorkspaceClosed`, and it is asked first: `Close` leaves
  the same prepared-with-no-session shape a failed preparation does, and a
  consumer whose workspace is gone must be told that rather than sent to open
  another one for a preparation that succeeded. Closing the *session* changes
  nothing — it releases the binaries and the probe tree, not the snapshot — so
  commands go on running.

  A command run after `Prepare` may write into the tree, and doing so does not
  invalidate the session: the overlay still names the frozen sources, the
  binaries are already built, and executions go on answering. What it changes is
  the tree every later target runs in, and `Session.Changes` reports it against
  the manifest preparation captured — the same call, and the same answer, as for
  a write a `Session.Exec` or `Session.Control` target made. What it does *not*
  change is `Catalog.WorkspaceDigest` or `Catalog.PreparedDigest`: both were
  frozen by `Prepare`, so evidence keyed on them must not be carried across a
  write the consumer made, and `Changes` is the only thing that can say there
  was one. The easiest such write to make by accident is fuzzing: the session's
  calls run a fuzz target in a copy of the tree and reserve
  `-test.fuzzcachedir`, while a `go test -fuzz=…` through `Workspace.Exec` has
  neither and writes its crashers into `testdata/fuzz/` in the frozen tree. A
  consumer that writes there owns the consequences.
- **`Session.Control` runs the original program through the binaries the
  session already built, so a consumer no longer needs a second workspace to
  get a control.** A mutant's suite going red is evidence about the mutant only
  if the same suite is green without it, and a prepared session could run
  mutants and probes and nothing else. goatest reaches that answer two ways
  today and this replaces both: it opens a *second workspace* over the same root
  and runs `Workspace.Exec` there — a second snapshot, a second discovery pass
  and a second compile of every test binary, to run tests the first session had
  already compiled — and, where a probe tree exists, it reads `Session.Probe`'s
  `test-failed` outcome as "the original program is red", which spends a probe
  pass, requires `PrepareOptions.Probe`, measures the *probe* tree's binaries
  rather than the mutant tree's, and answers in a vocabulary built for infection
  facts instead of with the suite's own exit status and output.

  It was never needed. The mutant tree's binaries are the user's program plus a
  switch: instrumentation leaves every original branch in place and selects
  between them on `GO_MUTANTS_ACTIVE`, `Session.Exec` is the only thing that
  ever sets it, and the execution layer strips every `GO_MUTANTS_*` variable out
  of the frozen environment before it composes a child's — so those binaries
  with nothing activated *are* the original program. `Control` is an execution
  minus that one entry.

  The guarantee is that it is only that. The binaries, the arguments, the
  working directory, the paired `-test.timeout`, the instrumentation overlay,
  the reserved flags, the private scratch directory and the fuzz isolation are
  settled by one function inside the session for both calls, so a rule that
  reached one and not the other cannot make the control a measurement of
  something else; in a recording the two `exec` events differ in their kind,
  their subject, and one name in `env_names`. A control stops at the first
  binary that does not exit zero — that is the answer, not a saving — and a
  failing one comes back as a *result* with its output, because a red suite on
  the original program is a finding about the repository and not a broken
  engine. Errors are `Exec`'s with `Call` reading `control`.

  It is recorded as its per-binary `exec` events, of the new kind
  `control-run`, plus one `note` of the new kind `control` that
  `ControlResult.TraceSeq` points at. Both are additive: `gomutants-trace-v1`
  closes its event `type` enum, so a control gets no payload of its own, and the
  note is the one line a single call can be named by. A `note` has no
  `exec_seqs` the way `mutant-exec` and `probe-exec` do, so
  `ControlResult.ExecSeqs` carries the way down to those executions as a field
  rather than leaving a consumer to parse the note's prose.

  That is also why `trace.Recorder.Note` now **returns** the sequence it
  recorded at, as `Exec`, `MutantExec` and `ProbeExec` already do: something has
  to be able to point at the event afterwards, and for a control that event is
  the note. Calling `Note` as a statement is unaffected; a consumer that pinned
  the method as a `func(*trace.Recorder, string, string, string)` value adds the
  `int64`.
- **The run report explains its own cost, and every mutant's execution, without
  a trace.** A report said what happened to each mutant and almost nothing about
  how: `attempts: 2` was the whole of what a reader got about a mutant that took
  eleven seconds, and *why was this run slow* was unanswerable from the file at
  all. Both answers existed — in the trace, which is opt-in, kept for ten runs
  and then deleted, so the permanent record was the one document that could not
  explain itself.

  Six additions, all optional in `run-report-v1`. A run-level section is
  written when the run measured it — a run interrupted before validation has
  no `validation`, and a document says nothing rather than publishing a zero
  that reads as a measurement — while a mutant's `executions` is always there,
  `[]` for one that was cached, uncovered or never run.
  `timing` is the run's own timeline: `phases[]` and `stages[]` with the same
  names, the same durations and the same `succeeded`/`failed`/`skipped`
  vocabulary the recording uses, so a reader holding both is not reconciling two
  accounts of one run. `validation.builds` is how many compiles establishing the
  catalogue cost — one is the ordinary case, and anything more is a bisection
  and is where the minutes went. `workspace.snapshot` says whether the
  disposable copy took the tree's stable name, which is what decides whether the
  Go build cache was warm, and how many files it held. `test.toolchain` and
  `test.resolved_command` say *which* `go` ran the tests, which
  `workspace.go_version` — the module's own directive — cannot: under a
  toolchain manager the two disagree. `coverage.build_fallback` and
  `coverage.unavailable_reason` record the coverage-instrumented build that
  would not compile, in the length somebody investigating needs rather than the
  one line the console prints. And `mutants[].executions[]` is `attempts` in
  detail: one row per pass over the test binaries, with the worker that made it,
  what it observed, which binary caught the mutant, how long it took and which
  binaries it started. A confirmed timeout is now visibly two passes that both
  timed out, and an inconclusive one visibly a pass that timed out and a pass
  that did not — the shape no count of attempts could show.

  Nothing here changes a mutant id, an outcome, a score, the cache key, or how
  history matches one run to another, and `schema_version` stays 1: every key is
  optional, so a document an older build wrote still validates and still parses.
  `executions` is `[]` — present and empty — for a cached, uncovered or not-run
  mutant, none of which had a process started for it by this run; a cached
  mutant keeps the attempt count of the run that did measure it, which is the
  one place the two numbers legitimately differ, and `report.Build` refuses
  every other disagreement between them rather than publishing a document that
  contradicts itself.

  `report merge` omits all of it. A merged document is four shards from four
  machines, so there is no single timeline, no one snapshot, no one toolchain
  and no worker that ran a given mutant — and unlike the baseline timings, which
  a merge takes from the first shard and says so, these are the fields somebody
  reads precisely to explain a cost. Quoting one machine's would be worse than
  saying nothing; each shard's own document still has all of it. A matrix whose
  runners are on different go-mutants versions has to be merged by the *newer*
  binary: an older one reads the shard documents with `DisallowUnknownFields`
  and rejects the new keys outright, which is the same forward-compatibility
  policy the schemas state.

  One trace API change comes with it, and it is why the two documents can be
  believed together: `Recorder.PhaseStart`'s closer and `Recorder.Stage`'s now
  return the `time.Duration` they recorded, and the engine publishes *that*
  number in the report. They were two independent readings of one clock before
  — the recorder's pair around the work and the engine's pair around the
  recorder's — so a phase that really took 88.7 ms could be written down as 88
  in one document and 89 in the other, roughly once in a hundred runs on a busy
  machine. A nil recorder's closer returns zero and an untraced run times
  itself, so nothing about a run without a recording changes.
- **`run -v` and `-vv` answer "where did the time go, and why did this mutant
  get that outcome" without a trace file.** `-v` adds a
  `phase <name>: done (<duration>)` line to every phase, `killed by <import
  path>` and `(<n> attempts)` to the end of a result line, and under a survivor
  the suites that ran the line and did not notice — `covered by: …`, or
  `no test binary` when nothing runs it at all. It also states what a sweep
  reclaimed, and the *whole* reason a coverage-guided run had no coverage rather
  than the one line the warning folds it onto. `-vv` prints one line for every
  event the run records, indented two spaces, with durations in place of the
  timestamps: the trace as it happens, with no directory written. Recorded lines
  keep their recorded order among themselves and every one of them is indented,
  so `grep '^  '` reads the account and `grep -v '^  '` reads the run without
  it; where they interleave with the run's own lines is up to scheduling, and a
  line is as wide as the command it quotes, because an argument vector is meant
  to be pasted back into a shell.

  Verbosity is a renderer, not a second source of truth. Every one of those
  facts was already on the engine's event stream or in the recording the run
  keeps of itself whether or not anybody asked, so `-v` renders events the
  console had been dropping and `-vv` asks the engine to fan its recording onto
  the same stream (`Options.PublishTrace`, whose published branch is digested,
  so watching a run costs no copy of any captured output) — no new event, no
  second path from the engine to a console, and nothing computed by the renderer
  that the report could disagree with. The one field that was added is
  `engine.Warning.Detail`, which carries the whole reason a coverage-guided run
  had no coverage so that `-v` prints it under the warning it explains rather
  than wherever the recording happened to reach the console. A run at the
  default verbosity is byte-identical to what it printed before the flag
  existed, traced or not, which is what a golden in internal/console now pins.

  `-v` implies `--no-tui`: the lines exist to be scrolled back through, grepped
  and diffed, and a dashboard erases what it draws. It is refused with `--quiet`
  — the two directions of one dial — and with `--json`, on the same terms as
  `--explain`. Deeper than `-vv` is `-vv`.
- **A failed run leaves enough behind to be diagnosed without running it again,
  and `run --keep-temp` leaves the tree it ran in.** Two halves of one
  complaint: a run that failed in CI could only be investigated by reproducing
  it, and by the time anybody looked, the snapshot every question was about had
  been deleted.

  A run that fails now writes one directory —
  `<report.directory>/diagnostics/<run-id>/`, or its trace directory when it was
  traced — holding the rendered failure exactly as the console printed it, then
  the same error under `%+v`, then the typed chain one line per wrapped error;
  the environment's variable *names*, never their values; the `doctor` table for
  the machine it ran on; this run's own recording, taken out of the in-memory
  ring every run already keeps; the run report when there was one; and
  `preserved-paths.txt`. The stderr line `diagnostics: <dir>` is printed under
  the failure, so a CI step can grep for it and attach the directory.

  Nothing about it can change what a run reports. A bundle that cannot be
  written is a `GOM1014` warning and the same exit status; a run that was
  interrupted writes none, because nothing went wrong; `--no-diagnostics` and
  `GO_MUTANTS_DIAGNOSTICS=0` switch it off. It is the rule
  `docs/adr/0001-trace-is-not-evidence.md` states for the trace, applied to the
  diagnostic that arrives on the failure path: one that can change a verdict
  inverts the point of having it.

  `run --keep-temp` preserves the snapshot and the scratch directory instead of
  removing them — `--keep-temp=on-failure` only when the run failed, which is
  the mode a CI job can leave switched on, and never when it was interrupted.
  `GO_MUTANTS_KEEP_TEMP=1|true|always|on-failure` asks for the same without a
  flag. Each kept directory carries the owner marker that makes the next run's
  sweep leave it alone, so a keep survives the collector rather than lasting
  until somebody else runs go-mutants, and the console prints
  `kept <kind>: <path>` for each. `RunOutcome.Preserved` names them to a caller,
  and each is recorded as a `kept-snapshot` or `kept-scratch` artifact.

  Keeping is opt-in, and that is the whole reason it took a flag rather than a
  default: a kept snapshot is a full copy of the module, nothing will ever
  remove it, and unconditional keeping filled a developer's disk twice before
  the option had a name. Neither option takes any part in a mutant identity, a
  verdict, or a cache key — `cache.Context`'s field list is unchanged, and a run
  that keeps everything reuses exactly the outcomes a run that keeps nothing
  stored.

  Both roots under `report.directory` are collected by one implementation now,
  with one predicate swapped. A bundle is finished when its last file is there;
  a recording is finished when its stream ends with `run-end` *and* the bundle
  beside it — a traced run writes one into the recording's own directory — is
  finished too, so a half-written bundle holds its recording back rather than
  the answer depending on whether the run happened to be traced. The newest ten
  survive in each, half-written ones are left alone in both, and `trace clean`
  sweeps both — `trace list` still lists only recordings, because a bundle is
  not one.
- **A workspace records what it does, and every result says where.** goatest
  keeps a `goatest-trace-v1` recording of its own and had no way to line it up
  with the engine's: it could see that a mutant survived and not which binaries
  were run against it, with which arguments, under which timeout, or where the
  eight minutes went. `OpenOptions.Trace` takes a `trace.Sink`, and a
  `Workspace` records into it the toolchain probe, the sweep, both frozen trees,
  every preparation stage, every validation build and test-binary compile, every
  mutant execution and probe pass, and every directory a `KeepTemp` close left
  behind — as `gomutants-trace-v1`, the same contract `run --trace` writes.

  A nil sink does not switch recording off. The workspace records into a bounded
  ring instead, which `Workspace.Recording()` hands back after `Close`, because
  the failure nobody expected is exactly the failure nobody thought to ask for a
  recording of. A caller that supplied a sink gets `nil` from `Recording()`: a
  second, shorter copy of what the sink already holds is a document to reconcile
  and not a convenience.

  The join is exact rather than heuristic. `CommandResult.TraceSeq`,
  `MutantResult.TraceSeq` and `ProbeResult.TraceSeq` name the `exec`,
  `mutant-exec` and `probe-exec` event each result came from, so a consumer
  holding a result has the argv, the exit status, the timings and the preserved
  output without matching on anything. `MutantResult.Binaries` and
  `ProbeResult.Binaries` are what each call actually ran, by import path, which
  is the fact a consumer was previously deriving from the request it made rather
  than from the measurement it got.

  `Session.OverlayManifest()` and `Session.ProbeOverlayManifest()` are the other
  half of reproducing a run by hand — `cd <snapshot> && GOFLAGS=-overlay=<manifest>
  go test -c` rebuilds a binary, and `cd <exec.dir> && GO_MUTANTS_ACTIVE=<id>
  <exec.argv...>` runs it where the engine ran it, which is the *package's*
  directory rather than the snapshot root because a Go test resolves `testdata`
  relative to where it runs. A probe pass is the same with
  `GO_MUTANTS_PROBE=<a private log path>` and no mutant. Neither manifest could
  be derived from anything the API already returned.

  A trace is never evidence, and that is checked rather than asserted: a sink
  that refuses every event and panics on the rest changes no catalogue digest,
  no prepared digest, no kill and no probe result. `PrepareOptions.Trace` is
  unchanged and still receives every phase event; the recorder now sees the same
  timeline beside it.

  `OpenOptions.KeepTemp` gained the directory it used to miss. A session's
  per-execution and per-probe scratch is where the target's `TMPDIR` pointed and
  where anything the test wrote went, so a keep that left the snapshot and
  removed that was answering half of "what did the tree this mutant ran in look
  like". Every kept directory is now named by `Workspace.Preserved()` and
  recorded: a per-call one as `kept-exec-scratch` beside the execution it
  belonged to, and `kept-snapshot`, `kept-scratch` and `kept-probe-tree` at
  `Close`. Beside the execution rather than in a block at the end, because a
  session that keeps ten thousand executions would otherwise push its own
  `run-start`, its preparation timeline and every one of its attempts out of the
  bounded ring in the last moment of its life.

  Nothing of go-mutants' is written *into* a per-call directory. Only the three
  durable ones carry a lock and a marker, and only they need one: a sweep looks
  at the direct children of the temporary parent, so a nested per-call scratch
  is never a candidate and survives because its marked parent does — while a
  lock and a marker in the child's own `TMPDIR` would be two files in the very
  tree the keep exists to let somebody read. Keeping a probe pass's directory is
  safe because every pass already gets one of its own: what must never happen is
  two passes sharing an infection log, and two passes never share a directory.

  `trace.Recorder.MutantExec` and `trace.Recorder.ProbeExec` return the sequence
  number they recorded at, as `Exec` already did. An execution phase discards it
  — its attempts are only ever read back out of the recording — but the library
  hands one attempt straight to its caller, and that result's `TraceSeq` is the
  whole join.
- **`ReadBuildInfo`, `Version` and `ModulePath`: which engine build a consumer
  is running.** Evidence about a mutant is evidence about the engine that
  produced it, so every consumer storing any needs to know which go-mutants it
  linked — and until now every one of them wrote the same scan over
  `runtime/debug.BuildInfo`, with the same rules to get wrong: no build
  information at all, the module absent from it, the module named twice,
  `"(devel)"` read as if it were a version, a `replace` nobody noticed, and —
  since go1.24 stamps a main module built out of a checkout with a
  pseudo-version — a `+dirty` suffix on an edited tree, which looks exactly
  like a version and is served by no proxy. Most of those produce a string
  that looks like a version and is not one, so getting them wrong is not an
  error anybody sees; it is a cache that hits across two different engines.

  `ReadBuildInfo` does that scan once and reports what build information says:
  `Version`, `Sum`, `Replaced` with `ReplacePath`/`ReplaceVersion`, `Main`, and
  the `vcs.revision`/`vcs.modified` settings — read only for a main module,
  because that is the only module they describe. `Auditable` is the field a
  consumer decides on: `Version` names one immutable set of sources, meaning a
  tag or pseudo-version that is not replaced, or a main module with a clean
  revision — and never a version carrying build metadata, `+incompatible`
  aside, because `+dirty` and `vcs.modified` are stamped from the same status
  and build information that contradicts itself is answered closed. `ok` is
  false only when the program carries no build information at all; build
  information that does not name this module, or names it twice, is
  a successful reading whose answer is "nothing nameable" — the zero value,
  `Auditable` false — because build information contradicting itself cannot be
  answered with one of its two versions without picking one. `Version()` is the
  label to print, `"unknown"` when there is none, and never the value to decide
  on.

  One reading is absent rather than wrong, and it is written out on the
  function, in docs/library.md and in the contract test because a consumer will
  meet it first: a **test binary names no dependency at all**. The go command
  fills build information in before a test binary's imports are known, so from
  inside a consumer's own `go test` the engine it requires and links is simply
  not there — the zero value, `"unknown"` — and only a built program names what
  it linked. The engine's own contract test therefore builds a consumer program
  against a directory replacement and reads what it prints, because the same
  assertions made inside a test binary would hold against a stub that returned
  nothing.

  There is no `Identity()` and no embedded source digest, deliberately. The
  case that would want one is a `replace` pointing at a working tree, which is
  how every consumer develops against an unreleased engine, and it is the case
  such a digest gets wrong: anything computed here describes the sources that
  were committed, not the edited tree that actually compiled, so it would read
  as a proof exactly when it lies. The running executable's digest is the only
  content identity covering the bytes that ran, and it is the consumer's to
  take — only the consumer knows which file it launched. What the engine owes
  it is an honest `Auditable` false. docs/library.md's "Identity" section
  writes out the order to key stored evidence on: `Catalog.PreparedDigest` for
  the session, `BuildInfo.Version` when `Auditable`, the executable's SHA-256
  otherwise.

  The `go-mutants` command's own version string is unchanged: `--version`,
  report documents and cache keys still print `internal/cli.Version` — the
  goreleaser stamp, falling back to the module version without its leading "v"
  — while the library's `Version()` reports what build information says,
  verbatim, "(devel)" and all.
- **`list --explain` names every suppressed site by line and column.** The skip
  record was an aggregate — "four `const-decl` sites in this file" — and
  *which* four was a question nothing could answer. A user who wanted to look
  at the expressions discovery had passed over had to open the file and guess
  which of its forty constants the phase meant, which is the opposite of what
  `--explain` exists for: it is read by somebody asking why their catalogue is
  smaller than they expected, and "somewhere in this file" is not an answer for
  them.

  `internal/discover` now records a `SkipSite{Path, Reason, Line, Column}`
  beside every count, taken at the one place the walk is already holding the
  `token.Pos` of the edit it is declining, and `list --explain` prints one
  `path:line:col` row per site underneath each reason. A file that was never
  opened — generated, cgo, or removed by `mutation.include`/`mutation.exclude`
  — has no position to give and is printed as the bare path rather than as
  `:0:0`, which would read as a coordinate. Two rules declined at one position
  are two rows, because two edits really were declined there, and that is what
  keeps the rows summing to the count above them.

  The counts themselves are unchanged, byte for byte. Both records are written
  by one call, so grouping a pass's sites by file and reason reproduces its
  `Skips` rows exactly, and the catalogue document, the run report and their
  schemas carry the aggregate they always did. That split is deliberate rather
  than incidental: a document other tools read and diff should not grow forty
  positions per file for a phase nobody consumes per site, while a listing is
  read once, by the person asking the question. `run --explain` therefore keeps
  its per-file rows and says in one line where the finer ones live.
- **Truncation is a fact now, and every call can say how much output it will
  hold.** Three separate holes, all of them about the same bytes.

  A capture that hit its limit said so in a notice line — `[go-mutants] output
  truncated: …` — and nowhere else, so a consumer that needed to know whether
  it was holding all of a command's output had to match that sentence. A
  diagnostic written for a person had become a wire format nobody could reword,
  and a consumer had hard-coded it. `CommandResult`, `MutantResult`,
  `ProbeResult` and `*VerificationError` now carry `Truncated` and
  `TotalBytes`: the flag is the contract, `TotalBytes` is everything the child
  wrote whether kept or not, and the prefix stays exported as
  `gomutants.OutputTruncatedPrefix` for the renderers that style the notice and
  the consumers that were matching it. On `MutantResult` the notice was usually
  lost outright, because `OutputTail` is a *tail* and the notice sits at the
  top of a capped capture.

  Mutant and probe runs silently took internal/runner's one-mebibyte default
  with no way to raise or lower it, which is both far more than a console wants
  and far less than a consumer archiving the evidence of a kill might.
  `ExecRequest.OutputLimit` and `ProbeRequest.OutputLimit` are
  `Command.OutputLimit` for the two session calls, with the same defaults: the
  1 MiB when they are not positive, and a 256-byte floor so the notice still
  fits inside the budget.

  And the three result types carried output three different ways: a `[]byte` on
  `CommandResult` and `ProbeResult`, a fifty-line `OutputTail` string on
  `MutantResult`, and nothing at all beside it. `MutantResult.Output` is now the
  bounded combined output of the *deciding* binary, with `OutputTail` unchanged
  beside it as the summary a console prints — so a consumer that renders the
  tail needs no change, and one that wants the evidence no longer has to choose
  between fifty lines and running the mutant again. It is empty for a survivor,
  exactly as `OutputTail` has always been: a survivor's output is thousands of
  lines of nothing having gone wrong, multiplied by every mutant in a run, and
  holding it is how a mutation run runs a machine out of memory.

  The `probeable/` fixture gained a `TestPrintsALot` target for this, which is a
  corpus change and is recorded in `fixtures/README.md`. The probe session that
  fixture backs is prepared once and shared across the API suite, so a test
  needing a chatty target cannot write one into the tree. It reaches no probed
  site, so no claim the fixture already carried has moved.
- **`Catalog.PreparedDigest`, `Mutant.EndLine`, and a probe log the engine
  checks against its own catalogue.** All three close gaps a consumer was
  filling in by hand, and getting subtly wrong.

  `Catalog.Digest` covers the *set of mutants* and deliberately nothing else —
  not the module path, the toolchain, the profile, the test packages, the
  workspace digest, `Mutant.Package`, `Mutant.Accepted`, `Mutant.Probed` or the
  rejections. A consumer storing results per prepared session needs all of it,
  so goatest hashed its own: one fingerprint over the mutants plus `Package` and
  `Accepted`, a second over the probe status, because no single value it could
  compute covered both. Those are two re-derived recipes nobody versions, which
  go on hashing yesterday's fields the day the engine reports something new
  about a prepared mutant — and go on hitting, with evidence gathered against a
  session that is not this one. `PreparedDigest` is the engine's own answer: a
  SHA-256 over the domain separator `go-mutants-prepared-catalog-v1`, `Digest`,
  `WorkspaceDigest`, `ModulePath`, `GoVersion`, `Toolchain`, `Profile`, the test
  packages, and per mutant its ID, package and three flag bytes, plus the
  rejection IDs — length-prefixed with the mutant identity's own encoding, and
  written out in full on the field and in `docs/library.md` because a value a
  consumer keys a store on is a wire format. Line numbers, columns, display
  identities, diagnostics and branch proofs stay outside it: each is a function
  of what is hashed, and a key that moved when a line shifted above an untouched
  mutant would be a cache that never hits.

  `Mutant.EndLine` is the 1-based line the edit ends on — `Line` plus the
  newlines in `Original`, the rule `go-mutants run --changed` already applies to
  a diff. Without it a caller selecting by line range through the library had
  only `Line`, and comparing that alone silently drops every multi-line edit
  whose last line the range touches and whose first line it does not — which is
  exactly the mutant a `--changed` run would have executed.

  `Session.Probe` now proves the set it returns. `ProbeResult.Infected` has
  always promised to be ascending, in range, and to name only probed mutants;
  an index that failed one of those arrived as a fact, and a fact there licenses
  a consumer *not* to execute a test. Such an index can only come from
  go-mutants contradicting itself, so it is now `ErrProbeInconsistent` naming
  the index rather than a repaired set handed over as a measurement — an engine
  bug surfaces as an error, never as "no facts" and never as a fact. The shape
  is proved over the *raw* log, before the indices of mutants the mutant tree
  rejected are dropped: that filter has to tolerate an index outside the
  catalogue in order not to panic on one, so checking after it would let the
  runtime write about a catalogue that is not this one and have it read as an
  ordinary rejection. A consumer can delete its own catalogue validation and its
  unknown-index defences.
- **`run --trace` and `GO_MUTANTS_TRACE`, with a directory go-mutants owns and
  collects — and every run recording whether or not you ask.** The engine has
  been able to account for itself since the change below; nothing asked it to.
  Now `run --trace` does, `--trace=DIR` names somewhere else, and
  `GO_MUTANTS_TRACE=1|true|DIR` asks for the same thing from the invocation you
  cannot add a flag to. The variable becomes the flag inside `cli.Execute` and
  nowhere else, so there is one description of what the option means and one
  place its precedence is decided; an explicit flag always wins, and nothing is
  ever inserted after `--`, where the argument vector belongs to your test
  command.

  The part that is not opt-in is the point. A run that passes no flag still
  records, into a 4096-event ring in memory, because the failure nobody expected
  is exactly the failure nobody thought to pass `--trace` for — and an account
  that exists only when somebody predicted they would need it is an account of
  the runs that went fine. The ring costs a bounded amount once, writes no file,
  and is what the diagnostics bundle of a failed run will be assembled from.

  A recording lands in `<report.directory>/trace/<run-id>/`, named by the same id
  the report carries so that the two can be paired afterwards. That directory is
  not a matter of taste: `snapshot.Create` excludes `report.directory` and
  nothing else in the workspace, and a recording grows while the run digests the
  tree — so a stream written anywhere else inside the workspace would make the
  tree change under the run, and the run would report drift it caused itself. A
  `--trace=DIR` inside the workspace and outside that directory is therefore
  refused, symbolic links resolved on the longest existing prefix so that a name
  is judged by where it lands rather than by how it was spelled. The refusal
  costs a `trace-unavailable` note in the recording and a `warning GOM1013` on
  standard error; the run goes on, into the ring, and exits on its own verdict.
  A diagnostic that can fail the run it is a diagnostic of inverts the point of
  having one.

  Every byte a run writes has an owner and a collector, and this one has both. A
  traced run prunes its trace root to the newest ten recordings *as it opens its
  own*, and records what it removed as a `trace-gc` note. Collecting before the
  run's own directory exists is what keeps the rule free of an exception
  protecting the recording being written. `go-mutants trace clean [--keep N]` is
  the same collector run by hand, and it takes the trace directory itself away
  with the last recording in it, so a cleaned workspace looks like one that was
  never traced.

  Two things are never collected. Only a directory named by a run id and holding
  a `trace.jsonl` is a recording at all, so a file or a directory somebody else
  keeps beside them survives. And a recording whose stream does not end with its
  `run-end` is left alone — a run in progress, or one that died — because the
  account of the crash is the one you most want, and a collector that took it
  while keeping ten accounts of runs that went fine would be collecting exactly
  backwards. It is also what makes a live run safe from a concurrent
  `trace clean` rather than only from being the newest name in the root;
  `trace clean --all` is how somebody who has read them says so. Nothing is
  added under `TMPDIR`: a recording is something you attach to a bug report, and
  a run's temporary parent is swept by the next run of the same root.

  The two notes the command line contributes — the refusal and the collection —
  are handed to the run through the new `engine.Options.Notes` rather than
  written into the stream by the CLI, and the recorder emits them immediately
  after the `run-start`, which is the moment they are about. Sequence numbers,
  timestamps and the position of the last line belong to the recorder: a caller
  splicing an event into a stream it does not number is a caller that can break
  every one of them.

  `go-mutants trace list|summary|diff|validate|clean` reads what was written.
  `summary` says where a run went — phases and stages with their durations, the
  subprocesses tallied by kind, the mutant outcomes — and both it and `list` say
  first whether the recording is complete, lossy, or interrupted, which is the
  thing to know before reading a count out of one. `validate` checks every line
  against the published schema rather than the first, because a recording is a
  stream and a file that breaks halfway through is worse than one that never
  parsed at all. `--trace` is refused on every command that measures nothing.

  Nothing about an untraced run's output changed, byte for byte. A traced one
  gains one line, `trace: <dir>`, in the block that already names where the
  report went — it rides on `engine.ReportPublished` as `TracePath`, so it is
  laid out like the other paths, kept by `--quiet` like the other paths, and
  replayed into the scrollback after a dashboard run like the other paths.
- **A failed test keeps what it had, and says what it ran.** Every tree a test
  worked in — the fixture copy, the snapshot, the instrumented source, the
  composed environment's scratch — lived under a `t.TempDir` that the testing
  package removed the instant the assertion had been printed. Diagnosing a
  failure therefore meant reproducing it, which is expensive on a developer's
  machine and simply unavailable on a CI runner nobody can log into: the log was
  the whole of the evidence, and the log did not contain the source, the
  commands or the tree.

  `testkit.Scratch` is now what every constructor here hands a test instead of
  `t.TempDir`, and what becomes of that directory is a policy.
  `GO_MUTANTS_TEST_KEEP` unset is the old behaviour exactly — keeping
  unconditionally filled a disk twice, so the default is off — while `1`
  (equivalently `true`, `failed`, `on-failure`) keeps the directories of a test
  that failed and `always` keeps every test's. An unrecognised spelling is
  refused with the accepted ones named rather than read as "off": the variable
  is set once for a whole CI job in a file nobody reads again, and the only
  symptom of a typo would be a failed job with nothing attached to it.

  A kept directory is `<root>/<package>/<test name>-<six hex digits>` — the name
  folded, cut to 48 bytes and made unique, because a Go subtest name is a path
  and a directory named after one is a tree — and it holds `KEPT.txt`: the test,
  the fixture, the toolchain, the build cache in use, every child command the
  test ran through `testkit.Exec` quoted the way a shell would take them back,
  and — under `Also kept:` — the test's *other* kept directories. A real
  integration test takes a scratch for its environment, one for each snapshot and
  one for a fixture copy; they are siblings named after the same test with
  different suffixes, and a reader who opened one had no way to know the others
  existed.

  `testkit.DumpFiles` prints the files a glob matches when the test failed,
  capped at 64 KiB each and 1 MiB in total so that a dump cannot push the
  assertion off the top of a CI log, and copies them whole into
  `dump/<n>-<tree>/` beside the account — one directory per call, because a test
  that instruments the same fixture twice has two trees holding the same relative
  paths and one `dump/` gave the reader a single tree made of halves of each.
  Every printed header names the file by its full path for the same reason.
  `mutantkit.Snapshot` registers `**/*.go` over the snapshot, which is what makes
  an instrumentation or validation failure print the instrumented source instead
  of only its verdict, and it no longer removes the snapshot that is being kept.

  `mutantkit.Trace` and `mutantkit.TraceSink` attach one bounded recording per
  test — the first for the options that take a `trace.Recorder`, the second for
  `engine.Options.TraceSink`, which opens a recorder of its own. The engine and
  validate suites attach one by default, so a failing run there logs the tail of
  what it actually executed and files the whole ring in the kept directory as
  `trace.jsonl`. The harness writes the run-end event when the recording is its
  own: without it every kept recording read as `incomplete`, which is what an
  interrupted run looks like, and the drop tally was never written at all.

  The root package's shared prepared sessions go through
  `testkit.PackageScratch`, which is the same policy for a directory a `TestMain`
  owns and no `testing.TB` can be asked about: `release(m.Run() != 0)` keeps it
  when the package failed and prints where it is. Under the policy those
  workspaces are opened with `KeepTemp`, because `Close` is what removes the
  engine's own snapshot, probe tree and per-execution scratch — so a kept parent
  used to hold the fixture copy and nothing else, which is the one part of a
  failed session a reader can already get from `fixtures/`.

  Every CI and nightly test job sets the policy and uploads the root as an
  artifact on a failure (`if-no-files-found: ignore`, since a job can go red
  before any test does). The dogfood job deliberately does not: a mutation run
  makes this repository's own tests fail thousands of times on purpose, and
  keeping there would be a disk full of evidence of the run working.
  `mise run test-cache-status` already reported the kept root and
  `mise run test-clean` already emptied it; the harness now stamps it with the
  `.go-mutants-kept` marker that licenses that removal, and
  `TestKeptRootAgreesWithTestkit` pins the harness's copy of the rule against the
  collector's the way `TestPathAgreesWithTestkit` does for the build cache.

  `GO_MUTANTS_TEST_FORCE_FAIL=<test name>` fails one named test on purpose,
  which is the documented way to see any of this on a test that works —
  `TestValidateFailureShowsTheInstrumentedSource` uses it on a real validation
  test and asserts that the child prints the generated runtime no pristine
  fixture contains. It fires from the one line every constructor in the harness
  already logs, so a test that takes no scratch directory of its own can still be
  named; a test that reaches the harness not at all cannot, and the release of a
  package scratch says so rather than leaving somebody watching a green run.
  `GO_MUTANTS_TEST_VERBOSE=1` prints a dump on a test that passed. The four
  variables are tabulated in `internal/testkit/doc.go`, together with what
  reclaims a kept root: nothing but `mise run test-clean` — re-running a test
  files a new directory beside the old one, `always` grows for as long as it is
  left on, and `testcache trim` never looks at this root.
- **Test tiers, and the four things nobody was measuring: race, coverage,
  cost and benchmarks.** `mise run test` was paying for forty toolchain-driving
  tests on three operating systems every push — the root package alone took 28
  seconds of `go build`, `go test -c` and mutant processes for tests that
  cannot run on a machine without Go — while `-race` had never been run against
  this repository at all, coverage had never been measured, and a test that
  quietly stopped running left no trace anywhere.

  The suite is now two tiers. `go test ./...` runs the whole unit tier — every
  test that needs a compiler and nothing else, which is most of them — and
  `go test -tags integration ./...` adds the suites that drive a real toolchain:
  a `go build`, a `go test -c`, a mutation run. Two tests keep the line where
  it is: `TestRootPackageUnitTierNeedsNoToolchain` asks the unit-tier binary which
  tests it contains, because a lost `//go:build` line is invisible in every
  other way, and `TestEveryToolchainDrivingTestIsIntegrationTagged` scans every
  `_test.go` in the tree for a `go` command started outside the tag — parsing
  the build constraint with `go/build/constraint` and asking whether the file is
  excluded whenever `integration` is off, because a term match would have
  accepted `integration || !windows`, which every unit-tier run outside Windows
  builds. The second
  keeps a ledger,
  `internal/testkit/testdata/unit-toolchain-allowlist.txt`, of the nine files
  that drive the toolchain in the unit tier on purpose — internal/gocmd, whose
  subject *is* the `go` command; internal/discover, which loads modules with
  go/packages; internal/instrument, which has to compile what it generates to
  know it is a program; and the harness's own tests, which are the toolchain
  policy's tests — and reports a stale entry as loudly as an offender, so the
  list shrinks on its own and grows only deliberately. The three harness needles
  are matched unqualified (`GoBinary(`, not `testkit.GoBinary(`) precisely so
  that internal/testkit is subject to the rule it defines rather than exempt
  from it by construction.

  One silent skip went with it. `internal/discover`'s toolchain helper used a
  bare `t.Skipf` when `gocmd.Locate` failed, so a runner that lost its Go would
  have retired seventy-four tests and reported a green build; it now goes
  through `testkit.GoBinary`, which skips on a developer's machine and fails
  under `GO_MUTANTS_TEST_REQUIRE_TOOLS=1`. `session_integration_test.go`'s
  `exec.LookPath("go")` was the same shape and moved the same way.

  New tasks, each with its reasoning beside it in `mise.toml`: `test-race` and
  `test-integration-race` (ubuntu-only, because `-race` links the C runtime and
  a data race is a property of the Go code rather than of the platform),
  `cover` and `cover-integration` (a signal, never a gate — the gate on whether
  the tests catch anything is `mise run dogfood`, which is far stricter), and
  `bench`, which writes `bench.txt` for `benchstat` to read against yesterday's.

  New in CI: a `race` job on every push, a `coverage` job on `main` and on
  demand — ninety minutes that cannot fail anything does not belong in front of
  every pull request — uploading `cover.out` and `cover.html` for fourteen days,
  and nightly `race-integration` and `bench` jobs. Every job's `timeout-minutes`
  is five to ten past its task's own `-timeout`, so Go's alarm fires first and
  reports the package that hung with a stack, rather than the runner cancelling
  the job and saying nothing.

  **`race` is not yet a required check.** Adding it to the branch ruleset is an
  owner action and is not part of this change; until somebody does it, a data
  race fails a job that a merge can ignore.
- **`internal/devtools/testcost`: what a test run cost, and what did not run.**
  Both numbers were invisible. `ok <pkg> <elapsed>` is printed once per
  package, interleaved with forty others, so a package that grew from two
  seconds to ninety was noticed months later by somebody complaining about CI;
  and a skip prints nothing at all without `-v`, so a test that stopped running
  — a missing tool, an unset variable, a filesystem that refused a symlink —
  read exactly like a test that passed.

  It reads `go test -json` and prints one Markdown table, `package | tests |
  skipped | elapsed`, slowest first, naming every skipped test underneath it. A
  package holding no test files is left out rather than printed as a row of
  zeroes, and an unreadable line in the stream is named without costing the
  table the forty packages that parsed — the run somebody is reading this for is
  usually one that already went wrong.
  In CI it appends to `$GITHUB_STEP_SUMMARY`, so the table is on the run's own
  page. It also carries the run's verdict, which is not a nicety, and it *runs*
  `go test` rather than being piped its output: a pipeline reports its last
  command's status in every shell there is, `set -o pipefail` is not portable to
  the `cmd /c` these tasks get on Windows, and the only failures a piped reader
  can see are the ones cmd/go wrote into the stream — so a `go` command that
  fell over before it started testing would have been reported as a green run
  that never happened. With `testcost -- go test -json …` the command's status
  and the stream's verdict are both honoured, and a command given without `--`
  is still a usage error rather than something the tool will execute.

  A failure also carries what was said about it. Under `-json` cmd/go runs the
  binary with `-test.v`, so every line a failing test wrote is in the stream —
  the assertion with its file and line, the panic, the goroutine dump — and a
  reader that consumed those records to print a count turned a red CI run into
  "something failed", diagnosable only by re-running it on a machine nobody has.
  Each failure's output is now replayed verbatim under a `--- FAIL: <pkg>.<test>`
  header, bounded at 256 KiB apiece; a package-level failure prints the output no
  test owned, which is where a panic or a timeout lands, and a build failure
  prints the compiler's diagnostics. A passing test's output is discarded, since
  replaying all of it would make a green log a `go test -v` transcript — that is
  what `--verbose` is for. `--no-skips`
  exits non-zero naming the skips; it is deliberately not passed in CI, because
  every remaining `t.Skip` here is a platform-capability guard or a fuzz body
  rejecting an input, at least one fires on each of the three operating
  systems, and the skips it was wanted for — a missing tool — are already fatal
  through `GO_MUTANTS_TEST_REQUIRE_TOOLS=1`.
- **Typed errors on the engine API, with every message unchanged.** A consumer
  driving `Workspace` and `Session` had to tell three things apart and could
  only do it by matching text: the user's test suite failing on the instrumented
  tree, the repository moving under the run, and go-mutants itself breaking.
  Those three want three different reactions — quote the suite's output to its
  author, say which file changed and start again, file a bug with a diagnostic
  code — and the only signal separating them was a sentence, which is the one
  part of an API nobody promises twice. A consumer that parsed one was one
  reworded message away from reporting an engine bug as somebody's failing test.

  The root package now returns sentinels and typed errors for every refusal a
  caller can act on: `ErrWorkspaceClosed`, `ErrWorkspacePrepared`,
  `ErrSessionClosed`, `ErrInvalidMutantID`, `ErrMutantNotFound`,
  `ErrAmbiguousMutant` and `ErrMutantRejected`, and the types
  `*MutantSelectionError`, `*DriftError`, `*VerificationError`, `*BuildError`,
  `*ExecutionError`, `*PackageNotPreparedError` and `*ReservedError`.
  `DiagnosticCode(err)` returns the stable `GOM####` code carried anywhere in
  the chain, because those codes live in packages a consumer cannot import and
  lifting four characters out of a message is the coupling this change removes.

  Nothing prints differently. Every sentinel is wrapped into the sentence the
  engine already produced, and every type renders the message it replaced — the
  drift errors down to the snapshot layer's own kind words, which spell a
  modified file *changed* where `ChangeKind` spells it `modified`. What is new
  is what the values carry: `*DriftError` names the drifting paths with the
  digests on both sides instead of a list of lines; `*VerificationError` carries
  the command, the status and the output; `*BuildError` carries the phase, the
  package, the argv and the code, so a bug report can be reproduced; and
  `*MutantSelectionError` carries the sorted display identities an ambiguous
  prefix named. `internal/drift` grew `UnexpectedDrifts` for the structured
  answer and kept `Unexpected` for the CLI, and `execute.Error` and
  `validate.Error` grew the fields the public types report — `Package`,
  `ExitCode`, `TimedOut` — where the failure knew them.

  `*ExecutionError.Package` is the failing binary's import path rather than the
  request's. `ExecRequest.Package` and `ProbeRequest.Package` are selectors: one
  may be a module-relative directory, and both are empty for the ordinary
  request that measures every prepared binary — so a consumer grouping
  infrastructure failures by package would have been grouping most of them under
  the empty string. Every execution failure that names a binary now carries that
  binary's import path on `execute.Error.Package`: the start failure, the stale
  catalogue, the probe start failure, and the two cancellations, which name the
  binary that was cut off and nothing at all when the pass stopped between
  binaries with none running. The request's selector is the fallback, for the
  failures that are about a pass rather than about one binary.
- **The engine accounts for what it did, into a trace nobody passes yet.**
  `engine.Options` gains `RunID`, `TraceSink` and `PublishTrace`, and a run now
  records itself: each of the four phases and every stage inside them, every
  subprocess it starts under a label (`go-version`, `scope-list`,
  `baseline-build`, `baseline-test`, `instrumented-baseline`,
  `covdata-textfmt`, and everything internal/execute and internal/validate
  already labelled), the snapshot it froze, the leftovers it swept, one coverage
  decision per mapped mutant, every cache lookup and write-back, each file it
  wrote, and every warning it published.

  All of it goes into the `gomutants-trace-v1` stream, which is where a question
  like "why did that run take eleven minutes" or "which binary killed this
  mutant" becomes answerable. Until now the only account of a run was its report,
  and a report is a statement about the *code*: it says which mutants there are
  and what became of each, and deliberately says nothing about the work. Between
  the two there was nowhere to put "validation spent nine builds", "the coverage
  profiles named no file inside the module", "the sweep reclaimed four
  gigabytes" — facts about the run rather than about the program, every one of
  which was either dropped on the floor or reduced to a single console line.

  Nothing passes a sink yet: `--trace` is the next change, and the report gains
  none of this until the one after that. What lands here is the recording itself
  and the invariant it lives or dies by, which is that a trace is a diagnostic
  and never evidence. No trace option enters the cache key, the workspace digest,
  the catalogue or a mutant id, and a sink that refuses every event costs the
  events and nothing else: the same mutants, the same verdicts, the same exit
  status, the same document byte for byte. `cache.Context`'s field set is now
  pinned by a test of its own, so that a future option cannot drift into the key
  unnoticed and silently empty every user's cache.

  Three consequences of that invariant are worth naming, because each is a way a
  diagnostic could have cost a run. A sink that *panics* is now counted exactly
  as one that returned an error: a `Sink` is an interface, an embedder's
  implementation of it is ordinary Go code, and the panic would otherwise unwind
  through the recorder on whichever goroutine was recording — during execution,
  one of the workers — and take the process with it. Publishing the trace onto
  the event stream goes through a bounded buffer and one forwarding goroutine, so
  the recorder's lock is never held across a send onto a channel a terminal is
  draining; without that, asking to watch a run would have made every worker
  queue behind the screen. And the disabled recorder allocates nothing: taking a
  record's address inside a method the compiler inlines was moving the caller's
  copy to the heap, at eighty to a hundred and twelve bytes per mutant, per cache
  lookup and per mapped mutant, on runs that record nothing at all.

  `engine.Options.RunID` is checked against the form `NewRunID` mints —
  `RunIDPattern`, now exported because `internal/cli` will read it back off a
  directory listing — and a value that is not a run id is refused with the new
  `GOM4005` before the workspace is copied. It is refused rather than replaced:
  the id names files, and a caller that minted one has something of its own filed
  under it, so running under a quietly different id would leave the two unable to
  find each other.

  Three things a renderer could not previously say are on the event stream as
  well. `engine.PhaseCompleted` answers every `PhaseChanged` with the phase's
  duration — including the last phase of a run, which has no next one to be
  followed by and is now closed on every path out of it, the failure and the
  interruption included. `engine.Traced` carries the recording itself, published
  only when `PublishTrace` asks for it, which is how `-vv` will draw a run
  without opening a second recording of it. And `engine.MutantResult` gains
  `KilledBy`, `Attempts` and `CoveringTestPackages`: all three were in the
  document and in none of the events, so a console could not say which suite
  caught a mutant, how many attempts it took, or which suites ran a survivor's
  line and did not notice. `RunOutcome` gains `Timing`, `Validation`, `Snapshot`
  and `CoverageFallback` for the same reason — the run measured them and had
  nowhere to put them.
- **`engine.Options.TempDirectory`: a run can name the parent of its own
  temporary directories.** The engine put its snapshot, and the scratch
  directory beside it, under `os.TempDir()`, and swept that same directory for
  the leftovers of runs that had been killed. Neither was a decision the caller
  could take any part in, so the only way the engine's own tests could keep
  their snapshots private was to point `TMPDIR`, `TMP` and `TEMP` somewhere of
  their own — a process-wide global, which is why every one of those tests had
  to hold it alone and none of them could run in parallel.

  It mirrors `OpenOptions.TempDirectory` in the root package: the snapshot
  lands there, the scratch directory is created beside the snapshot and so
  lands there too, and the sweep only ever collects under it. Empty is
  `os.TempDir()`, which is what `go-mutants run` passes and therefore what
  every real run still does, and a relative path is resolved against the
  working directory rather than refused, because internal/snapshot and the
  sweep both read it against that same directory.

  The sweep is the half that is more than a convenience. A collector that
  deletes directories is one a caller has to be able to point at a parent it
  owns, rather than one turned loose on a directory shared with the whole
  machine: sweep only a named parent. Nothing changes for the run's children —
  their `TMPDIR`, `TMP` and `TEMP` still point at a per-worker directory under
  the run's scratch, wherever that scratch now sits.
- **The engine API's contract is written down and pinned:
  `KnownPreparePhases`, `docs/library.md`, and a test per invariant.** Every
  claim a consumer was already relying on lived in somebody's head or in the
  shape of the code: that a mutant ID is 64 hex characters, that `Mutants[i]`
  carries `Index == i` so a probe log's indices need no translation, that a
  `DisplayID` is a prefix of its ID and unique, that a mutant is accepted
  exactly when it carries no rejection, that `KilledBy` is filled in for a kill
  and a timeout and for nothing else, that a measured `Infected` set is
  ascending, distinct, in range and probed. None of it was checked, so any of it
  could have stopped being true in a refactor that looked local, and the
  consumer would have found out in production. `api_contract_test.go` now checks
  those claims against the sessions the suite already prepares, so the cost is
  assertions rather than minutes. The catalogue invariants run over a third
  shared session, prepared over `fixtures/rejectable` without a probe tree or a
  verification, because probeable's three mutants all compile: every clause
  about a rejection would otherwise pass over an empty list, and a vacuous
  assertion is a claim nobody is keeping. `external_contract_test.go` compiles a
  synthetic consumer module against `Swept`, `Preserved`, `ToolchainVersion`,
  `PrepareOptions.Trace`'s exact type, `SweepResult`'s named fields,
  `BranchProof`'s named fields, and every constant of all six vocabularies.

  `KnownPreparePhases()` exists because `PreparePhase` is an **open**
  vocabulary and nothing said so. A consumer with a closed schema of its own —
  a JSON enumeration, a fixed set of timers — had no way to pin the list except
  by reading the source, so the day go-mutants split a phase in two would have
  been the day that consumer's users saw a validation failure. The list is now
  a function of this build: a consumer pins it in a test, gets told by its own
  suite when it changes, and is told in the doc to accept an unknown phase
  verbatim or map it to a catch-all in the meantime.

  `docs/library.md` is the reference the package comment could not be: the
  lifecycle and exactly which lock each call holds for how long, every option
  field with its default, the invariants above, the guarantees (a private
  `TMP`/`TEMP`/`TMPDIR` per call, the reserved `GO_MUTANTS_*` and temporary
  variables, the paired timeouts — supervisor at `Timeout`, the binary's own
  `-test.timeout` at twice it so the two never race — the snake_case `Outcome`
  vocabulary against `run-report-v1`'s kebab-case, and temporary-directory
  ownership, sweep and keep), what `Catalog.Digest` does and does not cover, and
  a `go list -json` passthrough recipe that says why `OutputLimit` has to be
  sized for a document: truncation keeps the tail, and the tail of a JSON stream
  does not parse.
- **One shared, hermetic test harness in `internal/testkit`, and a test that
  keeps production code out of it.** Every helper it holds existed three or four
  times before, and the copies disagreed — which is how the suites came to
  depend on the machine running them. Three packages redirected the user cache
  directory and all three were wrong on macOS, where `os.UserCacheDir` ignores
  XDG, so those tests read the developer's real `~/Library/Caches/go-mutants`
  back and interfered with each other through it. Two suites pointed the
  temporary directory somewhere private and a third did not, so "the snapshot
  was removed" was an assertion in two places and a guess in the third. One
  suite composed the environment its children got and another inherited it, so a
  developer with `GO_MUTANTS_ACTIVE` exported in their shell ran a mutant as the
  baseline in half the repository. None of that is a fact about go-mutants: it
  is a fact about the operating system, the go command and git, and a copy of
  such a rule per package is a rule that drifts.

  So there is one copy: the module and corpus paths, tree copies aged past
  cmd/go's two-second index cutoff, a synthesized-module builder, the hermetic
  environment as both a process redirection and a value a parallel test can hand
  a child, a stated skip-or-fail policy for a missing `go` or `git`
  (`GO_MUTANTS_TEST_REQUIRE_TOOLS`, set for every CI job so a runner without a
  toolchain fails instead of quietly running a smaller suite), deterministic git
  repositories, and child-process assertions that quote the child's output on
  every mismatch — because in CI the log is all there is. The suites' child `go`
  commands are pointed at one dedicated build cache (`GO_MUTANTS_TEST_GOCACHE`)
  rather than the developer's, which had reached 14 GB of entries keyed on
  absolute paths that existed for a single run. That cache defaults to
  `<os.UserCacheDir()>/go-mutants-test/go-build`, and `mise run test-clean`
  empties it.

  Nothing that ships may import the harness — that would link `testing`, and its
  flag registrations, into `go-mutants` — and the harness may not import
  anything from this module, so the tests of the pure packages can use it
  without pulling the engine in behind them. Both halves are enforced:
  `TestProductionCodeDoesNotImportTestkit` parses every non-test file in the
  tree, and `TestTheHarnessImportsNothingFromThisModule` parses the harness's
  own.
- **One `-update` flag, one helper-process shape and one clock, in
  `internal/testkit`, with `internal/testkit/mutantkit` beside it.** Two packages
  registered a `-update` flag of their own — `internal/report` and
  `internal/instrument` — and the public `trace` package registered a third. They
  are `flag.Bool` calls on `flag.CommandLine`, so the moment any test binary
  linked two of those packages, package flag would have panicked "flag redefined:
  update" before a single test ran; the only reason it had not happened yet is
  that no binary had linked two of them. There is now exactly one, registered in
  the harness, and `mise run golden-update` names the packages that hold goldens
  explicitly — because one flag in every test binary means `go test -update
  ./...` would regenerate every golden in the tree in one command, and a golden
  regenerated without being read is a golden that records a bug.
  `TestGoldenPackagesAreNamedByTheUpdateTask` scans for `testdata/*.golden*` and
  fails when that list goes stale. A mismatch is now a `cmp.Diff` naming the
  line that moved, rather than the two whole documents four suites each
  printed, and a missing golden fails closed instead of recording silently — a
  first recording turns a new test, and every run after a golden is lost in a
  merge, into a green one that pins nothing.

  `testkit.Helper` is the TestMain shape a package whose tests need real
  processes uses. It carries the rule that took a day to find the first time: a
  helper is the test binary re-executed, so under `go test -cover` its exit hook
  writes `covmeta.<hash>` into the single `GOCOVERDIR` that `go test` exports,
  under a name derived from the binary and so identical for every helper —
  and on Windows the losing rename prints "coverage meta-data emit failed:
  Access is denied" onto the very stderr internal/runner asserts the exact bytes
  of. `testkit.Clock` replaces three time sources that had each been written
  separately, two of them as slices of instants popped one per read: a slice
  makes a test depend on how many times the code under test happens to read the
  clock, so an extra read is an index-out-of-range panic on a goroutine nobody
  owns and the assertion the test is making is nowhere in its source.

  `internal/testkit/mutantkit` is the half of the harness that may hold
  go-mutants' own types, kept separate so that the harness itself goes on
  importing nothing from this module: snapshots that are aged and whose removal
  is registered before anything else can fail, the discover/catalogue/hint/
  instrument sequence four suites each wrote out, mutant lookups that assert
  there is exactly one match and that it was accepted, and run reports marshalled
  through the published schema and normalised so that two runs can be compared —
  fixing the tool version, the go version, the host's GOOS and GOARCH, every
  measured duration and every absolute path, and deliberately leaving the
  digests and mutant ids, which are content-addressed and are what proves two
  runs measured the same program.
- **The toolchain-driving suites of `internal/validate`, `internal/instrument`,
  `internal/execute` and `internal/coverage` run on the shared harness.** Four
  packages each carried their own `snapshotFixture`, `locateToolchain`,
  `fixtureEnv`, `goInSnapshot`, `runSuite`, `requireExit`, `requireOutput` and
  `mutantAt`, and the copies disagreed about the things that decide what a test
  means. Two of them made a missing `go` a skip and two made it a fatal, so the
  same machine ran a different suite depending on which package it was in.
  Three composed the environment their children got and one inherited the
  developer's whole shell, so a `GO_MUTANTS_ACTIVE` exported in it ran a mutant
  as the baseline in the fourth. `internal/coverage` looked its toolchain up
  with `exec.LookPath("go")` and then located a second one, which is two chances
  to find two different compilers. And every one of them pointed its children at
  the developer's own build cache with fixture paths that exist for a single
  run. They now call `mutantkit.Toolchain`, `Snapshot`, `Discover`, `Catalog`,
  `Hints`, `Instrument`, `RunGo`, `RunSuite`, `RequireExit`, `RequireOutput`,
  `MutantAt` and `Activate` over a `testkit.Compose` environment, so those
  questions have one answer each. The suites that no longer touch a process
  global run in parallel, which is where the wall-clock time went. Measured on
  the machine this was written on, warm — the developer's cache warm before, the
  harness's warm after — the four integration suites take about 9 s of wall clock
  (the median of three runs, and the same three runs each) where the same four
  took 17 s, and `internal/execute`'s about 3 s where it took 8 s. Some of what
  is left is deliberate: the build-cache guard below hands its validation a cache
  nobody has written to, so it pays for a cold compile on every run.

  `TestValidateDoesNotTouchTheUsersBuildCache` is the new assertion behind that
  last point, and it is stated three ways because the three fail separately:
  `go env GOCACHE` under the very environment a validation ran its builds with
  has to name the harness's cache, something under that cache has to have
  changed, and no cache may appear under the private home an unpinned `GOCACHE`
  would resolve to. The developer's own `go-build` is compared as a directory —
  its modification time and the names in it — because `go test` is itself a go
  command using that cache while the test runs, so anything finer would report
  somebody else's build as this phase's.

  `mutantkit.Discover` and `mutantkit.Instrument` now compose an environment of
  their own, and `DiscoverWith`/`InstrumentWith` take the caller's. Discovery
  reads types, so go/packages asks the go command for export data, so a discovery
  pass *compiles* the module and everything below it — which makes it the
  heaviest writer of build cache entries in every suite that drives it, heavier
  than the builds those suites are about, and left on the process's own
  environment every one of those entries landed in the developer's cache: a plain
  `go test -tags integration ./internal/testkit/...` put 28 files there. There is
  no form of these helpers that does that any more. The pair of tests beside them
  watches a private, empty cache receive the work — counting the files a compile
  writes rather than the directory's own entries, because a go command creates
  all 256 shards and its README when it merely *opens* a cache, so a top-level
  listing says nothing about whether anything was compiled
  (`testkit.BuildCacheEntries` is the one implementation of that question).

  The synthesized modules in these suites are built by `testkit.NewModule`,
  which carries this repository's own `go` directive rather than a written-down
  one — under the policy's `GOTOOLCHAIN=local`, a directive newer than the
  toolchain in use is an error rather than a download — and puts the SPDX header
  on every file, including the ones that only ever exist inside a `t.TempDir()`.
  The local `readFile`/`writeFile` pairs in `internal/instrument` and
  `internal/execute` are gone the same way, and so is the second copy of the
  build environment that `internal/instrument`'s probe-site tests had grown
  (`goCommandWithEnv`): `-mod=mod` and `-buildvcs=false` are on the command
  line now, where a flag that overrides `GOFLAGS` belongs, with the note about
  why a module in a temporary directory must never be VCS-stamped kept beside
  them.
- **A run trace: `github.com/P4suta/go-mutants/trace` and the
  `gomutants-trace-v1` contract.** A report says a mutant survived; nothing said
  which test binaries were run against it, with which arguments, for how long,
  or where their output went — so diagnosing a run meant re-running it with
  print statements. The new public package is a machine-readable account of what
  a run did: an `Event` for every phase, step, subprocess, mutant attempt, probe
  pass, validation step, coverage placement, cache decision, snapshot and sweep;
  a `Recorder` whose disabled form is a nil pointer every method is safe on; a
  directory sink that leaves a readable stream even for a killed run; a bounded
  in-memory ring for the failure nobody asked for a recording of; and strict
  readers (`Read`, `ReadSummary`, `Diff`) for reading one back. It is
  deliberately *not* evidence: no trace option enters a mutant identity, the
  workspace digest or a cache key, a failing sink costs the event and never the
  run, environment variables are recorded by name and never by value, and
  captured output is digested into the event and preserved beside the stream
  rather than serialised into it. Every recording ends with
  `events_emitted`/`events_dropped`, because best effort is only honest if the
  loss is reported. The `exec`, `prepare`, `mutant`, `artifact`, `note` and
  `run` payloads share their field names with goatest's own trace — which spells
  `note` a `progress` record, with the same `kind` and `detail` — so an embedder
  that records both streams gets one timeline across two tools rather than two
  to reconcile. Nothing asks for a recording yet — the flag, the
  environment variable and the wiring come next; the format landed first so that
  what is recorded into it is recorded against a contract. Documented in
  `docs/trace-v1.md`, published as `schema/trace-v1.schema.json`, and reasoned
  about in `docs/adr/0001-trace-is-not-evidence.md`.
- **Every subprocess is recorded where it is started, and every failure carries
  the command it was about.** go-mutants starts processes from a dozen places —
  the `go version` probe, both baselines, a compile per package, the coverage
  pass, each validation build, a run per mutant — and asking each of them to
  remember to record itself would be a rule with a dozen chances to be broken
  silently, in exactly the run somebody is trying to diagnose. So the account of
  a run starts at the choke point instead: `runner.Run` records exactly one
  `exec` event per call, after the child has been reaped and its output
  captured, and returns the sequence it was recorded at as `Result.TraceSeq`. A
  call site can no longer forget to record a command; it can only forget to
  *label* one, and the schema's `kind` enum makes an unlabelled command a
  recording that does not validate. A spec the runner refuses and a command that
  could not be started are recorded too — a command that never became a process
  is precisely what a reader needs to be told, and it is the one case a missing
  event would make indistinguishable from a command nobody issued.

  The second half is the error that used to arrive as "could not start
  /tmp/go-build123/b001/pkg.test" with no working directory, no arguments and
  nothing to reproduce it with. Every `runner.Error` now carries the
  `Invocation` it was about — argv, directory, label, and the trace sequence
  that leads to the whole output — and a `gocmd.Error` from a failed `go
  version` carries the probe's invocation and what the probe printed, because a
  binary that is not a Go toolchain explains itself in its own output. The
  messages themselves are unchanged, so two runs of the same failure still
  render the same line; what is new is what a renderer can ask for and print
  underneath it. Environment variables reach the recording as names and never as
  values, output is digested rather than serialised, and a recorder that is nil
  records nothing and costs a zero sequence, so a traced run and an untraced one
  take the same path.

  Nothing hands the choke point a recorder yet. The runner records into whatever
  `Spec.Trace` it is given, and today every production call site still gives it
  nil — only the `go version` probe names a kind (`go-version`) at all. The
  engine's labels, the options that carry a recorder down to them, and the sink
  that turns a recording into a file come in the changes after this one. What
  landed here is the guarantee that when they do, no command can be missing from
  the account.
- **The execution and validation phases say what they did: every build labelled,
  every mutant attempt recorded, every bisection step written down.** A run that
  spends four minutes before the first mutant and rejects eleven candidates
  along the way has, in its report, one number for each of those facts. Which
  compile took the four minutes, which file the bisection searched, which
  candidate the compiler condemned and in what words, which binaries a mutant
  was actually measured against, and which attempt of the two produced its
  verdict were all reasoning that existed only while the phase ran.

  So `internal/execute` and `internal/validate` now record into a
  `*trace.Recorder` carried on their `Options`. Every `go` command and every
  test binary they start names what it is — `go-list`, `go-test-c` with the
  package it compiles, `coverage-run` with the package it profiles,
  `mutant-run` with the mutant it activates, `probe-run`, `validate-build` — so
  a recording of a slow run is a list of labelled durations rather than of
  paths in a temporary directory. A `probe-run` names its package only when the
  pass was narrowed to one binary, because a probe pass is one measurement over
  everything it started rather than a fact about each child; `docs/trace-v1.md`
  says so beside the rest of the `subject` rule. `execute.Schedule` records one
  `mutant-exec` per *attempt*: the worker that ran it, the mutant's own package
  and short id, the binaries it tried in launch order, the outcome in the same
  word the report uses, and the `exec` events of the commands underneath it.
  Two attempts stay two events, because "survived" and "survived on the second
  try" are different facts about a flaky suite, and the serial retry pass — the
  one part of an execution phase that is deliberately not parallel, and where a
  run full of timeouts loses its wall-clock time — is timed as a `retry` stage
  when it has anything to retry, and reports itself failed when a cancellation
  left a mutant unretried. Validation records the instrumentation; each build
  with the file it was spent on and, for a failing one, the files the compiler
  blamed and how many are still undecided; the pristine gate that licenses the
  whole bisection; each file it isolated with what that file offered and what it
  kept; each condemned candidate with the compiler's own first line about it; and
  a closing `done` with what the phase spent and decided. `Attempt` and
  `ProbeAttempt` now carry the binaries they tried and the sequences those runs
  were recorded at — a pass that could not be finished included, because which
  binaries had already run is the first question a failed one raises — so an
  attempt and the commands it issued are one thing rather than two lists to
  reconcile.

  Nobody hands these options a recorder yet: the engine does that in the change
  after this one, and until it does every one of these calls is made on a nil
  recorder, which is the disabled trace. That is the point of recording
  unconditionally — a traced run and an untraced one take the same path, and no
  accepted set, no outcome and no message can come to depend on whether anybody
  was watching. The one thing a recording adds to a result is the sequence
  numbers that point into it.

  `execute.MutantRun` gains `DisplayID` and `Package`, both carried for the
  recording alone and both the caller's to fill, because a mutant's short id and
  its import path are the catalogue's answers rather than something the
  execution layer could derive from an activation identity. `Session.Exec` fills
  both. `internal/engine` fills the short id; **its `Package` is left for the
  change that hands the engine a recorder**, which is where the run's discovery
  results and the selection meet — until then a CLI run's `mutant-exec` carries
  no package, and nothing reads the field in the meantime.
- **The test suites' `go` commands no longer fill the developer's own build
  cache, and one command now shows and empties the one they do fill.** The
  bloat was never go-mutants compiling itself: the suites drive thousands of
  child `go build`, `go test -c` and `go list` commands against fixtures,
  synthesized modules and instrumented snapshots, each living at an absolute
  path that exists for a single run, so every entry they produce is keyed on a
  path nothing will ever look up again. That is what took `~/.cache/go-build`
  to 14 GB and filled a disk twice, from the direction nobody was watching. The
  obvious fix is the opposite failure — a `GOCACHE` under `t.TempDir()` is
  perfectly hermetic and recompiles the standard library once per test binary —
  so there is exactly one cache instead: shared, persistent, outside every
  temporary directory a test owns, and budgeted.

  `internal/devtools/testcache` is what names it and what cleans up after it.
  `mise run test-cache-status` (also `just test-cache-status`) prints both
  directories the harness owns outside a temporary directory — the build cache
  and the root a kept-on-failure scratch directory is filed under — with their
  sizes in both spellings and their file counts; `mise run test-clean` empties
  both. `mise run test-integration` and `mise run dogfood` now run through
  `testcache exec --budget 4GiB`, which exports `GOCACHE` and
  `GO_MUTANTS_TEST_GOCACHE` into the child, reports the growth on stderr when it
  ends, empties the cache only if the run left it over budget, and exits with
  the child's own status — so both gates are exactly as strict as they were.
  `mise run test` deliberately stays on the developer's cache, because it
  compiles go-mutants itself. CI points `GO_MUTANTS_TEST_GOCACHE` at the
  runner's temporary area, which dies with the runner and is never restored from
  an actions cache, and ends both jobs with a `status` step that runs even when
  the job failed.

  Nothing is removed without a marker. This is a tool that empties directories
  and is pointed at them by an environment variable, and
  `GO_MUTANTS_TEST_GOCACHE=$HOME` is an absolute path like any other — nothing
  in a path says who made it. So ownership is written down: the harness stamps
  the cache with `.go-mutants-testcache` when a test resolves it, `exec` stamps
  it before the run it wraps, and nothing removes a directory that does not
  carry that file. Two details make the scheme hold rather than merely exist. A
  stamp is never written into a directory that already holds files that are not
  the harness's — otherwise `exec` would issue itself the permission slip on the
  way in — so only an absent or empty directory is claimed, and anything else is
  reported and left alone. And three directories are refused whatever they
  contain, before anything is measured, created or removed: a filesystem root,
  the user's home, and the go command's own `<cache>/go-build`, the last because
  it is the exact directory this exists to keep the suites out of. The failure
  mode of the whole arrangement is a cache that grows, which is the problem it
  was written to notice rather than one it can cause.

  The two ways a directory does not get emptied end differently, and the
  difference is the point. A directory that carries no marker is *refused*:
  nothing is run against it and `mise run test-clean` exits non-zero, because
  being pointed at something that is not ours is the answer to the question the
  person asked. A directory that was ours and could not be finished — a file
  held open by an antivirus scanner, or a test binary Windows has not finished
  unmapping — is retried once, reported, and forgiven with a zero exit, because
  a collector that turns a green run red over a directory it wanted to delete
  has done more damage than the directory ever would. `trim` and `exec` forgive
  both, since they are housekeeping around somebody else's run.

  One more rule is deliberately the opposite of tidy: a trim never touches the
  kept scratch root, which holds the evidence of runs that failed — deleting a
  failing run's diagnostics because a *cache* grew is the one thing this tool
  must not do.
- **`Workspace.ToolchainVersion()`.** A workspace already resolves the
  toolchain it froze the module against, and every consumer that needed the
  version was running its own `go version` to learn something the workspace was
  holding. It is exposed as the resolved raw version string, empty on a nil
  workspace, so a caller pays for that resolution once per workspace rather
  than once per command that asks.
- **Concurrent, isolated `Workspace.Exec` controls with a snapshot-integrity
  gate.** Engine consumers may now run independent pre-preparation build and
  baseline commands concurrently. Every call gets a private `TMP`, `TEMP` and
  `TMPDIR`; `Prepare` and `Close` wait for in-flight calls, and `Prepare`
  re-digests the frozen tree before discovery and refuses any added, removed or
  changed path. This keeps concurrent controls from sharing temporary state or
  silently changing the program their later mutation session claims to
  measure.
- **Caller-owned generated-tree exclusions through
  `OpenOptions.SnapshotExclude`.** Embedders can keep known output and report
  trees out of the frozen mutation workspace instead of copying and hashing
  bytes that are outside their assurance input boundary. Invalid patterns fail
  before a temporary directory is created.
- **Package-scoped mutation discovery through
  `PrepareOptions.DiscoveryPackages`.** Callers that build test binaries for a
  narrow package set can now type-check and discover only the packages that
  may contain candidates and compile-validate only that instrumented package
  closure, while keeping the test-binary package set independent. Empty still
  selects `./...`.
- **An owner and a collector for every temporary directory, and
  `OpenOptions.KeepTemp` to keep one on purpose.** A run copies the whole
  module into the temporary area and removes the copy when it finishes, which
  is a promise no process can keep: a SIGKILL, an out-of-memory kill or a
  closed terminal leaves hundreds of megabytes behind that nothing will ever
  delete. On the machine this was written for, nine such directories had piled
  up at a quarter of a gigabyte each, beside the live ones of a run in
  progress.

  Every top-level directory go-mutants creates — the snapshot, the probe tree,
  the per-run scratch — now carries an `owner.lock` held open for its whole
  lifetime and an `owner.json` naming the schema, the process and the start
  time. The lock is the liveness signal and the only one: it disappears when
  the process does, however it died, which a pid cannot say on a machine that
  reuses them. Before it copies anything, a run collects the directories under
  its own prefixes whose lock is free — and, for one release, unowned ones that
  nothing has touched for a day, which is the shape older versions left behind.
  A directory somebody else is using holds its own lock and is never touched,
  so concurrent runs in one process or several are safe; nothing else in the
  temporary directory is ever looked at.

  `OpenOptions.KeepTemp` is the other half. The sweep would otherwise make one
  thing impossible — looking at the tree a failing mutant actually ran in — so
  a kept directory is marked `kept` in its marker rather than merely left on
  disk, which is what makes the next run's sweep leave it alone instead of
  collecting it minutes later. `Workspace.Preserved()` names what was kept
  after `Close`, and `Workspace.Swept()` reports what the workspace collected
  on the way in. Neither appears in any report or schema: they are facts about
  the machine, not about the run.
- **A probe pass on the engine API: `Session.Probe`.** The probe tree below
  could record an infection and nothing ran it. This runs it. A session prepared
  with `PrepareOptions.Probe` builds that tree beside the mutant one, and one
  call runs one test or fuzz target against it and returns the catalog indices
  whose site produced a value the mutant would not have:

  ```go
  measured, err := session.Probe(ctx, gomutants.ProbeRequest{
      Package: "example.com/project/internal/codec",
      Args: []string{"-test.run=^TestRoundTrip$"},
  })
  ```

  A target that never distinguished a mutant from the original program cannot
  kill it, so a consumer may skip that execution and record "survived". Every
  decision here is shaped by what happens when that licence is given wrongly. An
  empty set of indices and "no facts" are the same bytes to a careless caller and
  opposite instructions to a careful one, so they are different values:
  `ProbeResult.Infected` is non-nil exactly for the `measured` outcome, while
  `test-failed`, `timed-out`, `unavailable` and every error carry `nil`. The pass
  stops at the first binary that does not exit zero, because the indices the rest
  would append cannot be combined with a pass that already failed — the result
  would be a subset of the truth wearing the shape of the whole of it. A
  *missing* log after a clean exit is the one absence that is a fact and comes
  back as the empty set: the runtime writes its header in `init`, so a binary
  that wrote nothing linked no probe and ran no probed site.

  `Mutant.Probed` says which mutants the tree speaks for, and it is the
  conjunction of two things rather than either alone. A mutant with no probe form
  leaves its file untouched in the probe tree, so that tree compiles and the
  validation *accepts* it exactly as it accepts a probed one — and a caller
  reading "accepted" as "probed" would take its permanent absence from every log
  as licence to skip the tests that kill it. So the consumer's rule has two
  clauses: skip only when the mutant is `Probed` and a `measured` probe does not
  name it, and treat an unprobed mutant as infected by every test.

  The tree is copied from the pristine snapshot *before* the mutant tree's
  validation rewrites it in place, and a copy whose workspace digest differs
  fails `Prepare`: a probe tree that is not the mutant tree's source proves
  nothing about it. It is validated by the same phase in `ModeProbe`, so a probe
  site that does not compile is bisected out and costs that mutant its probe and
  nothing else, and it carries no verification command of its own — a probe pass
  already reports a failing target as no facts per call, so a suite-wide gate
  would buy what the per-call rule already gives and cost a full test run to get
  it. `Session.Close` removes it.

  `Probe` is off by default and the whole change is additive. `Session.Exec`, the
  mutant tree, `go-mutants list --json` and the run-report schema are unchanged,
  and `Probed` is session-local: it appears in no document, because it describes
  a tree that exists for as long as the session does.
- **A probe tree that measures return-value mutants.** The runtime below could
  record an infection and nothing called it. This is the first form that does.
  In `ModeProbe`, every eligible `return` carrying a return-value mutant becomes

  ```go
  { var r0 T = E0; var r1 U = E1; if r1 != K { __gm.Infect(i) }; return r0, r1 }
  ```

  and nothing else in the file changes. No mutant is active in it: what runs is
  the program the user wrote, and what is recorded is whether the mutated value
  would ever have differed from it — which is the fact that licenses not running
  a test against a mutant.

  The form is first because its exactness needs the least argument. `T` is the
  *declared result type* — `return 0` in a function returning `int64` becomes
  `var r0 int64 = 0` — because that is the conversion the `return` performs, and
  the one the mutant's constant would have gone through, so the comparison is
  between the two values the two programs would really have returned. Named
  results and `defer` see what they always saw, and a block ending in `return`
  is still a terminating statement.

  What is *probed* is narrower than what is mutated, because the mutant returns
  its constant **instead of evaluating** the operand it replaces. A statement is
  probed only when every one of its operands is effect-free — no call, no method
  call, no receive, no `append` — since an effect in the mutated operand is one
  the mutant never has, and an effect in any other operand makes the evaluation
  order matter, which the rewrite fixes to source order while the compiler is
  free to read a plain variable after the calls beside it. A result is probed
  only when its own operand cannot panic, because a panic is a divergence
  between the two programs that the comparison is never reached to record. And a
  floating-point or complex result is never probed, because `-0.0 != 0` is false
  while the two values are distinguishable. One refused result leaves the others
  in its statement probed.

  `discover.Guard` grows a `Return` hint for the six return-value rules, spelled
  with the machinery Form D's declarations already go through. Besides the three
  conditions above it is absent when a result type cannot be written in the file
  — a dot-imported package's, `unsafe.Pointer` — and when a result is or
  contains a type parameter, whose values need not be comparable with a
  constant. An absent hint costs the probe and nothing else: the candidate is
  still catalogued, still mutated and still guarded, which is why it is not a new
  skip reason and why nothing in `go-mutants list --json` or the run report
  changes.

  `validate.Options` grows the same `Mode`, so the probe tree is built and
  bisected by the phase that already builds and bisects the mutant tree. A
  rejection there means "this mutant's probe site does not compile", so that one
  mutant goes unmeasured while its neighbours in the same file still are.

  Every other family is unprobed for now, and a file holding only such mutants
  comes out of `ModeProbe` byte for byte as its author wrote it. A run then
  learns nothing about which tests could observe them and runs them all, which
  is the safe direction. The mutant tree is unchanged, byte for byte: the two
  modes share one site index, one alias, one splicer and one forest walk, and
  differ only in what a site is rewritten into.
- **A probe runtime, and the infection log it appends to.** The branch proof
  below discharges executions on paper by reasoning about a condition. The
  proof beneath it is *infection*: if the site of mutant `m` never evaluated to
  a value different from the original's during test `t`, then `t` cannot have
  killed `m` — a mutant is a one-site edit, so equal values at every evaluation
  and equal side effects mean the two programs ran the same state sequence, and
  running them against each other can only reproduce an answer already known.
  Measuring that needs a second instrumented tree in which no mutant is ever
  active, where the original semantics run and each site reports, without side
  effects, whether the mutated value would have differed.

  This is that tree's runtime half. `instrument.Options` grows a `Mode`; the
  zero value is the mutant tree every existing caller already builds, and
  `ModeProbe` generates the probe runtime into the snapshot. It rewrites no
  file yet — a probe tree today is the original source with a runtime nobody
  calls — and that is said out loud in `ModeProbe`'s own documentation rather
  than left to be discovered, because the runtime, its log format and the
  reader of that format are what the rewriting has to match, so they are
  settled first.

  The runtime exports one name, `Infect(i uint32)`, and carries no table from
  mutant ID to index: a probe tree activates nothing, so it never resolves an
  ID. One atomic compare-and-swap per mutant keeps a site evaluated a million
  times to a single line, and that line goes straight to an `O_APPEND` file —
  no exit hook, no flush window, so whatever a process wrote before it died is
  exactly what it proved. `GO_MUTANTS_PROBE` names the file; empty or unset is
  an ordinary run, which matters because the same tree is also built and run by
  people who are not probing. A log the runtime cannot open or write exits `98`
  instead of running the tests, because an empty log reads exactly like a run
  in which no site was ever infected, and *that* reading is what licenses
  skipping an execution — silence is the one answer a probe must never give.

  The format is `gomutants-infection-v1`: a header naming the catalog digest
  and the array width, then one decimal index per line. Several test processes
  append to one file, so the header appears once per process rather than once
  per file, and every occurrence must be identical.

  `instrument.ReadInfectionLog` reads it back and is handed the catalog's
  **size**, not that width. The two differ for exactly one catalog — the array
  is never zero-length, so an empty catalog's runtime writes a header saying
  one — and the reader derives the width itself through the rule the generators
  use, so no caller has to know it. Indices are bounded by the size instead, so
  an empty catalog's log stays readable (its header alone says nothing was
  infected, because nothing could be) while any index in it is refused as
  naming a mutant that does not exist.

  It is fail-closed throughout: an empty file, a foreign or inconsistent
  header, an index that is not a decimal `uint32`, an index at or past the
  catalog size, or a last line the writer never finished each yield `GOM7330`
  and no indices at all. The part of a damaged log that still parses is
  precisely what a smaller, wrong answer looks like, and a smaller answer here
  is a test that was skipped when it should have run.

  The mutant runtime is untouched, byte for byte, and a test now asserts that
  it still exports exactly `M`: the two packages carry the same name and only
  never meet because they are generated into different snapshots.
  `docs/architecture.md` has the tree, the format, and why 98 exists.
- **A branch proof on the mutants whose edit can only narrow a condition.**
  `le-to-lt`, `ge-to-gt`, `or-to-and` and `nil-error-branch` all produce a
  mutated condition that *implies* the original one. When such an edit sits in
  the condition of an `if` or a `for`, discovery now records the span of the
  body that condition gates and publishes it as an optional `branch` object on
  the mutant, in both `list --json` and the run report.

  The point is what a consumer can do with it without asking go-mutants
  anything further. If no statement of that body ran during a test, the original
  condition was false every time it was evaluated; the mutated one implies it,
  so it was false too, the branch taken was identical, and the two programs ran
  the same. That test cannot have observed the mutant and does not have to be
  executed against it — which is a whole class of executions a consumer holding
  per-test coverage can discharge on paper.

  The span is the body's braces rather than its first statement, because
  `cmd/cover` moved: for one `if`, Go 1.26.6 records the body block starting at
  the `{` and Go 1.27.0 starting at the first statement, and `[{, }]` is the one
  span that contains the recorded block start under both while containing no
  block that belongs to code outside the body. A proof is emitted only when the
  path from the edit to the statement is monotone, the *whole* condition is
  inert — the mutant may evaluate fewer sub-expressions than the original, so an
  effect or a panic in one it skips would be observable — the body is non-empty,
  and no `//line` directive would make the coordinates name another file. Every
  refusal is silent: an absent proof is not a place go-mutants declined to
  mutate, only one it declined to reason about. `docs/operators.md` has the
  lemma and every condition; `docs/json-schema.md` has the wire shape.

  Mutant identity is untouched: no rule's name, version, or emission changed, so
  every cached outcome survives.
- **Release automation, as two workflows with a human between them.**
  `release-please.yml` runs on every push to `main` and maintains one open
  Release PR: it reads the conventional-commit subjects, works out the next
  version, and rewrites `VERSION`, `internal/cli/root.go`'s marked line, and
  `.release-please/CHANGELOG.generated.md`. It deliberately does not tag —
  `skip-github-release: true`. `release-publish.yml` is `workflow_dispatch`
  only, behind the `release` environment's required reviewer, and does the
  other half: tag, verify, gate, build, sign, attest, upload.

  The split is not ceremony, it is the only shape that works. The obvious
  design — release-please pushes the tag, a tag-push workflow publishes — is
  the one `release.yml` implemented and it cannot work here, because
  release-please pushes with `GITHUB_TOKEN` and **a push made with
  `GITHUB_TOKEN` starts no workflow**. GitHub does that on purpose, to stop
  workflows triggering each other forever, and the usual escapes are a personal
  access token or a GitHub App key: a long-lived credential with write access
  to the repository, held so that a bot can start a job a human was going to
  approve anyway. Putting the approval on the workflow instead removes both the
  credential and the indirection. Merging the Release PR now publishes nothing;
  pressing *Publish release* and approving the environment is the release
  decision, and it is a decision a person makes once, in one place.

  `release.yml` is gone. Its `verify` job is not lost — the same gates
  (`check`, `test-integration`, and now `dogfood` too) run inside
  `release-publish.yml` against the checked-out tag, which is a stronger claim
  than the old job made: it also refuses to build if `VERSION` and
  `internal/cli`'s `defaultVersion` disagree with the tag, and it refuses
  before installing a toolchain, so a typo in the retry box costs seconds. The
  old tag-and-source agreement check lives on there in that form.
  `.goreleaser.yaml` stopped drafting: a draft nobody publishes was standing in
  for the human decision, and now the environment approval *is* the human
  decision, so a second one would only be a second place to forget. It gained
  `mode: keep-existing` so the archives attach to the release release-please
  created without overwriting its notes, an explicit `prerelease: true` because
  `auto` would have read `v0.1.0` as stable and quietly flipped
  release-please's prerelease flag on the way past, and
  `replace_existing_artifacts: true` because retrying a release is a designed
  path here and GitHub answers a re-uploaded asset name with a 422.

  **The generated changelog is deliberately not `CHANGELOG.md`.** It is
  `.release-please/CHANGELOG.generated.md`, which diverges from
  release-please's default. This file is the reason: its entries say *why*, they
  run to paragraphs, and several describe a decision spanning a dozen commits —
  none of which a concatenation of commit subjects can produce. Pointing the
  bot at this file would mean either losing that or hand-editing a file a bot
  rewrites, which is a merge conflict on every release. So the machine log goes
  somewhere else and feeds the GitHub release notes, this file stays
  authoritative, and it is rolled by hand (`[Unreleased]` → `## X.Y.Z - date`)
  in an ordinary pull request merged *before* the Release PR.
  `docs/release-checklist.md` documents that order, because
  `release-please-config.json` is JSON and cannot document itself.

  Two consequences worth stating plainly. Pull request titles must now be
  conventional commits, because squash-merging makes the title the subject on
  `main` and that subject is release-please's only input — a title it cannot
  parse produces no Release PR at all, a release that silently does not happen;
  `CONTRIBUTING.md` says so where the commit-style guidance already lived. And
  `internal/cli`'s checked-in version no longer carries a `-dev` suffix, since
  release-please owns that literal and writes the bare released version into
  it. `internal/cli.resolveVersion` is what compensates: an unstamped binary
  now consults `runtime/debug.ReadBuildInfo` before falling back, so a
  `go install …/cmd/go-mutants@latest` or `@main` build reports the version or
  pseudo-version the module proxy actually served it instead of whatever the
  source tree last said. A link-time stamp still outranks it, and a plain
  `go build` from a working tree still reports the default — the residual gap,
  recorded in the checklist rather than papered over.
- **Scoped test binaries.** A `test.command` that go-mutants can read as a set
  of package patterns now decides which test binaries a run builds, not just
  which suites the baseline measures. A module whose tests live in three of
  forty packages compiles three binaries and starts three processes per mutant
  instead of forty. This repository's own dogfood gate went from 16 mutants in
  7m08s to 121 mutants in 48-50 seconds on the same machine — seven times the
  scope in a ninth of the time — and could grow from two files to two whole
  packages because of it. What bounded it after that was missing tests rather
  than the clock, and those tests are in this release too, so the gate grew to
  three whole packages and 560 mutants in about 1m20s — see *The dogfood gate
  covers every pure-core package* below — and then to six packages and 600, in
  the entry at the top of this section. `.go-mutants.toml` records each
  measurement next to the scope that produced it, where somebody widening the
  scope again will read it.

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
- **The dogfood gate covers every pure-core package.** This repository's own
  `.go-mutants.toml` now includes `internal/mutation/*.go` where it included
  only `shard.go`, so the gate is every package the rest of the tool is built on
  — the mutation model, the glob engine, the interval relation — and nothing is
  left out of it because it was inconvenient: 560 mutants, 544 killed, 16
  declared, 100.00%, about 1m20s at `--jobs 4`. The widening and the tests that
  made it possible are one change, because the measurement recorded in that file
  said they had to be. It reported 41 unexpected survivors, every one of them in
  `internal/mutation`, and the file said in as many words that the honest way to
  include the package was to write the tests that kill them — not to declare 41
  expectations, which would be a skip list wearing a ledger's clothes, and not
  to exclude the seven files that survived, which would be choosing the scope to
  fit the score. The gaps the gate found were closed with tests, not with scope.

  Twenty-six of the 41 are now killed, by cases in the package's own suites.
  `Tier`'s names are pinned one at a time, which the round trip could not do
  because `ParseTier` resolves a name by comparing it against `String()`, so a
  `String()` that answered the same wrong thing twice still round-tripped. The
  catalogue's comparator is asserted in both directions at every tiebreak,
  which is what `slices.SortFunc`'s strict weak ordering actually asks of it.
  `normalizePath`'s volume-letter bounds are checked at all four ends, because
  a range one letter short accepts `z:\…` as a relative path and mints an
  identity for a file outside the module. `Span.Slice` is checked at the
  half-open end, where off by one refuses the last mutation site in every file.
  And `Verdict.OK`'s conjunction, `Verdict.Has`'s answer about a reason it was
  not given, `TallyOf`'s split of survivors into the two counters the ledger
  distinguishes, the registry accessors' refusal of an unknown name rather than
  a row zero with `ok`, `MustRegistry`'s panic on a table that fails its own
  consistency check, `AddAll`'s report of what `Add` refused, and the short-id
  boundaries at 64 characters and at a collision of exactly two all have tests
  that did not exist before.

  The remaining 15 are declared in `[[mutation.expect]]`, each with the
  argument for that row rather than fifteen copies of the word "equivalent":
  five are equivalent by construction — a comparison the surrounding branch has
  already decided, or a named constant that is its type's zero value — one is
  equivalent for every value another check in the same package calls coherent,
  one guards a state the package's own API cannot produce, and eight sit behind
  a 4 GiB length prefix or a `math.MaxUint32` index, where the test that would
  kill them is a test that allocates four gigabytes. Declared is not skipped:
  every row is judged on every run, survival is what fulfills it, and a kill or
  an id the catalogue no longer holds is exit 2. Ten survive a test binary that
  ran; the other five sit on lines no binary in the scope reaches, so the run
  reports them `survived (uncovered)` without starting a process — coverage
  reaching the same verdict those five reasons argue for, from a cheaper
  direction.

  `policy.minimum_score` moved 96 → 99 in the same change, because a percentage
  buys a different number of survivors at every catalogue size. The floor has
  never been the gate that guards CI — `--strict` fails on the first unexpected
  survivor, and that is the flag `mise run dogfood` passes — it is the backstop
  for a run that did not ask to be gated. Over 120 scored mutants, 96 bought
  four survivors of slack; over 544 it would buy twenty-one, which is a
  materially weaker backstop wearing a number that had not changed. Ninety-nine
  buys five.
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
- CI as workflows with every action pinned by commit SHA,
  `permissions: contents: read`, per-ref concurrency, and a timeout on every
  job: `ci.yml` (quality, a three-OS test matrix, artifacts, dogfood),
  `nightly.yml` (one leg per fuzz target the repository actually has, plus a
  property job at a deepened budget), and `release.yml` (verify, then a
  draft-only release job that is the sole holder of `contents: write`).
  Dependabot watches actions and Go modules weekly. *`release.yml` was replaced
  before any of this was released — see* **Release automation** *above for the
  two workflows that supersede it and why the tag-triggered shape could not
  work.*
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
- **Developer infrastructure: the corpus modules `fixtures/README.md` had been
  promising, the CRLF workspace it cannot hold, and a gate that keeps the ledger
  and the directories honest.** The document ended in a paragraph saying that
  later phases would add fixtures for `go.work`, build tags, CRLF sources, a
  package with no tests, a suite that writes into its own directory and a
  declaration whose type cannot be named — six cases the run has to get right
  that no amount of exercising `simple/` or `families/` reaches. They exist now,
  each driven by named tests: `workspace/`, `tagged/`, `untested/`,
  `selfwriting/` and `unnameable/`, plus a CRLF copy of `simple/` synthesized by
  `testkit.NewModule(t).From("simple").CRLF()` — which cannot be a directory,
  because `.gitattributes` pins `* -text` and a checked-in CRLF file would be
  CRLF on every platform and would change every mutant identity that covers it.

  `internal/testkit`'s `TestCorpusConformance` is the gate. Prose conventions
  are what a new fixture is written without reading, and each rule it checks is
  one somebody would otherwise break in silence: a missing `go.mod` makes a
  fixture a package of this repository, a `require` makes the integration suite
  depend on the network, a `go` directive above the toolchain in use turns every
  command into a toolchain download that `GOTOOLCHAIN=local` refuses, a CRLF
  ending changes the digests identities are made of, and a report directory or a
  compiled binary under `fixtures/` is a run that was pointed at the corpus by
  accident. It reads `fixtures/README.md` in both directions — a fixture with no
  row is undocumented, a row with no directory is a fixture somebody deleted —
  and requires every fixture to be named by some test, because a fixture nothing
  drives costs a checkout and proves nothing. The rules are a function of a root
  rather than of this repository, and are themselves tested against a corpus
  built to break them one convention at a time: a gate whose only evidence is
  that it passes on a conforming tree is a gate nobody has seen fail. The
  `git status --porcelain --ignored -- fixtures` step that guarded CI's platform
  tests now ends every job that runs a suite, the nightly ones included.
- **Four run reports of real runs are pinned as goldens**, in
  `internal/engine/testdata/`, for `simple/`, `killable/`, `untested/` and
  `tagged/`. Every other assertion about a report is a field somebody decided to
  write down; a golden is the whole document, so a field that appeared, one that
  vanished, a number that moved and a string that changed shape all arrive as a
  diff. They are recorded through `mutantkit.NormalizeRunReport`, generated on
  Linux and compared on all three operating systems CI runs — which makes the
  cross-platform comparison the normaliser's own test: a field that is a fact
  about the host, the toolchain, the clock or the scheduler and that nobody
  normalised fails the first time anybody looks.

  `mise run golden-update` regenerates them, and it is two commands now rather
  than one. The test that records them is `//go:build integration`-tagged, so a
  `go test ./internal/engine -update` without the tag would compile a package
  with no golden test in it, pass, and rewrite nothing — the task would name the
  package and regenerate none of it. `TestGoldenUpdateTaskHandlesTaggedPackages`
  requires every integration-only golden package to be reached with the tag, and
  the parser behind `TestGoldenPackagesAreNamedByTheUpdateTask` learned the
  array form of a mise `run` so that the second command is not invisible to it.

### Changed

- **A second concurrent `Workspace.Prepare` is refused straight away instead of
  waiting for the first.** A workspace is prepared exactly once, and that used
  to be enforced by the exclusive lock: the second caller queued behind the
  first preparation's ten minutes in order to be told it was never going to be
  allowed one. The claim is now taken under the workspace's state lock before
  anything is read, so the second call returns `ErrWorkspacePrepared`
  immediately. A `Prepare` refused for an *option* the engine does not accept
  still hands the claim back, exactly as before: nothing has been read or
  written, and a typo in a line number must not cost a caller a fresh snapshot.

  One narrow case changes for the worse and is worth naming. A second `Prepare`
  that arrives while the first is still checking its arguments is now refused,
  where the exclusive lock would have made it wait for the refusal and then
  serve it. The claim is held for the length of an argument check, so the window
  is microseconds and only a genuinely concurrent pair can meet in it — and a
  program with two goroutines racing to prepare one workspace is one whose
  second caller was never going to be served anyway.
- **The drift gate can now fire after discovery, with the same words, and a new
  `DriftError.Stage` names the write it could not have seen.** The integrity
  gate moved from before discovery to the top of the instrumentation window,
  because a gate that re-digests the tree has to run with the tree held
  exclusively — otherwise it can read a file a command is halfway through
  writing. Its message is unchanged (`gomutants: prepare commands changed the
  frozen snapshot:`) and so is `Stage: "commands"`; what changed is that a
  preparation now reaches it after a discovery pass rather than before one, so a
  tree a command had already changed costs a discovery pass before it is
  refused, and a change that stops discovery itself is reported as a discovery
  failure rather than as drift.

  The new stage is `"discovery"`, and it exists because discovery now reads the
  tree while a command may write it. A command that changed a source file *and
  put it back* leaves the gate nothing to find, while the catalogue built in
  between identifies its mutants by the digest of bytes that are nowhere on
  disk — and every later answer about one of them, a cached one most of all,
  would be about a program nobody has. So every file discovery *read* — not
  only the ones that yielded a mutant, because a file a transient edit emptied
  yields none at all — and the bytes captured for restoration are compared
  against the frozen manifest under the exclusive lock; a mismatch fails the
  preparation with `gomutants: prepare commands changed the snapshot during
  discovery:` and names the files. `internal/discover.Result` carries the new
  `SourceDigests` map that makes the check total over what was read.

  The other end of a preparation needs no stage, because the build reads no byte
  of the tree. The window ends at `main_restoration` and the binaries are
  compiled after it, so a command writing during the build could once have had
  its bytes compiled in; the binaries are now compiled from a copy of every
  frozen file, taken at the top of the window and mapped through the same
  overlay, and `Session.Changes` compares against the manifest those copies came
  from. A write a command leaves there changes the tree and nothing the session
  is made of, so it is reported by `Session.Changes` and fails nothing — see
  **The test binaries are compiled from the frozen manifest** above for what
  that replaced.
- **`run` and `list` pointed at the root of a `go.work` workspace are refused
  before anything is copied, with GOM4102 and the user's own `go.work` named.**
  Discovery has always refused a workspace — one module path, one set of
  module-relative identities, one baseline, and a workspace has none of them —
  but it is handed the snapshot, and it runs after the copy. `run` never
  reached it: the scope resolution got there first with a different story,
  because `go list ./...` in a workspace directory places no package, so the run
  reported the *user's test command* as matching nothing. That is a true
  sentence about the wrong subject, and it sends a reader to their
  `test.command`. `list` did reach it, and named the `go.work` inside a
  `go-mutants-snap-…` directory that its own cleanup had already removed — the
  one actionable thing in the message pointed at nothing. Both now ask before
  the copy, through `internal/discover.CheckWorkspace`: the same check
  discovery makes, exported so that neither caller grows a second code for one
  condition.
- **The message `gomutants: exec: workspace is already prepared; execute test
  targets through its session` is gone, because the case that printed it is
  gone.** It was `Workspace.Exec`'s single refusal after any `Prepare`, and a
  successful preparation no longer refuses anything. What remains of the rule —
  a preparation that failed — is a *different* condition with a different
  answer, so it says so in its own words: `gomutants: exec: workspace
  preparation failed; its tree may hold instrumented sources`, carrying
  `ErrPrepareFailed`.

  No message changed. This project freezes the sentences it prints, and that
  promise is about a case keeping its words, not about a case being kept: a
  consumer matching the old text was matching a refusal it will now not receive,
  and rewording it in place would have been worse — the same sentence for a
  narrower rule. Consumers classify these with `errors.Is` rather than by text;
  the sentinel is what the new case is for.
- **A run whose context ran out of time is a failure, not an interruption.**
  It used to be both, depending on which command happened to be in flight: the
  engine read any expired context as `GOM4030 the run was interrupted`, while
  the same expiry surfacing from internal/gocmd, internal/validate or
  internal/execute came back under those packages' own codes and was read as an
  ordinary failure. One cause, two answers — and now that a keep and a
  diagnostics bundle hang off the difference, the same deadline would have kept
  the snapshot or not depending on the timing.

  `engine.Interrupted` now asks for `context.Canceled` in the error's chain and
  nothing else, and `RunOutcome.Status` is decided by that same predicate. A
  cancellation is somebody's decision — a Ctrl-C, the dashboard's quit key, an
  embedder calling `cancel` — and needs no explanation: it reports
  `interrupted`, writes no bundle, keeps nothing that `--keep-temp=on-failure`
  would have kept, and exits 130 or 143 exactly as before. `--keep-temp=always`
  keeps after a cancellation as after anything else: it is the word the user
  typed, and somebody stopping a run *because* they have seen enough is somebody
  who wants the tree. A deadline is the run failing to finish in the time it was
  given, which is a question about where the time went: it reports the new
  `GOM4046`, exits 2, keeps what `--keep-temp=on-failure` was asked to keep, and
  the CLI writes it a bundle. Every package that raises an interruption already
  wrapped the context's own cause, so nothing about a real Ctrl-C moved.
- **Every boolean environment variable reads the same spellings.**
  `GO_MUTANTS_TRACE` used to accept only the literal `1` and `true` as a yes, so
  `GO_MUTANTS_TRACE=TRUE` was read as a request to record into a directory named
  `TRUE` — refused by the workspace rule, and reported as a `GOM1013` warning
  about a trace nobody could see they had asked for. All three of
  `GO_MUTANTS_TRACE`, `GO_MUTANTS_KEEP_TEMP` and `GO_MUTANTS_DIAGNOSTICS` now go
  through `strconv.ParseBool` — `1`, `t`, `T`, `TRUE`, `true`, `True` and their
  negatives — which is the vocabulary every Go program on the machine already
  answers to. An unset variable still means "said nothing", and anything that is
  not a boolean still means what it always did to the variable that accepts one:
  a directory for `--trace`, a mode for `--keep-temp`, and a usage error for
  `--no-diagnostics`. A user who learns the rule once should not find a third of
  it untrue.
- **The root suite is tiered, and one long sleep now happens only when it is
  asked for.** `api_integration_test.go`, `api_contract_test.go` and
  `errors_integration_test.go` carry `//go:build integration`;
  `workspace_test.go` and `external_contract_test.go` were renamed to
  `workspace_integration_test.go` and `external_contract_integration_test.go`
  and tagged with it; `session_test.go` was split, with its one
  toolchain-driving test moved to `session_integration_test.go` so the six pure
  ones stay in the tier a developer runs on every save. `go test .` went from
  28 seconds to 0.2, and needs no `go` on `PATH`.

  The fixture `api_integration_test.go` injects has a test that sleeps, because
  two assertions need a target that outlives its timeout and a sleep is the only
  way to write one. It was ungated at ten seconds, so it was paid twice by
  everything else that ran the package — the baseline `go test ./...` and the
  verification inside `Prepare` — for the sake of two executions that wanted it.
  It now sleeps only under `SESSION_BLOCK=yes`, which those two executions pass,
  and `TestSessionBlocksOnlyWhenAsked` asserts both directions, so that a
  deleted `if` fails rather than costing two silent minutes.
  A killed target is now proved dead rather than assumed. The elapsed bounds
  beside those executions only ever showed that `Session.Exec` *returned*: if
  SIGKILL or `TerminateJobObject` silently stopped working, the call would still
  come back inside `TerminationGrace + IODrainGrace` — `Cmd.WaitDelay` closes the
  pipe whatever the child is doing — while the target slept on for the rest of
  its minute and every check passed. The blocking target now records its own pid
  from `init()`, the earliest point at which a Go program can do anything, and
  each of the two executions polls the operating system afterwards until that pid
  is gone: signal 0 for `ESRCH` on POSIX, `OpenProcess` plus
  `GetExitCodeProcess` against `STILL_ACTIVE` on Windows. The probe has a test of
  its own against a live subprocess, because a probe that always answered "gone"
  would make every proof that uses it vacuous — which is the defect being fixed.

  The two elapsed bounds stay, now described as what they are: a promptness
  check, which still catches a supervisor that waited and a cancellation that
  stopped being delivered. They are
  `runner.TerminationGrace + runner.IODrainGrace + 20s` rather than a round
  number, and the gated sleep is a minute. Every number this replaced was wrong
  in one direction or the other: five seconds sat 750 ms above the supervisor's
  own escalation ceiling, so it measured the runner's load whenever SIGTERM was
  actually ignored; fifteen sat past the fixture's ten-second sleep, so it ruled
  out nothing at all; and three seconds of margin sat near enough to a Windows
  process start — Defender reads a fresh binary before it runs — for one Windows
  job to fail on it and the next to pass. Lengthening the sleep is what makes 24
  seconds sit in a real gap rather than a narrow one, and it is free precisely
  because the sleep is gated: nothing runs it but the two executions that kill it
  inside a second.

  The tests that need the fixture to sleep now strip `SESSION_BLOCK` from the
  environment `Open` freezes, and set it in the host on purpose first, so the
  removal is a claim the suite can fail rather than a precaution nobody
  exercises. An inherited one would otherwise have made the ungated half of
  `TestSessionBlocksOnlyWhenAsked` sleep and fail for a reason nothing in its
  output mentions.
- **Coverage renders even when the suite is red.** `mise run cover` and
  `mise run cover-integration` tolerate a failing suite and say so, instead of
  stopping before the rendering: `go test` writes the profile whether or not the
  tests passed, and a red suite is exactly when somebody wants to see what ran.
  A `;` between the commands would not have been enough — mise runs a task's
  script under `set -e` — so the first command carries an explicit `|| echo`
  that prints "the profile below is of the run that failed" to stderr. Nothing
  is loosened where it matters: `mise run test` and CI's `platform-tests` are
  where a failing suite fails the build, and the `coverage` job carries no
  verdict at all. What still fails these tasks is a profile that could not be
  rendered.
- **`internal/snapshot`'s benchmarks report a number by default.** Both
  skipped unless `GO_MUTANTS_BENCH_ROOT` named a tree, which meant the only two
  benchmarks in the repository measured nothing on every run nobody had
  exported a path for — every run. They now fall back to a private copy of
  `fixtures/families`. The variable still points them at a real checkout, which
  is what the parallel copy was tuned against.
- **`-test.timeout` in a session target's `Args` is refused by the session, and
  says so differently.** The session owns both timeout layers — the supervisor
  kills the process tree at `Timeout`, the binary gets `-test.timeout` at twice
  that so the two can never race — so a target supplying its own switched the
  in-process half off while the API still claimed the budget it was given. That
  was already refused, but four layers down, by the execution phase, as
  `GOM7511: the mutant … target overrides -test.timeout, which is reserved by
  the process supervisor`: a scratch directory and a launch decision after the
  request was already known to be impossible, with a code and a sentence about
  machinery the caller never asked for. `Session.Exec` and `Session.Probe` now
  refuse it where they refuse `-test.fuzzcachedir` and `-test.fuzzworker`, as a
  `*ReservedError` reading `gomutants: session exec: -test.timeout is reserved
  by the session's process supervisor`. The old text matched no test in this
  repository and none in the consumer.
- **A failed command now prints what it printed and what it was.** A test
  binary that would not compile arrived on standard error as a single line —
  `error GOM7505: the test binary for example.com/m/pkg could not be built:
  exited with status 2` — with the compiler's diagnostics, the only evidence of
  *why*, discarded on the way up. That code's own documentation reads the
  failure as a go-mutants bug in the instrumented rewrite, so the line was a bug
  report with the bug removed. The cause was narrow: the renderer asked
  internal/engine for a retained output, and this error is internal/execute's.

  It now asks the error itself. `engine.Error`, `execute.Error` and
  `validate.Error` join `gocmd.Error` and `runner.Error` in answering
  `RetainedOutput()` and `Command()`; the renderer walks the whole cause tree —
  branches of a joined error included — for the outermost error carrying
  either, and prints them under the message: the argument vector as `command:`,
  the working directory as `dir:`, and then the output tail exactly as before.
  Outermost wins because the outer error is the one that decided what a terminal
  should see, trimming a fifty-line tail where the runner retains a megabyte.
  An element of the command is quoted only when it contains whitespace or a
  quote, and by hand rather than through `strconv.Quote`, because the one
  platform whose paths contain spaces is the one where Go's quoting doubles
  every separator — and this line exists to be pasted.

  Every constructor that judges a command now names it — `engine.check`,
  `execute.commandFailure`, `validate.buildSnapshot`, the two errored mutant
  outcomes, and the interruption paths, since what was still running is the
  first thing anybody asks of a Ctrl-C — reusing the invocation internal/runner
  already attached rather than describing the same command twice, so what an
  error says ran and what the recording says ran cannot disagree. A Ctrl-C is
  carried the whole way: the attempt the signal cut off names its binary, and
  the error the execution phase returns — the one the command line prints —
  names it too, because a report keeps the number of attempts rather than the
  attempts, so this is the last layer that can still answer. An invocation
  carries no environment, so a mutant's `command:` line is the binary as it
  would run *unactivated*: a real command somebody can paste, with reproducing
  the mutant itself left to `explain`.

  Two consequences are worth stating. `validate.Error.Error()` no longer appends
  the compiler's output to its own text: it would now be printed twice, and
  folded into the message it reached the renderer as continuation lines and came
  back with a `GOM7420` in front of each, go-mutants claiming the compiler's
  words as diagnostics of its own. And the coverage build that fails and falls
  back to a plain one now *keeps* the whole failure, the compiler's output
  included, beside the one-line warning it publishes. Nothing surfaces it yet:
  the console still prints exactly the single line it printed before — a run
  that is about to succeed anyway should not dump a compiler blob at the user —
  and the changes that follow are what put the kept copy in front of a reader,
  in the report and in a recording. What has stopped happening is the
  discarding, at the one moment those diagnostics exist. No message text
  changed, so a line anybody greps for still reads exactly as it did.
- **`Mutant.Probed` now implies `Mutant.Accepted`, and `ProbeResult.Infected`
  no longer names a rejected mutant.** The two validations are independent
  passes over two trees, and the probe rewrite at a site is a different edit
  from the mutation there — often a smaller one — so a site whose probe compiles
  while its mutant does not was reported as `Probed: true` on a mutant the
  mutant tree had rejected. Nothing will ever execute that mutant, so the field
  was a statement about a run that cannot happen, while `Probed` is read as a
  fact about the executions a consumer may *skip*. A consumer keeping a probe
  status per mutant would have carried one for a mutant that was never a
  candidate.

  Tightening the field alone would have broken the other half of the same
  contract. The probe tree is instrumented from the whole catalogue and its
  runtime never learns the mutant tree's verdict, so such a site is still
  reached and still *records*: the log names an index whose mutant now reports
  `Probed: false`, while `Infected` is documented as naming only probed mutants.
  `Session.Probe` therefore filters those indices out before returning, keeping
  the order, keeping nil as nil and the empty set as the empty set. The one
  observable difference is that a rejected mutant no longer appears in a
  measurement — the same mutant `Session.Exec` already refused to run.
- **A snapshot now carries the modification times of the tree it copied.**
  Every file and directory landed stamped "now", which is not what the go
  command expects of a tree it is asked to build. cmd/go caches a package
  directory's index only when the directory has been still for a couple of
  seconds, so a freshly stamped snapshot is re-indexed by every `go list` and
  every load that follows it — the tree pays for looking new rather than for
  being different. The digest is taken from the bytes, so nothing about a
  snapshot's identity depends on this; only how much work the toolchain repeats
  does. Directory times are set after the files inside them, deepest first,
  since writing a file into a directory updates it. Measured on a consumer's
  scoped verification, discovery and the two preparation builds fell from a
  combined 3.01s to 2.39s on one pair of runs and 2.42s to 2.33s on the next.
- **`Prepare` overlaps its independent phases, and `PrepareEvent` now says
  so.** The main and probe binaries do not read each other's output, so
  compiling one after the other spent wall time on an order neither of them
  needed. Events are still emitted synchronously, callbacks are still
  serialized, and every phase still starts before it finishes — but two phases
  may now be open at once, so a consumer that assumed a start always followed
  the previous phase's finish must read the phase rather than the order. The
  prepared trees, digests and catalogue are unchanged.
- **Preparation builds its reusable binaries from source overlays and copies
  the frozen tree in parallel.** These are costs a first verification pays
  before it can say anything at all. The binaries were compiled from separately
  materialized trees when a source overlay already describes the same program
  to the go command, and the deterministic tree copy walked one file at a time
  while the other cores sat idle. The copy fans out over `GOMAXPROCS` workers
  and writes each result into its own slot, so the manifest order and the
  digests it produces remain exactly what a serial copy produced. On the
  repository this was measured against, the snapshot copy fell from roughly
  22-24 ms to 8-10 ms.
- A snapshot's copy of the module now lives in `tree` inside the directory
  go-mutants creates for it, rather than at the top of it. The ownership files
  described above need somewhere to live, and it cannot be beside the sources:
  `snapshot.Redigest` deliberately applies no exclusions, so a marker there
  would be reported as workspace drift by every run that checks, and the probe
  tree — a snapshot of a snapshot — would copy the marker and hash a manifest
  that no longer described the tree it came from. The visible consequence is
  that paths printed for diagnostics now end in `…/go-mutants-snap-XXXX/tree`.
- **The snapshot directory of a given module now has the same name on every
  run**, `go-mutants-snap-` followed by sixteen hex characters of the SHA-256 of
  the absolute source root, rather than a fresh `os.MkdirTemp` name. This is a
  performance change and a large one. The go command hashes each package's
  absolute directory into its compile action id unless `-trimpath` is passed,
  and go-mutants will not pass it — `-trimpath` changes the program under test,
  and a test that reads `runtime.Caller` paths would behave differently in the
  snapshot than in the tree the user is editing — so a snapshot at a fresh path
  shared nothing with the previous run's build cache. Every run recompiled the
  whole module from scratch, and the cache filled with one copy of the project's
  objects per run.

  Only the path is reused, never the bytes. A directory found under the name is
  swept exactly as any other leftover is, and the tree is copied into it fresh,
  so a run never inherits the half-instrumented tree of a run that died before
  it. What the sweep will not collect is not waited for either: a directory
  locked by a concurrent run of the same module, one a `KeepTemp` run preserved,
  or an unowned young one leaves this run with a random name instead, which
  costs it the cache hits and nothing else. Two runs of one module therefore end
  with one stable directory and one random one, each holding its own lock, and
  never with two runs in one tree.
- A run and an `Open` now remove the temporary directories abandoned by earlier
  go-mutants runs before they create their own, which is a deletion neither of
  them used to perform. It is confined to the directories go-mutants itself
  creates, under its own name prefixes, and to those whose owner is provably
  gone. When one cannot be removed, `run` publishes a `GOM4044` warning and
  carries on: failing to collect somebody else's leftovers is not a reason to
  refuse to measure anything.
- **The engine's own integration suite now runs against disposable copies of
  the corpus, and in parallel.** Every run was pointed straight at
  `fixtures/<name>` in this repository, which cost two things at once. The
  project artefacts had to be turned off — `report.formats` was emptied in the
  suite's options constructor — because a default `json,html` writes
  `reports/mutation/` into the workspace, so the one code path that writes into
  a user's own tree was exercised nowhere in the package that owns it; and
  proving that a run leaves nothing behind meant redirecting `TMPDIR`, `TMP` and
  `TEMP`, which are process-wide, so no test in the file could run beside
  another. The two end-to-end tests that drive the built command line did write
  the artefacts, into `fixtures/killable/`, where `.gitignore` hid them.

  Each run now works on a copy made through `internal/testkit` and is given a
  temporary parent, a history root and a cache root of its own, so the default
  formats are back on and the artefacts land where a user's would.
  `TestRunsNeverWriteIntoTheCorpus` is the guard — the copy holds
  `reports/mutation/mutation.json` and it validates, no corpus module grew a
  report directory while the suite ran, and `git status --porcelain -- fixtures`
  is empty — and CI runs a corpus check as a step of its own after the
  integration suite, because a test that ran inside a dirty tree is the test
  least able to notice. That step reads `--ignored`, since the files a stray run
  leaves are exactly the ones `.gitignore` covers, and it runs after a failed
  suite as well as a green one. The test compares the corpus before the run with
  the corpus after it rather than demanding an empty one: a `reports/` somebody
  left in a fixture a fortnight ago is not this run's doing, and a guard that
  blamed it is a guard people learn to delete.
  `TestRunLeavesNothingUnderItsTempDirectory` is the leftover assertion, made
  against `Options.TempDirectory` rather than against a redirected global.

  The CI step caught something on its first run, in another package.
  internal/cli's `--explain` tests started a real `run --explain` inside
  `fixtures/rejectable` — `run` writes its artefacts into the directory it is
  started in — and had been leaving a `reports/` there on every machine that
  ever ran the suite, seen by nobody because `.gitignore` covers it. Those
  tests, and the listing tests that shared the arrangement, now `t.Chdir` into a
  copy. The rest of internal/cli's harness is a later migration's.

  Everything but four tests then took `t.Parallel()`, and the suite went from
  around 120s to around 30s — around 40s at `-parallel 2`. The four that stay
  serial redirect the environment for the whole process, and each says why:
  `TestTempDirectoryIsWhereTheRunSnapshotsAndSweeps` is about the difference
  between the named temporary parent and the operating system's own, and the
  three `--changed` tests script a git repository whose configuration the run's
  own `git` reads — internal/engine resolves a diff through internal/gitdiff
  without naming an environment, so the git it drives is this process's. Those
  repositories are now built with the harness's git helpers, which pin the
  identity, the branch and the dates and point the configuration files at paths
  that do not exist, rather than with the `-c commit.gpgsign=false` the suite
  used to pass — which is precisely what the signing wrappers some developers
  install refuse, and which made those tests unrunnable on such a machine.
  `testkit.GitInit` gives the branch it makes no upstream, which costs these
  tests nothing because each names the commit it diffs against; the suites that
  exercise a bare `--changed`, and so resolve `@{upstream}`, will need one.
  Ageing a tree that is a repository is narrowed here to the files just written,
  for a related reason: `testkit.AgeTree` walks everything under the root, `.git`
  included, and rewriting the timestamps git keeps its stat cache on is no way to
  ask git a question. Teaching it to skip `.git` would be the general fix.

### Fixed

- The test harness no longer leaves a coverage directory in the system
  temporary directory once per test binary process. Every suite that runs
  through `testkit.Helper` created a private `go-mutants-helper-cover-*` root
  before its first test and removed it in a deferred function afterwards — and
  a deferred function is exactly what a process that is killed does not run.
  `go test -timeout`, a Ctrl-C, and above all a mutation run, which kills the
  mutants that hang and starts a test binary per mutant to find them, each left
  one behind for good; 11,842 of them were counted in one machine's `/tmp`.

  The root is now created only when there is coverage to keep apart, which is
  the only thing it was ever for. `go test -cover` exports `GOCOVERDIR` to the
  test binary and a plain `go test` does not, so a run without it makes no
  directory at all: nothing is instrumented, no coverage exit hook fires, and
  the shared directory a helper's private one existed to stay away from does
  not exist either. Under `-cover` nothing changes — each helper still writes
  into a directory of its own named by its pid, and the root still goes when
  the suite does. A directory that is never created cannot be left behind.
- Parallel tests under the keep policy no longer fail on the test harness's own
  bookkeeping. Every test in one binary files its scratch directory under the
  same `<kept root>/<package>` directory, and a passing test's cleanup removed
  its own directory and then that package directory as soon as it was empty — so
  one test's removal could land between another test's `MkdirAll` of the parent
  and the `Mkdir` of its own directory, and the second syscall failed with
  `ENOENT` in a test that had nothing to do with keeping. It is what turned an
  ubuntu job red: the `mkdir` of a kept directory under the run's
  `go-mutants-kept/testkit` came back `no such file or directory`.

  The fix is that nothing removes a package directory any more. A lock would
  have been the wrong answer, because the two racing sides need not be in one
  process: `go test ./...` runs the root package's test binary and
  `cmd/go-mutants`' beside each other, both are named `go-mutants` after the
  binary, both file under `<kept root>/go-mutants`, and one process's removal
  means nothing to the other's mutex. An empty package directory is the cheaper
  end of that trade — `actions/upload-artifact` puts files in an artifact and
  skips empty directories, so a green job still uploads nothing, and `mise run
  test-clean` empties the whole kept root regardless. A creation that loses to a
  deleter nothing here controls is still retried once.
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
