// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration && windows

package gomutants_test

import (
	"golang.org/x/sys/windows"
)

// stillActive is the exit code Windows reports for a process that has not
// exited, as GetExitCodeProcess documents it (STILL_ACTIVE, 259).
//
// It is spelled here because x/sys/windows does not export it, and it carries
// the usual caveat: a process that genuinely exits with 259 is indistinguishable
// from a running one. Nothing this suite starts does — a Go test binary exits 0
// or 1, and internal/runner's job termination code is its own — so the ambiguity
// costs nothing here.
const stillActive = 259

// processIsGone reports whether the process with this pid has ended.
//
// Windows has no signal-0 equivalent, so the question is asked by opening the
// process. PROCESS_QUERY_LIMITED_INFORMATION is the narrowest right that answers
// it and the one that is granted across integrity levels; a failure to open is
// taken as gone, which is what it means for a process this suite started and the
// runner has already waited for.
//
// A handle that does open still has to be interrogated: Windows keeps the object
// alive while any handle to it exists, so an open handle says the pid is
// resolvable rather than that the process is running. GetExitCodeProcess is the
// difference.
func processIsGone(pid int) (bool, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return true, nil
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return true, nil
	}
	return code != stillActive, nil
}
