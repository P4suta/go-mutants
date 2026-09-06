// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
)

// ModulePath is this module's path, exactly as build information names it.
//
// It is a constant rather than a string every consumer retypes because a
// consumer that wants to know which engine it linked has to scan
// [runtime/debug.BuildInfo] for this path, and a typo there is not a compile
// error: it is a scan that finds nothing and a build that quietly reports
// itself as unknown. Everything below does that scan so no consumer has to,
// but the constant stays exported for one that keeps its own.
const ModulePath = "github.com/P4suta/go-mutants"

// develVersion is the placeholder the go command records when it has no
// version to record: nothing can be fetched by it, and two builds carrying it
// can be any two trees.
//
// It is no longer the mark of a working-tree build, which is worth saying
// because it was until recently. Since go1.24 `go build` and `go install`
// stamp a main module built out of a VCS checkout with the pseudo-version of
// its last commit — plus "+dirty" when the tree has edits in it — so what
// still reports "(devel)" is a build the go command could not stamp: a *test*
// binary, which it never stamps; a build with -buildvcs=false; a tree under no
// version control at all; and a git *worktree*, because the go command wants
// .git to be a directory and in a worktree it is a file. That last one is how
// this project is developed, so it is the case its own gates run in.
//
// It is also what an empty version becomes anywhere in build information: the
// go command substitutes it when it writes the module lines, which is why a
// directory replacement reports it rather than "".
const develVersion = "(devel)"

// unknownVersion is what [Version] says when build information does not name
// this module. It is deliberately not a version-shaped string — no leading
// "v", nothing resolvable — so that a value printed into a report or a header
// cannot be mistaken for one somebody could fetch.
const unknownVersion = "unknown"

// BuildInfo is what this build's build information says about go-mutants
// itself: which version of the engine a program linked, whether it was
// replaced, and whether any of that names one immutable set of sources.
//
// Every consumer that stores mutation evidence needs this, and before it was
// exported every one of them wrote the same scan over
// [runtime/debug.BuildInfo] — with the same handful of cases to get wrong: a
// module named twice, a `replace` nobody noticed, "(devel)" read as if it were
// a version. The scan lives here now, and the rules it applies are documented
// on the fields rather than left for each consumer to rediscover.
//
// The zero value is what a build that does not name go-mutants at all reports.
// It is not an error: a program can carry perfectly good build information in
// which this module simply does not appear.
type BuildInfo struct {
	// Version is the module version build information names for go-mutants: a
	// tag, a pseudo-version — which since go1.24 is also what a main module
	// built out of a VCS checkout gets, with "+dirty" appended when the tree
	// had edits in it — "(devel)" when the go command had no version to record,
	// or "" when build information does not name this module at all. See
	// develVersion for which builds still report "(devel)".
	//
	// Under a replacement this is the version that was *required*, which is
	// not the code that ran; ReplacePath and ReplaceVersion name what ran.
	//
	// It is a label, not a decision. Auditable is the decision.
	Version string

	// Sum is the module checksum ("h1:…") when build information carries one.
	//
	// It is "" under a replacement: the checksum recorded there covers the
	// replacement, and a checksum of a fork is not a checksum of go-mutants.
	// It is also "" whenever the go command had none to record — a build from
	// a working tree, a vendored build — which is why Auditable does not
	// require it. A main module is not automatically without one: `go install
	// example.com/tool@v1.0.0` stamps the checksum of what the proxy served.
	//
	// What it is for is a consumer that wants to record the checksum it linked
	// against, not a proof it should be computing itself.
	Sum string

	// Replaced reports that a `replace` directive was in effect for
	// go-mutants, so the code that ran is not the code Version names.
	Replaced bool

	// ReplacePath is where the replacement pointed: a module path, or a
	// directory. ReplaceVersion is its version — "(devel)" for a directory,
	// which has none of its own and which the go command fills in with that
	// placeholder like every other empty version it writes.
	ReplacePath    string
	ReplaceVersion string

	// Main reports that go-mutants is the main module of the running program —
	// the `go-mutants` command itself, or this module's own tests — rather than
	// a dependency of somebody else's. It is what makes VCSRevision meaningful:
	// build settings describe the main module and nothing else.
	Main bool

	// VCSRevision and VCSModified are the `vcs.revision` and `vcs.modified`
	// build settings, and they are read only when Main is true, because that
	// is the only module they describe. Both are zero in a build that stamped
	// nothing — `-buildvcs=false`, or a build from an unpacked archive — and a
	// `vcs.modified` value that cannot be read as a boolean counts as
	// modified, because a tree that cannot be shown clean is not one to
	// audit.
	VCSRevision string
	VCSModified bool

	// Auditable reports that Version names one immutable set of sources: a tag
	// or a pseudo-version that is not replaced, or a main module with a clean
	// VCS revision.
	//
	// A version carrying build metadata — anything after a "+" — is never one
	// of those, whatever the VCS settings beside it say. "+dirty", which
	// go1.24 and later append to a main module built from an edited checkout,
	// is the case this rule exists for: it looks exactly like a pseudo-version
	// and no proxy will ever serve it. The one exception is "+incompatible",
	// which is a published version of a v2-or-later module without a go.mod
	// and can be fetched like any other. A version and a vcs.modified setting
	// that disagree are build information contradicting itself, and this
	// answers false rather than believing the half that is more convenient.
	//
	// A replaced module is never auditable, and nothing this package can
	// compute about itself would change that. An embedded source digest is the
	// obvious idea and it is the wrong one: it would describe the sources that
	// were committed, not the replaced working tree with edits in it that
	// actually compiled, so it would read as a proof exactly when it is a lie.
	// The running executable's digest is the only content identity that covers
	// the bytes actually running, and taking it is the consumer's to do — it
	// alone knows which file it launched, whether that file is still there,
	// and whether reading it is worth the cost.
	Auditable bool
}

// ReadBuildInfo reports what this program's build information says about
// go-mutants.
//
// ok is false only when the program carries no build information at all — a
// binary the go command did not build, or one built with the information
// stripped. It is not about go-mutants being named: a program with build
// information that does not mention this module returns the zero [BuildInfo]
// and ok true, because the build information was read successfully and what it
// says is "not here".
//
// Build information that names go-mutants more than once is reported the same
// way: the zero value, ok true, Auditable false. Such build information
// contradicts itself, so no single version can be named — and answering with
// one of them would be picking an answer rather than reading one.
//
// One reading is absent rather than wrong, and a consumer has to know it: a
// *test* binary records no dependency list at all. The go command fills build
// information in before a test binary's imports are known, so `go version -m`
// on one shows the main module and no dep lines — and this therefore reports
// the zero value from inside a consumer's own `go test`, however firmly that
// consumer's go.mod requires go-mutants. Only a built program names the engine
// it linked, so a consumer that records which engine produced its evidence has
// to do it from a real binary, not from its test suite.
//
// The reading is done once. Build information cannot change while a process
// runs, and every caller after the first gets the same answer without another
// scan of the module graph.
func ReadBuildInfo() (info BuildInfo, ok bool) { return readBuildInfoOnce() }

// readBuildInfoOnce is the one reading. buildInfoFrom stays a pure function of
// its argument so the table tests keep working on values of their own.
var readBuildInfoOnce = sync.OnceValues(func() (BuildInfo, bool) {
	// The error case and the nil case are the same case, and buildInfoFrom
	// already has to handle nil for the tests that build their own.
	raw, _ := debug.ReadBuildInfo()
	return buildInfoFrom(raw)
})

// Version is [ReadBuildInfo]'s Version, or "unknown" when build information
// does not name this module.
//
// It is the label to print — in a report header, a log line, a `--version`
// string — and never the value to decide on: "(devel)" and a replaced module's
// required version both come back from here looking like versions, and only
// [BuildInfo.Auditable] says whether they name any particular source.
func Version() string { return versionOf(ReadBuildInfo()) }

// versionOf exists so that the "unknown" rule is testable without a build that
// carries no build information, which is not a build `go test` can produce.
func versionOf(info BuildInfo, ok bool) string {
	if !ok || info.Version == "" {
		return unknownVersion
	}
	return info.Version
}

// buildInfoFrom is the whole reading, taken apart from [debug.ReadBuildInfo]
// so that every shape build information can take — a replacement, a module
// named twice, a modified working tree — is a value a test can write down.
func buildInfoFrom(raw *debug.BuildInfo) (BuildInfo, bool) {
	if raw == nil {
		return BuildInfo{}, false
	}
	if raw.Main.Path == ModulePath {
		return mainModuleInfo(raw), true
	}
	var found *debug.Module
	for _, dependency := range raw.Deps {
		if dependency == nil || dependency.Path != ModulePath {
			continue
		}
		if found != nil {
			// Named twice: see ReadBuildInfo. Nothing here can be reported as
			// the version, so nothing is.
			return BuildInfo{}, true
		}
		found = dependency
	}
	if found == nil {
		return BuildInfo{}, true
	}
	info := moduleInfo(*found)
	info.Auditable = auditable(info)
	return info, true
}

// mainModuleInfo reads the build go-mutants is itself: the command, or this
// module's own test binaries. Only here do the VCS build settings mean
// anything, because they describe the main module and no other.
func mainModuleInfo(raw *debug.BuildInfo) BuildInfo {
	info := moduleInfo(raw.Main)
	info.Main = true
	for _, setting := range raw.Settings {
		switch setting.Key {
		case "vcs.revision":
			info.VCSRevision = setting.Value
		case "vcs.modified":
			modified, err := strconv.ParseBool(setting.Value)
			// A value nobody can read is not evidence of a clean tree.
			info.VCSModified = err != nil || modified
		}
	}
	info.Auditable = auditable(info)
	return info
}

// moduleInfo copies one module entry across, following the replacement if
// there is one. Sum stays behind under a replacement deliberately: what build
// information records there is the replacement's checksum, and reporting it as
// go-mutants' own would be the one lie this type exists to avoid.
func moduleInfo(module debug.Module) BuildInfo {
	info := BuildInfo{Version: module.Version, Sum: module.Sum}
	if module.Replace == nil {
		return info
	}
	info.Sum = ""
	info.Replaced = true
	info.ReplacePath = module.Replace.Path
	info.ReplaceVersion = module.Replace.Version
	return info
}

// auditable applies the rule written out on [BuildInfo.Auditable].
//
// The local-metadata clause comes before the VCS one deliberately: "+dirty"
// and vcs.modified are stamped from the same status, so a version that says
// the tree was edited settles the question whatever the setting says. The VCS
// clause is guarded by Main because a dependency's sources are not what the
// main module's revision describes — buildInfoFrom reads those settings only
// for a main module, so the guard is a second lock on a door it also keeps
// shut, and it is what makes this function safe to call on any value.
func auditable(info BuildInfo) bool {
	switch {
	case info.Replaced, hasLocalMetadata(info.Version):
		return false
	case namesSources(info.Version):
		return true
	}
	return info.Main && info.VCSRevision != "" && !info.VCSModified
}

// namesSources reports whether a version string names sources anybody could
// fetch again and get the same bytes. A tag and a pseudo-version do; the
// placeholder and the empty string do not.
func namesSources(version string) bool {
	return version != "" && version != develVersion
}

// hasLocalMetadata reports a version carrying build metadata no module proxy
// can serve.
//
// Everything after the "+" is semantic versioning's build metadata, and the go
// command puts exactly one thing there that can still be fetched:
// "+incompatible", the marker of a v2-or-later module published without a
// go.mod. Anything else was written by whoever built the binary — "+dirty" is
// the go command's own — and names no version anybody else can resolve.
func hasLocalMetadata(version string) bool {
	plus := strings.IndexByte(version, '+')
	return plus >= 0 && version[plus:] != "+incompatible"
}
