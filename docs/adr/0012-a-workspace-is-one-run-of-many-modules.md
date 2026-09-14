<!--
SPDX-FileCopyrightText: 2026 go-mutants contributors
SPDX-License-Identifier: MIT OR Apache-2.0
-->

# 0012 — A workspace is one run over many modules

## Status

Accepted, 2026-09-14. Implemented by `internal/discover`'s `DetectWorkspace` and
`DiscoverWorkspace`, by `mutation.Identity.ModulePath` and its second domain, by
`instrument.Options.Module`, by `validate.Options.Modules`, by
`execute.Options.Workspace`, and by `report.WorkspaceReport` and the
`go-mutants/workspace-report` schema it publishes.
[ADR 0010](0010-narrowing-to-tests-is-sound.md) is the reason the modules cannot
be measured separately: a mutant's verdict is a statement about every test that
covers it, and in a workspace those tests need not be in its own module.

## Context

Until this record a `go.work` at the root was refused outright, and the refusal
said what it cost: one module path, one set of module-relative identities, one
baseline. Every one of those assumptions is load-bearing somewhere, and the
refusal was the honest answer while they held.

Two readings of "support workspaces" were available and they are not the same
feature.

The first is N runs that happen to share a directory: measure each module the
way a single-module run measures one, and report N times. It is much the
simpler, and it is **wrong** in a way a user would find out from a score. A
module's tests routinely cover a sibling module's code — that is what a
workspace is for — so a mutant of `lib` that only `app`'s tests can kill would
be reported as a survivor by `lib`'s run, and `app`'s run would never see it.
The run would be reporting that the suite does not catch something the suite
catches.

The second is one run over one catalogue that spans the modules. Then a mutant
of `lib` is executed against every test binary that covers it, whichever module
compiled it, and the verdict means what it means everywhere else in this tool.

## Decision

A workspace is measured as **one run**: one snapshot, one catalogue, one
validation, one baseline, one execution — and reported as **one run report per
module**, plus a `go-mutants/workspace-report` that names them.

Six things make that work, and each is a decision that could have gone
otherwise.

1. **A mutant of a workspace has a module path, under a domain of its own.**
   Two modules can each hold an `app.go`, and both normalize to `app.go` against
   their own module root, so the frozen nine-field recipe would mint one
   identity for two mutants. `Identity.ModulePath` is a tenth field, hashed
   under `IDDomainWorkspace`, and the nine-field recipe under `IDDomain` is byte
   for byte what it was.

   A workspace-relative path would have avoided the tenth field and would have
   been worse: it changes the identity of every mutant of a module the moment
   somebody measures that module from one directory up, silently. The cost of
   the second domain is stated instead of hidden — a module measured alone and
   the same module measured in its workspace mint different identities, so
   neither the cache, nor a stored report, nor an expectation crosses between
   them. That is the truthful answer to "what is the same mutant" when a
   module-relative path is the only coordinate there is.

2. **The catalogue is keyed on the module too, in all four places a path was a
   coordinate.** Two candidates naming one path in two modules are two files:
   the source-digest and original-text conflicts, the canonical order and the
   deduplication key all take the module first. The module comes first in the
   order so that a module's mutants are contiguous in the catalogue and in the
   dense index assigned from it. A catalogue holding some candidates that name a
   module and some that do not is refused: that is a catalogue whose mutants
   were minted under two different recipes, and no run produces one.

3. **Instrumentation is per module; every runtime carries the whole
   catalogue.** A module's files can only import a runtime its own module
   declares — a generated package under `first/` is not on `second/`'s import
   path without a `require`, and editing a go.mod inside the snapshot is editing
   the tree under test — so each module gets a pass and a runtime of its own.
   Each of those runtimes is generated from the *whole* catalogue, because a
   mutant of one module is activated while another module's tests are running;
   a runtime knowing only its own module's indices would meet an id it had never
   heard of and exit as if the snapshot were stale, turning every cross-module
   mutant into an infrastructure error.

4. **The one workspace file the tree carries is obeyed, and no other.**
   Everything that reaches for a `go` command runs with `GOWORK` pinned, and
   until now it was pinned to `off`: the go command searches every parent
   directory and obeys `$GOWORK`, so a snapshot placed below somebody's
   workspace would resolve against a file the snapshot does not contain. A
   workspace run *removes* it instead, and lets the go command find the
   workspace file by walking up from the directory it is running in. That is
   the snapshot's own, or a worker's copy of it under `--isolate`, and in both
   cases it is the file beside the code being built.

   Removed rather than named, and the difference is not cosmetic. A named path
   has a spelling and a working directory has another: a temporary directory
   reached through a symlink — which is every one of them on macOS — gives the
   go command a resolved working directory and an unresolved `GOWORK`, and it
   compares the two as text. `directory prefix app does not contain modules
   listed in go.work`, about a module that is right there, is what that looks
   like. Removed rather than emptied, too: the go command reads an empty
   `GOWORK` as "no workspace" rather than as "decide for yourself". The
   sentence the pin exists for is unchanged — the snapshot is the whole truth,
   and the caller's own `$GOWORK` still decides nothing — and what stops being
   true is only "there is no such file".

   `./...` is expanded into one `./<dir>/...` per module for the same kind of
   reason: the go command does not accept `./...` at a workspace root, because
   the root is not itself a module, and every module's own `./...` is what the
   user meant by it.

5. **A workspace file that cannot be measured is refused before anything is
   copied.** `GOM4102` named exactly one condition before this record and names
   exactly one now: a `go.work` this run cannot proceed on. What changed is
   which workspaces those are — one that does not parse, uses no module, names a
   directory that is not a module, joins two modules spelling one module path,
   or reaches outside the snapshot with a `use` or a filesystem `replace`. The
   last is the snapshot argument again: a module the snapshot does not carry is
   one the measurement cannot be reproduced from.

6. **One baseline, and the report splits rather than the run.** A per-module
   baseline was considered and rejected. The two reasons for it were a tighter
   derived timeout and a red module failing the run; the second is already true
   of one baseline — a failing test anywhere fails it — and the first is a
   statement about how long a run takes, which is not a thing this project
   sizes its decisions on. What a per-module baseline *would* have cost is a
   second answer to "which tests measure this mutant", which is exactly the
   question ADR 0010 settles once.

   So the split happens where it is real: `workspace.module_path` is required by
   the run report and a workspace has no single answer for it, so each module
   gets a run report of its own, carrying its own module path, its own mutants
   and its own summary. The fields that describe the run rather than the module
   — the timing, the baseline, the platform, the workspace digest — are the
   run's in every one of them, because that is what produced the numbers. The
   `go-mutants/workspace-report` is the document that says which reports belong
   together and what they add up to.

## Consequences

A workspace run's identities do not cross to a single-module run of the same
module, in either direction. That is point 1's cost, and it is paid by the
cache, the history store and every `[[mutation.expect]]` row. A project that
measures both ways keeps two ledgers, and the domain in the identity is what
makes that visible rather than mysterious.

The derived per-mutant timeout of a workspace is sized on the slowest baseline
run of the whole workspace, so a fast module's mutants get a budget a fast
module would not have needed. Nothing is reported differently because of it.

`report merge` and the history store already key a stored run on its module
path, so N module reports under one run id are the shape `--shard` already
produces and needed no schema change; the workspace report is a new document
type rather than a second version of the run report, which is the extension
point `internal/schemas` was built for.

A module with no mutants is still one of the workspace's modules: it is
instrumented (its runtime is written), it is reported, and its report is empty.
Leaving it out would make "which modules does this workspace hold" a question
answered by the catalogue, which is a different question.
