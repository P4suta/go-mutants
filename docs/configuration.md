<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Configuration

**Status: the decoder is implemented.** Every key on this page is decoded,
validated, and merged with the CLI flags today. Not every key yet changes
behaviour, because the subsystems some of them configure do not exist; those
sections say so individually below. `.go-mutants.toml` in the repository root
is the working example of the surface.

Configuration lives in its own file, `.go-mutants.toml`, next to `go.mod`. It
is never merged into `go.mod`, and there is no environment-variable
configuration. `version = 1` is required at the root.

Decoding is strict: `pelletier/go-toml/v2` with `DisallowUnknownFields()`, so
an unknown or misspelled key is an error that names the file, line, and column.
BurntSushi/toml was not chosen because it cannot report that position.

## Full example

```toml
version = 1

[mutation]
profile = "balanced"
include = ["cmd/**/*.go", "internal/**/*.go"]
exclude = ["**/*_test.go", "**/testdata/**"]
operators = ["comparison", "error-swallowing"]

[[mutation.expect]]
id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
reason = "Equivalent: the branch is unreachable for all valid inputs."

[test]
command = ["go", "test", "./..."]
timeout = "60s"
baseline_runs = 3

[execution]
jobs = 8

[cache]
mode = "auto"
directory = "team-cache"

[policy]
strict = false
minimum_score = 0
require_mutants = true

[report]
directory = "reports/mutation"
formats = ["json", "html"]
high = 80
low = 60
```

## Fields

### `[mutation]`

- `profile`: `"balanced"` (default), `"strong"`, or `"all"`. The tiers are
  monotonically inclusive; see [operators](operators.md).
- `include` / `exclude`: glob arrays. `/` is the portable separator, `*`
  matches within one path component, `?` matches one non-separator byte, and
  `**` crosses directory boundaries. Excludes apply after includes. The glob
  engine is implemented in-tree and fuzzed, because `**` semantics differ
  between third-party libraries and mutant IDs must not depend on which one is
  installed.
- `operators`: family names. Omitted means "whatever the profile selects",
  which is what `init` writes.
- `[[mutation.expect]]`: repeated rows of a unique full 64-hex `id` and a
  non-empty `reason`. An expectation is evidence to check, not a skip list. The
  mutant still runs on every invocation and never uses a cached outcome:
  survival fulfills it, a kill or confirmed timeout is exit 2, and an ID that
  has disappeared from the catalog is stale and also exit 2. The rows are
  decoded and checked for shape and uniqueness today; the checking of the
  evidence arrives with mutant execution.

### `[test]`

- `command`: an argv vector, never a shell string. Defaults to
  `["go", "test", "./..."]`.

  Setting it to anything else has one consequence worth knowing before you do:
  it turns coverage-guided selection off, with a `GOM7601` warning naming both
  commands. The narrowing maps a *test binary* onto the lines it reached, and
  that is only sound because go-mutants compiled those binaries itself and
  knows which package each one is. A custom command is an opaque program — it
  may run a subset, a superset, several suites, or not be `go test` at all —
  and guessing which of go-mutants' own binaries its coverage belongs to would
  skip mutants a test does cover, which loses a kill rather than costing time.
  The run is slower and its verdicts are unchanged. The same applies to a
  `-- <test argv>` passthrough, since what matters is what the command does
  rather than where it was written.
- `timeout`: a duration string such as `"60s"` or `"2m"`. Omitted derives
  `max(10s, slowest baseline × 5)`.
- `baseline_runs`: positive integer, default 3. Every observation is retained
  in the report, not just the slowest.

### `[execution]`

- `jobs`: positive worker count. Defaults to `min(NumCPU, 8)`.

### `[cache]`

**Status: implemented.** A run may answer a mutant from an outcome it has
proven before, so a second run over unchanged code measures only what has
moved.

- `mode`: `"auto"` (default), `"on"`, or `"off"`, overridden by `--cache`.

  `auto` reuses outcomes only when `test.command` is the built-in
  `go test ./...`, and does nothing at all otherwise, with a `GOM7901` warning
  naming the command. go-mutants knows what `go test ./...` does; it knows
  nothing about a command you wrote, which may consult a clock, a database, or a
  network — and none of those can be in the key. Set `mode = "on"` to promise
  that your command is reproducible and get the cache anyway. `off` never reads
  and never writes.

  Turning off, rather than degrading to read-only, is the same judgment: a
  read-only cache over a command go-mutants cannot reason about would still be
  adopting outcomes it cannot justify.

- `directory`: optional. Relative paths resolve under the OS cache root, never
  under the workspace. It replaces the `go-mutants` element, so
  `directory = "team-cache"` puts the store at `<os cache>/team-cache`.

The key covers the tool version, the running executable's digest, the Go
toolchain's own release, the workspace digest, the catalog digest, the test
command, the timeout **as configured**, and `CGO_ENABLED`, `GOARCH`, `GODEBUG`,
`GOEXPERIMENT`, `GOFLAGS` and `GOOS` — with an unset variable hashing
differently from one set to nothing. Entries are filed under that key, so
nothing is ever invalidated: editing a file moves the key, and the old entries
become unreachable rather than wrong.

The toolchain's release is there because nothing else in the key carries it. The
test command is hashed as you wrote it, so `go test ./...` hashes the literal
word `go` and not the toolchain it resolves to, and `go.mod` pins a language
version like `go 1.26` rather than a patch release — without this field a
1.26.5→1.26.6 upgrade would keep every outcome the old compiler measured
reachable. The environment names are on the list for one reason each: all six
change what your tests compile to or how they are run. `CGO_ENABLED` is the one
worth watching, because its *default* depends on whether a C toolchain is
installed, so two CI images identical in every other respect can compile
different programs.

The configured timeout rather than the derived one is deliberate. A derived
timeout is `max(10s, slowest baseline × 5)`, a wall-clock measurement that moves
on every run, so hashing it would silently switch the cache off for exactly the
projects worth caching. Each entry records the bound it was measured under
instead, and a run adopts it only if its own bound could have produced the same
answer: a kill or a survival is reusable when the measurement fits inside this
run's bound, and a confirmed timeout when this run's bound is no larger than the
one it already blew.

Killed, survived, and **confirmed** timed-out are the only outcomes stored.
Inconclusive results, harness errors, interruptions, mutants no test covers, and
every mutant named in `[[mutation.expect]]` are measured on every invocation.
Inconclusive is the one worth spelling out: it means two attempts disagreed, and
a cache that froze a disagreement would make a flake permanent.

Nothing about the cache can change a verdict or fail a run. A cache that cannot
be opened, an entry that cannot be read, an outcome that cannot be written: each
is a `GOM79xx` warning and a run that measures more than it had to.

`go-mutants cache status` prints where the store is and what is in it,
`cache gc --days N` (default 30) removes outcomes written more than N days ago —
age is the modification time, and reading an entry does not refresh it, so this
removes what is old and not what is unpopular — and `cache clean` removes them
all. All three refuse a directory in the OS cache
that does not carry go-mutants' own ownership marker, and none of them touches
the run history filed beside the outcomes.

### `[policy]`

**Status: implemented.** The keys decode and validate, including their ranges,
and a run enforces them against the score it measured: exit 1 is a policy
failure and nothing else.

- `strict`: default `false`. **go-mutants does not fail a build unless asked**,
  in a TTY, a pipe, and CI alike.
- `minimum_score`: percentage that `strict` compares against. Exit 1 is
  reserved for this opt-in policy failure and nothing else.
- `require_mutants`: default `true`. A selection that produced no mutants at
  all is a configuration problem, not a perfect score.

### `[report]`

This section governs the two files a run writes into your own tree. Everything
else go-mutants produces goes into a disposable snapshot or into the OS cache
directory: the authoritative `RunReport v1` is published to the history store
(and to standard output under `run --json`), and `list --json` writes its
catalogue to standard output. Neither is ever written here.

- `directory`: default `reports/mutation`. A relative path resolves under the
  workspace root; an absolute or escaping path is refused, because the
  artefacts are a project's own output and belong beside the project. The
  directory is excluded from the snapshot manifest and from cache identity, so
  publishing into it cannot change a workspace digest or invalidate an outcome.
- `formats`: any of `"json"` and `"html"`; default both. `"json"` is
  `mutation.json`, the one-way projection into the Mutation Testing Report
  Schema; `"html"` is `mutation.html`, a single self-contained page embedding
  that projection and a vendored copy of the Mutation Testing Elements viewer,
  which fetches nothing and opens from `file://`. `[]` disables the project
  artefacts entirely — and does so before anything is read, so turning them off
  also turns off the work of building them. It removes no existing file.
- `high` / `low`: the viewer's colouring thresholds, as percentages, and
  nothing else. They are deliberately independent of `[policy]`: making a
  report prettier must never change whether CI passes, and the two are kept
  apart so nobody can do it by accident. `policy.strict` and
  `policy.minimum_score` are the only settings that decide an exit status. A
  value outside 0..100 written in this file is a positioned error. A `low`
  above `high` is a cross-field rule checked after merging and reported against
  `report.low` with no position — `low` may come from the file and `high` from
  a flag, so there is no single place to point at.

`run --report none|json|html|json,html` overrides `formats` for one
invocation. The pair is published together or not at all: if the HTML cannot be
written, the JSON written moments before is put back as it was found, because
two files describing different runs are worse than either alone. Both are
staged and renamed into place, and both are written only after the run's own
record is safely filed in the history store.

See [`stryker-compatibility.md`](stryker-compatibility.md) for the outcome
mapping, the UTF-16 column rule, and the report's security properties.

## Precedence

Built-in defaults, then `.go-mutants.toml`, then CLI flags. Repeating
`--include`, `--exclude`, or `--operator` replaces the corresponding TOML array
rather than appending to it. `--include`/`--exclude` use `StringArrayVar`: a
pattern containing a comma is one pattern, not two.

`--mutant ID_PREFIX` resolves against the complete catalog, including families
outside the configured `operators` and rules outside the selected profile.

Contradictory flags are rejected before any work starts, through
`MarkFlagsMutuallyExclusive` plus semantic validation, with a stable `GOM10xx`
error code.

The command tree today is `run`, `list`, `doctor`, `init`,
`report list|latest|clean|merge|validate`, and `cache status|gc|clean`.

`init` writes this file with every built-in default in it and a comment
explaining each one, so adopting it changes nothing. It never overwrites and
there is no `--force`: delete the file first if that is what you mean.
`--dry-run` prints what would be written and touches nothing; `--check` exits 0
when the file already there is byte-identical to what this build would write and
1 when it is not, which is a CI freshness gate rather than a policy failure.

`doctor` reports the toolchain, the module, git, the cache directory, the
platform, and whether this file parses — as an aligned table, or as a
`go-mutants/doctor` v1 document with `--json`.
