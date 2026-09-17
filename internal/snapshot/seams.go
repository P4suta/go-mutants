// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"os"
	"path/filepath"
)

var (
	absPath = filepath.Abs

	makeTreeDir = os.Mkdir
	makeDirTree = os.MkdirAll
	setDirPerm  = finalizeDirPerm
	claimDir    = claimDestination

	finalizeCopyPerm = finalizePerm
	closeCopy        = (*os.File).Close
	setFileTimes     = os.Chtimes
)
