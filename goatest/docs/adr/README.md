<!--
SPDX-FileCopyrightText: 2026 goatest contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# Architecture decision records

A record here states a decision that is expensive to revisit and cheap to
forget: what was decided, what it was decided against, and what it costs. The
body is not revised once accepted. A decision that supersedes another says so in
its own Status line, and the record it replaces gains one line saying which one
replaced it.

`internal/devgates` pins this index to the directory beside it, in both
directions: a record with no line here fails, and a line here naming a record
the tree no longer holds fails as loudly.

| Record | Decides |
| --- | --- |
| [0001](0014-seam-policy.md) | Test seams are arguments, not package-level variables |
| [0002](0015-trace-is-not-evidence.md) | A trace is not evidence |
| [0003](0016-no-replay-engine.md) | No general record/replay engine |
| [0004](0017-proof-layers-not-budgets.md) | Proof layers, not budgets |
| [0005](0018-build-cache-goatest-owns.md) | A build cache goatest owns, and what may write to it |
| [0006](0019-every-temporary-directory-has-an-owner.md) | Every temporary directory has an owner |
| [0007](0020-survived-evidence-is-universal.md) | Survived evidence is a universal proposition over the reaching set |
| [0008](0021-controls-before-timeouts.md) | Controls before timeouts |
| [0009](0022-parallel-measurement-serial-commit.md) | Parallel measurement, serial commit |
| [0010](0023-whole-suite-reach-before-fallback.md) | Whole-suite reach before fallback execution |
| [0011](0024-append-only-checkpoint-journal.md) | Append-only checkpoint journal |
| [0012](0025-aggregate-proof-before-timeout.md) | Exact compatible-group mutation proofs |
| [0013](0026-preserve-block-routing-across-resume.md) | Preserve block routing across resume |
| [0014](0027-resume-complete-probe-phase.md) | Resume a complete probe phase |
| [0015](0028-execute-framed-baselines-directly.md) | Execute framed baseline targets directly |
| [0016](0029-publish-every-baseline-control.md) | Publish every completed baseline control |
| [0017](0030-project-controls-use-a-native-cache-projection.md) | Project controls use a native cache projection |
| [0018](0031-confirm-comparative-watchdogs.md) | Fail closed at comparative watchdogs |
| [0019](0032-instrument-the-test-binary-import-closure.md) | Instrument the test binary import closure |
| [0020](0033-bootstrap-cold-preparation-with-verified-local-work.md) | Bootstrap cold preparation with verified local work |
| [0021](0034-what-a-ledger-cannot-check.md) | What a ledger cannot check |
| [0038](0038-a-request-with-no-prior-observation-measures-its-own.md) | A request with no prior observation measures its own |
