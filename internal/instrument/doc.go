// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package instrument rewrites snapshot source bytes so that every compilable
// mutant of a file lives in the file at once, dormant behind a guard.
//
// [Instrument] is the whole of it: point it at a snapshot and a catalogue and
// it rewrites the files the catalogue names, generates the runtime package that
// decides which mutant is live, and imports that package into every file it
// touched. One build then serves every mutant, and activating one costs an
// environment variable per test process rather than a rebuild.
//
// This file documents the invariants the whole package is built to hold. The
// two lowest pieces — the flattener and the splicer — are the byte-level
// foundation the guard forms are composed from, and each invariant below is
// either enforced by one of them or asserted by them on behalf of a caller.
//
// # The guard form
//
// v1 emits one guard form, Form C, which rewrites a bool-valued expression into
// a selector over its alternatives:
//
//	(__gm.M[3] && (a >= b) || !(__gm.M[3]) && (a > b))
//
// Both branches are ordinary expressions in the site's own context, so the
// compiler settles typing, evaluation order and short-circuiting, and exactly
// one branch is ever evaluated. With every flag false — the instrumented
// baseline — the branch that runs is the original bytes, byte for byte.
//
// A helper call taking the operands (`__gm.Cmp(3, a, b)`) would have been
// shorter and is not usable: it breaks on untyped constants, on shifts, and on
// named types, all of which the guard form leaves to the compiler.
//
// Several mutants of one expression are alternatives inside a single guard
// rather than nested guards — mutants are mutually exclusive, and a chain says
// so — while genuinely nested expressions become nested guards, composed
// children first so that an enclosing site is rendered from text its children
// have already finished. Each alternative is rendered from the pristine source
// with that one candidate's edit applied and nothing else, because a mutant is
// one edit to the program the user wrote; the branch that keeps the original is
// the only one that carries the guards of the sites nested inside it.
//
// The statement and declaration forms the design calls for — and with them the
// families whose edits are not bool-valued expressions — are not in this
// version. A catalogued mutant from such a family is refused by name
// ([CodeUnsupportedFamily]) rather than guarded as something it is not.
//
// # Named boolean types are the validation phase's problem
//
// Form C evaluates to `bool`. Where the site's context wants a named boolean
// type — `type Flag bool`, and a site that is one, initialises one, is assigned
// to one, or is returned as one — the rewritten file does not compile, because
// `bool` is not assignable to `Flag`.
//
// This package instruments those candidates anyway, deliberately. Deciding it
// here would mean type-checking every file to ask a question the compiler is
// about to answer for free, and answering it conservatively would silently drop
// candidates that were fine. The next phase compiles the instrumented tree and
// bisects the failures, so a candidate whose guard cannot compile is rejected
// individually, with a reason, and lands in the run's rejected set. That is the
// contract: instrumentation is a byte rewrite that always produces the same
// bytes for the same input, and whether those bytes compile is established by
// compiling them.
//
// # The generated runtime
//
// Activation lives in a package generated into the snapshot at
// [Result.RuntimeDir], holding one bool per catalogued mutant and a table from
// mutant ID to index. Its `init` reads [ActiveEnv]: empty is the instrumented
// baseline, a known ID sets exactly one flag, and an unknown ID makes the
// process exit [UnknownMutantExit] instead of running the tests — a stale
// catalogue that activated nothing would otherwise report survivors for mutants
// that were never live.
//
// The package is first-party to the module under test, so no go.mod edit and no
// vendor entry is needed, and its directory may not begin with "_" or ".": the
// go tool ignores such directories, and a runtime hidden in one would never be
// built.
//
// Files that received guards import it under an alias, injected as an insertion
// that holds no line break so that the import section keeps its shape and the
// file keeps its line numbering. The alias dodges every name in two scopes: the
// identifiers the file itself spells, which an import would otherwise shadow or
// be shadowed by, and the identifiers the package block binds anywhere in the
// package, which Go refuses to see declared a second time by a file-scoped
// import. The second means a directory read: a `var __gm` in a sibling file
// makes `import __gm "…"` here a compile error, and no amount of looking at
// this file alone would say so. A file with no guards is not touched at all,
// since an unused import does not compile.
//
// # Line preservation
//
// Instrumentation preserves the line number of every byte of the original
// file. Nothing is ever inserted on a line of its own: a rewrite splices its
// guard onto the same line as the statement it guards, and the mutated copy it
// carries is folded onto that line by [Flatten]. This is not a cosmetic
// preference. Go's coverage data addresses statements by line (columns do not
// survive `go tool covdata textfmt`), so coverage collected from an
// instrumented binary is only usable against the pristine tree if line numbers
// on both sides denote the same code. Preserving them makes that mapping 1:1;
// giving it up would mean re-deriving coverage against instrumented sources,
// and every panic trace, every compiler diagnostic, and every reported mutant
// coordinate would point at a line the user cannot find in their editor.
//
// [LinePreserving] is the machine-checkable form of this invariant, and
// callers assert it over the splices they are about to apply. It holds exactly
// when each replacement contains as many line breaks as the original bytes it
// replaces — see its documentation for why per-splice equality is the right
// statement rather than "the replacement is one line".
//
// # Byte fidelity
//
// Rewriting is a byte edit, never an AST pretty-print. Comments, alignment,
// build tags, and CRLF line endings in the surrounding file survive a mutation
// untouched, because everything outside a spliced span is copied verbatim.
// Inside a guard, the branch that reproduces the original behaviour keeps the
// original bytes byte-for-byte; only the mutated copy is re-rendered, and only
// as far as [Flatten] must go to fit it on one line.
//
// # No silent wrong edit
//
// A byte edit derived from a span computed by another package is a standing
// invitation to corrupt somebody's source when the two disagree about what the
// span covers. [Apply] therefore refuses to edit anything until every splice
// has proved that the bytes it claims to replace are the bytes actually there,
// and reports a [CodeSpliceMismatch] error when they are not. The alternative
// — writing the replacement wherever the offsets happen to land — turns a
// stale span into a plausible-looking mutant that tests something nobody
// asked for.
//
// # Meaning preservation
//
// [Flatten] is the only place in the package that re-renders Go source, and it
// is allowed exactly two token rewrites, both of them a literal that carries a
// line break inside itself being re-spelled as the same value without one: a
// raw string literal spanning lines becomes an interpreted literal, and an
// interpreted string or rune literal holding a raw carriage return has that
// carriage return escaped. Everything else is the original token,
// byte-for-byte, joined by whatever separator keeps the stream tokenizing the
// same way. Flatten proves that on every call by
// re-scanning its own output and comparing the token stream with the one it
// emitted, so a spacing bug is a loud [CodeNotIdentical] error rather than a
// silently different program.
package instrument
