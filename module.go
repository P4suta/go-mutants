// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"

	"github.com/P4suta/go-mutants/trace"
)

const (
	// moduleCall is what [Workspace.Module] calls itself: in a lifecycle
	// refusal, in the `exec` event's subject, and in [ExecutionError.Call]. One
	// constant, because a consumer that matched the trace subject and branched
	// on the error's Call would otherwise be matching two spellings of one
	// thing.
	moduleCall = "module"
	// moduleAllPackages is the pattern an empty [ModuleQuery.Packages] means,
	// and it is `go list`'s own spelling of "this module" rather than a
	// convention of go-mutants'.
	moduleAllPackages = "./..."
	// moduleListFields bounds the document `go list` prints to the fields
	// [Package] carries.
	//
	// It is a bound rather than a preference. `go list -json` with no field
	// list prints everything the go command knows about a package — the build
	// flags, the cgo directives, the embed patterns, the stale reason — which
	// for a large module is tens of megabytes of text this call would read,
	// decode and throw away. Naming the fields is what keeps the document
	// proportional to the answer.
	moduleListFields = "ImportPath,Dir,Name,GoFiles,TestGoFiles,XTestGoFiles,Imports,Deps,EmbedFiles"
	// moduleListLimit is how much of that document is kept.
	//
	// It is sized for a document rather than for a test failure, which is the
	// rule docs/library.md states for any `go list -json` passthrough: what
	// survives a truncation is the notice line and the tail, and the tail of a
	// JSON stream is not a shorter document but an unparsable one. Nothing is
	// allocated up front — internal/runner grows its capture — so the cost of a
	// generous limit is paid only by a module that really is this big.
	moduleListLimit = 32 << 20
)

// ErrInvalidQuery reports a [ModuleQuery] the engine will not hand to
// `go list`: a package pattern that is empty, absolute, escaping the module,
// not module-relative, or beginning with a dash the go command would read as a
// flag — or a build tag that is not one tag.
//
// It is a refusal rather than a listing of nothing, and it is the judgement
// [ErrInvalidSelection] already makes: a pattern nothing can satisfy names no
// package, and a consumer told that the module holds nothing would believe it.
// The refusal happens before any child starts, so a mistake in the request does
// not cost a `go list` to find.
//
// Unlike the sentinels in errors.go it carries its own sentence rather than
// being wrapped into one, because there is no single sentence to wrap it into:
// every reason names the pattern or the tag that was wrong, and that clause is
// what a caller needs.
var ErrInvalidQuery = errors.New("gomutants: module: invalid query")

// ModuleQuery selects what [Workspace.Module] answers about.
//
// Its zero value is the whole module under no build tags, which is the question
// almost every consumer asks.
type ModuleQuery struct {
	// Packages are module-relative package patterns as `go list` reads them:
	// `./...`, `./internal/...`, `./cmd/x`. Empty means `./...`.
	//
	// An absolute path, a pattern that escapes the module, and a pattern
	// beginning with `-` are refused with [ErrInvalidQuery] before anything
	// runs. The order is kept as it was given; it does not change the answer,
	// which is sorted by import path, but it is what the refusal quotes and
	// what the recording shows.
	Packages []string
	// Tags are build constraints, as `-tags` takes them: one tag per element,
	// with no comma and no whitespace inside one.
	//
	// They are the consumer's own. go-mutants does not invent build tags, and a
	// package set listed under one set of tags is a different package set from
	// the one listed under another — which is why they are part of what the
	// answer is memoised under.
	Tags []string
}

// Package is one package of the frozen module, as `go list` reported it.
//
// Every list of files is relative to [Package.Dir] and spelled exactly as
// `go list` prints it: the Go source lists hold bare file names, because a Go
// source is always a direct child of its package directory, and
// [Package.EmbedFiles] holds slash-separated relative paths, because a
// `//go:embed` may name a file anywhere below it.
type Package struct {
	// ImportPath is the package's import path.
	ImportPath string
	// Dir is the absolute directory holding the package's files, inside the
	// frozen snapshot and never inside the tree the caller handed [Open]. A
	// consumer that opens a file may open this one.
	Dir string
	// Name is the package clause's name, which is not the last element of the
	// import path for a `main` or a `v2`.
	Name string
	// HasTests reports that the package has a test file: [Package.TestGoFiles]
	// or [Package.XTestGoFiles] is non-empty.
	//
	// It is carried rather than left to be derived because it is the question
	// consumers ask — a package with no test binary is measured differently,
	// or not at all — and two consumers deriving it from two different field
	// lists is two answers to one question.
	HasTests bool
	// GoFiles are the package's Go sources under the tags the query asked for.
	GoFiles []string
	// TestGoFiles are the in-package test files, and XTestGoFiles the ones in
	// the `_test` package beside it.
	TestGoFiles  []string
	XTestGoFiles []string
	// Imports are the packages this one imports directly, and Deps everything
	// it depends on transitively, both as `go list` computes them.
	Imports []string
	Deps    []string
	// EmbedFiles are the files `//go:embed` directives in this package name, as
	// slash-separated paths relative to [Package.Dir]. A directive may name a
	// file at any depth, so these are paths — `assets/deep/x.txt` — and not the
	// bare names the Go source lists hold.
	EmbedFiles []string
}

// Module is what one frozen module turned out to hold.
type Module struct {
	// Path is the module path go.mod declares.
	Path string
	// GoVersion is the `go` directive of that same go.mod — "1.26", without the
	// keyword — and is empty for a module that declares none.
	//
	// It is read in this process with golang.org/x/mod/modfile rather than
	// asked of a child, because the file is right there in the frozen tree and
	// a subprocess to read one line of it would be a subprocess.
	GoVersion string
	// Toolchain is what [Workspace.ToolchainVersion] reports: the `go version`
	// line of the toolchain [Open] located, which is the one that produced this
	// listing and the one every later command will use.
	Toolchain string
	// Packages are the packages the query matched, sorted by import path.
	Packages []Package
	// TraceSeq is the `seq` of the `exec` event the `go list` was recorded at,
	// and is how a consumer joins this answer to the workspace's recording. It
	// is the field [CommandResult.TraceSeq] is, and it means the same thing.
	//
	// A memoised answer carries the sequence of the listing that produced it
	// rather than a new one, because there was no new command: the recording
	// holds one `go-list` event per distinct query, and this names it.
	TraceSeq int64
}

// A moduleAnswer is one listing, complete or still being made.
//
// It is a value shared between the caller that is running the `go list` and any
// caller that asked the same question while it ran, which is what keeps "one
// listing per distinct query" true of a workspace several goroutines are using
// rather than only of one they take turns with. done is closed exactly once,
// whichever way the listing ended.
//
// The listing belongs to nobody, and that is the point of ctx. It is derived
// with [context.WithoutCancel] from whichever caller happened to arrive first,
// so a `go list` shared between two callers does not die because one of them
// went away — a waiter handed the first caller's `context canceled` would be
// receiving a failure that had nothing to do with it. What cancels it instead
// is the count: interested is every caller still waiting for this answer, and
// the listing is stopped and forgotten the moment it reaches zero before the
// answer was settled, so a listing nobody wants does not go on holding a child.
type moduleAnswer struct {
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	// The three fields below are guarded by [Workspace.moduleMu], except that
	// module and err are also safe to read once done is closed: settled is set
	// under the lock in the same critical section that closes it.
	interested int
	settled    bool
	module     Module
	err        error
}

// A moduleQuery is a [ModuleQuery] the engine has accepted: patterns that
// resolve, tags in one order, and no empty spellings of either.
type moduleQuery struct {
	packages []string
	tags     []string
}

// Module reports what the frozen module holds: its path, the Go version it
// declares, the toolchain [Open] located, and the packages a query matched.
//
// It is the typed form of the `go list -json` a consumer would otherwise run
// through [Workspace.Exec] — with its own conventions for patterns, for tags,
// for how much output to keep and for where the module's Go version comes from.
// The workspace has already frozen the tree and probed the toolchain, so the
// answer is one thing rather than four, and the `go list` is in the recording
// as an `exec` event of kind `go-list` with the subject `module`, like every
// other child this package starts.
//
// # When it may be called
//
// Exactly when [Workspace.Exec] may be, and for the same reasons. It runs
// before a preparation, beside one — holding the tree's shared half, so it
// overlaps everything a preparation does except the instrumentation window, and
// waits for that window if it is open — and after one that succeeded, whose
// tree is byte for byte the one [Open] froze. It is refused after a preparation
// that began and failed with [ErrPrepareFailed], and after [Workspace.Close]
// with [ErrWorkspaceClosed], which wins when both are true.
//
// Like [Workspace.Exec] it may not be called from a [PrepareOptions.Trace]
// callback: that callback runs on the preparation's own goroutine, and the
// deadlock is the one [Workspace.Exec] describes in full.
//
// One `go list` is bounded by the same ten-minute safety default a
// [Command] with no [Command.Timeout] gets, and by the caller's context, which
// is the shorter of the two that a consumer can set.
//
// # What it costs
//
// One `go list` per distinct query, until a command has run. The answer is
// memoised because the frozen tree does not change on its own: a consumer that
// wants the package list in three places pays for it once.
//
// Two listings are not remembered. One that *failed*, because a toolchain that
// could not answer once may answer the next time and a cached refusal would
// outlive the reason for it; and one every caller *abandoned* — every caller
// gone before it finished — because what a listing being torn down came back
// with was produced for nobody, and the next caller asked for a fresh answer
// rather than that one.
//
// The key is the normalised query: patterns in the order they were given, tags
// sorted and deduplicated. So `ModuleQuery{}` and
// `ModuleQuery{Packages: []string{"./..."}}` are one question, and so are the
// same two tags in either order — but a different tag set is a different
// question, because it is a different package set.
//
// Every listing is dropped when a [Workspace.Exec] call returns, because that
// is the one call that can change the tree: nothing refuses a command that
// writes into the snapshot — a `go generate`, a test that rewrites a golden
// file, a fuzz target the go command files a crasher for — and a package set
// that outlived one would describe a tree nobody has. It is dropped whatever
// the command did, because "did this one write" cannot be answered without
// re-freezing the tree. A `Prepare` needs no such rule: its integrity gate
// refuses a tree a command has changed, and the tree a successful preparation
// leaves is byte for byte the one [Open] froze.
//
// # Concurrency
//
// Two callers asking the same question share one `go list`. The listing runs
// under a context of its own rather than under whichever caller reached the
// memo first, so a caller that goes away does not take the answer with it: the
// one still waiting is answered. When the *last* interested caller leaves, the
// child is killed and nothing is remembered, so the next caller asks again
// rather than waiting on a listing no goroutine will finish, or being handed
// what one that was being torn down came back with. A caller that leaves on
// its own cancelled context is told about its own context and never about
// somebody else's.
//
// "Left before it finished" is the whole of that rule, and the other order is
// not a hole in it: a listing that had already finished when its last caller
// went away is a complete, correct listing of a frozen tree, and it stays in
// the memo for whoever asks next.
//
// The returned value is the caller's own. Editing it changes nothing about the
// next call's answer.
//
// # Failures
//
// Every way a listing can fail comes back as an [ExecutionError] with
// `Call: "module"` and a message beginning `gomutants: module: `, whether it
// was `go list` exiting non-zero, a child that would not start, a cancelled
// context or a go.mod that could not be read. The two exceptions are the ones a
// consumer branches on rather than reports: [ErrInvalidQuery] for a query this
// call will not resolve, and the lifecycle sentinels [ErrPrepareFailed] and
// [ErrWorkspaceClosed]. (A nil receiver is a programming error and comes back
// as a plain one, as it does from [Workspace.Exec].)
func (w *Workspace) Module(ctx context.Context, q ModuleQuery) (Module, error) {
	if w == nil {
		return Module{}, errors.New("gomutants: module: nil workspace")
	}
	// Shared for the whole call, which is what makes Close — the one caller
	// that takes it exclusively — wait for this listing rather than remove the
	// tree it is reading.
	w.mu.RLock()
	defer w.mu.RUnlock()
	if err := w.allowed(moduleCall); err != nil {
		return Module{}, err
	}
	query, err := normaliseModuleQuery(q)
	if err != nil {
		return Module{}, err
	}

	key := query.key()
	answer, leading := w.joinModule(key, ctx)
	release := w.moduleReleaser(key, answer)
	if !leading {
		defer release()
		select {
		case <-answer.done:
			if answer.err != nil {
				return Module{}, answer.err
			}
			return cloneModule(answer.module), nil
		case <-ctx.Done():
			// This caller's own context and never the listing's. Leaving is
			// what release() is for: it stops this caller counting, and the
			// listing goes on for as long as somebody else is still waiting.
			//
			// The bare error, because [moduleFailure] is what names the call:
			// wrapping it here as well would put `gomutants: module: ` in front
			// of a sentence that already begins with it.
			return Module{}, moduleFailure(ctx.Err())
		}
	}
	// The leader runs the listing on this goroutine, because the tree and the
	// workspace are held by this call and a listing outliving them would be
	// reading a snapshot Close had already removed. So its *interest* has to be
	// released by something else: it is inside `go list` until the listing
	// returns, and a caller whose context is done has stopped wanting the
	// answer whether or not it has stopped running. This goroutine is what
	// notices, and it is why a leader that goes away with no waiter behind it
	// cancels its own child rather than waiting the whole listing out.
	watching := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			release()
		case <-watching:
		}
	}()

	var (
		module   Module
		listErr  error
		finished bool
	)
	// Whatever happens below, the waiters are released and a query that did not
	// produce an answer is forgotten. A memo entry nobody completes is a
	// workspace whose every later Module call for that query blocks forever,
	// and a panic unwinding out of a decode is the way to get one.
	defer func() {
		close(watching)
		if !finished && listErr == nil {
			listErr = errors.New("gomutants: module: the listing did not finish")
		}
		w.settleModule(key, answer, module, listErr)
		release()
	}()
	module, listErr = w.listModule(answer.ctx, query)
	finished = true
	if listErr != nil {
		return Module{}, listErr
	}
	return cloneModule(module), nil
}

// joinModule registers this caller's interest in one query's answer and says
// whether it is the caller that has to produce it.
//
// The listing's context is derived from whichever caller creates the entry,
// with [context.WithoutCancel]: it keeps that caller's values — a consumer may
// have put a deadline-free tracing span in there — and drops its cancellation,
// because the listing is shared and no one caller's cancellation is the whole
// story. [Workspace.leaveModule] is what cancels it, when nobody is left.
func (w *Workspace) joinModule(key string, ctx context.Context) (*moduleAnswer, bool) {
	w.moduleMu.Lock()
	defer w.moduleMu.Unlock()
	if answer, ok := w.modules[key]; ok {
		answer.interested++
		return answer, false
	}
	listing, cancel := context.WithCancel(context.WithoutCancel(ctx))
	answer := &moduleAnswer{done: make(chan struct{}), ctx: listing, cancel: cancel, interested: 1}
	if w.modules == nil {
		w.modules = make(map[string]*moduleAnswer)
	}
	w.modules[key] = answer
	return answer, true
}

// moduleReleaser is one caller's release, which may be called more than once:
// the leader releases from the goroutine watching its context and again on the
// way out, and only the first of those counts.
func (w *Workspace) moduleReleaser(key string, answer *moduleAnswer) func() {
	var once sync.Once
	return func() { once.Do(func() { w.leaveModule(key, answer) }) }
}

// leaveModule drops one caller's interest, and stops a listing nobody is
// waiting for any more.
//
// A settled answer is left alone: it is a value in a map that later callers may
// have, and the count going to zero merely means nobody is holding it at this
// instant. An *unsettled* one at zero is a listing whose every caller has gone
// away, so its child is killed and its entry is forgotten — otherwise the next
// caller would wait on an answer that is never coming.
func (w *Workspace) leaveModule(key string, answer *moduleAnswer) {
	w.moduleMu.Lock()
	defer w.moduleMu.Unlock()
	answer.interested--
	if answer.interested > 0 || answer.settled {
		return
	}
	answer.cancel()
	if w.modules[key] == answer {
		delete(w.modules, key)
	}
}

// settleModule publishes what the listing came back with and releases everybody
// waiting on it.
//
// Two outcomes are not remembered, and both are decided in the same critical
// section that settles them, so that a caller joining between the two cannot
// take a share in an answer that is already known not to be one.
//
//   - A listing that *failed*. A toolchain that could not answer once may
//     answer the next time, and a cached refusal would outlive its reason.
//   - A listing the memo has already **let go of**: one whose last interested
//     caller left before it finished, so that [Workspace.leaveModule] removed
//     the entry and cancelled the child, or one a [Workspace.Exec] cleared out
//     from under. Putting either back would hand the next caller an answer
//     produced for nobody, from a listing that was being torn down while it
//     ran — and, where a later caller has since started a listing of its own,
//     would overwrite an entry that is not this one's to touch.
//
// Which of "the last caller left" and "the listing finished" happened first is
// a race, and it is not one this can decide: a listing that finished before its
// last caller went away is a complete, correct listing of a frozen tree and it
// stays. What must never happen, in either order, is an entry going back into
// the map after the memo has dropped it.
func (w *Workspace) settleModule(key string, answer *moduleAnswer, module Module, err error) {
	w.moduleMu.Lock()
	defer w.moduleMu.Unlock()
	answer.module, answer.err, answer.settled = module, err, true
	switch {
	case w.modules[key] != answer:
		// Already let go of, or already replaced. Neither is ours to put back.
	case err != nil:
		delete(w.modules, key)
	}
	// The listing is over either way, so its context is released here rather
	// than left for [Workspace.leaveModule] — which, for a settled answer, does
	// nothing at all.
	answer.cancel()
	// Last, and unconditional: a caller that joined while the entry was still
	// in the map is waiting on this, and is released whichever way it ended.
	close(answer.done)
}

// forgetModules drops every listing the workspace is holding.
//
// [Workspace.Exec] calls it as it returns, because a command may have written
// into the frozen tree and a package set that outlived one would describe a
// tree nobody has; [Workspace.Close] calls it because the tree is going away.
//
// A listing still in flight is left to finish for whoever is waiting on it — it
// is simply no longer in the map, so nothing later reuses it. That is the right
// way round: the caller waiting gets the answer it asked for, and the next
// caller asks again.
func (w *Workspace) forgetModules() {
	w.moduleMu.Lock()
	defer w.moduleMu.Unlock()
	clear(w.modules)
}

// listModule is one `go list` against the frozen tree, decoded, with every
// failure typed as this call's.
func (w *Workspace) listModule(ctx context.Context, query moduleQuery) (Module, error) {
	var module Module
	err := w.underTreeRead(func() error {
		// Asked again, now that the tree is held, for the reason
		// [Workspace.Exec] asks again: the unlock that wakes a call queued
		// behind an instrumentation window may be the unlock of a preparation
		// that gave up with the instrumented sources still in the tree, and a
		// listing taken from those would name a generated runtime package the
		// module does not have.
		if allowedErr := w.allowed(moduleCall); allowedErr != nil {
			return allowedErr
		}
		declared, err := readModuleFile(w.snapshot.Root)
		if err != nil {
			return err
		}
		// The workspace's own environment rather than a per-call one, and no
		// per-call scratch directory: those exist so that two *consumers'*
		// commands cannot observe each other through TMPDIR, and this is
		// go-mutants' own `go list` — it writes nothing a later call could
		// read, and a directory of its own would be a directory
		// [OpenOptions.KeepTemp] then has to account for.
		//
		// GOWORK=off for the reason internal/discover and the instrumented
		// verification both pin it: the go command searches every parent
		// directory for a `go.work` and obeys $GOWORK, and a snapshot in a
		// temporary directory below somebody's workspace would otherwise be
		// listed against a file the snapshot does not contain.
		//
		// The standard output is kept on its own because it is the half that
		// has to parse. Everything else the go command says — a warning that a
		// pattern matched nothing, a module being downloaded, a toolchain being
		// switched — goes to stderr on a command that exits *zero*, and a
		// decoder handed the combined capture would fail on the first byte of
		// it. The combined capture is still what a failure quotes.
		listed, err := w.runCommand(ctx, Command{
			Argv:        query.argv(),
			Env:         []string{"GOWORK=off"},
			OutputLimit: moduleListLimit,
		}, w.env, commandLabel{kind: trace.ExecKindGoList, subject: moduleCall, splitStdout: true})
		if err != nil {
			return err
		}
		packages, err := decodeListing(query, listed)
		if err != nil {
			return err
		}
		module = Module{
			Path:      declared.Path,
			GoVersion: declared.GoVersion,
			Toolchain: w.toolchain.Version.Raw,
			Packages:  packages,
			TraceSeq:  listed.TraceSeq,
		}
		return nil
	})
	if err != nil {
		return Module{}, moduleFailure(err)
	}
	return module, nil
}

// moduleFailure types one failure of [Workspace.Module] as this call's.
//
// Every way a listing can fail arrives here, and all but two leave as an
// [ExecutionError] with `Call: "module"`. The two that do not are the ones a
// consumer *branches* on rather than reports: a query this call will not
// resolve, and the lifecycle sentinels, which mean the workspace itself is no
// longer answering and are the same sentinels [Workspace.Exec] carries.
//
// The rest — a child that would not start, a context cancelled before it did,
// a go.mod that could not be read — used to arrive as whatever the layer
// underneath said, which for anything going through [Workspace.runCommand] is a
// sentence beginning `gomutants: exec:`: a call the consumer never made. The
// wrap puts this call's name in front of it and keeps the cause, so
// errors.Is still reaches a cancellation and the original sentence is still
// there to read.
func moduleFailure(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalidQuery), errors.Is(err, ErrWorkspaceClosed), errors.Is(err, ErrPrepareFailed):
		return err
	}
	var typed *ExecutionError
	if errors.As(err, &typed) && typed.Call == moduleCall {
		return err
	}
	// The code is taken from the cause rather than left empty, on the rule
	// [executionError] follows: a failure that carried a go-mutants diagnostic
	// still carries it here — a child that could not be started is the engine's
	// own fault and has a code somebody can quote — while the go command's own
	// refusals have none and leave the field as it should be.
	return &ExecutionError{
		Call:  moduleCall,
		Code:  DiagnosticCode(err),
		cause: fmt.Errorf("gomutants: module: %w", err),
	}
}

// readModuleFile is the module's own declaration of itself: the path and the
// `go` directive, read from the frozen go.mod in this process.
//
// The parse is lax because this reads two lines out of a file the go command
// owns: a directive a later release adds is not a reason for a listing to fail,
// and the two fields wanted here are among the ones a lax parse still fills in.
func readModuleFile(root string) (Module, error) {
	path := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		return Module{}, fmt.Errorf("gomutants: module: read go.mod: %w", err)
	}
	parsed, err := modfile.ParseLax("go.mod", data, nil)
	if err != nil {
		return Module{}, fmt.Errorf("gomutants: module: parse go.mod: %w", err)
	}
	var module Module
	if parsed.Module != nil {
		module.Path = parsed.Module.Mod.Path
	}
	if parsed.Go != nil {
		module.GoVersion = parsed.Go.Version
	}
	return module, nil
}

// listedPackage is one object of the `go list -json` stream.
//
// It is a type of its own rather than [Package] decoded in place, so that the
// coupling to the go command's field names lives in exactly one place and
// [Package] stays free to name a field whatever a consumer is owed —
// [Package.HasTests] being the one that already does.
type listedPackage struct {
	ImportPath   string   `json:"ImportPath"`
	Dir          string   `json:"Dir"`
	Name         string   `json:"Name"`
	GoFiles      []string `json:"GoFiles"`
	TestGoFiles  []string `json:"TestGoFiles"`
	XTestGoFiles []string `json:"XTestGoFiles"`
	Imports      []string `json:"Imports"`
	Deps         []string `json:"Deps"`
	EmbedFiles   []string `json:"EmbedFiles"`
}

// decodeListing turns one `go list` capture into the packages it named, or says
// why it could not.
//
// The three refusals are checked in the order a reader needs them. A command
// that exited non-zero carries the toolchain's own words and is the answer to
// almost every mistake in a query; a capture that was truncated is refused
// outright rather than decoded, because the notice line and the tail are what
// survive and a syntax error at byte zero explains nothing; and only then is
// the document read.
//
// The document is the child's *standard output* and the failures quote its
// combined capture, and the split is not a nicety. `go: warning: "./x/..."
// matched no packages`, `go: downloading …` and a toolchain switch all go to
// stderr on a command that exits zero, so a decoder reading the combined stream
// would refuse a perfectly good listing on the first byte of a line the go
// command writes as a courtesy. [Result.Truncated] speaks for both captures at
// once: the combined total is at least the stdout total.
func decodeListing(query moduleQuery, listed commandRun) ([]Package, error) {
	switch {
	case listed.TimedOut:
		return nil, moduleListError(
			"gomutants: module: "+query.describe()+" did not answer within the command timeout", listed)
	case listed.ExitCode != 0:
		return nil, moduleListError(
			"gomutants: module: "+query.describe()+" exited with status "+strconv.Itoa(listed.ExitCode), listed)
	case listed.Truncated:
		return nil, moduleListError(
			"gomutants: module: "+query.describe()+" produced "+strconv.FormatInt(listed.TotalBytes, 10)+
				" bytes, more than the "+strconv.Itoa(moduleListLimit)+" this call keeps; "+
				"the tail of a JSON stream is not a shorter document but an unparsable one", listed)
	}

	decoder := json.NewDecoder(bytes.NewReader(listed.stdout))
	packages := make([]Package, 0, 16)
	for decoder.More() {
		var one listedPackage
		if err := decoder.Decode(&one); err != nil {
			return nil, moduleListError("gomutants: module: reading the "+query.describe()+
				" document: "+err.Error(), listed)
		}
		packages = append(packages, Package{
			ImportPath:   one.ImportPath,
			Dir:          one.Dir,
			Name:         one.Name,
			HasTests:     len(one.TestGoFiles) != 0 || len(one.XTestGoFiles) != 0,
			GoFiles:      one.GoFiles,
			TestGoFiles:  one.TestGoFiles,
			XTestGoFiles: one.XTestGoFiles,
			Imports:      one.Imports,
			Deps:         one.Deps,
			EmbedFiles:   one.EmbedFiles,
		})
	}
	// Sorted here rather than trusted from the go command: the order of a
	// listing over several patterns follows the patterns, and a consumer that
	// binary-searches or diffs two listings is entitled to one order. The
	// compaction is defensive — `go list` deduplicates overlapping patterns
	// itself — and costs one pass over a sorted slice.
	slices.SortFunc(packages, func(a, b Package) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	packages = slices.CompactFunc(packages, func(a, b Package) bool { return a.ImportPath == b.ImportPath })
	return packages, nil
}

// moduleListError types one `go list` this call could not use.
//
// It is an [ExecutionError] rather than a type of its own because it is the
// same fact that type already carries: a child go-mutants started could not
// answer, and the answer a caller needs is the toolchain's own output rather
// than a sentence go-mutants wrote. `Call` is `module`, beside the session's
// three.
//
// The output carried is the *combined* capture and not the document half. What
// a person reads a failure for is the diagnostic, and for a `go list` the
// diagnostic is on stderr — a listing that quoted its own empty stdout would be
// a refusal with nothing in it.
func moduleListError(message string, listed commandRun) error {
	return &ExecutionError{
		Call:   moduleCall,
		Output: string(listed.Output),
		cause:  errors.New(message),
	}
}

// normaliseModuleQuery is the query the engine will ask, or the refusal that
// says why it will not.
//
// The two halves are normalised differently on purpose. Patterns keep the order
// they were given — it is what a refusal quotes and what the recording shows,
// and it cannot change an answer that comes back sorted — while tags are sorted
// and deduplicated, because `-tags a,b` and `-tags b,a` select one file set and
// two memo entries for one question would be one `go list` too many.
func normaliseModuleQuery(q ModuleQuery) (moduleQuery, error) {
	packages := slices.Clone(q.Packages)
	if len(packages) == 0 {
		packages = []string{moduleAllPackages}
	}
	for _, pattern := range packages {
		if err := checkModulePattern(pattern); err != nil {
			return moduleQuery{}, err
		}
	}
	tags := slices.Clone(q.Tags)
	for _, tag := range tags {
		if strings.TrimSpace(tag) == "" || strings.ContainsAny(tag, ", \t\r\n") {
			return moduleQuery{}, fmt.Errorf(
				"%w: build tag %q must be one tag, with no comma and no whitespace", ErrInvalidQuery, tag)
		}
	}
	slices.Sort(tags)
	tags = slices.Compact(tags)
	return moduleQuery{packages: packages, tags: tags}, nil
}

// checkModulePattern is every shape of pattern this call refuses, each with the
// reason that tells a caller what to write instead.
//
// The dash is first because it is the only one that is dangerous rather than
// merely wrong: the go command reads a leading `-` on a positional argument as
// a flag, so a pattern spelled that way would not be refused by `go list` — it
// would be *obeyed*, as a flag nobody meant to pass.
func checkModulePattern(pattern string) error {
	switch {
	case strings.TrimSpace(pattern) == "":
		return fmt.Errorf("%w: a package pattern is empty", ErrInvalidQuery)
	case strings.HasPrefix(pattern, "-"):
		return fmt.Errorf("%w: package pattern %q begins with a dash, which the go command reads as a flag",
			ErrInvalidQuery, pattern)
	case absoluteAnywhere(pattern):
		return fmt.Errorf("%w: package pattern %q is absolute; patterns are module-relative",
			ErrInvalidQuery, pattern)
	case slices.Contains(strings.Split(strings.ReplaceAll(pattern, `\`, "/"), "/"), ".."):
		return fmt.Errorf("%w: package pattern %q escapes the module", ErrInvalidQuery, pattern)
	case !relativePackagePattern(pattern):
		return fmt.Errorf(`%w: package pattern %q is not module-relative; use "." or a "./" pattern`,
			ErrInvalidQuery, pattern)
	}
	return nil
}

// absoluteAnywhere reports whether a pattern is absolute on *any* supported
// platform rather than only on this one.
//
// The refusal has to be portable because the query is not: a consumer composes
// `ModuleQuery.Packages` from its own configuration, and `C:\src\thing` typed
// on Windows reaches a Linux CI runner unchanged. Left to [filepath.IsAbs] that
// pattern would be refused on one operating system as absolute and on the other
// as "not module-relative", which is two answers to one mistake — and only one
// of them tells the reader what is actually wrong with it.
//
// Three shapes are checked outright, and the platform's own rules after them so
// that nothing this list has not thought of slips through:
//
//   - a leading `/`, which is a POSIX absolute path;
//   - a leading `\\`, which is a UNC or extended-length Windows path
//     (`\\server\share`, `\\?\C:\x`);
//   - a drive letter followed by a colon and a separator (`C:\x`, `C:/x`), or a
//     bare `C:`. A drive-*relative* `C:x` is left to the module-relative check,
//     because it is not absolute and saying so would be wrong.
func absoluteAnywhere(pattern string) bool {
	native := filepath.FromSlash(pattern)
	switch {
	case strings.HasPrefix(pattern, "/"), strings.HasPrefix(pattern, `\\`):
		return true
	case filepath.IsAbs(native), filepath.VolumeName(native) != "":
		return true
	case len(pattern) < 2 || pattern[1] != ':' || !isDriveLetter(pattern[0]):
		return false
	}
	return len(pattern) == 2 || pattern[2] == '/' || pattern[2] == '\\'
}

func isDriveLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// argv is the `go list` this query asks for.
//
// `-e=false` is spelled out rather than left to the default because it is the
// difference between this call and a quieter, worse one: with `-e` a package
// that does not load is reported as a package with an error field and the
// command exits zero, so a broken module would come back as a listing rather
// than as the failure it is. The field list bounds the document; see
// [moduleListFields].
func (q moduleQuery) argv() []string {
	argv := make([]string, 0, len(q.packages)+4)
	argv = append(argv, "go", "list", "-e=false", "-json="+moduleListFields)
	if len(q.tags) != 0 {
		argv = append(argv, "-tags="+strings.Join(q.tags, ","))
	}
	return append(argv, q.packages...)
}

// key is what this query is memoised under.
//
// Every element is quoted, which is what makes the key injective: a pattern
// holding a space and two patterns are otherwise the same string, and a memo
// that confused them would answer one question with another's package set.
func (q moduleQuery) key() string {
	var key strings.Builder
	for _, pattern := range q.packages {
		key.WriteString(strconv.Quote(pattern))
		key.WriteByte(' ')
	}
	key.WriteString("-tags ")
	for _, tag := range q.tags {
		key.WriteString(strconv.Quote(tag))
		key.WriteByte(' ')
	}
	return key.String()
}

// describe names this query in a failure, as the whole command somebody would
// type: `go` included, because a message quoting `list -e=false …` is one a
// reader cannot paste into a shell.
//
// It is the argv as this package composed it rather than as the child received
// it, so `go` stands where [Workspace.runCommand] substitutes the located
// toolchain's absolute path. That is the readable half of the two, and the
// recording's `exec` event carries the resolved one for anybody reproducing the
// failure exactly.
func (q moduleQuery) describe() string {
	return "`" + strings.Join(q.argv(), " ") + "`"
}

// cloneModule is the copy a caller receives.
//
// A memoised answer is handed to every caller that asks the same question, so
// what comes back has to be a value one of them may sort, filter or edit
// without changing what the next one is told.
//
// A nil Packages stays nil. It is the zero value every failing path returns,
// and a clone that turned it into an empty non-nil slice would make the same
// failure two different values depending on which caller was holding it.
func cloneModule(module Module) Module {
	clone := module
	if module.Packages == nil {
		return clone
	}
	clone.Packages = make([]Package, len(module.Packages))
	for i, pkg := range module.Packages {
		clone.Packages[i] = Package{
			ImportPath:   pkg.ImportPath,
			Dir:          pkg.Dir,
			Name:         pkg.Name,
			HasTests:     pkg.HasTests,
			GoFiles:      slices.Clone(pkg.GoFiles),
			TestGoFiles:  slices.Clone(pkg.TestGoFiles),
			XTestGoFiles: slices.Clone(pkg.XTestGoFiles),
			Imports:      slices.Clone(pkg.Imports),
			Deps:         slices.Clone(pkg.Deps),
			EmbedFiles:   slices.Clone(pkg.EmbedFiles),
		}
	}
	return clone
}
