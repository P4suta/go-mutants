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

The keys decode and validate; there is no outcome cache yet, so nothing is
stored or reused whatever the mode says.

- `mode`: `"auto"` (default), `"on"`, or `"off"`. The key covers the tool
  version, the executable digest, the workspace digest, the catalog ID set, the
  test command, the timeout, and the environment. Errors, cancellations,
  inconclusive outcomes, unconfirmed timeouts, and expected mutants are never
  stored as reusable outcomes.
- `directory`: optional. Relative paths resolve under the OS cache root, never
  under the workspace.

### `[policy]`

The keys decode and validate, including their ranges; no policy is enforced
yet, because no run has produced a score to compare against.

- `strict`: default `false`. **go-mutants does not fail a build unless asked**,
  in a TTY, a pipe, and CI alike.
- `minimum_score`: percentage that `strict` compares against. Exit 1 is
  reserved for this opt-in policy failure and nothing else.
- `require_mutants`: default `true`. A selection that produced no mutants at
  all is a configuration problem, not a perfect score.

### `[report]`

The keys decode and validate — an out-of-range `high` or `low` is a positioned
error today — but no report is written yet. `list --json` writes its catalogue
document to standard output and never to this directory.

- `directory`: default `reports/mutation`. Excluded from the snapshot manifest
  and from cache identity.
- `formats`: any of `"json"` and `"html"`. `[]` disables project reports
  without deleting existing files.
- `high` / `low`: HTML colouring thresholds only. They are deliberately
  independent of `[policy]`, so making a report prettier can never change
  whether CI passes.

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

The command tree today is `run` and `list`. `init` is planned, along with its
`--dry-run` and `--check` flags, which will let it describe or verify a
configuration without writing one; it will never overwrite an existing file.
