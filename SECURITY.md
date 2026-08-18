<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Security policy

## Supported versions

go-mutants is a `0.1.0` pre-release with no published artifact. Only the latest
`main` receives fixes. After the first stable release, the latest patch line
will receive security fixes and older prereleases will not.

## Reporting a vulnerability

Do not open a public issue. Use GitHub's
[private vulnerability reporting](https://github.com/P4suta/go-mutants/security/advisories/new)
on this repository. Include affected versions, impact, and a minimal
reproduction that contains no real secrets. You should receive an
acknowledgement within seven days.

## Scope

In scope, and taken seriously:

- Escaping the snapshot boundary, or any write to an original target workspace,
  including dirty and untracked files.
- Following a symlink, junction, or other reparse point out of a directory the
  tool owns, during snapshotting, reporting, or cache maintenance.
- Executing content that the tool should have treated as data — configuration
  values, report contents, mutant text, or `go` output.
- Failing open where the design says fail closed, in particular Windows Job
  Object ownership and process-tree termination.
- Cache poisoning: reusing an outcome whose inputs have changed.

## Operational notes

A test command is trusted project code. It runs inside a disposable snapshot
with a per-worker temporary directory, which is an isolation boundary for
*files*, not an operating-system sandbox. Reports may contain original and
mutated source plus compiler and test output, so treat CI artifacts with the
same care as the source itself. The tool performs no telemetry and no runtime
network requests, and the HTML report makes none either.
