// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package discover finds the mutation candidates in a snapshot.
//
// Discovery is the first phase that needs a Go toolchain and the first that
// needs types. It loads the snapshot with golang.org/x/tools/go/packages,
// walks the syntax of every file the module owns, and produces two things: the
// candidates a later phase will instrument, and a recorded reason for every
// place it deliberately did not produce one. Nothing is dropped silently —
// "why is there no mutant here?" is a question `--explain` has to be able to
// answer without re-running anything.
//
// # A compiling tree is a precondition
//
// Any package that fails to load or type-check stops discovery with
// [CodePackageErrors]. This is deliberate. Every rule in this package is
// type-directed to some degree — a boolean literal is only a candidate when it
// really is the universe constant, a type argument is only recognisable as a
// type through [types.Info] — and a partially typed tree would silently
// produce a different, smaller catalog rather than an error. Since the run
// would fail at the baseline build minutes later anyway, failing here is both
// faster and more precise: the message names the first few errors and where
// they are.
//
// The single exception is a package that imports "C". Those are excluded from
// mutation wholesale (v1 limitation), so their own build failures are not
// something the user has to fix before mutation testing can start; whatever
// depends on them still fails the gate, because that dependency is real.
//
// # What is never mutated
//
// Test files are structural: `_test.go` is built, type-checked, and run, and
// never mutated. That is not recorded as a skip, because it is not a decision
// about a particular file.
//
// Everything else that is passed over is recorded as a [Skip] with a
// [SkipReason]: whole files (generated code, cgo packages, files the include
// and exclude patterns removed) and individual expressions sitting in a
// context that instrumentation cannot rewrite — constant declarations, array
// lengths, case labels, package-level variable initialisers, and type
// parameter lists or explicit type arguments. The reason reported for an
// expression is the outermost suppressed region containing it: that is the
// region a walker would have declined to descend into, so it is the reason
// that stays true no matter what happens inside it.
//
// # What the build configuration decides
//
// Discovery mutates what the build contains, and the build is a function of
// GOOS, GOARCH, and build tags. Two consequences are worth stating out loud,
// because neither produces a skip:
//
// A file excluded by a build constraint — `foo_linux.go` on Windows, anything
// behind a tag that is not set — is not part of any package here, has no type
// information, and is not mutated. A package whose files are *all* excluded is
// not even matched by `./...`, which is what happens to a pure cgo package when
// CGO_ENABLED is 0: the go command does not build it, does not test it, and
// does not list it, so there is nothing for discovery to skip. A cgo package
// that also holds ordinary Go files does exist under either setting, and is
// then skipped whole, every file named.
//
// # Determinism
//
// Two discoveries over the same bytes produce identical results, field for
// field. Candidates are emitted in (path, span start, rule registry position)
// order and skips in (path, reason) order, both compared byte-wise with no
// locale involved. Nothing downstream — the catalog, the dense runtime
// indices, a shard assignment — can drift because a map was ranged over or a
// directory was read in a different order.
//
// # The go command
//
// go/packages shells out to `go list`, and it finds that executable on the
// *process* PATH rather than on the environment handed to it. [Options.Toolchain]
// is therefore prepended to the child environment's PATH — which is what makes
// the child resolve GOROOT and toolchain lines consistently — but a `go` that
// is not on this process's PATH at all cannot be reached from here. Callers
// that manage their toolchain out of band (mise, asdf) already run go-mutants
// through it; when they have not, [CodeLoadFailed] says so.
//
// The child also runs with GOWORK=off. A snapshot is meant to be the whole
// truth about what is being tested, and a `go.work` in one of its parent
// directories or named by $GOWORK is a file the snapshot does not contain; a
// workspace at the snapshot root itself is a different matter and is refused
// outright with [CodeWorkspace].
package discover
