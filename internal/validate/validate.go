// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"bytes"
	"context"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/trace"
)

// DefaultBuildTimeout bounds one `go build ./...` when [Options.BuildTimeout]
// is not set.
//
// It is the same fixed, generous budget internal/engine puts on a baseline
// command, and for the same reason: this phase runs before there is any
// measurement to derive a budget from, so the number's only job is to stop a
// hung toolchain from hanging the run forever.
const DefaultBuildTimeout = 10 * time.Minute

// Options is everything [Validate] needs.
//
// The zero value is not usable: a snapshot, a catalogue, a module path and a
// located toolchain have no defaults that could not be somebody's working tree
// or somebody else's `go`.
type Options struct {
	// Snap is the snapshot to validate. It is rewritten in place — that is what
	// a snapshot is for — and it must be the same copy the catalogue was
	// discovered in.
	Snap *snapshot.Snapshot

	// Catalog is the mutant set to instrument and validate. Its dense indices
	// size the generated activation array, so the catalogue validated here and
	// the catalogue the runner activates against must be the same one.
	Catalog *mutation.Catalog

	// Hints are the rewrite sites discovery chose, one per catalogued mutant,
	// as [instrument.HintsOf] indexes them. They travel through this phase
	// untouched: every rewrite it makes, the first and every one the search
	// makes afterwards, is composed from the same hints against the same
	// pristine bytes.
	Hints instrument.Hints

	// Modules are the modules the snapshot holds, in the order the phase
	// instruments them. A single-module snapshot is one entry rooted at `.`;
	// a workspace is one entry per `use` line.
	//
	// A module at a time is how a workspace has to be instrumented, because a
	// module's files can only import a runtime its own module declares: a
	// generated package under `first/` is not on `second/`'s import path
	// without a `require`, and editing a go.mod inside the snapshot is editing
	// the tree under test. Each module therefore gets a pass and a runtime of
	// its own, and each of those runtimes carries the whole catalogue.
	Modules []Module

	// Toolchain is the located Go toolchain every build goes through.
	Toolchain gocmd.Toolchain

	// Jobs is the build parallelism, passed to `go build -p`. Zero or negative
	// leaves the go command to its own default. It is the *compiler's*
	// parallelism and never this phase's: the builds are strictly serial,
	// because each one is a statement about the bytes currently in the snapshot
	// and two concurrent builds of one directory would be statements about
	// bytes neither of them chose.
	Jobs int

	// BuildTimeout bounds one build. Zero selects [DefaultBuildTimeout].
	BuildTimeout time.Duration

	// Env is the complete environment for every build, in "KEY=VALUE" form. Nil
	// inherits this process's environment, which is os/exec's rule; a run
	// composes the set explicitly so that a GOFLAGS or a GOWORK from the
	// developer's shell cannot decide what the snapshot resolves against.
	Env []string

	// Mode selects which tree is instrumented and validated. The zero value is
	// [instrument.ModeMutant], so a caller written before the probe tree existed
	// keeps validating exactly what it always did.
	//
	// Everything this phase does is the same either way — one build, the
	// pristine gate, per-file isolation, rejections in catalogue order — and so
	// is the meaning of accepting a mutant. What changes is what a *rejection*
	// says: in the mutant tree it is "this mutant cannot be compiled and will
	// not be run", and in the probe tree it is "this mutant's probe site cannot
	// be compiled and will not be measured". The mutant itself is untouched
	// either way, since the two trees are different snapshots.
	//
	// The result does not carry the mode back. A caller that cannot say which
	// tree it asked for has a bigger problem than this field would solve, and a
	// [Result] that answered it would invite exactly the code that reads the
	// answer instead of knowing it.
	Mode instrument.Mode

	Packages []string

	// Trace is where this phase's builds and bisection steps are recorded. A
	// nil recorder is the disabled trace and every method on it is safe, which
	// is why everything here records unconditionally: the traced path and the
	// untraced one are the same path, and no accepted set can come to depend on
	// which of them a run took.
	Trace *trace.Recorder
}

// A Module is one module of the tree being validated.
type Module struct {
	// Dir is the module root relative to the snapshot root, slash-separated,
	// and "." for the module at the root itself.
	Dir string
	// Path is the module's import path, which the generated runtime's import
	// path is built from.
	Path string
}

// mutants reports which catalogued mutants belong to this module, which is
// what a mutant's own [mutation.Candidate.ModulePath] says -- and the empty
// string when the catalogue names no module, because then there is one module
// and everything belongs to it.
func (m Module) mutants(catalog *mutation.Catalog) string {
	if first, ok := catalog.At(0); ok && first.ModulePath == "" {
		return ""
	}
	return m.Path
}

// A Rejection is one catalogued mutant that cannot be compiled, and the
// compiler's own explanation of why.
//
// Rejections are data rather than errors. A candidate whose guard does not
// compile is an ordinary, expected outcome of the design — see internal/instrument
// on why compiling is how that is established — and the run reports it, scores
// around it, and carries on. What must never happen is a candidate disappearing silently, which is why
// every field here is filled in: an ID nothing can look up, a coordinate nobody
// can jump to, or a rejection with no diagnostic would each amount to the same
// silence in a different disguise.
type Rejection struct {
	// ID is the full 64 hex character stable identity.
	ID string
	// DisplayID is the short form, as the console prints it.
	DisplayID string
	// Path is the '/'-normalized module-relative source path.
	Path string
	// Line is the 1-based line the candidate's span starts on, and Column the
	// 1-based byte offset within that line — the same coordinates discovery
	// reports, so a rejected mutant and a live one are named the same way.
	Line   int
	Column int
	// Rule is the operator that proposed the edit.
	Rule string
	// Diagnostic is what the compiler said about the build that condemned this
	// candidate, location prefix and all.
	Diagnostic string
}

// A Result is everything one validation pass established.
type Result struct {
	// AcceptedIDs are the mutants that compile, in catalogue order. It is the
	// set the execution phase may run, and its order is the catalogue's so that
	// two runs over one workspace produce the same sequence.
	//
	// It means "compiles" only when [Validate] returned no error. Alongside an
	// error it is what had not been rejected when the phase stopped, which is
	// something to report and never something to run.
	AcceptedIDs []string

	// Rejected are the mutants that do not compile, in catalogue order.
	Rejected []Rejection

	// Instrumented describes the snapshot as it finally stands: the guards that
	// survived validation, keyed by the path they sit at *relative to the
	// snapshot root*. Its GuardsByFile and FilesInstrumented are the state
	// after isolation rather than before it, so a file whose every candidate
	// was rejected is absent from both.
	//
	// Its RuntimeDir and RuntimeImport name the tree's one generated runtime,
	// and are empty for a workspace, where there is one per module and no
	// single answer to name. [Result.Runtimes] is the field that always has
	// the answer.
	Instrumented instrument.Result

	// Runtimes are the generated runtime packages this pass wrote, one per
	// module, in [Options.Modules] order. Their RuntimeDir is relative to the
	// snapshot root, as everything else here is.
	//
	// Their FilesInstrumented and GuardsByFile are the state *before* the
	// search, which is what makes [Result.Instrumented] the field to read for
	// what is in the tree now: a file whose every candidate was rejected is
	// named here and absent there. Only the runtime directories are the same
	// either way, because a runtime is written once and never regenerated.
	Runtimes []instrument.Result

	// Builds is how many `go build` invocations the phase spent. One means the
	// whole catalogue compiled on the first try, which is the ordinary case and
	// the one the schemata design exists to make ordinary.
	Builds int
}

// RuntimeDirs is where every generated runtime package sits, relative to the
// snapshot root, in module order.
//
// It is what the drift gate needs and the one thing [Result.Runtimes] carries
// that is true after the search as well as before it.
func (r Result) RuntimeDirs() []string {
	dirs := make([]string, 0, len(r.Runtimes))
	for _, runtime := range r.Runtimes {
		if runtime.RuntimeDir != "" {
			dirs = append(dirs, runtime.RuntimeDir)
		}
	}
	return dirs
}

// Validate instruments the snapshot with the whole catalogue and establishes,
// by compiling it, which mutants are real.
//
// The fast path is one build. Instrumentation writes every catalogued mutant
// into the tree at once, `go build ./...` compiles it, and a green build means
// every candidate is accepted — no bisection, no second compile, nothing else
// to decide.
//
// A red build means at least one guard cannot compile, and the work is to find
// out which without losing the ones that can. Every catalogued file is restored
// to its pristine bytes and the tree is built again: if it still fails, nothing
// this phase could reject would fix it and it says so ([CodeNotMutantInduced])
// rather than bisecting a tree that was already broken. Otherwise the files the
// compiler named are searched one at a time — halving while that is cheaper,
// scanning when it is not, verifying every join — and each ends up carrying the
// largest subset of its candidates that was seen to compile. The undecided
// files go back in, the tree is built again, and whatever the compiler names
// this time is searched in the same way, until a build comes back green.
//
// Determinism is a promise about the whole of that: the same snapshot and the
// same catalogue produce the same accepted set, the same rejections in
// catalogue order, and the same bytes on disk. It rests on the compiler being
// deterministic too — the same source naming the same files — which is a
// property go builds have and is worth stating because the search would inherit
// any drift in it.
//
// On failure the Result is returned filled in as far as the phase got, so that
// a caller can report what was established before it stopped.
func Validate(ctx context.Context, opts Options) (Result, error) {
	if err := opts.validate(); err != nil {
		return Result{}, err
	}
	v := &validator{
		root:      opts.Snap.Root,
		catalog:   opts.Catalog,
		hints:     opts.Hints,
		mode:      opts.Mode,
		packages:  slices.Clone(opts.Packages),
		toolchain: opts.Toolchain,
		jobs:      opts.Jobs,
		timeout:   opts.BuildTimeout,
		env:       opts.Env,
		recorder:  opts.Trace,
		modules:   slices.Clone(opts.Modules),
		byPath:    make(map[string][]mutation.Mutant),
		pristine:  make(map[string][]byte),
		guards:    make(map[string]int),
		files:     make(map[string]fileRef),
	}
	v.timeout = buildTimeout(opts.BuildTimeout)
	v.build = v.buildSnapshot
	v.apply = v.instrumentFile
	return v.run(ctx)
}

// buildTimeout is the bound one build of the snapshot runs under.
//
// It is a function rather than a branch inside the constructor for the reason
// internal/gocmd's sameEnvKeyOn is one: a decision written where it is made is
// a decision only a test that can make the whole phase run may ask about, and
// this one is a number a reader of `-v` output sees. Zero and anything below it
// mean "the caller did not choose", which is one statement and not two: a
// negative bound is not a shorter build, it is a caller that set nothing.
func buildTimeout(configured time.Duration) time.Duration {
	if configured <= 0 {
		return DefaultBuildTimeout
	}
	return configured
}

// validate rejects options that cannot describe a validation pass.
func (o Options) validate() error {
	switch {
	case o.Snap == nil:
		return &Error{Code: CodeOptions, Message: "no snapshot was given"}
	case strings.TrimSpace(o.Snap.Root) == "":
		return &Error{Code: CodeOptions, Message: "the snapshot has no root directory"}
	case o.Catalog == nil:
		return &Error{Code: CodeOptions, Message: "no catalogue was given"}
	case len(o.Modules) == 0:
		return &Error{Code: CodeOptions, Message: "no module was given to instrument"}
	case strings.TrimSpace(o.Toolchain.GoBin) == "":
		// Refused here rather than left to surface from the first build as a
		// spec error about an empty program name, which describes the symptom
		// and not the mistake.
		return &Error{Code: CodeOptions, Message: "no Go toolchain was located"}
	}
	for _, module := range o.Modules {
		if strings.TrimSpace(module.Dir) == "" || strings.TrimSpace(module.Path) == "" {
			return &Error{
				Code:    CodeOptions,
				Message: "a module was given with no directory or no import path",
			}
		}
	}
	// Every catalogued mutant has to belong to one of the modules given, or the
	// phase would instrument a tree that does not hold it and then accept it on
	// the strength of a build that never saw it. The check is here rather than
	// in the loop because a tree half-instrumented before the mistake is found
	// is a tree somebody has to clean up.
	named := make(map[string]bool, len(o.Modules))
	for _, module := range o.Modules {
		named[module.mutants(o.Catalog)] = true
	}
	for _, m := range o.Catalog.Mutants() {
		if !named[m.ModulePath] {
			return &Error{
				Code: CodeOptions,
				Message: "the catalogue holds mutants of " + strconv.Quote(m.ModulePath) +
					", which is not one of the modules given",
			}
		}
	}
	return nil
}

// A validator is one validation pass.
//
// The two function fields are the seam the search is tested through. Everything
// above them — restoring bytes, writing guards, running a toolchain, reading an
// exit status — is what a fake replaces, and what is left is an algorithm over
// "does this subset compile", which is the part worth testing exhaustively and
// the part a real toolchain makes far too slow to test that way.
type validator struct {
	root    string
	catalog *mutation.Catalog
	hints   instrument.Hints
	// mode is the tree being validated, carried into both instrumentation
	// calls: the whole-tree one that starts the phase and the per-file one every
	// step of the search rewrites through. A mode that reached only the first
	// would produce a tree that turned back into the other the moment anything
	// was bisected.
	mode      instrument.Mode
	packages  []string
	toolchain gocmd.Toolchain
	jobs      int
	timeout   time.Duration
	env       []string
	// recorder is the run's own, from [Options.Trace]. It is named for what it
	// is rather than for the package it comes from, because `trace` is the
	// package this file already imports.
	recorder *trace.Recorder

	// runtimes are the generated activation packages, one per module, as the
	// full instrumentation pass settled them. Every later rewrite is handed the
	// same one its file started with: a package is written once and never
	// regenerated, because its dense indices are what every guard in the tree
	// spells.
	runtimes []instrument.Result

	// modules is [Options.Modules], in the order the phase instruments them.
	modules []Module

	// paths are the catalogued files in sorted order, and byPath their mutants
	// in catalogue order. A path here is relative to the *snapshot* root and
	// not to a module -- which for a single-module tree is the same string, and
	// for a workspace is what keeps two modules' `app.go` apart. It is also
	// what the compiler names in a diagnostic, because the build runs at the
	// snapshot root, and what the drift gate compares against.
	paths  []string
	byPath map[string][]mutation.Mutant
	// files is where each of those paths actually is: which module it belongs
	// to, what it is called within that module, and which runtime its guards
	// import.
	files map[string]fileRef
	// pristine holds the bytes of every catalogued file as they were before
	// instrumentation. They are read once, up front, and every rewrite in the
	// phase is composed against them.
	pristine map[string][]byte
	// guards counts the guards each file currently carries.
	guards map[string]int
	// builds counts the builds spent.
	builds int

	apply func(path string, subset []mutation.Mutant) error
	build func(ctx context.Context) (verdict, error)
}

// run is the phase proper.
func (v *validator) run(ctx context.Context) (Result, error) {
	if err := v.readPristine(); err != nil {
		return Result{}, err
	}
	if err := v.instrumentModules(); err != nil {
		return Result{}, err
	}
	// What the phase started from: the whole catalogue, written into the tree
	// at once. Every later step of the recording is about narrowing this number
	// down, and without it a reader cannot tell a validation that rejected two
	// of four hundred from one that rejected two of three.
	v.recorder.Validate(trace.ValidateRecord{
		Tree:       v.tree(),
		Op:         trace.ValidateOpInstrument,
		Candidates: v.catalog.Len(),
	})

	rejected, searchErr := v.search(ctx)
	result := v.result()
	result.Rejected, result.AcceptedIDs = v.report(rejected)
	if searchErr != nil {
		return result, searchErr
	}
	return result, nil
}

// search establishes which candidates compile, leaving the snapshot holding
// exactly those, and closes the recording of the phase with what it decided.
//
// The closing event is recorded here rather than by the caller because it is
// what a reader looks for to know the account is complete: a recording that
// holds builds and rejections but no `done` is a validation that stopped, and
// the error the phase returns is then the thing to read. So it is written on
// the way out of a search that finished, and on no other path.
func (v *validator) search(ctx context.Context) ([]condemned, error) {
	rejected, err := v.bisect(ctx)
	if err != nil {
		return rejected, err
	}
	v.recorder.Validate(trace.ValidateRecord{
		Tree:     v.tree(),
		Op:       trace.ValidateOpDone,
		Builds:   v.builds,
		Accepted: v.candidates() - len(rejected),
		Rejected: len(rejected),
	})
	return rejected, nil
}

// bisect is the search proper.
//
// The first build is the whole phase in the ordinary case. Everything after it
// exists for the case where a guard did not compile, and the shape of that work
// is fixed by one requirement: whether a subset of one file compiles must be a
// question about that file. So before anything is searched, every catalogued
// file is put back to its pristine bytes — which both proves the failure is
// something this phase can fix and makes the empty subset a known-good starting
// point — and the files that have not been decided yet stay pristine while
// their neighbours are searched. A file left instrumented would answer for
// itself in every build and the search would reject candidates until it ran out
// of them.
func (v *validator) bisect(ctx context.Context) ([]condemned, error) {
	pending := slices.Clone(v.paths)
	failing, err := v.compile(ctx, pending, "")
	if err != nil {
		return nil, err
	}
	if !failing.failed {
		return nil, nil
	}

	if restoreErr := v.restore(pending); restoreErr != nil {
		return nil, restoreErr
	}
	// No pending set: a gate that fails ends the phase, so the files the
	// compiler named are not a list of where to search next.
	gate, err := v.compile(ctx, nil, "")
	if err != nil {
		return nil, err
	}
	// The one failure this phase refuses to blame on a candidate, recorded
	// whichever way it came out: a gate that passed is what licenses everything
	// the search does afterwards, and a reader given only the rejections would
	// have to take that licence on trust.
	v.recorder.Validate(trace.ValidateRecord{
		Tree:    v.tree(),
		Op:      trace.ValidateOpGate,
		Build:   v.builds,
		Failed:  gate.failed,
		ExecSeq: gate.execSeq,
	})
	if gate.failed {
		return nil, &Error{
			Code: CodeNotMutantInduced,
			Message: "the snapshot does not build with every mutant removed, so the failure is not " +
				"something go-mutants introduced; nothing was rejected",
			Output: gate.output,
		}
	}

	var rejected []condemned
	// One pass per catalogued file, and one more. Every pass decides at least
	// one of them -- [validator.blame] never answers an empty list while
	// anything is pending -- so the search cannot run more passes than there
	// are files, and the bound says that in the loop's shape rather than in a
	// lemma about another function.
	//
	// It is written this way for a measured reason. As `for {}` the exit rested
	// on that lemma, and every edit to a condition inside it -- the build's
	// verdict, the emptiness of the pending set, the blame that feeds it --
	// produced a phase that never returns. This repository's own gate paid a
	// per-mutant timeout, twice, for thirteen of them; bounded, the same edits
	// come back as a wrong answer that a test can state.
	for range len(pending) + 1 {
		for _, path := range failing.blamed {
			accepted, condemnedHere, err := isolate(ctx, v.byPath[path], v.probe(path))
			if err != nil {
				return rejected, err
			}
			v.recordIsolation(path, len(accepted), condemnedHere)
			// The last probe left whichever subset it tried on disk, which is
			// not necessarily the accepted one.
			if err := v.apply(path, accepted); err != nil {
				return rejected, err
			}
			rejected = append(rejected, condemnedHere...)
			pending = slices.DeleteFunc(pending, func(p string) bool { return p == path })
		}

		// Everything still undecided goes back in whole, and the build says
		// whether the phase is finished or has just learned where to look next.
		if err := v.reinstate(pending); err != nil {
			return rejected, err
		}
		result, err := v.compile(ctx, pending, "")
		if err != nil {
			return rejected, err
		}
		if !result.failed {
			return rejected, nil
		}
		if len(pending) == 0 {
			return rejected, &Error{
				Code: CodeStillFailing,
				Message: "the snapshot does not build although every catalogued file was isolated and " +
					"each accepted subset compiled on its own, which means candidates in different files " +
					"interact; the accepted set cannot be trusted",
				Output: result.output,
			}
		}
		if err := v.restore(pending); err != nil {
			return rejected, err
		}
		failing = result
	}
	// Past the bound, which is past the point where every catalogued file has
	// been isolated. It is the same thing the exhausted search says above and
	// for the same reason -- the snapshot does not build and there is nothing
	// left to look at -- and it is stated rather than left as a fall-through,
	// because a search that came out here has been wrong about its own bound
	// and the accepted set is exactly as untrustworthy either way.
	return rejected, &Error{
		Code: CodeStillFailing,
		Message: "the snapshot does not build after one isolation pass per catalogued file, " +
			"which means candidates in different files interact; the accepted set cannot be trusted",
	}
}

// restore puts files back to their pristine bytes, and reinstate writes every
// candidate of theirs back in.
//
// The pair is the state machine the search runs on, and they are named rather
// than written out at each of their three call sites because the difference
// between them is the difference between "this file is not being asked about"
// and "this file is being asked about whole".
func (v *validator) restore(paths []string) error {
	for _, path := range paths {
		if err := v.apply(path, nil); err != nil {
			return err
		}
	}
	return nil
}

func (v *validator) reinstate(paths []string) error {
	for _, path := range paths {
		if err := v.apply(path, v.byPath[path]); err != nil {
			return err
		}
	}
	return nil
}

// blame returns the undecided files a failing build pointed at, in pending
// order.
//
// A failure that names no undecided file is not a reason to stop. The compiler
// can report a guard's damage at a line in another file, or say nothing about a
// file whose package never got compiled because a dependency failed first, and
// in both cases the candidates are still in the tree and still have to be
// found. So the fallback is to search everything still undecided: slower, and
// it terminates with the same answer, which is the trade this phase should
// always make.
func (v *validator) blame(failing verdict, pending []string) []string {
	named := make(map[string]bool)
	for _, path := range blamedPaths(parseDiagnostics(failing.output, v.root)) {
		named[path] = true
	}
	blamed := make([]string, 0, len(pending))
	for _, path := range pending {
		if named[path] {
			blamed = append(blamed, path)
		}
	}
	if len(blamed) == 0 {
		return slices.Clone(pending)
	}
	return blamed
}

// probe binds the search's one operation to a file: write exactly this subset,
// build, and report whether the snapshot still fails.
func (v *validator) probe(path string) probe {
	return func(ctx context.Context, subset []mutation.Mutant) (verdict, error) {
		if err := v.apply(path, subset); err != nil {
			return verdict{}, err
		}
		// No pending set: a trial build asks whether one proposed subset of one
		// file compiles, and the files the compiler names in answer are not a
		// list of where to look next — the search already knows where it is,
		// and says so with the path.
		return v.compile(ctx, nil, path)
	}
}

// compile spends one build, counts it, and records what it said.
//
// Every build the phase makes goes through here rather than through the seam
// directly, and that is what makes the count and the recording one thing: a
// build that was spent and not recorded is invisible in exactly the run
// somebody is trying to explain, and the two numbers drifting apart would make
// the recording say the phase was cheaper than it was.
//
// A build that could not be *run* is not recorded as a build. The compiler
// answered nothing, so `failed` would be a claim about a compile that never
// happened; the execution itself is recorded at the choke point, and the error
// this returns carries the command.
//
// pending is the files still undecided, for the two facts a reader of a failing
// build needs next: how much is left to search, and which of it the compiler
// pointed at. Both are recorded for a build that failed and neither for one
// that did not, because a green build leaves nothing undecided and nobody
// blamed. Two kinds of build pass no pending set at all, because neither is
// being asked where to look next: a trial inside an isolation, which asks
// whether one proposed subset of one file compiles, and the pristine gate,
// whose failure means nothing this phase could reject would help and whose
// blamed files would read as candidates about to be condemned.
//
// path is the file an isolation is searching, and empty for the builds that are
// about the tree as a whole. It is what tells a reader which of them a
// bisection spent: half the builds of a validation that had to search can
// belong to one file.
//
// The blame is computed once, here, and travels back on the verdict: the
// recording of the build and the search's next step want the same answer, and
// deriving it twice would parse the compiler's whole output twice per failing
// build.
func (v *validator) compile(ctx context.Context, pending []string, path string) (verdict, error) {
	v.builds++
	result, err := v.build(ctx)
	if err != nil {
		return result, err
	}
	if result.failed && len(pending) > 0 {
		result.blamed = v.blame(result, pending)
	}
	record := trace.ValidateRecord{
		Tree:    v.tree(),
		Op:      trace.ValidateOpBuild,
		Build:   v.builds,
		Failed:  result.failed,
		Path:    path,
		Blamed:  result.blamed,
		ExecSeq: result.execSeq,
	}
	if len(result.blamed) > 0 {
		record.Pending = len(pending)
	}
	v.recorder.Validate(record)
	return result, nil
}

// recordIsolation records one file's search and each candidate it condemned.
//
// The rejections are recorded here, where the search learns of them, rather
// than inside the bisection: that algorithm is deliberately nothing but "does
// this subset compile", tested against a table with no snapshot and no
// toolchain in sight, and a recorder reaching into it would be the first thing
// it knew about the world. What matters about the moment of rejection is kept
// either way — the diagnostic is the output of the build that condemned the
// candidate, captured then, because by the time the phase ends the tree
// compiles and that message exists nowhere else.
func (v *validator) recordIsolation(path string, accepted int, rejected []condemned) {
	v.recorder.Validate(trace.ValidateRecord{
		Tree:       v.tree(),
		Op:         trace.ValidateOpIsolate,
		Path:       path,
		Candidates: len(v.byPath[path]),
		Accepted:   accepted,
	})
	for _, c := range rejected {
		v.recorder.Validate(trace.ValidateRecord{
			Tree: v.tree(),
			Op:   trace.ValidateOpReject,
			Path: c.mutant.Path,
			// The full identity: a recording is joined to a report on it, and
			// the shortened form is a rendering rather than a key.
			MutantID:   c.mutant.ID,
			Diagnostic: v.condemnation(c),
		})
	}
}

// condemnation is the compiler's reason for one rejection, in one line.
//
// It is chosen by exactly the rule [validator.rejection] uses, so the reason in
// the recording and the reason in the report are the same sentence about the
// same mutant rather than two texts a reader has to reconcile. What differs is
// the length: a rejection in a report is the whole message, continuations and
// all, while an event carries its first line — the whole of the condemning
// build's output is preserved once, under the `exec` event of that build, and
// copying a page of it into every rejected candidate's event would be the same
// bytes several times over.
func (v *validator) condemnation(c condemned) string {
	return firstLine(v.rejection(c.mutant, c.output).Diagnostic)
}

// tree is which of the two trees this phase is validating, as the recording
// spells it.
func (v *validator) tree() string {
	if v.mode == instrument.ModeProbe {
		return trace.ValidateTreeProbe
	}
	return trace.ValidateTreeMutant
}

// candidates is how many mutants the phase started with. It is counted from the
// files rather than taken from the catalogue so that it means the same thing in
// a search driven straight through the seam, where there is no catalogue at all.
func (v *validator) candidates() int {
	total := 0
	for _, path := range v.paths {
		total += len(v.byPath[path])
	}
	return total
}

// readPristine reads every catalogued file before instrumentation touches it.
//
// Reading up front rather than on demand is what makes the phase possible at
// all: after the first pass the files on disk hold guards, and the spans in the
// catalogue describe the bytes underneath them. It also fixes the order of the
// files, which is the order everything downstream reports in.
func (v *validator) readPristine() error {
	dirs := make(map[string]string, len(v.modules))
	for _, module := range v.modules {
		dirs[module.mutants(v.catalog)] = module.Dir
	}
	for _, m := range v.catalog.Mutants() {
		key := snapshotPath(dirs[m.ModulePath], m.Path)
		if _, seen := v.byPath[key]; !seen {
			v.paths = append(v.paths, key)
			v.files[key] = fileRef{module: m.ModulePath, path: m.Path}
		}
		v.byPath[key] = append(v.byPath[key], m)
	}
	slices.Sort(v.paths)

	for _, path := range v.paths {
		src, err := os.ReadFile(filepath.Join(v.root, filepath.FromSlash(path)))
		if err != nil {
			return &Error{
				Code:    CodeSourceUnreadable,
				Message: "cannot read " + strconv.Quote(path) + " in the snapshot",
				Err:     err,
			}
		}
		v.pristine[path] = src
	}
	return nil
}

// A fileRef is where one catalogued file is: which module it belongs to, what
// it is called within that module, and which runtime its guards import.
//
// The last is filled in by [validator.instrumentModules] rather than by
// [validator.readPristine], because it is not a fact about the file until the
// runtime has been written.
type fileRef struct {
	module        string
	path          string
	root          string
	runtimeImport string
	// loopBase is the first loop-site index this file's counters answer to,
	// as [instrument.Instrument] numbered them across the tree. It is carried
	// here because the search rewrites one file at a time and the ceilings are
	// one array in one generated package: a file re-instrumented from base zero
	// would hold its loops to another file's ceilings. See ADR 0013.
	loopBase uint32
}

// snapshotPath joins a module's directory to a module-relative path, which is
// where that file sits in the snapshot.
//
// No guard on the empty directory or on ".", because path.Join already answers
// both with the path itself -- and a guard that only ever agreed with the line
// below it would be a branch no test could tell from its absence.
func snapshotPath(dir, rel string) string {
	return path.Join(dir, rel)
}

// instrumentModules writes every catalogued mutant into the tree, a module at a
// time, and records where each file's guards import their runtime from.
//
// A module at a time, because a module's files can only import a runtime its
// own module declares. The whole catalogue goes into every one of those
// runtimes: a mutant of one module can be activated while another module's
// tests are running, and a runtime knowing only its own module's indices would
// meet an id it had never heard of and exit as if the snapshot were stale.
func (v *validator) instrumentModules() error {
	for _, module := range v.modules {
		root := filepath.Join(v.root, filepath.FromSlash(module.Dir))
		instrumented, err := instrument.Instrument(instrument.Options{
			SnapshotRoot: root,
			ModulePath:   module.Path,
			Module:       module.mutants(v.catalog),
			Catalog:      v.catalog,
			Hints:        v.hints,
			Mode:         v.mode,
		})
		if err != nil {
			return err
		}
		// Everything this phase reports is snapshot-relative, so the module's
		// own answers are lifted here and nowhere else.
		instrumented.RuntimeDir = snapshotPath(module.Dir, instrumented.RuntimeDir)
		for i, rel := range instrumented.FilesInstrumented {
			instrumented.FilesInstrumented[i] = snapshotPath(module.Dir, rel)
		}
		lifted := make(map[string]int, len(instrumented.GuardsByFile))
		for rel, count := range instrumented.GuardsByFile {
			key := snapshotPath(module.Dir, rel)
			lifted[key] = count
			v.guards[key] = count
		}
		instrumented.GuardsByFile = lifted
		bases := make(map[string]uint32, len(instrumented.LoopBase))
		for rel, base := range instrumented.LoopBase {
			bases[snapshotPath(module.Dir, rel)] = base
		}
		instrumented.LoopBase = bases
		for _, key := range instrumented.FilesInstrumented {
			ref := v.files[key]
			ref.root = root
			ref.runtimeImport = instrumented.RuntimeImport
			ref.loopBase = bases[key]
			v.files[key] = ref
		}
		v.runtimes = append(v.runtimes, instrumented)
	}
	// A file with no guards at all is still a file the search may have to
	// rewrite -- restoring it, and writing its candidates back in -- so it
	// needs a root and a runtime too.
	for key, ref := range v.files {
		if ref.root != "" {
			continue
		}
		module, ok := v.moduleOf(ref.module)
		if !ok {
			return &Error{
				Code:    CodeOptions,
				Message: "no module was given for " + strconv.Quote(ref.module),
			}
		}
		ref.root = filepath.Join(v.root, filepath.FromSlash(module.Dir))
		ref.runtimeImport = v.runtimeImportOf(module)
		ref.loopBase = v.loopBaseOf(module, key)
		v.files[key] = ref
	}
	return nil
}

// moduleOf finds the module a catalogued mutant's module path names.
func (v *validator) moduleOf(modulePath string) (Module, bool) {
	for _, module := range v.modules {
		if module.mutants(v.catalog) == modulePath {
			return module, true
		}
	}
	return Module{}, false
}

// loopBaseOf is the loop-site base the given module's pass gave one file, and
// zero for a file that pass never reached -- which is a file with no counters
// to number.
func (v *validator) loopBaseOf(module Module, key string) uint32 {
	for i, candidate := range v.modules {
		if candidate == module && i < len(v.runtimes) {
			return v.runtimes[i].LoopBase[key]
		}
	}
	return 0
}

// runtimeImportOf is the import path the given module's runtime was written at.
func (v *validator) runtimeImportOf(module Module) string {
	for i, candidate := range v.modules {
		if candidate == module && i < len(v.runtimes) {
			return v.runtimes[i].RuntimeImport
		}
	}
	return ""
}

// instrumentFile is the real [validator.apply]: rewrite one file so that it
// carries exactly this subset of its candidates.
func (v *validator) instrumentFile(path string, subset []mutation.Mutant) error {
	ref := v.files[path]
	guards, err := instrument.InstrumentFile(instrument.FileOptions{
		SnapshotRoot:  ref.root,
		RuntimeImport: ref.runtimeImport,
		Path:          ref.path,
		Source:        v.pristine[path],
		Mutants:       subset,
		Hints:         v.hints,
		Mode:          v.mode,
		LoopBase:      ref.loopBase,
	})
	if err != nil {
		return err
	}
	if guards == 0 {
		delete(v.guards, path)
		return nil
	}
	v.guards[path] = guards
	return nil
}

// buildSnapshot is the real [validator.build]: one `go build ./...` in the
// snapshot, with whatever it links sent to the null device.
//
// The order of the cases is the contract, and it is internal/engine's. A
// cancelled run comes back from the runner as an unavailable exit code with no
// error and no timeout, which is indistinguishable from a build failure unless
// the context is asked — so it is asked before the exit status is judged, and
// after the two conditions that are definitely not cancellations. A non-zero
// exit is the only one of the four that is not an error: it is the compiler
// answering the question this phase asked it.
func (v *validator) buildSnapshot(ctx context.Context) (verdict, error) {
	spec := v.toolchain.Command(buildArgs(v.jobs, v.packages)...)
	spec.Dir = v.root
	spec.Env = v.env
	spec.Timeout = v.timeout
	spec.Trace = v.recorder
	spec.Kind = trace.ExecKindValidateBuild

	result := runner.Run(ctx, spec)
	// The sequence rides back on every one of the four answers, the three that
	// are errors included: a build that could not be run is still an execution
	// the recording holds, and a caller that keeps the verdict beside the error
	// can point at it without re-deriving anything.
	spent := verdict{execSeq: result.TraceSeq}
	switch {
	case result.Err != nil:
		return spent, &Error{
			Code:       CodeBuildFailed,
			Message:    "the snapshot could not be built: the command could not be run",
			Output:     string(result.Output),
			Err:        result.Err,
			Invocation: runner.CommandOf(spec, result),
		}
	case result.TimedOut:
		return spent, &Error{
			Code:       CodeBuildTimedOut,
			Message:    "the snapshot did not build within " + v.timeout.String(),
			Output:     string(result.Output),
			Invocation: runner.CommandOf(spec, result),
			TimedOut:   true,
		}
	case ctx.Err() != nil:
		return spent, &Error{
			Code:       CodeInterrupted,
			Message:    "validation was interrupted",
			Err:        ctx.Err(),
			Invocation: runner.CommandOf(spec, result),
		}
	}
	spent.failed = result.ExitCode != 0
	spent.output = string(result.Output)
	return spent, nil
}

// buildArgs is the argument vector of one validation build.
//
// `-o os.DevNull` is the load-bearing part of it, and it is not tidiness. The
// go command writes a linked executable into its working directory whenever the
// pattern it is given resolves to exactly one package and that package is
// `main` — cmd/go decides it with `len(pkgs) == 1 && pkgs[0].Name == "main" &&
// cfg.BuildO == ""` — and the working directory here is the snapshot root.
// Nothing downstream would forgive that file: [snapshot.Snapshot.Redigest]
// applies no exclusions, because every byte under the root is go-mutants' own,
// and internal/engine's drift gate forgives exactly two things, a changed file
// that carries guards and an addition under the generated runtime directory. So
// a single-directory `package main` module — the shape of most Go command line
// tools — would end its run being told that its tests write into the package
// directory they run in, about a file go-mutants had written itself.
//
// The null device rather than a scratch directory outside the snapshot, which
// is how internal/execute keeps its test binaries out of the tree: `-o
// <directory>` makes cmd/go build the main packages *and only those*, skipping
// every package whose name is not `main`, so a guard in a library package would
// never be compiled and a library-only module — the shape of most Go modules —
// would fail outright with "go: no main packages to build". The null device is
// special-cased instead by clearing the output path, which lands on the same
// "compile every package, discard the objects" path a bare `go build ./...`
// takes: same packages, same diagnostics, same exit status, nothing written
// anywhere. Measured on go1.26.5 over four module shapes — single main, main
// plus library, library only, two mains — building and failing to build, the
// output was byte-identical to the bare form every time, which is what keeps
// [parseDiagnostics] and the search reading exactly what they read before. It
// asks nothing new of a toolchain either: cmd/go has had the case since at
// least go1.16, and internal/execute already needs the `go list -json=<fields>`
// form that arrived in go1.19.
//
// It is a function of its own so that the flag can be tested without a
// toolchain. Every other statement this package makes about a build is made by
// an integration test, and a missing `-o` is invisible to all of them: the
// generated runtime package is a second package under `./...`, so by the time
// this phase builds anything the go command has no single main package to name
// an executable after and writes nothing whatever this vector says. The flag is
// what makes that a property of the phase instead of a property of the tree it
// happens to be pointed at.
func buildArgs(jobs int, packages []string) []string {
	args := make([]string, 0, 5+len(packages))
	args = append(args, "build", "-o", os.DevNull)
	if jobs > 0 {
		args = append(args, "-p", strconv.Itoa(jobs))
	}
	if len(packages) == 0 {
		packages = []string{"./..."}
	}
	return append(args, packages...)
}

// result assembles what the snapshot now holds, starting from what the full
// instrumentation pass reported and correcting it to the state isolation left
// behind.
func (v *validator) result() Result {
	files := make([]string, 0, len(v.guards))
	guards := make(map[string]int, len(v.guards))
	for _, path := range v.paths {
		if count, ok := v.guards[path]; ok {
			files = append(files, path)
			guards[path] = count
		}
	}
	// The runtime a tree with one module has is the tree's; a workspace has one
	// per module and no single answer, so the merged view names none and
	// [Result.Runtimes] is where a caller reads them.
	merged := instrument.Result{FilesInstrumented: files, GuardsByFile: guards}
	if len(v.runtimes) == 1 {
		merged.RuntimeDir = v.runtimes[0].RuntimeDir
		merged.RuntimeImport = v.runtimes[0].RuntimeImport
	}
	return Result{Instrumented: merged, Runtimes: slices.Clone(v.runtimes), Builds: v.builds}
}

// report turns the condemned candidates into the two lists a caller reads,
// both in catalogue order.
func (v *validator) report(rejected []condemned) ([]Rejection, []string) {
	condemnedBy := make(map[string]condemned, len(rejected))
	for _, c := range rejected {
		condemnedBy[c.mutant.ID] = c
	}

	rejections := make([]Rejection, 0, len(rejected))
	accepted := make([]string, 0, v.catalog.Len())
	for _, m := range v.catalog.Mutants() {
		c, ok := condemnedBy[m.ID]
		if !ok {
			accepted = append(accepted, m.ID)
			continue
		}
		rejections = append(rejections, v.rejection(m, c.output))
	}
	return rejections, accepted
}

// rejection describes one rejected mutant, in the coordinates discovery uses
// and the compiler's own words.
func (v *validator) rejection(m mutation.Mutant, output string) Rejection {
	src := v.pristine[m.Path]
	startLine, column := position(src, m.Span.StartByte)
	endLine, _ := position(src, m.Span.EndByte)

	// Never empty. A rejection with no explanation is the silence this whole
	// phase exists to avoid, and RunReport v1 requires a non-empty diagnostic
	// on every rejected entry, so the last resort says that the compiler said
	// nothing rather than saying nothing itself.
	diagnostic := chooseDiagnostic(parseDiagnostics(output, v.root), m.Path, startLine, endLine)
	if diagnostic == "" {
		diagnostic = firstLine(output)
	}
	if diagnostic == "" {
		diagnostic = "the build failed without printing a diagnostic"
	}
	return Rejection{
		ID:         m.ID,
		DisplayID:  m.DisplayID,
		Path:       m.Path,
		Line:       startLine,
		Column:     column,
		Rule:       m.Rule.Name,
		Diagnostic: diagnostic,
	}
}

// position returns the 1-based line and 1-based byte column of an offset.
//
// Bytes rather than runes, and 1-based on both axes, because that is what
// go/token reports and therefore what internal/discover puts on a candidate: a
// rejected mutant and a live one have to be named the same way, and nothing
// downstream would catch it if they were not.
func position(src []byte, offset uint32) (int, int) {
	// Clamped rather than guarded: an offset of exactly the length is the whole
	// file either way, so a comparison here would have a second reading no span
	// could tell from the first.
	end := min(int(offset), len(src))
	before := src[:end]
	line := 1 + bytes.Count(before, []byte("\n"))
	column := end - (bytes.LastIndexByte(before, '\n') + 1) + 1
	return line, column
}

// firstLine returns the first non-empty line of s, trimmed. It is the last
// resort for a rejection's diagnostic, for output that held no line this
// package could locate.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
