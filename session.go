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
	defaultProfile         = "balanced"
	defaultMutantTimeout   = 10 * time.Second
	defaultMutantMemory    = 1 << 30
	mutantMemoryFactor     = 4
	defaultBuildTimeout    = 10 * time.Minute
	sessionPrefix          = "session-"
	execPrefix             = "exec-"
	probePrefix            = "probe-"
	infectionLogName       = "infection.log"
	maximumArtifacts       = 128
	maximumArtifactBytes   = 2 << 20
	maximumArtifactsSize   = 16 << 20
	mainOverlayName        = "main-overlay"
	probeOverlayName       = "probe-overlay"
	overlayManifestName    = "overlay.json"
	stageFreezeBuildInputs = "freeze-build-inputs"
	privateDirectoryMode   = 0o700
	privateFileMode        = 0o600
	uncachedTestFlag       = "-count=1"
)

type Session struct {
	mu             sync.RWMutex
	root           string
	scratch        string
	env            []string
	catalog        *mutation.Catalog
	publicCatalog  Catalog
	modulePath     string
	accepted       map[string]bool
	rejections     map[string]Rejection
	binaries       []execute.TestBinary
	executeOptions execute.Options
	mutantTimeout  time.Duration
	mutantMemory   int64
	preparedFiles  map[string]fileState
	overlayPath    string
	closed         bool

	probeSnapshot *snapshot.Snapshot
	probeBinaries []execute.TestBinary
	probeOptions  execute.Options
	probeOverlay  string

	keepTemp    bool
	keepMu      sync.Mutex
	keptScratch []string
	preserved   []keptDirectory

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

func (w *Workspace) Prepare(ctx context.Context, options PrepareOptions) (*Session, error) {
	if w == nil {
		return nil, errors.New("gomutants: prepare: nil workspace")
	}
	return w.prepare(ctx, options)
}

func (w *Workspace) prepare(ctx context.Context, options PrepareOptions) (session *Session, err error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if err = w.beginPrepare(); err != nil {
		return nil, err
	}
	spent := false
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
	var scratch string
	fail := func(err error) (*Session, error) { return failPrepare(probeSnap, scratch, w.keepTemp, err) }

	var validated validate.Result
	var overlayPath string
	var frozen frozenInputs
	err = w.underTreeWrite(func() (windowErr error) {
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
		scratch, err = os.MkdirTemp(w.scratch, sessionPrefix)
		if err != nil {
			return fmt.Errorf("gomutants: prepare session scratch: %w", err)
		}
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
				Modules:      []validate.Module{{Dir: ".", Path: found.ModulePath}},
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

	var verifiedPeak int64
	if !resolved.SkipVerify {
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
		resolved.Selection,
	)
	session = &Session{
		root:           w.snapshot.Root,
		scratch:        scratch,
		env:            slices.Clone(w.env),
		catalog:        catalog,
		publicCatalog:  publicCatalog,
		modulePath:     found.ModulePath,
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

func effectiveMemoryLimit(requested, session int64, args []string) int64 {
	if requested != 0 || hasFuzzTarget(args) {
		return requested
	}
	return session
}

func sessionMemoryBound(peak int64) int64 {
	if !runner.MemoryBoundSupported() {
		return 0
	}
	return max(int64(defaultMutantMemory), mutantMemoryFactor*peak)
}

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

func checkDiscoveredSources(
	manifest []snapshot.Entry, scanned map[string]string,
	catalog *mutation.Catalog, captured map[string]sourceImage,
) error {
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

func stageResult(err error) string {
	if err != nil {
		return trace.ResultFailed
	}
	return trace.ResultSucceeded
}

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
			Modules:      []validate.Module{{Dir: ".", Path: opts.modulePath}},
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
		probeResolved, resolveErr := filepath.EvalSymlinks(opts.snap.Root)
		if resolveErr != nil {
			return fmt.Errorf("gomutants: prepare probe instrumentation overlay: %w", resolveErr)
		}
		overlayPath, validateErr = writeInstrumentationOverlay(opts.snap.Root, probeResolved,
			opts.scratch, probeOverlayName, validated.Instrumented, nil)
		if validateErr != nil {
			return fmt.Errorf("gomutants: prepare probe instrumentation overlay: %w", validateErr)
		}
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

func (s *Session) Catalog() Catalog {
	if s == nil {
		return Catalog{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneCatalog(s.publicCatalog)
}

func (s *Session) OverlayManifest() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.overlayPath
}

func (s *Session) ProbeOverlayManifest() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.probeOverlay
}

func (s *Session) keepScratch(scratch string) {
	s.recorder.Artifact(trace.ArtifactKeptExecScratch, scratch)
	s.keepMu.Lock()
	defer s.keepMu.Unlock()
	s.keptScratch = append(s.keptScratch, scratch)
}

func (s *Session) keptScratchDirs() []string {
	if s == nil {
		return nil
	}
	s.keepMu.Lock()
	defer s.keepMu.Unlock()
	return slices.Clone(s.keptScratch)
}

type sessionTarget struct {
	options      execute.Options
	binaries     []execute.TestBinary
	indexes      []int
	args         []string
	timeout      time.Duration
	memory       int64
	scratch      string
	artifactRoot string
}

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
	memory = effectiveMemoryLimit(memory, s.mutantMemory, args)
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
	traceSeq := s.recorder.MutantExec(execute.AttemptRecord(run, attempt, 1))
	if s.keepTemp {
		s.keepScratch(target.scratch)
		kept = true
	}
	artifacts, artifactErr := captureFuzzArtifacts(target.artifactRoot)
	result := MutantResult{
		ID:             mutant.ID,
		DisplayID:      mutant.DisplayID,
		Outcome:        Outcome(attempt.Outcome.String()),
		KilledBy:       attempt.KilledBy,
		Duration:       attempt.Duration,
		OutputTail:     attempt.OutputTail,
		Output:         attempt.Output,
		Truncated:      attempt.Truncated,
		TotalBytes:     attempt.OutputBytes,
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
	record := execute.ControlRecord(run, attempt)
	traceSeq := s.recorder.Note(record.Kind, record.Code, record.Detail)
	if s.keepTemp {
		s.keepScratch(target.scratch)
		kept = true
	}
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
	result := ControlResult{
		Package:        attempt.Package,
		ExitCode:       attempt.ExitCode,
		TimedOut:       attempt.TimedOut,
		Duration:       attempt.Duration,
		Output:         attempt.Output,
		Truncated:      attempt.Truncated,
		TotalBytes:     attempt.OutputBytes,
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
	memory := effectiveMemoryLimit(request.MemoryLimit, s.mutantMemory, request.Args)
	scratch, err := os.MkdirTemp(s.scratch, probePrefix)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("gomutants: session probe scratch: %w", err)
	}
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
	traceSeq := s.recorder.ProbeExec(s.probePassRecord(pass, attempt, binaryIndexes))
	if s.keepTemp {
		s.keepScratch(scratch)
		kept = true
	}
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
	if err := checkInfectedShape(attempt.Infected, len(s.publicCatalog.Mutants)); err != nil {
		return partial, err
	}
	infected := filterInfected(attempt.Infected, s.publicCatalog.Mutants)
	if err := checkInfectedProbed(infected, s.publicCatalog.Mutants); err != nil {
		return partial, err
	}
	return ProbeResult{
		Outcome:        ProbeOutcome(attempt.Outcome),
		Infected:       infected,
		ExitCode:       attempt.ExitCode,
		Duration:       attempt.Duration,
		Output:         attempt.Output,
		Truncated:      attempt.Truncated,
		TotalBytes:     attempt.OutputBytes,
		PeakMemory:     attempt.PeakMemory,
		MemoryExceeded: attempt.MemoryExceeded,
		Binaries:       attempt.Binaries,
		TraceSeq:       traceSeq,
		TestLogs:       publicTestLogs(attempt.TestLogs),
	}, nil
}

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

func filterInfected(indices []uint32, mutants []Mutant) []uint32 {
	if indices == nil {
		return nil
	}
	kept := make([]uint32, 0, len(indices))
	for _, index := range indices {
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
			Probed:       probed[internal.ID] && accepted[internal.ID],
		}
		mutants = append(mutants, public)
	}
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
	public.PreparedDigest = preparedDigest(public)
	return public, rejectionIndex
}

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
