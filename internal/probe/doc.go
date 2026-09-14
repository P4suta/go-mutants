// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package probe decides what a run may skip on the strength of an infection
// log.
//
// # What the evidence is
//
// A probe tree is the original program with a report attached: nothing is
// activated, and each site records, per mutant, whether this pass could rule
// that mutant out. internal/instrument writes the trees and the log format,
// internal/execute runs the passes, and this package is the *rule* — the step
// between "these indices appeared in these logs" and "this mutant need not be
// executed".
//
// It is a separate package for internal/coverage's reason. The decision is
// pure: it reads facts and produces verdicts, starts no process, touches no
// file, and can therefore be tested against every shape of evidence a run could
// produce rather than against the shapes a fixture happens to make.
//
// # The rule, and why it is safe
//
// For a mutant m covered by binaries C(m), if every binary in C(m) produced
// facts and none of them names m, then no test in this run could observe m: the
// mutant is a survivor, and executing it would establish what is already known.
// Otherwise the run executes m — narrowed, where it can be, to the binaries
// that named it, since the others have already said they could not see it.
//
// Three things make that sound, and each is a way the rule stays conservative
// when something goes wrong:
//
//   - **A mutant with no probe form is never settled.** Its site is not in the
//     probe tree at all, so its absence from every log means nothing. Only
//     internal/instrument knows which forms exist, so only it can say, and the
//     answer arrives here as a field rather than being re-derived.
//   - **A binary that produced no facts is never read as silence.** A pass that
//     failed, timed out, or could not write its log is a binary nothing is
//     known about, so every mutant it covers is executed.
//   - **A mutant with no covering binaries is never settled.** The empty
//     intersection is vacuously "no binary named it", which is the same bytes
//     as "every covering binary ran and saw nothing" and would license skipping
//     a mutant nothing ever looked at. Coverage settles those before this
//     package is asked, and this refuses them anyway.
//
// # What it deliberately does not do
//
// The evidence is per *binary*, never per test, and internal/probe would be the
// place to change that. It is not a gap left for later: a test profiled alone
// is a different execution from the same test inside its suite — shared state,
// ordering, `TestMain` — so a per-test log licenses less than it appears to,
// and buying that licence means paying the controls internal/engine's set
// verifier already pays, a second time, for a second soundness argument. The
// whole-binary rule needs neither, and it subsumes ADR 0010's confirmation
// pass: where a probe proves a binary could not observe a mutant, the run that
// would have confirmed a survivor against that binary is not skipped but
// *proved* unnecessary.
package probe
