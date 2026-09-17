// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import "slices"

func Types() []string {
	return []string{
		TypeRunStart, TypePhaseStart, TypePhaseEnd, TypePrepare, TypeExec,
		TypeMutantExec, TypeRoute, TypeProbeExec, TypeProgress, TypeArtifact,
		TypeRunEnd,
	}
}

func PreparePhases() []string {
	return []string{
		PreparePhaseDiscovery, PreparePhaseProbeSnapshot, PreparePhaseMainValidation,
		PreparePhaseMainRestoration, PreparePhaseVerification, PreparePhaseBinaryBuild,
		PreparePhaseProbeValidation, PreparePhaseProbeCoverageBuild, PreparePhaseProbeRestoration,
	}
}

func PrepareStates() []string {
	return []string{PrepareStateStarted, PrepareStateFinished}
}

func PrepareResults() []string {
	return []string{PrepareResultSucceeded, PrepareResultFailed, PrepareResultSkipped}
}

func RouteReasons() []string {
	return []string{ReasonCoverageReaching, ReasonProbeReaching, ReasonUnreached}
}

func Granularities() []string {
	return []string{GranularityBlock, GranularityFile}
}

func RouteFallbacks() []string {
	return []string{FallbackPositionUnknown, FallbackOutsideBlocks}
}

func DischargeReasons() []string {
	return []string{DischargeBranchNeverTaken, DischargeNeverInfected}
}

func ProbeOutcomes() []string {
	return []string{
		ProbeOutcomeMeasured, ProbeOutcomeTestFailed, ProbeOutcomeTimedOut,
		ProbeOutcomeUnavailable,
	}
}

func WholeTreeReasons() []string {
	return []string{
		WholeTreeStaticUnobservable, WholeTreeLogUnavailable, WholeTreeLogAmbiguous,
		WholeTreeDirectoryAccess, WholeTreeOutsideInput,
	}
}

func knownPreparePhase(phase string) bool {
	return slices.Contains(PreparePhases(), phase)
}
