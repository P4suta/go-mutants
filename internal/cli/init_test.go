// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// TestStarterConfigurationIsTheDefaults is the property the generated file
// exists for: adopting it changes nothing.
//
// It is also what stops the text and the code drifting apart. Every value in
// the file is interpolated from [config.Defaults], so a changed default cannot
// leave a stale number behind — and a key that stopped round-tripping, or a
// default that stopped being writable at all, fails here rather than six months
// later in somebody's repository.
func TestStarterConfigurationIsTheDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), config.FileName)
	file, err := config.Parse(path, []byte(StarterConfig()))
	if err != nil {
		t.Fatalf("the file `init` writes does not parse: %v", err)
	}
	resolved := config.Merge(config.Defaults(), file, config.Overlay{})
	if err = resolved.Validate(); err != nil {
		t.Fatalf("the file `init` writes does not validate: %v", err)
	}
	if diff := cmp.Diff(config.Defaults(), resolved); diff != "" {
		t.Errorf("the generated configuration is not the defaults (-want +got):\n%s", diff)
	}
}

// TestStarterConfigurationWritesEveryKeyTheSchemaDefines is the drift gate on
// the generated file's *coverage*, which the round trip above cannot see: a
// configuration missing a key still parses, still validates, and still resolves
// to the defaults, because the default is what a missing key means.
//
// The generated file is what a project adopts as its record of what can be
// configured, so a setting that never appears in it — set or shown as a
// commented example — is a setting most users will never learn exists. Adding
// one to internal/config and forgetting the line here has to fail something,
// and this is that something.
func TestStarterConfigurationWritesEveryKeyTheSchemaDefines(t *testing.T) {
	t.Parallel()

	keys := config.SchemaKeys()
	if len(keys) == 0 {
		t.Fatal("the configuration schema defines no keys at all")
	}
	written := starterKeys(t, StarterConfig())
	for _, key := range keys {
		if isTableKey(key, keys) {
			// A table is written as the `[section]` its settings live under, and
			// the settings are what the loop below is about.
			if _, found := written[key]; !found {
				t.Errorf("the schema defines the table %s, and the file `init` writes has no such section", key)
			}
			continue
		}
		section, name := splitLastKey(key)
		if !written[section][name] {
			t.Errorf("%s is a setting go-mutants reads and the file `init` writes never mentions it; "+
				"write it under [%s] as `%s = ` or `# %s = `", key, section, name, name)
		}
	}
}

// isTableKey reports whether a key is a table rather than a setting, which is
// true exactly when the schema defines something inside it.
func isTableKey(key string, keys []string) bool {
	for _, other := range keys {
		if strings.HasPrefix(other, key+".") {
			return true
		}
	}
	return false
}

// splitLastKey splits a dotted key into the table it belongs to and its own
// name. A key with no dot belongs to the file's top level, which is spelled
// here as the empty section.
func splitLastKey(key string) (section, name string) {
	i := strings.LastIndex(key, ".")
	if i < 0 {
		return "", key
	}
	return key[:i], key[i+1:]
}

// starterKeys reads the generated file the way a user reads it: which settings
// are written under which table, counting the commented-out ones, since a key
// shown as an example is documented rather than forgotten.
//
// The top level is the empty section, and a `[[table]]` header names the same
// table a dotted key does, so the map is keyed exactly as [config.SchemaKeys]
// spells things.
func starterKeys(t *testing.T, content string) map[string]map[string]bool {
	t.Helper()
	written := map[string]map[string]bool{"": {}}
	section := ""
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
		switch {
		case strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]"):
			section = strings.TrimSuffix(strings.TrimPrefix(line, "[["), "]]")
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
		default:
			name, _, found := strings.Cut(line, "=")
			name = strings.TrimSpace(name)
			if !found || !isBareKey(name) {
				// Prose, or a blank line. Only a bare key on the left of an
				// assignment is a setting; anything else is a comment that
				// happens to contain the character.
				continue
			}
			if written[section] == nil {
				written[section] = map[string]bool{}
			}
			written[section][name] = true
			continue
		}
		if written[section] == nil {
			written[section] = map[string]bool{}
		}
	}
	return written
}

// isBareKey reports whether s is a TOML bare key, which is what every setting
// in the generated file is written as.
func isBareKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// TestStarterConfigurationDoesNotDependOnTheMachine. `init --check` is a
// freshness gate a CI job runs, so a file carrying this machine's core count or
// this run's clock would fail on the wrong hardware and be unfixable there. The
// two settings whose defaults are not constants are commented out, with the
// rule stated in prose instead.
func TestStarterConfigurationDoesNotDependOnTheMachine(t *testing.T) {
	t.Parallel()

	text := StarterConfig()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "jobs") || strings.HasPrefix(line, "timeout") {
			t.Errorf("a derived default is written out as a value: %q", line)
		}
	}
	for _, needed := range []string{"# jobs = ", "# timeout = ", "min(CPU count, 8)"} {
		if !strings.Contains(text, needed) {
			t.Errorf("the file does not explain %q", needed)
		}
	}
	// The generated file is one string; two calls a moment apart must produce
	// exactly the same bytes, or --check would be comparing against a moving
	// target.
	if StarterConfig() != text {
		t.Error("two calls produced different files")
	}
}

// TestInitWritesOnceAndNeverAgain is the whole of the overwrite contract: the
// first invocation writes, the second refuses with its own code, and the file
// on disk is untouched by the refusal.
func TestInitWritesOnceAndNeverAgain(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, config.FileName)

	code, stdout, stderr := execute(t, "init")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, path) {
		t.Errorf("stdout does not name the file it wrote: %q", stdout)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the written file: %v", err)
	}
	if string(written) != StarterConfig() {
		t.Error("the file on disk is not what `init` generates")
	}

	// Edited by hand, exactly as a real one would be, so that a refusal that
	// wrote anyway would be visible.
	edited := string(written) + "\n# a decision somebody made\n"
	if err = os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatalf("editing the file: %v", err)
	}
	code, _, stderr = execute(t, "init")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d for a second init, want 2", code)
	}
	if !strings.Contains(stderr, "error "+string(CodeConfigurationExists)) {
		t.Errorf("stderr = %q, want the refusal's own code", stderr)
	}
	if !strings.Contains(stderr, "hint: ") {
		t.Errorf("stderr = %q, want a hint naming the way out", stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file after the refusal: %v", err)
	}
	if string(after) != edited {
		t.Error("the refusal changed the file it refused to overwrite")
	}
}

// TestInitHasNoForceFlag. The absence is the design — see [initLong] — so it is
// pinned rather than left to whoever next reads a feature request.
func TestInitHasNoForceFlag(t *testing.T) {
	t.Chdir(t.TempDir())

	code, _, stderr := execute(t, "init", "--force")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unknown flag: --force") {
		t.Errorf("stderr = %q, want an unknown flag", stderr)
	}
}

// TestInitDryRunPrintsAndWritesNothing, including in the one place a real init
// would refuse: seeing what would be written is the whole point, and a file
// already being there is a common reason to ask.
func TestInitDryRunPrintsAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	code, stdout, stderr := execute(t, "init", "--dry-run")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if stdout != StarterConfig() {
		t.Error("--dry-run printed something other than the file it would write")
	}
	if _, err := os.Stat(filepath.Join(dir, config.FileName)); err == nil {
		t.Fatal("--dry-run wrote the file")
	}

	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("version = 1\n"), 0o600); err != nil {
		t.Fatalf("writing a configuration: %v", err)
	}
	if code, stdout, _ = execute(t, "init", "--dry-run"); code != int(mutation.ExitOK) {
		t.Errorf("--dry-run over an existing file exited %d, want 0", code)
	}
	if stdout != StarterConfig() {
		t.Error("--dry-run over an existing file printed something else")
	}
}

// TestInitCheckIsAFreshnessGate walks the three answers it can give, including
// the one exit status in this package that is not 0 or 2.
func TestInitCheckIsAFreshnessGate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, config.FileName)

	code, _, stderr := execute(t, "init", "--check")
	if code != int(mutation.ExitPolicyFailure) {
		t.Errorf("exit = %d for a missing file, want 1", code)
	}
	if !strings.Contains(stderr, "error "+string(CodeConfigurationStale)) {
		t.Errorf("stderr = %q, want the stale code", stderr)
	}

	if err := os.WriteFile(path, []byte(StarterConfig()), 0o600); err != nil {
		t.Fatalf("writing the generated file: %v", err)
	}
	code, stdout, stderr := execute(t, "init", "--check")
	if code != int(mutation.ExitOK) {
		t.Errorf("exit = %d for the generated file, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, path) {
		t.Errorf("stdout = %q, want the path it compared", stdout)
	}

	// One byte different is a different file. The comparison is deliberately
	// not "does it resolve to the same configuration": a reworded comment is
	// exactly the drift a freshness check is for.
	if err := os.WriteFile(path, []byte(StarterConfig()+"\n"), 0o600); err != nil {
		t.Fatalf("editing the file: %v", err)
	}
	if code, _, _ = execute(t, "init", "--check"); code != int(mutation.ExitPolicyFailure) {
		t.Errorf("exit = %d for an edited file, want 1", code)
	}
}

// TestInitCheckAndDryRunAreExclusive. Neither flag is wrong on its own, so the
// refusal is the combination rather than either value.
func TestInitCheckAndDryRunAreExclusive(t *testing.T) {
	t.Chdir(t.TempDir())

	code, _, stderr := execute(t, "init", "--check", "--dry-run")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "if any flags in the group") {
		t.Errorf("stderr = %q, want cobra's exclusivity refusal", stderr)
	}
}
