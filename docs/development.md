<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Development

How the developer infrastructure of this repository fits together: what a test
gets from the harness, which tier it belongs in, where it writes, what a failure
leaves behind, and how to read the account a run keeps of itself.

[CONTRIBUTING.md](../CONTRIBUTING.md) is the shorter document — setup, the gates
to run before submitting, the rules a change has to keep. This one is the
reference underneath it. The decisions that produced any of it are recorded in
[the ADRs](adr/README.md); the architecture of the tool itself is
[docs/architecture.md](architecture.md).

Everything here is checked. `internal/testkit/devdocs_test.go` fails when this
page stops naming an environment variable the harness reads, stops naming a
`mise` task, or documents a command that is not one — so a fact that has gone
stale is a red build rather than a reader's wasted afternoon.

## 1. The harness

`internal/testkit` is the one test harness every suite in this repository
shares. It finds the module, copies a fixture, hands a test a hermetic
environment, locates the toolchain, runs a child process, and asserts on what
came back. `internal/testkit/mutantkit` is the half that knows what go-mutants
*is* — snapshots, catalogues, mutants, run reports, the toolchain that builds
them — and it is a separate package for a reason the import gate below makes
concrete.

Every helper in it existed three or four times before it lived there, and the
copies disagreed. The package doc names each failure that taught a rule; read
`internal/testkit/doc.go` before adding a helper.

### What a test gets

`testkit.Env(t)` moves the process into a hermetic environment and returns it as
a value. It calls `t.Setenv`, so a test that calls it may not call `t.Parallel`;
a test that only needs the value form — to hand to a child — wants
`testkit.Compose(t, scratch)`, which applies the same rows without touching the
process.

The policy is stated once, in `Env`'s doc comment, and this is it:

| What | What it becomes |
| --- | --- |
| `TMPDIR`, `TMP`, `TEMP` | the test's own scratch directory |
| `HOME`, `home`, `USERPROFILE` | a private home |
| `XDG_CACHE_HOME`, `LocalAppData` | the cache below it, telemetry off |
| `GOCACHE` | the shared test-owned build cache |
| `GOENV`, `GOPATH`, `GOMODCACHE` | the machine's real ones, pinned |
| `GOWORK`, `GOPROXY`, `GOSUMDB` | off |
| `GOTOOLCHAIN` | `local` |
| `GOFLAGS` | `-mod=readonly` |
| every `GO_MUTANTS_*` | removed |
| `GIT_CONFIG_GLOBAL`, `GIT_CONFIG_SYSTEM` | paths that do not exist |
| `GIT_AUTHOR_*`, `GIT_COMMITTER_*` | the fixed identity in `env.go` |
| `GITHUB_STEP_SUMMARY` | blank |
| `PATH` and everything else | inherited |

Three rows are worth reading twice. `GOTOOLCHAIN=local` is why no test can
download a compiler, which is why a fixture whose `go` directive is above the
toolchain in use is a corpus-conformance failure rather than a slow test.
Stripping every `GO_MUTANTS_*` variable is why a developer with
`GO_MUTANTS_ACTIVE` exported in their shell does not run a mutant as the
baseline — and it is why the harness's own variables are named the way they are:
`GO_MUTANTS_TEST_*` is read *before* the stripping happens, and
`TESTKIT_HELPER_COVERDIR_ROOT` wears a different prefix precisely because it has
to survive it. Inheriting is the default for everything unnamed, because a
child needs a `PATH`, a shell and a terminal, and an allow-list is a list that
grows an entry every time a platform is added.

The hermetic value wins on conflict. A developer who exports `GOFLAGS` gets
`-mod=readonly`; a test that needs something else passes it on the command line
or names it in `testkit.Inherit`. `testkit.KeepHome()` is the option for a test
whose subject *is* the real home directory, and asking to inherit a home
variable without it is a fatal error rather than a silent read of the
developer's own `~`.

The returned `*testkit.Environment` names the four directories one test owns —
`Scratch`, `Home`, `Cache`, `GoCache` — and `Vars()` returns the environment as
a copy, for a child started with an explicit one.

### The test-owned build cache

The suites drive thousands of child `go build`, `go test -c` and `go list`
commands against fixtures, synthesized modules and instrumented snapshots, each
living at an absolute path that exists for a single run. Every entry is
therefore keyed on a path nothing will ever look up again. That took one
developer's `~/.cache/go-build` to 14 GB and filled a disk twice, which is why
`Env` and `Compose` point `GOCACHE` somewhere else.

`GO_MUTANTS_TEST_GOCACHE` names that directory when it holds an absolute path,
and `<os.UserCacheDir()>/go-mutants-test/go-build` is the default. CI points it
at the runner's own temporary area, which dies with the runner.

Ownership is a file rather than a promise. `GO_MUTANTS_TEST_GOCACHE=$HOME` is an
absolute path like any other, so whatever resolves the cache stamps it with
`.go-mutants-testcache`, and the collector removes nothing that does not carry
one. `internal/devtools/testcache` is that collector:

```console
go run ./internal/devtools/testcache path [--kept] [--marker]
go run ./internal/devtools/testcache status
go run ./internal/devtools/testcache clean
go run ./internal/devtools/testcache trim --budget 4GiB
go run ./internal/devtools/testcache exec --budget 4GiB -- \
    go test -tags integration ./...
```

`exec` is the form the tasks use. It exports `GOCACHE` and
`GO_MUTANTS_TEST_GOCACHE` into the child — the first for every `go` command
below it, the second so that a test composing an environment of its own resolves
the same directory — prints the size and the growth to stderr when the child
ends, empties the cache if the run left it over the budget, and exits with the
child's own status, so a wrapped gate is exactly as strict as an unwrapped one.

Through `mise`, the two a developer types are:

```console
mise run test-cache-status
mise run test-clean
```

`test-cache-status` prints both directories the harness owns outside a temporary
directory, and how much each holds:

```text
build cache:  /home/dev/.cache/go-mutants-test/go-build
              1.6 GiB (1770573144 bytes) in 129796 files
kept scratch: /home/dev/.cache/go-mutants-test/kept
              does not exist (0 bytes in 0 files)
```

`test-clean` empties both. It exits non-zero for exactly one reason — it was
pointed at a directory carrying no marker, and refused — and never because a
file was busy: an unlinkable file is retried once, reported, and the task still
succeeds. Both directories live under one parent, so a developer who wants
everything this harness ever wrote can delete `<cache root>/go-mutants-test`.

### Reaching the toolchain

`testkit.GoBinary(t)` and `testkit.GitBinary(t)` return the path of the tool, and
either skip or fail the test when there is none. Which of the two is the policy
`GO_MUTANTS_TEST_REQUIRE_TOOLS` states, and the split matters in both
directions: a developer running the unit tier on a machine without a toolchain
should see those tests skip, and a CI job without one is a broken job rather
than a smaller suite. Both workflows set it at the workflow level, so a runner
that lost its `go` fails the job instead of retiring the suites that need one and
reporting green — `internal/discover` alone is seventy-four tests that would go
quiet.

`testkit.RequireTools()` reads the variable, falling back to the value it had
before any test ran — because `Env` strips the `GO_MUTANTS_` prefix, and without
the fallback a test that redirected its environment and then reached for a tool
would have turned CI's requirement off.

### The two-second rule

`cmd/go` indexes a package directory only when every file in it is at least two
seconds old. A tree written a moment ago therefore behaves differently from a
tree a user has: the first `go list` over it indexes nothing and the second one
might, which makes any assertion about cache entries or misses depend on how long
the copy took.

So every tree the harness hands out is aged. `testkit.TreeAge` is an hour — past
that cutoff, past the one-second timestamp granularity some filesystems still
have, and past any clock skew between a container and its host — and
`testkit.AgeTree(t, root)` applies it, deepest-first so that ageing a directory
does not undo itself. `testkit.Copy` already calls it; a test that builds a tree
by hand and then measures the go command has to.

### The import gate

Two rules, each enforced by a test in `internal/testkit`:

- **Production code may never import the harness.** A production package that
  imported `testkit` would link `testing` into `go-mutants`: flag
  registrations, the testing package's init work, and a public API that can fail
  a test that does not exist. `TestProductionCodeDoesNotImportTestkit` parses
  every production file in the tree and says so. The one exception is
  `internal/testsupport/cache.go`, a forwarder on its way out, named as a *file*
  so that the permission cannot silently extend to a package somebody adds
  beside it.
- **The harness may import nothing from this module.** A harness compiled from
  the code under test is a harness whose bug and the bug it is hiding are the
  same bug. `TestTheHarnessImportsNothingFromThisModule` names the next
  third-party import rather than letting it in; outside the standard library the
  list is `github.com/google/go-cmp` alone, which is there because
  `testkit.Golden` prints a diff.

`internal/testkit/mutantkit` is where the second rule sends a helper that needs
go-mutants' own types. It imports the engine packages freely and is imported
only from *external* test packages (`package foo_test`), because an engine
package importing it back would be a cycle and a production package linking
`testing`.

## 2. Tiers and cost

There are two tiers, separated by a build tag.

- **The unit tier** is `go test ./...`: everything that needs a compiler and
  nothing else. It is what a developer runs on every save and what CI runs on
  three operating systems.
- **The integration tier** is `go test -tags integration ./...`: every suite
  that drives a real toolchain — a `go build`, a `go test -c`, a mutation run —
  and it costs tens of minutes.

The split was not free to get wrong. Forty toolchain-driving tests sat in the
unit tier until the root suite was tiered, and nothing about that *failed*: the
tests passed, on every platform, and only the clock said anything. The root
package alone went from 28 seconds to 0.2.

### The allowlist ratchet

`internal/testkit/tiers_test.go` is what keeps the tiers apart. One test asks the
root package's unit-tier binary which tests it contains; the other scans every
`_test.go` in the tree for a call that starts a `go` or `git` command outside the
tag — `gomutants.Open(`, `gocmd.Locate(`, `exec.LookPath("go")`, and the harness's
own `GoBinary(`, `GitBinary(`, `Toolchain(`, matched unqualified so that the
harness's own package is not exempt from its own rule.

Every other file that starts one must carry `//go:build integration`, or be
written down — one path per line with a paragraph saying why — in
`internal/testkit/testdata/unit-toolchain-allowlist.txt`. What is left in that
ledger is `internal/discover`, which loads fixtures with `go/packages` by
construction; the three instrumentation suites that compile what they generate;
and the harness's own four files, which are the toolchain policy's tests.

There is one exemption, and it is granted by construction rather than by the
ledger: **a file that constructs a scripted `go` is supplying the toolchain
rather than reaching for one.** That is what took `internal/gocmd` out of the
list entirely — see [the tiers exemption](#the-tiers-exemption) in section 6 for
how narrow it is and why a file may not do both.

It is a ledger, not a configuration. A stale entry is reported as loudly as an
offender, so the list shrinks when a suite is tagged or scripted, and grows only
when somebody writes a path down and says why underneath it.

### The tasks

```console
mise run test
mise run test-race
mise run test-integration
mise run test-integration-race
mise run cover
mise run cover-integration
mise run bench
mise run test-cost
mise run test-cost-integration
```

`mise.toml` carries the reasoning behind each one; the short version:

- `test` is the unit tier, deliberately *not* wrapped in `testcache exec`,
  because it compiles go-mutants itself and that belongs in the developer's own
  build cache like every other `go test` they run. The suites' *child* `go`
  commands still go to the test cache, because the harness points `GOCACHE`
  there in every environment it composes.
- `test-race` is the unit tier under `-race`, ubuntu-only in CI because `-race`
  links the C runtime and a data race is a property of the Go code rather than
  of the platform.
- `test-integration` and `test-integration-race` go through `testcache exec
  --budget 4GiB`. The second is nightly: instrumentation costs a factor of two
  to ten, and a pull request should not wait for it.
- `cover` and `cover-integration` are a signal and never a gate — there is no
  threshold in either, because a coverage percentage measures which lines ran
  rather than which behaviour is checked, and the gate on whether the tests
  *catch* anything is `mise run dogfood`. `cover-integration` passes
  `-coverpkg=./...`, because the integration tier's whole shape is one package's
  suite driving five others.
- `bench` names its three packages rather than `./...`, writes `bench.txt`, and
  is compared with `benchstat` against yesterday's.

The alarms are budgets rather than targets: 15m for the unit tier, 30m under
`-race`, 40m for the integration tier and 90m for it under `-race`. They exist
so that a suite which hangs is reported as a suite that hung, with the package
and a stack, rather than as a job the runner cancelled with nothing to say.

### What `testcost` prints

`test-cost` and `test-cost-integration` run exactly the commands `test` and
`test-integration` do, through `internal/devtools/testcost`, which reads the
`go test -json` stream and prints one Markdown table sorted slowest first — the
columns, in order, are:

```text
| package | tests | skipped | elapsed |
| --- | ---: | ---: | ---: |
```

one row per package, a bolded totals row at the bottom, and every skipped test
named underneath. The numeric columns are right-aligned because the one place
this is read is a GitHub step summary, which renders the Markdown; a plain-text
table would be rendered there as a paragraph with the columns run together.

It exists because both numbers that decide what a suite costs are invisible in
an ordinary log: `ok <pkg> <elapsed>` is printed once per package and
interleaved with forty others, and a skip prints nothing at all without `-v` —
so a test that quietly stopped running reads exactly like a test that passed.

`testcost` *runs* the command rather than being piped its output, and that is the
difference between a gate and a formatter: a pipeline reports its last command's
status, so a report on the end of one would turn a red suite green, and a `go`
that fell over before it started testing writes plain text on stderr and nothing
on stdout. Both verdicts are combined — the task fails if `go test` failed *or*
if the stream carried a failure. `-count=1` is passed because the tool measures,
and a cached package emits no test events at all.

`--no-skips` exists and is deliberately not passed, here or in CI: every
remaining `t.Skip` in this repository is a platform-capability guard or a fuzz
body rejecting an input, at least one fires on each of the three operating
systems, and the skips the flag was wanted for — a missing tool — are already
fatal in CI through `GO_MUTANTS_TEST_REQUIRE_TOOLS`. `--verbose` replays every
test's output rather than only a failure's.

### The CI jobs

`.github/workflows/ci.yml`, on every push and pull request:

| Job | What it runs |
| --- | --- |
| `quality` | `mise run check`, the corpus gate, `committed` over the range |
| `platform-tests` | `mise run test-cost` and `test-cost-integration` on ubuntu, windows and macos |
| `race` | `mise run test-race`, ubuntu |
| `coverage` | `mise run cover-integration`, ubuntu, not on pull requests |
| `dogfood` | `mise run dogfood` |
| `artifacts` | `mise run package`, and the snapshot archives |

`.github/workflows/nightly.yml`, at 02:17 UTC: `fuzz` (five minutes per target),
`property` (the `rapid` suites deepened and repeated with random seeds),
`race-integration` (`mise run test-integration-race`) and `bench`.

Both workflows set `GO_MUTANTS_TEST_REQUIRE_TOOLS: "1"` at the workflow level, so
a missing `go` or `git` fails a job rather than silently narrowing it to the
tests that need no toolchain. Every job that runs a suite then publishes
`GO_MUTANTS_TEST_GOCACHE`, `GO_MUTANTS_TEST_KEEP` and
`GO_MUTANTS_TEST_KEEP_DIR` into `$GITHUB_ENV`, pointing the first and third at
the runner's own temporary directory — which dies with the runner and is never
restored by a cache action, so a job can never inherit yesterday's gigabytes.
(`$GITHUB_ENV` rather than a job-level `env:`, because the `runner` context is
not available there.)

`dogfood` and `bench` set only the cache, and the first of those is the
interesting exception: **a mutation run over this repository makes its own tests
fail on purpose, thousands of times**, and every one of those test binaries
links the harness. Keeping on failure would file a directory per killed mutant,
which is a disk full of evidence of the run working exactly as intended.

### Typical timings

Measured on one warm Linux machine, so read them as orders of magnitude rather
than as numbers to compare a run against:

```console
go test -count=1 ./internal/testkit/... ./internal/cli ./trace
```

is on the order of ten seconds. `mise.toml` describes the whole unit tier as
seconds on a warm machine and a couple of minutes cold, which is what its 15m
alarm is sized around; the integration tier is tens of minutes, of which
`internal/engine` alone is eight to ten of real toolchain work; and
`.go-mutants.toml` sizes `mise run dogfood` at one and a half to two and a half
minutes at `--jobs 4` against a warm build cache, and three to four against a
cold one — of which about a minute is three mutants that never return, waiting
out a per-mutant timeout that is itself derived from the baseline and so moves
with the machine.
`.go-mutants.toml` records what `mise run dogfood` has been measured at, which
for the current eleven-package scope is 6m13s–6m30s at `--jobs 4` against a warm
build cache on a quiet machine, 8m42s–9m02s on the same machine under other
work, and 9m31s cold before the engine sized the timeout on the runs after the
first — more than half of that figure being four mutants that never return
waiting out a timeout sized on the compiling first baseline run. After that
change, GitHub's ubuntu runner ran the same scope cold in about 4m45s (a
5m22s job), with the timeout at its 10 s floor.
Anything far from those shapes is worth a `mise run test-cost` before it is
worth a workaround.

## 3. Where tests write, and who collects it

A test that fails takes its evidence with it: the fixture copy, the instrumented
tree, the composed environment's scratch and the commands it ran are all under a
`t.TempDir` that the testing package removes the moment the assertion has been
printed. Reproducing it means running it again, which is not available on a
runner nobody can log into and is expensive everywhere else.

So `testkit.Scratch(t)` replaces `t.TempDir` in every constructor that hands a
test a tree to work in, and what happens to that tree is a policy.

### The keep policy

| Variable | What it does |
| --- | --- |
| `GO_MUTANTS_TEST_KEEP` | unset, `0`, `false`, `no`, `n`, `off`, `disabled`: remove it, exactly as before. `1`, `true`, `yes`, `y`, `on`, `enabled`, `failed`, `on-failure`: keep it when the test failed. `always`: keep it whatever happened. Anything else is refused, with the spellings named — a typo in a CI job must not read as "off" |
| `GO_MUTANTS_TEST_KEEP_DIR` | the root a kept directory is filed under; `<os.UserCacheDir()>/go-mutants-test/kept` by default |
| `GO_MUTANTS_TEST_VERBOSE` | `1` prints a `DumpFiles` dump on a test that *passed* |
| `GO_MUTANTS_TEST_FORCE_FAIL` | fail the test of this exact name on purpose, which is how to see what a failure leaves |

Locally the policy is off, because keeping unconditionally filled a disk twice.
CI sets it to `1` for every job that runs a suite and uploads the root as a
`kept-scratch-*` artifact, kept for fourteen days, so a red build arrives with
the evidence attached.

`GO_MUTANTS_TEST_KEEP_DIR` is also how to put the root somewhere fast: pointing
it at a tmpfs makes every kept directory a memory write and every removal free,
at the price of losing the evidence with the reboot — the right trade for a
keep-everything run and the wrong one for a nightly job somebody reads in the
morning.

### What a kept directory holds

- `KEPT.txt` — which test filed it, the outcome, the policy in force, the
  scratch path, the fixture, the build cache, this test's other kept
  directories, and every child the test ran through `testkit.Exec`. The argv
  list is bounded at two hundred and says how many it dropped.
- `dump/` — whatever `testkit.DumpFiles` was pointed at, whole, one numbered
  directory per call, so two dumps in one test do not overwrite each other.
- `trace.jsonl` — for a test that took its recorder from `mutantkit.Trace` or
  `mutantkit.TraceSink`, in the encoding `go-mutants trace validate` reads.

`testkit.Scratch` called twice returns two directories: a test that copies two
fixtures wants two trees, both are kept, and each one's account names the others.
`testkit.KeptDir(t)` is the first of them, which is where dumps and recordings
go, and it is the empty string when nothing is being kept.

`testkit.PackageScratch(name)` is the same thing for a directory a `TestMain`
owns, for a package that prepares one expensive fixture and shares it. It has no
`testing.TB` to hang a cleanup on, so the caller releases it with the status
`m.Run` returned, and nothing enforces that.

### Retention

**Nothing reclaims a kept directory but `mise run test-clean`.** Re-running a
test files a *new* directory beside the old one — the suffix is random, and two
runs of one test are two pieces of evidence — so `always` grows for as long as it
is left on. `testcache trim`, which enforces the budget on the build cache, never
looks at this root; a directory here is somebody's evidence and nothing decides
on their behalf that it is stale.

**Empty package directories remain too.** `<kept root>/<package>/` is made when
the first test of a binary files something and nothing removes it afterwards —
not even a green run that removed everything in it. Removing it the moment it
went empty is what raced two test binaries filing under one short name against
each other, with an `ENOENT` in whichever was between making the parent and
making its own directory; the fix was to stop removing it rather than to lock
it, because the two racing sides need not be in one process and no mutex reaches
across them. An empty directory is not evidence, and a green job still uploads
nothing: `actions/upload-artifact` puts *files* in an artifact and skips empty
directories.

### The corpus is an input

Every integration suite runs against a disposable copy of a fixture module,
because a run writes `reports/mutation/` into the tree it is pointed at and
several tests read the same module at once. CI gates on it, on every platform,
after a green suite and after a failed one:

```console
git status --porcelain --ignored -- fixtures
```

`--ignored` is what makes it a gate rather than a formality: the files a stray
run leaves are exactly the ones `.gitignore` covers. The runner starts from a
fresh checkout, which is what makes the strict reading affordable there and not
on a developer's machine, where the same directory may be a fortnight old.
`internal/testkit`'s `TestCorpusConformance` says the same thing locally, without
the strict reading.

### Manual runs

The harness moves `TMPDIR`, `HOME` and the cache directory for *tests*. Nothing
moves them for a command somebody types. A hand-run `go-mutants run`, a `go test`
outside the tasks, or an agent driving either of them writes into the real
`HOME`, the real `TMPDIR` and the developer's own `~/.cache/go-build` — which is
the one thing the whole build-cache arrangement exists to avoid.

So a manual run that is not the one being debugged should redirect them itself,
by exporting `TMPDIR`, `HOME` and `XDG_CACHE_HOME` at somewhere disposable
before it starts, and by pointing `GO_MUTANTS_TEST_KEEP_DIR` at the same place.
`mise run test-cache-status` is how to find out afterwards what was written
anyway, and `mise run test-clean` is how to give it back.

## 4. Diagnosing a failing test

The keep policy is designed to be turned on for one test, and
`GO_MUTANTS_TEST_FORCE_FAIL` is how to see what it does without waiting for a
real failure:

```console
GO_MUTANTS_TEST_KEEP=1 GO_MUTANTS_TEST_FORCE_FAIL=TestSomething \
    go test -count=1 -tags integration ./internal/engine -run TestSomething -v
```

The name is matched exactly, and it is one test rather than a pattern. What comes
back looks like this — the `testkit:` lines are what every constructor logs when
it resolves something, and the last line is the directory (long lines are one
line each in reality, wrapped here to fit the page):

```text
=== RUN   TestCopies
    tree_test.go:59: forced failure by GO_MUTANTS_TEST_FORCE_FAIL=TestCopies
    tree_test.go:59: testkit: fixture=/repo/fixtures/simple
        scratch=/kept/testkit/TestCopies-37ff50/simple keep=on-failure
    keptdir.go:233: kept: /kept/testkit/TestCopies-37ff50
--- FAIL: TestCopies (0.00s)
```

Those lines alone answer the first three questions a failure in CI raises —
which fixture, which toolchain, which cache — without anybody having to reproduce
anything.

Then read what was kept, in this order:

1. **`KEPT.txt`.** It is the index of everything else:

   ```text
   This directory was kept because the go-mutants test harness was asked
   to keep it.

     test       TestCopies
     outcome    failed
     policy     on-failure (GO_MUTANTS_TEST_KEEP)
     scratch    /kept/testkit/TestCopies-37ff50
     fixture    /repo/fixtures/simple
     gocache    /home/dev/.cache/go-mutants-test/go-build

   Nothing here is precious: `mise run test-clean` empties the whole kept root.
   ```

   A test that ran children through `testkit.Exec` gets an `Also ran:` section
   with their argument vectors; a test that kept more than one directory gets an
   `Also kept:` section naming the others.

2. **The tail of `trace.jsonl`**, if there is one. Two things to check before
   trusting a recording: that its last line is a `run-end`, and that its
   `events_dropped` is zero. `go-mutants trace summary <path>` reads it, and
   `go-mutants trace validate <path>/trace.jsonl` checks it against the
   published schema.

3. **`dump/`**, which is whatever the failing assertion thought was worth
   copying — usually the instrumented source, sometimes the generated runtime.
   Each file is truncated at `testkit.DumpFileLimit`, so a dump is a diagnostic
   rather than a copy of a build.

4. **The scratch tree itself**, which is the fixture copy exactly as the test
   left it. This is the only way to answer "what did the tree this ran in
   actually look like".

`GO_MUTANTS_TEST_VERBOSE=1` prints the dump for a test that *passed*, which is
the tool for "this assertion is right and I want to see what it is asserting on".

On CI there is nothing to type: the failed job's `kept-scratch-<os>` artifact is
the whole of the above. A green job uploads nothing, and a job that went red
before any test ran uploads nothing either.

## 5. Goldens

`testkit.Golden(t, name, got)` compares against `testdata/<name>` and prints a
unified diff on a mismatch — go-cmp's, because a hand-written diff of two
multi-line documents is either wrong or is go-cmp again. A missing golden is a
failure, never an implicit record.

**There is one `-update` flag in this repository.** It is registered in
`internal/testkit`, in a non-test file, so it is the same flag in every test
binary that links the harness. That is not tidiness: `internal/report` and
`internal/instrument` each registered their own, and the moment a third package
importing both had registered a second in one binary, package `flag` would have
panicked with `flag redefined: update` before a single test ran.

```console
mise run golden-update
```

It is two commands rather than one, and both name their packages *before*
`-update`:

- `go test ./internal/console ./internal/instrument ./internal/report ./trace -update`
- `go test -tags integration ./internal/engine -update`

The second exists because `internal/engine`'s goldens are run reports of real
runs, so the test that records them is `//go:build integration`-tagged: without
the tag the command compiles a package with no golden test in it, passes, and
rewrites nothing.

The package order is not a style choice. `cmd/go` knows its own test flags and
nothing else; on an unrecognised one it stops parsing and hands the rest of the
command line to the test binary — so `go test -update ./internal/report` runs the
package in the *working directory* and silently leaves every golden alone.

Two tests keep the task honest. `TestGoldenPackagesAreNamedByTheUpdateTask`
parses those lines and fails when a package holding a `testdata/*.golden*` file
is missing from them; `TestGoldenUpdateTaskHandlesTaggedPackages` requires every
integration-only golden package to be reached with the tag.

**A golden regenerated without being read is a golden that records a bug.** That
is why the task exists at all instead of `go test ./... -update`, and it is the
whole of the review obligation: run it, read the diff, then commit.

### Normalising a run report

A run report is mostly facts about the code — which mutants there are, what
happened to each — and that is the part a golden can pin. The rest is facts about
the *run*: the tool's version, the go directive, the toolchain's version line,
`GOOS`/`GOARCH`, the wall clock at both ends, every measured duration, the
scheduler slot each attempt ran in, every absolute path, and the elapsed times
`go test` writes into captured output. A golden that kept them would fail on the
next machine, on the next toolchain, on the other two platforms, and on the
second run of the same day.

`mutantkit.NormalizeRunReport(t, data)` replaces all of it, and
`internal/testkit/mutantkit/report_test.go` carries the ledger of what "all of
it" means: `varyingFields` maps every JSON pointer to the value it becomes, and
`TestNormalizeRunReportFixesOnlyTheVaryingFields` checks both directions — every
listed field moved, and *nothing else did*.

Two things are deliberately not normalised. Mutant ids and workspace digests are
content-addressed: they are derived from the bytes of the program under test, so
they are the same on every machine and they are exactly what proves two runs
measured the same thing.

## 6. Helper processes and the scripted `go`

Two things a test can start that are really this same test binary re-executed: a
helper, which is a child whose *behaviour* is the subject, and the scripted `go`,
which is a child pretending to be the toolchain.

### Helper processes

Some helpers cannot be a program: they need a `*testing.T`, because what they do
is fail. A barrier that has to report "the release file never appeared" after
thirty seconds, a child that asserts on the environment it was handed — those are
tests, and re-executing the test binary with the run pattern is how a test becomes
a subprocess without compiling anything at test time.

`testkit.HelperArgv(testName)` builds that command line. The anchors are the
point of it existing: `-test.run=TestFoo` is a regular expression that also
selects `TestFooAndMore`, and a helper process that ran a second test writes a
second test's output into the stream its parent is parsing. The name is escaped,
because a subtest path with a `+` or a `.` in it is a pattern that matches
something else. Every hand-written copy of that line had the anchors; the next one
would have been the one that did not.

`testkit.TestBinary()` is the absolute path of the running binary, which is the
whole contract: a helper runs in a directory the parent chose, so a relative
`argv[0]` names nothing.

`testkit.Helper(m, variable, program)` is the other shape — the `TestMain` of a
package whose tests need real processes:

```go
func TestMain(m *testing.M) {
    os.Exit(testkit.Helper(m, "GO_MUTANTS_RUNNER_TEST_HELPER", runHelper))
}
```

When the variable is set the process *is* the helper: `program` runs with
`os.Args[1:]` and its return value is the process's status. Otherwise the suite
runs as usual. `testkit.HelperEnabled` is the gate inside a `Test`-shaped helper,
and it treats *any* non-empty value as "you are the helper", including `0` — a
helper that decided the value looked falsy and ran the suite instead would look
exactly like a test that passed.

`testkit.HelperMisuse` is `97`, the status a helper exits with when it cannot set
itself up. It is a status no test in this repository asks a helper to produce, so
a harness failure can never be read as the exit code a test was expecting.

`TESTKIT_HELPER_COVERDIR_ROOT` names the directory each helper carves its own
`GOCOVERDIR` out of, so a coverage-instrumented run does not lose what its
children executed. It is deliberately outside the `GO_MUTANTS_` namespace: `Env`
and `Compose` strip that prefix, and a cover root stripped on the way into a
helper is a helper with nowhere private to write.

Two questions decide this, and they have two different answers.

**Whether a suite publishes a root at all is `testing.CoverMode()`** — the
compiled-in fact that this binary was built with `-cover`, which is `"set"`,
`"count"` or `"atomic"` there and empty otherwise. It is emphatically not
`GOCOVERDIR`: a *test* binary emits through `testing`'s `coverTearDown`, which
reads `-test.gocoverdir` and not the variable, so go-mutants passes the flag —
and since a child no longer inherits a parent's `GOCOVERDIR`, a mutant's test
binary is an instrumented process with no variable to read at all. While
`GOCOVERDIR` was the test, every such binary answered "no coverage here",
published no root, and left each of its helper children printing `warning:
GOCOVERDIR not set, no coverage data emitted` onto the stderr `internal/gocmd`
asserts the exact bytes of. Measured over that package: 103 of its 104 mutants
were reported killed at a score of 100.00%, and 90 of those kills carry the
warning in their output; with the root published the same run kills 91 and
reports the twelve survivors that were being hidden.

**What a helper does about its own output is the root**, rather than the
helper's own `GOCOVERDIR`. `internal/execute` strips `GOCOVERDIR` from every
child environment it composes — a mutant's test binary may not append its
counters into a profile somebody else is collecting — and that package's unit
tests start this very binary as their scripted `go`. The variable is gone by the
time such a helper looks; the instrumentation is not, and the coverage runtime's
exit hook writes all the same. The root is published only by a suite that is
itself instrumented and no policy strips it, so it is the signal that survives —
and a helper is that suite's binary re-executed, so it does not ask about the
cover mode a second time. A helper with a `GOCOVERDIR` and no root is the
opposite shape and is refused with `testkit.HelperMisuse`.

The root is made under `os.TempDir()` and removed by a deferred function, which
is exactly what a killed process does not run — so what a kill leaves behind
depends on where that is. Under a mutation run it is the worker's scratch
directory, which `internal/execute`'s `workerScratch` resolves and whose
`baseEnvFrom` points `TMPDIR`, `TMP` and `TEMP` at it; the run removes the whole
thing afterwards, so a killed mutant's root goes with it. Under a developer's
own `go test -cover`, or CI's coverage job, `TMPDIR` is the machine's, and a
Ctrl-C or a `-timeout` there does leave one behind.

### The scripted `go`

`mutantkit.Toolchain(t)` locates the machine's real `go`, which is what a test
that wants to know whether a mutant is really killed needs. `mutantkit.FakeGo(t)`
is the other half, for the questions that are about what go-mutants does when the
toolchain *misbehaves*: a version probe that hangs, one that answers garbage, a
`go list` that refuses a pattern, a compile that fails with diagnostics, a
baseline suite that is red.

Every one of those is a failure go-mutants exists to report clearly, and every
one of them was either an integration-tier test costing minutes and a toolchain,
or no test at all — **a `go` that hangs is not something anybody can install.**

The fake is an executable named `go` (`go.exe` on Windows) that re-executes this
test binary and answers from a rule table the test writes. The package's
`TestMain` therefore has to dispatch:

```go
func TestMain(m *testing.M) {
    os.Exit(mutantkit.Main(m))
}
```

`mutantkit.IsFakeGo()` composes that with a `TestMain` that has something of its
own to do. A binary started as `go` with the switch unset is *refused* rather
than left to run its own suite in place of the go command — the stray-fake guard,
and the reason `Main` is not optional.

#### Scripting it

`Fake.On(argv...)` matches every call whose argv begins with the given arguments,
and returns the rule to fill in. The arguments are what follows the binary name,
so `f.On("test", "-c")` matches `go test -c -o out ./pkg`. **The longest matching
prefix wins, and among prefixes of the same length the one registered last** — so
a convenience rule can be laid down first and overridden for the one call that
matters:

```go
f := mutantkit.FakeGo(t)
f.Version("1.99.0")                       // `go version`
f.On("list").Stdout(listing)              // every `go list`
f.On("list", "-e").Exit(1).Stderr(oops)   // except the scope resolution
```

At least one argument is required: a rule matching everything would be the silent
success the fail-closed rule below exists to refuse.

| Setter | What it scripts |
| --- | --- |
| `.Stdout(text)` / `.Stderr(text)` | the two streams separately, because a compiler writes diagnostics to one and a listing writes its document to the other, and whatever runs the child is what merges them |
| `.Exit(code)` | the status; zero is the default and needs no rule |
| `.Sleep(d)` | a toolchain that does not answer. Output is written first and the sleep is last, so "it never spoke" and "it spoke and then stopped" are tellable apart, and the caller's own deadline is what ends it — which is the thing being tested |
| `.CreateOutput()` | an executable file at the call's `-o` path. A rule that promises it and is matched by an argv with no `-o` in it is a misconfigured fake and exits `FakeGoNoRule` rather than pretending |

Every setter writes the whole table back to disk before it returns, so a rule may
be amended at any point up to the call it answers — including after the binary's
path has already been handed to the code under test.

Two convenience rules cover the calls every consumer makes.
`Fake.Version(release)` scripts `go version`, with *this* process's own target,
because `internal/gocmd` rejects a line that does not end in a well-formed
`os/arch`; use a release no toolchain will ever carry — `1.99.0` — so that an
answer accidentally produced by the real `go` cannot be mistaken for a scripted
one. `Fake.ListJSON(packages...)` scripts `go list -json` in the go command's own
encoding, a stream of pretty-printed objects with no enclosing array, which is
what `internal/execute`'s streaming decoder expects; it matches *every* `go
list`, so a phase that issues a second one with a different shape — the engine's
scope resolution runs `go list -e -f` — scripts that separately with a longer
prefix.

#### Fail closed

**A call no rule matches is refused with `mutantkit.FakeGoNoRule` and a message
naming the argv.** That constant is `testkit.HelperMisuse`, so it is the same
`97` every helper in this repository uses for "I could not set myself up", and it
is a status no test asks for deliberately.

That is the one behaviour of the fake that is not configurable. A stand-in that
answered an unscripted command with a silent success would let a test pass on a
command its author never considered, which is the failure shape a fake must never
have. The other side of the same rule: every call is recorded *before* it is
answered, so a refused call is in the log too.

#### What it recorded

`Fake.Calls()` returns every invocation the fake answered, in order. It reads the
log rather than remembering anything, because the calls happen in other
processes — which is exactly what makes it worth having: what a phase asked for
is a fact on disk, so a test can assert on an argv a call site composed without
that call site having to be injectable.

Each `mutantkit.Call` carries `Argv` (what followed the binary name), `Dir`
(because a `go list` issued from the wrong directory resolves a different module,
and that is a defect a test should be able to name), `EnvNames` (every variable
the child could see) and `Env` — **the values of exactly nine variables**:
`GOFLAGS`, `GOWORK`, `GOCACHE`, `GOTOOLCHAIN`, `GOENV`, `GOMODCACHE`, `PATH`, and
the two activation variables whose presence or absence is the difference between
a baseline and a mutant.

Nine rather than all of them is a promise rather than an economy. A call log is
written into a test's scratch directory and, under the keep policy, uploaded
as a CI artifact — so it must never become the place a developer's tokens or a
runner's credentials are written down. Every one of the nine is a flag list or a
filesystem path that go-mutants itself is supposed to compose, which is what
gives a test a claim to make about it. `Call.String()` leaves the environment out
entirely, because a failure that printed a hundred and fifty variable names would
bury the argv it was about.

#### Getting it in front of the code under test

There are two ways, and both are used here.

- **By path**, for a call site that takes one: `gocmd.Options.Explicit`,
  `execute.Options.Toolchain`, `gomutants.OpenOptions.GoBinary`. `Fake.Bin()` is
  that path, absolute because every such call site runs with a working directory
  of its own. This is the form to prefer: it names the binary under test and the
  test stays parallel. `Fake.Env(base)` adds the two control variables to an
  environment the test composed — the test's own, from `testkit.Compose` or
  `Environment.Vars`, rather than this process's, so that a developer with
  `GO_MUTANTS_ACTIVE` exported does not get a scripted compile running a mutant.
- **On PATH**, for a call site that locates the toolchain itself. `internal/cli`'s
  `doctor` and `internal/engine`'s run pipeline both call `gocmd.Locate` with no
  explicit path and no environment, so the process's own is the only way in.
  `Fake.Export()` publishes the control variables and puts `Fake.BinDir()` in
  front of `PATH`. It calls `t.Setenv`, so a test that uses it may not call
  `t.Parallel`.

`Export` forces `testkit.ResolveToolchainDirectories()` before it changes
anything, and that is not tidiness. After the export, *every* `go` this process
starts is the fake — the harness's own environment probe included. That probe is
lazy, so whether it has already run depends on which tests ran before it: a
package where it had not would send its `go env GOENV GOPATH GOMODCACHE` to the
fake, which records a call nobody asked for — aimed straight at the exact-count
assertions the call log exists to make possible — and would fall back to
`build.Default` for the directories. Both symptoms are order-dependent, which is
the worst shape a test failure has.

The two control variables are the whole protocol:

| Variable | What it is |
| --- | --- |
| `TESTKIT_FAKE_GO_RULES` | the file holding the rule table. Its presence is the switch that turns this test binary into the go command; its value is where the answers are |
| `TESTKIT_FAKE_GO_CALLS` | the append-only log each call is recorded in, which `Fake.Calls` reads |

Neither begins with `GO_MUTANTS_`, and that is the whole reason the prefix was
chosen: `internal/execute` and `internal/engine` strip that namespace from every
child they start, so a switch wearing it would be stripped out of the very
environments the fake exists to observe — and the test binary would then run its
own suite in place of the go command.

#### One install per test binary

The file is this test binary: six megabytes of it, and a unit-tier run of this
repository builds twenty-eight fakes. So it is installed **once per test binary**
rather than once per fake, into a directory of the process's own that
`mutantkit.Main` removes when the suite ends. The same file answers every fake,
because the rule table and the call log are named by the environment rather than
by the binary. It lives outside the tests' scratch directories for a second
reason: under the keep policy a failing test's scratch is what CI uploads, and a
six-megabyte binary in it is six megabytes of artifact saying nothing — what a
reader needs is the rule table and the call log, which are two small text files
and stay where they were. `Fake.Install(dir)` is the exception, for a test whose
subject is *where* the toolchain is.

**A hard link where the filesystem allows one, a copy where it does not**, and
the difference is worth knowing. A hard link is a second name for one file, so a
link to the running test binary names an image the operating system has mapped —
and Windows refuses to unlink a mapped image with `Access is denied`. Both things
installed here are exactly that: the shared `go`, which `Main` removes while the
process is still alive, and every `-o` output a scripted compile makes, which the
test's own cleanup removes. The failure arrived as a `t.TempDir` cleanup error
attached to a test that had already passed. `mutantkit.HardLinksAreRemovable(goos)`
is that rule, and a Windows runner would force a copy anyway: GitHub's puts
`RUNNER_TEMP` on `D:` and the build cache on `C:`, and `os.Link` cannot answer
across volumes.

`CreateOutput` produces the fake itself, on every platform, which is what carries
it past the build: `internal/execute`'s scheduler starts a compiled test binary
directly, so a scripted compile whose output was an inert file could only ever be
stat'ed and the whole half of that package below the build would still need a
real toolchain. Because the output *is* the fake, the scheduler's child answers
the same table, records its argv and its activation in the same log, and a mutant
can be scripted killed or survived by exit status. A shell script would have been
simpler and is wrong twice over: Windows has no shebang, so the two platforms
would produce different things, and neither could be asked to fail on demand.

The hazard that comes with it, stated rather than discovered: the produced file
is this test binary named whatever `-o` said rather than `go`, so `Main`'s
refusal cannot recognise it. Run with the control variables — which is what every
environment `internal/execute` composes carries — it is the fake; run without
them it would run the suite. Start it the way the code under test does.

#### What it cannot stand in for

A whole run past the baseline. **Discovery type-checks the module with
`go/packages`**, and that needs real source and a real toolchain: the questions
`internal/discover` answers — the universe's `true` against a shadowed one, a
type argument against a map index, a cgo file on a machine with cgo switched off
— are facts about `go/types` that a stand-in would have to invent, so there would
be nothing left to test behind it. That is why `internal/discover` is in the
allowlist rather than behind a fake, and why the instrumentation suites compile
what they generate: the only claim that matters about generated source is that it
is a program.

The same limit read from the other end: a helper process can stand in for
anything whose behaviour is the subject — a child that hangs, one that exits
non-zero, one that writes to both streams, one that ignores a signal, one that
spawns a grandchild. That is what `internal/runner`'s suite is made of, and its
`TestMain` is one line of `testkit.Helper`.

### The tiers exemption

A fake-driven test calls `gocmd.Locate` exactly like a real one — that is the
point of the fake, since the code under test must not be able to tell — so the
tier scan cannot separate them by *that* call. It separates them by the other
one: **a file that constructs a scripted `go` is supplying the toolchain, and a
file that does not is reaching for the machine's.**

The exemption is narrow in two ways, in exchange for being per *file* rather than
per call — the two halves are always written together, and a rule that tried to
pair them up would be a parser rather than a scan.

- **Constructing a fake is the only thing that grants it.** So a file that wants
  both a scripted toolchain and a real one has to be two files.
- **It covers only the calls a fake can be handed**, which are `gomutants.Open(`
  and `gocmd.Locate(` — the two that take a path. A file that builds a fake and
  also calls `exec.LookPath("go")`, `testkit.GoBinary`, `testkit.GitBinary` or
  `mutantkit.Toolchain` is reported like any other, because those reach for the
  machine's toolchain and no fake is in the way.

That is what let `internal/gocmd` leave the ledger. Its unit tier scripts every
misbehaviour, and the four claims that are about a *real* `go` rather than about
go-mutants' own code moved intact to
`internal/gocmd/toolchain_integration_test.go`. On a cold build cache that
package's unit tier went from 16.2 s and 34 MB of cache entries to 0.42 s and
8 KB.

`TestAFileThatScriptsTheToolchainIsNotDrivingOne` pins all three cases against a
synthesized module: the scripted file exempt, the same `gocmd.Locate` call
without a fake reported, and a file that does both reported for the half a fake
cannot stand in for. The last of those is the hole the exemption used to have.
See [ADR 0008](adr/0008-the-unit-tier-scripts-the-toolchain.md).

## 7. The corpus

`fixtures/` holds small Go modules that go-mutants runs against in its own tests.
Each one is a whole workspace rather than a package: the engine snapshots a
directory, builds it, and runs its tests, so a fixture has to be something a `go`
command can be pointed at.

[`fixtures/README.md`](../fixtures/README.md) is the ledger. It states the
conventions in prose and carries one table row per fixture saying what that
fixture is *for* — `simple/` is the happy path, `killable/` is the end-to-end
kill with thirteen predetermined fates, `families/` holds at least one live
candidate for each of the 42 rules, `selfwriting/` is the suite that writes into
the tree it runs in, and so on.

The conventions, each of which is a rule somebody would otherwise break
silently:

- **One module per fixture directory**, each with its own `go.mod` — which is
  what keeps this repository's own `./...` from picking a fixture up, and why a
  fixture that fails on purpose does not fail this repository's suite.
- **Module paths under `fixture.example/`**, a domain RFC 2606 reserves, so no
  fixture path can collide with a published module and no `go get` of one can
  reach the network.
- **No dependencies, ever.** A `require` line needs `go.sum` entries and a module
  cache inside the snapshot, which would make the integration suite depend on the
  network.
- **No `go` directive above the toolchain in use.** `GOTOOLCHAIN=local` turns a
  request to download another one into an error.
- **LF line endings.** A mutant identity is the digest of the bytes, so a CRLF
  fixture would produce different ids on the machine that wrote it and on every
  other.
- **Fast.** The baseline is measured several times before anything else happens
  and the derived timeout is five times the slowest run after the first, so a
  slow fixture costs
  the suite twice over.
- **SPDX headers everywhere**, `go.mod` included: `gofmt -l .` and the licensing
  check walk the filesystem rather than the module graph.

`TestCorpusConformance` in `internal/testkit` enforces all of that, in both
directions — a fixture with no ledger row and a ledger row with no directory are
both failures — and
`TestCorpusConformanceReportsEveryKindOfBreach` points the same scan at a corpus
built to break each rule in turn, because a gate whose only evidence is that it
passes on a conforming tree is a gate nobody has seen fail.

To add a fixture: write the module, add the row to `fixtures/README.md` saying
what it is for and what would break without it, and name it from the test that
drives it. A fixture nothing drives is a module that costs a checkout and proves
nothing.

## 8. Execution tracing, for developers

Every run keeps a diagnostic account of itself. Three properties make that
usable, and all three are load-bearing:

- **Every subprocess is recorded where it is started.** `internal/runner` is the
  one choke point, and `runner.Spec.Kind` — one of `trace.ExecKinds()` — is the
  one thing the runner cannot know for itself, so it is the one thing a call site
  has to supply. An empty `Kind` is accepted by the runner and refused by the
  schema, because a runner that rejected an unlabelled spec would turn a missing
  diagnostic into a failed run.
- **Every options struct carries a `Trace`.** It is the run's own recorder,
  handed down through each layer rather than reached for through a global, so two
  runs in one process record into two recordings.
- **Recording is unconditional and nil-safe.** `trace.New` returns a nil
  `*trace.Recorder` for a nil sink and every method of a nil recorder does
  nothing, so a call site writes `opts.Trace.Exec(...)` with no branch. A branch
  is a place for the traced and the untraced path to diverge, and the one
  property tracing must not break is that recording a run cannot change it. See
  [ADR 0002](adr/0002-every-subprocess-is-recorded-at-the-runner.md).

An untraced run records into a bounded in-memory ring, which is why a failure
nobody passed a flag for still has an account attached to it.

### Reading a recording

```console
go-mutants trace list
go-mutants trace summary [RUN-ID|PATH]
go-mutants trace diff BEFORE AFTER
go-mutants trace validate FILE
go-mutants trace clean
```

`summary` reads the newest recording in this workspace and says where the run
went and what it ran; `diff` compares two summaries, which is how a run that got
slower is investigated without reading either stream by eye; `validate` checks
one against the schema go-mutants publishes. `list` is newest-first, and `clean`
is the same collector a traced run applies to the newest ten.

`go-mutants explain <mutant-id-prefix>` joins the report and the recording for
one mutant: what it is, what happened to it, which suites cover it, every pass
the run made over the test binaries with the commands underneath, and a command
to paste that runs it again. `go-mutants explain <path>:<line>` asks the same
question from the other end. `--report FILE`, `--run RUN_ID` and `--trace DIR`
choose which run it reads; `--json` is refused, because the report and the
recording are already the machine-readable forms.

[docs/trace-v1.md](trace-v1.md) is the event contract, and
`trace/docs_test.go` fails when an `ExecKind` exists in the code and nowhere on
that page.

### Tracing from a test

`mutantkit.Trace(t)` returns a recorder and `mutantkit.TraceSink(t)` the sink
under it, both writing into `testkit.KeptDir(t)` — so a test that takes one and
then fails leaves `trace.jsonl` beside `KEPT.txt`, in the encoding
`go-mutants trace validate` reads.

## 9. Diagnosing a failing run

The three options are one command:

```console
go-mutants run --trace -vv --keep-temp=on-failure
```

- `--trace` writes the account into `<report.directory>/trace/<run-id>/` — a
  bare flag; `--trace=DIR` names somewhere else, and the value takes an equals
  sign.
- `-vv` renders the same events to the terminal as they happen, one line each,
  so two runs can be diffed without either writing a file. `-v` is the smaller
  form: phase durations, what killed each mutant, how many attempts it took, and
  which suites cover a survivor — or that none does. Both imply `--no-tui`, and
  neither can be combined with `--quiet` or `--json`.
- `--keep-temp=on-failure` leaves the snapshot and the scratch directory on disk
  when the run fails and not when it is interrupted, which is the mode a CI job
  can leave on. A bare `--keep-temp` keeps them whatever happened.

A run that fails writes a bundle by default, and **where it
lands depends on whether the run was traced.** A traced run — which the command
above is — files it in that run's own recording directory,
`<report.directory>/trace/<run-id>/`, so the failure and the account of the run
are one directory rather than two halves of one story. An untraced run has no
such directory, so its bundle goes in
`<report.directory>/diagnostics/<run-id>/`. Under `--no-diagnostics`, or
`GO_MUTANTS_DIAGNOSTICS=0` for an invocation nobody can add a flag to, there is
no bundle at all. Either way, it holds:

| File | What it holds |
| --- | --- |
| `error.txt` | the rendered failure and its typed chain |
| `environment.txt` | the environment's variable *names* |
| `doctor.txt` | the `doctor` table for the machine |
| `trace.jsonl` | the run's own account, out of the in-memory ring — only when the run was not traced, because a traced run's stream is already in this directory and writing the ring out beside it would be the same run told twice with one telling truncated |
| `report.json` | the run report, if there was one |
| `preserved-paths.txt` | what the run left on disk, or a line saying it left nothing |

`error.txt` is written first and `preserved-paths.txt` last, and the presence of
the last one — never empty, so a reader is never left wondering whether the run
kept nothing or the writer stopped — is what says the bundle is finished. That is
how the collector tells a finished bundle from a run still writing one. The
newest ten are kept, `go-mutants trace clean` sweeps them with the recordings, an
interrupted run writes none, and a bundle that cannot be written is a `GOM1014`
warning rather than a different exit code.

So the order is: read the bundle's `error.txt` for what failed — in
`trace/<run-id>/` after the traced command above, in `diagnostics/<run-id>/`
after an untraced one — then `go-mutants trace summary` for where the time went
and which command was the last one, `go-mutants explain <id>` for one mutant's
whole story and the line to paste, and the kept snapshot for what the tree
looked like. See
[ADR 0003](adr/0003-diagnostics-live-in-the-report-directory.md) for why the
bundle is in the report directory and never in a temporary one.

### Environment spellings

Three variables ask for the same three things, for the invocation nobody can add
a flag to — a `go test` that drives go-mutants, a CI step whose command line is
generated, a script somebody else owns:

| Variable | What it asks for |
| --- | --- |
| `GO_MUTANTS_TRACE` | a truthy value for the default directory; any other non-empty value names one |
| `GO_MUTANTS_KEEP_TEMP` | a truthy value for `always`; `always`, `on-failure` or `never` verbatim |
| `GO_MUTANTS_DIAGNOSTICS` | a falsy value to switch the bundle off |

The yes and the no are `strconv.ParseBool`'s and nothing of go-mutants' own —
`1`, `t`, `T`, `TRUE`, `true`, `True` and their negatives — because a reader
that took one spelling and treated the other as something else would be a trap
whose failure mode depends on the shift key.

An explicit flag always wins, so a nested invocation can say something else
without unsetting a variable it does not own. An empty value means "said
nothing", exactly as never having exported it does, which is how a job switches
an inherited request back off; because the defaults differ, that means no
recording, no keep, and a bundle. Nothing is ever inserted after `--`:
everything there is the test command's own argv, and adding a flag to somebody
else's command line is the one thing the separator promises never happens. This
is the whole of go-mutants' reading of the environment for these options, and it
happens in one function, reached from `cli.Execute` alone.

## 10. TDD and review

**Red first.** A test that has never failed is a test nobody has seen work.
Write the assertion, watch it fail for the reason you expect, and only then write
the code — and when the subject is a document rather than a function, break the
document on purpose once to see the failure, then put it back. The pins that
guard this page were written that way.

**Review adversarially.** The question is not "does this look right" but "what
would this pass that it should not". Most of the comments in this repository
exist because the answer was interesting.

The things a change has to keep:

- **The pinned vocabularies.** `api_contract_test.go`'s
  `TestVocabulariesArePinned` writes out every string constant a consumer may
  have serialized — the outcome names, the probe states, the prepare phases, the
  change kinds, the test-log operations — as literals rather than as comparisons
  against the constants they came from. A renamed constant is a compile error for
  a consumer; a changed *value* is not, so it is spelled out. Changing one is a
  decision rather than an accident.
- **The consumer contract.** `external_contract_integration_test.go` writes three
  consumer sources into modules of their own, points each at this checkout
  with a `replace` directive and runs a real `go test` inside it, with
  `GOPROXY` off: one for the engine API, one for the public `trace` package,
  and one built program that names the engine it linked. It is the only way to
  prove the bridge is usable without an internal package.
- **`docs/library.md` for every public change.** The engine API is a published
  surface; a new field, a new error, a new phase name belongs on that page in the
  same change.
- **`CHANGELOG.md`.** Entries go under `## [Unreleased]`, in `### Added`,
  `### Changed`, `### Fixed` or `### Notes`, as a top-level bullet whose first
  sentence is bolded and says what changed, followed by prose saying **why**. The
  reason is the entry; the shape is already in the diff.
- **SPDX on every new file**, `// SPDX-FileCopyrightText: 2026 go-mutants
  contributors` and `// SPDX-License-Identifier: MIT OR Apache-2.0`, in an HTML
  comment for Markdown and a `#` comment for TOML and YAML. Files that cannot
  carry one are annotated in `REUSE.toml`.
- **`committed`.** Imperative subjects of at most 72 characters, wrapped bodies.
  The `commit-msg` hook and the CI lint job both run it. Pull request titles are
  conventional commits, because this repository squash-merges and the title
  becomes the subject on `main`.
- **`lefthook`.** `mise run hooks` installs the pre-commit and commit-msg hooks.
  Pre-commit runs the fast gates only — `mise run fmt`, `typos`, `taplo check` on
  staged TOML, `actionlint` on staged workflows — so a commit never waits on a
  full compile. The slow ones stay in `mise run check` and CI.

Before pushing:

```console
mise run check
```

which is `fmt`, `build`, `test` and `lint` in CI order. Run `mise run
test-integration` as well when the change touches snapshotting, the runner, or
anything that shells out to `go`.

## 11. Dogfood

`mise run dogfood` runs go-mutants against go-mutants with `--strict`, so an
undeclared survivor fails the build. It is the gate on whether the tests *catch*
anything, which is why coverage is allowed to be a signal.

The scope, the measured score and the floor live in `.go-mutants.toml`, next to
the settings they justify. It covers eleven whole packages:

| package | mutants | what it is |
| --- | --- | --- |
| `internal/report` | 1095 | what a run writes down — the RunReport v1 document, the history store, the projection into the published format, the self-contained page, and the merge that puts a split run back together |
| `internal/config` | 461 | the reader of the file above — decoding, validation, precedence, the byte-size vocabulary, and the walk that locates a diagnostic in it |
| `internal/mutation` | 450 | the mutation model everything downstream is built on — catalogue, identity, rule set, scoring, sharding, exit policy |
| `internal/coverage` | 146 | the profile reader, and the mapping that decides which suites a mutant is measured against |
| `internal/gocmd` | 106 | the toolchain wrapper — locating `go`, probing and parsing its version, the GOFLAGS merge, and the typed failures of all three |
| `internal/schemas` | 89 | the JSON schema validation every published document goes through |
| `internal/glob` | 68 | the glob engine those identities depend on |
| `internal/interval` | 48 | the five-way span relation the interval forest is built on |
| `internal/operatorselect` | 16 | which rules a profile or an `--operator` name selects |
| `internal/drift` | 11 | which change to an instrumented snapshot the instrumentation did not make |
| `internal/testflag` | 7 | which argument names a test-binary flag |

Nine of the ten are pure arithmetic, pure text matching, a pure filter over a
digest table, or a pure decision over values handed in, with no clock and no
network, so a mutant either changes an answer or it does not. `internal/config`
reaches the filesystem in exactly one place — `os.ReadFile` in `LoadFile` — and
everything under it takes bytes and returns an answer.

`internal/gocmd` is the exception and the reason it is worth naming separately:
it starts processes. Its unit tier is toolchain-free all the same, because
`internal/testkit/mutantkit`'s scripted `go` answers from a table — so a probe
that hangs, one that exits non-zero, one the operating system refuses to start
and one that prints something no toolchain has ever printed are all real child
processes and none of them is the machine's `go`. The claims that are about a
real toolchain rather than about go-mutants' own code carry
`//go:build integration` and are outside this scope.

`internal/report` is the one that does not, and it is the largest. It writes
files: two artefacts into the user's own tree and a run history into a directory
it shares with every other program on the machine. So a survivor there can be a
failure the operating system will not produce on demand rather than a value
nobody asserted, and widening to it needed both halves of an answer to that. The
failures the operating system *will* produce are staged for real — a directory
where a file has to go, a path under a file that is not a directory, a symbolic
link to itself, a store named by a relative path, a directory that refuses
writes — and three that it will not, a write, a flush and a close that fail on a
file it has just created, go through package-level seams named for them:
`createTemp` and `openMarker` in `history.go`, `readDir` in `enumerate.go`, and
`strykerSchemaSource` in `strykerschema.go`. They are the same kind of seam as
`verifyViewer`, which has been there since the HTML report was written, and each
carries the reason it exists where it is declared. They also buy the one thing a
real failure cannot: a moment. The artefacts are published one file at a time
and put back one file at a time, and the ownership claim reads a marker before
it creates one — so "the write that puts the document back is the one that
fails" and "a marker appeared between the read and the create" are staged by
failing the *n*-th file creation, or by acting just before it. A test that uses
any of this cannot be `t.Parallel`.

Determinism survives that, and it is worth saying how. Every path those tests
touch is under a `t.TempDir`; the failures are real errors from the real
operating system, not sentinels; nothing asserts a wall clock or a directory
iteration order; the one permission trick probes for its own enforcement and
skips where a platform or a user is not stopped by it, rather than naming
Windows or asking `os.Getuid`; and the tests that create symbolic links skip
where a platform refuses to create one.

The numbers the gate is sized against: 2497 mutants catalogued, 2432 detected —
2428 killed, two of them by the memory bound, and four caught by the per-mutant
timeout — sixty-five declared expectations, **a score of 100.00%**, at
`--jobs 4` against a warm test-owned build cache. `policy.minimum_score = 99.5`
is compared on every run, `--strict` or not, and at this size it does not fail
until the thirteenth unexpected survivor — so `--strict` is the thing that
actually fails this job, on the first.

The wall clock, on the shared machine that widened the scope: warm, with the
derived timeout at its 10 s floor, 6m13s and 6m30s; two more warm runs on the
same machine read 8m42s and 9m02s while it had picked up other work, and the
difference is the load rather than the tree — one derived a 23.6 s timeout from
a baseline that read 4.7 s under load, paid twice by each of the four mutants
that never return, and the other reported two of its kills as `inconclusive`,
killed once and timed out on the confirming run. Cold, 9m31s, of which more
than half is those four mutants waiting out, twice each, a timeout sized on the
compiling first baseline run: 38.7 s where the runs after it asked for 10 s.
That is answered in the engine, which now sizes the budget on the runs after
the first; the first cold run on that engine — GitHub's ubuntu runner, this
scope, `slowest 138ms` after the first run and the timeout at its floor — took
about 4m45s in a 5m22s job. The
tally was identical on every run but the loaded one's `inconclusive` column,
which is the usual caveat: only the ratios travel, and `.go-mutants.toml`
records the paired before-and-after that makes them a comparison. Whether this
scope wants an explicit `test.timeout` was the open question the first two
runs left; on the floor the tally is exact, so the timeout stays derived.

Six of those mutants never return, and they are worth knowing about because
they, rather than the catalogue, are much of what sets this gate's wall clock.
**Four spin**: `negate-loop-condition` on `internal/coverage/textfmt.go`'s `for
scanner.Scan()`, the same operator on either loop of `internal/config`'s
position walk, and the same operator again on the loop in
`internal/report/html.go` that neutralises a double hyphen inside the HTML
report's attribution comment, which never stops replacing what it has just
written. A spinning mutant holds nothing, so only the clock can catch it, and a
timeout is measured a second time before it is believed — two ten-second waits
each, eighty seconds of worker time.

**Two allocate**, and they are the reason a mutant is now bounded in memory as
well as in time. `internal/config`'s `lineStarts`, with `i < 0` negated or its
stride turned into a subtraction, appends to a slice instead of advancing
through the file. Before the bound existed those two were the most expensive
mutants in the run and their verdict was a race — killed when the allocator
reached them first, timed out when the clock did — and on a GitHub runner the
job did not go red so much as disappear, with "The runner has received a
shutdown signal". Now each is stopped at about 1.1 GiB after a second and a half
and reported as `killed`, once, with no second attempt: a memory kill is a kill
rather than a verdict to confirm. See
[ADR 0009](adr/0009-a-mutant-is-bounded-in-memory-as-in-time.md).

There were nearly seven. Negating `timeout <= 0` in `internal/gocmd`'s
`LocateContext` replaces a probe's configured deadline with the thirty-second
default, and the hanging-probe test scripted a `go` that slept for two minutes —
so nothing but the per-mutant timeout could end it. The sleep bought nothing the
test's own assertion did not already buy, since it fails a probe still running
at five times its deadline; shortened to three seconds it is still fifteen times
that deadline, and the mutant now comes back with a parse error and dies in
about three seconds. Before widening a scope, look at what its slowest mutants
are actually waiting for: sometimes it is the code, and sometimes it is a
constant in a test.

The bound is derived from the same baseline runs the timeout is, as
`max(1 GiB, largest baseline peak × 4)`, and `go-mutants run -v` prints what it
resolved to:

```text
memory: baseline peak 157.5 MiB, bound 1.0 GiB (derived)
```

157.5 MiB × 4 is 630 MiB, so the 1 GiB floor still applies and the bound is
about six times what the unmutated suite needs — far enough above anything
legitimate that it catches runaways rather than honest tests. The peak itself is
one reading rather than a constant: the nine-package scope read 125.2 MiB, the
ten-package one 141.5–147.4 MiB across three runs, and this one 157.5 MiB —
each suite that starts processes or writes documents moved the number the bound
is derived from, and none of them moved the bound, because the floor was always
the larger of the two. `-v` also names the bound on each mutant it stops
(`killed by … (memory: 1.1 GiB > 1.0 GiB bound)`), and the JSON report carries
`memory_exceeded` and `peak_memory_bytes` on the mutant and on each execution.

With that in place the whole summary is stable: the same 2497 / 2428 / 4 / 65 on
every run, killed-versus-timed-out included, except for the two kills a loaded
machine reported as inconclusive. It was not before, and a widening that makes
a gate's own tally a coin flip is a widening that is not finished.

Two things live outside the file. `--strict` is passed by the task rather than
written into `policy.strict`, because the gate belongs to the caller: a developer
who wants to *look* at a survivor runs `go-mutants run` and gets a report
instead of a failure. And `RAPID_SEED=1` is exported, because
`pgregory.net/rapid` draws a fresh seed per process and `internal/interval`'s
property suite is part of what kills these mutants — unpinned, a marginal mutant
could be killed on one run and survive on the next, which would make the score,
and therefore the `minimum_score` floor, a coin flip. Nightly re-explores with
random seeds and a far bigger budget instead.

### Widening it

**Write the tests that kill the survivors first, and only then the line.**

Excluding the files that survive is choosing the scope to fit the score, and
declaring the survivors away is a skip list wearing a ledger's clothes. Neither
is a widening; both are a smaller gate with a larger number on it.
`internal/mutation` was a single file in this list — `shard.go` — until its own
suites grew the tests that kill what the whole-package scope found.

A `[[mutation.expect]]` row is for a survivor no honest test can reach — an
equivalent mutant, where the rewritten program computes the same thing, or an
unreachable one, where an earlier refusal means the branch has no input that
gets to it — and it carries a `reason` that argues which. A row whose reason is
"no test covers this" is a missing test, and so is one whose reason is "the
operating system will not fail on demand": that is what a seam is for, and
`internal/report` has four of them with the argument written where each is
declared.

The floor is re-checked with the scope, and it has to be: a percentage buys a
different number of survivors at every size, so leaving `minimum_score` alone
while the catalogue grows can make the gate looser without anybody deciding to.
Re-checked is not the same as moved. It went 96 → 99 when the catalogue went
from 120 scored mutants to 544, where the old number would have bought
twenty-one survivors of slack instead of four; it then stayed at 99 through six
widenings, because one percent of 549, 583, 809, 1266 and 1371 is five, five,
eight, twelve and thirteen — always short of the twenty-one that moved it the
time before. One percent of 2432 is twenty-four, which is not short of it, so
with the eleventh package the same rule moved the number again, to 99.5: twelve
survivors of slack (2420/2432 clears, 2419/2432 does not) where 99 bought
thirteen before the widening. The floor is a fixed number of survivors rather
than a fixed percentage of a growing catalogue. Do the arithmetic, write the
answer next to the number, and only then decide whether it moves.
