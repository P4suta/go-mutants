// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/drift"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/operatorselect"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testflag"
	"github.com/P4suta/go-mutants/internal/validate"
	"github.com/P4suta/go-mutants/trace"
)

const (
	defaultProfile       = "balanced"
	defaultMutantTimeout = 10 * time.Second
	// defaultMutantMemory is the floor under a session's per-mutant memory
	// bound, and the whole of it for a session whose verification was skipped.
	//
	// It is engine.MinDerivedMemory's number and it is written again here for
	// the reason defaultMutantTimeout is: a session has no baseline phase, so
	// the two derive from different observations and neither should have to
	// import the other's package to say what a small suite gets. A gibibyte is
	// justified where the engine's constant is documented; the short of it is
	// that anything tighter is tripped by a `-cover` build or a race-detecting
	// one, and anything looser is not a bound.
	defaultMutantMemory = 1 << 30
	// mutantMemoryFactor multiplies the peak the verification run reached.
	mutantMemoryFactor   = 4
	defaultBuildTimeout  = 10 * time.Minute
	sessionPrefix        = "session-"
	execPrefix           = "exec-"
	probePrefix          = "probe-"
	infectionLogName     = "infection.log"
	maximumArtifacts     = 128
	maximumArtifactBytes = 2 << 20
	maximumArtifactsSize = 16 << 20
	mainOverlayName      = "main-overlay"
	probeOverlayName     = "probe-overlay"
	overlayManifestName  = "overlay.json"
	// stageFreezeBuildInputs names the recorded step that copies the frozen
	// tree. It is a trace stage rather than a [PreparePhase] because the phase
	// vocabulary is shared with goatest and closed; see [freezeBuildInputs].
	stageFreezeBuildInputs = "freeze-build-inputs"
	privateDirectoryMode   = 0o700
	privateFileMode        = 0o600
	uncachedTestFlag       = "-count=1"
)

// Session is a validated mutation catalog with reusable test binaries.
type Session struct {
	mu             sync.RWMutex
	root           string
	scratch        string
	env            []string
	catalog        *mutation.Catalog
	publicCatalog  Catalog
	accepted       map[string]bool
	rejections     map[string]Rejection
	binaries       []execute.TestBinary
	executeOptions execute.Options
	mutantTimeout  time.Duration
	// mutantMemory is the per-mutant memory bound a request that names none
	// gets, in bytes, and is zero on a platform that cannot enforce one. See
	// [defaultMutantMemory].
	mutantMemory  int64
	preparedFiles map[string]fileState
	overlayPath   string
	closed        bool

	probeSnapshot *snapshot.Snapshot
	probeBinaries []execute.TestBinary
	probeOptions  execute.Options
	probeOverlay  string

	// keepTemp is the workspace's OpenOptions.KeepTemp. A kept session leaves
	// its probe tree on disk and leaves its own scratch directory alone: the
	// scratch lives inside the workspace's, which is being kept too.
	keepTemp bool
	// preserved names the durable directories a kept session left behind — its
	// probe tree, and nothing else — for the Workspace to report and to record
	// at Close. keptScratch names the per-call scratch of every execution and
	// probe pass a kept session made; those are recorded where they are kept
	// and only reported here.
	//
	// keptScratch has a lock of its own because Exec and Probe write it while
	// holding the read half of the session's, which is what lets them run
	// concurrently; a reader cannot take the writer without deadlocking itself.
	keepMu      sync.Mutex
	keptScratch []string
	preserved   []keptDirectory

	// recorder is the workspace's, so that one recording holds the preparation,
	// the builds under it, and every execution and probe pass that follows. It
	// is never nil for a session Prepare returned, and every method on it is
	// safe on one that is.
	recorder *trace.Recorder
}

type mainBuildResult struct {
	options  execute.Options
	binaries []execute.TestBinary
}

type probeBuildResult struct {
	options  execute.Options
	binaries []execute.TestBinary
	probed   map[string]bool
	overlay  string
}

// Prepare discovers, validates, instruments, verifies, and builds one reusable
// mutation session. A Workspace may be prepared exactly once, including when
// preparation fails after it has begun.
//
// It does not hold the workspace against commands. [Workspace.Exec] runs beside
// a preparation and waits only for its *instrumentation window* — the stretch
// from the integrity gate to the end of `main_restoration`, during which the
// sources on disk have been rewritten — so a consumer may run its own `go vet`,
// `go build` or baseline against the same workspace rather than opening a
// second one for them. [Workspace.Exec] sets out the whole rule.
//
// A second Prepare is refused with [ErrWorkspacePrepared] straight away rather
// than queued behind the first. A preparation refused for an *option* this
// engine does not accept leaves the workspace unspent: nothing has been read or
// written, and the next Prepare is judged on its own request — though a second
// Prepare that arrives during that first one's argument check is refused, where
// the exclusive lock this replaced would have made it wait and then serve it.
//
// [PrepareOptions.Trace] callbacks run on this call's own goroutine, so one
// must not call back into the workspace. Inside the instrumentation window that
// goroutine holds the tree exclusively and the call would wait for it; outside
// the window the call runs, until a [Workspace.Close] queues for the workspace
// — Go's RWMutex hands out no more read locks once a writer is waiting, and the
// command, the Close and the preparation then wait for each other.
func (w *Workspace) Prepare(ctx context.Context, options PrepareOptions) (*Session, error) {
	if w == nil {
		return nil, errors.New("gomutants: prepare: nil workspace")
	}
	return w.prepare(ctx, options)
}

func (w *Workspace) prepare(ctx context.Context, options PrepareOptions) (session *Session, err error) {
	// Shared rather than exclusive, and held for the whole call. Shared,
	// because a preparation is minutes of reading the frozen tree and compiling
	// it and only seconds of rewriting it, and the seconds are what
	// [Workspace.tree] is for. Held for all of it, because [Workspace.Close]
	// takes the same lock exclusively, and a workspace that removed its
	// snapshot while a preparation was still compiling in it would be a
	// use-after-free with a friendlier name.
	w.mu.RLock()
	defer w.mu.RUnlock()
	// Everything above the recorder's note is a refusal rather than a
	// preparation failure: a closed workspace, one already prepared, a
	// cancelled context, an option that is not a value this engine accepts.
	// None of them started a preparation, so a `prepare-failed` note about one
	// would name no phase and describe nothing that happened.
	//
	// The claim is taken here, before anything is read, and it is the whole of
	// what refuses a second Prepare. That used to be the write lock, which
	// meant a second caller waited out the first preparation's ten minutes in
	// order to be told it was never going to be allowed one.
	if err = w.beginPrepare(); err != nil {
		return nil, err
	}
	// The workspace is spent once the options are accepted, and not one line
	// earlier. Spending it is about a preparation that *began*: one that
	// stopped part-way may have left instrumented sources in the frozen tree,
	// so the tree promises nothing and every Workspace.Exec is refused. Nothing
	// above that point has read or written anything — the refusals are a state
	// check and an argument check — so charging a caller a fresh workspace and
	// a fresh snapshot for a typo in a line number would be charging it for
	// damage nothing did.
	//
	// The integrity gate below is deliberately on the other side: it reads the
	// tree, and a snapshot that has already moved is spent whatever the caller
	// does next.
	spent := false
	// From here on a failure is one this workspace had, and the recording says
	// which phase it was in. Deferred rather than written at each return,
	// because a preparation has a dozen of them and the one that would be
	// forgotten is the one somebody is reading the recording to find.
	//
	// What it keys on is the *session*, not the error, and that is the
	// difference between a rule and a rule with a hole in it. A panic — a
	// consumer's own Trace callback is ordinary Go code, and ordinary Go code
	// panics — unwinds through this function with the named error still nil,
	// and a workspace that read that as success would go on serving commands
	// against a tree instrumentation may have been halfway through. So the
	// question asked here is "did this call publish a session", which only the
	// one successful path can answer yes to.
	defer func() {
		if !spent {
			w.abandonPrepare()
			return
		}
		if err == nil && session != nil {
			return
		}
		w.prepareDidFail()
		if err != nil {
			w.recorder.Note(trace.NotePrepareFailed, "", prepareFailedDetail(err))
		}
	}()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("gomutants: prepare: %w", ctxErr)
	}
	resolved, err := resolvePrepareOptions(options)
	if err != nil {
		return nil, err
	}
	spent = true
	phases := newPrepareTrace(resolved.Trace, w.recorder)
	rules, err := selectRules(resolved.Profile, resolved.Operators)
	if err != nil {
		return nil, err
	}
	include, err := discover.CompilePatterns(resolved.Include)
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare include patterns: %w", err)
	}
	exclude, err := discover.CompilePatterns(resolved.Exclude)
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare exclude patterns: %w", err)
	}

	var found discover.Result
	var catalog *mutation.Catalog
	var pristineSources map[string]sourceImage
	var hints instrument.Hints
	// Discovery reads the tree and does not write it, so it holds the tree
	// shared and a command may run beside it. What that costs is stated by the
	// gate below: a command that wrote a source file here would be catalogued,
	// and the catalogue is checked against the frozen manifest before anything
	// is built from it.
	err = w.underTreeRead(func() error {
		return phases.run(PreparePhaseDiscovery, func() error {
			found, err = discover.Discover(ctx, discover.Options{
				SnapshotRoot: w.snapshot.Root,
				Toolchain:    w.toolchain,
				Env:          slices.Clone(w.env),
				Rules:        rules,
				Include:      include,
				Exclude:      exclude,
				Packages:     slices.Clone(resolved.DiscoveryPackages),
			})
			if err != nil {
				return buildError(PreparePhaseDiscovery, fmt.Errorf("gomutants: prepare discovery: %w", err))
			}
			catalog, err = discover.BuildCatalog(found)
			if err != nil {
				return fmt.Errorf("gomutants: prepare catalog: %w", err)
			}
			pristineSources, err = captureInstrumentationSources(w.snapshot.Root, catalog)
			if err != nil {
				return err
			}
			hints, err = instrument.HintsOf(found.Candidates)
			if err != nil {
				return fmt.Errorf("gomutants: prepare instrumentation hints: %w", err)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	validationEnv, err := overlayEnvironment(w.env, []string{"GOWORK=off"})
	if err != nil {
		return nil, fmt.Errorf("gomutants: prepare validation environment: %w", err)
	}
	validationEnv = prependEnvironmentPath(validationEnv, filepath.Dir(w.toolchain.GoBin))

	// The probe tree is copied here and nowhere later, and the ordering is
	// forced rather than chosen. A workspace holds one snapshot, validation
	// instruments it *in place*, and the probe tree has to be the same source
	// as the mutant tree — so the copy is taken from the pristine snapshot
	// before the next line rewrites it, and from the snapshot rather than from
	// the user's tree, which may have moved since Open froze it.
	//
	// It is a read of the whole tree, so it holds the tree shared and overlaps
	// commands like discovery does. Its own digest comparison is what stands in
	// for the gate here: a copy taken while anything was writing is a copy that
	// does not digest as the snapshot, and it is refused rather than probed.
	var probeSnap *snapshot.Snapshot
	if resolved.Probe {
		err = w.underTreeRead(func() error {
			return phases.run(PreparePhaseProbeSnapshot, func() error {
				started := time.Now()
				probeSnap, err = snapshot.Create(w.snapshot.Root, snapshot.Options{
					DestParent: w.snapshot.Parent(),
				})
				recordSnapshot(w.recorder, trace.SnapshotKindProbe, w.snapshot.Root, probeSnap, time.Since(started), err)
				if err != nil {
					return fmt.Errorf("gomutants: prepare probe snapshot: %w", err)
				}
				if probeSnap.WorkspaceDigest != w.snapshot.WorkspaceDigest {
					return fmt.Errorf(
						"gomutants: prepare probe snapshot digest %s does not match the mutant snapshot's %s",
						probeSnap.WorkspaceDigest, w.snapshot.WorkspaceDigest)
				}
				return nil
			})
		})
		if err != nil {
			return failPrepare(probeSnap, "", w.keepTemp, err)
		}
	} else {
		phases.skip(PreparePhaseProbeSnapshot)
	}
	// The session's scratch directory, declared here because the failure path
	// below has to be able to remove it: it is made inside the window, and from
	// that moment it holds a copy of every frozen file.
	var scratch string
	// Every return from here on goes through fail, so that a probe tree copied
	// and then abandoned — or a frozen copy taken and then given up on — does
	// not outlive the call that made it, as Open cleans up its own snapshot
	// when the scratch directory beside it fails.
	fail := func(err error) (*Session, error) { return failPrepare(probeSnap, scratch, w.keepTemp, err) }

	// The instrumentation window: the integrity gate, the check on what
	// discovery read, and the two phases that rewrite the tree and put it back.
	// It is the only stretch of a preparation during which the files on disk
	// are not the program anybody wrote, and it is therefore the only stretch a
	// command has to be kept out of. Taking the tree exclusively here waits for
	// every command already running — a long baseline delays the window and can
	// never corrupt it — and blocks every command issued while it is held,
	// which runs against the restored tree afterwards.
	var validated validate.Result
	var overlayPath string
	var frozen frozenInputs
	err = w.underTreeWrite(func() (windowErr error) {
		// The failure is published from *inside* the window, before the unlock
		// that wakes the commands queued behind it. Leaving it to the deferred
		// note above would have every one of those commands run first, on a
		// tree this preparation has just given up on with the instrumented
		// sources still in it — and succeed, quietly answering about a program
		// nobody wrote. It is the one place stateMu is taken under the tree
		// lock, which is why the order is mu, then tree, then stateMu.
		defer func() {
			if windowErr != nil {
				w.prepareDidFail()
			}
		}()
		if pristineErr := checkPristineSnapshot(w.snapshot); pristineErr != nil {
			return pristineErr
		}
		if driftErr := checkDiscoveredSources(
			w.snapshot.Manifest, found.SourceDigests, catalog, pristineSources); driftErr != nil {
			return driftErr
		}
		// The build's inputs are taken here, out of a tree the two checks above
		// have just proved is the manifest and before the next statement
		// rewrites it. Everything downstream — the overlay, the test binaries,
		// the baseline [Session.Changes] measures against — is about these
		// bytes and not about what the tree holds later, so a command writing
		// there while the binaries compile changes the tree and nothing else.
		scratch, err = os.MkdirTemp(w.scratch, sessionPrefix)
		if err != nil {
			return fmt.Errorf("gomutants: prepare session scratch: %w", err)
		}
		// Recorded as a stage rather than as a phase: the phase vocabulary is
		// goatest's and closed, and this is exactly what a stage is for — a
		// step inside a phase that a reader of a slow preparation needs to see.
		endFreeze := w.recorder.Stage(stageFreezeBuildInputs, frozenInputsDetail(w.snapshot.Manifest))
		frozen, err = freezeBuildInputs(
			ctx, w.snapshot.Root, w.snapshot.Manifest, filepath.Join(scratch, frozenInputsName))
		endFreeze(stageResult(err))
		if err != nil {
			return fmt.Errorf("gomutants: prepare frozen build inputs: %w", err)
		}
		if validationErr := phases.run(PreparePhaseMainValidation, func() error {
			validated, err = validate.Validate(ctx, validate.Options{
				Snap:         w.snapshot,
				Catalog:      catalog,
				Hints:        hints,
				ModulePath:   found.ModulePath,
				Toolchain:    w.toolchain,
				Jobs:         resolved.Jobs,
				BuildTimeout: resolved.BuildTimeout,
				Env:          validationEnv,
				Packages:     resolved.DiscoveryPackages,
				Trace:        w.recorder,
			})
			if err != nil {
				return buildError(PreparePhaseMainValidation, fmt.Errorf("gomutants: prepare validation: %w", err))
			}
			return nil
		}); validationErr != nil {
			return validationErr
		}
		return phases.run(PreparePhaseMainRestoration, func() error {
			overlayPath, err = writeInstrumentationOverlay(w.snapshot.Root, frozen.resolvedRoot,
				scratch, mainOverlayName, validated.Instrumented, frozen.replacements)
			if err != nil {
				return fmt.Errorf("gomutants: prepare instrumentation overlay: %w", err)
			}
			// The manifest is what a consumer needs to reproduce an execution by
			// hand — `GOFLAGS=-overlay=<manifest>` in the snapshot — so it is in the
			// recording as well as behind [Session.OverlayManifest].
			w.recorder.Artifact(trace.ArtifactOverlayManifest, overlayPath)
			if err = restoreInstrumentationSources(w.snapshot.Root, pristineSources, validated.Instrumented); err != nil {
				return fmt.Errorf("gomutants: prepare restore source tree: %w", err)
			}
			if driftErr := checkInitialDrift(w.snapshot, instrument.Result{}, "source restoration"); driftErr != nil {
				return driftErr
			}
			return nil
		})
	})
	if err != nil {
		return fail(err)
	}

	// What the unmutated tests cost, measured by the one run this session makes
	// of them. It is the session's equivalent of the engine's baseline peak and
	// it is zero for a session that skipped verification or ran on a platform
	// that cannot measure, both of which leave the bound at its floor.
	var verifiedPeak int64
	if !resolved.SkipVerify {
		// Under the tree's shared half, exactly as [Workspace.Exec] runs: this
		// is a command against the restored tree like any other, and its drift
		// check reads the same bytes. It costs nothing — a command holds the
		// same half, so the two run side by side — and it keeps "everything
		// that reads the tree holds the tree" a rule with no exceptions to
		// remember.
		err = w.underTreeRead(func() error {
			return phases.run(PreparePhaseVerification, func() error {
				verify := resolved.Verify
				verifyBase, verifyErr := overlayEnvironment(w.env, verify.Env)
				if verifyErr != nil {
					return fmt.Errorf("gomutants: prepare verification environment: %w", verifyErr)
				}
				verify.Env = nil
				verifyBase, verifyErr = instrumentationEnvironment(verifyBase, overlayPath)
				if verifyErr != nil {
					return fmt.Errorf("gomutants: prepare verification overlay: %w", verifyErr)
				}
				verified, verifyErr := w.runCommand(ctx, verify, verifyBase,
					commandLabel{kind: trace.ExecKindVerify})
				if verifyErr != nil {
					return fmt.Errorf("gomutants: prepare instrumented verification: %w", verifyErr)
				}
				if verified.TimedOut || verified.ExitCode != 0 {
					return &VerificationError{
						Command:    verify,
						ExitCode:   verified.ExitCode,
						TimedOut:   verified.TimedOut,
						Duration:   verified.Duration,
						Output:     verified.Output,
						Truncated:  verified.Truncated,
						TotalBytes: verified.TotalBytes,
					}
				}
				verifiedPeak = verified.PeakMemory
				if driftErr := checkInitialDrift(w.snapshot, instrument.Result{}, "verification"); driftErr != nil {
					return driftErr
				}
				return nil
			})
		})
		if err != nil {
			return fail(err)
		}
	} else {
		phases.skip(PreparePhaseVerification)
	}

	mainSpan := phases.begin(PreparePhaseBinaryBuild)
	mainFinished := make(chan PrepareEvent, 1)
	mainBuild, probeBuild, err := runPreparationBuilds(ctx,
		func(ctx context.Context) (mainBuildResult, error) {
			var result mainBuildResult
			buildErr := func() error {
				executionEnv, envErr := instrumentationEnvironment(w.env, overlayPath)
				if envErr != nil {
					return fmt.Errorf("gomutants: prepare execution overlay: %w", envErr)
				}
				result.options = execute.Options{
					Toolchain:    w.toolchain,
					SnapshotRoot: w.snapshot.Root,
					Packages:     slices.Clone(resolved.Packages),
					BinDir:       filepath.Join(scratch, "bin"),
					ScratchDir:   filepath.Join(scratch, "targets"),
					Env:          executionEnv,
					Jobs:         resolved.Jobs,
					Timeout:      resolved.BuildTimeout,
					Trace:        w.recorder,
				}
				var binaryErr error
				result.binaries, binaryErr = execute.BuildTestBinaries(ctx, result.options)
				if binaryErr != nil {
					return buildError(PreparePhaseBinaryBuild,
						fmt.Errorf("gomutants: prepare test binaries: %w", binaryErr))
				}
				return nil
			}()
			mainFinished <- mainSpan.complete(buildErr)
			// Tagged here rather than by [prepareTrace.run], because this is
			// the one phase driven by a span of its own: it starts before the
			// probe tree's three and finishes after them, so it cannot be a
			// call that returns when the work does.
			return result, inPhase(PreparePhaseBinaryBuild, buildErr)
		},
		func(ctx context.Context) (probeBuildResult, error) {
			options, binaries, probed, overlay, probeErr := prepareProbeTree(ctx, probeTreeOptions{
				snap:               probeSnap,
				catalog:            catalog,
				hints:              hints,
				modulePath:         found.ModulePath,
				toolchain:          w.toolchain,
				jobs:               resolved.Jobs,
				buildTimeout:       resolved.BuildTimeout,
				packages:           resolved.Packages,
				validationPackages: resolved.DiscoveryPackages,
				coverPackages:      resolved.ProbeCoverPackages,
				env:                w.env,
				validateEnv:        validationEnv,
				scratch:            scratch,
				pristineSources:    pristineSources,
				phases:             phases,
				recorder:           w.recorder,
			})
			return probeBuildResult{options: options, binaries: binaries, probed: probed, overlay: overlay}, probeErr
		},
	)
	phases.finish(<-mainFinished)
	if err != nil {
		return fail(err)
	}
	// No frozen file reaches the compiler from the tree, and that is why
	// nothing here re-digests it. The binaries were compiled from the copies
	// [freezeBuildInputs] took at the top of the window, through the overlay,
	// so a command that rewrote a frozen file while they compiled — and equally
	// one that wrote and undid it between two of the compiler's reads, which no
	// digest could ever have caught — changed nothing the session is made of.
	// What the go command does still read off the disk is the package
	// directories themselves, so a file a command *adds* to one is compiled in;
	// that is the residual named in ADR 0007, and a re-digest here would not be
	// the fix for it, because it never was one for the transient case. Either
	// way the tree every later target runs in has moved, and that is reported
	// by [Session.Changes] against the manifest baseline below.

	accepted := make(map[string]bool, len(validated.AcceptedIDs))
	for _, id := range validated.AcceptedIDs {
		accepted[id] = true
	}
	publicCatalog, rejectionIndex := makeCatalog(
		w.snapshot.WorkspaceDigest,
		w.toolchain,
		resolved.Profile,
		found,
		catalog,
		validated.Rejected,
		accepted,
		probeBuild.probed,
		mainBuild.binaries,
		resolved.Selection,
	)
	session = &Session{
		root:           w.snapshot.Root,
		scratch:        scratch,
		env:            slices.Clone(w.env),
		catalog:        catalog,
		publicCatalog:  publicCatalog,
		accepted:       accepted,
		rejections:     rejectionIndex,
		binaries:       slices.Clone(mainBuild.binaries),
		executeOptions: mainBuild.options,
		mutantTimeout:  resolved.MutantTimeout,
		mutantMemory:   sessionMemoryBound(verifiedPeak),
		preparedFiles:  frozen.files,
		overlayPath:    overlayPath,
		probeSnapshot:  probeSnap,
		probeBinaries:  probeBuild.binaries,
		probeOptions:   probeBuild.options,
		probeOverlay:   probeBuild.overlay,
		keepTemp:       w.keepTemp,
		recorder:       w.recorder,
	}
	w.publishSession(session)
	return session, nil
}

// sessionMemoryBound resolves the per-mutant memory bound a session applies
// where a request names none.
//
// It is the engine's derivation with the engine's baseline replaced by the one
// measurement a session has: what the verification run of the unmutated tests
// cost. A session that skipped verification, or ran where nothing could measure,
// gets the floor rather than no bound — which is the one place this differs
// from the engine, and it differs because the two are answering different
// questions. The engine has measured the suite and can say "I have nothing to
// derive from"; a session with SkipVerify has been told not to measure, and
// leaving it unbounded would mean a consumer that turned verification off also
// silently turned the bound off.
//
// A platform that cannot enforce a bound gets zero either way, because a number
// nothing enforces is worse than an honest absence: it would appear in a
// consumer's own report as a budget that was never applied.
func sessionMemoryBound(peak int64) int64 {
	if !runner.MemoryBoundSupported() {
		return 0
	}
	return max(int64(defaultMutantMemory), mutantMemoryFactor*peak)
}

// checkPristineSnapshot is the barrier between arbitrary commands and mutation
// instrumentation. Commands may be used for build, vet and baseline controls,
// and they may run beside a preparation, but none may silently rewrite the
// frozen program those controls are meant to justify.
//
// It runs at the top of the instrumentation window, with the tree held
// exclusively, and that is what makes its answer worth having: no command can
// be halfway through a write while it re-digests, and none can start one until
// restoration has put the sources back. What it can no longer see is a write
// that happened *and was undone* before it ran — during discovery, say — which
// is what [checkDiscoveredSources] is for.
func checkPristineSnapshot(snap *snapshot.Snapshot) error {
	drifts, err := snap.Redigest()
	if err != nil {
		return fmt.Errorf("gomutants: prepare snapshot integrity: %w", err)
	}
	if len(drifts) == 0 {
		return nil
	}
	return &DriftError{Stage: driftStageCommands, Changes: driftChanges(drifts)}
}

// checkDiscoveredSources requires every file discovery read to be the file the
// manifest froze.
//
// It exists because discovery reads the tree while a command may write it. The
// integrity gate above proves the tree is pristine *now*, and a command that
// changed a source file during discovery and put it back before the gate runs
// leaves nothing for the gate to find — while the catalogue it produced is full
// of mutants identified by the digest of bytes that are nowhere on disk. Every
// later answer about one of those, a cached one most of all, would be about a
// program nobody has.
//
// Three digests can answer for one path, and they are three different claims.
// [discover.Result.SourceDigests] is what discovery *read*, and it covers every
// file the pass opened rather than only the ones that yielded a mutant — which
// matters, because a file a transient edit emptied of everything mutable is
// read, walked, and catalogued as nothing at all. The catalogue's own
// SourceDigest answers for a path discovery recorded none for, so that a
// catalogue built by anything else is still checked. And the captured image is
// the bytes restoration will write back over the instrumented file, so it
// settles what the tree *becomes* rather than what it was.
//
// A path the manifest never held is the remaining case: a file a command
// created and removed again, whose mutants name a file the snapshot does not
// have.
func checkDiscoveredSources(
	manifest []snapshot.Entry, scanned map[string]string,
	catalog *mutation.Catalog, captured map[string]sourceImage,
) error {
	// Both sides of every lookup below are module-relative slash paths taken
	// from a listing of the same tree — the snapshot manifest's and
	// discovery's — so an exact map lookup is the comparison, and a
	// case-folding or separator-normalising one would only be able to conflate
	// two paths the same walk kept apart.
	frozen := make(map[string]string, len(manifest))
	for _, entry := range manifest {
		frozen[entry.RelPath] = entry.SHA256
	}
	observed := make(map[string]string, len(scanned)+len(captured))
	for path, digest := range scanned {
		observed[path] = digest
	}
	if catalog != nil {
		for _, mutant := range catalog.Mutants() {
			if _, ok := observed[mutant.Path]; !ok {
				observed[mutant.Path] = mutant.SourceDigest
			}
		}
	}
	// The captured image last and only where everything else agreed with the
	// manifest: what discovery read is what the identities were minted from, so
	// it is the more interesting answer when the two disagree, and the image is
	// the answer for a file discovery read pristine and the capture a moment
	// later did not.
	for path, image := range captured {
		if observed[path] == frozen[path] {
			if digest := mutation.Digest(image.data); digest != observed[path] {
				observed[path] = digest
			}
		}
	}

	var changes []Change
	for path, read := range observed {
		want, recorded := frozen[path]
		switch {
		case !recorded:
			changes = append(changes, Change{Kind: ChangeAdded, Path: path, AfterSHA256: read})
		case read != want:
			changes = append(changes, Change{
				Kind:         ChangeModified,
				Path:         path,
				BeforeSHA256: want,
				AfterSHA256:  read,
			})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	slices.SortFunc(changes, func(a, b Change) int { return strings.Compare(a.Path, b.Path) })
	return &DriftError{Stage: driftStageDiscovery, Changes: changes}
}

func runPreparationBuilds(
	ctx context.Context,
	main func(context.Context) (mainBuildResult, error),
	probe func(context.Context) (probeBuildResult, error),
) (mainBuildResult, probeBuildResult, error) {
	type completedMain struct {
		result   mainBuildResult
		err      error
		canceled bool
	}
	buildCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	mainFailure := errors.New("gomutants: main preparation build failed")
	probeFailure := errors.New("gomutants: probe preparation build failed")
	mainDone := make(chan completedMain, 1)
	go func() {
		result, err := main(buildCtx)
		canceled := buildContextCanceled(err, context.Cause(buildCtx))
		if err != nil && !canceled {
			cancel(mainFailure)
		}
		mainDone <- completedMain{result: result, err: err, canceled: canceled}
	}()
	probeResult, probeErr := probe(buildCtx)
	probeCanceled := buildContextCanceled(probeErr, context.Cause(buildCtx))
	if probeErr != nil && !probeCanceled {
		cancel(probeFailure)
	}
	mainResult := <-mainDone
	if mainResult.err != nil && !mainResult.canceled {
		return mainBuildResult{}, probeBuildResult{}, mainResult.err
	}
	if probeErr != nil && !probeCanceled {
		return mainBuildResult{}, probeBuildResult{}, probeErr
	}
	if mainResult.err != nil {
		return mainBuildResult{}, probeBuildResult{}, mainResult.err
	}
	if probeErr != nil {
		return mainBuildResult{}, probeBuildResult{}, probeErr
	}
	return mainResult.result, probeResult, nil
}

func buildContextCanceled(err, cause error) bool {
	return cause != nil && (errors.Is(err, context.Canceled) ||
		errors.Is(cause, context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded))
}

// failPrepare returns a preparation failure, removing the two whole copies of
// somebody's module a preparation may have taken by then.
//
// A probe tree is one, and the session's scratch directory — which since the
// binaries stopped being compiled from the tree holds a copy of every frozen
// file — is the other. Both are taken before a preparation can know it will
// finish, and nothing else knows either exists: the Session they would have
// belonged to is never returned, and the Workspace's own Close cleans up only
// the snapshot and the scratch it made itself. So a Prepare that gives up
// removes them, and a preparation that failed leaves a workspace no heavier
// than one that never started.
//
// Unless the caller asked to keep them. [OpenOptions.KeepTemp] exists so that
// somebody debugging a preparation can look at what it built, and the frozen
// copy is exactly what the binaries would have been compiled from. It needs no
// artifact of its own: it lives inside the workspace's scratch directory, which
// Close preserves and records as `kept-scratch`.
func failPrepare(probeSnap *snapshot.Snapshot, scratch string, keepTemp bool, err error) (*Session, error) {
	if scratch != "" && !keepTemp {
		if removeErr := os.RemoveAll(scratch); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
	}
	if probeSnap == nil {
		return nil, err
	}
	if cleanupErr := probeSnap.Cleanup(); cleanupErr != nil {
		return nil, errors.Join(err, cleanupErr)
	}
	return nil, err
}

// stageResult is what a recorded stage reports, derived from the step's own
// error so that no call site can report a failed step as a successful one.
func stageResult(err error) string {
	if err != nil {
		return trace.ResultFailed
	}
	return trace.ResultSucceeded
}

// probeTreeOptions is what [prepareProbeTree] needs, gathered so that the one
// caller reads as the decision it is making rather than as eleven arguments.
type probeTreeOptions struct {
	snap               *snapshot.Snapshot
	catalog            *mutation.Catalog
	hints              instrument.Hints
	modulePath         string
	toolchain          gocmd.Toolchain
	jobs               int
	buildTimeout       time.Duration
	packages           []string
	validationPackages []string
	coverPackages      []string
	env                []string
	validateEnv        []string
	scratch            string
	pristineSources    map[string]sourceImage
	phases             prepareTrace
	recorder           *trace.Recorder
}

// prepareProbeTree instruments, validates and builds the probe tree, and
// reports which mutants it ends up speaking for.
//
// A nil snapshot is the session prepared without one: no work, no binaries, and
// an empty probed set, which is what makes every [Mutant.Probed] false and
// [Session.Probe] refuse.
//
// The tree goes through the same [validate.Validate] the mutant tree does, with
// [instrument.ModeProbe], so a probe site that does not compile is bisected out
// by the phase that already knows how — and a rejection here is not a rejected
// mutant. The mutant is untouched in its own tree; what it loses is its probe.
//
// There is deliberately no verification command on this tree. The mutant tree's
// verify exists because a whole run is scored against it and one broken build
// would falsify every number; a probe pass is per call and already reports a
// failing target as "no facts", so a suite-wide gate here would buy a guarantee
// the per-call rule already gives and cost a full test run to get it.
//
// The probed map this returns is the probe tree's own two-clause answer, and
// not the whole of [Mutant.Probed]. The mutant must have a probe form —
// [instrument.Hints.Probes] — because a mutant with none leaves its file
// untouched and is therefore *accepted* by this validation exactly as a probed
// one is; and its site must have survived that validation, because a probe that
// did not compile was bisected back out of the tree. Reading either half as the
// whole would mark a mutant nothing can record as one whose silence means
// something.
//
// The third clause belongs to the mutant tree and is applied by [makeCatalog],
// which conjoins acceptance: this function cannot see that verdict, because it
// runs alongside the validation that produces it.
func prepareProbeTree(ctx context.Context, opts probeTreeOptions) (
	execute.Options, []execute.TestBinary, map[string]bool, string, error,
) {
	if opts.snap == nil {
		opts.phases.skip(PreparePhaseProbeValidation)
		opts.phases.skip(PreparePhaseProbeCoverageBuild)
		opts.phases.skip(PreparePhaseProbeRestoration)
		return execute.Options{}, nil, nil, "", nil
	}

	var validated validate.Result
	var overlayPath string
	err := opts.phases.run(PreparePhaseProbeValidation, func() error {
		var validateErr error
		validated, validateErr = validate.Validate(ctx, validate.Options{
			Snap:         opts.snap,
			Catalog:      opts.catalog,
			Hints:        opts.hints,
			ModulePath:   opts.modulePath,
			Toolchain:    opts.toolchain,
			Jobs:         opts.jobs,
			BuildTimeout: opts.buildTimeout,
			Env:          opts.validateEnv,
			Mode:         instrument.ModeProbe,
			Packages:     opts.validationPackages,
			Trace:        opts.recorder,
		})
		if validateErr != nil {
			return buildError(PreparePhaseProbeValidation,
				fmt.Errorf("gomutants: prepare probe validation: %w", validateErr))
		}
		if driftErr := checkInitialDrift(opts.snap, validated.Instrumented, "probe instrumentation"); driftErr != nil {
			return driftErr
		}
		// The probe tree is a snapshot of its own and is not compiled from the
		// frozen copies, so it resolves its own root — the same spelling
		// question, asked about a different tree.
		probeResolved, resolveErr := filepath.EvalSymlinks(opts.snap.Root)
		if resolveErr != nil {
			return fmt.Errorf("gomutants: prepare probe instrumentation overlay: %w", resolveErr)
		}
		overlayPath, validateErr = writeInstrumentationOverlay(opts.snap.Root, probeResolved,
			opts.scratch, probeOverlayName, validated.Instrumented, nil)
		if validateErr != nil {
			return fmt.Errorf("gomutants: prepare probe instrumentation overlay: %w", validateErr)
		}
		// The probe tree's own manifest, recorded under a kind of its own: the
		// two trees are reproduced with two different overlays, and one kind for
		// both would leave a reader guessing which tree a path belongs to.
		opts.recorder.Artifact(trace.ArtifactProbeOverlayManifest, overlayPath)
		return nil
	})
	if err != nil {
		return execute.Options{}, nil, nil, "", err
	}
	probeOptions := execute.Options{
		Toolchain:    opts.toolchain,
		SnapshotRoot: opts.snap.Root,
		Packages:     slices.Clone(opts.packages),
		BinDir:       filepath.Join(opts.scratch, "probe-bin"),
		ScratchDir:   filepath.Join(opts.scratch, "probe-targets"),
		Env:          slices.Clone(opts.env),
		Jobs:         opts.jobs,
		Timeout:      opts.buildTimeout,
		CoverPkg:     strings.Join(opts.coverPackages, ","),
		Trace:        opts.recorder,
	}
	var binaries []execute.TestBinary
	err = opts.phases.run(PreparePhaseProbeCoverageBuild, func() error {
		var buildErr error
		binaries, buildErr = execute.BuildTestBinaries(ctx, probeOptions)
		if buildErr != nil {
			return buildError(PreparePhaseProbeCoverageBuild,
				fmt.Errorf("gomutants: prepare probe test binaries: %w", buildErr))
		}
		return nil
	})
	if err != nil {
		return execute.Options{}, nil, nil, "", err
	}
	var probeEnv []string
	err = opts.phases.run(PreparePhaseProbeRestoration, func() error {
		if restoreErr := restoreInstrumentationSources(opts.snap.Root, opts.pristineSources, validated.Instrumented); restoreErr != nil {
			return fmt.Errorf("gomutants: prepare restore probe source tree: %w", restoreErr)
		}
		if driftErr := checkInitialDrift(opts.snap, instrument.Result{}, "probe source restoration"); driftErr != nil {
			return driftErr
		}
		var environmentErr error
		probeEnv, environmentErr = instrumentationEnvironment(opts.env, overlayPath)
		if environmentErr != nil {
			return fmt.Errorf("gomutants: prepare probe execution overlay: %w", environmentErr)
		}
		return nil
	})
	if err != nil {
		return execute.Options{}, nil, nil, "", err
	}
	probeOptions.Env = probeEnv

	survived := make(map[string]bool, len(validated.AcceptedIDs))
	for _, id := range validated.AcceptedIDs {
		survived[id] = true
	}
	probed := make(map[string]bool, len(survived))
	for _, m := range opts.catalog.Mutants() {
		if survived[m.ID] && opts.hints.Probes(m) {
			probed[m.ID] = true
		}
	}
	return probeOptions, binaries, probed, overlayPath, nil
}

type sourceImage struct {
	data []byte
	mode fs.FileMode
}

func captureInstrumentationSources(root string, catalog *mutation.Catalog) (map[string]sourceImage, error) {
	images := make(map[string]sourceImage)
	for _, mutant := range catalog.Mutants() {
		if _, captured := images[mutant.Path]; captured {
			continue
		}
		name := filepath.Join(root, filepath.FromSlash(mutant.Path))
		info, err := os.Stat(name)
		if err != nil {
			return nil, fmt.Errorf("gomutants: capture source %s: %w", mutant.Path, err)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("gomutants: capture source %s: %w", mutant.Path, err)
		}
		images[mutant.Path] = sourceImage{data: data, mode: info.Mode().Perm()}
	}
	return images, nil
}

func restoreInstrumentationSources(root string, images map[string]sourceImage, result instrument.Result) error {
	for _, path := range result.FilesInstrumented {
		image, captured := images[path]
		if !captured {
			return fmt.Errorf("no pristine source was captured for %s", path)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), image.data, image.mode); err != nil {
			return fmt.Errorf("restore source %s: %w", path, err)
		}
	}
	runtimeDirectory := filepath.FromSlash(result.RuntimeDir)
	if runtimeDirectory == "." || !filepath.IsLocal(runtimeDirectory) {
		return fmt.Errorf("generated runtime directory %q is not local", result.RuntimeDir)
	}
	if err := os.RemoveAll(filepath.Join(root, runtimeDirectory)); err != nil {
		return fmt.Errorf("remove generated runtime %s: %w", result.RuntimeDir, err)
	}
	return nil
}

// writeInstrumentationOverlay writes the `go` overlay manifest one tree is
// compiled and executed through.
//
// It is two mappings in one file, and their order is the point. `frozen` is
// every file the snapshot froze, copied by [freezeBuildInputs] into the
// preparation's own directory, so that a build reads no byte of the tree at
// all; the instrumented sources and the generated runtime are written over the
// top, because the mutated program is what the session exists to run. A file
// instrumentation did not touch keeps the frozen copy.
//
// `frozen` is nil for the probe tree, which is a separate snapshot with a
// separate overlay and is not covered by this.
//
// `resolvedRoot` is `root` with its symbolic links resolved, which is the
// spelling the go command looks a path up under on a platform that reaches its
// temporary directory through one. It is passed in rather than derived here so
// that it is the *same* spelling [freezeBuildInputs] keyed its half by. Two
// routes to the resolved path would agree in practice and the day they did not
// would be the worst day this package has: the frozen copy would sit on the key
// cmd/go reads and the instrumented copy on one it does not, so the session
// would compile the un-mutated program and report every mutant as a survivor,
// with nothing failing.
func writeInstrumentationOverlay(
	root, resolvedRoot, scratch, name string, result instrument.Result, frozen map[string]string,
) (string, error) {
	backingRoot := filepath.Join(scratch, name)
	if err := os.MkdirAll(backingRoot, privateDirectoryMode); err != nil {
		return "", err
	}
	paths := slices.Clone(result.FilesInstrumented)
	runtimeDirectory := filepath.FromSlash(result.RuntimeDir)
	if runtimeDirectory == "." || !filepath.IsLocal(runtimeDirectory) {
		return "", fmt.Errorf("generated runtime directory %q is not local", result.RuntimeDir)
	}
	err := filepath.WalkDir(filepath.Join(root, runtimeDirectory), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("generated runtime contains non-regular file %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return "", err
	}
	slices.Sort(paths)
	replacements := make(map[string]string, len(frozen)+2*len(paths))
	maps.Copy(replacements, frozen)
	for _, path := range paths {
		relative := filepath.FromSlash(path)
		if !filepath.IsLocal(relative) {
			return "", fmt.Errorf("instrumented source path %q is not local", path)
		}
		source := filepath.Join(root, relative)
		target := filepath.Join(backingRoot, relative)
		info, statErr := os.Stat(source)
		if statErr != nil {
			return "", statErr
		}
		data, readErr := os.ReadFile(source)
		if readErr != nil {
			return "", readErr
		}
		if mkdirErr := os.MkdirAll(filepath.Dir(target), privateDirectoryMode); mkdirErr != nil {
			return "", mkdirErr
		}
		if writeErr := os.WriteFile(target, data, info.Mode().Perm()); writeErr != nil {
			return "", writeErr
		}
		replacements[source] = target
		// Both spellings of the same file, for the reason given above the
		// signature, and spelled from the one resolved root rather than
		// resolved again per file.
		if resolvedRoot != root {
			replacements[filepath.Join(resolvedRoot, relative)] = target
		}
	}
	manifest, err := json.Marshal(struct {
		Replace map[string]string `json:"Replace"`
	}{Replace: replacements})
	if err != nil {
		return "", err
	}
	manifestPath := filepath.Join(scratch, name+"-"+overlayManifestName)
	if err := os.WriteFile(manifestPath, manifest, privateFileMode); err != nil {
		return "", err
	}
	return manifestPath, nil
}

func instrumentationEnvironment(base []string, overlayPath string) ([]string, error) {
	overlayFlag, err := quotedGoFlag("-overlay=" + overlayPath)
	if err != nil {
		return nil, err
	}
	env := gocmd.AppendGoflags(base, overlayFlag)
	env = gocmd.AppendGoflags(env, gocmd.VetOff)
	return gocmd.AppendGoflags(env, uncachedTestFlag), nil
}

func quotedGoFlag(flag string) (string, error) {
	if !strings.ContainsAny(flag, " \t\r\n") {
		return flag, nil
	}
	if !strings.ContainsRune(flag, '\'') {
		return "'" + flag + "'", nil
	}
	if !strings.ContainsRune(flag, '"') {
		return `"` + flag + `"`, nil
	}
	return "", fmt.Errorf("gomutants: go flag %q contains whitespace and both quote characters", flag)
}

func resolvePrepareOptions(opts PrepareOptions) (PrepareOptions, error) {
	if opts.Profile == "" {
		opts.Profile = defaultProfile
	}
	if _, err := mutation.ParseTier(opts.Profile); err != nil {
		return PrepareOptions{}, fmt.Errorf("gomutants: prepare profile %q: expected balanced, strong, or all", opts.Profile)
	}
	if opts.Jobs < 0 || opts.Jobs > config.MaxJobs {
		return PrepareOptions{}, fmt.Errorf("gomutants: prepare jobs %d: expected 0 through %d", opts.Jobs, config.MaxJobs)
	}
	if opts.Jobs == 0 {
		opts.Jobs = config.DefaultJobs()
	}
	if opts.BuildTimeout < 0 {
		return PrepareOptions{}, errors.New("gomutants: prepare build timeout is negative")
	}
	if opts.BuildTimeout == 0 {
		opts.BuildTimeout = defaultBuildTimeout
	}
	if opts.MutantTimeout < 0 {
		return PrepareOptions{}, errors.New("gomutants: prepare mutant timeout is negative")
	}
	if opts.MutantTimeout == 0 {
		opts.MutantTimeout = defaultMutantTimeout
	}
	if len(opts.Packages) == 0 {
		opts.Packages = []string{"./..."}
	}
	if len(opts.DiscoveryPackages) == 0 {
		opts.DiscoveryPackages = []string{"./..."}
	}
	for _, pattern := range opts.Packages {
		if !relativePackagePattern(pattern) {
			return PrepareOptions{}, fmt.Errorf("gomutants: prepare package pattern %q is not module-relative", pattern)
		}
	}
	for _, pattern := range opts.DiscoveryPackages {
		if !relativePackagePattern(pattern) {
			return PrepareOptions{}, fmt.Errorf("gomutants: prepare discovery package pattern %q is not module-relative", pattern)
		}
	}
	for _, pattern := range opts.ProbeCoverPackages {
		if strings.TrimSpace(pattern) == "" || strings.Contains(pattern, ",") {
			return PrepareOptions{}, fmt.Errorf("gomutants: prepare probe coverage package %q is invalid", pattern)
		}
	}
	// Here rather than where the catalogue is narrowed, which is the far side
	// of discovery, validation and two tree builds. A selection nobody can read
	// is a mistake in the caller's own request, and finding it after ten
	// minutes of preparation would be finding it after the expensive part.
	// Normalising here also means the value the session keeps is the canonical
	// one, and nothing downstream ever sees the caller's spelling.
	selection, err := normaliseSelection(opts.Selection)
	if err != nil {
		return PrepareOptions{}, err
	}
	opts.Selection = selection
	if opts.SkipVerify && (len(opts.Verify.Argv) != 0 || len(opts.Verify.Env) != 0 || opts.Verify.Dir != "" ||
		opts.Verify.Timeout != 0 || opts.Verify.OutputLimit != 0) {
		return PrepareOptions{}, errors.New("gomutants: prepare cannot combine skip verify with a verification command")
	}
	if !opts.SkipVerify && len(opts.Verify.Argv) == 0 {
		opts.Verify.Argv = []string{"go", "test", "./..."}
	}
	if !opts.SkipVerify && opts.Verify.Timeout == 0 {
		opts.Verify.Timeout = opts.BuildTimeout
	}
	return opts, nil
}

func relativePackagePattern(pattern string) bool {
	if pattern != "." && !strings.HasPrefix(pattern, "./") {
		return false
	}
	for element := range strings.SplitSeq(strings.ReplaceAll(pattern, `\`, "/"), "/") {
		if element == ".." {
			return false
		}
	}
	return true
}

func selectRules(profile string, operators []string) ([]mutation.Rule, error) {
	tier, _ := mutation.ParseTier(profile)
	rules, unknown := operatorselect.Select(tier, operators)
	if unknown != "" {
		return nil, fmt.Errorf("gomutants: prepare operator %q is not a canonical family or rule", unknown)
	}
	return rules, nil
}

// Catalog returns a deep copy of the session's deterministic catalog.
func (s *Session) Catalog() Catalog {
	if s == nil {
		return Catalog{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneCatalog(s.publicCatalog)
}

// OverlayManifest is the `go` overlay this session's mutant tree is compiled
// and executed through: the file `GOFLAGS=-overlay=<manifest>` names.
//
// It is the other half of reproducing an execution by hand, and it is exported
// because there is no way to derive it — the manifest lives in a scratch
// directory whose name is chosen when the session is prepared, and the
// instrumented sources it points at live nowhere else.
//
// Rebuilding one of the session's test binaries with it is:
//
//	cd <snapshot> && GOFLAGS=-overlay=<manifest> go test -c -o mutant.test ./<package>
//
// and running it is the `exec` event's own `argv`, in the `exec` event's own
// `dir`, with the mutant switched on:
//
//	cd <exec.dir> && GO_MUTANTS_ACTIVE=<mutant id> <exec.argv...>
//
// The directory is the *package's*, not the snapshot root: a Go test resolves
// testdata relative to where it runs, so the engine starts every test binary in
// the directory of the package it was built from and a reproduction started
// anywhere else is running a different program.
//
// The path is absolute, and it stays valid for as long as the session does:
// [Session.Close] removes the scratch directory it lives in unless
// [OpenOptions.KeepTemp] asked for the tree to be kept, which is the option
// this accessor is usually reached for beside.
func (s *Session) OverlayManifest() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.overlayPath
}

// ProbeOverlayManifest is the same file for the session's probe tree, and is
// empty for a session prepared without one.
//
// The two trees are separate copies compiled through separate overlays, so they
// are two accessors rather than one: a caller reproducing a probe pass by hand
// needs the probe tree's manifest and the probe tree's snapshot root, and
// reaching for the mutant tree's would compile a program with no probe in it.
//
// A probe pass activates no mutant. What it needs instead is a log to record
// into, which is [github.com/P4suta/go-mutants/internal/instrument.ProbeEnv] —
// `GO_MUTANTS_PROBE` — naming a *file path* rather than any kind of index:
//
//	cd <exec.dir> && GO_MUTANTS_PROBE=/tmp/infection.log <exec.argv...>
//
// The path must be private to the pass. Every binary of one pass appends to one
// log and the log is read once at the end, so a file two passes share is two
// measurements nothing can tell apart.
func (s *Session) ProbeOverlayManifest() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.probeOverlay
}

// keepScratch keeps one per-call scratch directory a [OpenOptions.KeepTemp]
// session is preserving, exactly as [Workspace.keepExecScratch] keeps the
// workspace's own: the artifact is recorded here, beside the execution or probe
// pass it belonged to and never again at Close, and nothing is written into the
// directory itself.
func (s *Session) keepScratch(scratch string) {
	s.recorder.Artifact(trace.ArtifactKeptExecScratch, scratch)
	s.keepMu.Lock()
	defer s.keepMu.Unlock()
	s.keptScratch = append(s.keptScratch, scratch)
}

// keptScratchDirs is the per-call scratch a kept session left behind. They are
// reported by [Workspace.Preserved] and were recorded where they were kept.
func (s *Session) keptScratchDirs() []string {
	if s == nil {
		return nil
	}
	s.keepMu.Lock()
	defer s.keepMu.Unlock()
	return slices.Clone(s.keptScratch)
}

// A sessionTarget is everything [Session.Exec] and [Session.Control] settle
// before a test binary is started: which binaries, which of them the request
// selected, the arguments and environment they run with, the budget they run
// under, and the private scratch directory all of it lives in.
//
// It is one value built in one place rather than two copies of a procedure,
// because the whole worth of a control depends on the two agreeing. A control
// is evidence about an execution only if the same binaries ran the same target
// under the same budget in the same directory with the same overlay — so a rule
// that reached one and not the other, a newly reserved flag, a change to how a
// fuzz target is isolated, a different default timeout, would quietly make the
// control a measurement of something else and nothing would say so.
type sessionTarget struct {
	options  execute.Options
	binaries []execute.TestBinary
	indexes  []int
	args     []string
	timeout  time.Duration
	memory   int64
	scratch  string
	// artifactRoot is the private copy of the snapshot a fuzz target runs in,
	// and is empty for every other target. Only [Session.Exec] captures what a
	// fuzz run leaves there; see [Session.Control] for why a control does not.
	artifactRoot string
}

// target settles one, or refuses the request.
//
// call is "exec" or "control" and names itself in every message, so a
// diagnostic says which of the session's two runs refused — one request
// vocabulary, one set of reserved flags, one message shape, as
// [sessionTargetArgs] puts it.
//
// The scratch directory is created here and removed again by every failure
// after it: a directory nothing ran in holds nothing to look at. A target that
// comes back belongs to the caller, which removes it unless the session is
// keeping temporaries.
func (s *Session) target(
	call, pkg string, args, env []string, timeout time.Duration, memory int64, recordTestLog bool,
) (sessionTarget, error) {
	indexes, err := selectTestPackages(s.root, s.binaries, pkg, call)
	if err != nil {
		return sessionTarget{}, err
	}
	composed, err := overlayEnvironment(s.env, env)
	if err != nil {
		return sessionTarget{}, fmt.Errorf("gomutants: session %s environment: %w", call, err)
	}
	composed, err = instrumentationEnvironment(composed, s.overlayPath)
	if err != nil {
		return sessionTarget{}, fmt.Errorf("gomutants: session %s overlay: %w", call, err)
	}
	if timeout == 0 {
		timeout = s.mutantTimeout
	}
	if memory == 0 {
		memory = s.mutantMemory
	}
	scratch, err := os.MkdirTemp(s.scratch, execPrefix)
	if err != nil {
		return sessionTarget{}, fmt.Errorf("gomutants: session %s scratch: %w", call, err)
	}
	settled := false
	defer func() {
		if !settled {
			_ = os.RemoveAll(scratch)
		}
	}()
	targetArgs, err := sessionTargetArgs(args, scratch, call, recordTestLog)
	if err != nil {
		return sessionTarget{}, err
	}

	options := s.executeOptions
	options.ScratchDir = scratch
	options.Env = composed
	settledTarget := sessionTarget{
		options:  options,
		binaries: s.binaries,
		indexes:  indexes,
		args:     targetArgs,
		timeout:  timeout,
		memory:   memory,
		scratch:  scratch,
	}
	if hasFuzzTarget(args) {
		settledTarget.artifactRoot = filepath.Join(scratch, "fuzz-workspace")
		settledTarget.binaries, err = prepareFuzzWorkspace(s.root, settledTarget.artifactRoot, s.binaries)
		if err != nil {
			return sessionTarget{}, fmt.Errorf("gomutants: session %s fuzz workspace: %w", call, err)
		}
	}
	settled = true
	return settledTarget, nil
}

// Exec runs one mutant against a selected test or fuzz target without
// rebuilding the prepared test binaries.
func (s *Session) Exec(ctx context.Context, request ExecRequest) (MutantResult, error) {
	if s == nil {
		return MutantResult{}, errors.New("gomutants: session exec: nil session")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return MutantResult{}, fmt.Errorf("gomutants: session exec: %w", ErrSessionClosed)
	}
	if request.Timeout < 0 {
		return MutantResult{}, errors.New("gomutants: session exec: timeout is negative")
	}
	if request.MemoryLimit < 0 {
		return MutantResult{}, errors.New("gomutants: session exec: memory limit is negative")
	}
	mutant, err := s.catalog.ResolvePrefix(request.Mutant)
	if err != nil {
		return MutantResult{}, s.selectionError(request.Mutant, err)
	}
	if !s.accepted[mutant.ID] {
		return MutantResult{}, rejectionError(request.Mutant, mutant.DisplayID, s.rejections[mutant.ID])
	}
	target, err := s.target("exec", request.Package, request.Args, request.Env,
		request.Timeout, request.MemoryLimit, request.RecordTestLog)
	if err != nil {
		return MutantResult{}, err
	}
	// Kept only once the execution has actually happened, and removed on every
	// path that never reached one: a directory nothing ran in holds nothing to
	// look at, and keeping it would put a path in Preserved that answers no
	// question.
	kept := false
	defer func() {
		if !kept {
			_ = os.RemoveAll(target.scratch)
		}
	}()
	run := execute.MutantRun{
		ID:            mutant.ID,
		DisplayID:     mutant.DisplayID,
		Package:       s.packageOf(mutant),
		Timeout:       target.timeout,
		MemoryLimit:   target.memory,
		Binaries:      target.indexes,
		Args:          target.args,
		OutputLimit:   request.OutputLimit,
		RecordTestLog: request.RecordTestLog,
	}
	attempt := execute.RunOne(ctx, target.options, run, target.binaries)
	// One attempt, on the caller's goroutine, with nothing else of this
	// session's in flight that it shares a worker with: attempt 1 and worker 0
	// are the facts rather than placeholders. The summary is built by
	// internal/execute so that an attempt recorded through this API and one
	// recorded by a run describe themselves the same way, field for field.
	traceSeq := s.recorder.MutantExec(execute.AttemptRecord(run, attempt, 1))
	if s.keepTemp {
		s.keepScratch(target.scratch)
		kept = true
	}
	artifacts, artifactErr := captureFuzzArtifacts(target.artifactRoot)
	// The capture is handed over rather than copied. internal/execute already
	// cloned it out of the runner's buffer, and the attempt is a local value
	// nothing else can reach, so a second copy of up to the whole output limit
	// would buy nothing. [Session.Probe] does the same with its own attempt.
	result := MutantResult{
		ID:         mutant.ID,
		DisplayID:  mutant.DisplayID,
		Outcome:    Outcome(attempt.Outcome.String()),
		KilledBy:   attempt.KilledBy,
		Duration:   attempt.Duration,
		OutputTail: attempt.OutputTail,
		Output:     attempt.Output,
		Truncated:  attempt.Truncated,
		TotalBytes: attempt.OutputBytes,
		// Carried up unchanged from internal/execute, which took the maximum
		// over the binaries the call started.
		PeakMemory:     attempt.PeakMemory,
		MemoryExceeded: attempt.MemoryExceeded,
		Artifacts:      artifacts,
		Binaries:       attempt.Binaries,
		TraceSeq:       traceSeq,
		TestLogs:       publicTestLogs(attempt.TestLogs),
	}
	if artifactErr != nil {
		return result, fmt.Errorf("gomutants: session exec artifacts: %w", artifactErr)
	}
	if attempt.Err != nil {
		return result, executionError("exec", request.Package,
			fmt.Errorf("gomutants: session exec: %w", attempt.Err))
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("gomutants: session exec: %w", err)
	}
	return result, nil
}

// packageOf is the import path of the package a catalogued mutant sits in, for
// the recording of an execution.
//
// It comes from the public catalogue because that is where discovery's answer
// is kept: the internal mutant carries a module-relative *path*, and a path is
// not an import path. The lookup is by dense index, which is the order
// [makeCatalog] built the public catalogue in, and it verifies the identity it
// landed on rather than trusting that order — a mismatch answers "no package"
// instead of naming somebody else's.
func (s *Session) packageOf(m mutation.Mutant) string {
	if int(m.Index) >= len(s.publicCatalog.Mutants) {
		return ""
	}
	public := s.publicCatalog.Mutants[m.Index]
	if public.ID != m.ID {
		return ""
	}
	return public.Package
}

// Control runs one test or fuzz target against the session's prepared binaries
// with no mutant activated, which runs the program the user wrote.
//
// The mutant tree's binaries are that program plus a switch. Instrumentation
// leaves every original branch in place and selects between them on one
// environment variable, and [Session.Exec] is the only thing that ever sets it —
// the execution layer strips every GO_MUTANTS_ variable out of the frozen
// environment before it composes a child's, so an activation exported in a
// developer's shell cannot reach one either. With nothing switched on, those
// binaries therefore *are* the original program, and this call is an execution
// minus that one entry.
//
// What it is for is the run beside a mutant execution. A mutant's suite going
// red is evidence about the mutant only if the same suite is green without it,
// and a prepared session could not say so. goatest reaches that answer two ways
// today and this replaces both: it opens a *second workspace* over the same root
// and runs [Workspace.Exec] there — a second snapshot, a second discovery pass
// and a second compile of everything, to run tests this session had already
// compiled — and, where a probe tree exists, it reads [Session.Probe]'s
// [ProbeTestFailed] as "the original program is red", which spends a probe
// pass, needs [PrepareOptions.Probe], measures the *probe* tree's binaries
// rather than the mutant tree's, and answers with an outcome vocabulary built
// for infection facts rather than with the suite's own exit status and output.
//
// **The guarantee is the same binaries, the same launch shape, no activation.**
// A control and an execution of the same package and the same arguments differ
// in exactly one thing: the argument vector, the working directory, the paired
// timeouts, the instrumentation overlay and the composed environment are
// settled by one unexported function for both, and the environment the
// control's child sees is the execution's minus the activation variable.
// The `exec` events of the two calls say so in a recording, and the assertion
// that they do is a test rather than a comment.
//
// The run stops at the first binary that does not exit zero, and here that is
// the answer rather than an optimisation: a control asks whether the original
// program passes these tests, and the first binary that says no has answered.
// A failing control is a finding about the *repository* — the suite is red, or
// flaky, or depends on something the frozen snapshot does not carry — and it
// comes back as a result with the output rather than as an error, because a
// consumer has to be able to report it as the user's own failure.
//
// Timeouts are the pair [Session.Exec] uses and are documented under *Paired
// timeouts* in docs/library.md: the supervisor kills the whole process tree at
// Timeout — the request's when positive, otherwise
// [PrepareOptions.MutantTimeout] — and the binary is additionally given
// `-test.timeout` at twice that, so the two never race. A tree the supervisor
// killed has no exit status, so [ControlResult.TimedOut] is the fact and
// [ControlResult.ExitCode] stays zero beside it.
//
// Errors are [Session.Exec]'s, with `Call` reading "control": a
// [*PackageNotPreparedError] for a package this session built no binary for, a
// [*ReservedError] for a flag or variable the engine owns, [ErrSessionClosed]
// after a close, and an [*ExecutionError] when the measurement itself could not
// be made — a binary that would not start, or a child a cancellation killed.
//
// A cancellation is never an exit status. A child go-mutants killed comes back
// with none, and reading that as an ordinary non-zero one would report the
// original program as failing whenever somebody stopped the run — so a control
// whose child was cut off returns an [*ExecutionError] carrying
// [ControlResult.Binaries], [ControlResult.ExecSeqs] and
// [ControlResult.TraceSeq] and no verdict at all. A context that was cancelled
// *after* the run had already finished is the other case and is reported the
// way [Session.Exec] reports it: the populated result comes back beside a plain
// wrapped context error, because the run did establish what it says it did.
//
// A fuzz target runs in a private copy of the snapshot, exactly as it does
// under [Session.Exec], so a corpus entry it writes cannot drift the tree every
// later mutant is measured against. Unlike an execution, the corpus is *not*
// captured: [MutantResult.Artifacts] exists to preserve the input that killed a
// mutant, and a control kills nothing. The copy is removed with the rest of the
// call's scratch unless [OpenOptions.KeepTemp] asked for it, in which case it is
// preserved and recorded as `kept-exec-scratch` — the same kind an execution's
// is, because it is the same kind of directory.
//
// Each call gets its own scratch directory, and Control is safe to call
// concurrently with itself, with [Session.Exec] and with [Session.Probe]; they
// share the session and nothing else.
func (s *Session) Control(ctx context.Context, request ControlRequest) (ControlResult, error) {
	if s == nil {
		return ControlResult{}, errors.New("gomutants: session control: nil session")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ControlResult{}, fmt.Errorf("gomutants: session control: %w", ErrSessionClosed)
	}
	if request.Timeout < 0 {
		return ControlResult{}, errors.New("gomutants: session control: timeout is negative")
	}
	if request.MemoryLimit < 0 {
		return ControlResult{}, errors.New("gomutants: session control: memory limit is negative")
	}
	target, err := s.target("control", request.Package, request.Args, request.Env,
		request.Timeout, request.MemoryLimit, request.RecordTestLog)
	if err != nil {
		return ControlResult{}, err
	}
	kept := false
	defer func() {
		if !kept {
			_ = os.RemoveAll(target.scratch)
		}
	}()

	run := execute.ControlRun{
		Timeout:       target.timeout,
		MemoryLimit:   target.memory,
		Binaries:      target.indexes,
		Args:          target.args,
		OutputLimit:   request.OutputLimit,
		RecordTestLog: request.RecordTestLog,
	}
	attempt := execute.RunControl(ctx, target.options, run, target.binaries)
	// Recorded before the failures below become errors, so that a control that
	// could not be made is in the account of the session rather than only in
	// the error one caller received. The summary is built by internal/execute,
	// where the facts about the run are, and it is a note because the trace
	// contract has no payload for a control: the `control-run` executions
	// underneath it are the account, and this is the line that names them.
	record := execute.ControlRecord(run, attempt)
	traceSeq := s.recorder.Note(record.Kind, record.Code, record.Detail)
	if s.keepTemp {
		s.keepScratch(target.scratch)
		kept = true
	}
	// A control that reached an execution and then failed carries its sequence
	// and its binaries beside the error and nothing else, exactly as a probe
	// pass does: it is in the recording whatever became of it, and a failure
	// that handed back a zero sequence would be the one case a consumer most
	// wants to read about and the one it could not find. The status, the
	// timeout flag and the capture stay at their zero values, because the run
	// established none of them.
	if attempt.Err != nil {
		return ControlResult{
				Binaries: attempt.Binaries,
				ExecSeqs: attempt.ExecSeqs,
				TraceSeq: traceSeq,
				TestLogs: publicTestLogs(attempt.TestLogs),
			},
			executionError("control", request.Package,
				fmt.Errorf("gomutants: session control: %w", attempt.Err))
	}
	// The capture is handed over rather than copied, as [Session.Exec] and
	// [Session.Probe] hand over their own: internal/execute already cloned it
	// out of the runner's buffer, and this attempt is a local value nothing
	// else can reach.
	result := ControlResult{
		Package:    attempt.Package,
		ExitCode:   attempt.ExitCode,
		TimedOut:   attempt.TimedOut,
		Duration:   attempt.Duration,
		Output:     attempt.Output,
		Truncated:  attempt.Truncated,
		TotalBytes: attempt.OutputBytes,
		// Carried up unchanged from internal/execute, which took the maximum
		// over the binaries the call started.
		PeakMemory:     attempt.PeakMemory,
		MemoryExceeded: attempt.MemoryExceeded,
		Binaries:       attempt.Binaries,
		ExecSeqs:       attempt.ExecSeqs,
		TraceSeq:       traceSeq,
		TestLogs:       publicTestLogs(attempt.TestLogs),
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("gomutants: session control: %w", err)
	}
	return result, nil
}

// Probe runs one test or fuzz target against the session's probe tree and
// reports which catalogued mutants that target could have observed.
//
// The probe tree is the same source as the mutant tree with no mutant ever
// active: the program the user wrote runs, and each site go-mutants has a probe
// form for records — without side effects — whether the mutated value would
// have differed from the one the original produced. So a mutant that is
// [Mutant.Probed] and absent from [ProbeResult.Infected] is one this target
// never saw a differing value at, and a target that cannot distinguish a mutant
// from the original program cannot kill it. That is the licence: the caller may
// skip executing that (mutant, target) pair, and the result it would have got
// is "survived".
//
// The licence is only ever given by a [ProbeMeasured] outcome, and only for a
// probed mutant. Every other outcome, and every error, means the pass has no
// facts — not that nothing was infected. **A caller that reads an error, a
// failed target, a timeout or an unavailable runtime as "not infected" is
// unsound**: it will drop executions that would have found kills, and the
// mutation score it reports will be higher than the truth with nothing in the
// output saying so. An unprobed mutant is the same trap in a different shape.
// It is absent from every measurement there will ever be, because nothing was
// compiled that could record it, and a caller has to treat it as infected by
// every target.
//
// An error is returned when the session was prepared without a probe tree
// ([ErrProbeNotPrepared]), when the request names a package with no prepared
// test binary or is otherwise malformed — the same checks [Session.Exec]
// makes — and when the infrastructure fails: a scratch directory that cannot be
// made, a process that will not start, or an infection log that is there and
// cannot be read. A *missing* log after a clean exit is not a failure but the
// empty set: the probe runtime writes its header in `init`, before any test
// code runs, so a binary that wrote no log linked no probe and ran no probed
// site.
//
// One error is not about the request or the machine at all.
// [ErrProbeInconsistent] is returned when the log names an index this session's
// catalogue cannot account for. The set is examined in two stages, with the
// filter that drops rejected mutants between them:
//
//   - The raw log must be strictly ascending and inside the catalogue. That is
//     checked first, before anything is dropped, because the filter has to
//     tolerate an out-of-range index in order not to panic on one — and an
//     index past the end of the catalogue is the runtime writing about a
//     catalogue that is not this one, which must not be mistaken for an
//     ordinary rejection.
//   - The indices of mutants the *mutant tree's* validation rejected are then
//     dropped, not refused. That is the one surprising index which is no bug at
//     all: the probe tree is instrumented from the whole catalogue, so its log
//     legitimately names a site whose mutation did not compile. Such an index
//     never produces this error, however well formed it is.
//   - Every index that survives the filter must name a mutant [Mutant.Probed]
//     reports as probed. An *accepted* mutant that is not probed is the
//     catalogue and the probe tree disagreeing about a mutant neither has an
//     excuse for.
//
// Nothing a caller does can cause any of that: the indices are go-mutants' own,
// written against the catalogue go-mutants prepared, so it is the engine
// contradicting itself and the answer is a bug report. It is an error rather
// than a repaired set because a repaired set would be handed over as a
// measurement, and a measurement is a licence to skip executions.
//
// Each call gets its own scratch directory and its own log, which is what makes
// the answer a statement about this target and this call. Probe is safe to call
// concurrently with itself and with [Session.Exec]; the two share the session
// and nothing else.
func (s *Session) Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	if s == nil {
		return ProbeResult{}, errors.New("gomutants: session probe: nil session")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ProbeResult{}, fmt.Errorf("gomutants: session probe: %w", ErrSessionClosed)
	}
	if s.probeSnapshot == nil {
		return ProbeResult{}, fmt.Errorf("gomutants: session probe: %w", ErrProbeNotPrepared)
	}
	if request.Timeout < 0 {
		return ProbeResult{}, errors.New("gomutants: session probe: timeout is negative")
	}
	if request.MemoryLimit < 0 {
		return ProbeResult{}, errors.New("gomutants: session probe: memory limit is negative")
	}
	binaryIndexes, err := selectTestPackages(s.probeSnapshot.Root, s.probeBinaries, request.Package, "probe")
	if err != nil {
		return ProbeResult{}, err
	}
	env, err := overlayEnvironment(s.env, request.Env)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("gomutants: session probe environment: %w", err)
	}
	env, err = instrumentationEnvironment(env, s.probeOverlay)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("gomutants: session probe overlay: %w", err)
	}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = s.mutantTimeout
	}
	memory := request.MemoryLimit
	if memory == 0 {
		memory = s.mutantMemory
	}
	scratch, err := os.MkdirTemp(s.scratch, probePrefix)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("gomutants: session probe scratch: %w", err)
	}
	// The log lives and dies with the call. Every pass gets a directory of its
	// own, so the file a kept one leaves behind is nobody else's to append to:
	// what must never happen is two passes sharing one log, and two passes
	// never share a directory. Without a keep it goes, because a pass a caller
	// did not ask to preserve is a directory nothing will ever read.
	kept := false
	defer func() {
		if !kept {
			_ = os.RemoveAll(scratch)
		}
	}()
	targetArgs, err := sessionTargetArgs(request.Args, scratch, "probe", request.RecordTestLog)
	if err != nil {
		return ProbeResult{}, err
	}

	opts := s.probeOptions
	opts.ScratchDir = scratch
	opts.Env = env
	pass := execute.ProbeRun{
		Timeout:       timeout,
		MemoryLimit:   memory,
		Binaries:      binaryIndexes,
		Args:          targetArgs,
		OutputLimit:   request.OutputLimit,
		RecordTestLog: request.RecordTestLog,
		LogPath:       filepath.Join(scratch, infectionLogName),
		Digest:        s.catalog.Digest(),
		Mutants:       s.catalog.Len(),
	}
	attempt := execute.RunProbe(ctx, opts, pass, s.probeBinaries)
	// Recorded before the failures below are turned into errors, so that a pass
	// that could not be made is in the account of the session rather than only
	// in the error one caller received. The infection set is named by mutant
	// identity rather than by the catalogue index the runtime wrote, because an
	// index means nothing outside this session and an identity is what the
	// report, the cache and a consumer's own recording all key on.
	traceSeq := s.recorder.ProbeExec(s.probePassRecord(pass, attempt, binaryIndexes))
	if s.keepTemp {
		s.keepScratch(scratch)
		kept = true
	}
	// Everything below returns this rather than a zero value. A pass that
	// reached an execution is in the recording whatever became of it, and
	// TraceSeq is documented as the event that explains the result — so a
	// failure that handed back a zero sequence would be the one case a consumer
	// most wants to read about and the one case it cannot find. What it carries
	// is what the pass established and nothing it did not: the binaries it
	// started and the account of them, never an outcome or an infection set.
	partial := ProbeResult{
		Binaries: attempt.Binaries,
		TraceSeq: traceSeq,
		TestLogs: publicTestLogs(attempt.TestLogs),
	}
	if attempt.Err != nil {
		return partial, executionError("probe", request.Package,
			fmt.Errorf("gomutants: session probe: %w", attempt.Err))
	}
	if err := ctx.Err(); err != nil {
		return partial, fmt.Errorf("gomutants: session probe: %w", err)
	}
	// The set a caller receives is one it may index the catalogue with directly,
	// and that promise is kept here rather than left to the runtime that wrote
	// the log. Anything either check refuses is a go-mutants bug, so it surfaces
	// as an error: dropping the index would hand over a repaired set as a
	// measurement, and reporting no facts would say the pass could not be
	// vouched for, which is a different and equally untrue sentence.
	//
	// The shape is proved over the raw log, before filtering, because the filter
	// drops an index outside the catalogue on its way to dropping the rejected
	// ones — and those two are not the same thing at all.
	if err := checkInfectedShape(attempt.Infected, len(s.publicCatalog.Mutants)); err != nil {
		return partial, err
	}
	infected := filterInfected(attempt.Infected, s.publicCatalog.Mutants)
	if err := checkInfectedProbed(infected, s.publicCatalog.Mutants); err != nil {
		return partial, err
	}
	// The capture is handed over rather than copied, as [Session.Exec] hands
	// over its own: internal/execute already cloned it out of the runner's
	// buffer, and this attempt is a local value nothing else can reach.
	return ProbeResult{
		Outcome:    ProbeOutcome(attempt.Outcome),
		Infected:   infected,
		ExitCode:   attempt.ExitCode,
		Duration:   attempt.Duration,
		Output:     attempt.Output,
		Truncated:  attempt.Truncated,
		TotalBytes: attempt.OutputBytes,
		// Carried up unchanged from internal/execute, which took the maximum
		// over the binaries the call started.
		PeakMemory:     attempt.PeakMemory,
		MemoryExceeded: attempt.MemoryExceeded,
		Binaries:       attempt.Binaries,
		TraceSeq:       traceSeq,
		TestLogs:       publicTestLogs(attempt.TestLogs),
	}, nil
}

// publicTestLogs is the execution layer's record of what each binary consulted,
// as the public value.
//
// It is a copy rather than a re-typing, because the two vocabularies are
// separate on purpose: the public [TestLogOp] is what a consumer serialises and
// switches on, and the internal one is what internal/testlog parses. Nil stays
// nil, which is the whole of the field's contract — nil is "nothing was
// recorded" and an empty slice would be "these binaries touched nothing".
func publicTestLogs(logs []execute.TestLog) []TestLog {
	if logs == nil {
		return nil
	}
	out := make([]TestLog, len(logs))
	for i, log := range logs {
		out[i] = TestLog{
			Package:  log.Package,
			Dir:      log.Dir,
			Complete: log.Complete,
			Err:      log.Err,
		}
		if log.Entries == nil {
			continue
		}
		entries := make([]TestLogEntry, len(log.Entries))
		for j, entry := range log.Entries {
			entries[j] = TestLogEntry{Op: TestLogOp(entry.Op), Name: entry.Name}
		}
		out[i].Entries = entries
	}
	return out
}

// probePassRecord is one pass as the recording holds it: what internal/execute
// knows about it, plus the two things only this session can say.
//
// The package is named exactly when the pass *selected* one binary, which is the
// rule the `probe-run` executions underneath it are stamped with — a pass over
// several packages is a measurement of no single one, and picking a member of
// the set would be worse than naming none. The selected set and not the started
// one: a pass that stopped at its first binary of three was still a measurement
// of three, and a session with exactly one prepared binary names it whether or
// not the request narrowed anything.
//
// The infection set is the raw log's, mapped to identities and neither filtered
// nor checked. Filtering happens afterwards and for a reason about the
// *contract* — an index naming a mutant nothing will execute licenses no
// skipping — while a recording is the account of what the pass recorded, and an
// index dropped before it reached the recording would be a fact removed from
// the one place a reader goes to find out why a pass said what it did. An index
// outside this catalogue is left out rather than made up: the check that
// refuses one runs next, and a recording is not the place to panic.
func (s *Session) probePassRecord(
	pass execute.ProbeRun, attempt execute.ProbeAttempt, binaryIndexes []int,
) trace.ProbeRecord {
	record := execute.ProbePassRecord(pass, attempt)
	switch {
	case binaryIndexes == nil && len(s.probeBinaries) == 1:
		record.Package = s.probeBinaries[0].ImportPath
	case len(binaryIndexes) == 1:
		record.Package = s.probeBinaries[binaryIndexes[0]].ImportPath
	}
	if attempt.Infected != nil {
		infected := make([]string, 0, len(attempt.Infected))
		for _, index := range attempt.Infected {
			if uint64(index) < uint64(len(s.publicCatalog.Mutants)) {
				infected = append(infected, s.publicCatalog.Mutants[index].ID)
			}
		}
		record.Infected = infected
	}
	return record
}

// filterInfected drops the indices of mutants the mutant tree's validation
// rejected.
//
// The probe tree is instrumented from the *whole* catalogue, and its validation
// is an independent pass over a different tree: the probe rewrite at a site is
// a different edit from the mutation there, so a site whose probe compiles
// while its mutation does not is instrumented, is reached, and records. The log
// can therefore name a mutant [Session.Exec] would refuse to run.
//
// Two contract claims meet here, and the catalogue alone can keep only one of
// them. [Mutant.Probed] is false for such a mutant, because a probe status on
// something nothing will execute is a statement about a run that cannot happen
// — while the consumer's rule reads an index in [ProbeResult.Infected] as a
// mutant it may look up and act on. Left in, the index would contradict the
// field that is supposed to explain it. Filtering is the reconciliation, and it
// happens here rather than by instrumenting the probe tree differently because
// that tree is built alongside validation rather than after it: making its
// contents depend on the mutant tree's compiler would serialise two phases that
// currently run at once, to drop indices a slice skip drops.
//
// Order is preserved, so an ascending set stays ascending. Nil stays nil: nil
// is "no facts" and the empty set is "nothing was infected", and those two must
// not become each other here of all places.
func filterInfected(indices []uint32, mutants []Mutant) []uint32 {
	if indices == nil {
		return nil
	}
	kept := make([]uint32, 0, len(indices))
	for _, index := range indices {
		// The bounds check cannot fire today — instrument.ReadInfectionLog
		// refuses a log naming an index outside the catalogue it was written
		// for — and it is here so this function cannot panic on one.
		if uint64(index) < uint64(len(mutants)) && mutants[index].Accepted {
			kept = append(kept, index)
		}
	}
	return kept
}

func hasFuzzTarget(arguments []string) bool {
	return slices.ContainsFunc(arguments, func(argument string) bool {
		return testflag.Match(argument, "test.fuzz")
	})
}

func prepareFuzzWorkspace(root, destination string, binaries []execute.TestBinary) ([]execute.TestBinary, error) {
	if err := copyTree(root, destination); err != nil {
		return nil, err
	}
	cloned := slices.Clone(binaries)
	for i := range cloned {
		relative, err := snapshotRelativePath(root, cloned[i].Dir)
		if err != nil {
			return nil, fmt.Errorf("test package %q is outside the snapshot", cloned[i].ImportPath)
		}
		cloned[i].Dir = filepath.Join(destination, relative)
	}
	return cloned, nil
}

func snapshotRelativePath(root, path string) (string, error) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("path is outside the snapshot")
	}
	return relative, nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if entry.Type()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refuses non-regular fuzz-workspace entry %s", relative)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeOutErr := output.Close()
		closeInErr := input.Close()
		return errors.Join(copyErr, closeOutErr, closeInErr)
	})
}

func captureFuzzArtifacts(root string) ([]Artifact, error) {
	if root == "" {
		return nil, nil
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	artifacts := make([]Artifact, 0)
	total := int64(0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refuses symbolic link %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuses irregular artifact %s", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		header := make([]byte, len("go test fuzz v1\n"))
		_, headerErr := io.ReadFull(file, header)
		_ = file.Close()
		if headerErr != nil || string(header) != "go test fuzz v1\n" {
			return nil
		}
		if info.Size() > maximumArtifactBytes || total+info.Size() > maximumArtifactsSize || len(artifacts) >= maximumArtifacts {
			return fmt.Errorf("fuzz artifacts exceed the session capture limit")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		artifacts = append(artifacts, Artifact{
			Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(sum[:]), Data: data,
		})
		total += info.Size()
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(artifacts, func(a, b Artifact) int { return strings.Compare(a.Path, b.Path) })
	return artifacts, nil
}

// sessionTargetArgs supplies the one flag cmd/go normally adds around a fuzz
// target. A test binary refuses -test.fuzz without -test.fuzzcachedir; keeping
// that cache in the execution scratch preserves the read-only snapshot and
// lets the standard seed corpus compiled from testdata/fuzz run unchanged.
//
// The three flags it refuses are the ones the session owns. Two belong to the
// fuzz machinery above; the third is `-test.timeout`, which the session sets at
// twice the supervisor's budget so that the in-process deadline can never fire
// first. A target supplying its own would switch that insurance off while the
// API still claimed the budget it was given, so it is refused here — before a
// scratch directory is made or a binary is started — rather than four layers
// down by the execution phase, where the same refusal had a code and a sentence
// about a process supervisor the caller never asked for.
//
// A fourth is refused *conditionally*, and it is the only one that is.
// `-test.testlogfile` belongs to the session exactly while a request asked it
// to record one, because two of them are not two logs: the standard flag
// package keeps the last value it sees, so one of the two would silently win
// and the other would report on a file nobody wrote. A request that did not ask
// is not composing anything, so the flag passes through verbatim — that is the
// method a consumer smuggling its own has been using, and it goes on working.
//
// The call names itself so that a diagnostic says which of the session's three
// runs refused the target. All three go through this function because a caller
// composing arguments for one has to be able to hand them to the others — a
// mutant execution and the control beside it are the same target twice — so:
// one request vocabulary, one set of reserved flags, one message shape.
func sessionTargetArgs(args []string, scratch, call string, recordTestLog bool) ([]string, error) {
	out := slices.Clone(args)
	fuzz := false
	for _, argument := range args {
		switch {
		case testflag.Match(argument, "test.fuzz"):
			fuzz = true
		case testflag.Match(argument, "test.fuzzcachedir"):
			return nil, &ReservedError{Call: call, Flag: "-test.fuzzcachedir", Owner: "the session"}
		case testflag.Match(argument, "test.fuzzworker"):
			return nil, &ReservedError{Call: call, Flag: "-test.fuzzworker", Owner: "the Go fuzz coordinator"}
		case testflag.Match(argument, "test.timeout"):
			return nil, &ReservedError{Call: call, Flag: "-test.timeout", Owner: "the session's process supervisor"}
		case recordTestLog && testflag.Match(argument, "test.testlogfile"):
			return nil, &ReservedError{
				Call: call, Flag: "-test.testlogfile", Owner: "the request's test log recording",
			}
		}
	}
	if !fuzz {
		return out, nil
	}
	cache := filepath.Join(scratch, "fuzz-cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, fmt.Errorf("gomutants: session %s fuzz cache: %w", call, err)
	}
	return append(out, "-test.fuzzcachedir="+cache), nil
}

func selectTestPackages(root string, binaries []execute.TestBinary, selected, call string) ([]int, error) {
	if selected == "" {
		return nil, nil
	}
	var selectedDir string
	if selected == "." || strings.HasPrefix(selected, "./") {
		var err error
		selectedDir, err = moduleDirectory(root, selected)
		if err != nil {
			return nil, fmt.Errorf("gomutants: session %s package: %w", call, err)
		}
	}
	var indexes []int
	for i, binary := range binaries {
		if binary.ImportPath == selected || selectedDir != "" && samePath(binary.Dir, selectedDir) {
			indexes = append(indexes, i)
		}
	}
	if len(indexes) == 0 {
		return nil, &PackageNotPreparedError{Call: call, Package: selected}
	}
	return indexes, nil
}

func samePath(a, b string) bool {
	// macOS exposes temporary directories through aliases such as /var and
	// /private/var. go list may report the canonical spelling while the
	// workspace retains the spelling returned by os.MkdirTemp, so compare the
	// filesystem-resolved paths when both still exist.
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Close waits for target executions and releases the session's binaries, its
// scratch files, and the probe tree when it has one. It is idempotent. Closing
// a Session does not close its parent Workspace; closing the Workspace closes
// both.
//
// The probe tree goes here rather than with the Workspace's own snapshot
// because the session is what made it: a second copy of the module, built for
// this session's catalogue, useless to anything that outlives it.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true

	var closeErr error
	if s.scratch != "" && !s.keepTemp {
		closeErr = os.RemoveAll(s.scratch)
	}
	if s.probeSnapshot != nil {
		kept, err := keepOrRemove(s.keepTemp, s.probeSnapshot.Keep, s.probeSnapshot.Cleanup)
		closeErr = errors.Join(closeErr, err)
		if kept {
			s.keepMu.Lock()
			s.preserved = append(s.preserved,
				keptDirectory{kind: trace.ArtifactKeptProbeTree, path: s.probeSnapshot.Dir()})
			s.keepMu.Unlock()
		}
	}
	if closeErr != nil {
		return fmt.Errorf("gomutants: close session: %w", closeErr)
	}
	return nil
}

// preservedDirs names the durable directories this session left on disk, which
// is the probe tree of a kept session and nothing else. The per-call scratch is
// [Session.keptScratchDirs], recorded where it was kept; the session's own
// scratch is neither, because it lives inside the workspace's, which the
// Workspace settles on its own.
//
// It is unexported because [Workspace.Preserved] is the one place a caller asks
// this question: a session whose directories were kept is always closed by, or
// before, the workspace that owns it. The artifact kind travels with each path
// so that the workspace's recording can say what each one was.
func (s *Session) preservedDirs() []keptDirectory {
	if s == nil {
		return nil
	}
	s.keepMu.Lock()
	defer s.keepMu.Unlock()
	return slices.Clone(s.preserved)
}

func makeCatalog(
	workspaceDigest string,
	toolchain gocmd.Toolchain,
	profile string,
	found discover.Result,
	catalog *mutation.Catalog,
	rejected []validate.Rejection,
	accepted map[string]bool,
	probed map[string]bool,
	binaries []execute.TestBinary,
	selection *Selection,
) (Catalog, map[string]Rejection) {
	type locationKey struct {
		path string
		span mutation.Span
		rule string
	}
	locations := make(map[locationKey]discover.Located, len(found.Candidates))
	for _, candidate := range found.Candidates {
		key := locationKey{candidate.Path, candidate.Span, candidate.Rule.Name}
		if _, exists := locations[key]; !exists {
			locations[key] = candidate
		}
	}
	mutants := make([]Mutant, 0, catalog.Len())
	byID := make(map[string]Mutant, catalog.Len())
	for _, internal := range catalog.Mutants() {
		where := locations[locationKey{internal.Path, internal.Span, internal.Rule.Name}]
		public := Mutant{
			Index:        internal.Index,
			ID:           internal.ID,
			DisplayID:    internal.DisplayID,
			Path:         internal.Path,
			Package:      where.Package,
			Line:         where.Line,
			Column:       where.Column,
			EndLine:      endLine(where.Line, internal.Original),
			StartByte:    internal.Span.StartByte,
			EndByte:      internal.Span.EndByte,
			Family:       string(internal.Rule.Family),
			Rule:         internal.Rule.Name,
			RuleVersion:  internal.Rule.Version,
			SourceDigest: internal.SourceDigest,
			Original:     internal.Original,
			Replacement:  internal.Replacement,
			Accepted:     accepted[internal.ID],
			Branch:       publicBranch(where.Branch),
			// The conjunction, not the probe tree's answer alone. The two
			// validations are independent passes over two trees, and the probe
			// rewrite at a site is a different edit from the mutation there, so
			// a site whose probe compiles while its mutant does not is an
			// ordinary outcome rather than a contradiction. But a rejected
			// mutant is never executed: "the probe tree speaks for it" would be
			// a statement about a run that cannot happen, and a consumer reads
			// Probed as a fact about the executions it may skip.
			Probed: probed[internal.ID] && accepted[internal.ID],
		}
		mutants = append(mutants, public)
	}
	// After the whole slice is built, and after discovery and validation have
	// had their say: a selection narrows what a caller means to execute and
	// nothing else, so it runs over finished mutants rather than deciding which
	// ones exist. Everything above this line is what a preparation with no
	// selection produces, byte for byte.
	//
	// The index is filled from the finished slice rather than inside the loop
	// above, so that there is exactly one description of each mutant. Filling it
	// as each mutant was built would leave it holding values from before
	// applySelection ran, and the rejections rendered from it just below would
	// then disagree with the catalogue about Selected.
	applySelection(mutants, selection)
	for _, mutant := range mutants {
		byID[mutant.ID] = mutant
	}
	rejections := make([]Rejection, 0, len(rejected))
	rejectionIndex := make(map[string]Rejection, len(rejected))
	for _, internal := range rejected {
		mutant := byID[internal.ID]
		public := Rejection{
			ID:         internal.ID,
			DisplayID:  mutant.DisplayID,
			Path:       mutant.Path,
			Line:       mutant.Line,
			Column:     mutant.Column,
			Rule:       mutant.Rule,
			Diagnostic: internal.Diagnostic,
		}
		rejections = append(rejections, public)
		rejectionIndex[public.ID] = public
	}
	packages := make([]string, len(binaries))
	for i, binary := range binaries {
		packages[i] = binary.ImportPath
	}
	public := Catalog{
		WorkspaceDigest: workspaceDigest,
		Digest:          catalog.Digest(),
		ModulePath:      found.ModulePath,
		GoVersion:       found.GoVersion,
		Toolchain:       toolchain.Version.Raw,
		Profile:         profile,
		Mutants:         mutants,
		Rejections:      rejections,
		TestPackages:    packages,
		Selection:       cloneSelection(selection),
	}
	// Last, over the finished value: the prepared digest is a function of every
	// other field, so computing it anywhere but here would leave one of them
	// free to move afterwards without the key noticing.
	public.PreparedDigest = preparedDigest(public)
	return public, rejectionIndex
}

// publicBranch converts discovery's branch proof into the public one. Nil
// stays nil: no proof is not the same statement as no branch.
func publicBranch(proof *discover.BranchProof) *BranchProof {
	if proof == nil {
		return nil
	}
	return &BranchProof{
		Direction:       proof.Direction,
		BodyStartLine:   proof.BodyStartLine,
		BodyStartColumn: proof.BodyStartColumn,
		BodyEndLine:     proof.BodyEndLine,
		BodyEndColumn:   proof.BodyEndColumn,
	}
}

// cloneCatalog copies everything a caller could write through, the branch
// proofs included. Session.Catalog promises a deep copy, and an aliased
// pointer would leave a caller one assignment away from rewriting the
// session's own catalogue.
func cloneCatalog(c Catalog) Catalog {
	c.Mutants = slices.Clone(c.Mutants)
	for i := range c.Mutants {
		c.Mutants[i].Branch = publicBranchCopy(c.Mutants[i].Branch)
	}
	c.Rejections = slices.Clone(c.Rejections)
	c.TestPackages = slices.Clone(c.TestPackages)
	c.Selection = cloneSelection(c.Selection)
	return c
}

// publicBranchCopy duplicates a proof, keeping nil as nil.
func publicBranchCopy(proof *BranchProof) *BranchProof {
	if proof == nil {
		return nil
	}
	copied := *proof
	return &copied
}

func outputSummary(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "no output"
	}
	if line, _, ok := strings.Cut(trimmed, "\n"); ok {
		return strconv.Quote(strings.TrimSpace(line))
	}
	return strconv.Quote(trimmed)
}

func checkInitialDrift(snap *snapshot.Snapshot, instrumented instrument.Result, what string) error {
	unexpected, err := drift.UnexpectedDrifts(snap, instrumented)
	if err != nil {
		return fmt.Errorf("gomutants: prepare drift check: %w", err)
	}
	if len(unexpected) != 0 {
		return &DriftError{Stage: what, Changes: driftChanges(unexpected)}
	}
	return nil
}
