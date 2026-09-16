// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The version this tree proposes, against the tags the repository already has.
//
// [TestTheVersionFilesAgreeWithTheConstant] pins the three files release-please
// rewrites to each other, and it was green while `origin` carried v0.1.0,
// v0.1.1 and v0.1.2 — three tags no commit on main can reach. Three files
// agreeing with each other is a different statement from those three files
// describing the repository, and nothing was making the second one. The module
// proxy resolved `@latest` to a version main did not have, and the next Release
// PR would have proposed v0.1.0 for a second time.
//
// What separates the two statements is reading git, so this is the tier the
// check lives in. The unit test stays as it is: it answers a question that
// needs no toolchain, and answering it faster is worth more than folding it
// into this one.
package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"

	"github.com/P4suta/go-mutants/internal/testkit"
)

// TestTheNextReleaseDoesNotCollideWithATagThisRepositoryAlreadyHas refuses a
// release number the repository has already published.
//
// release-please decides the next version from two places, and which one it
// reads depends on the manifest: an empty `.release-please-manifest.json` means
// nothing has been released, so `initial-version` in release-please-config.json
// is the proposal, and a manifest naming a version means the proposal is the
// one after it. Neither place consults the tags. A number that is already a tag
// is one release-publish.yml cannot push and a `go get` cannot disambiguate, so
// the collision has to be refused here rather than discovered at a tag push.
func TestTheNextReleaseDoesNotCollideWithATagThisRepositoryAlreadyHas(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	tags := releaseTags(t, root)

	var manifest map[string]string
	if err := json.Unmarshal([]byte(releaseFile(t, root, ".release-please-manifest.json")), &manifest); err != nil {
		t.Fatalf("reading .release-please-manifest.json: %v", err)
	}

	released, recorded := manifest["."]
	if recorded {
		// A manifest names the last release, so that release is a tag. A
		// manifest naming a version nothing tagged is release-please recording
		// something that never reached anybody.
		if !slices.Contains(tags, "v"+released) {
			t.Errorf(".release-please-manifest.json records %q, and no tag v%s exists;\n"+
				"\tthe manifest is release-please's record of what it published, so a version "+
				"nothing tagged is a record of a release nobody can fetch\n\ttags: %v",
				released, released, tags)
		}
		return
	}

	// No manifest entry: initial-version is the proposal, and it must be a
	// version nobody has published.
	initial := initialVersion(t, root)
	if initial == "" {
		return
	}
	if slices.Contains(tags, "v"+initial) {
		t.Errorf("release-please-config.json proposes initial-version %q and v%s is already a tag;\n"+
			"\trelease-please would ask for a tag that exists, and the module proxy already "+
			"answers `@latest` with one of these\n\ttags: %v", initial, initial, tags)
	}
}

// releaseTags is every `vX.Y.Z` tag the repository carries, sorted.
//
// A repository with no tags is a legitimate state — this one had none until
// somebody tagged a branch — and an empty result is not a failure. A checkout
// that never fetched the tags reports the same empty list for a different
// reason, and reading that as "nothing collides" is the shape of skip this
// repository refuses, so the two are separated before the list is read.
func releaseTags(t *testing.T, root string) []string {
	t.Helper()

	git := testkit.GitBinary(t)
	requireWholeHistory(t, root)

	cmd := exec.Command(git, "tag", "--list", "v*")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing tags with %s: %v", git, err)
	}

	tags := strings.Fields(string(out))
	slices.Sort(tags)
	return tags
}

// requireWholeHistory fails a checkout that cannot answer questions about tags.
//
// A shallow clone has no tags and a truncated history, and both of this file's
// questions read as "nothing is wrong" against it: no tag collides with the
// next release, and no tag is unreachable from a mainline the clone does not
// have either. `actions/checkout` is shallow by default, so the quiet pass is
// the one CI would have produced — which is the failure this file exists to
// stop, arriving through the tier that checks for it.
func requireWholeHistory(t *testing.T, root string) {
	t.Helper()

	git := testkit.GitBinary(t)
	cmd := exec.Command(git, "rev-parse", "--is-shallow-repository")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("asking whether the checkout is shallow: %v", err)
	}
	if strings.TrimSpace(string(out)) == "true" {
		t.Fatalf("this checkout is shallow, so it carries no tags and a truncated history;\n" +
			"\tboth questions in this file read as \"nothing is wrong\" against it, which is the " +
			"answer it exists to refuse\n\tCI needs fetch-depth: 0 on the job that runs this tier")
	}
}

// initialVersion is release-please-config.json's `initial-version` for the root
// package, or the empty string when it names none.
func initialVersion(t *testing.T, root string) string {
	t.Helper()

	var config struct {
		Packages map[string]struct {
			InitialVersion string `json:"initial-version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(releaseFile(t, root, "release-please-config.json")), &config); err != nil {
		t.Fatalf("reading release-please-config.json: %v", err)
	}
	return config.Packages["."].InitialVersion
}

// TestEveryTagOutsideTheMainlineIsRetracted requires a `retract` for every
// published version no commit on the mainline can reach.
//
// A tag is a promise the module proxy keeps forever: it caches the version
// immutably, and `@latest` goes on answering with it after the branch it was
// cut from is deleted. So a tag on a branch that never merged is not a mistake
// anybody can take back — v0.1.0, v0.1.1 and v0.1.2 were cut from
// `feat/dogfood-deep`, and `go get -u` still resolves to v0.1.2 today.
//
// What can be said is why. A `retract` in go.mod is the one statement the
// toolchain reads back to a person: `go get` stops choosing the version, `go
// list -m -versions` stops listing it, and the rationale comment is printed
// where somebody who already has it will see it. Refusing the tag is not
// available; refusing to leave it unexplained is.
//
// The mainline is `main` where the checkout has it and `origin/main` otherwise,
// because a worktree on a feature branch is the ordinary case here and neither
// spelling is more true than the other.
func TestEveryTagOutsideTheMainlineIsRetracted(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	tags := releaseTags(t, root)
	if len(tags) == 0 {
		return
	}

	mainline := mainlineRevision(t, root)
	retracted := retractedVersions(t, root)

	for _, tag := range tags {
		if reachableFrom(t, root, tag, mainline) {
			continue
		}
		if slices.Contains(retracted, tag) {
			continue
		}
		t.Errorf("%s is tagged, no commit on %s can reach it, and go.mod does not retract it;\n"+
			"\tthe proxy answers for that version forever, so the only thing left to decide is "+
			"whether a person who fetches it is told why", tag, mainline)
	}
}

// mainlineRevision is the revision this repository releases from, as `main`
// where the checkout has it and `origin/main` otherwise.
//
// A checkout with neither is a failure rather than a reason to pass: the
// question this file asks is about reachability, and a repository that cannot
// name its mainline cannot answer it. Reporting that as "nothing is
// unreachable" is the shape of skip this repository refuses.
func mainlineRevision(t *testing.T, root string) string {
	t.Helper()

	git := testkit.GitBinary(t)
	for _, revision := range []string{"main", "origin/main"} {
		cmd := exec.Command(git, "rev-parse", "--verify", "--quiet", revision+"^{commit}")
		cmd.Dir = root
		if err := cmd.Run(); err == nil {
			return revision
		}
	}
	t.Fatalf("neither main nor origin/main is in this checkout;\n" +
		"\treachability is what this file checks, so a checkout without the mainline cannot " +
		"be read as a checkout with nothing unreachable")
	return ""
}

// reachableFrom reports whether revision is an ancestor of mainline.
func reachableFrom(t *testing.T, root, revision, mainline string) bool {
	t.Helper()

	git := testkit.GitBinary(t)
	cmd := exec.Command(git, "merge-base", "--is-ancestor", revision, mainline)
	cmd.Dir = root
	switch err := cmd.Run(); {
	case err == nil:
		return true
	case isExitCode(err, 1):
		return false
	default:
		t.Fatalf("asking whether %s is an ancestor of %s: %v", revision, mainline, err)
		return false
	}
}

// isExitCode reports whether err is a child that exited with the given status.
// `git merge-base --is-ancestor` says no with 1 and fails with anything else,
// so the two have to be told apart rather than both read as no.
func isExitCode(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}

// retractedVersions is every version go.mod retracts, spelled as tags.
func retractedVersions(t *testing.T, root string) []string {
	t.Helper()

	path := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	parsed, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		t.Fatalf("parsing go.mod: %v", err)
	}

	var versions []string
	for _, retract := range parsed.Retract {
		// A retract can name an interval. Only the single-version spelling is
		// used here, and an interval is reported as the two versions bounding
		// it rather than expanded: nothing in this repository writes one, and
		// inventing the versions between would be this test deciding what the
		// interval meant.
		versions = append(versions, retract.Low)
		if retract.High != retract.Low {
			versions = append(versions, retract.High)
		}
	}
	slices.Sort(versions)
	return versions
}

// releaseFile reads one file of the repository, as text.
func releaseFile(t *testing.T, root, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}
