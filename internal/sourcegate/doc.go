// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package sourcegate holds the rules about this module's own source that need
// the module type-checked to state.
//
// It has no code in it, and that is what it is for. A rule like "a Go pointer
// is never handed to the kernel as a uintptr except inside the syscall's own
// argument list" is a claim about bindings — which package a selector names
// under an import alias, whether an identifier is an import or a local — so it
// needs go/packages and a real toolchain, and it holds a type-checked tree of
// the whole module while it runs.
//
// That is why it is a package of its own rather than a test file beside the
// code each rule protects, and the reason is sharper than tidiness. The first
// home for the pointer rule was internal/runner, whose memory tests derive
// every bound in them from [testkit]-style measurement of a child that does
// nothing but exit — and on Linux that measurement comes back as *the parent's*
// resident high-water mark. os/exec forks with clone(CLONE_VM|CLONE_VFORK), so
// the child shares the parent's address space until it execs, and wait4's
// ru_maxrss for it starts from the parent's hiwater_rss, which nothing lowers
// again: debug.FreeOSMemory does not move it. A gate that had type-checked the
// module first therefore took that package's measured "footprint" from 25 MiB
// to 1.2 GiB, and every bound derived from it with it, so nothing tripped and
// four memory tests failed together.
//
// A rule that needs a loaded module belongs here. One that can be answered from
// the text belongs where the text is: internal/testkit's import gate is the
// other module-wide rule in this repository, and it stays there because a scan
// of imports needs neither a toolchain nor a heap.
//
// [testkit]: https://pkg.go.dev/github.com/P4suta/go-mutants/internal/testkit
package sourcegate
