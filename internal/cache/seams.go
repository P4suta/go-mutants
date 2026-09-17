// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"os"
	"path/filepath"
)

var (
	executablePath = os.Executable
	readExecutable = os.ReadFile

	absPath = filepath.Abs

	evalSymlinks = filepath.EvalSymlinks

	readDir = os.ReadDir

	writeTemp = (*os.File).Write
	syncTemp  = (*os.File).Sync
	closeTemp = (*os.File).Close
)
