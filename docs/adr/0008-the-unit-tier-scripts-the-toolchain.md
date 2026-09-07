<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0008 — The unit tier scripts the toolchain

## Status

Accepted, 2026-09-07. Implemented by `internal/testkit/mutantkit`'s `FakeGo`,
`Main` and the tier exemption in `internal/testkit/tiers_test.go` (#49).
[ADR 0005](0005-the-test-harness-owns-its-temporaries.md) records the
temporary-directory and build-cache decisions the tiering rests on; this one is
about what a test in the unit tier may reach for.

## Context

go-mutants is a program whose whole job is to drive another program. Most of
what it must get right is what it does when that other program *misbehaves*: a
version probe that answers garbage, one that exits non-zero, one that never
answers at all; a `go list` that refuses a pattern; a `go test -c` that fails
with diagnostics the user needs to see; a baseline suite that is red.

Every one of those was either an integration-tier test — minutes, and a real
toolchain — or no test at all. The second category is the interesting one,
because it is not a matter of budget: **there is no way to install a `go` that
hangs.** The behaviour that most needed pinning was the behaviour that could not
be arranged.

The cost of the first category was not small either. `internal/gocmd` is the
wrapper every other package depends on, and its subject *is* the toolchain, so
it sat in the unit-toolchain allowlist and paid for a real `go` on every run:
16.2 s and 34 MB of build-cache entries on a cold cache, for a package whose
assertions are almost all about parsing what a child printed.

Mocking the wrapper away was not an option worth taking. The defects this
repository has actually shipped in that area were in the composition of an argv
and an environment — which flags a compile carries, which directory a listing is
issued from, whether an activation variable was stripped — and an injected
function cannot be wrong about those in the way a process can.

## Decision

The unit tier gets a `go` command it writes the answers for: an executable that
is **this test binary re-executed**, dispatched by the package's `TestMain` and
answering from a rule table on disk.

1. **The fake is the test binary.** `mutantkit.FakeGo(t)` installs an executable
   named `go` that re-executes the running binary; `mutantkit.Main` is the
   `TestMain` hook that recognises it. A shell script would have been simpler and
   is wrong twice over — Windows has no shebang, so the two platforms would
   produce different things, and neither could be asked to fail on demand.

2. **It reaches the code under test the way a real toolchain does.** By path
   through `gocmd.Options.Explicit`, `execute.Options.Toolchain` and
   `gomutants.OpenOptions.GoBinary`, or on `PATH` through `Fake.Export` for the
   two call sites that locate one themselves. Nothing under test is made
   injectable to accommodate it, which is what keeps the test a test of the real
   composition.

3. **Fail closed.** A call no rule matches exits `FakeGoNoRule` — the harness's
   own `97` — with a message naming the argv, and every call is recorded before
   it is answered so a refused one is in the log too. A stand-in that answered an
   unscripted command with a silent success would let a test pass on a command
   its author never considered, which is the failure shape a fake must never
   have. A binary started as `go` with the switch unset is refused as well,
   rather than running its own suite in place of the go command.

4. **The log keeps names, not values — except nine.** A call records its argv,
   its working directory, and the *name* of every variable the child could see;
   only `GOFLAGS`, `GOWORK`, `GOCACHE`, `GOTOOLCHAIN`, `GOENV`, `GOMODCACHE`,
   `PATH` and the two activation variables are kept by value. Every one of those
   is a flag list or a filesystem path that go-mutants itself composes, which is
   what gives a test a claim to make about it — and a call log is written into a
   test's scratch directory and uploaded as a CI artifact under the keep policy,
   so it must never become the place a token is written down. This is
   [ADR 0001](0001-trace-is-not-evidence.md)'s secret-safety rule applied to the
   harness.

5. **The tier ledger reads a fake as the opposite of driving a toolchain.** A
   file that constructs one is exempt from
   `TestEveryToolchainDrivingTestIsIntegrationTagged`, because it supplies the
   toolchain rather than reaching for the machine's. The exemption is per file
   and narrow in two ways: constructing a fake is the only thing that grants it,
   and it covers only the two calls a fake can be handed — `gocmd.Locate` and
   `gomutants.Open`. A file that also calls `exec.LookPath("go")`,
   `testkit.GoBinary`, `testkit.GitBinary` or `mutantkit.Toolchain` is reported
   like any other.

6. **Discovery stays real.** No fake stands in for `go/packages`. The questions
   `internal/discover` answers are facts about `go/types`, and a stand-in would
   have to invent them, so there would be nothing left to test behind it.

## Consequences

- `internal/gocmd` left the allowlist. Its unit tier now scripts every
  misbehaviour and runs on a machine with no Go on it — `PATH=/nonexistent` is
  enough — at **0.42 s and 8 KB of cache entries**, from 16.2 s and 34 MB. The
  four claims that are about a real `go` rather than about go-mutants' own code
  moved intact to `internal/gocmd/toolchain_integration_test.go`.
- Because the exemption is per file, **a package that wants both a scripted
  toolchain and a real one has to be two files.** That is not a workaround; it is
  the cost of a scan rather than a parser, and it makes the split visible in the
  file listing.
- The unit tier gained an assertion it could not previously make: the argv, the
  directory and the composed environment a child process really received. "The
  compile carries `-vet=off` and the listing does not" is now a claim about a
  process.
- A scripted `go test -c -o X` produces the fake again rather than an inert file,
  so `internal/execute`'s scheduler can be driven end to end without a toolchain.
  The hazard that comes with it is that the produced file is the test binary
  under a name `Main`'s refusal cannot recognise; started without the control
  variables it would run the suite. A test must start it the way the code under
  test does.
- `Fake.Export` changes a process-wide global, so a test that uses it cannot be
  parallel and every `go` that process starts afterwards is the fake — the
  harness's own lazy `go env` probe included, which is why `Export` forces
  `testkit.ResolveToolchainDirectories` first. The path form has neither problem
  and is the one to prefer.
- A whole run past the baseline is still out of reach, and this ADR does not
  claim otherwise. The integration tier is not shrinking; what moved out of it is
  the part that was never about a real toolchain in the first place.
