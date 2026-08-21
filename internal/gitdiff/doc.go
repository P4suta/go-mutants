// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package gitdiff answers one question: which lines of the workspace have
// changed since a given ref.
//
// It exists for `--changed`, which narrows a run to the mutants sitting on
// edited lines. That is a selection decision and nothing more: discovery,
// validation and the catalogue still cover the whole module, so the ids in a
// `--changed` report are the ids a full run would have minted and the two
// documents can be compared mutant for mutant.
//
// # What it reads, and where
//
// The original workspace, never the snapshot. A snapshot deliberately excludes
// `.git`, so a diff taken inside one would find no repository at all — and the
// question "what did you change" is a question about the tree the developer is
// working in.
//
// Every invocation is read-only: `rev-parse`, `merge-base`, `diff`,
// `ls-files`. Nothing here writes to the repository, stages anything, or
// changes a ref, and a failure to run git at all is reported rather than
// guessed around — a `--changed` run that quietly fell back to "everything
// changed" would execute a full catalogue while claiming to be a diff run, and
// one that fell back to "nothing changed" would report a perfect score for a
// run that measured nothing.
//
// # What the diff is taken against
//
// The merge base of the named ref and HEAD, not the ref itself. That is the
// commit the branch actually left, so the changed set is the work of this
// branch rather than the work of everybody who has pushed to the target since
// it was cut — which is what makes `--changed origin/main` mean "my changes"
// on a branch that is a week behind.
//
// The diff runs against the working tree, so uncommitted edits count. A
// developer asking "did my tests catch what I just wrote" has usually not
// committed it yet.
//
// Files git has not been told about count too, as the whole of themselves. A
// file with no index entry has nothing for `git diff` to compare it against, so
// a selection built on the diff alone would find every mutant on an edited line
// of a tracked file and none at all in a file written from scratch — the same
// afternoon's work measured or not depending on whether it happened to be `git
// add`ed, and the unstaged half reported as a score of nothing. Every line of
// such a file is new, so what is recorded is what the diff itself will say the
// moment the file is added. Ignored files are left out: a repository that
// ignores a tree has said it is not source.
//
// # v1 limitations
//
// Rename detection is off. A renamed file is reported as every line added,
// which selects more mutants than strictly necessary and never fewer; the
// alternative — mapping lines through a rename — would have to be exactly right
// to be worth anything, since a mistake there hides mutants rather than adding
// them.
//
// Only the subtree under the workspace root is considered. A module inside a
// larger repository is diffed with its own directory as the pathspec, and paths
// come back relative to the module root, which is the form the catalogue speaks.
package gitdiff
