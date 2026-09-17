// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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
		if !slices.Contains(tags, "v"+released) {
			t.Errorf(".release-please-manifest.json records %q, and no tag v%s exists;\n"+
				"\tthe manifest is release-please's record of what it published, so a version "+
				"nothing tagged is a record of a release nobody can fetch\n\ttags: %v",
				released, released, tags)
		}
		return
	}

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

func isExitCode(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}

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
		versions = append(versions, retract.Low)
		if retract.High != retract.Low {
			versions = append(versions, retract.High)
		}
	}
	slices.Sort(versions)
	return versions
}

func releaseFile(t *testing.T, root, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}
