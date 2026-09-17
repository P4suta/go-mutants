// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import "slices"

const (
	LimitationAssuranceIncomplete              = "assurance-incomplete"
	LimitationAssuranceInterrupted             = "assurance-interrupted"
	LimitationConfigurationMetadataUnavailable = "configuration-metadata-unavailable"
	LimitationContractMetadataUnavailable      = "contract-metadata-unavailable"
	LimitationGitMetadataUnavailable           = "git-metadata-unavailable"
	LimitationGoMutantsMetadataUnavailable     = "go-mutants-metadata-unavailable"
	LimitationGoToolchainMetadataUnavailable   = "go-toolchain-metadata-unavailable"
	LimitationLaterPhasesNotRun                = "later-phases-not-run"
	LimitationModuleMetadataUnavailable        = "module-metadata-unavailable"
	LimitationPlanCostEstimate                 = "plan-cost-estimate"
	LimitationProjectExclude                   = "project-exclude"
	LimitationRaceScopeStaticEstimate          = "race-scope-static-estimate"
	LimitationRepairPreimageChanged            = "repair-preimage-changed"
	LimitationRepairValidationRejected         = "repair-validation-rejected"
	LimitationResourceCacheDisabled            = "resource-cache-disabled"
	LimitationSnapshotMetadataUnavailable      = "snapshot-metadata-unavailable"
	LimitationUnresolvedMutationGaps           = "unresolved-mutation-gaps"
	LimitationWholeTreeBehaviourKeys           = "whole-tree-behaviour-keys"
)

var limitationCodes = []string{
	LimitationAssuranceIncomplete,
	LimitationAssuranceInterrupted,
	LimitationConfigurationMetadataUnavailable,
	LimitationContractMetadataUnavailable,
	LimitationGitMetadataUnavailable,
	LimitationGoMutantsMetadataUnavailable,
	LimitationGoToolchainMetadataUnavailable,
	LimitationLaterPhasesNotRun,
	LimitationModuleMetadataUnavailable,
	LimitationPlanCostEstimate,
	LimitationProjectExclude,
	LimitationRaceScopeStaticEstimate,
	LimitationRepairPreimageChanged,
	LimitationRepairValidationRejected,
	LimitationResourceCacheDisabled,
	LimitationSnapshotMetadataUnavailable,
	LimitationUnresolvedMutationGaps,
	LimitationWholeTreeBehaviourKeys,
}

func LimitationCodes() []string { return slices.Clone(limitationCodes) }

func KnownLimitationCode(code string) bool {
	return slices.Contains(limitationCodes, code)
}
