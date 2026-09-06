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
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testflag"
	"github.com/P4suta/go-mutants/internal/validate"
	"github.com/P4suta/go-mutants/trace"
)

const (
	defaultProfile       = "balanced"
	defaultMutantTimeout = 10 * time.Second
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
	privateDirectoryMode = 0o700
	privateFileMode      = 0o600
	uncachedTestFlag     = "-count=1"
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
	preparedFiles  map[string]fileState
	overlayPath    string
	closed         bool

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
	files    map[string]fileState
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
func (w *Workspace) Prepare(ctx context.Context, options PrepareOptions) (*Session, error) {
	if w == nil {
		return nil, errors.New("gomutants: prepare: nil workspace")
	}
	return w.prepare(ctx, options)
}

func (w *Workspace) prepare(ctx context.Context, options PrepareOptions) (session *Session, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Everything above the recorder's note is a refusal rather than a
	// preparation failure: a closed workspace, one already prepared, a
	// cancelled context, an option that is not a value this engine accepts.
	// None of them started a preparation, so a `prepare-failed` note about one
	// would name no phase and describe nothing that happened.
	if w.closed {
		return nil, fmt.Errorf("gomutants: prepare: %w", ErrWorkspaceClosed)
	}
	if w.prepared {
		return nil, fmt.Errorf("gomutants: prepare: %w", ErrWorkspacePrepared)
	}
	w.prepared = true
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("gomutants: prepare: %w", ctxErr)
	}
	resolved, err := resolvePrepareOptions(options)
	if err != nil {
		return nil, err
	}
	phases := newPrepareTrace(resolved.Trace, w.recorder)
	// From here on a failure is one this workspace had, and the recording says
	// which phase it was in. Deferred rather than written at each return,
	// because a preparation has a dozen of them and the one that would be
	// forgotten is the one somebody is reading the recording to find.
	defer func() {
		if err != nil {
			w.recorder.Note(trace.NotePrepareFailed, "", prepareFailedDetail(err))
		}
	}()
	if pristineErr := checkPristineSnapshot(w.snapshot); pristineErr != nil {
		return nil, pristineErr
	}
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
	err = phases.run(PreparePhaseDiscovery, func() error {
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
	var probeSnap *snapshot.Snapshot
	if resolved.Probe {
		err = phases.run(PreparePhaseProbeSnapshot, func() error {
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
		if err != nil {
			return failPrepare(probeSnap, err)
		}
	} else {
		phases.skip(PreparePhaseProbeSnapshot)
	}
	// Every return from here on goes through fail, so that a probe tree copied
	// and then abandoned does not outlive the call that made it — as Open
	// cleans up its own snapshot when the scratch directory beside it fails.
	fail := func(err error) (*Session, error) { return failPrepare(probeSnap, err) }

	var validated validate.Result
	err = phases.run(PreparePhaseMainValidation, func() error {
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
	})
	if err != nil {
		return fail(err)
	}
	var scratch string
	var overlayPath string
	err = phases.run(PreparePhaseMainRestoration, func() error {
		scratch, err = os.MkdirTemp(w.scratch, sessionPrefix)
		if err != nil {
			return fmt.Errorf("gomutants: prepare session scratch: %w", err)
		}
		overlayPath, err = writeInstrumentationOverlay(w.snapshot.Root, scratch, mainOverlayName, validated.Instrumented)
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
	if err != nil {
		return fail(err)
	}

	if !resolved.SkipVerify {
		err = phases.run(PreparePhaseVerification, func() error {
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
			verified, verifyErr := w.runCommand(ctx, verify, verifyBase, trace.ExecKindVerify)
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
			if driftErr := checkInitialDrift(w.snapshot, instrument.Result{}, "verification"); driftErr != nil {
				return driftErr
			}
			return nil
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
				var scanErr error
				result.files, scanErr = scanFiles(w.snapshot.Root)
				if scanErr != nil {
					return fmt.Errorf("gomutants: prepare snapshot state: %w", scanErr)
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
		preparedFiles:  mainBuild.files,
		overlayPath:    overlayPath,
		probeSnapshot:  probeSnap,
		probeBinaries:  probeBuild.binaries,
		probeOptions:   probeBuild.options,
		probeOverlay:   probeBuild.overlay,
		keepTemp:       w.keepTemp,
		recorder:       w.recorder,
	}
	w.session = session
	return session, nil
}

// checkPristineSnapshot is the barrier between arbitrary pre-preparation
// commands and mutation discovery. Commands may be used for build, vet and
// baseline controls, but none may silently rewrite the frozen program those
// controls are meant to justify. Prepare holds the Workspace write lock here,
// so Redigest cannot observe an Exec halfway through a write.
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

// failPrepare returns a preparation failure, removing the probe tree first when
// one had already been copied.
//
// A probe tree is a whole second copy of somebody's module, so a Prepare that
// gives up after taking one has to remove it: nothing else knows it exists —
// the Session it would have belonged to is never returned — and the Workspace's
// own Close cleans up only the snapshot it made itself.
func failPrepare(probeSnap *snapshot.Snapshot, err error) (*Session, error) {
	if probeSnap == nil {
		return nil, err
	}
	if cleanupErr := probeSnap.Cleanup(); cleanupErr != nil {
		return nil, errors.Join(err, cleanupErr)
	}
	return nil, err
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
		overlayPath, validateErr = writeInstrumentationOverlay(opts.snap.Root, opts.scratch, probeOverlayName, validated.Instrumented)
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

func writeInstrumentationOverlay(root, scratch, name string, result instrument.Result) (string, error) {
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
	replacements := make(map[string]string, len(paths))
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
		// The go command resolves the package directory it is given through the
		// file system before it looks a path up in the overlay, so on a platform
		// whose temporary directory is reached through a symbolic link — macOS
		// reaches /var/folders through /private/var — the key written from the
		// snapshot root would never be the key looked up. Both spellings name
		// the same file, so both map to the same backing copy.
		if resolved, resolveErr := filepath.EvalSymlinks(source); resolveErr == nil && resolved != source {
			replacements[resolved] = target
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
	mutant, err := s.catalog.ResolvePrefix(request.Mutant)
	if err != nil {
		return MutantResult{}, s.selectionError(request.Mutant, err)
	}
	if !s.accepted[mutant.ID] {
		return MutantResult{}, rejectionError(request.Mutant, mutant.DisplayID, s.rejections[mutant.ID])
	}
	binaryIndexes, err := selectTestPackages(s.root, s.binaries, request.Package, "exec")
	if err != nil {
		return MutantResult{}, err
	}
	env, err := overlayEnvironment(s.env, request.Env)
	if err != nil {
		return MutantResult{}, fmt.Errorf("gomutants: session exec environment: %w", err)
	}
	env, err = instrumentationEnvironment(env, s.overlayPath)
	if err != nil {
		return MutantResult{}, fmt.Errorf("gomutants: session exec overlay: %w", err)
	}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = s.mutantTimeout
	}
	scratch, err := os.MkdirTemp(s.scratch, execPrefix)
	if err != nil {
		return MutantResult{}, fmt.Errorf("gomutants: session exec scratch: %w", err)
	}
	// Kept only once the execution has actually happened, and removed on every
	// path that never reached one: a directory nothing ran in holds nothing to
	// look at, and keeping it would put a path in Preserved that answers no
	// question.
	kept := false
	defer func() {
		if !kept {
			_ = os.RemoveAll(scratch)
		}
	}()
	targetArgs, err := sessionTargetArgs(request.Args, scratch, "exec")
	if err != nil {
		return MutantResult{}, err
	}

	opts := s.executeOptions
	opts.ScratchDir = scratch
	opts.Env = env
	runBinaries := s.binaries
	artifactRoot := ""
	if hasFuzzTarget(request.Args) {
		artifactRoot = filepath.Join(scratch, "fuzz-workspace")
		runBinaries, err = prepareFuzzWorkspace(s.root, artifactRoot, s.binaries)
		if err != nil {
			return MutantResult{}, fmt.Errorf("gomutants: session exec fuzz workspace: %w", err)
		}
	}
	run := execute.MutantRun{
		ID:          mutant.ID,
		DisplayID:   mutant.DisplayID,
		Package:     s.packageOf(mutant),
		Timeout:     timeout,
		Binaries:    binaryIndexes,
		Args:        targetArgs,
		OutputLimit: request.OutputLimit,
	}
	attempt := execute.RunOne(ctx, opts, run, runBinaries)
	// One attempt, on the caller's goroutine, with nothing else of this
	// session's in flight that it shares a worker with: attempt 1 and worker 0
	// are the facts rather than placeholders. The summary is built by
	// internal/execute so that an attempt recorded through this API and one
	// recorded by a run describe themselves the same way, field for field.
	traceSeq := s.recorder.MutantExec(execute.AttemptRecord(run, attempt, 1, 0))
	if s.keepTemp {
		s.keepScratch(scratch)
		kept = true
	}
	artifacts, artifactErr := captureFuzzArtifacts(artifactRoot)
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
		Artifacts:  artifacts,
		Binaries:   attempt.Binaries,
		TraceSeq:   traceSeq,
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
	targetArgs, err := sessionTargetArgs(request.Args, scratch, "probe")
	if err != nil {
		return ProbeResult{}, err
	}

	opts := s.probeOptions
	opts.ScratchDir = scratch
	opts.Env = env
	pass := execute.ProbeRun{
		Timeout:     timeout,
		Binaries:    binaryIndexes,
		Args:        targetArgs,
		OutputLimit: request.OutputLimit,
		LogPath:     filepath.Join(scratch, infectionLogName),
		Digest:      s.catalog.Digest(),
		Mutants:     s.catalog.Len(),
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
	partial := ProbeResult{Binaries: attempt.Binaries, TraceSeq: traceSeq}
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
		Binaries:   attempt.Binaries,
		TraceSeq:   traceSeq,
	}, nil
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
// The call names itself so that a diagnostic says which of the session's two
// measurements refused the target. Both go through this function because a
// caller composing arguments for one has to be able to hand them to the other:
// one request vocabulary, one set of reserved flags, one message shape.
func sessionTargetArgs(args []string, scratch, call string) ([]string, error) {
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
		byID[public.ID] = public
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
