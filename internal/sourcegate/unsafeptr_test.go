// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourcegate_test

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/P4suta/go-mutants/internal/testkit"
)

// safeCallees are the functions a Pointer may be converted to a uintptr inside,
// spelled as the type checker names them.
//
// Every entry was read in the source of the version this module builds against
// before it was written down, because the whole content of the list is a
// compiler pragma that is invisible from a call site.
//
//   - The syscall package's assembly entry points carry `//go:uintptrkeepalive`
//     and `//go:nosplit`, which is the pair that makes the conversion mean
//     anything: the first keeps the pointed-at object alive for the call, and
//     the second is why the *stack* cannot move underneath it — in the
//     runtime's own words beside those pragmas, "stack copying does not account
//     for uintptrkeepalive". `unsafe`'s documentation names syscall.Syscall as
//     the canonical case. The set differs by platform:
//     Syscall/Syscall6/RawSyscall/RawSyscall6 everywhere, plus
//     Syscall9/12/15/18 and SyscallN on Windows. Listing the union is right,
//     because a name that does not exist on a target cannot resolve to this
//     object there.
//   - The DLL call wrappers — syscall's and golang.org/x/sys/windows' `Proc`
//     and `LazyProc` — carry `//go:uintptrescapes`, which is stronger still: it
//     forces the arguments to escape to the heap, where nothing moves. They are
//     here so that the documented way of calling into a DLL is not reported as
//     a defect, which is how a gate teaches people to work around it.
//
// golang.org/x/sys/unix is deliberately absent. Its Syscall wrappers in v0.47.0
// carry neither pragma, this module converts no pointer through them, and an
// entry nobody has checked is worse than an entry that is missing.
var safeCallees = []string{
	"syscall.Syscall",
	"syscall.Syscall6",
	"syscall.Syscall9",
	"syscall.Syscall12",
	"syscall.Syscall15",
	"syscall.Syscall18",
	"syscall.SyscallN",
	"syscall.RawSyscall",
	"syscall.RawSyscall6",
	"syscall.Proc.Call",
	"syscall.LazyProc.Call",
	"golang.org/x/sys/windows.Proc.Call",
	"golang.org/x/sys/windows.LazyProc.Call",
}

// TestEveryPointerHandedToASyscallIsConvertedInsideTheCall is a rule of the Go
// compiler, written down where it can fail.
//
// `uintptr(unsafe.Pointer(&x))` is only meaningful in the argument list of the
// assembly syscall itself. The runtime says why, in syscall/dll_windows.go
// beside the pragmas that make it work: "//go:nosplit because stack copying
// does not account for uintptrkeepalive, so the stack must not grow. Stack
// copying cannot blindly assume that all uintptr arguments are pointers,
// because some values may look like pointers, but not really be pointers, and
// adjusting their value would break the call."
//
// Write the conversion one Go frame earlier — in a call to an ordinary wrapper
// that spells the parameter `uintptr`, which is how golang.org/x/sys/windows
// spells SetInformationJobObject and QueryInformationJobObject — and none of
// that holds. Escape analysis leaves the value on the stack, because a uintptr
// is not a pointer; the wrapper's own prologue is a stack-growth point; a
// goroutine that grows there has its frames copied and its old stack span
// returned to the pool with the uintptr still naming the old address. Another
// goroutine takes the span, writes its own frames into it, and the kernel reads
// whatever landed at that offset. runtime.KeepAlive does not help: it keeps a
// value from being collected, and a stack frame is not collected, it is moved.
//
// That is a defect only a concurrent run can show, and it showed once —
// windows-latest, 2026-09-06, `GOM7201: could not set kill-on-close on the
// Windows job object ...: The parameter is incorrect.` under eight concurrent
// Workspace.Exec calls, green on every other Windows run. `go vet` has no
// analyzer for this direction of the conversion, so the gate is one of this
// repository's own.
//
// It is a *type-checked* gate rather than a scan over identifier text, and the
// three cases that decided that are in
// [TestThePointerConversionGateSeesThroughAliasesAndShadowing].
//
// It lives in a package of its own rather than beside internal/runner, whose
// Windows files are what it was written for. Package [sourcegate]'s doc says
// why in full: a type-checked module is a gigabyte of heap, and on Linux a
// gigabyte of heap in a test binary is charged to the next child that binary
// forks — which is exactly how internal/runner's memory tests measure the
// footprint every bound in them is derived from.
func TestEveryPointerHandedToASyscallIsConvertedInsideTheCall(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	for _, goos := range targetsToTypeCheck() {
		t.Run(goos, func(t *testing.T) {
			t.Parallel()

			offenders := unguardedConversions(t, root, goos)
			if len(offenders) != 0 {
				t.Errorf("%d conversion(s) hand a pointer to a uintptr parameter of an ordinary Go "+
					"function, where a stack copy would leave the kernel reading a freed frame:\n\t%s",
					len(offenders), strings.Join(offenders, "\n\t"))
			}
		})
	}
}

// TestThePointerConversionGateSeesThroughAliasesAndShadowing proves the gate
// can fail, that it fails precisely, and that it resolves bindings rather than
// matching names.
//
// A gate that only ever passes is indistinguishable from one that cannot see
// anything, and a gate that matched identifier text would be wrong in three
// directions at once. So the fixtures are the three shapes that separate the
// two: an aliased `unsafe` import, which no scan for `unsafe.Pointer` would
// find; a local variable named `syscall` whose SyscallN *method* makes an
// unguarded conversion look guarded; and an aliased real `syscall` import,
// which a scan would report as an offender that is not one.
func TestThePointerConversionGateSeesThroughAliasesAndShadowing(t *testing.T) {
	t.Parallel()

	// The conversion one frame early, spelled through an import alias, so that
	// nothing in the file contains the text "unsafe.Pointer".
	const aliasedUnsafe = `package aliasedunsafe

import u "unsafe"

func Set(fn uintptr, info *[16]byte) {
	wrapper(fn, uintptr(u.Pointer(info)))
}

func wrapper(fn, addr uintptr) { _, _ = fn, addr }
`
	// A local named syscall whose SyscallN is a method. The call reads exactly
	// like the safe one and is not: nothing about a method on somebody's own
	// type carries //go:uintptrkeepalive.
	const shadowedSyscall = `package shadowedsyscall

import "unsafe"

type stand struct{}

func (stand) SyscallN(trap uintptr, args ...uintptr) (uintptr, uintptr, error) {
	_, _ = trap, args
	return 0, 0, nil
}

func Set(fn uintptr, info *[16]byte) {
	syscall := stand{}
	_, _, _ = syscall.SyscallN(fn, uintptr(unsafe.Pointer(info)), unsafe.Sizeof(*info))
}
`
	// The real thing under a different name, which is safe and must not be
	// reported.
	const aliasedSyscall = `package aliasedsyscall

import (
	sc "syscall"
	"unsafe"
)

func Set(fn uintptr, info *[16]byte) {
	_, _, _ = sc.SyscallN(fn, uintptr(unsafe.Pointer(info)), unsafe.Sizeof(*info))
}
`
	// And the plain forms of both, so the ordinary cases are covered too — the
	// safe one in a _test.go file, because the gate reads those as well.
	const plainlyUnsafe = `package plainlyunsafe

import "unsafe"

func Set(info *[16]byte) uintptr { return uintptr(unsafe.Pointer(info)) }
`
	const plainlySafeTest = `package plainlysafe

import (
	"syscall"
	"unsafe"
)

func Set(fn uintptr, info *[16]byte) {
	_, _, _ = syscall.SyscallN(fn, uintptr(unsafe.Pointer(info)), unsafe.Sizeof(*info))
}
`

	// The pointer parked in a variable first, so the argument of the uintptr
	// conversion is an identifier rather than a call: the same hazard, one
	// statement apart.
	const storedPointer = `package storedpointer

import "unsafe"

func Set(fn uintptr, info *[16]byte) {
	p := unsafe.Pointer(info)
	wrapper(fn, uintptr(p))
}

func wrapper(fn, addr uintptr) { _, _ = fn, addr }
`
	// Both halves through defined types whose underlying types are the
	// dangerous pair. The names are somebody's own; the types are not.
	const definedTypes = `package definedtypes

import "unsafe"

type address uintptr

type raw unsafe.Pointer

func Set(fn uintptr, info *[16]byte) {
	wrapper(fn, address(raw(unsafe.Pointer(info))))
}

func wrapper(fn uintptr, addr address) { _, _ = fn, addr }
`

	m := testkit.NewModule(t).Module("fixture.example/ptr")
	m.Source("aliasedunsafe/a.go", aliasedUnsafe)
	m.Source("shadowedsyscall/s.go", shadowedSyscall)
	m.Source("aliasedsyscall/a.go", aliasedSyscall)
	m.Source("plainlyunsafe/p.go", plainlyUnsafe)
	m.Source("plainlysafe/p_test.go", plainlySafeTest)
	m.Source("storedpointer/s.go", storedPointer)
	m.Source("definedtypes/d.go", definedTypes)

	// The fixtures name syscall.SyscallN, which exists on Windows and nowhere
	// else, so the module is type-checked for Windows whatever the host is —
	// which is also the target the real gate cares about most.
	got := unguardedConversions(t, m.Root(), "windows")

	// Derived rather than typed: [testkit.Module.Source] writes an SPDX header
	// in front of each body, so a line counted off the literals above would be
	// wrong by however tall that header is today.
	want := []string{
		fmt.Sprintf("aliasedunsafe/a.go:%d", lineOf(testkit.SPDXHeader+aliasedUnsafe, "wrapper(fn,")),
		fmt.Sprintf("definedtypes/d.go:%d", lineOf(testkit.SPDXHeader+definedTypes, "wrapper(fn,")),
		fmt.Sprintf("plainlyunsafe/p.go:%d", lineOf(testkit.SPDXHeader+plainlyUnsafe, "func Set(")),
		fmt.Sprintf("shadowedsyscall/s.go:%d", lineOf(testkit.SPDXHeader+shadowedSyscall, "syscall.SyscallN(fn,")),
		fmt.Sprintf("storedpointer/s.go:%d", lineOf(testkit.SPDXHeader+storedPointer, "wrapper(fn,")),
	}
	if !slices.Equal(got, want) {
		t.Errorf("the gate reported\n\t%v\nwant\n\t%v\nthe aliased `unsafe` import, the shadowed "+
			"`syscall` local, a pointer parked in a variable and a pair of defined types are offenders "+
			"a syntactic match would miss, and the aliased real `syscall` import is one it would invent",
			got, want)
	}
}

// lineOf is the one-based number of the first line of source containing needle.
func lineOf(source, needle string) int {
	for i, line := range strings.Split(source, "\n") {
		if strings.Contains(line, needle) {
			return i + 1
		}
	}
	return 0
}

// targetsToTypeCheck names the operating systems the gate loads the module for.
//
// Windows always, because the files that hand a pointer to the kernel are the
// Windows ones and a gate that could not see them on a Linux developer's
// machine would be a gate for nobody. The host too, when it is something else,
// so that the POSIX halves of this package are covered by the job that runs on
// them — between the three operating systems CI already runs the unit tier on,
// every file in the module is type-checked by somebody.
//
// A build constraint hides a file from the compiler and from the type checker
// alike, which is the whole reason this is a list rather than one load: nothing
// a `GOOS=linux` pass sees says anything at all about supervisor_windows.go.
func targetsToTypeCheck() []string {
	if runtime.GOOS == "windows" {
		return []string{"windows"}
	}
	return []string{"windows", runtime.GOOS}
}

// unguardedConversions type-checks every package under root for one target and
// returns the pointer-to-uintptr conversions that are not written in the
// argument list of a call the compiler treats specially, as "<path>:<line>",
// sorted and without duplicates.
//
// It is a type-checked pass rather than a syntactic one because every question
// it asks is about a binding: which package `u.Pointer` names, whether the
// `syscall` in `syscall.SyscallN` is an import or a local, which function an
// aliased selector resolves to. A scan can be wrong about all three, and was.
//
// The cost is one `go list` and one type-checking pass per target, which is why
// this file is in the unit-toolchain ledger. Test files are included, because
// nothing about the rule is different in one.
func unguardedConversions(t *testing.T, root, goos string) []string {
	t.Helper()

	// The located toolchain rather than whatever is first on PATH: go/packages
	// runs the `go` command it finds, and [testkit.GoBinary] is what applies
	// this repository's skip-or-fail policy on a machine that has none.
	toolchain := testkit.GoBinary(t)
	env := append(os.Environ(), "GOOS="+goos,
		"PATH="+filepath.Dir(toolchain)+string(filepath.ListSeparator)+os.Getenv("PATH"))

	fset := token.NewFileSet()
	loaded, err := packages.Load(&packages.Config{
		Context: t.Context(),
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   root,
		Env:   env,
		Fset:  fset,
		Tests: true,
	}, "./...")
	if err != nil {
		t.Fatalf("loading %s for %s: %v", root, goos, err)
	}
	if len(loaded) == 0 {
		t.Fatalf("no packages loaded from %s for %s, so this proves nothing", root, goos)
	}
	// A package that did not type-check has an incomplete types.Info, and every
	// conversion in it would read as "not resolved" — which this gate reports
	// as nothing at all. A load error is therefore fatal rather than skipped.
	var failures []string
	for _, pkg := range loaded {
		for _, loadErr := range pkg.Errors {
			failures = append(failures, pkg.PkgPath+": "+loadErr.Error())
		}
	}
	if len(failures) != 0 {
		t.Fatalf("%d package error(s) for %s, so the gate saw less than the module:\n\t%s",
			len(failures), goos, strings.Join(failures, "\n\t"))
	}

	var offenders []string
	for _, pkg := range loaded {
		for _, file := range pkg.Syntax {
			for _, call := range unguardedInFile(pkg.TypesInfo, file) {
				position := fset.Position(call.Pos())
				rel, relErr := filepath.Rel(root, position.Filename)
				if relErr != nil {
					rel = position.Filename
				}
				offenders = append(offenders, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), position.Line))
			}
		}
	}
	// `Tests: true` returns a package's files under several identities — the
	// package, its test variant and its external test package — so the same
	// conversion arrives more than once and is one finding.
	slices.Sort(offenders)
	return slices.Compact(offenders)
}

// unguardedInFile returns the pointer-to-uintptr conversions in one file that
// are not arguments of a call in [safeCallees].
//
// The permitted ones are collected first and whole, because "is this expression
// an argument of that call" is a question about a parent and ast.Inspect
// answers questions about children.
func unguardedInFile(info *types.Info, file *ast.File) []*ast.CallExpr {
	permitted := make(map[*ast.CallExpr]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isSafeCallee(info, call) {
			return true
		}
		for _, arg := range call.Args {
			if inner, ok := arg.(*ast.CallExpr); ok && isPointerToUintptr(info, inner) {
				permitted[inner] = true
			}
		}
		return true
	})

	var unguarded []*ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && isPointerToUintptr(info, call) && !permitted[call] {
			unguarded = append(unguarded, call)
		}
		return true
	})
	return unguarded
}

// isPointerToUintptr reports whether call converts a value whose underlying
// type is unsafe.Pointer to a type whose underlying type is uintptr.
//
// Both halves are asked of the type checker rather than of the text, and of
// the *underlying* types rather than the spelled ones. `u.Pointer` under an
// import alias is the same type as `unsafe.Pointer`; a pointer parked in a
// variable one statement earlier is still an unsafe.Pointer when it reaches
// the conversion; `type address uintptr` is still a uintptr as far as a stack
// copy is concerned. All of those have to be reported. A method named Pointer
// on somebody's own type is not a conversion at all and must not be.
func isPointerToUintptr(info *types.Info, call *ast.CallExpr) bool {
	if !isConversionTo(info, call, types.Typ[types.Uintptr]) {
		return false
	}
	tv, ok := info.Types[call.Args[0]]
	return ok && tv.IsValue() && tv.Type != nil &&
		types.Identical(tv.Type.Underlying(), types.Typ[types.UnsafePointer])
}

// isConversionTo reports whether call converts its one argument to a type
// whose underlying type is want.
func isConversionTo(info *types.Info, call *ast.CallExpr, want types.Type) bool {
	if len(call.Args) != 1 {
		return false
	}
	tv, ok := info.Types[call.Fun]
	return ok && tv.IsType() && tv.Type != nil && types.Identical(tv.Type.Underlying(), want)
}

// isSafeCallee reports whether call goes to one of [safeCallees].
//
// The callee is resolved to the object it names, which is what tells an aliased
// import of the real `syscall` — safe — from a local variable called `syscall`
// whose SyscallN is a method on somebody's own type, which is not. A method is
// keyed by its receiver's type so the two cannot collide: a method on a local
// type carries the module's own package path.
func isSafeCallee(info *types.Info, call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := info.Uses[selector.Sel].(*types.Func)
	if !ok {
		return false
	}
	key := calleeKey(fn)
	return key != "" && slices.Contains(safeCallees, key)
}

// calleeKey names a function the way [safeCallees] spells it:
// "<package path>.<function>", or "<package path>.<receiver type>.<method>".
func calleeKey(fn *types.Func) string {
	pkg := fn.Pkg()
	if pkg == nil {
		return ""
	}
	recv := fn.Signature().Recv()
	if recv == nil {
		return pkg.Path() + "." + fn.Name()
	}
	receiver := recv.Type()
	if pointer, ok := receiver.(*types.Pointer); ok {
		receiver = pointer.Elem()
	}
	named, ok := receiver.(*types.Named)
	if !ok {
		return ""
	}
	return pkg.Path() + "." + named.Obj().Name() + "." + fn.Name()
}
