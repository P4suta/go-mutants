// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"errors"
	"slices"
	"strings"
)

type Code string

const (
	CodeInvalidRunID Code = "GOM5101"

	CodeInvalidStatus Code = "GOM5102"

	CodeInvalidTimestamps Code = "GOM5103"

	CodeInvalidWorkspaceDigest Code = "GOM5104"

	CodeInvalidSelection Code = "GOM5105"

	CodeInvalidTestCommand Code = "GOM5106"

	CodeNoReport Code = "GOM5107"

	CodeInvalidCoverage Code = "GOM5108"

	CodeInvalidCache Code = "GOM5109"

	CodeNoCatalog Code = "GOM5110"

	CodeUnknownMutant Code = "GOM5111"

	CodeDuplicateEntry Code = "GOM5112"

	CodeMissingResult Code = "GOM5113"

	CodeMissingLocation Code = "GOM5114"

	CodeInvalidOutcome Code = "GOM5115"

	CodeEncodeFailed Code = "GOM5120"

	CodeInvalidExecutions Code = "GOM5121"

	CodeInvalidMemory Code = "GOM5122"

	CodeCacheUnavailable Code = "GOM5130"

	CodeHistoryDirectory Code = "GOM5131"

	CodeHistoryWrite Code = "GOM5132"

	CodeForeignWorkspace Code = "GOM5133"

	CodeInvalidNotRunReason Code = "GOM5116"
	CodeMalformedDocument   Code = "GOM5117"

	CodeHistoryUnreadable Code = "GOM5118"

	CodeHistoryNotRemoved Code = "GOM5119"
)

const (
	CodeProjectionSourceUnreadable Code = "GOM5201"

	CodeProjectionSourceDrift Code = "GOM5202"

	CodeProjectionInvalid Code = "GOM5203"

	CodeProjectionSchemaUnusable Code = "GOM5204"

	CodeVendoredAssetTampered Code = "GOM5210"

	CodeArtifactDirectory Code = "GOM5220"

	CodeArtifactWrite Code = "GOM5221"

	CodeArtifactRollback Code = "GOM5222"
)

const (
	CodeInvalidShardSpec Code = "GOM7801"
	CodeInvalidShard     Code = "GOM7802"

	CodeNoShardReports         Code = "GOM7810"
	CodeNotAShardReport        Code = "GOM7811"
	CodeIncongruentShards      Code = "GOM7812"
	CodeIncompleteShardSet     Code = "GOM7813"
	CodeShardOwnershipMismatch Code = "GOM7814"
)

func (c Code) String() string { return string(c) }

var codes = []Code{
	CodeInvalidRunID,
	CodeInvalidStatus,
	CodeInvalidTimestamps,
	CodeInvalidWorkspaceDigest,
	CodeInvalidSelection,
	CodeInvalidTestCommand,
	CodeNoReport,
	CodeInvalidCoverage,
	CodeInvalidCache,
	CodeNoCatalog,
	CodeUnknownMutant,
	CodeDuplicateEntry,
	CodeMissingResult,
	CodeMissingLocation,
	CodeInvalidOutcome,
	CodeInvalidNotRunReason,
	CodeMalformedDocument,
	CodeHistoryUnreadable,
	CodeHistoryNotRemoved,
	CodeEncodeFailed,
	CodeInvalidExecutions,
	CodeInvalidMemory,
	CodeCacheUnavailable,
	CodeHistoryDirectory,
	CodeHistoryWrite,
	CodeForeignWorkspace,
	CodeProjectionSourceUnreadable,
	CodeProjectionSourceDrift,
	CodeProjectionInvalid,
	CodeProjectionSchemaUnusable,
	CodeVendoredAssetTampered,
	CodeArtifactDirectory,
	CodeArtifactWrite,
	CodeArtifactRollback,
	CodeInvalidShardSpec,
	CodeInvalidShard,
	CodeNoShardReports,
	CodeNotAShardReport,
	CodeIncongruentShards,
	CodeIncompleteShardSet,
	CodeShardOwnershipMismatch,
}

func Codes() []Code { return slices.Clone(codes) }

type Error struct {
	Code    Code
	Message string
	Err     error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
