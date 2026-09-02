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
// # The three guard forms
//
// A rewrite site is a bool-valued expression, a statement, or a statement that
// declares — and each has a form of its own. Which one a mutant gets is not
// decided here: it is [Hints], the site hint internal/discover computes with
// the type information this package deliberately does not have.
//
// Form C rewrites a bool-valued expression into a selector over its
// alternatives:
//
//	(__gm.M[3] && (a >= b) || !(__gm.M[3]) && (a > b))
//
// Form S wraps a statement in a branch chain, with the statement's own bytes in
// the `else`:
//
//	if __gm.M[7] { n = a - b } else { n = a + b }
//
// Form D is Form S for a statement that declares. The declarations are hoisted
// out in front of the guard, so that the names stay in the scope the rest of
// the function reads them from, and both branches assign to them:
//
//	var n int; if __gm.M[9] { n = a - b } else { n = a + b }
//
// In every form both branches are ordinary code in the site's own context, so
// the compiler settles typing, evaluation order and short-circuiting, and
// exactly one of them is ever taken. With every flag false — the instrumented
// baseline — what runs is the original bytes, byte for byte.
//
// A helper call taking the operands (`__gm.Cmp(3, a, b)`) would have been
// shorter and is not usable: it breaks on untyped constants, on shifts, and on
// named types, all of which the guard forms leave to the compiler.
//
// Several mutants of one site are alternatives inside a single guard rather
// than nested guards — mutants are mutually exclusive, and a chain says so —
// whatever families they come from: an arithmetic swap and a deletion of the
// statement it sits in are two branches of one chain. Genuinely nested sites
// become nested guards, composed children first so that an enclosing site is
// rendered from text its children have already finished. Each alternative is
// rendered from the pristine source with that one candidate's edit applied and
// nothing else, because a mutant is one edit to the program the user wrote; the
// branch that keeps the original is the only one that carries the guards of the
// sites nested inside it.
//
// A statement-deletion mutant is the branch chain's one degenerate case, and it
// is not a special case at all: its edit replaces the whole statement with
// nothing, so its branch renders empty — `if __gm.M[4] { } else { … }` — which
// is exactly what "this statement does not run" has to mean.
//
// # Whether the guard compiles is the validation phase's problem
//
// Nothing here type-checks anything, and a mutated copy can still be a program
// the compiler refuses: `x * 0` swapped into `x / 0` is a constant division by
// zero, and an operator swap can push an untyped constant out of the range its
// context allows.
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
// # The probe runtime
//
// [ModeProbe] generates a different package into a different snapshot, for the
// tree a probe pass runs: one in which no mutant is ever active, so the original
// semantics execute and each site reports, without side effects, whether the
// mutated value would have differed. Its one export is `Infect(i uint32)`,
// which records mutant i's dense index the first time that site is seen to
// differ and never again, and its `init` reads [ProbeEnv] for the file to append
// those indices to. Nothing in the environment is an ordinary run: the runtime
// is linked in, records nothing, and costs a nil check.
//
// A probe that cannot write its log exits [ProbeUnavailableExit] rather than
// carrying on, for the reason an unknown mutant ID stops the other runtime. An
// empty log reads exactly like a run in which no site was ever infected, and
// that reading is what licenses a consumer to skip an execution, so silence is
// the one thing a probe may never answer with.
//
// The log is the append-only `gomutants-infection-v1` format: a header line
// naming the format, the catalogue digest and the array width, then one decimal
// index per line. Several test processes append to one file, so the header
// appears once per process rather than once per file, and every occurrence has
// to be identical. [ReadInfectionLog] reads it back, fail-closed. It is given
// the catalogue's size rather than that width, derives the width itself through
// the rule the generators size the array with, and bounds every index by the
// size — so an empty catalogue's log is readable and admits no index at all.
// See its documentation for why a partial answer is the one thing it must not
// return.
//
// # The probe forms
//
// One form is written, for the return-value rules, whose replacement is always
// a constant K. A `return` carrying such a mutant at result position j becomes
//
//	{ var r0 T0 = E0; var r1 T1 = E1; …; if rj != K { __gm.Infect(i) }; return r0, r1, … }
//
// where Tj is the *declared result type* of the enclosing function, which is
// the conversion the `return` itself performs. Go evaluates a return's operands
// once each and then assigns them to the results, so this evaluates nothing
// twice and nothing extra: it is the same program, with one comparison and one
// call added after the values are in hand. probe.go states the argument in full
// — why the comparison is total, why the block is still a terminating
// statement, and the one case where IEEE equality makes the probe report less
// than it could.
//
// Everything else is unprobed. A mutant of another family is catalogued and
// mutated exactly as before and simply not measured, so a file holding only
// such mutants comes out of [ModeProbe] byte for byte as its author wrote it —
// with no runtime import, since an unused import does not compile. A run then
// learns nothing about which tests could observe those mutants and executes
// them all, which is the safe direction and the one this mode always errs in.
//
// The temporaries are named from the same alias the runtime import takes, and
// checked against the same two scopes. The reason is capture rather than
// shadowing: a declared name's scope begins at the end of its own
// specification, so a temporary named for the first result is in scope for the
// second one's initialiser, and a file already spelling that name would have
// its second operand quietly rebound.
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
