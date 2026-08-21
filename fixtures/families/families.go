// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package families holds one live candidate for every rule in the v1 catalogue.
//
// One module, one package, and twenty very small functions. Between them they
// carry at least one mutant for each of the forty-two rules the canonical
// registry names, which is what makes this the fixture an end-to-end run of the
// whole catalogue is judged against: the other corpus modules each prove one
// mechanism, and this one proves that every operator family reaches execution.
//
// # Killed and survived are both deliberate
//
// Most functions here are pinned by a test that fails for every mutant of them.
// A few are deliberately under-tested: the test calls them — so their mutants
// are covered, and really executed — and then asserts something every mutant
// also satisfies. Those survivors are as load-bearing as the kills. A run in
// which everything died would be indistinguishable from a run whose tests are
// simply strong, and a run in which everything survived would be activation
// that never happened; only a fixture with both fates in it can tell the two
// apart. fixtures/README.md names every under-tested function and says what its
// test leaves out.
//
// One function, [Orphan], is not called at all. It is the third fate: nothing
// reaches its line, so coverage-guided selection settles both of its mutants
// without executing either.
//
// # Every loop here terminates under every mutant of it
//
// That is a property of the fixture rather than a happy accident, and it is the
// one invariant an edit here is most likely to break. `negate-loop-condition`
// turns `for i := 0; i < limit; i++` into a loop that never ends when `limit` is
// not positive, and `gt-to-ge` does the same to a counter running down to zero.
// A mutant that hangs is reported as a timeout after five times the baseline,
// which turns a fast fixture into a slow one and an exact expectation into a
// vague one.
//
// So the loops here either run over a length no rule can rewrite — `for range`
// has no condition to negate — or take a bound the tests never hand a
// degenerate value to. [Steps] says which value that is; adding a zero row to
// its test would re-arm the landmine.
package families
